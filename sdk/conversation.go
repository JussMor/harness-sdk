package autobuild

import (
	"sync"
	"time"
)

// Conversation holds the state of an ongoing conversation across multiple
// Runtime.Run calls.
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

// NewConversation starts a fresh conversation.
func NewConversation(id string) *Conversation {
	return &Conversation{
		ID:        id,
		Messages:  make([]ChatMessage, 0, 8),
		CreatedAt: time.Now(),
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
