package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ab "github.com/everfaz/autobuild-sdk"
)

// agentRuntime wires the SDK Engine + Runtime for a single request.
type agentRuntime struct {
	chatID         int64
	tools          *ab.ToolRegistry
	engine         *ab.Engine
	runtime        *ab.Runtime
	execCtx        ab.ExecutionContext
	subagentEngine *ab.Engine
	skills         ab.SkillProvider
	memory         ab.MemoryProvider
	checkpoints    *checkpointStore
	convStore      ab.ConversationStore
}

// ── Tool registries ──────────────────────────────────────────────────────────

func (r *agentRuntime) buildToolRegistry() *ab.ToolRegistry {
	reg := ab.NewToolRegistry()
	reg.Register(r.newCheckpointTool())
	// Prefer sandbox file tools when available to avoid writing local host files.
	if !isSandboxAvailable() || r.chatID <= 0 {
		reg.Register(r.newDocumentTool())
	}
	if r.skills != nil {
		reg.Register(r.newSkillsTool())
	}
	if r.memory != nil {
		reg.Register(r.newMemoryTool())
	}
	reg.Register(r.newSubagentDispatchTool())
	// Sandbox tools — only when OPEN_SANDBOX_API_KEY is configured
	if isSandboxAvailable() && r.chatID > 0 {
		reg.Register(r.newBashTool(r.chatID))
		reg.Register(r.newCodeInterpreterTool(r.chatID))
		reg.Register(r.newFileWriteTool(r.chatID))
		reg.Register(r.newFileReadTool(r.chatID))
	}
	return reg
}

func (r *agentRuntime) buildSubagentToolRegistry() *ab.ToolRegistry {
	reg := ab.NewToolRegistry()
	reg.Register(r.newCheckpointTool())
	if !isSandboxAvailable() || r.chatID <= 0 {
		reg.Register(r.newDocumentTool())
	}
	if r.memory != nil {
		reg.Register(r.newMemoryTool())
	}
	if isSandboxAvailable() && r.chatID > 0 {
		reg.Register(r.newBashTool(r.chatID))
		reg.Register(r.newCodeInterpreterTool(r.chatID))
		reg.Register(r.newFileWriteTool(r.chatID))
		reg.Register(r.newFileReadTool(r.chatID))
	}
	return reg
}

// ── Tool definitions ─────────────────────────────────────────────────────────

func (r *agentRuntime) newMemoryTool() *ab.Tool {
	return &ab.Tool{
		Name:        "memory-operations",
		Description: "View and edit persistent memory in user or project scope.",
		Category:    ab.ToolCategoryMemory,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"operation": {
					Type:        "string",
					Description: "One of: view, create, str_replace, delete, rename, list, search.",
					Enum:        []string{"view", "create", "str_replace", "delete", "rename", "list", "search"},
				},
				"scope":   {Type: "string", Description: "user or project", Enum: []string{"user", "project", "*"}},
				"path":    {Type: "string", Description: "Memory path."},
				"content": {Type: "string", Description: "File content for create."},
				"oldStr":  {Type: "string", Description: "Old string for str_replace."},
				"newStr":  {Type: "string", Description: "New string for str_replace."},
				"oldPath": {Type: "string", Description: "Source path for rename."},
				"newPath": {Type: "string", Description: "Destination path for rename."},
				"query":   {Type: "string", Description: "Query text for search."},
			},
			Required: []string{"operation"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			if r.memory == nil {
				return "memory provider not configured", nil
			}
			op := strings.ToLower(strings.TrimSpace(asString(args["operation"])))
			scope := parseMemoryScope(asString(args["scope"]))

			switch op {
			case "view":
				out, err := r.memory.View(ctx, scope, asString(args["path"]))
				return out, err
			case "create":
				if err := r.memory.Create(ctx, ensureWritableScope(scope), asString(args["path"]), asString(args["content"])); err != nil {
					return "", err
				}
				return "memory created", nil
			case "str_replace":
				if err := r.memory.StrReplace(ctx, ensureWritableScope(scope), asString(args["path"]), asString(args["oldStr"]), asString(args["newStr"])); err != nil {
					return "", err
				}
				return "memory updated", nil
			case "delete":
				if err := r.memory.Delete(ctx, ensureWritableScope(scope), asString(args["path"])); err != nil {
					return "", err
				}
				return "memory deleted", nil
			case "rename":
				if err := r.memory.Rename(ctx, ensureWritableScope(scope), asString(args["oldPath"]), asString(args["newPath"])); err != nil {
					return "", err
				}
				return "memory renamed", nil
			case "list":
				items, err := r.memory.List(ctx, scope, asString(args["path"]))
				if err != nil {
					return "", err
				}
				return strings.Join(items, "\n"), nil
			case "search":
				entries, err := r.memory.Search(ctx, scope, asString(args["query"]))
				if err != nil {
					return "", err
				}
				lines := make([]string, 0, len(entries))
				for _, e := range entries {
					lines = append(lines, fmt.Sprintf("- [%s] %s", e.Scope, e.Path))
				}
				return strings.Join(lines, "\n"), nil
			}
			return "unsupported operation", nil
		},
	}
}

