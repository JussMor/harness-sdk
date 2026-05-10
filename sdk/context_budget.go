package autobuild

import "context"

// ContextBudget defines token limits for the different layers of context
// injected into each LLM request. Without this, long memory + deep
// conversation history silently overflow the context window.
//
// Skills no longer participate in budget — they are loaded lazily on demand
// (Claude Code model). This keeps the budget agnostic of the SDK's
// extensibility surface.
type ContextBudget struct {
	// TotalTokens is the model's context window size.
	TotalTokens int

	// MemoryBudget is the max tokens that injected memory may consume.
	MemoryBudget int

	// HistoryBudget is the max tokens reserved for conversation history.
	HistoryBudget int

	// ReserveTokens is reserved for the model's own response.
	// Never filled with input.
	ReserveTokens int
}

// DefaultContextBudget returns sensible defaults for the given window size.
// Distribution: 25% memory, 60% history, 15% reserve.
func DefaultContextBudget(windowSize int) ContextBudget {
	return ContextBudget{
		TotalTokens:   windowSize,
		MemoryBudget:  windowSize * 25 / 100,
		HistoryBudget: windowSize * 60 / 100,
		ReserveTokens: windowSize * 15 / 100,
	}
}

// Available returns how many tokens remain after reserve.
func (b ContextBudget) Available() int {
	return b.TotalTokens - b.ReserveTokens
}

// WouldOverflow returns true if the given token counts would exceed the budget.
func (b ContextBudget) WouldOverflow(memoryTokens, historyTokens int) bool {
	if memoryTokens > b.MemoryBudget {
		return true
	}
	if historyTokens > b.HistoryBudget {
		return true
	}
	total := memoryTokens + historyTokens + b.ReserveTokens
	return total > b.TotalTokens
}

// EnforcementResult describes what was done to bring usage under budget.
type EnforcementResult struct {
	OverflowTokens   int  `json:"overflow_tokens"`
	TruncatedHistory bool `json:"truncated_history"`
	HistoryDropped   int  `json:"history_dropped"`
	StillOverflow    bool `json:"still_overflow"`
}

// Enforce takes action on overflow:
//
//   1. Truncate oldest non-system history messages until history budget fits
//   2. Report any remaining overflow as StillOverflow=true
//
// History is mutated in place. Returns the actions taken. Always non-nil.
func (b ContextBudget) Enforce(
	_ context.Context,
	_ *Conversation,
	memoryTokens int,
	historyMessages *[]ChatMessage,
) *EnforcementResult {
	res := &EnforcementResult{}

	// Estimate history tokens (avg 4 chars/token)
	historyTokens := 0
	for _, m := range *historyMessages {
		historyTokens += len(m.Content) / 4
	}

	if !b.WouldOverflow(memoryTokens, historyTokens) {
		return res
	}

	res.OverflowTokens = (memoryTokens + historyTokens + b.ReserveTokens) - b.TotalTokens

	if historyTokens > b.HistoryBudget {
		newHistory := make([]ChatMessage, 0, len(*historyMessages))
		// Always keep system messages
		for _, m := range *historyMessages {
			if m.Role == RoleSystem {
				newHistory = append(newHistory, m)
			}
		}
		nonSystem := make([]ChatMessage, 0, len(*historyMessages))
		for _, m := range *historyMessages {
			if m.Role != RoleSystem {
				nonSystem = append(nonSystem, m)
			}
		}
		kept := 0
		runningTokens := 0
		for i := len(nonSystem) - 1; i >= 0; i-- {
			tok := len(nonSystem[i].Content) / 4
			if runningTokens+tok > b.HistoryBudget {
				break
			}
			runningTokens += tok
			kept++
		}
		newHistory = append(newHistory, nonSystem[len(nonSystem)-kept:]...)
		res.HistoryDropped = len(*historyMessages) - len(newHistory)
		res.TruncatedHistory = res.HistoryDropped > 0
		*historyMessages = newHistory
	}

	historyTokens = 0
	for _, m := range *historyMessages {
		historyTokens += len(m.Content) / 4
	}
	res.StillOverflow = b.WouldOverflow(memoryTokens, historyTokens)
	return res
}
