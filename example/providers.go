package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	ab "github.com/everfaz/autobuild-sdk"
)

// ═══════════════════════════════════════════════════════════════════════
// MemoryStore — in-memory MemoryProvider implementation
// ═══════════════════════════════════════════════════════════════════════

type MemoryStore struct {
	mu    sync.RWMutex
	files map[ab.Scope]map[string]string // scope → path → content
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		files: map[ab.Scope]map[string]string{
			ab.ScopeUser:    {},
			ab.ScopeProject: {},
		},
	}
}

func (m *MemoryStore) View(_ context.Context, scope ab.Scope, path string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if path == "/" {
		var listing string
		for p := range m.files[scope] {
			listing += p + "\n"
		}
		return listing, nil
	}
	c, ok := m.files[scope][path]
	if !ok {
		return "", fmt.Errorf("not found: %s/%s", scope, path)
	}
	return c, nil
}

func (m *MemoryStore) Create(_ context.Context, scope ab.Scope, path, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.files[scope][path]; exists {
		return fmt.Errorf("already exists: %s/%s", scope, path)
	}
	m.files[scope][path] = content
	return nil
}

func (m *MemoryStore) StrReplace(_ context.Context, scope ab.Scope, path, oldStr, newStr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.files[scope][path]
	if !ok {
		return fmt.Errorf("not found: %s/%s", scope, path)
	}
	m.files[scope][path] = replaceOnce(c, oldStr, newStr)
	return nil
}

func (m *MemoryStore) Insert(_ context.Context, scope ab.Scope, path string, line int, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.files[scope][path]
	lines := splitLines(c)
	if line > len(lines) {
		line = len(lines)
	}
	lines = append(lines[:line], append([]string{text}, lines[line:]...)...)
	m.files[scope][path] = joinLines(lines)
	return nil
}

func (m *MemoryStore) Delete(_ context.Context, scope ab.Scope, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files[scope], path)
	return nil
}

func (m *MemoryStore) Rename(_ context.Context, scope ab.Scope, oldPath, newPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.files[scope][oldPath]
	if !ok {
		return fmt.Errorf("not found: %s/%s", scope, oldPath)
	}
	m.files[scope][newPath] = c
	delete(m.files[scope], oldPath)
	return nil
}

func (m *MemoryStore) List(_ context.Context, scope ab.Scope, _ string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var paths []string
	for p := range m.files[scope] {
		paths = append(paths, p)
	}
	return paths, nil
}

func (m *MemoryStore) Search(_ context.Context, scope ab.Scope, query string) ([]ab.MemoryEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var results []ab.MemoryEntry
	for p, c := range m.files[scope] {
		if contains(c, query) || contains(p, query) {
			results = append(results, ab.MemoryEntry{Path: p, Scope: scope, Content: c})
		}
	}
	return results, nil
}

// ═══════════════════════════════════════════════════════════════════════
// LocalSandbox — stub SandboxDriver (prints commands)
// ═══════════════════════════════════════════════════════════════════════

type LocalSandbox struct {
	nextID int
}

func (s *LocalSandbox) Create(_ context.Context, cfg ab.SandboxConfig) (string, error) {
	s.nextID++
	return fmt.Sprintf("sbx_%d", s.nextID), nil
}

func (s *LocalSandbox) Exec(_ context.Context, id, command string) (ab.ExecResult, error) {
	return ab.ExecResult{Stdout: fmt.Sprintf("[%s] $ %s\n(simulated)", id, command), ExitCode: 0}, nil
}

func (s *LocalSandbox) WriteFile(_ context.Context, _, path, _ string) error {
	fmt.Printf("    [sandbox] write %s\n", path)
	return nil
}

func (s *LocalSandbox) ReadFile(_ context.Context, _, path string) (string, error) {
	return fmt.Sprintf("(contents of %s)", path), nil
}

func (s *LocalSandbox) Destroy(_ context.Context, _ string) error { return nil }

func (s *LocalSandbox) Status(_ context.Context, _ string) (ab.SandboxStatus, error) {
	return ab.SandboxStatusRunning, nil
}

func (s *LocalSandbox) IP(_ context.Context, _ string) (string, error) {
	return "127.0.0.1", nil
}

// ═══════════════════════════════════════════════════════════════════════
// SkillStore — in-memory SkillProvider
// ═══════════════════════════════════════════════════════════════════════