func (r *agentRuntime) newSkillsTool() *ab.Tool {
	return &ab.Tool{
		Name:        "skills-operations",
		Description: "List, match, inspect, load, and unload backend skills.",
		Category:    ab.ToolCategoryPlanning,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"operation": {Type: "string", Enum: []string{"list", "match", "get", "load", "unload"}},
				"skillName": {Type: "string", Description: "Skill name for get/load/unload."},
				"query":     {Type: "string", Description: "Text to match triggers against."},
			},
			Required: []string{"operation"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			if r.skills == nil {
				return "skills provider not configured", nil
			}
			op := strings.ToLower(strings.TrimSpace(asString(args["operation"])))
			skillName := strings.TrimSpace(asString(args["skillName"]))
			query := strings.TrimSpace(asString(args["query"]))

			switch op {
			case "list":
				names, err := r.skills.List(ctx)
				if err != nil {
					return "", err
				}
				if len(names) == 0 {
					return "No skills available.", nil
				}
				for i := range names {
					names[i] = "- " + names[i]
				}
				return "Available skills:\n" + strings.Join(names, "\n"), nil
			case "match":
				if query == "" {
					return "query is required for match", nil
				}
				matched, err := r.skills.Match(ctx, query)
				if err != nil {
					return "", err
				}
				if len(matched) == 0 {
					return "No skills matched.", nil
				}
				lines := make([]string, 0, len(matched))
				for _, m := range matched {
					lines = append(lines, fmt.Sprintf("- %s (score %.2f): %s", m.Skill.Name, m.Score, m.Skill.Meta.Description))
				}
				return "Matched skills:\n" + strings.Join(lines, "\n"), nil
			case "get":
				if skillName == "" {
					return "skillName is required", nil
				}
				skill, err := r.skills.Get(ctx, skillName)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Skill %s\n\n%s", skill.Name, strings.TrimSpace(skill.Content)), nil
			case "load":
				if skillName == "" {
					return "skillName is required", nil
				}
				skill, err := r.skills.Load(ctx, skillName)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Loaded skill %s\n\n%s", skill.Name, strings.TrimSpace(skill.Content)), nil
			case "unload":
				if skillName == "" {
					return "skillName is required", nil
				}
				if err := r.skills.Unload(ctx, skillName); err != nil {
					return "", err
				}
				return fmt.Sprintf("Unloaded skill %s", skillName), nil
			}
			return "unsupported operation", nil
		},
	}
}

func (r *agentRuntime) newCheckpointTool() *ab.Tool {
	return &ab.Tool{
		Name:        "create-checkpoint",
		Description: "Create a lightweight checkpoint label before or after a mutation.",
		Category:    ab.ToolCategoryPlanning,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"label": {Type: "string", Description: "Human-readable checkpoint label."},
			},
			Required: []string{"label"},
		},
		Execute: func(_ context.Context, _ string, args map[string]any) (string, error) {
			label := strings.TrimSpace(asString(args["label"]))
			if label == "" {
				return "", fmt.Errorf("checkpoint label is required")
			}
			id := r.checkpoints.Create(label)
			return fmt.Sprintf("checkpoint created: %s (%s)", id, label), nil
		},
	}
}

