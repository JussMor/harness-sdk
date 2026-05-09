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

	// memoryViewed tracks which "scope:path" pairs the LLM has read this run.
	// memory_create requires that MEMORY.md was viewed first — mirrors Claude
	// Code's FileWriteTool.MUST_READ_FIRST contract: the only reliable way to
	// stop the model from creating duplicates is to make create() fail unless
	// the index was actually inspected.
	memoryViewedMu sync.Mutex
	memoryViewed   map[string]bool
}

func (r *agentRuntime) markMemoryViewed(scope ab.Scope, path string) {
	r.memoryViewedMu.Lock()
	defer r.memoryViewedMu.Unlock()
	if r.memoryViewed == nil {
		r.memoryViewed = make(map[string]bool)
	}
	r.memoryViewed[string(scope)+":"+path] = true
}

func (r *agentRuntime) hasMemoryViewed(scope ab.Scope, path string) bool {
	r.memoryViewedMu.Lock()
	defer r.memoryViewedMu.Unlock()
	return r.memoryViewed[string(scope)+":"+path]
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
		for _, t := range r.newMemoryTools() {
			reg.Register(t)
		}
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
		for _, t := range r.newMemoryTools() {
			reg.Register(t)
		}
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

// newMemoryTools returns the 5 individual memory tools that mirror Claude
// Code's Read/Edit/Write/Glob model — separate verbs, separate descriptions,
// strict validation. The LLM picks the right one instead of guessing which
// "operation" enum value goes with which params (which was the source of
// the Failed tool calls in the monolithic version).
func (r *agentRuntime) newMemoryTools() []*ab.Tool {
	return []*ab.Tool{
		r.memoryViewTool(),
		r.memoryCreateTool(),
		r.memoryStrReplaceTool(),
		r.memoryDeleteTool(),
		r.memoryListTool(),
	}
}

func memoryScopeParam() ab.ToolParam {
	return ab.ToolParam{
		Type:        "string",
		Description: "Scope: 'user' for cross-project memory (preferences, role), 'project' for chat-specific memory.",
		Enum:        []string{"user", "project"},
	}
}

// requireScope returns an explicit error rather than silently defaulting,
// because silent defaults caused user-scope writes to land in project.
func requireScope(value string) (ab.Scope, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "user":
		return ab.ScopeUser, nil
	case "project":
		return ab.ScopeProject, nil
	default:
		return "", fmt.Errorf("scope must be 'user' or 'project' (got %q)", value)
	}
}

// validateMemoryWritePath rejects unsafe or special paths before any write.
//   - Must be non-empty and end in .md (memory files are markdown).
//   - Must not be the entrypoint MEMORY.md (use memory_str_replace to update
//     the index — recreating it would clobber the curated index).
//   - Must not contain ".." (path traversal) or absolute path roots.
func validateMemoryWritePath(path string) error {
	p := strings.TrimSpace(path)
	if p == "" {
		return fmt.Errorf("path is required")
	}
	if !strings.HasSuffix(strings.ToLower(p), ".md") {
		return fmt.Errorf("path must end in .md (got %q)", p)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("path must not contain '..'")
	}
	base := p
	if i := strings.LastIndex(p, "/"); i >= 0 {
		base = p[i+1:]
	}
	if strings.EqualFold(base, ab.EntrypointName) {
		return fmt.Errorf("%s is the always-loaded index and cannot be created/recreated; use memory_str_replace to update it", ab.EntrypointName)
	}
	return nil
}

func (r *agentRuntime) memoryViewTool() *ab.Tool {
	return &ab.Tool{
		Name:        "memory_view",
		Description: "Read a memory file or list a memory directory. **You MUST call this on /MEMORY.md before any memory_create** — it is how you avoid duplicates.",
		Category:    ab.ToolCategoryMemory,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"scope": memoryScopeParam(),
				"path":  {Type: "string", Description: "Path within the scope, e.g. '/MEMORY.md' or '/feedback_testing.md'. Use '/' for the scope root listing."},
			},
			Required: []string{"scope", "path"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			if r.memory == nil {
				return "", fmt.Errorf("memory provider not configured")
			}
			scope, err := requireScope(asString(args["scope"]))
			if err != nil {
				return "", err
			}
			path := asString(args["path"])
			out, err := r.memory.View(ctx, scope, path)
			if err != nil {
				return "", err
			}
			r.markMemoryViewed(scope, path)
			return out, nil
		},
	}
}

