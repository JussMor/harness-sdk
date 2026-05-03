package autobuild

import (
	"context"
	"fmt"
	"strings"
)

// SafetyFilter inspects a tool call before dispatch and decides whether
// to allow, block, or transform it. Multiple filters chain together —
// each one can short-circuit dispatch.
//
// This is the layer that prevents the LLM from executing dangerous commands
// (rm -rf /, leaking secrets, calling network endpoints it shouldn't).
// Without this, the SDK trusts the LLM completely.
type SafetyFilter interface {
	// Inspect returns the verdict for a tool call.
	// Allow: dispatch normally.
	// Block: skip dispatch, return Reason as the tool result.
	// Transform: dispatch with modified arguments.
	Inspect(ctx context.Context, call ToolCallEntry) SafetyVerdict
}

// SafetyVerdict is the outcome of a safety check.
type SafetyVerdict struct {
	Decision  SafetyDecision
	Reason    string
	NewArgs   string // JSON, only used when Decision == Transform
}

// SafetyDecision is what to do with a tool call.
type SafetyDecision int

const (
	SafetyAllow SafetyDecision = iota
	SafetyBlock
	SafetyTransform
)

// SafetyFilterFunc adapts a function to SafetyFilter.
type SafetyFilterFunc func(ctx context.Context, call ToolCallEntry) SafetyVerdict

func (f SafetyFilterFunc) Inspect(ctx context.Context, call ToolCallEntry) SafetyVerdict {
	return f(ctx, call)
}

// SafetyChain runs multiple filters in order. First Block wins. Transforms
// stack (each filter sees the previous filter's output).
type SafetyChain struct {
	filters []SafetyFilter
}

// NewSafetyChain composes filters into a chain.
func NewSafetyChain(filters ...SafetyFilter) *SafetyChain {
	return &SafetyChain{filters: filters}
}

// Inspect runs the chain.
func (c *SafetyChain) Inspect(ctx context.Context, call ToolCallEntry) SafetyVerdict {
	current := call
	for _, f := range c.filters {
		v := f.Inspect(ctx, current)
		switch v.Decision {
		case SafetyBlock:
			return v
		case SafetyTransform:
			current.Arguments = v.NewArgs
		}
	}
	return SafetyVerdict{Decision: SafetyAllow}
}

// ── Built-in filters ─────────────────────────────────────────────────────────

// DangerousCommandFilter blocks bash tool calls that contain dangerous
// patterns. Tunable — defaults are conservative.
type DangerousCommandFilter struct {
	// ToolNames is the set of tool names to inspect (default: ["bash", "shell", "exec"]).
	ToolNames []string

	// BlockedPatterns are substring matches that trigger a block.
	// Defaults include: rm -rf /, dd of=/dev/, mkfs, :(){ :|:&; }:, fork bombs.
	BlockedPatterns []string
}

// DefaultDangerousCommandFilter returns a filter with conservative defaults.
func DefaultDangerousCommandFilter() *DangerousCommandFilter {
	return &DangerousCommandFilter{
		ToolNames: []string{"bash", "shell", "exec", "run", "execute"},
		BlockedPatterns: []string{
			"rm -rf /",
			"rm -rf /*",
			"rm -rf ~",
			"rm -rf $HOME",
			"dd of=/dev/sd",
			"dd of=/dev/nvme",
			"dd of=/dev/hd",
			"mkfs.",
			":(){ :|:&};:", // fork bomb
			"> /dev/sda",
			"> /dev/nvme",
			"chmod -R 777 /",
			"chown -R", // dangerous when targeting /
			"curl | sh",
			"curl | bash",
			"wget -O- | sh",
		},
	}
}

func (f *DangerousCommandFilter) Inspect(_ context.Context, call ToolCallEntry) SafetyVerdict {
	if !f.matchesTool(call.Name) {
		return SafetyVerdict{Decision: SafetyAllow}
	}
	for _, pattern := range f.BlockedPatterns {
		if strings.Contains(call.Arguments, pattern) {
			return SafetyVerdict{
				Decision: SafetyBlock,
				Reason:   "blocked by safety filter: dangerous command pattern detected",
			}
		}
	}
	return SafetyVerdict{Decision: SafetyAllow}
}

