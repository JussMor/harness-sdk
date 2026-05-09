package autobuild

import (
	"context"
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
			// Anthropic
			"sk-ant-",
			// OpenAI
			"sk-proj-", "sk-",
			// GitHub
			"ghp_", "github_pat_", "ghs_", "gho_",
			// AWS
			"AKIA", "ASIA", "AROA",
			// Google / GCP
			"AIza", "ya29.",
			// Azure
			"DefaultEndpointsProtocol=", "AccountKey=",
			// Slack
			"xoxb-", "xoxp-", "xoxa-",
			// Stripe
			"sk_live_", "rk_live_", "sk_test_",
			// Cloudflare
			"eyJhbGciOiJSUzI1NiIsImtpZCI6Ikp", // Cloudflare JWT prefix
			// SendGrid / Twilio / other SaaS
			"SG.", "AC", "SK",
			// Generic high-entropy patterns
			"-----BEGIN RSA PRIVATE KEY-----",
			"-----BEGIN OPENSSH PRIVATE KEY-----",
			"-----BEGIN EC PRIVATE KEY-----",
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

// ApplyFilter runs a SafetyFilter against a batch of tool calls and
// separates them into allowed and blocked. Tool calls that get Block
// receive the Reason as their result content, so the LLM can see why
// and self-correct. SafetyPause is treated as Block here — the pause
// logic lives in HumanApprovalFilter.Inspect which blocks synchronously.
func ApplyFilter(filter SafetyFilter, ctx context.Context, calls []ToolCallEntry) (allowed []ToolCallEntry, blocked []ToolResult) {
	for _, c := range calls {
		v := filter.Inspect(ctx, c)
		switch v.Decision {
		case SafetyBlock, SafetyPause:
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