func (r *agentRuntime) memoryCreateTool() *ab.Tool {
	return &ab.Tool{
		Name:        "memory_create",
		Description: "Create a new memory file. ALWAYS read MEMORY.md first to ensure the topic isn't already covered by an existing file — duplicates pollute recall. Content must start with YAML frontmatter (name, description, type).",
		Category:    ab.ToolCategoryMemory,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"scope":   memoryScopeParam(),
				"path":    {Type: "string", Description: "Path ending in .md, e.g. '/feedback_testing.md'. Cannot be /MEMORY.md."},
				"content": {Type: "string", Description: "Full file content including YAML frontmatter (---\\nname: ...\\ndescription: ...\\ntype: user|feedback|project|reference\\n---\\n\\nbody)."},
			},
			Required: []string{"scope", "path", "content"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			if r.memory == nil {
				return "", fmt.Errorf("memory provider not configured")
			}
			scope, err := requireScope(asString(args["scope"]))
			if err != nil {
				return "", err
			}
			path := asString(args["path"])
			if err := validateMemoryWritePath(path); err != nil {
				return "", err
			}
			content := asString(args["content"])
			if !strings.HasPrefix(strings.TrimSpace(content), "---") {
				return "", fmt.Errorf("content must begin with YAML frontmatter delimited by --- lines")
			}
			name, desc, mtype := extractMemoryFrontmatterFields(content)
			if name == "" {
				return "", fmt.Errorf("frontmatter must include a 'name' field")
			}
			if desc == "" {
				return "", fmt.Errorf("frontmatter must include a 'description' field")
			}
			if !isValidMemoryType(mtype) {
				return "", fmt.Errorf("frontmatter 'type' must be one of: user, feedback, project, reference (got %q)", mtype)
			}
			// Read-before-write enforcement (Claude Code FileWriteTool pattern).
			// Per-scope: each scope (user, project) has its OWN MEMORY.md index
			// and must be viewed independently before its first write.
			if !r.hasMemoryViewed(scope, "/"+ab.EntrypointName) {
				return "", fmt.Errorf(
					"read-before-write violation. Each scope has its own MEMORY.md index. Before creating in scope=%s you must call: memory_view {scope: %q, path: \"/%s\"}. Then retry this memory_create call. (Reading another scope's MEMORY.md does NOT count.)",
					scope, string(scope), ab.EntrypointName,
				)
			}
			// Duplicate detection: scan headers and reject if a sibling file has
			// a clearly similar description. Forces the model to update instead
			// of creating a parallel file with a slightly different name.
			if scanner, ok := r.memory.(ab.MemoryHeaderScanner); ok {
				headers, _ := scanner.ScanHeaders(ctx, scope, "/")
				for _, h := range headers {
					if h.Path == path {
						continue
					}
					if memoryDescriptionsCollide(h.Description, desc) {
						return "", fmt.Errorf("a similar memory already exists at %s%s (description: %q). Use memory_view to read it then memory_str_replace to update it \u2014 do not create a parallel file", scope, h.Path, h.Description)
					}
				}
			}
			if err := r.memory.Create(ctx, scope, path, content); err != nil {
				return "", err
			}
			indexNote := upsertMemoryIndex(ctx, r.memory, scope, path, name, desc, mtype)
			return fmt.Sprintf(
				"created %s%s\n<system-reminder>%s. The MEMORY.md index was updated automatically; do not call memory_str_replace on it for this entry. If the description is wrong, fix the file's frontmatter via memory_str_replace and re-create.</system-reminder>",
				scope, path, indexNote,
			), nil
		},
	}
}

func (r *agentRuntime) memoryStrReplaceTool() *ab.Tool {
	return &ab.Tool{
		Name:        "memory_str_replace",
		Description: "Edit a memory file by replacing an exact substring. The old_str must appear EXACTLY once in the file. Use this to update existing memories or to add lines to MEMORY.md.",
		Category:    ab.ToolCategoryMemory,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"scope":   memoryScopeParam(),
				"path":    {Type: "string", Description: "Path of the existing memory file to edit."},
				"old_str": {Type: "string", Description: "Exact substring to replace. Must match exactly once."},
				"new_str": {Type: "string", Description: "Replacement text."},
			},
			Required: []string{"scope", "path", "old_str", "new_str"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			if r.memory == nil {
				return "", fmt.Errorf("memory provider not configured")
			}
			scope, err := requireScope(asString(args["scope"]))
			if err != nil {
				return "", err
			}
			path := asString(args["path"])
			if strings.TrimSpace(path) == "" {
				return "", fmt.Errorf("path is required")
			}
			if err := r.memory.StrReplace(ctx, scope, path, asString(args["old_str"]), asString(args["new_str"])); err != nil {
				return "", err
			}
			return "updated " + string(scope) + path, nil
		},
	}
}

