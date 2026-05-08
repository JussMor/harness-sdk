package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	ab "github.com/everfaz/autobuild-sdk"
)

// ── Webhook + Interrupt handlers ─────────────────────────────────────────────
//
// Routes:
//
//   POST   /api/webhooks                  Create an outbound webhook subscription
//   GET    /api/webhooks                  List subscriptions (no secrets in response)
//   DELETE /api/webhooks/:id              Remove a subscription
//   POST   /api/interrupts/:token/resolve Resolve a paused interrupt via signed token
//
// The subscription store is process-local (in-memory). A production deployment
// should swap NewInMemoryWebhookStore for a Postgres-backed implementation.
//
// Note on lifecycle: webhookManager is initialized lazily. Call
// app.ensureWebhookManager() before any handler that needs it. The manager
// owns the dispatcher; close it at process shutdown if you care about
// draining in-flight deliveries.

type webhookManager struct {
	store      ab.WebhookStore
	dispatcher *ab.WebhookDispatcher
}

var (
	webhookOnce sync.Once
	webhooks    *webhookManager
)

func ensureWebhookManager() *webhookManager {
	webhookOnce.Do(func() {
		store := ab.NewInMemoryWebhookStore()
		webhooks = &webhookManager{
			store:      store,
			dispatcher: ab.NewWebhookDispatcher(store, nil),
		}
	})
	return webhooks
}

// handleWebhooks dispatches between create (POST) and list (GET).
func (a *BackendChatApp) handleWebhooks(w http.ResponseWriter, r *http.Request) {
	mgr := ensureWebhookManager()
	switch r.Method {
	case http.MethodPost:
		var sub ab.WebhookSubscription
		if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if sub.URL == "" {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("url is required"))
			return
		}
		if sub.ID == "" {
			sub.ID = "wh_" + randomToken(8)
		}
		if sub.Secret == "" {
			sub.Secret = randomToken(32)
		}
		sub.Active = true
		if err := mgr.store.Put(r.Context(), sub); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		// Return the secret ONCE on creation so the caller can verify signatures.
		writeJSON(w, http.StatusCreated, sub)

	case http.MethodGet:
		subs, err := mgr.store.List(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		safe := make([]ab.WebhookSubscription, 0, len(subs))
		for _, s := range subs {
			s.Secret = "" // never echo secrets
			safe = append(safe, s)
		}
		writeJSON(w, http.StatusOK, safe)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleWebhookByID handles DELETE /api/webhooks/:id.
func (a *BackendChatApp) handleWebhookByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	mgr := ensureWebhookManager()
	id := strings.TrimPrefix(r.URL.Path, "/api/webhooks/")
	if id == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("subscription id required"))
		return
	}
	if err := mgr.store.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleInterruptResolve resolves a paused interrupt via a signed token.
//
// POST /api/interrupts/:token/resolve
//
//	{
//	  "chat_id":       123,                // which session's gate to resolve against
//	  "approved":      true,
//	  "answer":        {...},              // optional JSON payload (Question/FormInput)
//	  "modified_args": "{...}"             // optional override (Approval kind)
//	}
//
// The token is opaque — it was previously returned by gate.IssueResolutionToken.
// Use this endpoint when the SSE stream is closed (Slack approval link, mobile
// push, email, etc.) but the agent loop is still paused waiting for input.
func (a *BackendChatApp) handleInterruptResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Path: /api/interrupts/:token/resolve
	rest := strings.TrimPrefix(r.URL.Path, "/api/interrupts/")
	rest = strings.TrimSuffix(rest, "/resolve")
	token := strings.TrimSpace(rest)
	if token == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("token required in path"))
		return
	}

	var body struct {
		ChatID       int64           `json:"chat_id"`
		Approved     bool            `json:"approved"`
		Answer       json.RawMessage `json:"answer"`
		ModifiedArgs string          `json:"modified_args"`
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	if body.ChatID == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("chat_id required"))
		return
	}

	gate, ok := ensureInterruptRegistry().Get(body.ChatID)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no pending interrupts for chat %d", body.ChatID))
		return
	}

	resp := ab.InterruptResponse{
		Approved:     body.Approved,
		Answer:       body.Answer,
		ModifiedArgs: body.ModifiedArgs,
	}

	// The path segment may be a plain interrupt ID (from the live SSE flow
	// where the frontend passes InterruptRequest.ID) or a signed resolution
	// token (from out-of-band webhooks/email links). Try the plain ID first
	// — it's the common case for interactive frontends.
	resp.ID = token
	if gate.Respond(resp) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	// Not a pending plain ID — try as a signed resolution token.
	if err := gate.ResolveByToken(token, resp); err != nil {
		status := http.StatusGone
		if strings.Contains(err.Error(), "signature") || strings.Contains(err.Error(), "expired") || strings.Contains(err.Error(), "malformed") {
			status = http.StatusUnauthorized
		}
		writeErr(w, status, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// randomToken returns a hex string of n bytes of randomness.
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand only fails on broken systems; fall back to a constant
		// sentinel to keep the request alive (caller will treat it as an id).
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(b)
}
