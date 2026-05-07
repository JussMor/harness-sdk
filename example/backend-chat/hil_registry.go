package main

import (
	"sync"

	ab "github.com/everfaz/autobuild-sdk"
)

// hilRegistry maps active chatIDs to their ApprovalGate.
// When a chat session has HIL enabled, its gate is registered here.
// The confirm handler looks up the gate by chatID to deliver human decisions.
var hilRegistry = &approvalGateRegistry{
	gates: make(map[int64]*ab.ApprovalGate),
}

type approvalGateRegistry struct {
	mu    sync.RWMutex
	gates map[int64]*ab.ApprovalGate
}

func (r *approvalGateRegistry) Register(chatID int64, gate *ab.ApprovalGate) {
	r.mu.Lock()
	r.gates[chatID] = gate
	r.mu.Unlock()
}

func (r *approvalGateRegistry) Unregister(chatID int64) {
	r.mu.Lock()
	delete(r.gates, chatID)
	r.mu.Unlock()
}

func (r *approvalGateRegistry) Get(chatID int64) (*ab.ApprovalGate, bool) {
	r.mu.RLock()
	gate, ok := r.gates[chatID]
	r.mu.RUnlock()
	return gate, ok
}
