package main

import (
	"fmt"
	"sync"
	"time"
)

type RunnerEvent struct {
	Type      string         `json:"type"`
	RunID     string         `json:"runId"`
	ChatID    int64          `json:"chatId"`
	Runner    *RunnerSummary `json:"runner,omitempty"`
	Trace     *TraceStep     `json:"trace,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
}

type RunnerEventHub struct {
	mu   sync.Mutex
	subs map[int64]map[chan RunnerEvent]struct{}
}

func NewRunnerEventHub() *RunnerEventHub {
	return &RunnerEventHub{subs: make(map[int64]map[chan RunnerEvent]struct{})}
}

func (h *RunnerEventHub) Subscribe(chatID int64) (<-chan RunnerEvent, func()) {
	ch := make(chan RunnerEvent, 64)

	h.mu.Lock()
	if _, ok := h.subs[chatID]; !ok {
		h.subs[chatID] = make(map[chan RunnerEvent]struct{})
	}
	h.subs[chatID][ch] = struct{}{}
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if subs, ok := h.subs[chatID]; ok {
			delete(subs, ch)
			if len(subs) == 0 {
				delete(h.subs, chatID)
			}
		}
		close(ch)
	}
}

func (h *RunnerEventHub) Publish(event RunnerEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for ch := range h.subs[event.ChatID] {
		select {
		case ch <- event:
		default:
		}
	}
}

func newRunID() string {
	return fmt.Sprintf("run_%d", time.Now().UnixNano())
}
