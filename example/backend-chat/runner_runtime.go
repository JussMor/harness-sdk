package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ab "github.com/everfaz/autobuild-sdk"
)

type RunnerSummary struct {
	ID     string `json:"id"`
	Tier   string `json:"tier"`
	Task   string `json:"task"`
	Status string `json:"status"`
	Result string `json:"result,omitempty"`
	Model  string `json:"model,omitempty"`
}

type agentRuntime struct {
	provider    ab.LLMProvider
	model       string
	logContext  RuntimeLogContext
	events      *ab.InMemoryEventBus
	tools       *ab.ToolRegistry
	threads     *inMemoryThreadProvider
	plans       ab.PlanProvider
	workflow    ab.WorkflowEngine
	skills      ab.SkillProvider
	memory      ab.MemoryProvider
	checkpoints *checkpointStore
}

func newAgentRuntime(provider ab.LLMProvider, model string, logContext RuntimeLogContext, skills ab.SkillProvider, memory ab.MemoryProvider) *agentRuntime {
	runtime := &agentRuntime{
		provider:    provider,
		model:       model,
		logContext:  logContext,
		events:      ab.NewEventBus(),
		skills:      skills,
		memory:      memory,
		checkpoints: &checkpointStore{},
	}
	runtime.plans = newInMemoryPlanProvider(runtime.events)
	runtime.workflow = newInMemoryWorkflowEngine(runtime.events)
	runtime.threads = newInMemoryThreadProvider(runtime)
	runtime.tools = runtime.buildToolRegistry()
	return runtime
}

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

func (r *agentRuntime) buildRunnerToolRegistry() *ab.ToolRegistry {
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
					Description: "One of: view, create, str_replace, insert, delete, rename, list, search.",
					Enum:        []string{"view", "create", "str_replace", "insert", "delete", "rename", "list", "search"},
				},
				"scope": {
					Type:        "string",
					Description: "Memory scope: user, project, or * for cross-scope reads.",
					Enum:        []string{"user", "project", "*"},
				},
				"path": {
					Type:        "string",
					Description: "Memory path relative to scope root.",
				},
				"content": {
					Type:        "string",
					Description: "File content for create.",
				},
				"oldStr": {
					Type:        "string",
					Description: "Old string for str_replace.",
				},
				"newStr": {
					Type:        "string",
					Description: "New string for str_replace.",
				},
				"line": {
					Type:        "number",
					Description: "0-based line number for insert.",
				},
				"text": {
					Type:        "string",
					Description: "Text to insert for insert.",
				},
				"oldPath": {
					Type:        "string",
					Description: "Source path for rename.",
				},
				"newPath": {
					Type:        "string",
					Description: "Destination path for rename.",
				},
				"query": {
					Type:        "string",
					Description: "Query text for search.",
				},
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
				if err != nil {
					return "", err
				}
				return out, nil

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

			case "insert":
				line := asInt(args["line"])
				if err := r.memory.Insert(ctx, ensureWritableScope(scope), asString(args["path"]), line, asString(args["text"])); err != nil {
					return "", err
				}
				return "memory inserted", nil

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
				if len(items) == 0 {
					return "", nil
				}
				return strings.Join(items, "\n"), nil

			case "search":
				entries, err := r.memory.Search(ctx, scope, asString(args["query"]))
				if err != nil {
					return "", err
				}
				if len(entries) == 0 {
					return "No memory matches found.", nil
				}
				lines := make([]string, 0, len(entries))
				for _, entry := range entries {
					lines = append(lines, fmt.Sprintf("- [%s] %s", entry.Scope, entry.Path))
				}
				return strings.Join(lines, "\n"), nil
			}

			return "unsupported operation", nil
		},
	}
}

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
	if scope == ab.ScopeUser {
		return ab.ScopeUser
	}
	return ab.ScopeProject
}

