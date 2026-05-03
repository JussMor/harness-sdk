package autobuild

import (
	"sync"
	"time"
)

// Conversation holds the state of an ongoing conversation across multiple
// Runtime.Run calls. It tracks the message history, which skills are loaded,
// whether memory has been read, and which fields are warm vs. cold.
//
// A Conversation is the unit of multi-turn interaction. Create one when the
// user starts talking; reuse it for every subsequent message in the same
// thread. Discard it when the conversation ends.
//
// Conversation is safe for sequential use (one Run at a time). It is NOT
// safe for concurrent Run calls on the same conversation — that would
// produce interleaved messages.
type Conversation struct {
	// ID identifies this conversation for tracing and persistence.
	ID string

	// ThreadID optionally links to a ThreadProvider entity.
	ThreadID string

	// Messages is the full conversation history.
	// Append-only; never mutated by Runtime except to add new turns.
	Messages []ChatMessage

	// LoadedSkills tracks which skills are currently in context.
	// Used to avoid double-loading and to drive eviction.
	LoadedSkills map[string]LoadedSkill

	// MemoryRead is true after orientation has read memory at least once.
	// Subsequent turns skip memory re-read unless ForceRefresh is set.
	MemoryRead bool

	// TurnCount is incremented each Run call.
	TurnCount int

	// CreatedAt is when the conversation started.
	CreatedAt time.Time

	// LastTurnAt is when the most recent Run completed.
	LastTurnAt time.Time

	mu sync.Mutex
}

// LoadedSkill tracks metadata about a skill currently in context.
type LoadedSkill struct {
	Name        string    `json:"name"`
	LoadedAt    time.Time `json:"loaded_at"`
	LastUsed    time.Time `json:"last_used"`
	Score       float64   `json:"score"`        // match score when loaded
	TokenEstimate int     `json:"token_estimate"`
}

// NewConversation starts a fresh conversation.
func NewConversation(id string) *Conversation {
	return &Conversation{
		ID:           id,
		Messages:     make([]ChatMessage, 0, 8),
		LoadedSkills: make(map[string]LoadedSkill),
		CreatedAt:    time.Now(),
	}
}

// IsCold returns true on the first turn (before any user message processed).
// Cold-start triggers full orientation: memory read, skill matching, etc.
// Warm turns skip these and only append the new user message.
func (c *Conversation) IsCold() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.TurnCount == 0
}

// AppendUser adds a user message to the history.
func (c *Conversation) AppendUser(content string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Messages = append(c.Messages, ChatMessage{
		Role:    RoleUser,
		Content: content,
	})
}

// AppendAssistant adds an assistant message to the history.
func (c *Conversation) AppendAssistant(content string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Messages = append(c.Messages, ChatMessage{
		Role:    RoleAssistant,
		Content: content,
	})
}

// MergeMessages appends the messages produced during a Run (assistant +
// any tool messages). Skips messages that duplicate the last user message.
func (c *Conversation) MergeMessages(newMessages []ChatMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Find where the new messages diverge from existing history
	startIdx := len(c.Messages)
	if startIdx > len(newMessages) {
		// New messages came back shorter than what we have — likely
		// the loop returned only assistant turn. Take from end.
		startIdx = 0
	}
	for i := startIdx; i < len(newMessages); i++ {
		c.Messages = append(c.Messages, newMessages[i])
	}
}

// MarkSkillLoaded records that a skill was loaded for this conversation.
func (c *Conversation) MarkSkillLoaded(name string, score float64, tokens int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if existing, ok := c.LoadedSkills[name]; ok {
		existing.LastUsed = now
		c.LoadedSkills[name] = existing
		return
	}
	c.LoadedSkills[name] = LoadedSkill{
		Name:          name,
		LoadedAt:      now,
		LastUsed:      now,
		Score:         score,
		TokenEstimate: tokens,
	}
}

// IsSkillLoaded reports whether a skill is currently loaded.
func (c *Conversation) IsSkillLoaded(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.LoadedSkills[name]
	return ok
}

// MarkSkillUnloaded removes a skill from the loaded set.
func (c *Conversation) MarkSkillUnloaded(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.LoadedSkills, name)
}

// SkillsByLastUsed returns loaded skills sorted oldest-used first.
// Useful for eviction policies (LRU).
func (c *Conversation) SkillsByLastUsed() []LoadedSkill {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]LoadedSkill, 0, len(c.LoadedSkills))
	for _, s := range c.LoadedSkills {
		out = append(out, s)
	}
	// Insertion sort — small set, simple
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].LastUsed.Before(out[j-1].LastUsed); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// IncrementTurn bumps the turn counter and updates LastTurnAt.
func (c *Conversation) IncrementTurn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.TurnCount++
	c.LastTurnAt = time.Now()
}

// MessageCount returns the number of messages in history.
func (c *Conversation) MessageCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.Messages)
}
