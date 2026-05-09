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
	"time"

	ab "github.com/everfaz/autobuild-sdk"
)

// agentRuntime wires the SDK Engine + Runtime for a single request.
type agentRuntime struct {
	chatID         int64
	modelName      string // effective model for this run (e.g. "anthropic/claude-haiku-4-5-20251001")
	tools          *ab.ToolRegistry
	engine         *ab.Engine
	runtime        *ab.Runtime
	subagentEngine *ab.Engine
	skills         ab.SkillProvider
	memory         ab.MemoryProvider
	convStore      ab.ConversationStore
	execCtx        *ab.InMemoryExecutionContext // task checklist (TodoWrite/TodoRead)
}

// ── Tool registries ──────────────────────────────────────────────────────────

func (r *agentRuntime) buildToolRegistry() *ab.ToolRegistry {
	reg := ab.NewToolRegistry()
	reg.Register(r.newTodoTool())
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
	reg.Register(r.newRenderComponentTool())
	if r.chatID > 0 {
		reg.Register(r.newAwaitComponentInputTool(r.chatID))
	}
	// Sandbox tools — only when OPEN_SANDBOX_API_KEY is configured
	if isSandboxAvailable() && r.chatID > 0 {
		reg.Register(r.newBashTool(r.chatID))
		reg.Register(r.newCodeInterpreterTool(r.chatID))
		reg.Register(r.newFileWriteTool(r.chatID))
		reg.Register(r.newFileReadTool(r.chatID))
		reg.Register(r.newGlobTool(r.chatID))
		reg.Register(r.newGrepTool(r.chatID))
	} else {
		reg.Register(r.newGlobTool(0))
		reg.Register(r.newGrepTool(0))
	}
	return reg
}

func (r *agentRuntime) buildSubagentToolRegistry() *ab.ToolRegistry {
	reg := ab.NewToolRegistry()
	reg.Register(r.newTodoTool())
	if !isSandboxAvailable() || r.chatID <= 0 {
		reg.Register(r.newDocumentTool())
	}
	if r.memory != nil {
		reg.Register(r.newMemoryTool())
	}
	reg.Register(r.newRenderComponentTool())
	if isSandboxAvailable() && r.chatID > 0 {
		reg.Register(r.newBashTool(r.chatID))
		reg.Register(r.newCodeInterpreterTool(r.chatID))
		reg.Register(r.newFileWriteTool(r.chatID))
		reg.Register(r.newFileReadTool(r.chatID))
		reg.Register(r.newGlobTool(r.chatID))
		reg.Register(r.newGrepTool(r.chatID))
	} else {
		reg.Register(r.newGlobTool(0))
		reg.Register(r.newGrepTool(0))
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

func (r *agentRuntime) newTodoTool() *ab.Tool {
	return &ab.Tool{
		Name:        "todo_write",
		Description: "Manage the task checklist for the current session. Use this to track multi-step work so you always know what's done and what's next. Update the list as you go — mark items in_progress when you start them, completed when done.",
		Category:    ab.ToolCategoryPlanning,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"operation": {
					Type:        "string",
					Description: "One of: write (replace full list), read (get current list), mark_done (complete one item by id).",
					Enum:        []string{"write", "read", "mark_done"},
				},
				"todos": {
					Type:        "array",
					Description: "For write: full list of todos. Each item must have id (string), content (string), status (pending|in_progress|completed).",
					Items: &ab.ToolParam{
						Type: "object",
						Properties: map[string]ab.ToolParam{
							"id":      {Type: "string", Description: "Unique identifier for this todo item."},
							"content": {Type: "string", Description: "Description of the task."},
							"status":  {Type: "string", Description: "pending, in_progress, or completed.", Enum: []string{"pending", "in_progress", "completed"}},
						},
					},
				},
				"id": {Type: "string", Description: "For mark_done: the todo item ID to mark as completed."},
			},
			Required: []string{"operation"},
		},
		Execute: func(_ context.Context, _ string, args map[string]any) (string, error) {
			op := strings.ToLower(strings.TrimSpace(asString(args["operation"])))
			switch op {
			case "read":
				todos := r.execCtx.Todos()
				out, _ := json.Marshal(todos)
				return string(out), nil
			case "mark_done":
				id := strings.TrimSpace(asString(args["id"]))
				if id == "" {
					return "", fmt.Errorf("id is required for mark_done")
				}
				r.execCtx.MarkDone(id)
				return fmt.Sprintf("marked %q as completed", id), nil
			case "write":
				rawList, _ := args["todos"].([]any)
				todos := make([]ab.Todo, 0, len(rawList))
				for _, raw := range rawList {
					m, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					status := ab.TodoStatus(strings.ToLower(strings.TrimSpace(asString(m["status"]))))
					if status == "" {
						status = ab.TodoStatusPending
					}
					todos = append(todos, ab.Todo{
						ID:      strings.TrimSpace(asString(m["id"])),
						Content: strings.TrimSpace(asString(m["content"])),
						Status:  status,
					})
				}
				r.execCtx.SetTodos(todos)
				out, _ := json.Marshal(todos)
				return string(out), nil
			}
			return "unsupported operation", nil
		},
	}
}