func (r *agentRuntime) memoryDeleteTool() *ab.Tool {
	return &ab.Tool{
		Name:        "memory_delete",
		Description: "Delete a memory file. Also remove its line from MEMORY.md afterwards via memory_str_replace.",
		Category:    ab.ToolCategoryMemory,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"scope": memoryScopeParam(),
				"path":  {Type: "string", Description: "Path of the memory file to delete. Cannot delete MEMORY.md."},
			},
			Required: []string{"scope", "path"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			if r.memory == nil {
				return "", fmt.Errorf("memory provider not configured")
			}
			scope, err := requireScope(asString(args["scope"]))
			if err != nil {
				return "", err
			}
			path := asString(args["path"])
			base := path
			if i := strings.LastIndex(path, "/"); i >= 0 {
				base = path[i+1:]
			}
			if strings.EqualFold(base, ab.EntrypointName) {
				return "", fmt.Errorf("%s cannot be deleted", ab.EntrypointName)
			}
			if err := r.memory.Delete(ctx, scope, path); err != nil {
				return "", err
			}
			indexNote := removeMemoryIndexEntry(ctx, r.memory, scope, path)
			return fmt.Sprintf(
				"deleted %s%s\n<system-reminder>%s.</system-reminder>",
				scope, path, indexNote,
			), nil
		},
	}
}

func (r *agentRuntime) memoryListTool() *ab.Tool {
	return &ab.Tool{
		Name:        "memory_list",
		Description: "List memory files in a scope. Useful before creating a new file to spot existing topics.",
		Category:    ab.ToolCategoryMemory,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"scope": memoryScopeParam(),
				"path":  {Type: "string", Description: "Optional subdirectory; defaults to '/'."},
			},
			Required: []string{"scope"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			if r.memory == nil {
				return "", fmt.Errorf("memory provider not configured")
			}
			scope, err := requireScope(asString(args["scope"]))
			if err != nil {
				return "", err
			}
			path := asString(args["path"])
			if strings.TrimSpace(path) == "" {
				path = "/"
			}
			items, err := r.memory.List(ctx, scope, path)
			if err != nil {
				return "", err
			}
			if len(items) == 0 {
				return "(empty)", nil
			}
			return strings.Join(items, "\n"), nil
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

// extractMemoryFrontmatterFields pulls name/description/type from the YAML
// frontmatter at the top of a memory file. Empty strings if absent.
func extractMemoryFrontmatterFields(content string) (name, description, mtype string) {
	trimmed := strings.TrimLeft(content, " \t\n\r")
	if !strings.HasPrefix(trimmed, "---") {
		return "", "", ""
	}
	lines := strings.Split(trimmed, "\n")
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			break
		}
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.Trim(strings.TrimSpace(line[colon+1:]), "\"'")
		switch strings.ToLower(key) {
		case "name":
			name = val
		case "description":
			description = val
		case "type":
			mtype = val
		}
	}
	return name, description, mtype
}

func isValidMemoryType(t string) bool {
	for _, m := range ab.AllMemoryTypes {
		if string(m) == t {
			return true
		}
	}
	return false
}

// memoryDescriptionsCollide reports whether two memory descriptions cover
// substantially the same topic. Uses a token-overlap (Jaccard) heuristic
// — cheap, no embeddings required. Threshold 0.5 is intentionally lenient
// because a false positive forces the model to update an existing file
// (correct behavior) while a false negative lets a duplicate slip through.
func memoryDescriptionsCollide(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	tokensA := memoryDescTokens(a)
	tokensB := memoryDescTokens(b)
	if len(tokensA) == 0 || len(tokensB) == 0 {
		return false
	}
	var inter int
	for tok := range tokensA {
		if tokensB[tok] {
			inter++
		}
	}
	union := len(tokensA) + len(tokensB) - inter
	if union == 0 {
		return false
	}
	return float64(inter)/float64(union) >= 0.5
}

