package autobuild

import "context"

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

// EnforcementResult describes what was done to bring usage under budget.
type EnforcementResult struct {
	OverflowTokens int      `json:"overflow_tokens"`
	EvictedSkills  []string `json:"evicted_skills,omitempty"`
	TruncatedHistory bool   `json:"truncated_history"`
	HistoryDropped int      `json:"history_dropped"`
	StillOverflow  bool     `json:"still_overflow"`
}

// Enforce takes action on overflow instead of just warning. Order:
//
//   1. Evict skills (oldest first) until skill budget fits
//   2. Truncate oldest non-system history messages until history budget fits
//   3. Report any remaining overflow as StillOverflow=true
//
// Skills are evicted via SkillProvider.Unload. History is mutated in place.
// Returns the actions taken. Always non-nil.
func (b ContextBudget) Enforce(
	ctx context.Context,
	conv *Conversation,
	skills SkillProvider,
	skillTokens, memoryTokens int,
	historyMessages *[]ChatMessage,
) *EnforcementResult {
	res := &EnforcementResult{}

	// Estimate history tokens (avg 4 chars/token)
	historyTokens := 0
	for _, m := range *historyMessages {
		historyTokens += len(m.Content) / 4
	}

	if !b.WouldOverflow(skillTokens, memoryTokens, historyTokens) {
		return res
	}

	res.OverflowTokens = (skillTokens + memoryTokens + historyTokens + b.ReserveTokens) - b.TotalTokens

	// 1. Skill eviction (LRU)
	if skills != nil && conv != nil && skillTokens > b.SkillBudget {
		toFree := skillTokens - b.SkillBudget
		policy := LRUEvictionPolicy{}
		victims := policy.Evict(conv.SkillsByLastUsed(), toFree)
		for _, name := range victims {
			if err := skills.Unload(ctx, name); err == nil {
				conv.MarkSkillUnloaded(name)
				res.EvictedSkills = append(res.EvictedSkills, name)
			}
		}
	}

	// 2. History truncation — drop oldest non-system messages
	if historyTokens > b.HistoryBudget {
		newHistory := make([]ChatMessage, 0, len(*historyMessages))
		// Always keep system messages
		for _, m := range *historyMessages {
			if m.Role == RoleSystem {
				newHistory = append(newHistory, m)
			}
		}
		// Add messages from newest, stopping when budget reached
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

	// 3. Re-check
	historyTokens = 0
	for _, m := range *historyMessages {
		historyTokens += len(m.Content) / 4
	}
	res.StillOverflow = b.WouldOverflow(skillTokens, memoryTokens, historyTokens)
	return res
}
