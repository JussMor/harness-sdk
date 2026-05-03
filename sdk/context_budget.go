package autobuild

// ContextBudget defines token limits for the different layers of context
// injected into each LLM request. Without this, loading many skills +
// long memory + deep conversation history silently overflows the context
// window and the provider returns an error or truncates.
//
// Usage:
//
//	budget := DefaultContextBudget(128_000)
//	if budget.WouldOverflow(skillTokens, memoryTokens, historyTokens) {
//	    // evict oldest skills or summarize memory before calling LLM
//	}
type ContextBudget struct {
	// TotalTokens is the model's context window size.
	TotalTokens int

	// SkillBudget is the max tokens that loaded skills may consume.
	// Skills are evicted (oldest first) when this is exceeded.
	SkillBudget int

	// MemoryBudget is the max tokens that injected memory may consume.
	MemoryBudget int

	// HistoryBudget is the max tokens reserved for conversation history.
	HistoryBudget int

	// ReserveTokens is reserved for the model's own response.
	// Never filled with input.
	ReserveTokens int
}

// DefaultContextBudget returns sensible defaults for the given window size.
// Distribution: 10% skills, 15% memory, 60% history, 15% reserve.
func DefaultContextBudget(windowSize int) ContextBudget {
	return ContextBudget{
		TotalTokens:   windowSize,
		SkillBudget:   windowSize / 10,
		MemoryBudget:  windowSize * 15 / 100,
		HistoryBudget: windowSize * 60 / 100,
		ReserveTokens: windowSize * 15 / 100,
	}
}

// Available returns how many tokens remain after reserve.
func (b ContextBudget) Available() int {
	return b.TotalTokens - b.ReserveTokens
}

// WouldOverflow returns true if the given token counts would exceed the budget.
func (b ContextBudget) WouldOverflow(skillTokens, memoryTokens, historyTokens int) bool {
	if skillTokens > b.SkillBudget {
		return true
	}
	if memoryTokens > b.MemoryBudget {
		return true
	}
	if historyTokens > b.HistoryBudget {
		return true
	}
	total := skillTokens + memoryTokens + historyTokens + b.ReserveTokens
	return total > b.TotalTokens
}

// SkillEvictionCount returns how many skills to evict (from oldest) to fit
// within the skill budget, given a uniform token cost per skill.
func (b ContextBudget) SkillEvictionCount(loadedSkills int, tokensPerSkill int) int {
	if loadedSkills == 0 || tokensPerSkill == 0 {
		return 0
	}
	maxSkills := b.SkillBudget / tokensPerSkill
	if loadedSkills <= maxSkills {
		return 0
	}
	return loadedSkills - maxSkills
}