// memoryDescTokens lowercases, splits on non-letters, and drops short
// stopwords so common words like "the" / "and" don't bloat the union.
func memoryDescTokens(s string) map[string]bool {
	out := make(map[string]bool)
	var b strings.Builder
	flush := func() {
		w := b.String()
		b.Reset()
		if len(w) < 4 {
			return
		}
		out[w] = true
	}
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// upsertMemoryIndex appends (or replaces) a one-line pointer in MEMORY.md
// for the given memory file. Lines are uniquely keyed by the file path so
// repeated creates/updates don't accumulate duplicates. Returns a short
// human-readable note for the tool result.
func upsertMemoryIndex(ctx context.Context, mem ab.MemoryProvider, scope ab.Scope, path, name, desc, mtype string) string {
	indexPath := "/" + ab.EntrypointName
	rel := strings.TrimPrefix(path, "/")
	newLine := fmt.Sprintf("- [%s] [%s](%s) — %s", mtype, name, rel, desc)

	current, err := mem.View(ctx, scope, indexPath)
	if err != nil || strings.TrimSpace(current) == "" {
		// Index doesn't exist yet — create a minimal one.
		seed := fmt.Sprintf("# %s memory index\n\n%s\n", scope, newLine)
		if cerr := mem.Create(ctx, scope, indexPath, seed); cerr == nil {
			return "indexed in MEMORY.md"
		}
		return "created (could not seed MEMORY.md)"
	}

	// Look for an existing line that points to the same file (any line that
	// contains the rel path inside () — robust to mtype/desc updates).
	marker := "(" + rel + ")"
	if idx := strings.Index(current, marker); idx >= 0 {
		// Find the full line containing the marker.
		lineStart := strings.LastIndex(current[:idx], "\n") + 1
		lineEnd := idx + len(marker)
		if nl := strings.Index(current[lineEnd:], "\n"); nl >= 0 {
			lineEnd += nl
		} else {
			lineEnd = len(current)
		}
		oldLine := current[lineStart:lineEnd]
		if oldLine == newLine {
			return "already indexed in MEMORY.md"
		}
		if err := mem.StrReplace(ctx, scope, indexPath, oldLine, newLine); err == nil {
			return "updated entry in MEMORY.md"
		}
		return "indexed (line update failed)"
	}

	// Append a new line. Find a unique anchor (the trailing newline of the
	// current content) so StrReplace's "must appear once" rule is satisfied.
	if strings.HasSuffix(current, "\n") {
		anchor := current[len(current)-1:]
		if err := mem.StrReplace(ctx, scope, indexPath, anchor, "\n"+newLine+"\n"); err == nil {
			return "indexed in MEMORY.md"
		}
	}
	// Fall back to a delete+create (preserves content + appends).
	updated := strings.TrimRight(current, "\n") + "\n" + newLine + "\n"
	if err := mem.Delete(ctx, scope, indexPath); err == nil {
		if cerr := mem.Create(ctx, scope, indexPath, updated); cerr == nil {
			return "indexed in MEMORY.md"
		}
	}
	return "indexed (append failed)"
}

// removeMemoryIndexEntry deletes the line in MEMORY.md that points to the
// given file path. No-op if MEMORY.md doesn't exist or has no such entry.
func removeMemoryIndexEntry(ctx context.Context, mem ab.MemoryProvider, scope ab.Scope, path string) string {
	indexPath := "/" + ab.EntrypointName
	rel := strings.TrimPrefix(path, "/")
	current, err := mem.View(ctx, scope, indexPath)
	if err != nil || current == "" {
		return "removed from index (no MEMORY.md)"
	}
	marker := "(" + rel + ")"
	idx := strings.Index(current, marker)
	if idx < 0 {
		return "no MEMORY.md entry to remove"
	}
	lineStart := strings.LastIndex(current[:idx], "\n") + 1
	lineEnd := idx + len(marker)
	if nl := strings.Index(current[lineEnd:], "\n"); nl >= 0 {
		lineEnd += nl + 1 // include the newline
	} else {
		lineEnd = len(current)
	}
	oldLine := current[lineStart:lineEnd]
	if err := mem.StrReplace(ctx, scope, indexPath, oldLine, ""); err != nil {
		return "removed file (could not update MEMORY.md)"
	}
	return "removed entry from MEMORY.md"
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
					for _, t := range r.newMemoryTools() {
						subReg.Register(t)
					}
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
