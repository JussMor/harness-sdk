// Package autobuild — AgentTool v3.
//
// Mirrors Claude Code's AgentTool faithfully but stays agnostic:
//   - no provider lock-in
//   - no language defaults
//   - no Claude-specific frontmatter quirks
//   - layout supports BOTH `<root>/<name>.md` AND `<root>/<name>/AGENT.md`
//
// An Agent is a focused, isolated loop the model spawns to handle a
// self-contained task. The model invokes the Agent tool with a description
// and a prompt; the SDK runs an inner AgentLoop with the agent's own tool
// allowlist, model, and max-turn cap, then returns the final assistant text.
//
// Parallel execution: when the LLM emits multiple Agent tool_use blocks in
// the same assistant message, the dispatcher (sdk/dispatch.go) runs them
// concurrently as long as the AgentTool's IsConcurrencySafe predicate
// returns true (it does — agents are independent by construction).
//
// Cross-reference: see /memories/session/agent-tool-v3-spec.md for the
// full mapping from Claude Code source.
package autobuild

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ─── Agent definition ─────────────────────────────────────────────────────

// AgentSourceKind identifies where an Agent definition was loaded from.
type AgentSourceKind string

const (
	AgentSourceBundled    AgentSourceKind = "bundled"
	AgentSourceFilesystem AgentSourceKind = "filesystem"
	AgentSourceRemote     AgentSourceKind = "remote"
)

// Agent is a single subagent definition resolved at startup.
//
// The Body is the system prompt for the spawned loop. Argument substitution
// and ${SESSION_ID} / ${AGENT_DIR} expansion happen at invoke time.
type Agent struct {
	// Identity
	Type        string // canonical name; matches `subagent_type` in tool input
	DisplayName string
	Description string // short — drives the agent listing's whenToUse text
	Color       string

	// Tool gating
	Tools           []string // allowlist; empty = inherit all parent tools
	DisallowedTools []string // denylist; combined with allowlist if both set

	// Behaviour
	Model       string // "" or "inherit" → use parent model
	Effort      string // "low" | "medium" | "high" | numeric
	MaxTurns    int    // 0 → use AgentToolConfig default
	Background  bool   // run async; caller is notified later
	InitialPrompt string // prepended to first user turn

	// Skills the agent should preload (list, comma- or space-separated)
	PreloadSkills []string

	// Storage
	Body    string // SYSTEM PROMPT body (after frontmatter stripping)
	BaseDir string // absolute path of the agent's dir (or file's dir for .md)

	// Provenance
	Source AgentSourceKind
}

// AgentResult is what an Agent returns to its parent (and to the LLM as
// the tool_result content). Mirrors the old SubagentResult shape so
// streaming consumers stay source-compatible.
type AgentResult struct {
	Type         string          `json:"type"`         // agent type that ran ("" for fork / ad-hoc)
	Description  string          `json:"description"`  // 3-5 word task summary the model gave
	Task         string          `json:"task"`         // full prompt the model gave
	Output       string          `json:"output"`       // final assistant text
	Turns        int             `json:"turns"`
	Usage        TokenUsage      `json:"usage"`
	StopReason   string          `json:"stop_reason"`
	Duration     time.Duration   `json:"duration_ms"`
	Error        error           `json:"-"`
	Trace        []ReasoningStep `json:"trace,omitempty"`
	Model        string          `json:"model,omitempty"`
	SystemPrompt string          `json:"system_prompt,omitempty"`
	Background   bool            `json:"background,omitempty"`
}

// AgentSource is a pluggable source of agent definitions.
type AgentSource interface {
	SourceName() string
	List(ctx context.Context) ([]*Agent, error)
}

// ─── Filesystem source ────────────────────────────────────────────────────

// FilesystemAgentSource scans agent definitions from a directory. Supports
// BOTH layouts (matching Claude Code's loadAgentsDir):
//   - <Root>/<name>.md         (file form)
//   - <Root>/<name>/AGENT.md   (directory form)
type FilesystemAgentSource struct {
	Root  string
	Label string
	Kind  AgentSourceKind

	mu     sync.Mutex
	cached []*Agent
	loaded bool
}

