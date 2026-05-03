package autobuild

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// InferredMemoryWriter asks the LLM, after a conversation turn, to identify
// facts worth remembering for future sessions. Unlike DefaultMemoryTriggerDetector
// which only catches explicit phrases ("remember that..."), this captures
// implicit facts the agent learned during the turn.
//
// This mirrors how Claude updates memory: most writes are inferences, not
// explicit user commands.
type InferredMemoryWriter struct {
	// Provider is the LLM used to extract facts. If nil, no inference happens.
	Provider LLMProvider

	// Model is the model identifier for the extraction call.
	// A small model is fine — extraction is cheap.
	Model string

	// MaxFacts caps how many facts can be extracted per turn. Default 3.
	MaxFacts int

	// MinConfidence is the threshold for the LLM's self-reported confidence.
	// Range 0-1. Default 0.7.
	MinConfidence float64
}

// InferredFact is a single memorable fact extracted from a turn.
type InferredFact struct {
	Content    string      `json:"content"`
	Layer      MemoryLayer `json:"layer"`      // always Inferred from this writer
	Scope      Scope       `json:"scope"`      // User or Project
	Confidence float64     `json:"confidence"` // 0-1
	Reason     string      `json:"reason"`
}

// Extract runs the inference and returns facts above MinConfidence.
// Returns empty slice if Provider is nil or extraction fails.
func (w *InferredMemoryWriter) Extract(ctx context.Context, conv *Conversation, finalResponse string) ([]InferredFact, error) {
	if w.Provider == nil {
		return nil, nil
	}
	maxFacts := w.MaxFacts
	if maxFacts <= 0 {
		maxFacts = 3
	}
	minConf := w.MinConfidence
	if minConf <= 0 {
		minConf = 0.7
	}

	// Build a compact summary of the turn — last user message + assistant response
	var lastUser string
	for i := len(conv.Messages) - 1; i >= 0; i-- {
		if conv.Messages[i].Role == RoleUser {
			lastUser = conv.Messages[i].Content
			break
		}
	}

	prompt := fmt.Sprintf(`Identify up to %d facts from this conversation turn that are worth remembering across future sessions. Only include facts that:
- Reveal user preferences, workflow, or persistent context
- Affect how to respond in future conversations
- Are NOT ephemeral (i.e. not "currently debugging X")

For each fact, output a JSON object on its own line:
{"content": "...", "scope": "user" or "project", "confidence": 0.0-1.0, "reason": "..."}

Output only JSON lines. No prose.

User message: %s

Assistant response: %s`, maxFacts, truncate(lastUser, 500), truncate(finalResponse, 1000))

	resp, err := w.Provider.Chat(ctx, ChatRequest{
		Model: w.Model,
		Messages: []ChatMessage{
			{Role: RoleUser, Content: prompt},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("extract facts: %w", err)
	}

	var facts []InferredFact
	for _, line := range strings.Split(resp.Content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var raw struct {
			Content    string  `json:"content"`
			Scope      string  `json:"scope"`
			Confidence float64 `json:"confidence"`
			Reason     string  `json:"reason"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		if raw.Confidence < minConf || raw.Content == "" {
			continue
		}
		scope := ScopeUser
		if raw.Scope == "project" {
			scope = ScopeProject
		}
		facts = append(facts, InferredFact{
			Content:    raw.Content,
			Layer:      MemoryLayerInferred,
			Scope:      scope,
			Confidence: raw.Confidence,
			Reason:     raw.Reason,
		})
		if len(facts) >= maxFacts {
			break
		}
	}
	return facts, nil
}

// ── Skill eviction ───────────────────────────────────────────────────────────

// SkillEvictionPolicy decides which loaded skills to remove when budget is tight.
type SkillEvictionPolicy interface {
	// Evict returns the names of skills to unload, given the current set
	// and how many tokens need to be freed.
	Evict(loaded []LoadedSkill, tokensToFree int) []string
}

// LRUEvictionPolicy unloads the least-recently-used skills first.
type LRUEvictionPolicy struct{}

func (LRUEvictionPolicy) Evict(loaded []LoadedSkill, tokensToFree int) []string {
	if tokensToFree <= 0 || len(loaded) == 0 {
		return nil
	}
	// loaded is assumed sorted oldest-used first by SkillsByLastUsed
	var freed int
	var names []string
	for _, s := range loaded {
		if freed >= tokensToFree {
			break
		}
		names = append(names, s.Name)
		freed += s.TokenEstimate
	}
	return names
}

// TTLEvictionPolicy unloads skills that haven't been used in the given duration.
type TTLEvictionPolicy struct {
	MaxIdle time.Duration
}

func (p TTLEvictionPolicy) Evict(loaded []LoadedSkill, _ int) []string {
	now := time.Now()
	var names []string
	for _, s := range loaded {
		if now.Sub(s.LastUsed) > p.MaxIdle {
			names = append(names, s.Name)
		}
	}
	return names
}

// ── helpers ──────────────────────────────────────────────────────────────────

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