type SkillStore struct {
	mu     sync.RWMutex
	skills map[string]*ab.Skill
	loaded map[string]bool
}

func NewSkillStore() *SkillStore {
	return &SkillStore{skills: make(map[string]*ab.Skill), loaded: make(map[string]bool)}
}

func (s *SkillStore) Add(sk *ab.Skill) { s.skills[sk.Name] = sk }

func (s *SkillStore) Load(_ context.Context, name string) (*ab.Skill, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, ok := s.skills[name]
	if !ok {
		return nil, fmt.Errorf("skill not found: %s", name)
	}
	s.loaded[name] = true
	return sk, nil
}

func (s *SkillStore) Unload(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.loaded, name)
	return nil
}

func (s *SkillStore) Match(_ context.Context, text string) ([]*ab.Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matched []*ab.Skill
	for _, sk := range s.skills {
		if sk.MatchesTrigger(text) {
			matched = append(matched, sk)
		}
	}
	return matched, nil
}

func (s *SkillStore) List(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var names []string
	for n := range s.skills {
		names = append(names, n)
	}
	return names, nil
}

func (s *SkillStore) Get(_ context.Context, name string) (*ab.Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sk, ok := s.skills[name]
	if !ok {
		return nil, fmt.Errorf("skill not found: %s", name)
	}
	return sk, nil
}

// LoadedSkills returns the content of all currently loaded skills.
func (s *SkillStore) LoadedSkills() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var parts []string
	for name := range s.loaded {
		if sk, ok := s.skills[name]; ok {
			parts = append(parts, sk.Content)
		}
	}
	return joinLines(parts)
}

// ═══════════════════════════════════════════════════════════════════════
// ThreadStore — in-memory ThreadProvider
// ═══════════════════════════════════════════════════════════════════════

type ThreadStore struct {
	mu      sync.Mutex
	threads map[string]*ab.Thread
	nextID  int
}

func NewThreadStore() *ThreadStore {
	return &ThreadStore{threads: make(map[string]*ab.Thread)}
}

func (t *ThreadStore) Spawn(_ context.Context, r ab.Runner) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	id := fmt.Sprintf("th_%03d", t.nextID)
	t.threads[id] = &ab.Thread{
		ID:     id,
		ModeID: string(r.Tier),
		Status: ab.ThreadStatusActive,
	}
	return id, nil
}

func (t *ThreadStore) Archive(_ context.Context, threadID string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if th, ok := t.threads[threadID]; ok {
		th.Status = ab.ThreadStatusArchived
	}
	return nil
}

func (t *ThreadStore) SendMessage(_ context.Context, msg ab.Message) error {
	fmt.Printf("    [thread-msg] %s → %s: %s\n", msg.FromThreadID, msg.ToThreadID, msg.Content)
	return nil
}

func (t *ThreadStore) ReportStatus(_ context.Context, parentID string, report ab.ObjectiveReport) error {
	fmt.Printf("    [report] → %s: status=%s summary=%s\n", parentID, report.Status, report.Summary)
	return nil
}

func (t *ThreadStore) Get(_ context.Context, threadID string) (*ab.Thread, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	th, ok := t.threads[threadID]
	if !ok {
		return nil, fmt.Errorf("thread not found: %s", threadID)
	}
	return th, nil
}

// ═══════════════════════════════════════════════════════════════════════
// CheckpointStore — in-memory CheckpointProvider
// ═══════════════════════════════════════════════════════════════════════

type CheckpointStore struct {
	mu          sync.Mutex
	checkpoints []*ab.Checkpoint
	nextID      int
}

func (c *CheckpointStore) Create(_ context.Context, description string) (*ab.Checkpoint, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	cp := &ab.Checkpoint{
		ID:          fmt.Sprintf("cp_%03d", c.nextID),
		Description: description,
		CreatedAt:   time.Now(),
	}
	c.checkpoints = append(c.checkpoints, cp)
	fmt.Printf("    [checkpoint] %s: %s\n", cp.ID, description)
	return cp, nil
}

func (c *CheckpointStore) Restore(_ context.Context, checkpointID string) error {
	fmt.Printf("    [checkpoint] restoring %s\n", checkpointID)
	return nil
}

func (c *CheckpointStore) List(_ context.Context) ([]*ab.Checkpoint, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*ab.Checkpoint, len(c.checkpoints))
	copy(out, c.checkpoints)
	return out, nil
}