func (f *DangerousCommandFilter) matchesTool(name string) bool {
	for _, t := range f.ToolNames {
		if t == name {
			return true
		}
	}
	return false
}

// SecretLeakFilter blocks tool calls whose arguments contain known secret patterns.
// Useful for preventing the LLM from echoing API keys, tokens, or env vars.
type SecretLeakFilter struct {
	// Patterns are substrings that should never appear in tool arguments.
	// Caller populates with known secret prefixes (sk_, ghp_, github_pat_, etc.).
	Patterns []string
}

// DefaultSecretLeakFilter returns a filter with common secret prefixes.
// Add your own via the Patterns field.
func DefaultSecretLeakFilter() *SecretLeakFilter {
	return &SecretLeakFilter{
		Patterns: []string{
			"sk-ant-",     // Anthropic
			"sk-proj-",    // OpenAI project
			"sk-",         // OpenAI
			"ghp_",        // GitHub classic PAT
			"github_pat_", // GitHub fine-grained PAT
			"AKIA",        // AWS access key
			"AIza",        // Google API key
			"xoxb-",       // Slack bot token
			"xoxp-",       // Slack user token
		},
	}
}

func (f *SecretLeakFilter) Inspect(_ context.Context, call ToolCallEntry) SafetyVerdict {
	for _, p := range f.Patterns {
		if strings.Contains(call.Arguments, p) {
			return SafetyVerdict{
				Decision: SafetyBlock,
				Reason:   "blocked by safety filter: potential secret in tool arguments",
			}
		}
	}
	return SafetyVerdict{Decision: SafetyAllow}
}

// ── Wellbeing detection ──────────────────────────────────────────────────────

// WellbeingSignal indicates that the user message contains content suggesting
// distress, crisis, or vulnerability that should adjust agent behavior.
type WellbeingSignal struct {
	Detected bool
	Category WellbeingCategory
	Severity WellbeingSeverity
	Phrases  []string // matched phrases for transparency
}

// WellbeingCategory describes what kind of distress was detected.
type WellbeingCategory string

const (
	WellbeingCategoryNone        WellbeingCategory = ""
	WellbeingCategorySelfHarm    WellbeingCategory = "self_harm"
	WellbeingCategoryCrisis      WellbeingCategory = "crisis"
	WellbeingCategoryDistress    WellbeingCategory = "distress"
	WellbeingCategoryEatingDisorder WellbeingCategory = "eating_disorder"
)

// WellbeingSeverity is the urgency level.
type WellbeingSeverity int

const (
	WellbeingSeverityNone WellbeingSeverity = iota
	WellbeingSeverityLow                    // sad, frustrated, venting
	WellbeingSeverityMedium                 // distress, struggling
	WellbeingSeverityHigh                   // crisis, immediate concern
)

// WellbeingDetector inspects user messages for distress signals.
// This is the layer that lets the agent adapt tone, avoid harmful suggestions,
// and surface support resources when appropriate.
//
// Note: this is a heuristic detector. It is NOT a substitute for clinical
// assessment. Its job is to give the agent a chance to respond appropriately,
// not to diagnose.
type WellbeingDetector interface {
	Detect(message string) WellbeingSignal
}

// DefaultWellbeingDetector implements simple keyword + phrase matching.
// Multilingual: includes English and Spanish patterns.
type DefaultWellbeingDetector struct{}

