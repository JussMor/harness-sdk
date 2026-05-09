package autobuild

import (
	"context"
	"fmt"
	"strings"
)

// Compactor summarizes dropped conversation history into a compact memory
// entry instead of silently discarding it. When the context budget enforces
// truncation, old messages are gone from the active window — but their
// content shouldn't be lost entirely.
//
// This mirrors how Claude handles long context: older turns get summarized
// and the summary is kept as a layered memory entry. Without this, the agent
// loses facts from early in the conversation the moment they scroll out.
//
// The Compactor runs after Budget.Enforce and before the next LLM call.
// It receives the dropped messages and returns a compact summary string
// that gets injected into LayerMemory (overwriting or appending).
type Compactor interface {
	// Compact receives the messages being dropped and returns a summary.
	// Return empty string to skip injection (e.g. if the messages are trivial).
	Compact(ctx context.Context, dropped []ChatMessage) (string, error)
}

// ── LLMCompactor ────────────────────────────────────────────────────────────

// LLMCompactor asks the LLM to summarize the dropped messages.
// Produces a compact summary with facts and decisions made in those turns.
type LLMCompactor struct {
	Provider  LLMProvider
	Model     string // small model recommended — compression is cheap
	MaxWords  int    // target summary length. Default 200 words
}

// Compact builds a summary from the dropped messages.
func (c *LLMCompactor) Compact(ctx context.Context, dropped []ChatMessage) (string, error) {
	if c.Provider == nil || len(dropped) == 0 {
		return "", nil
	}
	maxWords := c.MaxWords
	if maxWords <= 0 {
		maxWords = 200
	}

	// Build a transcript of the dropped turns
	var transcript strings.Builder
	for _, m := range dropped {
		if m.Role == RoleSystem {
			continue
		}
		transcript.WriteString(string(m.Role))
		transcript.WriteString(": ")
		transcript.WriteString(truncate(m.Content, 400))
		transcript.WriteString("\n\n")
	}
	if transcript.Len() == 0 {
		return "", nil
	}

	prompt := fmt.Sprintf(`Summarize the following conversation excerpt in %d words or fewer.
Focus on: decisions made, facts established, current state of work, open questions.
Omit: pleasantries, repetition, tool call boilerplate.
Output plain text — no headers, no bullets.

--- Excerpt ---
%s
--- End ---

Summary:`, maxWords, transcript.String())

	resp, err := c.Provider.Chat(ctx, ChatRequest{
		Model: c.Model,
		Messages: []ChatMessage{
			{Role: RoleUser, Content: prompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("compact: LLM: %w", err)
	}
	return strings.TrimSpace(resp.Content), nil
}

// ── Integration with ContextBudget ──────────────────────────────────────────

// EnforceWithCompaction extends Budget.Enforce to run a Compactor on the
// dropped messages before discarding them. The summary is injected as an
// additional paragraph in the memory layer.
//
// Usage in Runtime.preparation:
//
//	enforce := EnforceWithCompaction(ctx, budget, compactor, conv, skills, ...)
//	if enforce.Summary != "" {
//	    engine.Prompt.Append(LayerMemory, "Context summary:\n"+enforce.Summary)
//	}
type EnforceCompactionResult struct {
	*EnforcementResult
	// Summary is the compacted text of the dropped messages.
	// Empty if no messages were dropped or compactor returned nothing.
	Summary string
}

// EnforceWithCompaction runs budget enforcement and compacts any dropped messages.
func EnforceWithCompaction(
	ctx context.Context,
	budget *ContextBudget,
	compactor Compactor,
	conv *Conversation,
	skills SkillProvider,
	skillTokens, memoryTokens int,
) *EnforceCompactionResult {
	if budget == nil {
		return &EnforceCompactionResult{EnforcementResult: &EnforcementResult{}}
	}

	// Snapshot history before enforcement
	histBefore := make([]ChatMessage, len(conv.Messages))
	copy(histBefore, conv.Messages)

	enforce := budget.Enforce(ctx, conv, skills, skillTokens, memoryTokens, &conv.Messages)
	result := &EnforceCompactionResult{EnforcementResult: enforce}

	if !enforce.TruncatedHistory || compactor == nil {
		return result
	}

	// Identify dropped messages: those in histBefore but not in conv.Messages
	kept := make(map[int]bool)
	for _, m := range conv.Messages {
		for i, b := range histBefore {
			if b.Role == m.Role && b.Content == m.Content {
				kept[i] = true
				break
			}
		}
	}
	var dropped []ChatMessage
	for i, m := range histBefore {
		if !kept[i] {
			dropped = append(dropped, m)
		}
	}
	if len(dropped) == 0 {
		return result
	}

	summary, err := compactor.Compact(ctx, dropped)
	if err != nil {
		// Compaction failure is non-fatal — enforcement already happened
		return result
	}
	result.Summary = summary
	return result
}


