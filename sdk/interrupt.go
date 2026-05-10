package autobuild

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ── Interrupts ────────────────────────────────────────────────────────────────
//
// An Interrupt pauses the AgentLoop while the agent waits for a human to
// respond. There are three flavors today:
//
//   InterruptKindApproval  — gate a tool call (the legacy "confirmation"
//                            flow). Payload identifies the ToolCallEntry.
//
//   InterruptKindQuestion  — the agent asks a free-form or multiple-choice
//                            question. Used during alignment/planning.
//
//   InterruptKindFormInput — the agent requests structured data described
//                            by a JSON Schema; the frontend may render a
//                            generative-UI component instead of a plain form.
//
// All three share the same rendezvous primitive (InterruptGate) and the same
// stream events on the wire. Discriminate by InterruptRequest.Kind.
//
// Architecture (same as the legacy ApprovalGate):
//
//   AgentLoop  → SafetyFilter / ctx.RequestInput → gate.Wait(req)  ─┐
//                                                                   │ blocks
//   HTTP/WebSocket handler → gate.Respond(resp) ─────────────────────┘
//
// For inbound resolution from systems that don't keep the SSE open
// (mobile push, Slack approval link, email), use IssueResolutionToken
// to mint an HMAC-signed token and ResolveByToken to redeem it.

// InterruptKind discriminates between the payload variants of an interrupt.
type InterruptKind string

const (
	InterruptKindApproval  InterruptKind = "approval"
	InterruptKindQuestion  InterruptKind = "question"
	InterruptKindFormInput InterruptKind = "form_input"
)

// InterruptRequest is emitted when the agent needs human input.
// Exactly one of Approval / Question / Form is populated, matching Kind.
type InterruptRequest struct {
	ID        string        `json:"id"`
	Kind      InterruptKind `json:"kind"`
	Reason    string        `json:"reason,omitempty"`
	CreatedAt time.Time     `json:"created_at"`

	Approval *ApprovalPayload `json:"approval,omitempty"`
	Question *QuestionPayload `json:"question,omitempty"`
	Form     *FormPayload     `json:"form,omitempty"`
}

// ApprovalPayload describes a tool call awaiting approval.
type ApprovalPayload struct {
	ToolCall ToolCallEntry `json:"tool_call"`
}

// QuestionPayload describes a free-form or multiple-choice question
// the agent wants the user to answer before continuing.
type QuestionPayload struct {
	Prompt  string   `json:"prompt"`
	Choices []string `json:"choices,omitempty"`
	Multi   bool     `json:"multi,omitempty"` // true = the user may select multiple choices
}