// Detect runs the heuristic check.
func (DefaultWellbeingDetector) Detect(message string) WellbeingSignal {
	lower := strings.ToLower(message)

	// High-severity phrases (self-harm / crisis)
	highPhrases := []string{
		// English
		"kill myself", "end my life", "suicide", "suicidal",
		"want to die", "don't want to live",
		"hurt myself", "cut myself",
		// Spanish
		"quitarme la vida", "matarme", "quiero morir",
		"no quiero vivir", "lastimarme",
	}
	if hits := matchAny(lower, highPhrases); len(hits) > 0 {
		return WellbeingSignal{
			Detected: true,
			Category: WellbeingCategorySelfHarm,
			Severity: WellbeingSeverityHigh,
			Phrases:  hits,
		}
	}

	// Eating disorder signals
	edPhrases := []string{
		"thinspo", "meanspo", "fitspo",
		"how to throw up", "how to purge", "how to not eat",
		"como vomitar", "como purgarme", "no comer",
	}
	if hits := matchAny(lower, edPhrases); len(hits) > 0 {
		return WellbeingSignal{
			Detected: true,
			Category: WellbeingCategoryEatingDisorder,
			Severity: WellbeingSeverityMedium,
			Phrases:  hits,
		}
	}

	// Medium-severity distress
	mediumPhrases := []string{
		"hopeless", "can't go on", "nothing matters",
		"sin esperanza", "no puedo más", "nada importa",
	}
	if hits := matchAny(lower, mediumPhrases); len(hits) > 0 {
		return WellbeingSignal{
			Detected: true,
			Category: WellbeingCategoryDistress,
			Severity: WellbeingSeverityMedium,
			Phrases:  hits,
		}
	}

	// Low-severity venting
	lowPhrases := []string{
		"i'm so sad", "i feel terrible", "having a bad day",
		"estoy tan triste", "me siento horrible", "mal día",
	}
	if hits := matchAny(lower, lowPhrases); len(hits) > 0 {
		return WellbeingSignal{
			Detected: true,
			Category: WellbeingCategoryDistress,
			Severity: WellbeingSeverityLow,
			Phrases:  hits,
		}
	}

	return WellbeingSignal{Detected: false}
}

func matchAny(text string, patterns []string) []string {
	var hits []string
	for _, p := range patterns {
		if strings.Contains(text, p) {
			hits = append(hits, p)
		}
	}
	return hits
}

// ApplyFilter runs a SafetyFilter against a batch of tool calls and
// separates them into allowed and blocked. Tool calls that get Block
// receive the Reason as their result content, so the LLM can see why
// and self-correct.
func ApplyFilter(filter SafetyFilter, ctx context.Context, calls []ToolCallEntry) (allowed []ToolCallEntry, blocked []ToolResult) {
	for _, c := range calls {
		v := filter.Inspect(ctx, c)
		switch v.Decision {
		case SafetyBlock:
			blocked = append(blocked, ToolResult{
				ToolCallID: c.ID,
				Name:       c.Name,
				Content:    "[blocked] " + v.Reason,
			})
		case SafetyTransform:
			c.Arguments = v.NewArgs
			allowed = append(allowed, c)
		default:
			allowed = append(allowed, c)
		}
	}
	return allowed, blocked
}

// ── Output filtering ─────────────────────────────────────────────────────────

// OutputFilter inspects the LLM's final response BEFORE it reaches the user
// or downstream consumers. This is the symmetric counterpart of SafetyFilter
// (which inspects tool calls): SafetyFilter protects the system from the LLM,
// OutputFilter protects the user from the LLM.
//
// Common uses:
//   - Strip leaked secrets or PII from the response
//   - Replace verbatim copyrighted content with a refusal
//   - Add disclaimers to sensitive content
//   - Block responses entirely if they violate policy
//
// Multiple filters chain — first Block wins. Transforms stack.
type OutputFilter interface {
	// Inspect returns the verdict for an output text.
	Inspect(ctx context.Context, output string) OutputVerdict
}

// OutputVerdict is the outcome of an output check.
type OutputVerdict struct {
	Decision  OutputDecision
	Reason    string
	NewOutput string // only used when Decision == OutputTransform
}

// OutputDecision is what to do with the LLM's output.
type OutputDecision int

const (
	OutputAllow OutputDecision = iota
	OutputBlock
	OutputTransform
)

// OutputFilterFunc adapts a function to OutputFilter.
type OutputFilterFunc func(ctx context.Context, output string) OutputVerdict

func (f OutputFilterFunc) Inspect(ctx context.Context, output string) OutputVerdict {
	return f(ctx, output)
}