func (f *FilesystemAgentSource) SourceName() string {
	if f.Label != "" {
		return f.Label
	}
	return "fs:" + f.Root
}

func (f *FilesystemAgentSource) Reload() {
	f.mu.Lock()
	f.loaded = false
	f.cached = nil
	f.mu.Unlock()
}

func (f *FilesystemAgentSource) List(_ context.Context) ([]*Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loaded {
		return f.cached, nil
	}

	kind := f.Kind
	if kind == "" {
		kind = AgentSourceFilesystem
	}

	entries, err := os.ReadDir(f.Root)
	if err != nil {
		if os.IsNotExist(err) {
			f.loaded = true
			return nil, nil
		}
		return nil, fmt.Errorf("agents dir %s: %w", f.Root, err)
	}

	var out []*Agent
	for _, e := range entries {
		var name, baseDir, mdPath string
		switch {
		case e.IsDir():
			name = e.Name()
			baseDir = filepath.Join(f.Root, name)
			mdPath = filepath.Join(baseDir, "AGENT.md")
		case strings.HasSuffix(e.Name(), ".md"):
			name = strings.TrimSuffix(e.Name(), ".md")
			baseDir = f.Root
			mdPath = filepath.Join(f.Root, e.Name())
		default:
			continue
		}
		raw, err := os.ReadFile(mdPath)
		if err != nil {
			continue
		}
		ag, err := parseAgentMarkdown(name, baseDir, string(raw), kind)
		if err != nil {
			continue
		}
		out = append(out, ag)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	f.cached = out
	f.loaded = true
	return out, nil
}

func parseAgentMarkdown(name, baseDir, raw string, kind AgentSourceKind) (*Agent, error) {
	fields, lists, body, err := parseFrontmatter(raw)
	if err != nil {
		// No frontmatter: whole file is the system prompt.
		return &Agent{
			Type:    name,
			Body:    raw,
			BaseDir: baseDir,
			Source:  kind,
		}, nil
	}

	stripQuotes := func(s string) string {
		if len(s) >= 2 {
			if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
				return s[1 : len(s)-1]
			}
		}
		return s
	}

	splitCSV := func(v string) []string {
		var out []string
		for _, p := range strings.Split(stripQuotes(v), ",") {
			if t := strings.TrimSpace(p); t != "" {
				out = append(out, t)
			}
		}
		return out
	}

	tools := lists["tools"]
	if v, ok := fields["tools"]; ok && len(tools) == 0 {
		tools = splitCSV(v)
	}
	disallowed := lists["disallowedTools"]
	if v, ok := fields["disallowedTools"]; ok && len(disallowed) == 0 {
		disallowed = splitCSV(v)
	}
	skills := lists["skills"]
	if v, ok := fields["skills"]; ok && len(skills) == 0 {
		skills = splitCSV(v)
	}

	maxTurns := 0
	if v, ok := fields["maxTurns"]; ok {
		_, _ = fmt.Sscanf(stripQuotes(v), "%d", &maxTurns)
	}
	background := false
	if v, ok := fields["background"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "1", "on":
			background = true
		}
	}

	ag := &Agent{
		Type:            name,
		DisplayName:     stripQuotes(fields["name"]),
		Description:     stripQuotes(fields["description"]),
		Color:           stripQuotes(fields["color"]),
		Tools:           tools,
		DisallowedTools: disallowed,
		Model:           stripQuotes(fields["model"]),
		Effort:          stripQuotes(fields["effort"]),
		MaxTurns:        maxTurns,
		Background:      background,
		InitialPrompt:   stripQuotes(fields["initialPrompt"]),
		PreloadSkills:   skills,
		Body:            body,
		BaseDir:         baseDir,
		Source:          kind,
	}
	if ag.DisplayName == "" {
		ag.DisplayName = name
	}
	return ag, nil
}

// ─── Listing budget (mirrors SkillTool's listing budget pattern) ──────────

