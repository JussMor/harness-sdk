package autobuild

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// ── Phase 10 — Compaction v3 ────────────────────────────────────────────────
//
// Agnostic port of Claude Code's services/compact/. The TypeScript reference
// (compact.ts, autoCompact.ts, prompt.ts) drives a structured 9-section
// summary with an `<analysis>` drafting scratchpad and a `<summary>` block
// the post-processor strips/rewrites. We mirror that contract here without
// pulling in any provider-specific assumptions.
//
// Pieces:
//   - CompactDirection      — "from" (default) | "up_to" (partial-up-to slice)
//   - CompactionResult      — structured outcome (raw + formatted summary)
//   - Compactor             — single-method interface, returns *CompactionResult
//   - LLMCompactor          — prompts the LLM with the structured 9-section prompt
//   - GetCompactPrompt      — Claude Code prompt.ts port (NO_TOOLS preamble + 9 sections)
//   - GetPartialCompactPrompt — variant scoped to a slice of messages
//   - FormatCompactSummary  — strips <analysis>, rewrites <summary>
//   - AutoCompactPolicy     — token-budget-driven trigger (warning / error / auto / blocking)
//   - EnforceWithCompaction — back-compat hook that bridges ContextBudget enforcement.

// CompactDirection mirrors Claude Code's PartialCompactDirection.
type CompactDirection string

const (
	// CompactDirectionFrom summarises everything from a marker forward
	// (the default for full compaction).
	CompactDirectionFrom CompactDirection = "from"
	// CompactDirectionUpTo summarises everything up to a marker, leaving
	// the recent tail intact.
	CompactDirectionUpTo CompactDirection = "up_to"
)

// CompactionResult is the outcome of a single Compact() call.
type CompactionResult struct {
	// Summary is the raw text the LLM produced (still contains <analysis>
	// and <summary> XML blocks).
	Summary string `json:"summary"`
	// FormattedSummary is the post-processed version: <analysis> stripped,
	// <summary> tags rewritten to plain section headers.
	FormattedSummary string `json:"formatted_summary"`
	// Direction reports whether this was a full ("from") or partial
	// ("up_to") compaction.
	Direction CompactDirection `json:"direction"`
	// TurnsSummarized is the number of dropped chat messages that were
	// folded into this summary.
	TurnsSummarized int `json:"turns_summarized"`
	// TokensIn is a rough estimate of the prompt tokens fed to the LLM.
	TokensIn int `json:"tokens_in,omitempty"`
	// Error captures non-fatal compaction failures (e.g. LLM timeout).
	// The runtime treats compaction errors as non-fatal: if Error != nil
	// the original messages have already been dropped by the budget pass,
	// so the caller should still surface FormattedSummary == "".
	Error error `json:"-"`
}

// Compactor summarises dropped conversation history into a compact memory
// entry. Returning a *CompactionResult instead of a bare string lets callers
// observe the analysis / summary split without re-parsing.
type Compactor interface {
	Compact(ctx context.Context, dropped []ChatMessage, direction CompactDirection) *CompactionResult
}

// ── Prompts (port of services/compact/prompt.ts) ────────────────────────────

// noToolsPreamble matches Claude Code's NO_TOOLS_PREAMBLE verbatim. The
// preamble is critical: aggressively-thinking models otherwise attempt tool
// calls during compaction, wasting the only turn allotted.
const noToolsPreamble = "CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.\n\n" +
	"- Do NOT use Read, Bash, Grep, Glob, Edit, Write, or ANY other tool.\n" +
	"- You already have all the context you need in the conversation above.\n" +
	"- Tool calls will be REJECTED and will waste your only turn — you will fail the task.\n" +
	"- Your entire response must be plain text: an <analysis> block followed by a <summary> block.\n\n"

// noToolsTrailer matches Claude Code's NO_TOOLS_TRAILER.
const noToolsTrailer = "\n\nREMINDER: Do NOT call any tools. Respond with plain text only — " +
	"an <analysis> block followed by a <summary> block. " +
	"Tool calls will be rejected and you will fail the task."

// detailedAnalysisInstructionBase mirrors DETAILED_ANALYSIS_INSTRUCTION_BASE.
const detailedAnalysisInstructionBase = `Before providing your final summary, wrap your analysis in <analysis> tags to organize your thoughts and ensure you've covered all necessary points. In your analysis process:

1. Chronologically analyze each message and section of the conversation. For each section thoroughly identify:
   - The user's explicit requests and intents
   - Your approach to addressing the user's requests
   - Key decisions, technical concepts and code patterns
   - Specific details like:
     - file names
     - full code snippets
     - function signatures
     - file edits
   - Errors that you ran into and how you fixed them
   - Pay special attention to specific user feedback that you received, especially if the user told you to do something differently.
2. Double-check for technical accuracy and completeness, addressing each required element thoroughly.`

