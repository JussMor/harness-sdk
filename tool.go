package autobuild

import (
	"context"
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
	return out
}

// ToolDefs returns all tools as LLM wire-format definitions.
func (r *ToolRegistry) ToolDefs() []ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.ToToolDef())
	}
	return out
}