func (r *agentRuntime) newDocumentTool() *ab.Tool {
	return &ab.Tool{
		Name:        "document-operations",
		Description: "Create or overwrite a text document in the local workspace.",
		Category:    ab.ToolCategoryWorkspace,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"action":  {Type: "string", Description: "Supported values: create, write."},
				"path":    {Type: "string", Description: "Relative file path to create."},
				"content": {Type: "string", Description: "Text content to write."},
			},
			Required: []string{"action", "path", "content"},
		},
		Execute: func(_ context.Context, _ string, args map[string]any) (string, error) {
			action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
			if action != "create" && action != "write" {
				return "", fmt.Errorf("unsupported document action: %s", action)
			}
			relPath := filepath.Clean(strings.TrimSpace(asString(args["path"])))
			if relPath == "" || relPath == "." || filepath.IsAbs(relPath) || strings.HasPrefix(relPath, "..") {
				return "", fmt.Errorf("path must be a relative workspace path")
			}
			content := asString(args["content"])
			if err := os.MkdirAll(filepath.Dir(relPath), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(relPath, []byte(content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("document %s: %s", action, relPath), nil
		},
	}
}

// ── Checkpoint ───────────────────────────────────────────────────────────────

type checkpointStore struct {
	nextID atomic.Uint64
}

func (s *checkpointStore) Create(label string) string {
	id := s.nextID.Add(1)
	return fmt.Sprintf("cp_%d_%s", id, slugify(label))
}

// ── helpers ──────────────────────────────────────────────────────────────────

func parseMemoryScope(value string) ab.Scope {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "user":
		return ab.ScopeUser
	case "*", "all":
		return ab.Scope("*")
	default:
		return ab.ScopeProject
	}
}

func ensureWritableScope(scope ab.Scope) ab.Scope {
	if scope == ab.Scope("*") {
		return ab.ScopeProject
	}
	return scope
}

func asString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprintf("%v", value)
	}
}

func slugify(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	trimmed = strings.ReplaceAll(trimmed, " ", "-")
	trimmed = strings.ReplaceAll(trimmed, "/", "-")
	trimmed = strings.ReplaceAll(trimmed, "_", "-")
	if trimmed == "" {
		return "checkpoint"
	}
	return trimmed
}

func previewText(value string, limit int) string {
	trimmed := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(trimmed) <= limit || limit <= 3 {
		return trimmed
	}
	return trimmed[:limit-3] + "..."
}