// detailedAnalysisInstructionPartial mirrors DETAILED_ANALYSIS_INSTRUCTION_PARTIAL.
const detailedAnalysisInstructionPartial = `Before providing your final summary, wrap your analysis in <analysis> tags to organize your thoughts and ensure you've covered all necessary points. In your analysis process:

1. Analyze the recent messages chronologically. For each section thoroughly identify:
   - The user's explicit requests and intents
   - Your approach to addressing the user's requests
   - Key decisions, technical concepts and code patterns
   - Specific details like:
     - file names
     - full code snippets
     - function signatures
     - file edits
   - Errors that you ran into and how you fixed them
   - Pay special attention to specific user feedback that you received, especially if the user told you to do something differently.
2. Double-check for technical accuracy and completeness, addressing each required element thoroughly.`

// baseCompactPromptBody is the 9-section structured summary prompt body
// (ported from BASE_COMPACT_PROMPT in Claude Code). The example block is
// preserved so the model emits a well-shaped <summary>.
const baseCompactPromptBody = `Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and your previous actions.
This summary should be thorough in capturing technical details, code patterns, and architectural decisions that would be essential for continuing development work without losing context.

%s

Your summary should include the following sections:

1. Primary Request and Intent: Capture all of the user's explicit requests and intents in detail
2. Key Technical Concepts: List all important technical concepts, technologies, and frameworks discussed.
3. Files and Code Sections: Enumerate specific files and code sections examined, modified, or created. Pay special attention to the most recent messages and include full code snippets where applicable and include a summary of why this file read or edit is important.
4. Errors and fixes: List all errors that you ran into, and how you fixed them. Pay special attention to specific user feedback that you received, especially if the user told you to do something differently.
5. Problem Solving: Document problems solved and any ongoing troubleshooting efforts.
6. All user messages: List ALL user messages that are not tool results. These are critical for understanding the users' feedback and changing intent.
7. Pending Tasks: Outline any pending tasks that you have explicitly been asked to work on.
8. Current Work: Describe in detail precisely what was being worked on immediately before this summary request, paying special attention to the most recent messages from both user and assistant. Include file names and code snippets where applicable.
9. Optional Next Step: List the next step that you will take that is related to the most recent work you were doing. IMPORTANT: ensure that this step is DIRECTLY in line with the user's most recent explicit requests, and the task you were working on immediately before this summary request. If your last task was concluded, then only list next steps if they are explicitly in line with the users request. Do not start on tangential requests or really old requests that were already completed without confirming with the user first.
                       If there is a next step, include direct quotes from the most recent conversation showing exactly what task you were working on and where you left off. This should be verbatim to ensure there's no drift in task interpretation.

Here's an example of how your output should be structured:

<example>
<analysis>
[Your thought process, ensuring all points are covered thoroughly and accurately]
</analysis>

<summary>
1. Primary Request and Intent:
   [Detailed description]

2. Key Technical Concepts:
   - [Concept 1]
   - [Concept 2]

3. Files and Code Sections:
   - [File Name 1]
      - [Summary of why this file is important]
      - [Important Code Snippet]

4. Errors and fixes:
    - [Error description]:
      - [How you fixed it]

5. Problem Solving:
   [Description]

6. All user messages:
    - [Detailed non tool use user message]

7. Pending Tasks:
   - [Task 1]

8. Current Work:
   [Description of work in progress]

9. Optional Next Step:
   [Verbatim quote + planned next step]

</summary>
</example>

Please provide your summary following this structure, ensuring precision and thoroughness in your response.
`

// partialCompactPromptBody is the slice variant — same 9 sections but the
// final two are reframed for "this slice only" instead of "the full convo".
const partialCompactPromptBody = `Your task is to create a detailed summary of the recent portion of the conversation, paying close attention to the user's explicit requests and your previous actions in this slice.
This summary should be thorough in capturing technical details, code patterns, and architectural decisions that would be essential for continuing development work without losing context.

%s

Your summary should include the following sections:

1. Primary Request and Intent: Capture all of the user's explicit requests and intents within this slice in detail.
2. Key Technical Concepts: List all important technical concepts, technologies, and frameworks discussed.
3. Files and Code Sections: Enumerate specific files and code sections examined, modified, or created in this slice.
4. Errors and fixes: List all errors that you ran into within this slice, and how you fixed them.
5. Problem Solving: Document problems solved within this slice and any ongoing troubleshooting efforts.
6. All user messages: List ALL user messages from this slice that are not tool results.
7. Pending Tasks: Outline any pending tasks.
8. Work Completed: Describe what was accomplished by the end of this portion.
9. Context for Continuing Work: Summarize any context, decisions, or state that would be needed to understand and continue the work in subsequent messages.
`