func (r *agentRuntime) newGlobTool(chatID int64) *ab.Tool {
	return &ab.Tool{
		Name:        "glob",
		Description: "List files matching a glob pattern. Use this to explore the workspace structure and find files before reading or editing them.",
		Category:    ab.ToolCategoryWorkspace,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"pattern": {Type: "string", Description: "Glob pattern to match (e.g. '**/*.go', 'src/**/*.ts', '*.json'). Relative to sandbox root."},
			},
			Required: []string{"pattern"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			pattern := strings.TrimSpace(asString(args["pattern"]))
			if pattern == "" {
				return "", fmt.Errorf("pattern is required")
			}
			if isSandboxAvailable() && chatID > 0 {
				mgr := getSandboxManager()
				sbID, err := mgr.getOrCreateSandbox(ctx, chatID)
				if err != nil {
					return "", fmt.Errorf("sandbox unavailable: %w", err)
				}
				// Use find to support ** patterns across all depths
				safePattern := strings.ReplaceAll(pattern, "**", "*")
				cmd := fmt.Sprintf("find . -path %q -not -path '*/\\.*' 2>/dev/null | sort | head -200", "./"+safePattern)
				res, err := mgr.driver.Exec(ctx, sbID, cmd)
				if err != nil {
					return "", err
				}
				out := strings.TrimSpace(res.Stdout)
				if out == "" {
					return "no files matched", nil
				}
				return out, nil
			}
			// No sandbox fallback: local glob
			matches, err := filepath.Glob(pattern)
			if err != nil {
				return "", fmt.Errorf("invalid pattern: %w", err)
			}
			if len(matches) == 0 {
				return "no files matched", nil
			}
			return strings.Join(matches, "\n"), nil
		},
	}
}

