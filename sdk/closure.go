package autobuild

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// InferredMemoryWriter asks the LLM to identify facts worth remembering
// after each turn. Deduplication prevents near-identical entries accumulating.
type InferredMemoryWriter struct {
	Provider        LLMProvider
	Model           string
	MaxFacts        int
	MinConfidence   float64
	DedupeThreshold float64 // 0-1, default 0.6
}

// InferredFact is one memorable fact extracted from a turn.
type InferredFact struct {
	Content    string      `json:"content"`
	Layer      MemoryLayer `json:"layer"`
	Scope      Scope       `json:"scope"`
	Confidence float64     `json:"confidence"`
	Reason     string      `json:"reason"`
	Path       string      `json:"path,omitempty"`
	Merged     bool        `json:"merged,omitempty"`
}

// Extract runs inference and returns facts above MinConfidence.
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

	var lastUser string
	for i := len(conv.Messages) - 1; i >= 0; i-- {
		if conv.Messages[i].Role == RoleUser {
			lastUser = conv.Messages[i].Content
			break
		}
	}

	userSnip := truncate(lastUser, 500)
	respSnip := truncate(finalResponse, 1000)

	promptText := "Identify up to " + fmt.Sprintf("%d", maxFacts) + ` persistent facts from this turn.

Only include facts that:
- Reveal stable user preferences, identity, or workflow patterns
- Represent decisions/constraints affecting future sessions
- Are NOT ephemeral (not task-specific, not "currently doing X")
- Are NOT already obvious general knowledge

Output one JSON per line, no prose:
{"content":"...","scope":"user|project","confidence":0.0-1.0,"reason":"..."}

scope=user for personal prefs; scope=project for project-specific facts.
Output nothing if there are no persistent facts.

User: ` + userSnip + "\nAssistant: " + respSnip

	resp, err := w.Provider.Chat(ctx, ChatRequest{
		Model:    w.Model,
		Messages: []ChatMessage{{Role: RoleUser, Content: promptText}},
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

// WriteWithDedup writes facts to memory, merging into existing entries
// when content is sufficiently similar (avoids duplication).
// This must be called explicitly from the closure phase with access to MemoryProvider.
func (w *InferredMemoryWriter) WriteWithDedup(
	ctx context.Context,
	provider MemoryProvider,
	facts []InferredFact,
) ([]InferredFact, error) {
	threshold := w.DedupeThreshold
	if threshold <= 0 {
		threshold = 0.6
	}

	var written []InferredFact
	for i := range facts {
		fact := &facts[i]

		// Search for existing similar entries
		existing, err := provider.Search(ctx, fact.Scope, fact.Content)
		if err != nil {
			existing = nil
		}

		merged := false
		for _, entry := range existing {
			if entry.Content == "" {
				continue
			}
			if stringSimilarity(entry.Content, fact.Content) >= threshold {
				// Merge: replace old content with new (StrReplace the whole content)
				oldContent := entry.Content
				newContent := fact.Content
				if err := provider.StrReplace(ctx, fact.Scope, entry.Path, oldContent, newContent); err == nil {
					fact.Path = entry.Path
					fact.Merged = true
					merged = true
					break
				}
			}
		}

		if !merged {
			// Create new entry
			path := fmt.Sprintf("/facts/inferred-%d.md", time.Now().UnixNano())
			if err := provider.Create(ctx, fact.Scope, path, fact.Content); err == nil {
				fact.Path = path
			}
		}
		written = append(written, *fact)
	}
	return written, nil
}

// stringSimilarity returns a rough Dice coefficient between two strings.
// Range 0 (no overlap) to 1 (identical). Uses word-level bigrams.
func stringSimilarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	aWords := strings.Fields(strings.ToLower(a))
	bWords := strings.Fields(strings.ToLower(b))
	if len(aWords) == 0 || len(bWords) == 0 {
		return 0
	}

	// Build bigram sets
	aBigrams := bigrams(aWords)
	bBigrams := bigrams(bWords)

	if len(aBigrams) == 0 || len(bBigrams) == 0 {
		// Fall back to word-level Jaccard
		return jaccardWords(aWords, bWords)
	}

	// Count intersection
	var intersection int
	for bg := range aBigrams {
		if bBigrams[bg] {
			intersection++
		}
	}
	return float64(2*intersection) / float64(len(aBigrams)+len(bBigrams))
}

func bigrams(words []string) map[string]bool {
	out := make(map[string]bool, len(words))
	for i := 0; i+1 < len(words); i++ {
		out[words[i]+" "+words[i+1]] = true
	}
	return out
}

func jaccardWords(a, b []string) float64 {
	sa := make(map[string]bool, len(a))
	for _, w := range a {
		sa[w] = true
	}
	var intersection int
	union := len(sa)
	for _, w := range b {
		if sa[w] {
			intersection++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// ── Skill eviction ────────────────────────────────────────────────────────────

type SkillEvictionPolicy interface {
	Evict(loaded []LoadedSkill, tokensToFree int) []string
}

type LRUEvictionPolicy struct{}

func (LRUEvictionPolicy) Evict(loaded []LoadedSkill, tokensToFree int) []string {
	if tokensToFree <= 0 || len(loaded) == 0 {
		return nil
	}
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
