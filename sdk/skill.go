package autobuild

import (
	"context"
	"strings"
)

// SkillMeta holds the YAML frontmatter metadata required in every SKILL.md.
//
// Example frontmatter:
//
//	---
//	name: agentic-execution-planning
//	version: 1.0.0
//	description: Turn an approved spec into an execution plan.
//	category: autobuild
//	triggers:
//	  - execution planning
//	  - dependency dag
//	author: obvious-team
//	created: 2026-04-06
//	---
type SkillMeta struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Triggers    []string `json:"triggers"`
	Author      string   `json:"author"`
	Created     string   `json:"created"` // YYYY-MM-DD

	// Optional fields
	RequiredFeatureFlag string   `json:"requiredFeatureFlag,omitempty"`
	GrantedTools        []string `json:"grantedTools,omitempty"`
}

// Skill is a package of domain-specific knowledge that the agent loads
// on-demand. Skills override training data when there is a conflict.
// A Skill is typically parsed from a SKILL.md file containing YAML
// frontmatter (SkillMeta) followed by markdown content.
type Skill struct {
	// Meta holds all frontmatter fields parsed from the SKILL.md header.
	Meta SkillMeta `json:"meta"`

	// Name is the unique identifier (e.g. "writing", "user-cutlist-domain-model").
	// Populated from Meta.Name for convenience.
	Name string `json:"name"`

	// Domain describes the knowledge area in human terms.
	// Populated from Meta.Category for convenience.
	Domain string `json:"domain"`

	// Triggers are keywords that activate this skill.
	// Populated from Meta.Triggers for convenience.
	Triggers []string `json:"triggers"`

	// GrantedTools are additional tool names that become available when
	// this skill is loaded.
	GrantedTools []string `json:"granted_tools,omitempty"`

	// Content is the full markdown body (everything after the frontmatter).
	Content string `json:"content"`
}

// MatchesTrigger returns true if any of the skill's triggers appear
// (case-insensitive) in the given text.
func (s *Skill) MatchesTrigger(text string) bool {
	lower := strings.ToLower(text)
	for _, t := range s.Triggers {
		if strings.Contains(lower, strings.ToLower(strings.TrimSpace(t))) {
			return true
		}
	}
	return false
}

// SkillProvider abstracts loading, unloading, and discovering skills.
// Loaded skills persist in the agent's context for the lifetime of the thread.
type SkillProvider interface {
	// Load injects the skill into the active context. The skill's content
	// appears in every subsequent turn until unloaded.
	Load(ctx context.Context, name string) (*Skill, error)

	// Unload removes the skill from the active context, freeing token space.
	Unload(ctx context.Context, name string) error

	// Match returns all skills whose triggers match the given text.
	Match(ctx context.Context, text string) ([]*Skill, error)

	// List returns all available skill names.
	List(ctx context.Context) ([]string, error)

	// Get returns a skill by name without loading it into context.
	Get(ctx context.Context, name string) (*Skill, error)
}
