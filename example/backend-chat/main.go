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
)

func main() {
	loadBackendEnv()

	ctx := context.Background()
	dbPath := getenv("BACKEND_CHAT_DB", "example/backend-chat/chat.db")
	addr := getenv("BACKEND_CHAT_ADDR", ":8080")

	db, err := OpenSQLite(dbPath)
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	if err := EnsureSchema(ctx, db); err != nil {
		log.Fatalf("ensure schema: %v", err)
	}

	app := &BackendChatApp{
		db:        db,
		pub:       NewCentrifugoClient(getenv("CENTRIFUGO_API_URL", "http://localhost:8000/api"), getenv("CENTRIFUGO_API_KEY", "backend-chat-dev-api-key")),
		llm:       BuildLLMFromEnv(),
		runnerHub: NewRunnerEventHub(),
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

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", app.handleHealth)
	mux.HandleFunc("/api/modes", app.handleModes)
	mux.HandleFunc("/api/providers", app.handleProviders)
	mux.HandleFunc("/api/chats", app.handleChats)
	mux.HandleFunc("/api/chats/", app.handleChatRoutes)

	log.Printf("backend-chat listening on %s", addr)
	if err := http.ListenAndServe(addr, withRequestLog(withCORS(mux))); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

type BackendChatApp struct {
	db        *sql.DB
	pub       *CentrifugoClient
	llm       ab.LLMProvider
	multi     *ab.RoutedLLMProvider
	modes     ab.ModeProvider
	runnerHub *RunnerEventHub
	modelName string
}

type Chat struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Message struct {
	ID        int64     `json:"id"`
	ChatID    int64     `json:"chatId"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"createdAt"`
}

func (a *BackendChatApp) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
	case "events":
		a.handleRunnerEvents(w, r, chatID)
	case "run":
		a.handleRun(w, r, chatID)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *BackendChatApp) handleRunnerEvents(w http.ResponseWriter, r *http.Request, chatID int64) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming not supported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	events, unsubscribe := a.runnerHub.Subscribe(chatID)
	defer unsubscribe()

	_, _ = fmt.Fprint(w, "event: ready\ndata: {}\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			payload, err := json.Marshal(event)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, payload)
			flusher.Flush()
		}
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

func (a *BackendChatApp) handleRun(w http.ResponseWriter, r *http.Request, chatID int64) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
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

	effectiveModel := strings.TrimSpace(req.Model)
	if effectiveModel == "" {
		effectiveModel = a.modelName
	}

	requestedProvider := strings.ToLower(strings.TrimSpace(req.Provider))
	if requestedProvider != "" {
		if a.multi != nil && !a.multi.HasProvider(requestedProvider) {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("provider %q not configured", requestedProvider))
			return
		}
		_, modelOnly := ab.ParseModelRef(effectiveModel)
		if modelOnly == "" {
			modelOnly = effectiveModel
		}
		effectiveModel = requestedProvider + "/" + modelOnly
	}

	// Detach from the HTTP request context so LLM and DB operations finish
	// even if the browser closes the connection early (context canceled).
	ctx := context.WithoutCancel(r.Context())

	userMsg, err := InsertMessage(ctx, a.db, chatID, "user", req.Prompt, effectiveModel)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = a.pub.PublishChatMessage(ctx, chatID, userMsg)

	history, err := ListMessages(ctx, a.db, chatID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	runID := strings.TrimSpace(req.ClientRunID)
	if runID == "" {
		runID = newRunID()
	}
	logContext := RuntimeLogContext{ChatID: chatID, RunID: runID, Mode: strings.TrimSpace(req.Mode)}
	log.Printf("run.start chat_id=%d run_id=%s mode=%s model=%s history_messages=%d prompt=%q", chatID, runID, firstNonEmpty(logContext.Mode, "balanced"), effectiveModel, len(history), previewText(req.Prompt, 160))

	publishRunner := func(summary RunnerSummary) {
		a.runnerHub.Publish(RunnerEvent{
			Type:      "runner.update",
			RunID:     runID,
			ChatID:    chatID,
			Runner:    &summary,
			Timestamp: time.Now().UTC(),
		})
	}

	publishTrace := func(step TraceStep) {
		a.runnerHub.Publish(RunnerEvent{
			Type:      "trace.update",
			RunID:     runID,
			ChatID:    chatID,
			Trace:     &step,
			Timestamp: time.Now().UTC(),
		})
	}

	emitRunnerEvent := func(event ab.Event) {
		threadID := strings.TrimSpace(event.Source)
		summary := RunnerSummary{Model: effectiveModel, Status: "running"}

		if payloadThreadID := payloadString(event.Payload, "thread_id"); payloadThreadID != "" {
			threadID = payloadThreadID
		}
		if threadID != "" {
			summary.ID = threadID
		}
		if task := payloadString(event.Payload, "task"); task != "" {
			summary.Task = task
		}
		if tier := payloadString(event.Payload, "tier"); tier != "" {
			summary.Tier = tier
		}
		if model := payloadString(event.Payload, "model"); model != "" {
			summary.Model = model
		}

		switch event.Type {
		case ab.EventExecutableUpdated:
			status := payloadString(event.Payload, "status")
			if status != "" {
				summary.Status = status
			}
			summary.Result = payloadString(event.Payload, "result")
		case ab.EventSubagentCompleted:
			summary.Status = "success"
			summary.Result = asString(event.Payload["output"])
		case ab.EventSubagentStarted:
			summary.Status = "running"
		default:
			return
		}
		if strings.TrimSpace(summary.ID) == "" {
			return
		}
		log.Printf("run.event chat_id=%d run_id=%s mode=%s event=%s thread_id=%s status=%s", chatID, runID, firstNonEmpty(logContext.Mode, "balanced"), event.Type, threadID, summary.Status)

		publishRunner(summary)
	}

	assistant := GenerateAssistantReply(ctx, a.llm, toAgentMessages(history), req.Mode, effectiveModel, logContext, emitRunnerEvent, publishTrace)
	for _, summary := range assistant.Runners {
		publishRunner(summary)
	}
	assistantMsg, err := InsertMessage(ctx, a.db, chatID, "assistant", assistant.Content, effectiveModel)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = a.pub.PublishChatMessage(ctx, chatID, assistantMsg)

	assistantPayload := map[string]any{
		"id":        assistantMsg.ID,
		"chatId":    assistantMsg.ChatID,
		"runId":     runID,
		"role":      assistantMsg.Role,
		"content":   assistantMsg.Content,
		"model":     assistantMsg.Model,
		"createdAt": assistantMsg.CreatedAt,
	}
	if strings.TrimSpace(assistant.Reasoning) != "" {
		assistantPayload["reasoning"] = assistant.Reasoning
	}
	if len(assistant.Runners) > 0 {
		assistantPayload["runners"] = assistant.Runners
	}
	if len(assistant.Trace) > 0 {
		assistantPayload["trace"] = assistant.Trace
	}
	log.Printf("run.completed chat_id=%d run_id=%s mode=%s model=%s runners=%d assistant_message_id=%d", chatID, runID, firstNonEmpty(logContext.Mode, "balanced"), effectiveModel, len(assistant.Runners), assistantMsg.ID)

	writeJSON(w, http.StatusOK, map[string]any{
		"user":      userMsg,
		"assistant": assistantPayload,
	})
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return ""
	}
	text := strings.TrimSpace(fmt.Sprintf("%v", value))
	if text == "" || text == "<nil>" {
		return ""
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func toAgentMessages(messages []Message) []ab.ChatMessage {
	out := make([]ab.ChatMessage, 0, len(messages))
	for _, msg := range messages {
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}

		role := ab.RoleUser
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "assistant":
			role = ab.RoleAssistant
		case "tool":
			role = ab.RoleTool
		case "system":
			role = ab.RoleSystem
		}

		out = append(out, ab.ChatMessage{Role: role, Content: content})
	}
	return out
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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
	writeJSON(w, status, map[string]any{"error": err.Error()})
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
