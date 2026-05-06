package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ab "github.com/everfaz/autobuild-sdk"
	sdkthread "github.com/everfaz/autobuild-sdk/providers/thread"
)

func main() {
	loadBackendEnv()

	ctx := context.Background()
	dbPath := getenv("BACKEND_CHAT_DB", "example/backend-chat/chat.db")
	addr := getenv("BACKEND_CHAT_ADDR", ":9090")

	db, err := OpenSQLite(dbPath)
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	if err := EnsureSchema(ctx, db); err != nil {
		log.Fatalf("ensure schema: %v", err)
	}
	if err := EnsureConversationSchema(ctx, db); err != nil {
		log.Fatalf("ensure conversation schema: %v", err)
	}
	if err := EnsureArtifactSchema(ctx, db); err != nil {
		log.Fatalf("ensure artifact schema: %v", err)
	}

	r2 := NewR2Client()
	if r2.IsAvailable() {
		log.Printf("backend-chat R2 storage enabled (bucket: %s)", getenv("R2_BUCKET", "artifacts"))
	}

	app := &BackendChatApp{
		db:        db,
		r2:        r2,
		pub:       NewCentrifugoClient(getenv("CENTRIFUGO_API_URL", "http://localhost:8000/api"), getenv("CENTRIFUGO_API_KEY", "backend-chat-dev-api-key")),
		llm:       BuildLLMFromEnv(),
		modelName: getenv("BACKEND_MODEL", "anthropic/claude-sonnet-4-20250514"),
	}
	if modes, err := loadBackendModes(); err == nil {
		app.modes = modes
	} else {
		log.Printf("backend-chat modes unavailable: %v", err)
	}
	if multi, ok := app.llm.(*ab.RoutedLLMProvider); ok {
		app.multi = multi
	}

	// Thread provider (SQLite-backed, multi-user isolation)
	threadProvider, err := sdkthread.OpenSQLite(db)
	if err != nil {
		log.Printf("thread provider unavailable: %v", err)
	} else {
		app.threads = threadProvider
		log.Printf("backend-chat thread provider: SQLite")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", app.handleHealth)
	mux.HandleFunc("/api/modes", app.handleModes)
	mux.HandleFunc("/api/providers", app.handleProviders)
	mux.HandleFunc("/api/chats", app.handleChats)
	mux.HandleFunc("/api/chats/", app.handleChatRoutes)
	mux.HandleFunc("/admin/eval", app.handleEval)
	mux.HandleFunc("/api/threads", app.handleThreads)
	mux.HandleFunc("/api/threads/", app.handleThreadRoutes)
	app.registerArtifactRoutes(mux)

	log.Printf("backend-chat listening on %s", addr)
	if err := http.ListenAndServe(addr, withRequestLog(withCORS(mux))); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

type BackendChatApp struct {
	db        *sql.DB
	r2        *R2Client
	pub       *CentrifugoClient
	llm       ab.LLMProvider
	multi     *ab.RoutedLLMProvider
	modes     ab.ModeProvider
	threads   ab.ThreadProvider
	modelName string
}

type Chat struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Message struct {
	ID        int64            `json:"id"`
	ChatID    int64            `json:"chatId"`
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Model     string           `json:"model"`
	Metadata  *MessageMetadata `json:"metadata,omitempty"`
	CreatedAt time.Time        `json:"createdAt"`
}

// MessageMetadata stores tool calls and artifacts for persistence.
type MessageMetadata struct {
	ToolCalls []MetadataToolCall `json:"toolCalls,omitempty"`
	Artifacts []MetadataArtifact `json:"artifacts,omitempty"`
}

type MetadataToolCall struct {
	Name   string `json:"name"`
	Args   any    `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Error  bool   `json:"error,omitempty"`
}

type MetadataArtifact struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	Content  string `json:"content"`
}

func (a *BackendChatApp) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleEval runs the built-in regression eval suite.
// POST /admin/eval → {"pass": 4, "fail": 1, "results": [...]}
// GET  /admin/eval → same (runs without a request body)
func (a *BackendChatApp) handleEval(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	results := RunBackendEvals(ctx, a.llm, a.modelName)

	pass, fail := 0, 0
	for _, res := range results {
		if res.Pass {
			pass++
		} else {
			fail++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pass":    pass,
		"fail":    fail,
		"total":   len(results),
		"results": results,
	})
}

// isSandboxOutput returns true for tools that produce rich sandbox outputs.
func isSandboxOutput(toolName string) bool {
	switch toolName {
	case "bash", "code_interpreter", "file_write", "file_read":
		return true
	}
	return false
}

// parseSandboxOutput attempts to parse the tool result as a structured
// sandbox output event for the frontend canvas.
// Returns nil if the content is plain text (no special rendering needed).
func parseSandboxOutput(content string) map[string]any {
	// Look for markers the formatCodeExecution helper emits
	if strings.Contains(content, "[html_output available") ||
		strings.Contains(content, "[image output available") ||
		strings.Contains(content, "[svg output available") {
		return map[string]any{
			"has_rich_output": true,
			"text":            content,
		}
	}
	return nil
}

// inferLangFromPath returns the language for an artifact based on file extension.
func inferLangFromPath(path string) string {
	ext := filepath.Ext(path)
	if len(ext) > 0 {
		ext = ext[1:] // strip leading dot
	}
	switch strings.ToLower(ext) {
	case "html", "htm":
		return "html"
	case "md", "markdown":
		return "markdown"
	case "css":
		return "css"
	case "js":
		return "javascript"
	case "ts":
		return "typescript"
	case "jsx":
		return "jsx"
	case "tsx":
		return "tsx"
	case "py":
		return "python"
	case "json":
		return "json"
	case "yaml", "yml":
		return "yaml"
	case "go":
		return "go"
	case "svg":
		return "svg"
	case "sql":
		return "sql"
	case "sh", "bash":
		return "bash"
	default:
		return "text"
	}
}

func (a *BackendChatApp) handleProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if a.multi == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"default":   "echo",
			"providers": []map[string]any{{"name": "echo", "enabled": true}},
		})
		return
	}

	names := a.multi.Providers()
	providers := make([]map[string]any, 0, len(names))
	for _, name := range names {
		providers = append(providers, map[string]any{"name": name, "enabled": true})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"default":   a.multi.DefaultProvider(),
		"providers": providers,
	})
}

func (a *BackendChatApp) handleModes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if a.modes == nil {
		writeJSON(w, http.StatusOK, []map[string]any{})
		return
	}

	items, err := a.modes.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	resp := make([]map[string]any, 0, len(items))
	for _, mode := range items {
		resp = append(resp, map[string]any{
			"id":       mode.ID,
			"name":     mode.Name,
			"baseMode": mode.BaseModeID,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

func (a *BackendChatApp) handleChats(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := ListChats(r.Context(), a.db)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var req struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Title) == "" {
			req.Title = "New chat"
		}
		chat, err := CreateChat(r.Context(), a.db, req.Title)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, chat)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *BackendChatApp) handleChatRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/chats/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	chatID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid chat id"))
		return
	}

	switch parts[1] {
	case "messages":
		a.handleMessages(w, r, chatID)
	case "stream":
		a.handleStream(w, r, chatID)
	case "artifacts":
		a.handleChatArtifacts(w, r, chatID)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *BackendChatApp) handleMessages(w http.ResponseWriter, r *http.Request, chatID int64) {
	switch r.Method {
	case http.MethodGet:
		items, err := ListMessages(r.Context(), a.db, chatID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var req struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		msg, err := InsertMessage(r.Context(), a.db, chatID, req.Role, req.Content, a.modelName)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		_ = a.pub.PublishChatMessage(r.Context(), chatID, msg)
		writeJSON(w, http.StatusCreated, msg)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}



func newRunID() string {
	return fmt.Sprintf("run_%d", time.Now().UnixNano())
}

// handleStream is the real-time streaming endpoint for LLM generation.
// Uses Server-Sent Events to push token deltas as the LLM generates them.
//
// POST /api/chats/:id/stream
// Same request body as /run. Response is text/event-stream with events:
//   event: delta    → {"delta": "token text"}
//   event: tool     → {"name": "tool_name", "args": {...}}
//   event: done     → {"runId": "...", "messageId": 123}
//   event: error    → {"error": "message"}
func (a *BackendChatApp) handleStream(w http.ResponseWriter, r *http.Request, chatID int64) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming not supported"))
		return
	}

	var req struct {
		Prompt      string `json:"prompt"`
		Mode        string `json:"mode"`
		Provider    string `json:"provider"`
		Model       string `json:"model"`
		ClientRunID string `json:"clientRunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("prompt is required"))
		return
	}

	effectiveModel := strings.TrimSpace(req.Model)
	if effectiveModel == "" {
		effectiveModel = a.modelName
	}
	requestedProvider := strings.ToLower(strings.TrimSpace(req.Provider))
	if requestedProvider != "" {
		_, modelOnly := ab.ParseModelRef(effectiveModel)
		if modelOnly == "" {
			modelOnly = effectiveModel
		}
		effectiveModel = requestedProvider + "/" + modelOnly
	}

	// SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ctx := r.Context()
	tracer := ab.NewTracer()
	ctx = ab.WithTracer(ctx, tracer)
	if isSandboxAvailable() {
		getSandboxManager().setDB(a.db)
	}

	sseWrite := func(event, data string) {
		if ctx.Err() != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			log.Printf("stream.write_failed chat_id=%d run_id=%s error=%v", chatID, strings.TrimSpace(req.ClientRunID), err)
			return
		}
		flusher.Flush()
	}

	// Insert user message
	userMsg, err := InsertMessage(ctx, a.db, chatID, "user", req.Prompt, effectiveModel)
	if err != nil {
		d, _ := json.Marshal(map[string]string{"error": err.Error()})
		sseWrite("error", string(d))
		return
	}
	_ = a.pub.PublishChatMessage(ctx, chatID, userMsg)

	history, err := ListMessages(ctx, a.db, chatID)
	if err != nil {
		d, _ := json.Marshal(map[string]string{"error": err.Error()})
		sseWrite("error", string(d))
		return
	}

	runID := strings.TrimSpace(req.ClientRunID)
	if runID == "" {
		runID = newRunID()
	}
	logContext := RuntimeLogContext{ChatID: chatID, RunID: runID, Mode: strings.TrimSpace(req.Mode)}
	log.Printf("stream.start chat_id=%d run_id=%s model=%s", chatID, runID, effectiveModel)

	_, agentRT, err := newModeEngineWithDB(a.llm, effectiveModel, logContext, a.db, a.threads)
	if err != nil {
		d, _ := json.Marshal(map[string]string{"error": err.Error()})
		sseWrite("error", string(d))
		return
	}

	conv, err := LoadOrCreateConversation(ctx, agentRT.convStore, chatID, history)
	if err != nil {
		d, _ := json.Marshal(map[string]string{"error": err.Error()})
		sseWrite("error", string(d))
		return
	}

	events, err := agentRT.runtime.RunStream(ctx, conv, req.Prompt)
	if err != nil {
		d, _ := json.Marshal(map[string]string{"error": err.Error()})
		sseWrite("error", string(d))
		return
	}

	var fullResponse strings.Builder
	var streamMeta MessageMetadata
	var lastToolCall string // track last tool_call name for pairing with result
	for {
		select {
		case <-ctx.Done():
			log.Printf("stream.canceled chat_id=%d run_id=%s", chatID, runID)
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
		switch ev.Type {
		case ab.StreamEventDelta:
			fullResponse.WriteString(ev.Delta)
			d, _ := json.Marshal(map[string]string{"delta": ev.Delta})
			sseWrite("delta", string(d))

		case ab.StreamEventThinking:
			if ev.Thinking != "" {
				d, _ := json.Marshal(map[string]string{"thinking": ev.Thinking})
				sseWrite("thinking", string(d))
			}

		case ab.StreamEventToolCall:
			if ev.ToolCall != nil {
				// Parse args JSON string into object so frontend receives structured data
				var parsedArgs any
				if json.Unmarshal([]byte(ev.ToolCall.Arguments), &parsedArgs) != nil {
					parsedArgs = ev.ToolCall.Arguments // fallback to raw string
				}
				d, _ := json.Marshal(map[string]any{
					"name": ev.ToolCall.Name,
					"args": parsedArgs,
				})
				sseWrite("tool_call", string(d))

				lastToolCall = ev.ToolCall.Name

				// Collect file_write as artifact
				if ev.ToolCall.Name == "file_write" {
					var fileArgs struct {
						Path    string `json:"path"`
						Content string `json:"content"`
					}
					if json.Unmarshal([]byte(ev.ToolCall.Arguments), &fileArgs) == nil && fileArgs.Content != "" {
						streamMeta.Artifacts = append(streamMeta.Artifacts, MetadataArtifact{
							Path:     fileArgs.Path,
							Language: inferLangFromPath(fileArgs.Path),
							Content:  fileArgs.Content,
						})
					}
				}

				// Track tool call in metadata
				streamMeta.ToolCalls = append(streamMeta.ToolCalls, MetadataToolCall{
					Name: ev.ToolCall.Name,
					Args: parsedArgs,
				})
			}

		case ab.StreamEventToolResult:
			if ev.ToolResult != nil {
				d, _ := json.Marshal(map[string]any{
					"name":    ev.ToolResult.Name,
					"content": ev.ToolResult.Content,
					"error":   ev.ToolResult.Error != nil,
				})
				sseWrite("tool_result", string(d))

				// Pair result with last matching tool call in metadata
				for i := len(streamMeta.ToolCalls) - 1; i >= 0; i-- {
					if streamMeta.ToolCalls[i].Name == ev.ToolResult.Name && streamMeta.ToolCalls[i].Result == "" {
						streamMeta.ToolCalls[i].Result = ev.ToolResult.Content
						streamMeta.ToolCalls[i].Error = ev.ToolResult.Error != nil
						break
					}
				}
				_ = lastToolCall // used above

				// For sandbox tools, emit structured sandbox_output event
				if isSandboxOutput(ev.ToolResult.Name) {
					if sbData := parseSandboxOutput(ev.ToolResult.Content); sbData != nil {
						sbJSON, _ := json.Marshal(sbData)
						sseWrite("sandbox_output", string(sbJSON))
					}
				}

				// Extract files created by subagents so they appear as artifacts
				if ev.ToolResult.Name == "dispatch-subagents" && ev.ToolResult.Error == nil {
					var dispatchResult struct {
						FilesCreated []struct {
							Path    string `json:"path"`
							Content string `json:"content"`
						} `json:"files_created"`
					}
					if json.Unmarshal([]byte(ev.ToolResult.Content), &dispatchResult) == nil {
						for _, f := range dispatchResult.FilesCreated {
							if f.Path != "" && f.Content != "" {
								streamMeta.Artifacts = append(streamMeta.Artifacts, MetadataArtifact{
									Path:     f.Path,
									Language: inferLangFromPath(f.Path),
									Content:  f.Content,
								})
							}
						}
					}
				}
			}

		case ab.StreamEventPlanProposed:
			if ev.Plan != nil {
				executables := make([]map[string]any, 0, len(ev.Plan.Executables))
				for _, exec := range ev.Plan.Executables {
					executables = append(executables, map[string]any{
						"id":           exec.ID,
						"name":         exec.Name,
						"description":  exec.Description,
						"dependencies": exec.Dependencies,
						"status":       string(exec.Status),
					})
				}
				d, _ := json.Marshal(map[string]any{
					"id":          ev.Plan.ID,
					"title":       ev.Plan.Title,
					"objective":   ev.Plan.Objective,
					"executables": executables,
				})
				sseWrite("plan_proposed", string(d))
				log.Printf("stream.plan_proposed chat_id=%d run_id=%s executables=%d",
					chatID, runID, len(ev.Plan.Executables))
			}

		case ab.StreamEventSubagentResult:
			if ev.SubagentResult != nil {
				errMsg := ""
				if ev.SubagentResult.Error != nil {
					errMsg = ev.SubagentResult.Error.Error()
				}
				d, _ := json.Marshal(map[string]any{
					"id":          ev.SubagentResult.ID,
					"task":        ev.SubagentResult.Task,
					"output":      ev.SubagentResult.Output,
					"turns":       ev.SubagentResult.Turns,
					"stop_reason": ev.SubagentResult.StopReason,
					"duration_ms": ev.SubagentResult.Duration.Milliseconds(),
					"error":       errMsg,
				})
				sseWrite("subagent_result", string(d))
				log.Printf("stream.subagent_result chat_id=%d run_id=%s agent_id=%s",
					chatID, runID, ev.SubagentResult.ID)
			}

		case ab.StreamEventDone:
			// Persist assistant message with metadata
			var metaOpt []MessageMetadata
			if len(streamMeta.ToolCalls) > 0 || len(streamMeta.Artifacts) > 0 {
				metaOpt = []MessageMetadata{streamMeta}
			}
			assistantMsg, _ := InsertMessage(ctx, a.db, chatID, "assistant", fullResponse.String(), effectiveModel, metaOpt...)
			_ = a.pub.PublishChatMessage(ctx, chatID, assistantMsg)

			// Auto-persist artifacts detected from stream (file_write tool calls)
			// and emit artifact SSE events so the frontend canvas can show them.
			for _, metaArt := range streamMeta.Artifacts {
				art, ver, err := CreateArtifact(ctx, a.db, a.r2,
					chatID, &assistantMsg.ID,
					metaArt.Language, metaArt.Path, metaArt.Content,
				)
				if err == nil {
					d, _ := json.Marshal(map[string]any{
						"id":       art.ID,
						"language": art.Language,
						"title":    art.Title,
						"version":  ver.Version,
						"content":  ver.Content,
						"r2Url":    ver.R2URL,
					})
					sseWrite("artifact", string(d))
				}
			}

			d, _ := json.Marshal(map[string]any{"runId": runID, "messageId": assistantMsg.ID})
			sseWrite("done", string(d))
			log.Printf("stream.done chat_id=%d run_id=%s chars=%d artifacts=%d",
				chatID, runID, fullResponse.Len(), len(streamMeta.Artifacts))

		case ab.StreamEventError:
			msg := "unknown error"
			if ev.Error != nil {
				msg = ev.Error.Error()
			}
			d, _ := json.Marshal(map[string]string{"error": msg})
			sseWrite("error", string(d))
			log.Printf("stream.error chat_id=%d run_id=%s error=%s", chatID, runID, msg)
		}
		}
	}
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s -> %d (%s) from=%s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond), r.RemoteAddr)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	log.Printf("request failed status=%d error=%v", status, err)
	message := "request failed"
	if err != nil {
		message = err.Error()
	}
	writeJSON(w, status, map[string]any{"error": message})
}

func getenv(key, fallback string) string {
	v := os.Getenv(key)
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func loadBackendEnv() {
	paths := []string{
		".env",
		"example/backend-chat/.env",
		filepath.Join("..", "backend-chat", ".env"),
	}
	for _, p := range paths {
		_ = loadEnvFile(p)
	}
}

func loadEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}

	return nil
}