// FormatAgentsLine produces "- {type}: {description} (Tools: ...)" matching
// Claude Code's formatAgentLine().
func FormatAgentLine(a *Agent) string {
	return "- " + a.Type + ": " + a.Description + " (Tools: " + agentToolsDesc(a) + ")"
}

func agentToolsDesc(a *Agent) string {
	hasAllow := len(a.Tools) > 0
	hasDeny := len(a.DisallowedTools) > 0
	switch {
	case hasAllow && hasDeny:
		denied := make(map[string]bool, len(a.DisallowedTools))
		for _, d := range a.DisallowedTools {
			denied[d] = true
		}
		var out []string
		for _, t := range a.Tools {
			if !denied[t] {
				out = append(out, t)
			}
		}
		if len(out) == 0 {
			return "None"
		}
		return strings.Join(out, ", ")
	case hasAllow:
		return strings.Join(a.Tools, ", ")
	case hasDeny:
		return "All tools except " + strings.Join(a.DisallowedTools, ", ")
	default:
		return "All tools"
	}
}

// ─── Inner runner ─────────────────────────────────────────────────────────

// runAgentInner runs one Agent end-to-end and produces an AgentResult.
// The parent Engine supplies LLM, Memory, Tools, etc. The agent's
// Tools/DisallowedTools narrow the parent's registry.
func runAgentInner(ctx context.Context, parent *Engine, ag *Agent, description, prompt string, defaultMaxTurns int, defaultModel string) *AgentResult {
	start := time.Now()
	res := &AgentResult{
		Type:        ag.Type,
		Description: description,
		Task:        prompt,
		Background:  ag.Background,
	}

	maxTurns := ag.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}
	if maxTurns <= 0 {
		maxTurns = 10
	}

	model := ag.Model
	if model == "" || strings.EqualFold(model, "inherit") {
		model = defaultModel
	}

	// Build a narrowed tool registry (allow/deny) over the parent's tools.
	tools := narrowToolRegistry(parent.Tools, ag.Tools, ag.DisallowedTools)

	// Compose the system prompt: agent body + optional initial prompt prefix.
	systemPrompt := strings.TrimSpace(ag.Body)
	if ag.InitialPrompt != "" {
		systemPrompt = strings.TrimSpace(ag.InitialPrompt) + "\n\n" + systemPrompt
	}
	if systemPrompt == "" {
		// Fall back to a concise generic prompt so the spawned loop has
		// SOMETHING to anchor on.
		systemPrompt = "You are a focused agent. Complete the following task concisely and report your findings."
	}

	cfg := AgentLoopConfig{
		MaxTurns:     maxTurns,
		SystemPrompt: systemPrompt,
		Model:        model,
		Tools:        tools,
	}

	conv := NewConversation(fmt.Sprintf("agent-%s-%d", ag.Type, time.Now().UnixNano()))
	loopRes, err := RunAgentLoopWithEngine(ctx, parent, "", cfg, append(
		conv.Messages,
		ChatMessage{Role: RoleUser, Content: prompt},
	))
	res.Duration = time.Since(start)
	if err != nil {
		res.Error = err
		return res
	}
	res.Output = loopRes.FinalContent
	res.Turns = loopRes.TotalTurns
	res.Usage = loopRes.TotalUsage
	res.StopReason = loopRes.StopReason
	res.Trace = loopRes.ReasoningTrace
	res.Model = model
	res.SystemPrompt = systemPrompt
	return res
}

// narrowToolRegistry returns a new registry containing only the parent's
// tools that pass the allow/deny filters. nil/empty allow → all tools.
func narrowToolRegistry(parent *ToolRegistry, allow, deny []string) *ToolRegistry {
	if parent == nil {
		return nil
	}
	allowSet := make(map[string]bool, len(allow))
	for _, t := range allow {
		allowSet[t] = true
	}
	denySet := make(map[string]bool, len(deny))
	for _, t := range deny {
		denySet[t] = true
	}
	out := NewToolRegistry()
	for _, t := range parent.List() {
		if denySet[t.Name] {
			continue
		}
		if len(allowSet) > 0 && !allowSet[t.Name] {
			continue
		}
		out.Register(t)
	}
	return out
}

