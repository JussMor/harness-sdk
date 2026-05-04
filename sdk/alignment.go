package autobuild

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Planner decides whether a user message warrants a structured Plan and,
// if so, proposes one. This is the missing piece of the Alignment phase:
// the agent should propose a plan for complex tasks before diving into
// Execution, exactly as Claude does.
//
// Implementations choose between:
//   - Heuristic detection (cheap, no LLM call)
//   - LLM-driven planning (expensive, more accurate)
//   - Hybrid (heuristic gate, then LLM if gate passes)
type Planner interface {
	// ShouldPlan returns true when a plan is appropriate for this message.
	// Cheap check; called before Propose.
	ShouldPlan(ctx context.Context, userMessage string, conv *Conversation) bool

	// Propose builds a candidate plan. Called only when ShouldPlan returned true.
	// Returning a nil plan is allowed — means "I changed my mind after looking closer".
	Propose(ctx context.Context, userMessage string, conv *Conversation) (*Plan, error)
}

// ── HeuristicPlanner ─────────────────────────────────────────────────────────

// HeuristicPlanner detects "complex task" patterns by inspecting the user
// message and returns a generic 3-step plan. Cheap and deterministic, but
// the proposed steps are placeholders — the LLM still does the actual work
// during Execution.
//
// Use HeuristicPlanner when you want to track *that* a task is multi-step
// without paying for plan generation. For real planning, use LLMPlanner.
type HeuristicPlanner struct {
	// MinComplexitySignals is how many "complex" markers must appear in the
	// message before planning kicks in. Default 2.
	MinComplexitySignals int

	// ComplexitySignals are phrases or words that indicate multi-step work.
	// Defaults cover English + Spanish.
	ComplexitySignals []string
}

// DefaultHeuristicPlanner returns a planner with sensible defaults.
func DefaultHeuristicPlanner() *HeuristicPlanner {
	return &HeuristicPlanner{
		MinComplexitySignals: 2,
		ComplexitySignals: []string{
			// English
			"refactor", "migrate", "implement", "build", "design",
			"and then", "after that", "first", "then", "finally",
			"all of", "across", "every", "multiple",
			// Spanish
			"refactoriza", "migra", "implementa", "construye", "diseña",
			"y luego", "después", "primero", "finalmente",
			"todos los", "cada", "múltiples",
		},
	}
}

// ShouldPlan counts complexity signals.
func (p *HeuristicPlanner) ShouldPlan(_ context.Context, userMessage string, _ *Conversation) bool {
	lower := strings.ToLower(userMessage)
	hits := 0
	for _, signal := range p.ComplexitySignals {
		if strings.Contains(lower, signal) {
			hits++
			if hits >= p.MinComplexitySignals {
				return true
			}
		}
	}
	// Long messages are also a signal
	if len(userMessage) > 400 {
		return true
	}
	return false
}

// Propose builds a generic placeholder plan with 3 phases.
func (p *HeuristicPlanner) Propose(_ context.Context, userMessage string, _ *Conversation) (*Plan, error) {
	title := truncatePlanner(userMessage, 60)
	return &Plan{
		Title:     title,
		Objective: userMessage,
		Executables: []Executable{
			{ID: "analyze", Name: "Analyze the request", Status: ExecStatusPlanned},
			{ID: "execute", Name: "Execute the work", Status: ExecStatusPlanned, Dependencies: []string{"analyze"}},
			{ID: "verify", Name: "Verify and report", Status: ExecStatusPlanned, Dependencies: []string{"execute"}},
		},
	}, nil
}

// ── LLMPlanner ───────────────────────────────────────────────────────────────

// LLMPlanner asks the LLM to break down the user message into a concrete plan.
// More accurate than heuristic, but adds an LLM call per turn.
//
// For best results, use a small fast model (haiku-class) — planning is a
// classification task, not a reasoning marathon.
type LLMPlanner struct {
	Provider LLMProvider
	Model    string

	// MaxExecutables caps the number of steps. Default 5.
	MaxExecutables int

	// Threshold for ShouldPlan — proportion of "complex" indicators in
	// message before triggering. 0-1, default 0.0 (always plan, let Propose
	// decide if a plan adds value).
	ShouldPlanThreshold float64
}

// ShouldPlan defers to LLMPlanner: always returns true; Propose can return nil.
func (p *LLMPlanner) ShouldPlan(_ context.Context, _ string, _ *Conversation) bool {
	return p.Provider != nil
}

// Propose calls the LLM to decompose the task.
// Returns nil if the LLM thinks no plan is needed.
func (p *LLMPlanner) Propose(ctx context.Context, userMessage string, conv *Conversation) (*Plan, error) {
	maxExec := p.MaxExecutables
	if maxExec <= 0 {
		maxExec = 5
	}

	prompt := fmt.Sprintf(`You decide if this user request needs a structured plan and propose one if so.

Rules:
- If the request is a single quick question or single tool call, output: {"plan": null}
- If the request involves multiple steps with dependencies, propose a plan
- Maximum %d executables
- Each executable has: id (snake_case), name (one line), dependencies (array of ids that must complete first)

Output strictly JSON, no prose:
{"plan": {"title": "...", "objective": "...", "executables": [{"id": "...", "name": "...", "dependencies": []}]}}
or
{"plan": null}

User request: %s`, maxExec, userMessage)

	resp, err := p.Provider.Chat(ctx, ChatRequest{
		Model: p.Model,
		Messages: []ChatMessage{
			{Role: RoleUser, Content: prompt},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("planner LLM call: %w", err)
	}

	var wrapper struct {
		Plan *struct {
			Title       string `json:"title"`
			Objective   string `json:"objective"`
			Executables []struct {
				ID           string   `json:"id"`
				Name         string   `json:"name"`
				Dependencies []string `json:"dependencies"`
			} `json:"executables"`
		} `json:"plan"`
	}
	cleaned := stripJSONFence(resp.Content)
	if err := json.Unmarshal([]byte(cleaned), &wrapper); err != nil {
		return nil, fmt.Errorf("planner JSON parse: %w (got %q)", err, cleaned)
	}
	if wrapper.Plan == nil {
		return nil, nil
	}
	plan := &Plan{
		Title:     wrapper.Plan.Title,
		Objective: wrapper.Plan.Objective,
	}
	for _, e := range wrapper.Plan.Executables {
		plan.Executables = append(plan.Executables, Executable{
			ID:           e.ID,
			Name:         e.Name,
			Dependencies: e.Dependencies,
			Status:       ExecStatusPlanned,
		})
	}
	return plan, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func truncatePlanner(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
