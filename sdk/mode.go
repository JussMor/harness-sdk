package autobuild

import (
	"context"
	"fmt"
)

// BaseMode identifies one of the built-in execution modes.
type BaseMode string

const (
	BaseModeBalanced BaseMode = "balanced"
	BaseModeAnalyst  BaseMode = "analyst"
	BaseModeDeepWork BaseMode = "deep_work"
)

// ModeMeta holds the YAML frontmatter metadata for a mode's system.md file.
//
// Example frontmatter:
//
//	---
//	id: code-agent
//	name: Code Agent
//	base_mode: balanced
//	tools_mode: allowlist
//	tools:
//	  - computer-ops
//	  - memory
//	  - spawn-runner
//	model: claude-sonnet-4-20250514
//	reasoning_effort: high
//	author: obvious-team
//	created: 2026-04-01
//	---
type ModeMeta struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	BaseMode        string   `json:"base_mode"`
	ToolsMode       string   `json:"tools_mode,omitempty"`      // "allowlist" or "denylist"
	Tools           []string `json:"tools,omitempty"`
	Model           string   `json:"model,omitempty"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
	Temperature     string   `json:"temperature,omitempty"`
	Author          string   `json:"author,omitempty"`
	Created         string   `json:"created,omitempty"`
}

// ToolsMode determines how ToolsList is interpreted.
type ToolsMode string

const (
	// ToolsModeAllowlist means only the listed tools are available.
	ToolsModeAllowlist ToolsMode = "allowlist"

	// ToolsModeDenylist means all tools except the listed ones are available.
	ToolsModeDenylist ToolsMode = "denylist"
)

// ModelSettings overrides the default model configuration for a mode.
type ModelSettings struct {
	Model           string  `json:"model,omitempty"`
	ReasoningEffort string  `json:"reasoning_effort,omitempty"` // "low", "medium", "high"
	Temperature     float64 `json:"temperature,omitempty"`
}

// Mode defines how an agent behaves: which tools it can use, what prompt
// guides it, and which model configuration applies. Modes can be built-in
// (Balanced, Analyst, DeepWork) or custom (cloned from a base with overrides).
// A Mode is typically parsed from a system.md file containing YAML frontmatter
// (ModeMeta) followed by the system prompt content.
type Mode struct {
	// Meta holds all frontmatter fields parsed from the system.md header.
	Meta           ModeMeta       `json:"meta"`
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	BaseModeID    BaseMode       `json:"base_mode_id"`
	PromptContent string         `json:"prompt_content,omitempty"`
	ModelSettings *ModelSettings `json:"model_settings,omitempty"`
	ToolsMode     ToolsMode      `json:"tools_mode,omitempty"`
	ToolsList     []string       `json:"tools_list,omitempty"`
}

// IsToolAllowed returns whether a tool name is permitted under this mode's
// access control rules. If ToolsMode is empty, all tools are allowed.
func (m *Mode) IsToolAllowed(toolName string) bool {
	if m.ToolsMode == "" || len(m.ToolsList) == 0 {
		return true
	}
	listed := false
	for _, t := range m.ToolsList {
		if t == toolName {
			listed = true
			break
		}
	}
	switch m.ToolsMode {
	case ToolsModeAllowlist:
		return listed
	case ToolsModeDenylist:
		return !listed
	default:
		return true
	}
}

// ModeProvider abstracts discovery and management of execution modes.
type ModeProvider interface {
	// Get returns a mode by ID.
	Get(ctx context.Context, modeID string) (*Mode, error)

	// List returns all available modes (built-in + custom).
	List(ctx context.Context) ([]*Mode, error)

	// Create persists a new custom mode.
	Create(ctx context.Context, m Mode) (*Mode, error)

	// BuiltinModes returns the three base modes.
	BuiltinModes() []*Mode
}

// StaticModeProvider is an in-memory ModeProvider backed by a fixed set of modes.
// It is useful for file-loaded modes and tests that do not need persistence.
type StaticModeProvider struct {
	modes map[string]*Mode
	list  []*Mode
}

// NewStaticModeProvider builds an in-memory ModeProvider from a list of modes.
func NewStaticModeProvider(modes []*Mode) *StaticModeProvider {
	index := make(map[string]*Mode, len(modes))
	list := make([]*Mode, 0, len(modes))
	for _, mode := range modes {
		if mode == nil || mode.ID == "" {
			continue
		}
		index[mode.ID] = mode
		list = append(list, mode)
	}
	return &StaticModeProvider{modes: index, list: list}
}

func (p *StaticModeProvider) Get(_ context.Context, modeID string) (*Mode, error) {
	mode, ok := p.modes[modeID]
	if !ok {
		return nil, fmt.Errorf("mode not found: %s", modeID)
	}
	return mode, nil
}

func (p *StaticModeProvider) List(_ context.Context) ([]*Mode, error) {
	return p.list, nil
}

func (p *StaticModeProvider) Create(_ context.Context, m Mode) (*Mode, error) {
	return nil, fmt.Errorf("mode creation not supported by static mode provider")
}

func (p *StaticModeProvider) BuiltinModes() []*Mode {
	return nil
}
