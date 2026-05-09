package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// registerArtifactRoutes wires all artifact endpoints into the mux.
//
// GET  /api/chats/:id/artifacts             → list artifacts for a chat
// POST /api/chats/:id/artifacts             → create artifact (frontend-detected)
// GET  /api/artifacts/:id                   → get artifact + all versions
// POST /api/artifacts/:id/versions          → add a new version
// GET  /api/artifacts/:id/storage           → get storage (personal or shared)
// POST /api/artifacts/:id/storage           → set a key in storage
// DELETE /api/artifacts/:id/storage/:key    → delete a key from storage
func (a *BackendChatApp) registerArtifactRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/artifacts/", a.handleArtifactRoutes)
}

func (a *BackendChatApp) handleArtifactRoutes(w http.ResponseWriter, r *http.Request) {
	// /api/artifacts/:id
	// /api/artifacts/:id/versions
	// /api/artifacts/:id/storage
	// /api/artifacts/:id/storage/:key
	path := strings.TrimPrefix(r.URL.Path, "/api/artifacts/")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) == 0 || parts[0] == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	artifactID := parts[0]
	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}
	subKey := ""
	if len(parts) > 2 {
		subKey = parts[2]
	}

	switch {
	case sub == "" && r.Method == http.MethodGet:
		a.handleGetArtifact(w, r, artifactID)

	case sub == "versions" && r.Method == http.MethodPost:
		a.handleAddArtifactVersion(w, r, artifactID)

	case sub == "storage" && subKey == "" && r.Method == http.MethodGet:
		a.handleGetArtifactStorage(w, r, artifactID)

	case sub == "storage" && subKey == "" && r.Method == http.MethodPost:
		a.handleSetArtifactStorage(w, r, artifactID)

	case sub == "storage" && subKey != "" && r.Method == http.MethodDelete:
		a.handleDeleteArtifactStorageKey(w, r, artifactID, subKey)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// handleChatArtifacts handles GET/POST /api/chats/:id/artifacts
func (a *BackendChatApp) handleChatArtifacts(w http.ResponseWriter, r *http.Request, chatID int64) {
	switch r.Method {
	case http.MethodGet:
		// Include the latest version's content so the frontend can restore
		// the artifact canvas without extra per-artifact round-trips.
		arts, err := ListArtifactsWithLatestContent(r.Context(), a.db, chatID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if arts == nil {
			arts = []ArtifactRecord{}
		}
		writeJSON(w, http.StatusOK, arts)

	case http.MethodPost:
		var req struct {
			Language  string `json:"language"`
			Title     string `json:"title"`
			Content   string `json:"content"`
			MessageID *int64 `json:"messageId,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if req.Language == "" {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("language is required"))
			return
		}
		art, ver, err := CreateArtifact(r.Context(), a.db, a.r2,
			chatID, req.MessageID, req.Language, req.Title, req.Content,
		)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		art.Versions = []ArtifactVersionRecord{*ver}
		writeJSON(w, http.StatusCreated, art)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleGetArtifact returns an artifact with all its versions.
func (a *BackendChatApp) handleGetArtifact(w http.ResponseWriter, r *http.Request, artifactID string) {
	art, err := GetArtifact(r.Context(), a.db, artifactID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if art == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, art)
}

// handleAddArtifactVersion adds a new version to an artifact.
// Called when the user edits the artifact locally and saves,
// or when the LLM regenerates the artifact.
func (a *BackendChatApp) handleAddArtifactVersion(w http.ResponseWriter, r *http.Request, artifactID string) {
	art, err := GetArtifact(r.Context(), a.db, artifactID)
	if err != nil || art == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("artifact not found"))
		return
	}

	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	nextVer, err := NextVersion(r.Context(), a.db, artifactID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	ver, err := AddArtifactVersion(r.Context(), a.db, a.r2,
		artifactID, nextVer, art.Language, req.Content,
	)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, ver)
}

// handleGetArtifactStorage returns all storage keys for an artifact.
// Query params: ?shared=true|false  ?userId=...
func (a *BackendChatApp) handleGetArtifactStorage(w http.ResponseWriter, r *http.Request, artifactID string) {
	shared := r.URL.Query().Get("shared") == "true"
	userID := r.URL.Query().Get("userId")

	data, err := GetArtifactStorage(r.Context(), a.db, artifactID, shared, userID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"artifactId": artifactID,
		"shared":     shared,
		"data":       data,
	})
}

// handleSetArtifactStorage sets a key in artifact storage.
// Body: { key, value, shared, userId? }
func (a *BackendChatApp) handleSetArtifactStorage(w http.ResponseWriter, r *http.Request, artifactID string) {
	var req struct {
		Key    string `json:"key"`
		Value  any    `json:"value"`
		Shared bool   `json:"shared"`
		UserID string `json:"userId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Key == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("key is required"))
		return
	}
	if err := SetArtifactStorage(r.Context(), a.db, artifactID, req.Key, req.Value, req.Shared, req.UserID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteArtifactStorageKey deletes a key from artifact storage.
// Query params: ?shared=true|false  ?userId=...
func (a *BackendChatApp) handleDeleteArtifactStorageKey(w http.ResponseWriter, r *http.Request, artifactID, key string) {
	shared := r.URL.Query().Get("shared") == "true"
	userID := r.URL.Query().Get("userId")

	if err := DeleteArtifactStorageKey(r.Context(), a.db, artifactID, key, shared, userID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