// GetCompactPrompt returns the full compaction prompt (Claude Code parity).
// customInstructions, if non-empty, is appended verbatim before the trailer.
func GetCompactPrompt(customInstructions string) string {
	body := fmt.Sprintf(baseCompactPromptBody, detailedAnalysisInstructionBase)
	prompt := noToolsPreamble + body
	if s := strings.TrimSpace(customInstructions); s != "" {
		prompt += "\n\nAdditional Instructions:\n" + s
	}
	return prompt + noToolsTrailer
}

// GetPartialCompactPrompt is the slice variant of GetCompactPrompt.
func GetPartialCompactPrompt(customInstructions string, direction CompactDirection) string {
	body := fmt.Sprintf(partialCompactPromptBody, detailedAnalysisInstructionPartial)
	prompt := noToolsPreamble + body
	if s := strings.TrimSpace(customInstructions); s != "" {
		prompt += "\n\nAdditional Instructions:\n" + s
	}
	return prompt + noToolsTrailer
}

// ── Post-processing (port of formatCompactSummary) ──────────────────────────

var (
	analysisBlockRe = regexp.MustCompile(`(?s)<analysis>.*?</analysis>`)
	summaryOpenRe   = regexp.MustCompile(`(?i)<summary>\s*`)
	summaryCloseRe  = regexp.MustCompile(`(?i)\s*</summary>`)
)

// FormatCompactSummary mirrors Claude Code's formatCompactSummary:
//   - Strips the <analysis> drafting scratchpad.
//   - Replaces the <summary> XML wrapper with a plain "Conversation summary:"
//     header so downstream prompts can append it directly.
func FormatCompactSummary(raw string) string {
	out := analysisBlockRe.ReplaceAllString(raw, "")
	out = summaryOpenRe.ReplaceAllString(out, "Conversation summary:\n")
	out = summaryCloseRe.ReplaceAllString(out, "")
	return strings.TrimSpace(out)
}

// ── LLMCompactor ────────────────────────────────────────────────────────────

// LLMCompactor calls an LLM with the structured 9-section prompt. Drop-in
// replacement for the v2 implementation; existing callers using
// `&LLMCompactor{Provider, Model}` continue to compile (MaxWords is now
// ignored in favour of the structured prompt).
type LLMCompactor struct {
	// Provider is the LLM backend used for summarisation. Required.
	Provider LLMProvider
	// Model is the model name to send. A small, cheap model is recommended.
	Model string
	// CustomInstructions is appended to the prompt verbatim before the
	// no-tools trailer. Use to inject project-specific guidance (e.g.
	// "preserve all SQL schema definitions in section 3").
	CustomInstructions string
	// MaxWords is retained for backwards compatibility but no longer
	// affects the prompt — the structured prompt sets its own length
	// expectations through the section schema.
	MaxWords int
	// Direction overrides the default compaction direction. When unset
	// the direction passed to Compact() wins.
	Direction CompactDirection
}

// Compact runs the structured summary call.
func (c *LLMCompactor) Compact(ctx context.Context, dropped []ChatMessage, direction CompactDirection) *CompactionResult {
	res := &CompactionResult{Direction: direction, TurnsSummarized: len(dropped)}
	if c == nil || c.Provider == nil || len(dropped) == 0 {
		return res
	}
	if c.Direction != "" {
		res.Direction = c.Direction
		direction = c.Direction
	}

	// Render dropped turns as a transcript. We follow Claude Code's lead:
	// keep system messages out (they're the runtime's own scaffolding) and
	// truncate giant tool blobs so a single 4 MB tool result can't blow
	// the compaction call past its own context window.
	var transcript strings.Builder
	for _, m := range dropped {
		if m.Role == RoleSystem {
			continue
		}
		transcript.WriteString(string(m.Role))
		transcript.WriteString(": ")
		transcript.WriteString(compactTruncate(m.Content, 4000))
		transcript.WriteString("\n\n")
	}
	if transcript.Len() == 0 {
		return res
	}

	var sys string
	if direction == CompactDirectionUpTo {
		sys = GetPartialCompactPrompt(c.CustomInstructions, direction)
	} else {
		sys = GetCompactPrompt(c.CustomInstructions)
	}

	user := "--- Conversation excerpt ---\n" + transcript.String() + "--- End ---\n\nProduce the <analysis> + <summary> response now."

	res.TokensIn = (len(sys) + len(user)) / 4 // rough estimate

	resp, err := c.Provider.Chat(ctx, ChatRequest{
		Model: c.Model,
		Messages: []ChatMessage{
			{Role: RoleSystem, Content: sys},
			{Role: RoleUser, Content: user},
		},
	})
	if err != nil {
		res.Error = fmt.Errorf("compact: LLM: %w", err)
		return res
	}
	if resp == nil {
		res.Error = fmt.Errorf("compact: LLM returned nil response")
		return res
	}
	res.Summary = strings.TrimSpace(resp.Content)
	res.FormattedSummary = FormatCompactSummary(res.Summary)
	return res
}

