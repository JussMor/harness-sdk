package autobuild

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ToolCategory groups tools by functional domain.
type ToolCategory string

const (
	ToolCategoryWorkspace    ToolCategory = "workspace"
	ToolCategoryCompute      ToolCategory = "compute"
	ToolCategoryData         ToolCategory = "data"
	ToolCategoryWeb          ToolCategory = "web"
	ToolCategoryPlanning     ToolCategory = "planning"
	ToolCategoryComm         ToolCategory = "communication"
	ToolCategoryIntegrations ToolCategory = "integrations"
	ToolCategoryMemory       ToolCategory = "memory"
	ToolCategoryCustom       ToolCategory = "custom"
)

// ToolExecuteFunc is the function signature for tool execution.
type ToolExecuteFunc func(ctx context.Context, sandboxID string, args map[string]any) (string, error)

// ToolPredicate evaluates a tool input. Used for IsReadOnly / IsConcurrencySafe /
// IsDestructive. Defaults documented per-field on Tool.
type ToolPredicate func(args map[string]any) bool

// ToolValidator runs before execution and may reject the call with a user-visible error.
// Returning a non-nil error short-circuits the dispatcher with that message; the LLM
// sees the error string as the tool result.
type ToolValidator func(ctx context.Context, args map[string]any) error

// PermissionDecision is the outcome of a permission check. Mirrors Claude Code's
// PermissionResult shape.
type PermissionDecision string

const (
	PermissionAllow         PermissionDecision = "allow"
	PermissionDeny          PermissionDecision = "deny"
	PermissionAskUser       PermissionDecision = "ask_user"
)

// PermissionResult is what a Tool's CheckPermissions returns.
// Reason is shown to the user (and the LLM on deny).
type PermissionResult struct {
	Decision PermissionDecision `json:"decision"`
	Reason   string             `json:"reason,omitempty"`
	// UpdatedArgs lets a permission policy rewrite the args before execution
	// (e.g. inject default flags). nil = use original args.
	UpdatedArgs map[string]any `json:"updated_args,omitempty"`
}

// ToolPermissionFn is the optional permission gate.
type ToolPermissionFn func(ctx context.Context, args map[string]any) (PermissionResult, error)

// ToolReminderFn returns markdown to be injected as a per-turn system-reminder
// attachment on behalf of this tool. Used by Skill / Task tools to publish
// dynamic listings ("Available skills:...", "Available agents:...") without
// stuffing the cache-stable tool description.
//
// Returning empty string means no attachment this turn.
type ToolReminderFn func(ctx context.Context) (string, error)

// Tool defines a single executable capability exposed to the agent.
//
// All optional fields default to safe behavior when nil:
//   - IsReadOnly       → false (assume mutates state)
//   - IsConcurrencySafe → false (assume serial)
//   - IsDestructive    → false (no confirmation prompt by default)
//   - Validate         → no validation
//   - CheckPermissions → always allow
//   - DynamicReminder  → no per-turn attachment
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Category    ToolCategory   `json:"category"`
	Parameters  ToolFuncParams `json:"parameters"`
	Execute     ToolExecuteFunc `json:"-"`

	// Aliases are alternate names accepted by the dispatcher for backwards
	// compatibility after a rename. Lookup by alias still resolves to this tool.
	Aliases []string `json:"aliases,omitempty"`

	// SearchHint is short keyword text used by ToolSearch ranking. If empty,
	// search falls back to Name + Description.
	SearchHint string `json:"search_hint,omitempty"`

	// Safety / planning hooks (all optional).
	IsReadOnly        ToolPredicate `json:"-"`
	IsConcurrencySafe ToolPredicate `json:"-"`
	IsDestructive     ToolPredicate `json:"-"`

	// Validate runs before CheckPermissions. Reject with non-nil error.
	Validate ToolValidator `json:"-"`

	// CheckPermissions is the per-call permission gate. nil = always allow.
	CheckPermissions ToolPermissionFn `json:"-"`

	// DynamicReminder is invoked once per turn, before the model call. The
	// returned markdown is appended as a <system-reminder> attachment.
	DynamicReminder ToolReminderFn `json:"-"`

	// Visibility / loading.
	//
	// Hidden tools are present in the registry but excluded from ToolDefs.
	// Use Search + Reveal to expose them to the LLM dynamically.
	Hidden bool `json:"hidden,omitempty"`

	// Deferred tools are excluded from the initial ToolDefs but become
	// callable once discovered via ToolSearch (similar to Hidden, but with
	// search-driven activation semantics).
	Deferred bool `json:"deferred,omitempty"`

	// AlwaysLoad forces the tool into the initial ToolDefs even when other
	// heuristics would defer it. Mirrors Claude Code's alwaysLoad flag for
	// tools whose presence is mandatory (e.g. memory tools, todo tools).
	AlwaysLoad bool `json:"always_load,omitempty"`

	// MaxResultSizeChars is the soft cap (in characters) on the tool result
	// returned to the LLM. 0 = unlimited. Above this, the runtime persists
	// the full result to disk and substitutes a truncated handle.
	MaxResultSizeChars int `json:"max_result_size_chars,omitempty"`
}

