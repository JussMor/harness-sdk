package autobuild

import (
	"context"
	"sync"
	"testing"
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

// TestAgentModelReachesProvider creates an Agent invocation with a Model,
// runs it, and verifies the model arrives at the LLM provider.
func TestAgentModelReachesProvider(t *testing.T) {
	llm := &captureModelLLM{}
	engine := New(WithLLM(llm))

	ag := &Agent{
		Type:     "direct",
		Body:     "Test task",
		Model:    "claude-haiku-4-5-20251001",
		MaxTurns: 1,
		Source:   AgentSourceFilesystem,
	}

	ctx := context.Background()
	result := runAgentInner(ctx, engine, ag, "test", "Test task", 1, "")

	if result.Error != nil {
		t.Fatalf("agent error: %v", result.Error)
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