// compactTruncate is a local helper (was previously shared with closure.go,
// deleted in v3 demolition).
func compactTruncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// ── AutoCompactPolicy (port of autoCompact.ts thresholds) ───────────────────

// CompactionThresholds is the per-model token-window configuration (port of
// getEffectiveContextWindowSize + getAutoCompactThreshold).
//
// Defaults match Claude Code's constants:
//   - AutoBufferTokens     = 13_000
//   - WarningBufferTokens  = 20_000
//   - ErrorBufferTokens    = 20_000
//   - ManualBufferTokens   = 3_000
//   - SummaryReserveTokens = 20_000
type CompactionThresholds struct {
	// ContextWindow is the model's full context-window size (tokens).
	// Required: callers must set this since the SDK does not ship a
	// per-model registry (it stays agnostic).
	ContextWindow int
	// SummaryReserveTokens reserves headroom for the compaction call's
	// own output. Default 20_000 (matches Claude Code's
	// MAX_OUTPUT_TOKENS_FOR_SUMMARY).
	SummaryReserveTokens int
	// AutoBufferTokens is the trigger threshold below the effective
	// window. Default 13_000.
	AutoBufferTokens int
	// WarningBufferTokens flags "approaching limit". Default 20_000.
	WarningBufferTokens int
	// ErrorBufferTokens flags "very near limit". Default 20_000.
	ErrorBufferTokens int
	// ManualBufferTokens is the buffer reserved for the user's manual
	// /compact invocation. Default 3_000.
	ManualBufferTokens int
}

// effective applies defaults on read.
func (t CompactionThresholds) effective() CompactionThresholds {
	if t.SummaryReserveTokens <= 0 {
		t.SummaryReserveTokens = 20_000
	}
	if t.AutoBufferTokens <= 0 {
		t.AutoBufferTokens = 13_000
	}
	if t.WarningBufferTokens <= 0 {
		t.WarningBufferTokens = 20_000
	}
	if t.ErrorBufferTokens <= 0 {
		t.ErrorBufferTokens = 20_000
	}
	if t.ManualBufferTokens <= 0 {
		t.ManualBufferTokens = 3_000
	}
	return t
}

// EffectiveContextWindow returns ContextWindow - SummaryReserveTokens.
// Mirrors getEffectiveContextWindowSize in autoCompact.ts.
func (t CompactionThresholds) EffectiveContextWindow() int {
	t = t.effective()
	w := t.ContextWindow - t.SummaryReserveTokens
	if w < 0 {
		return 0
	}
	return w
}

// AutoCompactThreshold returns the token usage at which auto-compaction
// should fire. Mirrors getAutoCompactThreshold.
func (t CompactionThresholds) AutoCompactThreshold() int {
	t = t.effective()
	v := t.EffectiveContextWindow() - t.AutoBufferTokens
	if v < 0 {
		return 0
	}
	return v
}

// TokenWarningState is the result of CalculateTokenWarningState. Mirrors
// the shape of calculateTokenWarningState in autoCompact.ts.
type TokenWarningState struct {
	PercentLeft               int  `json:"percent_left"`
	IsAboveWarningThreshold   bool `json:"is_above_warning_threshold"`
	IsAboveErrorThreshold     bool `json:"is_above_error_threshold"`
	IsAboveAutoCompactThreshold bool `json:"is_above_auto_compact_threshold"`
	IsAtBlockingLimit         bool `json:"is_at_blocking_limit"`
}

