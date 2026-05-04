package autobuild

import (
	"context"
	"fmt"
	"strings"
)

// VerificationStrategy decides whether the agent's output is acceptable
// before transitioning to Closure. This is what makes phase 4 meaningful
// instead of a no-op.
//
// Implementations can run tests, validate output structure, check the LLM's
// own claims, or call back to the LLM with verification questions.
//
// Returning Verdict{Pass: false, Retry: true} causes Runtime to send the
// failure message back to the LLM (still in Execution) and try again.
// Returning Verdict{Pass: false, Retry: false} surfaces the failure to
// the caller as a runtime error.
type VerificationStrategy interface {
	// Verify inspects the result of an agent loop and returns a verdict.
	Verify(ctx context.Context, result *AgentLoopResult, conv *Conversation) Verdict
}

// Verdict is the outcome of a verification check.
type Verdict struct {
	// Pass is true if the result is acceptable.
	Pass bool

	// Reason explains the verdict (always set, even on Pass).
	Reason string

	// Retry asks Runtime to send the failure back to the LLM for another try.
	// Only meaningful when Pass is false. Capped by MaxVerificationRetries.
	Retry bool

	// Details are surfaced to the caller in the RuntimeResult.
	Details []string
}

// VerificationStrategyFunc adapts a function to VerificationStrategy.
type VerificationStrategyFunc func(ctx context.Context, result *AgentLoopResult, conv *Conversation) Verdict

func (f VerificationStrategyFunc) Verify(ctx context.Context, r *AgentLoopResult, c *Conversation) Verdict {
	return f(ctx, r, c)
}

// ── Built-in verification strategies ─────────────────────────────────────────

// NoOpVerification always passes. Use when you trust the LLM's "complete"
// signal (the previous SDK default).
type NoOpVerification struct{}

func (NoOpVerification) Verify(_ context.Context, _ *AgentLoopResult, _ *Conversation) Verdict {
	return Verdict{Pass: true, Reason: "no verification configured"}
}

// CompletionVerification checks that the loop ended with a clean "complete"
// stop reason and a non-empty response. Useful as a baseline check.
type CompletionVerification struct {
	MinLength int // minimum response length, default 1
}

func (cv CompletionVerification) Verify(_ context.Context, r *AgentLoopResult, _ *Conversation) Verdict {
	min := cv.MinLength
	if min <= 0 {
		min = 1
	}
	if r.StopReason != "complete" {
		return Verdict{
			Pass:   false,
			Reason: fmt.Sprintf("loop ended with %q, not 'complete'", r.StopReason),
			Retry:  r.StopReason == "max_turns",
		}
	}
	if len(strings.TrimSpace(r.FinalContent)) < min {
		return Verdict{
			Pass:   false,
			Reason: fmt.Sprintf("response too short (< %d chars)", min),
			Retry:  true,
		}
	}
	return Verdict{Pass: true, Reason: "completion check passed"}
}

// CriteriaVerification asks the LLM whether its own output meets specific
// criteria. The LLM must answer YES or NO followed by a short reason.
//
// This is a lightweight self-check — not a substitute for actual tests,
// but useful for catching obvious failures (truncation, missing sections,
// wrong format) without dedicated test infrastructure.
type CriteriaVerification struct {
	// Criteria are the checks the LLM must affirm.
	// Each criterion is a single statement, e.g. "The response includes a code example".
	Criteria []string

	// Provider is the LLM to ask. If nil, uses the same provider used in execution.
	Provider LLMProvider

	// Model overrides the model used for the verification call.
	Model string
}

func (cv CriteriaVerification) Verify(ctx context.Context, r *AgentLoopResult, _ *Conversation) Verdict {
	if len(cv.Criteria) == 0 {
		return Verdict{Pass: true, Reason: "no criteria configured"}
	}
	if cv.Provider == nil {
		return Verdict{
			Pass:   false,
			Reason: "CriteriaVerification has no Provider",
			Retry:  false,
		}
	}

	var prompt strings.Builder
	prompt.WriteString("You are verifying an agent's output. Answer with 'YES' if ALL criteria are met, ")
	prompt.WriteString("otherwise 'NO' followed by which criterion failed.\n\n")
	prompt.WriteString("Criteria:\n")
	for i, c := range cv.Criteria {
		prompt.WriteString(fmt.Sprintf("%d. %s\n", i+1, c))
	}
	prompt.WriteString("\n--- Output to verify ---\n")
	prompt.WriteString(r.FinalContent)
	prompt.WriteString("\n--- End ---\n\nVerdict:")

	resp, err := cv.Provider.Chat(ctx, ChatRequest{
		Model: cv.Model,
		Messages: []ChatMessage{
			{Role: RoleUser, Content: prompt.String()},
		},
	})
	if err != nil {
		return Verdict{
			Pass:   false,
			Reason: fmt.Sprintf("verification call failed: %v", err),
			Retry:  false,
		}
	}

	answer := strings.TrimSpace(resp.Content)
	upper := strings.ToUpper(answer)
	if strings.HasPrefix(upper, "YES") {
		return Verdict{Pass: true, Reason: answer}
	}
	return Verdict{
		Pass:    false,
		Reason:  answer,
		Retry:   true,
		Details: cv.Criteria,
	}
}

// ── Phase transition signals ─────────────────────────────────────────────────

// PhaseSignal describes why a phase should advance. Runtime uses signals
// instead of mechanically calling Advance() after every function — this
// matches how Claude actually moves between phases (based on LLM output,
// not elapsed time).
type PhaseSignal string

const (
	// SignalOrientationDone fires after memory is read and skills are loaded.
	SignalOrientationDone PhaseSignal = "orientation_done"

	// SignalPlanProposed fires when the LLM emitted a plan.
	SignalPlanProposed PhaseSignal = "plan_proposed"

	// SignalPlanApproved fires when the user (or auto-approve) accepted.
	SignalPlanApproved PhaseSignal = "plan_approved"

	// SignalAlignmentDone fires when no plan is needed or plan is approved.
	SignalAlignmentDone PhaseSignal = "alignment_done"

	// SignalPreparationDone fires after checkpoint and budget verification.
	SignalPreparationDone PhaseSignal = "preparation_done"

	// SignalExecutionComplete fires when the agent loop returns "complete".
	SignalExecutionComplete PhaseSignal = "execution_complete"

	// SignalVerificationPassed fires when VerificationStrategy.Pass is true.
	SignalVerificationPassed PhaseSignal = "verification_passed"

	// SignalVerificationFailed fires when verification fails.
	// Runtime decides whether to retry Execution or surface the error.
	SignalVerificationFailed PhaseSignal = "verification_failed"

	// SignalClosureDone fires after memory writes complete.
	SignalClosureDone PhaseSignal = "closure_done"
)

// PhaseTransition describes a state change driven by a signal.
type PhaseTransition struct {
	From   Phase
	To     Phase
	Signal PhaseSignal
	Reason string
}