func (r *agentRuntime) newGrepTool(chatID int64) *ab.Tool {
	return &ab.Tool{
		Name:        "grep",
		Description: "Search for a pattern in files. Returns matching lines with file names and line numbers. Use this to find definitions, usages, and references across the workspace.",
		Category:    ab.ToolCategoryWorkspace,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"pattern": {Type: "string", Description: "Text or regular expression to search for."},
				"path":    {Type: "string", Description: "Directory or file to search in. Defaults to '.' (entire workspace)."},
				"include": {Type: "string", Description: "Glob to restrict search to specific file types (e.g. '*.go', '*.ts')."},
			},
			Required: []string{"pattern"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			pattern := strings.TrimSpace(asString(args["pattern"]))
			if pattern == "" {
				return "", fmt.Errorf("pattern is required")
			}
			searchPath := strings.TrimSpace(asString(args["path"]))
			if searchPath == "" {
				searchPath = "."
			}
			include := strings.TrimSpace(asString(args["include"]))

			if isSandboxAvailable() && chatID > 0 {
				mgr := getSandboxManager()
				sbID, err := mgr.getOrCreateSandbox(ctx, chatID)
				if err != nil {
					return "", fmt.Errorf("sandbox unavailable: %w", err)
				}
				cmd := fmt.Sprintf("grep -rn --max-count=5 -e %q %q", pattern, searchPath)
				if include != "" {
					cmd += fmt.Sprintf(" --include=%q", include)
				}
				cmd += " 2>/dev/null | head -100"
				res, err := mgr.driver.Exec(ctx, sbID, cmd)
				if err != nil {
					return "", err
				}
				out := strings.TrimSpace(res.Stdout)
				if out == "" {
					return "no matches found", nil
				}
				return out, nil
			}
			// No sandbox fallback: local filesystem walk
			var results []string
			err := filepath.Walk(searchPath, func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil
				}
				if include != "" {
					matched, _ := filepath.Match(include, filepath.Base(path))
					if !matched {
						return nil
					}
				}
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					return nil
				}
				for i, line := range strings.Split(string(data), "\n") {
					if strings.Contains(line, pattern) {
						results = append(results, fmt.Sprintf("%s:%d:%s", path, i+1, line))
						if len(results) >= 100 {
							return filepath.SkipAll
						}
					}
				}
				return nil
			})
			if err != nil {
				return "", err
			}
			if len(results) == 0 {
				return "no matches found", nil
			}
			return strings.Join(results, "\n"), nil
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
	if value == nil {
		return ""
	}
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
					Description: "List of tasks to dispatch in parallel. Each task runs in its own isolated agent loop.",
					Items: &ab.ToolParam{
						Type: "object",
						Properties: map[string]ab.ToolParam{
							"id":                  {Type: "string", Description: "Optional identifier for tracing (autogenerated if omitted)."},
							"task":                {Type: "string", Description: "Self-contained instruction the subagent will execute."},
							"mode":                {Type: "string", Description: "Optional mode override (e.g. 'analyst', 'code-agent')."},
							"system_prompt":       {Type: "string", Description: "Custom persona for this subagent. Empty = generic focused-subagent prompt."},
							"model":               {Type: "string", Description: "Model override (e.g. 'claude-haiku-4-5-20251001' for cheaper specialists). Empty = engine default."},
							"max_turns":           {Type: "integer", Description: "Cap on subagent loop iterations (default 4)."},
							"timeout_seconds": {Type: "integer", Description: "Wall-clock timeout in seconds (default 120)."},
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
				systemPrompt := strings.TrimSpace(asString(m["system_prompt"]))
				model := strings.TrimSpace(asString(m["model"]))

				// Keep the routing prefix (e.g. "anthropic/...") so the
				// RoutedLLMProvider can dispatch to the correct backend.
				// RunAgentLoopWithEngine strips the prefix internally after
				// resolving the provider. Stripping here would send a bare
				// model name to the router, which has no match and falls
				// back to the raw engine.LLM with an empty model — the
				// Anthropic API then returns 404 with `model: <nil>`.
				effectiveModel := model
				if effectiveModel == "" {
					effectiveModel = r.modelName
				}
				subs = append(subs, ab.Subagent{
					ID:           id,
					Task:         task,
					Engine:       subEngine,
					Mode:         strings.TrimSpace(asString(m["mode"])),
					MaxTurns:     maxTurns,
					Timeout:      timeout,
					SystemPrompt: systemPrompt,
					Model:        effectiveModel,
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
				if res.Model != "" {
					entry["model"] = res.Model
				}
				if res.SystemPrompt != "" {
					entry["system_prompt"] = res.SystemPrompt
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