// newSubagentDispatchTool exposes the SDK's subagent system to the LLM as a
// callable tool. The model can fan out 1..N independent tasks, each running
// in an isolated agent loop with restricted tool access. Use this when the
// user asks for parallel work or the task naturally decomposes into
// independent units.
func (r *agentRuntime) newSubagentDispatchTool() *ab.Tool {
	return &ab.Tool{
		Name:        "dispatch-subagents",
		Description: "Spawn one or more isolated subagents to handle independent tasks in parallel. Each subagent gets its own focused agent loop with restricted tools (memory, document, checkpoint) and returns a structured result. Use this for fan-out work: research multiple sources, create multiple files, validate against multiple criteria. Each task must be self-contained — subagents do not share parent context beyond the task description. For research + writing tasks, set timeout_seconds to 60-120 (default 90).",
		Category:    ab.ToolCategoryPlanning,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"tasks": {
					Type:        "array",
					Description: "List of tasks to dispatch in parallel. Provide a clear, self-contained instruction per task.",
					Items: &ab.ToolParam{
						Type: "object",
						Properties: map[string]ab.ToolParam{
							"id":              {Type: "string", Description: "Optional identifier for this task (autogenerated if omitted)."},
							"task":            {Type: "string", Description: "Self-contained instruction the subagent will execute."},
							"mode":            {Type: "string", Description: "Optional mode override (e.g. 'analyst', 'code-agent')."},
							"max_turns":       {Type: "integer", Description: "Cap on subagent loop iterations (default 4)."},
							"timeout_seconds": {Type: "integer", Description: "Wall-clock timeout in seconds (default 90). Use 60-120 for larger research/writing tasks."},
						},
						Required: []string{"task"},
					},
				},
			},
			Required: []string{"tasks"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			if r.subagentEngine == nil {
				return "subagent engine is not configured", nil
			}
			rawTasks, ok := args["tasks"].([]any)
			if !ok || len(rawTasks) == 0 {
				return "tasks must be a non-empty array", nil
			}

			// Track file writes made by subagents so the parent stream
			// can emit them as artifacts visible to the frontend.
			tracker := &fileWriteTracker{}
			subEngine := r.subagentEngine
			if isSandboxAvailable() && r.chatID > 0 {
				subReg := ab.NewToolRegistry()
				subReg.Register(r.newCheckpointTool())
				if r.memory != nil {
					subReg.Register(r.newMemoryTool())
				}
				subReg.Register(r.newBashTool(r.chatID))
				subReg.Register(r.newCodeInterpreterTool(r.chatID))
				subReg.Register(r.newTrackedFileWriteTool(r.chatID, tracker))
				subReg.Register(r.newFileReadTool(r.chatID))
				subEngine = ab.New(
					ab.WithLLM(r.subagentEngine.LLM),
					ab.WithToolRegistry(subReg),
				)
			}

			subs := make([]ab.Subagent, 0, len(rawTasks))
			for i, raw := range rawTasks {
				m, ok := raw.(map[string]any)
				if !ok {
					return "", fmt.Errorf("tasks[%d] must be an object", i)
				}
				task := strings.TrimSpace(asString(m["task"]))
				if task == "" {
					return "", fmt.Errorf("tasks[%d].task is required", i)
				}
				id := strings.TrimSpace(asString(m["id"]))
				if id == "" {
					id = fmt.Sprintf("sub_%d_%d", time.Now().UnixNano(), i)
				}
				maxTurns := 4
				if v, ok := m["max_turns"].(float64); ok && v > 0 {
					maxTurns = int(v)
				}
				timeout := 120 * time.Second
				if v, ok := m["timeout_seconds"].(float64); ok && v > 0 {
					timeout = time.Duration(v) * time.Second
				}
				subs = append(subs, ab.Subagent{
					ID:       id,
					Task:     task,
					Engine:   subEngine,
					Mode:     strings.TrimSpace(asString(m["mode"])),
					MaxTurns: maxTurns,
					Timeout:  timeout,
				})
			}

			results := ab.RunSubagentsInParallel(ctx, subs)

			// Build LLM-friendly summary: keep it compact, surface errors clearly.
			summaries := make([]map[string]any, 0, len(results))
			for _, res := range results {
				if res == nil {
					continue
				}
				entry := map[string]any{
					"id":          res.ID,
					"task":        res.Task,
					"output":      truncate(res.Output, 2000),
					"turns":       res.Turns,
					"stop_reason": res.StopReason,
					"duration_ms": res.Duration.Milliseconds(),
				}
				if res.Error != nil {
					entry["error"] = res.Error.Error()
					if isTimeoutError(res.Error) {
						entry["timed_out"] = true
					}
				}
				summaries = append(summaries, entry)
			}
			out, err := json.Marshal(map[string]any{
				"count":         len(summaries),
				"results":       summaries,
				"files_created": tracker.collected(),
			})
			if err != nil {
				return "", fmt.Errorf("marshal subagent results: %w", err)
			}
			return string(out), nil
		},
	}
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "deadline exceeded")
}

// fileWriteTracker records file writes made during subagent execution so
// the parent stream handler can emit them as artifacts.
type fileWriteTracker struct {
	mu    sync.Mutex
	files []trackedFile
}

type trackedFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t *fileWriteTracker) record(path, content string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.files = append(t.files, trackedFile{Path: path, Content: content})
}

func (t *fileWriteTracker) collected() []trackedFile {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]trackedFile, len(t.files))
	copy(out, t.files)
	return out
}

// newTrackedFileWriteTool is like newFileWriteTool but also records writes to a tracker.
func (r *agentRuntime) newTrackedFileWriteTool(chatID int64, tracker *fileWriteTracker) *ab.Tool {
	mgr := getSandboxManager()
	return &ab.Tool{
		Name:        "file_write",
		Description: "Write a file to the sandbox filesystem. The file persists across turns and can be read, executed, or served by subsequent tool calls. Use for: saving scripts, data files, HTML pages, config files.",
		Category:    ab.ToolCategoryWorkspace,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"path":    {Type: "string", Description: "Absolute or relative file path (e.g. /workspace/script.py or data.csv)."},
				"content": {Type: "string", Description: "File content to write."},
			},
			Required: []string{"path", "content"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			path := strings.TrimSpace(asString(args["path"]))
			content := asString(args["content"])
			if path == "" {
				return "", fmt.Errorf("path is required")
			}

			sbID, err := mgr.getOrCreateSandbox(ctx, chatID)
			if err != nil {
				return "", fmt.Errorf("sandbox unavailable: %w", err)
			}

			if err := mgr.driver.WriteFile(ctx, sbID, path, content); err != nil {
				return "", fmt.Errorf("write file: %w", err)
			}
			tracker.record(path, content)
			return fmt.Sprintf("file written: %s (%d bytes)", path, len(content)), nil
		},
	}
}