// RunAgentsInParallel runs N Agent invocations concurrently and returns
// results in input order. Used by the parent dispatcher when the model
// emits multiple Agent tool_use blocks in a single assistant turn.
//
// Drop-in replacement for the old RunSubagentsInParallel.
type AgentInvocation struct {
	Agent       *Agent
	Description string
	Prompt      string
	MaxTurns    int    // 0 → use AgentToolConfig default
	Model       string // "" → use Agent.Model or parent default
}

func RunAgentsInParallel(ctx context.Context, parent *Engine, invs []AgentInvocation, defaultMaxTurns int, defaultModel string) []*AgentResult {
	if len(invs) == 0 {
		return nil
	}
	if len(invs) == 1 {
		return []*AgentResult{runAgentInner(ctx, parent, invs[0].Agent, invs[0].Description, invs[0].Prompt, max(invs[0].MaxTurns, defaultMaxTurns), firstNonEmpty(invs[0].Model, defaultModel))}
	}

	results := make([]*AgentResult, len(invs))
	var wg sync.WaitGroup
	wg.Add(len(invs))
	for i := range invs {
		go func(idx int, inv AgentInvocation) {
			defer wg.Done()
			results[idx] = runAgentInner(
				ctx, parent, inv.Agent, inv.Description, inv.Prompt,
				max(inv.MaxTurns, defaultMaxTurns),
				firstNonEmpty(inv.Model, defaultModel),
			)
		}(i, invs[i])
	}
	wg.Wait()
	return results
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ─── AgentTool factory ────────────────────────────────────────────────────

// AgentToolConfig wires an AgentTool. All fields are agnostic.
type AgentToolConfig struct {
	// Sources discover Agents. Order matters: earlier sources win on type
	// collisions.
	Sources []AgentSource

	// ParentEngine is the Engine whose tools/LLM the spawned agents reuse
	// (via RunAgentLoopWithEngine). Required.
	ParentEngine *Engine

	// DefaultModel is used when an agent definition doesn't specify one and
	// the tool input doesn't override.
	DefaultModel string

	// DefaultMaxTurns caps any agent that doesn't specify maxTurns.
	// Default 10.
	DefaultMaxTurns int

	// AllowedTypes optionally restricts which agent types the model can
	// spawn (mirrors Claude Code's `Agent(x,y)` allowedAgentTypes filter).
	AllowedTypes []string
}

// NewAgentTool returns the *Tool exposed to the LLM. The DynamicReminder
// hook surfaces the available agent listing as a <system-reminder>.
//
// IsConcurrencySafe returns true: agents are independent by construction,
// so the dispatcher can fan out parallel Agent tool_use blocks within a
// single assistant message.
func NewAgentTool(cfg AgentToolConfig) *Tool {
	if cfg.ParentEngine == nil {
		panic("autobuild: NewAgentTool requires ParentEngine")
	}
	if cfg.DefaultMaxTurns <= 0 {
		cfg.DefaultMaxTurns = 10
	}

	return &Tool{
		Name:              "Agent",
		Description:       agentToolPrompt,
		Category:          ToolCategoryPlanning,
		IsReadOnly:        func(map[string]any) bool { return false },
		IsConcurrencySafe: func(map[string]any) bool { return true },
		Parameters: ToolFuncParams{
			Type: "object",
			Properties: map[string]ToolParam{
				"description":   {Type: "string", Description: "A short (3-5 word) description of the task."},
				"prompt":        {Type: "string", Description: "The full task brief for the agent. Be specific — the agent starts with no implicit context."},
				"subagent_type": {Type: "string", Description: "Which agent type to spawn. Listed in <system-reminder>. Omit to use the first available."},
				"model":         {Type: "string", Description: "Optional model override. Defaults to the agent definition's model or the parent's."},
				"max_turns":     {Type: "integer", Description: "Cap on agent loop iterations. Defaults to the agent definition's maxTurns."},
				"name":          {Type: "string", Description: "Optional human-friendly label for the spawned agent."},
				"background":    {Type: "boolean", Description: "Reserved — background execution is not yet supported in v3."},
			},
			Required: []string{"description", "prompt"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			description, _ := args["description"].(string)
			prompt, _ := args["prompt"].(string)
			subagentType, _ := args["subagent_type"].(string)
			modelOverride, _ := args["model"].(string)
			maxTurnsOverride := 0
			switch v := args["max_turns"].(type) {
			case float64:
				maxTurnsOverride = int(v)
			case int:
				maxTurnsOverride = v
			}

			if strings.TrimSpace(prompt) == "" {
				return "", fmt.Errorf("agent: missing required arg 'prompt'")
			}

			ag, err := resolveAgent(ctx, cfg.Sources, cfg.AllowedTypes, subagentType)
			if err != nil {
				return "", err
			}

			model := modelOverride
			if model == "" {
				model = cfg.DefaultModel
			}
			maxTurns := maxTurnsOverride
			if maxTurns <= 0 {
				maxTurns = cfg.DefaultMaxTurns
			}

			res := runAgentInner(ctx, cfg.ParentEngine, ag, description, prompt, maxTurns, model)
			if res.Error != nil {
				return "", res.Error
			}
			return res.Output, nil
		},
		DynamicReminder: func(ctx context.Context) (string, error) {
			agents, err := collectAgents(ctx, cfg.Sources, cfg.AllowedTypes)
			if err != nil || len(agents) == 0 {
				return "", nil
			}
			lines := make([]string, 0, len(agents))
			for _, a := range agents {
				lines = append(lines, FormatAgentLine(a))
			}
			body := "Available agent types (invoke via the Agent tool):\n" + strings.Join(lines, "\n")
			return body, nil
		},
	}
}

func resolveAgent(ctx context.Context, sources []AgentSource, allowedTypes []string, subagentType string) (*Agent, error) {
	allowed := make(map[string]bool, len(allowedTypes))
	for _, t := range allowedTypes {
		allowed[t] = true
	}

	var first *Agent
	for _, src := range sources {
		list, err := src.List(ctx)
		if err != nil {
			continue
		}
		for _, a := range list {
			if len(allowed) > 0 && !allowed[a.Type] {
				continue
			}
			if first == nil {
				first = a
			}
			if subagentType != "" && a.Type == subagentType {
				return a, nil
			}
		}
	}
	if subagentType != "" {
		return nil, fmt.Errorf("agent type %q not found", subagentType)
	}
	if first == nil {
		return nil, fmt.Errorf("no agents available")
	}
	return first, nil
}

func collectAgents(ctx context.Context, sources []AgentSource, allowedTypes []string) ([]*Agent, error) {
	allowed := make(map[string]bool, len(allowedTypes))
	for _, t := range allowedTypes {
		allowed[t] = true
	}
	seen := make(map[string]bool)
	var out []*Agent
	for _, src := range sources {
		list, err := src.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, a := range list {
			if seen[a.Type] {
				continue
			}
			if len(allowed) > 0 && !allowed[a.Type] {
				continue
			}
			seen[a.Type] = true
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out, nil
}

const agentToolPrompt = `Launch a focused agent to handle a complex, multi-step task autonomously.

The Agent tool spawns a specialized agent with its own focused loop, tool allowlist, and turn budget. Each agent runs independently — provide a complete, self-contained brief.

How to invoke:
- Provide a 3-5 word ` + "`description`" + ` and a full ` + "`prompt`" + `.
- Optionally pick a specific agent type via ` + "`subagent_type`" + `.

Important:
- Available agent types and their tool access are listed in the <system-reminder> attached to this turn.
- For genuinely independent tasks, emit MULTIPLE Agent tool_use blocks in a SINGLE assistant message — the dispatcher will fan them out in parallel.
- Brief the agent like a smart colleague who just walked into the room. Explain what to do, what's already known, and what's out of scope. Don't write "based on your findings, X" — that delegates synthesis the parent should be doing.
- The agent's response is returned as the tool_result. Summarize it for the user; the user does not see the agent's raw output.
- Don't invoke an agent that's already running.`
