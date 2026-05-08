package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	ab "github.com/everfaz/autobuild-sdk"
)

// handleConfirm delivers a human approval/rejection to a paused agent loop.
//
// POST /api/confirm
//
//	{
//	  "chat_id":       123,
//	  "id":            "apr_abc123",   // ApprovalRequest.ID
//	  "approved":      true,
//	  "modified_args": "{...}"         // optional JSON — override tool args
//	}
//
// Response:
//
//	200 {"ok": true}
//	404 {"error": "no pending approval for chat 123"}
//	410 {"error": "approval apr_abc123 not found (already resolved or expired)"}
func (a *BackendChatApp) handleConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ChatID       int64  `json:"chat_id"`
		ID           string `json:"id"`
		Approved     bool   `json:"approved"`
		ModifiedArgs string `json:"modified_args"` // optional JSON override for tool args
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.ChatID == 0 || req.ID == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("chat_id and id are required"))
		return
	}

	gate, ok := hilRegistry.Get(req.ChatID)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no pending approval for chat %d", req.ChatID))
		return
	}

	delivered := gate.Respond(ab.ApprovalResponse{
		ID:           req.ID,
		Approved:     req.Approved,
		ModifiedArgs: req.ModifiedArgs,
	})
	if !delivered {
		writeErr(w, http.StatusGone, fmt.Errorf("approval %s not found (already resolved or expired)", req.ID))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
