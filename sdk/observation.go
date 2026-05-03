package autobuild

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// Observation is a fact discovered during execution that is worth keeping
// for the rest of the session but does NOT belong in permanent memory.
//
// The distinction:
//   - Memory    → survives across sessions, written consciously
//   - Observation → lives only in this session, written automatically
//
// Examples of what becomes an Observation:
//   - A web search result that's relevant to the current task
//   - An API response with data needed in a later turn
//   - A tool result that reveals a constraint or error pattern
//   - Anything you'd want to "remember during this conversation" without
//     polluting the permanent memory store
type Observation struct {
	ID        string     `json:"id"`
	Source    string     `json:"source"`    // "web_search", "tool_result", "user_message"
	Content   string     `json:"content"`
	Tags      []string   `json:"tags,omitempty"`
	Relevance float64    `json:"relevance"` // 0-1, higher = surfaces first
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"` // nil = lives for session duration
}

// IsExpired returns true if the observation has a TTL that has passed.
func (o *Observation) IsExpired() bool {
	if o.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*o.ExpiresAt)
}

// ObservationStore holds session-scoped observations and makes them
// retrievable by relevance. This is the agent's "working memory" —
// facts actively useful right now, not worth persisting forever.
type ObservationStore interface {
	// Record adds a new observation to the store.
	Record(ctx context.Context, obs Observation) error

	// Relevant returns the top-N most relevant observations for a query.
	// Matching is keyword-based by default; implementations may use embeddings.
	Relevant(ctx context.Context, query string, limit int) ([]Observation, error)

	// All returns every non-expired observation.
	All(ctx context.Context) ([]Observation, error)

	// Expire removes all observations past their ExpiresAt time.
	Expire(ctx context.Context) error

	// Clear removes all observations (call on session end).
	Clear(ctx context.Context) error
}

// ── InMemoryObservationStore ─────────────────────────────────────────────────

// InMemoryObservationStore is a simple keyword-matching ObservationStore.
// Suitable for single-process use. Replace with a vector store for
// semantic search in production.
type InMemoryObservationStore struct {
	mu   sync.RWMutex
	obs  []Observation
	seq  int
}

// NewObservationStore creates an empty in-memory store.
func NewObservationStore() *InMemoryObservationStore {
	return &InMemoryObservationStore{}
}

func (s *InMemoryObservationStore) Record(_ context.Context, obs Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	if obs.ID == "" {
		obs.ID = "obs-" + string(rune('0'+s.seq))
	}
	if obs.CreatedAt.IsZero() {
		obs.CreatedAt = time.Now()
	}
	s.obs = append(s.obs, obs)
	return nil
}

func (s *InMemoryObservationStore) Relevant(_ context.Context, query string, limit int) ([]Observation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	queryLower := strings.ToLower(query)
	type scored struct {
		obs   Observation
		score float64
	}
	var results []scored
	for _, o := range s.obs {
		if o.IsExpired() {
			continue
		}
		// Simple keyword match — weight by relevance field + hit count
		contentLower := strings.ToLower(o.Content)
		hits := float64(strings.Count(contentLower, queryLower))
		if hits == 0 {
			continue
		}
		results = append(results, scored{obs: o, score: hits*0.5 + o.Relevance*0.5})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})
	out := make([]Observation, 0, limit)
	for i, r := range results {
		if i >= limit {
			break
		}
		out = append(out, r.obs)
	}
	return out, nil
}

func (s *InMemoryObservationStore) All(_ context.Context) ([]Observation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Observation
	for _, o := range s.obs {
		if !o.IsExpired() {
			out = append(out, o)
		}
	}
	return out, nil
}

func (s *InMemoryObservationStore) Expire(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var active []Observation
	for _, o := range s.obs {
		if !o.IsExpired() {
			active = append(active, o)
		}
	}
	s.obs = active
	return nil
}

func (s *InMemoryObservationStore) Clear(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.obs = nil
	return nil
}