// ReadOnly reports whether this tool, given args, only reads state. Defaults
// to false (mutating) when IsReadOnly is unset.
func (t *Tool) ReadOnly(args map[string]any) bool {
	if t == nil || t.IsReadOnly == nil {
		return false
	}
	return t.IsReadOnly(args)
}

// ConcurrencySafe reports whether this tool, given args, may run in parallel
// with other tool calls in the same dispatch batch. Defaults to false.
func (t *Tool) ConcurrencySafe(args map[string]any) bool {
	if t == nil || t.IsConcurrencySafe == nil {
		return false
	}
	return t.IsConcurrencySafe(args)
}

// Destructive reports whether this call is destructive (deletes/overwrites
// data, sends external messages, etc.). Defaults to false.
func (t *Tool) Destructive(args map[string]any) bool {
	if t == nil || t.IsDestructive == nil {
		return false
	}
	return t.IsDestructive(args)
}

// ToolFuncParams is a JSON-Schema-like description of a tool's input.
type ToolFuncParams struct {
	Type       string                `json:"type"` // always "object"
	Properties map[string]ToolParam  `json:"properties,omitempty"`
	Required   []string              `json:"required,omitempty"`
}

// ToolParam describes a single parameter in the JSON Schema.
type ToolParam struct {
	Type        string            `json:"type"`
	Description string            `json:"description,omitempty"`
	Enum        []string          `json:"enum,omitempty"`
	Items       *ToolParam        `json:"items,omitempty"`       // for type=array
	Properties  map[string]ToolParam `json:"properties,omitempty"` // for type=object
	Required    []string          `json:"required,omitempty"`
}

// ToolDef is the wire format sent to an LLM for function-calling.
type ToolDef struct {
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction is the function metadata inside a ToolDef.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  ToolFuncParams `json:"parameters"`
}

// ToToolDef converts a Tool to the LLM wire format.
func (t *Tool) ToToolDef() ToolDef {
	return ToolDef{
		Type: "function",
		Function: ToolFunction{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		},
	}
}

// ToolRegistry is a thread-safe, categorized collection of tools.
type ToolRegistry struct {
	mu      sync.RWMutex
	tools   map[string]*Tool
	aliases map[string]string // alias -> canonical name
}

// NewToolRegistry creates an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]*Tool)}
}

// Register adds a tool. If a tool with the same name exists, it is replaced.
// Aliases are also indexed for lookup, but only the canonical Name is listed
// in Names()/List()/ToolDefs() so the LLM sees a single entry per tool.
func (r *ToolRegistry) Register(t *Tool) {
	if t == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name] = t
	for _, alias := range t.Aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || alias == t.Name {
			continue
		}
		// Aliases are stored in the same map but skipped during enumeration
		// because List/Names/ToolDefs walk r.tools and would double-count.
		// We stash them in r.aliases instead.
		if r.aliases == nil {
			r.aliases = make(map[string]string)
		}
		r.aliases[alias] = t.Name
	}
}

// Get returns a tool by name (or alias), or nil if not found.
func (r *ToolRegistry) Get(name string) *Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if t, ok := r.tools[name]; ok {
		return t
	}
	if canonical, ok := r.aliases[name]; ok {
		return r.tools[canonical]
	}
	return nil
}

// List returns all registered tools.
func (r *ToolRegistry) List() []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

// ByCategory returns tools matching the given category.
func (r *ToolRegistry) ByCategory(cat ToolCategory) []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Tool
	for _, t := range r.tools {
		if t.Category == cat {
			out = append(out, t)
		}
	}
	return out
}