// ═══════════════════════════════════════════════════════════════════════
// ModeStore — in-memory ModeProvider with builtins
// ═══════════════════════════════════════════════════════════════════════

type ModeStore struct {
	modes map[string]*ab.Mode
}

// NewModeStoreFromParsed creates a ModeStore from modes parsed from system.md files.
func NewModeStoreFromParsed(parsed []*ab.Mode) *ModeStore {
	store := &ModeStore{modes: make(map[string]*ab.Mode)}
	for _, m := range parsed {
		store.modes[m.ID] = m
	}
	return store
}

func (m *ModeStore) Get(_ context.Context, modeID string) (*ab.Mode, error) {
	mode, ok := m.modes[modeID]
	if !ok {
		return nil, fmt.Errorf("mode not found: %s", modeID)
	}
	return mode, nil
}

func (m *ModeStore) List(_ context.Context) ([]*ab.Mode, error) {
	var out []*ab.Mode
	for _, mode := range m.modes {
		out = append(out, mode)
	}
	return out, nil
}

func (m *ModeStore) Create(_ context.Context, mode ab.Mode) (*ab.Mode, error) {
	m.modes[mode.ID] = &mode
	return &mode, nil
}

func (m *ModeStore) BuiltinModes() []*ab.Mode {
	var builtins []*ab.Mode
	for _, mode := range m.modes {
		switch mode.BaseModeID {
		case ab.BaseModeBalanced, ab.BaseModeAnalyst, ab.BaseModeDeepWork:
			if mode.PromptStrategy == "" { // base modes have no strategy override
				builtins = append(builtins, mode)
			}
		}
	}
	return builtins
}

// ═══════════════════════════════════════════════════════════════════════
// PlanStore / TaskStore — minimal stubs
// ═══════════════════════════════════════════════════════════════════════

type PlanStore struct{}

func (p *PlanStore) Propose(_ context.Context, plan ab.Plan) (*ab.Plan, error) {
	plan.ID = "plan_001"
	return &plan, nil
}
func (p *PlanStore) Approve(_ context.Context, _ string, _ bool) error { return nil }
func (p *PlanStore) UpdateStatus(_ context.Context, _, _ string, _ ab.ExecutableStatus, _ string) error {
	return nil
}
func (p *PlanStore) GetPlan(_ context.Context, _ string) (*ab.Plan, error) { return nil, nil }

type TaskStore struct{}

func (t *TaskStore) Create(_ context.Context, task ab.Task) (*ab.Task, error) {
	task.ID = "tsk_001"
	return &task, nil
}
func (t *TaskStore) List(_ context.Context) ([]*ab.Task, error)          { return nil, nil }
func (t *TaskStore) Get(_ context.Context, _ string) (*ab.Task, error)   { return nil, nil }
func (t *TaskStore) Update(_ context.Context, task ab.Task) (*ab.Task, error) { return &task, nil }
func (t *TaskStore) Delete(_ context.Context, _ string) error            { return nil }
func (t *TaskStore) Run(_ context.Context, _ string, _ string) error     { return nil }

// ═══════════════════════════════════════════════════════════════════════
// SimulatedLLM — behaves like a real agent that follows the workflow
//
// This simulates what a real LLM (Claude, GPT, etc.) would do when given
// the system prompt + tools. It follows the 6-phase workflow:
//   Turn 1: Orientation — reads memory + explores artifacts
//   Turn 2: Preparation — creates checkpoint
//   Turn 3: Execution — calls computer-ops or spawns runner
//   Turn 4: Verification — reads result
//   Turn 5: Closure — creates final checkpoint + responds
// ═══════════════════════════════════════════════════════════════════════

type SimulatedLLM struct {
	callCount int
	model     string
}

func NewSimulatedLLM(model string) *SimulatedLLM {
	return &SimulatedLLM{model: model}
}

