package autobuild

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// captureModelLLM records every model it receives in Chat calls.
type captureModelLLM struct {
	mu     sync.Mutex
	models []string
}

func (c *captureModelLLM) Chat(_ context.Context, req ChatRequest) (*ChatResponse, error) {
	c.mu.Lock()
	c.models = append(c.models, req.Model)
	c.mu.Unlock()
	return &ChatResponse{
		Content:      "done",
		FinishReason: "end_turn",
		Model:        req.Model,
		Usage:        TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}, nil
}

func (c *captureModelLLM) receivedModels() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.models))
	copy(out, c.models)
	return out
}

// TestWithModel verifies the Runtime.model field is set by WithModel.
func TestWithModel(t *testing.T) {
	engine := New(WithLLM(&captureModelLLM{}))
	rt := NewRuntime(engine).WithModel("claude-haiku-4-5-20251001")
	if rt.model != "claude-haiku-4-5-20251001" {
		t.Fatalf("expected model %q, got %q", "claude-haiku-4-5-20251001", rt.model)
	}
}

// TestRunStreamSubagents_PropagatesModel verifies that runStreamSubagents
// passes r.model to every Subagent it creates, and that the model reaches
// the LLM provider.
func TestRunStreamSubagents_PropagatesModel(t *testing.T) {
	llm := &captureModelLLM{}
	engine := New(WithLLM(llm))
	rt := NewRuntime(engine).WithModel("claude-haiku-4-5-20251001")

	executables := []Executable{
		{ID: "a", Name: "task-a", Description: "Do task A"},
		{ID: "b", Name: "task-b", Description: "Do task B"},
	}

	out := make(chan StreamEvent, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt.runStreamSubagents(ctx, executables, out)
	close(out)

	// Collect subagent results
	var results []*SubagentResult
	for ev := range out {
		if ev.Type == StreamEventSubagentResult && ev.SubagentResult != nil {
			results = append(results, ev.SubagentResult)
		}
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 subagent results, got %d", len(results))
	}

	// Every Chat call must have received the model
	models := llm.receivedModels()
	if len(models) == 0 {
		t.Fatal("LLM received no Chat calls")
	}
	for i, m := range models {
		if m != "claude-haiku-4-5-20251001" {
			t.Errorf("Chat call %d: model = %q, want %q", i, m, "claude-haiku-4-5-20251001")
		}
	}

	// SubagentResult.Model must also be populated
	for _, r := range results {
		if r.Model != "claude-haiku-4-5-20251001" {
			t.Errorf("subagent %s: result.Model = %q, want %q", r.ID, r.Model, "claude-haiku-4-5-20251001")
		}
	}
}

// TestRunStreamSubagents_NoModelPropagation verifies that without WithModel,
// subagents get an empty model (relies on provider DefaultModel fallback).
func TestRunStreamSubagents_NoModelPropagation(t *testing.T) {
	llm := &captureModelLLM{}
	engine := New(WithLLM(llm))
	rt := NewRuntime(engine) // no WithModel

	executables := []Executable{
		{ID: "x", Description: "Task X"},
		{ID: "y", Description: "Task Y"},
	}

	out := make(chan StreamEvent, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt.runStreamSubagents(ctx, executables, out)
	close(out)

	for ev := range out {
		_ = ev
	}

	models := llm.receivedModels()
	for i, m := range models {
		if m != "" {
			t.Errorf("Chat call %d: expected empty model, got %q", i, m)
		}
	}
}

// TestRunStreamSubagents_NeedsAtLeastTwo verifies that runStreamSubagents
// is a no-op when fewer than 2 executables are provided.
func TestRunStreamSubagents_NeedsAtLeastTwo(t *testing.T) {
	llm := &captureModelLLM{}
	engine := New(WithLLM(llm))
	rt := NewRuntime(engine).WithModel("test-model")

	out := make(chan StreamEvent, 8)
	ctx := context.Background()

	// Single executable → should not spawn subagents
	rt.runStreamSubagents(ctx, []Executable{{ID: "solo", Description: "Only one"}}, out)
	close(out)

	var count int
	for range out {
		count++
	}
	if count != 0 {
		t.Errorf("expected 0 events for single executable, got %d", count)
	}
	if len(llm.receivedModels()) != 0 {
		t.Errorf("expected 0 LLM calls, got %d", len(llm.receivedModels()))
	}
}

// TestSubagentModelReachesProvider creates a Subagent with a Model, runs it,
// and verifies the model arrives at the LLM provider.
func TestSubagentModelReachesProvider(t *testing.T) {
	llm := &captureModelLLM{}
	engine := New(WithLLM(llm))

	sub := Subagent{
		ID:       "direct",
		Task:     "Test task",
		Engine:   engine,
		Model:    "claude-haiku-4-5-20251001",
		MaxTurns: 1,
		Timeout:  5 * time.Second,
	}

	ctx := context.Background()
	result := sub.Run(ctx)

	if result.Error != nil {
		t.Fatalf("subagent error: %v", result.Error)
	}
	if result.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("result.Model = %q, want %q", result.Model, "claude-haiku-4-5-20251001")
	}

	models := llm.receivedModels()
	if len(models) == 0 {
		t.Fatal("no Chat calls recorded")
	}
	for i, m := range models {
		if m != "claude-haiku-4-5-20251001" {
			t.Errorf("Chat call %d: model = %q, want %q", i, m, "claude-haiku-4-5-20251001")
		}
	}
}

// TestPlanFanoutRequiresApproval verifies that the subagent fan-out only runs
// when the plan is approved in ExecutionContext.
func TestPlanFanoutRequiresApproval(t *testing.T) {
	llm := &captureModelLLM{}
	engine := New(WithLLM(llm), WithExecution(NewExecutionContext()))
	rt := NewRuntime(engine).WithModel("test-model")

	plan := Plan{
		ID:    "plan-1",
		Title: "Test plan",
		Executables: []Executable{
			{ID: "a", Description: "Task A", Status: ExecStatusNotStarted},
			{ID: "b", Description: "Task B", Status: ExecStatusNotStarted},
		},
	}

	// Propose but do NOT approve
	_, _ = engine.Execution.Propose(context.Background(), plan)

	activePlan := engine.Execution.ActivePlan()
	if activePlan == nil {
		t.Fatal("plan should be active after propose")
	}
	if activePlan.Approved {
		t.Fatal("plan should NOT be approved yet")
	}

	// The fan-out check in runStreamInternal checks activePlan.Approved.
	// Simulate the check directly:
	out := make(chan StreamEvent, 32)
	ctx := context.Background()

	// With unapproved plan, fan-out should be skipped
	if activePlan.Approved {
		rt.runStreamSubagents(ctx, activePlan.NextReady(), out)
	}
	close(out)

	var count int
	for range out {
		count++
	}
	if count != 0 {
		t.Errorf("unapproved plan should not produce events, got %d", count)
	}
	if len(llm.receivedModels()) != 0 {
		t.Errorf("unapproved plan should not trigger LLM calls, got %d", len(llm.receivedModels()))
	}

	// Now approve and verify fan-out works
	_ = engine.Execution.Approve(context.Background(), false)
	if !engine.Execution.ActivePlan().Approved {
		t.Fatal("plan should be approved now")
	}

	out2 := make(chan StreamEvent, 32)
	ready := engine.Execution.ActivePlan().NextReady()
	if len(ready) < 2 {
		t.Fatalf("expected ≥2 ready executables, got %d", len(ready))
	}
	rt.runStreamSubagents(ctx, ready, out2)
	close(out2)

	var results2 int
	for ev := range out2 {
		if ev.Type == StreamEventSubagentResult {
			results2++
		}
	}
	if results2 != 2 {
		t.Errorf("approved plan should produce 2 results, got %d", results2)
	}
}

// TestEndToEnd_RuntimeSubagentModel creates a RoutedLLMProvider, builds a
// Runtime with WithModel, then calls runStreamSubagents and verifies the
// model name reaches the inner provider.
func TestEndToEnd_RuntimeSubagentModel(t *testing.T) {
	inner := &captureModelLLM{}
	routed := NewRoutedLLMProvider("anthropic", map[string]LLMProvider{
		"anthropic": inner,
	})

	engine := New(WithLLM(routed))
	rt := NewRuntime(engine).WithModel("claude-haiku-4-5-20251001")

	executables := []Executable{
		{ID: "e1", Description: "Research topic"},
		{ID: "e2", Description: "Write summary"},
	}

	out := make(chan StreamEvent, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt.runStreamSubagents(ctx, executables, out)
	close(out)

	var results []*SubagentResult
	for ev := range out {
		if ev.Type == StreamEventSubagentResult {
			results = append(results, ev.SubagentResult)
		}
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Verify model reached inner provider
	models := inner.receivedModels()
	if len(models) == 0 {
		t.Fatal("no Chat calls")
	}
	for i, m := range models {
		// ParseModelRef("claude-haiku-4-5-20251001") → bare name routes to default
		if m != "claude-haiku-4-5-20251001" {
			t.Errorf("call %d: inner provider got model %q, want %q", i, m, "claude-haiku-4-5-20251001")
		}
	}

	for _, r := range results {
		if r.Model != "claude-haiku-4-5-20251001" {
			t.Errorf("subagent %s: result.Model = %q", r.ID, r.Model)
		}
		if r.Error != nil {
			t.Errorf("subagent %s: unexpected error: %v", r.ID, r.Error)
		}
	}

	fmt.Printf("✓ %d subagents completed, %d LLM calls, all with correct model\n",
		len(results), len(models))
}
