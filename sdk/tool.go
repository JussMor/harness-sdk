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

// Tool defines a single executable capability exposed to the agent.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Category    ToolCategory   `json:"category"`
	Parameters  ToolFuncParams `json:"parameters"`
	Execute     ToolExecuteFunc `json:"-"`

	// Hidden tools are present in the registry but excluded from ToolDefs.
	// Use Search + Reveal to expose them to the LLM dynamically.
	// This mirrors Claude's tool_search pattern: visible list is partial.
	Hidden bool `json:"hidden,omitempty"`
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
	mu    sync.RWMutex
	tools map[string]*Tool
}

// NewToolRegistry creates an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]*Tool)}
}

// Register adds a tool. If a tool with the same name exists, it is replaced.
func (r *ToolRegistry) Register(t *Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name] = t
}

// Get returns a tool by name, or nil if not found.
func (r *ToolRegistry) Get(name string) *Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
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
// Hidden tools are excluded — use Search + Reveal to expose them.
func (r *ToolRegistry) ToolDefs() []ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		if t.Hidden {
			continue
		}
		out = append(out, t.ToToolDef())
	}
	return out
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
	if strings.Contains(strings.ToLower(t.Description), query) {
		score += 0.3
	}
	if strings.Contains(strings.ToLower(string(t.Category)), query) {
		score += 0.1
	}
	return score
}