// FormPayload describes structured input requested by the agent.
//
// Schema is a JSON Schema document describing the expected response shape.
// UIHint is an optional component name from the frontend's catalog
// (e.g. "PatientIntakeForm"); when present, the client may render that
// custom component instead of an auto-generated form.
type FormPayload struct {
	Title  string          `json:"title,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
	UIHint string          `json:"ui_hint,omitempty"`
}

// InterruptResponse is the human's verdict on an InterruptRequest.
//
// Approved is meaningful for InterruptKindApproval (and as a coarse
// allow/deny signal for any kind). Answer carries the structured
// payload for Question/FormInput and is interpreted per Kind.
// ModifiedArgs optionally overrides ToolCall.Arguments when the
// reviewer edited an Approval before allowing it.
type InterruptResponse struct {
	ID           string          `json:"id"`
	Approved     bool            `json:"approved"`
	Answer       json.RawMessage `json:"answer,omitempty"`
	ModifiedArgs string          `json:"modified_args,omitempty"`
}

// ── InterruptGate ─────────────────────────────────────────────────────────────

// InterruptGate is the rendezvous point between the agent loop (which
// blocks on Wait) and the transport handler (which calls Respond once the
// human answers). One gate per session is the typical pattern.
type InterruptGate struct {
	mu          sync.Mutex
	pending     map[string]chan InterruptResponse
	requests    chan InterruptRequest
	tokenSecret []byte
	store       InterruptStore
}

// NewInterruptGate creates a gate with a buffered request channel.
// bufSize ≤ 0 defaults to 8.
func NewInterruptGate(bufSize int) *InterruptGate {
	if bufSize <= 0 {
		bufSize = 8
	}
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	return &InterruptGate{
		pending:     make(map[string]chan InterruptResponse),
		requests:    make(chan InterruptRequest, bufSize),
		tokenSecret: secret,
	}
}

// WithStore attaches a persistence backend for pending interrupts. When set,
// Wait persists the request before blocking and Respond removes it on
// resolution. Without a store, interrupts only live in memory.
func (g *InterruptGate) WithStore(s InterruptStore) *InterruptGate {
	g.store = s
	return g
}

// WithTokenSecret overrides the random per-process HMAC secret with a
// caller-supplied one. Use this when running multiple replicas so that a
// token minted on instance A can be redeemed on instance B.
func (g *InterruptGate) WithTokenSecret(secret []byte) *InterruptGate {
	if len(secret) > 0 {
		g.tokenSecret = append([]byte(nil), secret...)
	}
	return g
}

// Requests returns the channel that emits InterruptRequests as they are
// raised. Stream the requests to the frontend (e.g. via SSE) so the user
// can respond.
func (g *InterruptGate) Requests() <-chan InterruptRequest {
	return g.requests
}

// Wait registers a pending interrupt and blocks until Respond is called or
// ctx is cancelled.
func (g *InterruptGate) Wait(ctx context.Context, req InterruptRequest) (InterruptResponse, error) {
	if req.ID == "" {
		req.ID = "int_" + randomHex(8)
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now()
	}

	ch := make(chan InterruptResponse, 1)
	g.mu.Lock()
	g.pending[req.ID] = ch
	g.mu.Unlock()

	if g.store != nil {
		_ = g.store.Put(ctx, req)
	}

	select {
	case g.requests <- req:
	case <-ctx.Done():
		g.mu.Lock()
		delete(g.pending, req.ID)
		g.mu.Unlock()
		if g.store != nil {
			_ = g.store.Delete(context.Background(), req.ID)
		}
		return InterruptResponse{}, fmt.Errorf("context cancelled before request sent: %w", ctx.Err())
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		g.mu.Lock()
		delete(g.pending, req.ID)
		g.mu.Unlock()
		if g.store != nil {
			_ = g.store.Delete(context.Background(), req.ID)
		}
		return InterruptResponse{}, fmt.Errorf("interrupt timed out: %w", ctx.Err())
	}
}

// Respond delivers a human decision to the waiting agent loop.
// Returns false if no request with that ID is pending.
func (g *InterruptGate) Respond(resp InterruptResponse) bool {
	g.mu.Lock()
	ch, ok := g.pending[resp.ID]
	if ok {
		delete(g.pending, resp.ID)
	}
	g.mu.Unlock()
	if !ok {
		return false
	}
	if g.store != nil {
		_ = g.store.Delete(context.Background(), resp.ID)
	}
	ch <- resp
	return true
}

// PendingCount returns how many interrupts are currently waiting.
func (g *InterruptGate) PendingCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.pending)
}

// ── Resolution tokens (inbound webhook resolution) ────────────────────────────

// resolutionToken is the on-the-wire payload signed with HMAC-SHA256.
// Layout: base64url( id || expiresUnix(8 BE bytes) ) || "." || base64url(sig).
type resolutionTokenClaims struct {
	id      string
	expires time.Time
}

// IssueResolutionToken returns a signed token allowing inbound resolution
// of the given interrupt id (e.g. via webhook). ttl bounds validity; pass
// 0 for the default of 1 hour.
//
// The token is opaque and short. Do NOT include user-controlled data in the
// id; treat tokens as bearer credentials.
func (g *InterruptGate) IssueResolutionToken(id string, ttl time.Duration) (string, error) {
	if id == "" {
		return "", errors.New("interrupt id required")
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	exp := time.Now().Add(ttl).Unix()
	body := make([]byte, 0, len(id)+8)
	body = append(body, []byte(id)...)
	expBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(expBytes, uint64(exp))
	body = append(body, expBytes...)

	mac := hmac.New(sha256.New, g.tokenSecret)
	mac.Write(body)
	sig := mac.Sum(nil)

	enc := base64.RawURLEncoding
	return enc.EncodeToString(body) + "." + enc.EncodeToString(sig), nil
}

// ResolveByToken delivers a response identified by a signed token previously
// returned by IssueResolutionToken.
func (g *InterruptGate) ResolveByToken(token string, resp InterruptResponse) error {
	claims, err := g.parseResolutionToken(token)
	if err != nil {
		return err
	}
	if time.Now().After(claims.expires) {
		return errors.New("interrupt resolution token expired")
	}
	resp.ID = claims.id
	if !g.Respond(resp) {
		return errors.New("interrupt not pending (already resolved or unknown)")
	}
	return nil
}

func (g *InterruptGate) parseResolutionToken(token string) (resolutionTokenClaims, error) {
	var out resolutionTokenClaims
	enc := base64.RawURLEncoding
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			body, err := enc.DecodeString(token[:i])
			if err != nil {
				return out, fmt.Errorf("invalid token body: %w", err)
			}
			sig, err := enc.DecodeString(token[i+1:])
			if err != nil {
				return out, fmt.Errorf("invalid token signature: %w", err)
			}
			if len(body) < 9 {
				return out, errors.New("token body too short")
			}
			mac := hmac.New(sha256.New, g.tokenSecret)
			mac.Write(body)
			if !hmac.Equal(mac.Sum(nil), sig) {
				return out, errors.New("token signature mismatch")
			}
			out.id = string(body[:len(body)-8])
			expUnix := int64(binary.BigEndian.Uint64(body[len(body)-8:]))
			out.expires = time.Unix(expUnix, 0)
			return out, nil
		}
	}
	return out, errors.New("malformed token (missing separator)")
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// newInterruptID returns a reasonably unique id with a kind-specific prefix.
// Use for callers that need to mint ids before invoking gate.Wait.
func newInterruptID(kind InterruptKind) string {
	prefix := "int_"
	switch kind {
	case InterruptKindApproval:
		prefix = "apr_"
	case InterruptKindQuestion:
		prefix = "qst_"
	case InterruptKindFormInput:
		prefix = "frm_"
	}
	return prefix + randomHex(8)
}

// hexEncode is a re-export to keep callers from importing encoding/hex
// just for trivial conversions.
func hexEncode(b []byte) string { return hex.EncodeToString(b) }

// ── Runtime wiring ───────────────────────────────────────────────────────────

// WithInterrupts attaches an InterruptGate to the runtime. When set, the
// runtime fans out interrupt requests on the streaming channel so the
// front-end can render approval / question / form-input UIs.
func (r *Runtime) WithInterrupts(gate *InterruptGate) *Runtime {
	r.interruptGate = gate
	return r
}

// InterruptGate returns the gate currently attached to the runtime, or nil.
func (r *Runtime) InterruptGate() *InterruptGate { return r.interruptGate }