func (s *SimulatedLLM) Chat(_ context.Context, req ab.ChatRequest) (*ab.ChatResponse, error) {
	s.callCount++
	model := s.model
	if req.Model != "" {
		model = req.Model
	}

	// Build a tool name lookup
	toolNames := make(map[string]bool)
	for _, t := range req.Tools {
		toolNames[t.Function.Name] = true
	}

	usage := ab.TokenUsage{PromptTokens: 300, CompletionTokens: 80, TotalTokens: 380}

	switch s.callCount {
	case 1:
		// Phase 0 — Orientation: read memory + explore artifacts (parallel)
		var calls []ab.ToolCallEntry
		if toolNames["memory"] {
			calls = append(calls, ab.ToolCallEntry{
				ID: "call_001", Name: "memory",
				Arguments: `{"operation":"view","path":"/","scope":"project"}`,
			})
		}
		if toolNames["explore-artifacts"] {
			calls = append(calls, ab.ToolCallEntry{
				ID: "call_002", Name: "explore-artifacts",
				Arguments: `{}`,
			})
		}
		if len(calls) == 0 {
			// No orientation tools, skip to execution
			s.callCount = 2
			return s.Chat(nil, req)
		}
		return &ab.ChatResponse{ToolCalls: calls, FinishReason: "tool_calls", Usage: usage, Model: model}, nil

	case 2:
		// Phase 2 — Preparation: create checkpoint before work
		return &ab.ChatResponse{
			ToolCalls: []ab.ToolCallEntry{{
				ID: "call_003", Name: "create-checkpoint",
				Arguments: `{"description":"Before implementing auth feature"}`,
			}},
			FinishReason: "tool_calls", Usage: usage, Model: model,
		}, nil

	case 3:
		// Phase 3 — Execution: the LLM decides to run a command
		if toolNames["computer-ops"] {
			return &ab.ChatResponse{
				ToolCalls: []ab.ToolCallEntry{{
					ID: "call_004", Name: "computer-ops",
					Arguments: `{"id":"cmp_001","command":"go build ./... && go test ./..."}`,
				}},
				FinishReason: "tool_calls", Usage: usage, Model: model,
			}, nil
		}
		// If no computer-ops, try spawn-runner
		if toolNames["spawn-runner"] {
			return &ab.ChatResponse{
				ToolCalls: []ab.ToolCallEntry{{
					ID: "call_004", Name: "spawn-runner",
					Arguments: `{"tier":"mini","task":"Implement users table migration"}`,
				}},
				FinishReason: "tool_calls", Usage: usage, Model: model,
			}, nil
		}
		s.callCount = 4
		return s.Chat(nil, req)

	case 4:
		// Phase 3 continued — LLM decides to create a document
		if toolNames["document-operations"] {
			return &ab.ChatResponse{
				ToolCalls: []ab.ToolCallEntry{{
					ID: "call_005", Name: "document-operations",
					Arguments: `{"operation":"write","title":"Auth Implementation Notes","content":"## Auth System\n\nJWT-based authentication..."}`,
				}},
				FinishReason: "tool_calls", Usage: usage, Model: model,
			}, nil
		}
		s.callCount = 5
		return s.Chat(nil, req)

	default:
		// Phase 5 — Closure: final response
		return &ab.ChatResponse{
			Content: fmt.Sprintf("I've completed the auth feature implementation:\n\n"+
				"1. ✓ Read project context from memory\n"+
				"2. ✓ Created safety checkpoint\n"+
				"3. ✓ Built and tested the code\n"+
				"4. ✓ Created documentation\n\n"+
				"The build passes and tests are green. Want me to create a PR or "+
				"add integration tests next?"),
			FinishReason: "stop",
			Usage:        ab.TokenUsage{PromptTokens: 400, CompletionTokens: 150, TotalTokens: 550},
			Model:        model,
		}, nil
	}
}

// ═══════════════════════════════════════════════════════════════════════
// StubRouter — routes models to different LLM providers
// ═══════════════════════════════════════════════════════════════════════

type StubRouter struct {
	providers map[string]ab.LLMProvider
	fallback  ab.LLMProvider
}

func NewStubRouter(fallback ab.LLMProvider) *StubRouter {
	return &StubRouter{providers: make(map[string]ab.LLMProvider), fallback: fallback}
}

func (r *StubRouter) Register(modelPrefix string, provider ab.LLMProvider) {
	r.providers[modelPrefix] = provider
}

func (r *StubRouter) Route(model string) (ab.LLMProvider, error) {
	// Simple prefix matching: "claude-*" → anthropic, "gpt-*" → openai
	for prefix, provider := range r.providers {
		if len(model) >= len(prefix) && model[:len(prefix)] == prefix {
			return provider, nil
		}
	}
	return r.fallback, nil
}

