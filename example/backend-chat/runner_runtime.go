package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	ab "github.com/everfaz/autobuild-sdk"
)

// RunnerSummary is the result of a spawned subagent, surfaced to the frontend.
type RunnerSummary struct {
	ID     string `json:"id"`
	Tier   string `json:"tier,omitempty"`
	Task   string `json:"task"`
	Status string `json:"status"`
	Result string `json:"result,omitempty"`
	Model  string `json:"model,omitempty"`
}

// agentRuntime wires the SDK Engine + Runtime for a single request.
type agentRuntime struct {
	provider   ab.LLMProvider
	model      string
	logContext RuntimeLogContext

	events         *ab.InMemoryEventBus
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
	reg.Register(r.newDocumentTool())
	if r.skills != nil {
		reg.Register(r.newSkillsTool())
	}
	if r.memory != nil {
		reg.Register(r.newMemoryTool())
	}
	return reg
}

func (r *agentRuntime) buildSubagentToolRegistry() *ab.ToolRegistry {
	reg := ab.NewToolRegistry()
	reg.Register(r.newCheckpointTool())
	reg.Register(r.newDocumentTool())
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

func providerOrEcho(provider ab.LLMProvider, model string) ab.LLMProvider {
	if provider != nil {
		return provider
	}
	return &EchoLLM{Model: model}
}
