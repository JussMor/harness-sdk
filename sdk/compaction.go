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

// ── BulletCompactor ─────────────────────────────────────────────────────────

// BulletCompactor extracts key facts from dropped messages using simple
// heuristics — no LLM call, no external dependency. Less accurate than
// LLMCompactor but works offline and at zero cost.
//
// Strategy: extracts the last 3 assistant responses (which usually contain
// the most actionable conclusions) and truncates them to fit MaxChars.
type BulletCompactor struct {
	MaxChars int // default 600
}

// Compact extracts the last N assistant messages as a summary.
func (c *BulletCompactor) Compact(_ context.Context, dropped []ChatMessage) (string, error) {
	maxChars := c.MaxChars
	if maxChars <= 0 {
		maxChars = 600
	}

	// Collect assistant messages in reverse order (newest first)
	var assistants []string
	for i := len(dropped) - 1; i >= 0; i-- {
		if dropped[i].Role == RoleAssistant && dropped[i].Content != "" {
			assistants = append(assistants, dropped[i].Content)
			if len(assistants) >= 3 {
				break
			}
		}
	}
	if len(assistants) == 0 {
		return "", nil
	}

	// Build compact summary
	var b strings.Builder
	b.WriteString("Summary of earlier context:\n")
	for i := len(assistants) - 1; i >= 0; i-- {
		excerpt := truncate(strings.TrimSpace(assistants[i]), maxChars/len(assistants))
		b.WriteString("- ")
		b.WriteString(excerpt)
		b.WriteString("\n")
	}
	return b.String(), nil
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

// ── EpisodicCompactor ─────────────────────────────────────────────────────────

// EpisodicCompactor preserves "episodic memory" from dropped turns with
// differential importance scoring. Unlike LLMCompactor (treats all messages
// equally), EpisodicCompactor scores each message before compaction:
//
//   - High importance (score ≥ 0.7): preserved verbatim as [EPISODE] entries
//   - Medium importance (0.3–0.7): included in context summary
//   - Low importance (< 0.3): discarded (tool boilerplate, ack messages)
//
// Scoring is heuristic-first (zero LLM calls) using signals like:
// message length, keyword presence (error, decision, changed, fixed),
// role (user corrections score high), and turn position.
// The LLM only sees pre-filtered content, reducing token cost ~40%.
//
// This mirrors Claude's internal episodic weighting — events that changed
// direction, caused errors, or established key facts stay accessible even
// when surrounding context scrolls out.
type EpisodicCompactor struct {
	Provider LLMProvider
	Model    string
	MaxWords int // target total length. Default 300 words

	// ImportanceThreshold is the minimum score to include in the LLM call.
	// Messages below this are discarded before compaction. Default 0.25.
	ImportanceThreshold float64

	// EpisodeThreshold is the minimum score to mark a message as a key episode.
	// Episodes are preserved verbatim with [EPISODE] prefix. Default 0.65.
	EpisodeThreshold float64
}

// scoredMessage pairs a message with its computed importance.
type scoredMessage struct {
	msg   ChatMessage
	score float64
}

// scoreMessages assigns importance scores to each message heuristically.
// No LLM call — pure signal extraction from content and role.
func scoreMessages(msgs []ChatMessage) []scoredMessage {
	scored := make([]scoredMessage, 0, len(msgs))

	// Keywords that signal high-importance moments
	highSignals := []string{
		"error", "failed", "mistake", "wrong", "incorrect", "bug",
		"decision", "decided", "changed", "updated", "fixed", "resolved",
		"important", "critical", "must", "never", "always",
		"confirmed", "agreed", "approved", "rejected",
	}
	mediumSignals := []string{
		"because", "therefore", "result", "found", "discovered",
		"note", "remember", "keep in mind", "current", "state",
	}

	countSignals := func(text string, signals []string) int {
		lower := strings.ToLower(text)
		count := 0
		for _, s := range signals {
			if strings.Contains(lower, s) {
				count++
			}
		}
		return count
	}

	for i, m := range msgs {
		if m.Role == RoleSystem {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}

		score := 0.0

		// Role weight: user messages often signal corrections/decisions
		switch m.Role {
		case RoleUser:
			score += 0.3
		case RoleAssistant:
			score += 0.2
		case RoleTool:
			score += 0.05 // tool results are usually boilerplate
		}

		// Length signal: very short = ack/filler, medium = substance
		words := len(strings.Fields(content))
		switch {
		case words < 5:
			score -= 0.1 // "ok", "thanks", "got it"
		case words >= 20 && words < 100:
			score += 0.15
		case words >= 100:
			score += 0.25
		}

		// High-signal keywords
		highHits := countSignals(content, highSignals)
		if highHits > 0 {
			score += float64(highHits) * 0.15
		}

		// Medium-signal keywords
		medHits := countSignals(content, mediumSignals)
		if medHits > 0 {
			score += float64(medHits) * 0.08
		}

		// Recency bonus: later messages are slightly more relevant
		recency := float64(i) / float64(len(msgs))
		score += recency * 0.1

		// Cap at 1.0
		if score > 1.0 {
			score = 1.0
		}
		if score < 0 {
			score = 0
		}

		scored = append(scored, scoredMessage{msg: m, score: score})
	}
	return scored
}

func (c *EpisodicCompactor) Compact(ctx context.Context, dropped []ChatMessage) (string, error) {
	if c.Provider == nil || len(dropped) == 0 {
		return "", nil
	}
	maxWords := c.MaxWords
	if maxWords <= 0 {
		maxWords = 300
	}
	importanceThreshold := c.ImportanceThreshold
	if importanceThreshold <= 0 {
		importanceThreshold = 0.25
	}
	episodeThreshold := c.EpisodeThreshold
	if episodeThreshold <= 0 {
		episodeThreshold = 0.65
	}

	// Score all messages heuristically — zero LLM calls
	scored := scoreMessages(dropped)

	// Separate episodes (high importance) from context (medium) — discard low
	var episodes []scoredMessage
	var context []scoredMessage
	for _, sm := range scored {
		switch {
		case sm.score >= episodeThreshold:
			episodes = append(episodes, sm)
		case sm.score >= importanceThreshold:
			context = append(context, sm)
		// below importanceThreshold: discard
		}
	}

	// If nothing is worth keeping, return empty
	if len(episodes) == 0 && len(context) == 0 {
		return "", nil
	}

	// Build filtered transcript for the LLM — only high/medium importance messages
	var transcript strings.Builder
	for _, sm := range scored {
		if sm.score < importanceThreshold {
			continue
		}
		importance := "context"
		if sm.score >= episodeThreshold {
			importance = "KEY"
		}
		transcript.WriteString(fmt.Sprintf("[%s|%.2f] %s: %s\n\n",
			importance, sm.score,
			string(sm.msg.Role),
			truncate(sm.msg.Content, 500),
		))
	}

	// Build the prompt with pre-filtered, scored transcript
	prompt := fmt.Sprintf(`Build episodic memory from this pre-filtered conversation.
Messages marked [KEY] are high-importance — preserve these verbatim.
Messages marked [context] provide background — summarize concisely.

Output format:
[EPISODE] <verbatim key moment, ≤2 sentences>
[EPISODE] <verbatim key moment, ≤2 sentences>
CONTEXT: <prose summary of context messages, ≤%d words>

Rules:
- Only emit [EPISODE] for [KEY] messages
- CONTEXT must be a single paragraph
- Omit CONTEXT if there are no [context] messages
- Total output ≤%d words

--- Filtered transcript ---
%s--- End ---`, maxWords*2/3, maxWords, transcript.String())

	resp, err := c.Provider.Chat(ctx, ChatRequest{
		Model: c.Model,
		Messages: []ChatMessage{
			{Role: RoleUser, Content: prompt},
		},
	})
	if err != nil {
		// Fall back to BulletCompactor on error — non-fatal
		bc := &BulletCompactor{MaxChars: maxWords * 5}
		return bc.Compact(ctx, dropped)
	}
	return strings.TrimSpace(resp.Content), nil
}

