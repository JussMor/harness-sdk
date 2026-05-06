package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	ab "github.com/everfaz/autobuild-sdk"
)

// ── Thread REST handlers ──────────────────────────────────────────────────────

// handleThreads handles:
//
//	POST /api/threads         → create thread
//	GET  /api/threads?user=X  → list threads by user
func (a *BackendChatApp) handleThreads(w http.ResponseWriter, r *http.Request) {
	if a.threads == nil {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("thread provider unavailable"))
		return
	}

	switch r.Method {
	case http.MethodPost:
		var req struct {
			UserID    string `json:"userId"`
			ProjectID string `json:"projectId"`
			ModeID    string `json:"modeId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}

		ctx := r.Context()
		var thread *ab.Thread
		var err error

		if multi, ok := a.threads.(ab.MultiUserThreadProvider); ok && req.UserID != "" {
			thread, err = multi.CreateForUser(ctx, req.UserID, req.ProjectID, req.ModeID)
		} else {
			thread, err = a.threads.Create(ctx, req.ProjectID, req.ModeID)
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, thread)

	case http.MethodGet:
		userID := r.URL.Query().Get("user")
		status, ok := parseThreadStatus(r.URL.Query().Get("status"))
		if !ok {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid status"))
			return
		}

		multi, ok := a.threads.(ab.MultiUserThreadProvider)
		if !ok || userID == "" {
			writeJSON(w, http.StatusOK, []any{})
			return
		}

		threads, err := multi.ListByUser(r.Context(), userID, status)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, threads)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleThreadRoutes handles:
//
//	GET /api/threads/:id → get thread
func (a *BackendChatApp) handleThreadRoutes(w http.ResponseWriter, r *http.Request) {
	if a.threads == nil {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("thread provider unavailable"))
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/threads/")
	threadID := strings.TrimRight(path, "/")
	if threadID == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		var thread *ab.Thread
		var err error
		if multi, ok := a.threads.(ab.MultiUserThreadProvider); ok {
			userID := strings.TrimSpace(r.URL.Query().Get("user"))
			if userID == "" {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("user is required"))
				return
			}
			thread, err = multi.GetForUser(r.Context(), userID, threadID)
			if errors.Is(err, ab.ErrThreadAccessDenied) {
				writeErr(w, http.StatusForbidden, err)
				return
			}
		} else {
			thread, err = a.threads.Get(r.Context(), threadID)
		}
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		if thread == nil {
			writeErr(w, http.StatusNotFound, fmt.Errorf("thread not found"))
			return
		}
		writeJSON(w, http.StatusOK, thread)

	case http.MethodDelete:
		if multi, ok := a.threads.(ab.MultiUserThreadProvider); ok {
			userID := strings.TrimSpace(r.URL.Query().Get("user"))
			if userID == "" {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("user is required"))
				return
			}
			if _, err := multi.GetForUser(r.Context(), userID, threadID); err != nil {
				if errors.Is(err, ab.ErrThreadAccessDenied) {
					writeErr(w, http.StatusForbidden, err)
					return
				}
				writeErr(w, http.StatusNotFound, err)
				return
			}
		}
		if err := a.threads.Archive(r.Context(), threadID); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"archived": true})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func parseThreadStatus(raw string) (ab.ThreadStatus, bool) {
	status := ab.ThreadStatus(strings.TrimSpace(raw))
	if status == "" {
		return ab.ThreadStatusActive, true
	}
	switch status {
	case ab.ThreadStatusActive, ab.ThreadStatusCompleted, ab.ThreadStatusFailed, ab.ThreadStatusArchived:
		return status, true
	default:
		return "", false
	}
}