// OutputFilterChain runs multiple filters in order. First Block wins.
// Transforms stack: each filter sees the previous filter's output.
type OutputFilterChain struct {
	filters []OutputFilter
}

// NewOutputFilterChain composes filters into a chain.
func NewOutputFilterChain(filters ...OutputFilter) *OutputFilterChain {
	return &OutputFilterChain{filters: filters}
}

// Inspect runs the chain.
func (c *OutputFilterChain) Inspect(ctx context.Context, output string) OutputVerdict {
	current := output
	for _, f := range c.filters {
		v := f.Inspect(ctx, current)
		switch v.Decision {
		case OutputBlock:
			return v
		case OutputTransform:
			current = v.NewOutput
		}
	}
	if current == output {
		return OutputVerdict{Decision: OutputAllow}
	}
	return OutputVerdict{Decision: OutputTransform, NewOutput: current}
}

// ── Built-in output filters ──────────────────────────────────────────────────

// SecretRedactionFilter scans the LLM's output for secret-looking patterns
// and redacts them. Symmetric counterpart of SecretLeakFilter (which
// inspects tool args).
type SecretRedactionFilter struct {
	Patterns []string
}

// DefaultSecretRedactionFilter returns a filter that redacts common token formats.
func DefaultSecretRedactionFilter() *SecretRedactionFilter {
	return &SecretRedactionFilter{
		Patterns: []string{
			"sk-ant-", "sk-proj-", "sk-",
			"ghp_", "github_pat_",
			"AKIA", "AIza",
			"xoxb-", "xoxp-",
		},
	}
}

func (f *SecretRedactionFilter) Inspect(_ context.Context, output string) OutputVerdict {
	redacted := output
	changed := false
	for _, p := range f.Patterns {
		for {
			idx := strings.Index(redacted, p)
			if idx < 0 {
				break
			}
			// Find end of token: whitespace, quote, or 80 chars max
			end := idx + len(p)
			for end < len(redacted) && end-idx < 80 && !isTokenBoundary(redacted[end]) {
				end++
			}
			redacted = redacted[:idx] + "[REDACTED]" + redacted[end:]
			changed = true
		}
	}
	if !changed {
		return OutputVerdict{Decision: OutputAllow}
	}
	return OutputVerdict{
		Decision:  OutputTransform,
		Reason:    "redacted secret-looking patterns",
		NewOutput: redacted,
	}
}

func isTokenBoundary(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '"' || c == '\'' ||
		c == '`' || c == ',' || c == ')' || c == ']' || c == '}'
}

// MaxLengthFilter blocks responses that exceed a length cap.
// Useful when you want hard limits on output size for cost or UI reasons.
type MaxLengthFilter struct {
	MaxChars int
}

func (f *MaxLengthFilter) Inspect(_ context.Context, output string) OutputVerdict {
	if f.MaxChars <= 0 || len(output) <= f.MaxChars {
		return OutputVerdict{Decision: OutputAllow}
	}
	return OutputVerdict{
		Decision: OutputBlock,
		Reason:   fmt.Sprintf("output exceeds %d char limit (got %d)", f.MaxChars, len(output)),
	}
}

// DisclaimerFilter appends a disclaimer to outputs containing certain triggers.
// Use for medical/legal/financial domains where the application requires it.
type DisclaimerFilter struct {
	Triggers   []string // case-insensitive substrings
	Disclaimer string   // appended at the end if any trigger matches
}

func (f *DisclaimerFilter) Inspect(_ context.Context, output string) OutputVerdict {
	if f.Disclaimer == "" {
		return OutputVerdict{Decision: OutputAllow}
	}
	lower := strings.ToLower(output)
	for _, t := range f.Triggers {
		if strings.Contains(lower, strings.ToLower(t)) {
			return OutputVerdict{
				Decision:  OutputTransform,
				Reason:    "added disclaimer for sensitive content",
				NewOutput: output + "\n\n" + f.Disclaimer,
			}
		}
	}
	return OutputVerdict{Decision: OutputAllow}
}