func (r *agentRuntime) newSkillsTool() *ab.Tool {
	return &ab.Tool{
		Name:        "skills-operations",
		Description: "List, match, inspect, load, and unload backend skills.",
		Category:    ab.ToolCategoryPlanning,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"operation": {
					Type:        "string",
					Description: "One of: list, match, get, load, unload.",
					Enum:        []string{"list", "match", "get", "load", "unload"},
				},
				"skillName": {
					Type:        "string",
					Description: "Skill name for get/load/unload.",
				},
				"query": {
					Type:        "string",
					Description: "Text to match skill triggers against.",
				},
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
				for _, skill := range matched {
					lines = append(lines, fmt.Sprintf("- %s: %s", skill.Name, skill.Meta.Description))
				}
				return "Matched skills:\n" + strings.Join(lines, "\n"), nil

			case "get":
				if skillName == "" {
					return "skillName is required for get", nil
				}
				skill, err := r.skills.Get(ctx, skillName)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Skill %s (%s)\n\n%s", skill.Name, skill.Meta.Description, strings.TrimSpace(skill.Content)), nil

			case "load":
				if skillName == "" {
					return "skillName is required for load", nil
				}
				skill, err := r.skills.Load(ctx, skillName)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Loaded skill %s\n\n%s", skill.Name, strings.TrimSpace(skill.Content)), nil

			case "unload":
				if skillName == "" {
					return "skillName is required for unload", nil
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

func (r *agentRuntime) newSpawnRunnerTool() *ab.Tool {
	return &ab.Tool{
		Name:        "spawn-runner",
		Description: "Spawn a parallel runner for an autonomous subtask.",
		Category:    ab.ToolCategoryCompute,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"tier": {Type: "string", Description: "Runner tier: nano or mini."},
				"task": {Type: "string", Description: "Self-contained task for the runner."},
				"resourceBundle": {
					Type:        "array",
					Description: "Optional resources for the runner.",
					Items: &ab.ToolParam{
						Type: "object",
						Properties: map[string]ab.ToolParam{
							"id":          {Type: "string"},
							"type":        {Type: "string"},
							"description": {Type: "string"},
						},
					},
				},
			},
			Required: []string{"tier", "task"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			tier := strings.ToLower(strings.TrimSpace(asString(args["tier"])))
			if tier == "" {
				tier = string(ab.RunnerTierMini)
			}

			runner := ab.Runner{
				Tier:           parseRunnerTier(tier),
				Task:           strings.TrimSpace(asString(args["task"])),
				ResourceBundle: parseResourceBundle(args["resourceBundle"]),
			}
			if runner.Task == "" {
				return "", fmt.Errorf("runner task is required")
			}

			threadID, err := r.threads.Spawn(ctx, runner)
			if err != nil {
				return "", err
			}

			payload, _ := json.Marshal(map[string]any{
				"threadId": threadID,
				"tier":     runner.Tier,
				"task":     runner.Task,
				"status":   ab.ObjectiveStatusPending,
			})
			return string(payload), nil
		},
	}
}

type checkpointStore struct {
	nextID atomic.Uint64
}

func (s *checkpointStore) Create(label string) string {
	id := s.nextID.Add(1)
	return fmt.Sprintf("cp_%d_%s", id, slugify(label))
}

type inMemoryThreadProvider struct {
	runtime *agentRuntime
	mu      sync.Mutex
	nextID  atomic.Uint64
	threads map[string]*ab.Thread
	runners map[string]*RunnerSummary
	wg      sync.WaitGroup
}

func newInMemoryThreadProvider(runtime *agentRuntime) *inMemoryThreadProvider {
	return &inMemoryThreadProvider{
		runtime: runtime,
		threads: make(map[string]*ab.Thread),
		runners: make(map[string]*RunnerSummary),
	}
}

func (p *inMemoryThreadProvider) Spawn(_ context.Context, r ab.Runner) (string, error) {
	id := fmt.Sprintf("th_runner_%d", p.nextID.Add(1))
	summary := &RunnerSummary{
		ID:     id,
		Tier:   string(r.Tier),
		Task:   r.Task,
		Status: string(ab.ObjectiveStatusPending),
		Model:  p.runtime.model,
	}

	p.mu.Lock()
	p.threads[id] = &ab.Thread{ID: id, Status: ab.ThreadStatusActive}
	p.runners[id] = summary
	p.mu.Unlock()
	p.runtime.events.Publish(ab.Event{
		Type:   ab.EventExecutableUpdated,
		Source: id,
		Payload: map[string]any{
			"thread_id": id,
			"tier":      summary.Tier,
			"task":      summary.Task,
			"status":    summary.Status,
			"model":     summary.Model,
		},
	})
	log.Printf("runner.spawn chat_id=%d run_id=%s mode=%s thread_id=%s tier=%s task=%q", p.runtime.logContext.ChatID, p.runtime.logContext.RunID, p.runtime.logContext.Mode, id, summary.Tier, previewText(summary.Task, 120))

	p.wg.Add(1)
	go p.run(id, r)

	return id, nil
}

func (p *inMemoryThreadProvider) run(threadID string, r ab.Runner) {
	defer p.wg.Done()
	startedAt := time.Now()

	p.mu.Lock()
	if summary, ok := p.runners[threadID]; ok {
		summary.Status = "running"
		p.runtime.events.Publish(ab.Event{
			Type:   ab.EventExecutableUpdated,
			Source: threadID,
			Payload: map[string]any{
				"thread_id": threadID,
				"tier":      summary.Tier,
				"task":      summary.Task,
				"status":    summary.Status,
				"model":     summary.Model,
			},
		})
	}
	p.mu.Unlock()
	log.Printf("runner.start chat_id=%d run_id=%s mode=%s thread_id=%s tier=%s", p.runtime.logContext.ChatID, p.runtime.logContext.RunID, p.runtime.logContext.Mode, threadID, r.Tier)

	result, err := p.runtime.executeRunner(r)
	p.mu.Lock()
	defer p.mu.Unlock()

	summary := p.runners[threadID]
	thread := p.threads[threadID]
	if err != nil {
		summary.Status = string(ab.ObjectiveStatusFailure)
		summary.Result = err.Error()
		thread.Status = ab.ThreadStatusFailed
		log.Printf("runner.failed chat_id=%d run_id=%s mode=%s thread_id=%s duration=%s error=%q", p.runtime.logContext.ChatID, p.runtime.logContext.RunID, p.runtime.logContext.Mode, threadID, time.Since(startedAt).Round(time.Millisecond), previewText(err.Error(), 160))
		p.runtime.events.Publish(ab.Event{Type: ab.EventRunnerFailed, Source: threadID, Payload: map[string]any{"thread_id": threadID, "error": err.Error()}})
		return
	}

	summary.Status = string(ab.ObjectiveStatusSuccess)
	summary.Result = result
	thread.Status = ab.ThreadStatusCompleted
	log.Printf("runner.completed chat_id=%d run_id=%s mode=%s thread_id=%s duration=%s result=%q", p.runtime.logContext.ChatID, p.runtime.logContext.RunID, p.runtime.logContext.Mode, threadID, time.Since(startedAt).Round(time.Millisecond), previewText(result, 160))
	p.runtime.events.Publish(ab.Event{Type: ab.EventRunnerCompleted, Source: threadID, Payload: map[string]any{"thread_id": threadID, "result": result}})
}

func (p *inMemoryThreadProvider) Wait(timeout time.Duration) []RunnerSummary {
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	if timeout > 0 {
		select {
		case <-done:
		case <-time.After(timeout):
		}
	} else {
		<-done
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]RunnerSummary, 0, len(p.runners))
	for _, runner := range p.runners {
		out = append(out, *runner)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (p *inMemoryThreadProvider) Snapshot() []RunnerSummary {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]RunnerSummary, 0, len(p.runners))
	for _, runner := range p.runners {
		out = append(out, *runner)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (p *inMemoryThreadProvider) Archive(_ context.Context, threadID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	thread, ok := p.threads[threadID]
	if !ok {
		return fmt.Errorf("thread not found: %s", threadID)
	}
	thread.Status = ab.ThreadStatusArchived
	return nil
}

func (p *inMemoryThreadProvider) SendMessage(_ context.Context, _ ab.Message) error {
	return nil
}

func (p *inMemoryThreadProvider) ReportStatus(_ context.Context, parentThreadID string, report ab.ObjectiveReport) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	runner, ok := p.runners[parentThreadID]
	if ok {
		runner.Status = string(report.Status)
		runner.Result = report.Summary
	}
	return nil
}

func (p *inMemoryThreadProvider) Get(_ context.Context, threadID string) (*ab.Thread, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	thread, ok := p.threads[threadID]
	if !ok {
		return nil, fmt.Errorf("thread not found: %s", threadID)
	}
	copy := *thread
	return &copy, nil
}

func (r *agentRuntime) executeRunner(runner ab.Runner) (string, error) {
	tools := r.buildRunnerToolRegistry()
	resourceHint := ""
	if len(runner.ResourceBundle) > 0 {
		parts := make([]string, 0, len(runner.ResourceBundle))
		for _, ref := range runner.ResourceBundle {
			parts = append(parts, fmt.Sprintf("- %s (%s): %s", ref.ID, ref.Type, ref.Description))
		}
		resourceHint = "\nResources:\n" + strings.Join(parts, "\n")
	}

	result, err := ab.RunAgentLoop(context.Background(), ab.AgentLoopConfig{
		Provider: providerOrEcho(r.provider, r.model),
		Model:    r.model,
		Tools:    tools,
		MaxTurns: 4,
		SystemPrompt: strings.TrimSpace(fmt.Sprintf(
			"You are a spawned runner. Execute the task directly using available tools when needed. Return a concise summary of what you actually did.%s\nTier: %s",
			resourceHint,
			runner.Tier,
		)),
	}, []ab.ChatMessage{{Role: ab.RoleUser, Content: runner.Task}})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.FinalContent), nil
}

func providerOrEcho(provider ab.LLMProvider, model string) ab.LLMProvider {
	if provider != nil {
		return provider
	}
	return &EchoLLM{Model: model}
}

func parseRunnerTier(value string) ab.RunnerTier {
	if value == string(ab.RunnerTierNano) {
		return ab.RunnerTierNano
	}
	return ab.RunnerTierMini
}

func parseResourceBundle(value any) []ab.ResourceRef {
	rawItems, ok := value.([]any)
	if !ok {
		return nil
	}

	items := make([]ab.ResourceRef, 0, len(rawItems))
	for _, item := range rawItems {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		items = append(items, ab.ResourceRef{
			ID:          asString(obj["id"]),
			Type:        asString(obj["type"]),
			Description: asString(obj["description"]),
		})
	}
	return items
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

func asInt(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float32:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
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