// CalculateTokenWarningState returns the threshold state for the given
// token usage. autoEnabled mirrors isAutoCompactEnabled().
func (t CompactionThresholds) CalculateTokenWarningState(tokenUsage int, autoEnabled bool) TokenWarningState {
	tt := t.effective()
	autoThresh := tt.AutoCompactThreshold()
	threshold := autoThresh
	if !autoEnabled {
		threshold = tt.EffectiveContextWindow()
	}
	if threshold < 1 {
		threshold = 1
	}
	left := ((threshold - tokenUsage) * 100) / threshold
	if left < 0 {
		left = 0
	}
	warningThresh := threshold - tt.WarningBufferTokens
	errorThresh := threshold - tt.ErrorBufferTokens
	blockingLimit := tt.EffectiveContextWindow() - tt.ManualBufferTokens
	return TokenWarningState{
		PercentLeft:                 left,
		IsAboveWarningThreshold:     tokenUsage >= warningThresh,
		IsAboveErrorThreshold:       tokenUsage >= errorThresh,
		IsAboveAutoCompactThreshold: autoEnabled && tokenUsage >= autoThresh,
		IsAtBlockingLimit:           tokenUsage >= blockingLimit,
	}
}

// AutoCompactPolicy is the optional auto-trigger glue. ShouldAutoCompact
// is the equivalent of shouldAutoCompact() in autoCompact.ts; it includes
// a circuit breaker matching MAX_CONSECUTIVE_AUTOCOMPACT_FAILURES.
type AutoCompactPolicy struct {
	// Thresholds defines per-model token windows. Required.
	Thresholds CompactionThresholds
	// Enabled is the master switch (mirrors isAutoCompactEnabled). When
	// false, ShouldAutoCompact always returns false.
	Enabled bool
	// MaxConsecutiveFailures stops auto-compaction after N straight
	// failures (default 3, matching Claude Code).
	MaxConsecutiveFailures int

	consecutiveFailures int
}

// ShouldAutoCompact returns true when token usage crosses the auto-compact
// threshold AND the circuit breaker hasn't tripped.
func (p *AutoCompactPolicy) ShouldAutoCompact(tokenUsage int) bool {
	if p == nil || !p.Enabled {
		return false
	}
	max := p.MaxConsecutiveFailures
	if max <= 0 {
		max = 3
	}
	if p.consecutiveFailures >= max {
		return false
	}
	state := p.Thresholds.CalculateTokenWarningState(tokenUsage, true)
	return state.IsAboveAutoCompactThreshold
}

// RecordSuccess resets the circuit breaker after a successful compaction.
func (p *AutoCompactPolicy) RecordSuccess() {
	if p == nil {
		return
	}
	p.consecutiveFailures = 0
}

// RecordFailure increments the circuit breaker counter.
func (p *AutoCompactPolicy) RecordFailure() {
	if p == nil {
		return
	}
	p.consecutiveFailures++
}

// ── Integration with ContextBudget ──────────────────────────────────────────

// EnforceCompactionResult bundles the budget enforcement outcome with the
// compaction summary. Back-compat shim so callers that previously consumed
// EnforceWithCompaction continue to work; the runtime now reads
// CompactionResult.FormattedSummary instead of the bare Summary string.
type EnforceCompactionResult struct {
	*EnforcementResult
	// Compaction is non-nil when at least one message was dropped AND a
	// compactor was configured. May still carry an Error field — see
	// CompactionResult.Error.
	Compaction *CompactionResult
	// Summary is FormattedSummary when Compaction != nil, "" otherwise.
	// Retained for back-compat with existing runtime call sites.
	Summary string
}

// EnforceWithCompaction runs the budget pass and, when history is truncated,
// asks the compactor to summarise the dropped messages.
func EnforceWithCompaction(
	ctx context.Context,
	budget *ContextBudget,
	compactor Compactor,
	conv *Conversation,
	memoryTokens int,
) *EnforceCompactionResult {
	if budget == nil {
		return &EnforceCompactionResult{EnforcementResult: &EnforcementResult{}}
	}

	// Snapshot history before enforcement so we can identify drops.
	histBefore := make([]ChatMessage, len(conv.Messages))
	copy(histBefore, conv.Messages)

	enforce := budget.Enforce(ctx, conv, memoryTokens, &conv.Messages)
	out := &EnforceCompactionResult{EnforcementResult: enforce}
	if !enforce.TruncatedHistory || compactor == nil {
		return out
	}

	kept := make(map[int]bool)
	for _, m := range conv.Messages {
		for i, b := range histBefore {
			if !kept[i] && b.Role == m.Role && b.Content == m.Content {
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
		return out
	}
	out.Compaction = compactor.Compact(ctx, dropped, CompactDirectionFrom)
	if out.Compaction != nil && out.Compaction.Error == nil {
		out.Summary = out.Compaction.FormattedSummary
	}
	return out
}
