package main

import (
	"sync"

	ab "github.com/everfaz/autobuild-sdk"
)

// interruptRegistry maps active chatIDs to their InterruptGate.
// One gate lives for the duration of a streaming session; the token-based
// resolve endpoint (POST /api/interrupts/:token/resolve) looks it up here.
var (
	interruptRegistryOnce sync.Once
	interruptRegistryInst *interruptGateRegistry
)

type interruptGateRegistry struct {
	mu    sync.RWMutex
	gates map[int64]*ab.InterruptGate
}

func ensureInterruptRegistry() *interruptGateRegistry {
	interruptRegistryOnce.Do(func() {
		interruptRegistryInst = &interruptGateRegistry{
			gates: make(map[int64]*ab.InterruptGate),
		}
	})
	return interruptRegistryInst
}

func (r *interruptGateRegistry) Register(chatID int64, gate *ab.InterruptGate) {
	r.mu.Lock()
	r.gates[chatID] = gate
	r.mu.Unlock()
}

func (r *interruptGateRegistry) Unregister(chatID int64) {
	r.mu.Lock()
	delete(r.gates, chatID)
	r.mu.Unlock()
}

func (r *interruptGateRegistry) Get(chatID int64) (*ab.InterruptGate, bool) {
	r.mu.RLock()
	gate, ok := r.gates[chatID]
	r.mu.RUnlock()
	return gate, ok
}