// ═══════════════════════════════════════════════════════════════════════
// Tool registry builder
// ═══════════════════════════════════════════════════════════════════════

func buildToolRegistry() *ab.ToolRegistry {
	reg := ab.NewToolRegistry()

	reg.Register(&ab.Tool{
		Name: "computer-ops", Description: "Execute shell commands in a sandbox",
		Category: ab.ToolCategoryCompute,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"id":      {Type: "string", Description: "Sandbox ID"},
				"command": {Type: "string", Description: "Shell command"},
			},
			Required: []string{"id", "command"},
		},
		Execute: func(ctx context.Context, sandboxID string, args map[string]any) (string, error) {
			return fmt.Sprintf("executed in %s: %v", sandboxID, args["command"]), nil
		},
	})

	reg.Register(&ab.Tool{
		Name: "memory", Description: "Read/write persistent memory",
		Category: ab.ToolCategoryMemory,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"operation": {Type: "string", Description: "view, create, str_replace, etc."},
				"path":      {Type: "string", Description: "File path"},
				"scope":     {Type: "string", Description: "user or project"},
			},
			Required: []string{"operation", "path"},
		},
		Execute: func(ctx context.Context, sandboxID string, args map[string]any) (string, error) {
			op, _ := args["operation"].(string)
			path, _ := args["path"].(string)
			scope, _ := args["scope"].(string)
			return fmt.Sprintf("memory %s %s/%s → ok", op, scope, path), nil
		},
	})

	reg.Register(&ab.Tool{
		Name: "explore-artifacts", Description: "Discover and inspect project artifacts",
		Category: ab.ToolCategoryWorkspace,
		Execute: func(ctx context.Context, sandboxID string, args map[string]any) (string, error) {
			return `[{"id":"art_001","type":"document","title":"Project Spec"},{"id":"art_002","type":"workbook","title":"Sales Data"}]`, nil
		},
	})

	reg.Register(&ab.Tool{
		Name: "web-operations", Description: "Web search, fetch URLs, crawl domains",
		Category: ab.ToolCategoryWeb,
		Execute: func(ctx context.Context, sandboxID string, args map[string]any) (string, error) {
			op, _ := args["operation"].(string)
			q, _ := args["query"].(string)
			return fmt.Sprintf("web %s: %s → 3 results", op, q), nil
		},
	})

	reg.Register(&ab.Tool{
		Name: "document-operations", Description: "Create and edit markdown documents",
		Category: ab.ToolCategoryWorkspace,
		Execute: func(ctx context.Context, sandboxID string, args map[string]any) (string, error) {
			op, _ := args["operation"].(string)
			title, _ := args["title"].(string)
			return fmt.Sprintf("document %s: %s → ok", op, title), nil
		},
	})

	reg.Register(&ab.Tool{
		Name: "create-checkpoint", Description: "Create a project checkpoint for rollback",
		Category: ab.ToolCategoryWorkspace,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"description": {Type: "string", Description: "What this checkpoint captures"},
			},
			Required: []string{"description"},
		},
		Execute: func(ctx context.Context, sandboxID string, args map[string]any) (string, error) {
			desc, _ := args["description"].(string)
			return fmt.Sprintf("checkpoint created: %s", desc), nil
		},
	})

	reg.Register(&ab.Tool{
		Name: "spawn-runner", Description: "Launch a subthread for parallel work",
		Category: ab.ToolCategoryPlanning,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"tier": {Type: "string", Description: "nano or mini"},
				"task": {Type: "string", Description: "Task description"},
			},
			Required: []string{"tier", "task"},
		},
		Execute: func(ctx context.Context, sandboxID string, args map[string]any) (string, error) {
			tier, _ := args["tier"].(string)
			task, _ := args["task"].(string)
			return fmt.Sprintf("runner spawned: tier=%s, task=%s → thread th_auto_001", tier, task), nil
		},
	})

	return reg
}

// ═══════════════════════════════════════════════════════════════════════
// String helpers
// ═══════════════════════════════════════════════════════════════════════

func contains(s, substr string) bool {
	return len(substr) > 0 && len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func replaceOnce(s, old, new_ string) string {
	i := 0
	for ; i <= len(s)-len(old); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new_ + s[i+len(old):]
		}
	}
	return s
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func joinLines(lines []string) string {
	result := ""
	for i, l := range lines {
		if i > 0 {
			result += "\n"
		}
		result += l
	}
	return result
}
