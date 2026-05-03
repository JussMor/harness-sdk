package autobuild

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// EvalCase is a single test case for an agent.
// Multiple cases form a suite that measures consistency and capability.
type EvalCase struct {
	// Name uniquely identifies the case.
	Name string `json:"name"`

	// Input is the user message to send.
	Input string `json:"input"`

	// SystemPrompt overrides the agent's default system prompt for this case.
	// Empty means use the agent's default.
	SystemPrompt string `json:"system_prompt,omitempty"`

	// Mode is the active mode for this case.
	Mode string `json:"mode,omitempty"`

	// Assertions are checks the output must satisfy.
	// Each assertion runs independently; case fails if any assertion fails.
	Assertions []Assertion `json:"assertions"`

	// Tags categorize cases (e.g. "memory", "tool-use", "refusal").
	Tags []string `json:"tags,omitempty"`
}

// Assertion is a single check on an agent's output.
type Assertion struct {
	// Type is the check kind.
	Type AssertionType `json:"type"`

	// Value is the expected value (interpretation depends on Type).
	Value string `json:"value"`

	// Description is human-readable.
	Description string `json:"description,omitempty"`
}

// AssertionType is the kind of check.
type AssertionType string

const (
	// AssertContains: output contains Value as a substring (case-insensitive).
	AssertContains AssertionType = "contains"

	// AssertNotContains: output does NOT contain Value (case-insensitive).
	AssertNotContains AssertionType = "not_contains"

	// AssertToolCalled: a tool with name=Value was called during the run.
	AssertToolCalled AssertionType = "tool_called"

	// AssertToolNotCalled: a tool with name=Value was NOT called.
	AssertToolNotCalled AssertionType = "tool_not_called"

	// AssertMaxTurns: total turns is <= Value (parsed as int).
	AssertMaxTurns AssertionType = "max_turns"

	// AssertStopReason: result.StopReason equals Value.
	AssertStopReason AssertionType = "stop_reason"
)

// EvalResult is the outcome of running a single case.
type EvalResult struct {
	Case        EvalCase           `json:"case"`
	Pass        bool               `json:"pass"`
	Output      string             `json:"output"`
	Failures    []AssertionFailure `json:"failures,omitempty"`
	Turns       int                `json:"turns"`
	Usage       TokenUsage         `json:"usage"`
	Duration    time.Duration      `json:"duration_ms"`
	Error       string             `json:"error,omitempty"`
	ToolCalls   []string           `json:"tool_calls,omitempty"`
}

// AssertionFailure describes which assertion failed and why.
type AssertionFailure struct {
	Assertion Assertion `json:"assertion"`
	Reason    string    `json:"reason"`
}

// EvalSuite is a collection of cases plus a runner.
type EvalSuite struct {
	Cases []EvalCase
}

// LoadEvalSuite parses a JSON file into an EvalSuite.
func LoadEvalSuite(data []byte) (*EvalSuite, error) {
	var s EvalSuite
	if err := json.Unmarshal(data, &s.Cases); err != nil {
		// Try wrapped form { "cases": [...] }
		var wrapped struct {
			Cases []EvalCase `json:"cases"`
		}
		if err2 := json.Unmarshal(data, &wrapped); err2 != nil {
			return nil, fmt.Errorf("parse eval suite: %w", err)
		}
		s.Cases = wrapped.Cases
	}
	return &s, nil
}

// Run executes all cases against the given runtime and returns results.
// Cases run sequentially; for parallel evaluation, split the suite.
func (s *EvalSuite) Run(ctx context.Context, runtime *Runtime) ([]EvalResult, error) {
	results := make([]EvalResult, 0, len(s.Cases))
	for _, c := range s.Cases {
		result := s.runCase(ctx, runtime, c)
		results = append(results, result)
	}
	return results, nil
}

func (s *EvalSuite) runCase(ctx context.Context, runtime *Runtime, c EvalCase) EvalResult {
	result := EvalResult{Case: c}
	start := time.Now()

	if c.Mode != "" {
		runtime = runtime.WithMode(c.Mode)
	}

	conv := NewConversation(fmt.Sprintf("eval-%s-%d", c.Name, time.Now().UnixNano()))
	rr, err := runtime.Run(ctx, conv, c.Input)
	result.Duration = time.Since(start)

	if err != nil {
		result.Error = err.Error()
		result.Pass = false
		return result
	}

	result.Output = rr.Response
	result.Turns = rr.Turns
	result.Usage = rr.Usage

	// Extract tool call names from spans (new) or fallback to nothing
	for _, span := range rr.Trace {
		if strings.HasPrefix(span.Name, "tool:") {
			result.ToolCalls = append(result.ToolCalls, strings.TrimPrefix(span.Name, "tool:"))
		}
	}

	for _, a := range c.Assertions {
		if reason := evaluateAssertion(a, &result, rr); reason != "" {
			result.Failures = append(result.Failures, AssertionFailure{
				Assertion: a,
				Reason:    reason,
			})
		}
	}
	result.Pass = len(result.Failures) == 0
	return result
}

func evaluateAssertion(a Assertion, result *EvalResult, rr *RuntimeResult) string {
	output := strings.ToLower(result.Output)
	value := strings.ToLower(a.Value)

	switch a.Type {
	case AssertContains:
		if !strings.Contains(output, value) {
			return fmt.Sprintf("output missing %q", a.Value)
		}
	case AssertNotContains:
		if strings.Contains(output, value) {
			return fmt.Sprintf("output contains forbidden %q", a.Value)
		}
	case AssertToolCalled:
		for _, name := range result.ToolCalls {
			if name == a.Value {
				return ""
			}
		}
		return fmt.Sprintf("tool %q was not called", a.Value)
	case AssertToolNotCalled:
		for _, name := range result.ToolCalls {
			if name == a.Value {
				return fmt.Sprintf("tool %q was called", a.Value)
			}
		}
	case AssertMaxTurns:
		var max int
		if _, err := fmt.Sscanf(a.Value, "%d", &max); err == nil {
			if result.Turns > max {
				return fmt.Sprintf("turns %d exceeds max %d", result.Turns, max)
			}
		}
	case AssertStopReason:
		if rr.StopReason != a.Value {
			return fmt.Sprintf("stop reason %q != %q", rr.StopReason, a.Value)
		}
	}
	return ""
}

// Summary aggregates results into pass/fail counts and headline metrics.
type EvalSummary struct {
	Total       int            `json:"total"`
	Passed      int            `json:"passed"`
	Failed      int            `json:"failed"`
	PassRate    float64        `json:"pass_rate"`
	TotalUsage  TokenUsage     `json:"total_usage"`
	TotalTime   time.Duration  `json:"total_time_ms"`
	ByTag       map[string]int `json:"by_tag,omitempty"`
}

// Summarize computes aggregate metrics from a result slice.
func Summarize(results []EvalResult) EvalSummary {
	s := EvalSummary{Total: len(results), ByTag: make(map[string]int)}
	for _, r := range results {
		if r.Pass {
			s.Passed++
		} else {
			s.Failed++
		}
		s.TotalUsage.PromptTokens += r.Usage.PromptTokens
		s.TotalUsage.CompletionTokens += r.Usage.CompletionTokens
		s.TotalUsage.TotalTokens += r.Usage.TotalTokens
		s.TotalTime += r.Duration
		for _, tag := range r.Case.Tags {
			if r.Pass {
				s.ByTag[tag]++
			}
		}
	}
	if s.Total > 0 {
		s.PassRate = float64(s.Passed) / float64(s.Total)
	}
	return s
}
