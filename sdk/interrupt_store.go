package autobuild

import (
	"context"
	"sync"
)

// InterruptStore persists pending interrupts so they survive transient
// disconnections (closed SSE, dropped websocket) and — with a shared
// backend — can be resumed across replicas.
//
// Implementations must be safe for concurrent use. Get returns ok=false
// when the id is unknown; Delete is idempotent.
type InterruptStore interface {
	Put(ctx context.Context, req InterruptRequest) error
	Get(ctx context.Context, id string) (InterruptRequest, bool, error)
	Delete(ctx context.Context, id string) error
}

// InMemoryInterruptStore is a process-local store, suitable for single-replica
// deployments and tests. For multi-replica setups, back the store with
// Postgres/Redis and inject via InterruptGate.WithStore.
type InMemoryInterruptStore struct {
	mu sync.RWMutex
	m  map[string]InterruptRequest
}

// NewInMemoryInterruptStore returns a ready-to-use in-memory store.
func NewInMemoryInterruptStore() *InMemoryInterruptStore {
	return &InMemoryInterruptStore{m: make(map[string]InterruptRequest)}
}

func (s *InMemoryInterruptStore) Put(_ context.Context, req InterruptRequest) error {
	s.mu.Lock()
	s.m[req.ID] = req
	s.mu.Unlock()
	return nil
}

func (s *InMemoryInterruptStore) Get(_ context.Context, id string) (InterruptRequest, bool, error) {
	s.mu.RLock()
	req, ok := s.m[id]
	s.mu.RUnlock()
	return req, ok, nil
}

func (s *InMemoryInterruptStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
	return nil
}