// Names returns a sorted slice of all registered tool names.
func (r *ToolRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// DescribeAvailable returns a stable, prompt-friendly bullet list of the
// currently registered tools.
func (r *ToolRegistry) DescribeAvailable() string {
	if r == nil {
		return "- none"
	}

	tools := r.List()
	if len(tools) == 0 {
		return "- none"
	}

	sort.Slice(tools, func(i, j int) bool {
		left := ""
		if tools[i] != nil {
			left = tools[i].Name
		}
		right := ""
		if tools[j] != nil {
			right = tools[j].Name
		}
		return left < right
	})

	lines := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool == nil || strings.TrimSpace(tool.Name) == "" {
			continue
		}
		line := "- " + tool.Name
		if desc := strings.TrimSpace(tool.Description); desc != "" {
			line += ": " + desc
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "- none"
	}

	return strings.Join(lines, "\n")
}

// ToolDefs returns all visible tools as LLM wire-format definitions.
// Hidden and Deferred tools are excluded — use Search + Reveal to expose them.
func (r *ToolRegistry) ToolDefs() []ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		if t.Hidden || t.Deferred {
			continue
		}
		out = append(out, t.ToToolDef())
	}
	return out
}

// CollectDynamicReminders runs every registered tool's DynamicReminder hook
// (skipping nil hooks) and returns the produced markdown blocks. Order is
// stable by tool name so cache fingerprints stay consistent across turns when
// the underlying content is unchanged.
func (r *ToolRegistry) CollectDynamicReminders(ctx context.Context) ([]string, error) {
	r.mu.RLock()
	tools := make([]*Tool, 0, len(r.tools))
	for _, t := range r.tools {
		if t.DynamicReminder != nil {
			tools = append(tools, t)
		}
	}
	r.mu.RUnlock()

	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	out := make([]string, 0, len(tools))
	for _, t := range tools {
		block, err := t.DynamicReminder(ctx)
		if err != nil {
			return nil, fmt.Errorf("tool %q dynamic reminder: %w", t.Name, err)
		}
		if strings.TrimSpace(block) == "" {
			continue
		}
		out = append(out, block)
	}
	return out, nil
}

// Search returns tools whose name, description, or category matches the query.
// Used by the LLM via a tool_search tool to discover capabilities dynamically
// instead of being shown all tools upfront. This mirrors how Claude operates:
// the visible tool list is partial; tools are discovered as needed.
//
// Hidden tools ARE included in search results — that's the point. Use
// Reveal to make a discovered tool callable.
func (r *ToolRegistry) Search(query string) []ToolMatch {
	r.mu.RLock()
	defer r.mu.RUnlock()
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	var matches []ToolMatch
	for _, t := range r.tools {
		score := scoreToolMatch(t, q)
		if score > 0 {
			matches = append(matches, ToolMatch{Tool: t, Score: score})
		}
	}
	// Sort desc by score (insertion sort — tool registries are small)
	for i := 1; i < len(matches); i++ {
		for j := i; j > 0 && matches[j].Score > matches[j-1].Score; j-- {
			matches[j], matches[j-1] = matches[j-1], matches[j]
		}
	}
	return matches
}

// Reveal makes a hidden tool visible (callable by the LLM in subsequent turns).
// Use after Search returns a hidden tool the agent decided to use.
func (r *ToolRegistry) Reveal(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tools[name]
	if !ok {
		return fmt.Errorf("tool %q not found", name)
	}
	t.Hidden = false
	r.tools[name] = t
	return nil
}

// Hide marks a tool as hidden — it stays in the registry but is excluded
// from ToolDefs. Useful for tools that should only be discoverable via Search.
func (r *ToolRegistry) Hide(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tools[name]
	if !ok {
		return fmt.Errorf("tool %q not found", name)
	}
	t.Hidden = true
	r.tools[name] = t
	return nil
}

// ToolMatch is a tool with its relevance score for a search query.
type ToolMatch struct {
	Tool  *Tool   `json:"tool"`
	Score float64 `json:"score"`
}

func scoreToolMatch(t *Tool, query string) float64 {
	var score float64
	if strings.Contains(strings.ToLower(t.Name), query) {
		score += 0.6
	}
	if strings.Contains(strings.ToLower(t.SearchHint), query) {
		score += 0.4
	}
	if strings.Contains(strings.ToLower(t.Description), query) {
		score += 0.3
	}
	if strings.Contains(strings.ToLower(string(t.Category)), query) {
		score += 0.1
	}
	for _, alias := range t.Aliases {
		if strings.Contains(strings.ToLower(alias), query) {
			score += 0.2
			break
		}
	}
	return score
}
