package autobuild

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ── Prompt assembly ─────────────────────────────────────────────────────────

func TestGetCompactPrompt_HasNoToolsGuardsAndAllSections(t *testing.T) {
	p := GetCompactPrompt("")
	if !strings.HasPrefix(p, "CRITICAL: Respond with TEXT ONLY") {
		t.Fatalf("missing NO_TOOLS_PREAMBLE: %q", p[:80])
	}
	if !strings.HasSuffix(p, "you will fail the task.") {
		t.Fatalf("missing trailer: %q", p[len(p)-40:])
	}
	for _, header := range []string{
		"1. Primary Request and Intent",
		"2. Key Technical Concepts",
		"3. Files and Code Sections",
		"4. Errors and fixes",
		"5. Problem Solving",
		"6. All user messages",
		"7. Pending Tasks",
		"8. Current Work",
		"9. Optional Next Step",
		"<analysis>",
		"<summary>",
	} {
		if !strings.Contains(p, header) {
			t.Errorf("compact prompt missing %q", header)
		}
	}
}

func TestGetCompactPrompt_AppendsCustomInstructions(t *testing.T) {
	p := GetCompactPrompt("preserve all SQL schemas")
	if !strings.Contains(p, "Additional Instructions:\npreserve all SQL schemas") {
		t.Fatal("custom instructions not appended")
	}
}

func TestGetPartialCompactPrompt_HasSliceSpecificSections(t *testing.T) {
	p := GetPartialCompactPrompt("", CompactDirectionFrom)
	if !strings.Contains(p, "8. Work Completed") {
		t.Fatal("partial prompt missing 'Work Completed' section")
	}
	if !strings.Contains(p, "9. Context for Continuing Work") {
		t.Fatal("partial prompt missing 'Context for Continuing Work' section")
	}
}

// ── FormatCompactSummary ────────────────────────────────────────────────────

func TestFormatCompactSummary_StripsAnalysisAndRewritesSummaryTags(t *testing.T) {
	raw := "<analysis>scratchpad notes\nmore notes</analysis>\n<summary>\n1. Primary Request: do thing\n</summary>"
	got := FormatCompactSummary(raw)
	if strings.Contains(got, "<analysis>") || strings.Contains(got, "scratchpad") {
		t.Fatalf("analysis not stripped: %q", got)
	}
	if strings.Contains(got, "<summary>") || strings.Contains(got, "</summary>") {
		t.Fatalf("summary tags not stripped: %q", got)
	}
	if !strings.HasPrefix(got, "Conversation summary:") {
		t.Fatalf("missing replacement header: %q", got)
	}
}

func TestFormatCompactSummary_NoTagsLeavesContentIntact(t *testing.T) {
	got := FormatCompactSummary("plain text only")
	if got != "plain text only" {
		t.Fatalf("unexpected mutation: %q", got)
	}
}

// ── LLMCompactor ────────────────────────────────────────────────────────────

type fakeLLM struct {
	systemSeen string
	userSeen   string
	reply      string
	err        error
}

func (f *fakeLLM) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	for _, m := range req.Messages {
		if m.Role == RoleSystem {
			f.systemSeen = m.Content
		}
		if m.Role == RoleUser {
			f.userSeen = m.Content
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return &ChatResponse{Content: f.reply}, nil
}

func TestLLMCompactor_FullCompactSendsBasePrompt(t *testing.T) {
	llm := &fakeLLM{reply: "<analysis>x</analysis><summary>S</summary>"}
	c := &LLMCompactor{Provider: llm, Model: "test"}
	res := c.Compact(context.Background(), []ChatMessage{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi"},
	}, CompactDirectionFrom)
	if res.Error != nil {
		t.Fatalf("unexpected err: %v", res.Error)
	}
	if !strings.Contains(llm.systemSeen, "8. Current Work") {
		t.Fatal("base prompt not sent")
	}
	if !strings.Contains(llm.userSeen, "user: hello") {
		t.Fatalf("transcript missing user line: %q", llm.userSeen)
	}
	if !strings.HasPrefix(res.FormattedSummary, "Conversation summary:") {
		t.Fatalf("formatted summary not produced: %q", res.FormattedSummary)
	}
	if res.TurnsSummarized != 2 {
		t.Fatalf("turns=%d want 2", res.TurnsSummarized)
	}
}

func TestLLMCompactor_PartialDirectionSendsPartialPrompt(t *testing.T) {
	llm := &fakeLLM{reply: "<summary>ok</summary>"}
	c := &LLMCompactor{Provider: llm, Model: "test"}
	c.Compact(context.Background(), []ChatMessage{{Role: RoleUser, Content: "x"}}, CompactDirectionUpTo)
	if !strings.Contains(llm.systemSeen, "8. Work Completed") {
		t.Fatal("partial prompt not sent")
	}
}

func TestLLMCompactor_LLMErrorCapturedNotPanicked(t *testing.T) {
	llm := &fakeLLM{err: errors.New("boom")}
	c := &LLMCompactor{Provider: llm, Model: "test"}
	res := c.Compact(context.Background(), []ChatMessage{{Role: RoleUser, Content: "x"}}, CompactDirectionFrom)
	if res.Error == nil {
		t.Fatal("expected error")
	}
	if res.FormattedSummary != "" {
		t.Fatal("expected empty summary on error")
	}
}

func TestLLMCompactor_EmptyDroppedReturnsNoOp(t *testing.T) {
	llm := &fakeLLM{}
	c := &LLMCompactor{Provider: llm, Model: "test"}
	res := c.Compact(context.Background(), nil, CompactDirectionFrom)
	if llm.systemSeen != "" {
		t.Fatal("LLM should not have been called")
	}
	if res.TurnsSummarized != 0 {
		t.Fatalf("turns=%d", res.TurnsSummarized)
	}
}

func TestLLMCompactor_LongMessagesAreTruncated(t *testing.T) {
	llm := &fakeLLM{reply: "<summary>ok</summary>"}
	c := &LLMCompactor{Provider: llm, Model: "test"}
	huge := strings.Repeat("a", 10_000)
	c.Compact(context.Background(), []ChatMessage{{Role: RoleUser, Content: huge}}, CompactDirectionFrom)
	if !strings.Contains(llm.userSeen, "...") {
		t.Fatal("expected truncation marker in transcript")
	}
}

// ── AutoCompactPolicy / Thresholds ──────────────────────────────────────────

func TestCompactionThresholds_Defaults(t *testing.T) {
	th := CompactionThresholds{ContextWindow: 200_000}
	if got := th.EffectiveContextWindow(); got != 180_000 {
		t.Fatalf("effective window=%d want 180000", got)
	}
	if got := th.AutoCompactThreshold(); got != 167_000 {
		t.Fatalf("auto threshold=%d want 167000", got)
	}
}

func TestAutoCompactPolicy_TriggersAtThreshold(t *testing.T) {
	p := &AutoCompactPolicy{
		Thresholds: CompactionThresholds{ContextWindow: 200_000},
		Enabled:    true,
	}
	if p.ShouldAutoCompact(100_000) {
		t.Fatal("should not trigger well below threshold")
	}
	if !p.ShouldAutoCompact(170_000) {
		t.Fatal("should trigger above threshold")
	}
}

func TestAutoCompactPolicy_DisabledNeverFires(t *testing.T) {
	p := &AutoCompactPolicy{
		Thresholds: CompactionThresholds{ContextWindow: 200_000},
		Enabled:    false,
	}
	if p.ShouldAutoCompact(190_000) {
		t.Fatal("disabled policy must never fire")
	}
}

func TestAutoCompactPolicy_CircuitBreakerStopsAfterFailures(t *testing.T) {
	p := &AutoCompactPolicy{
		Thresholds:             CompactionThresholds{ContextWindow: 200_000},
		Enabled:                true,
		MaxConsecutiveFailures: 2,
	}
	p.RecordFailure()
	if !p.ShouldAutoCompact(170_000) {
		t.Fatal("breaker should still allow at 1 failure")
	}
	p.RecordFailure()
	if p.ShouldAutoCompact(170_000) {
		t.Fatal("breaker should trip at 2 failures")
	}
	p.RecordSuccess()
	if !p.ShouldAutoCompact(170_000) {
		t.Fatal("RecordSuccess should reset breaker")
	}
}

func TestCalculateTokenWarningState_ThresholdFlags(t *testing.T) {
	th := CompactionThresholds{ContextWindow: 200_000}
	st := th.CalculateTokenWarningState(170_000, true)
	if !st.IsAboveAutoCompactThreshold {
		t.Fatal("expected auto threshold flag")
	}
	if st.PercentLeft >= 5 {
		t.Fatalf("percentLeft=%d expected near 0", st.PercentLeft)
	}
	st2 := th.CalculateTokenWarningState(50_000, true)
	if st2.IsAboveWarningThreshold || st2.IsAboveAutoCompactThreshold {
		t.Fatal("low usage should not trip thresholds")
	}
}
