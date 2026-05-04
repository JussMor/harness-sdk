package autobuild

import (
	"context"
	"math"
	"sort"
)

// Embedder produces a vector representation of text suitable for similarity
// search. Implementations wrap providers like Voyage, OpenAI, or local models.
//
// The SDK does not bundle an embedder — you bring your own. The interface
// exists so ObservationStore and SkillProvider can swap from keyword matching
// to semantic search without API changes for consumers.
type Embedder interface {
	// Embed returns a vector for a single text.
	Embed(ctx context.Context, text string) ([]float32, error)

	// EmbedBatch returns vectors for multiple texts in a single call.
	// More efficient than calling Embed in a loop.
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)

	// Dimensions returns the size of the vectors produced.
	Dimensions() int
}

// CosineSimilarity returns the cosine similarity between two vectors.
// Returns 0 if either vector is zero-length or if dimensions don't match.
// Range: -1 (opposite) to 1 (identical). Most embedding models produce
// non-negative values in practice.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		fa := float64(a[i])
		fb := float64(b[i])
		dot += fa * fb
		normA += fa * fa
		normB += fb * fb
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// ── Semantic ObservationStore ────────────────────────────────────────────────

// SemanticObservationStore is an ObservationStore that uses embeddings for
// retrieval instead of keyword matching. Falls back to keyword match when
// no Embedder is set.
//
// Use this when "the user mentioned auth" should match "JWT validation"
// without requiring exact word overlap.
type SemanticObservationStore struct {
	*InMemoryObservationStore
	embedder   Embedder
	embeddings map[string][]float32 // observation ID → vector
}

// NewSemanticObservationStore wraps an InMemoryObservationStore with semantic search.
func NewSemanticObservationStore(embedder Embedder) *SemanticObservationStore {
	return &SemanticObservationStore{
		InMemoryObservationStore: NewObservationStore(),
		embedder:                 embedder,
		embeddings:               make(map[string][]float32),
	}
}

// Record overrides the in-memory implementation to also embed the observation.
func (s *SemanticObservationStore) Record(ctx context.Context, obs Observation) error {
	if err := s.InMemoryObservationStore.Record(ctx, obs); err != nil {
		return err
	}
	if s.embedder == nil {
		return nil
	}
	vec, err := s.embedder.Embed(ctx, obs.Content)
	if err != nil {
		return nil // graceful degradation — the obs is recorded, just not embedded
	}
	// The InMemoryObservationStore assigns IDs internally; fetch the latest
	all, _ := s.InMemoryObservationStore.All(ctx)
	if len(all) > 0 {
		latest := all[len(all)-1]
		s.embeddings[latest.ID] = vec
	}
	return nil
}

// Relevant overrides keyword matching with semantic search when an embedder exists.
func (s *SemanticObservationStore) Relevant(ctx context.Context, query string, limit int) ([]Observation, error) {
	if s.embedder == nil || len(s.embeddings) == 0 {
		return s.InMemoryObservationStore.Relevant(ctx, query, limit)
	}
	queryVec, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return s.InMemoryObservationStore.Relevant(ctx, query, limit)
	}

	all, err := s.InMemoryObservationStore.All(ctx)
	if err != nil {
		return nil, err
	}

	type scored struct {
		obs   Observation
		score float64
	}
	var results []scored
	for _, o := range all {
		vec, ok := s.embeddings[o.ID]
		if !ok {
			continue
		}
		sim := CosineSimilarity(queryVec, vec)
		// Blend similarity with the obs's own relevance hint
		blended := 0.7*sim + 0.3*o.Relevance
		results = append(results, scored{obs: o, score: blended})
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

// ── Semantic Skill matching ──────────────────────────────────────────────────

// SemanticSkillMatcher provides scored skill matching using embeddings.
// Wrap your existing SkillProvider with this when keyword triggers
// undermatch — e.g. when users phrase requests differently than the
// skill's literal triggers.
//
// The wrapper computes embeddings lazily (on first Match call per skill)
// and caches them. If the underlying SkillProvider returns matches that
// score well by keywords, those wins; otherwise semantic similarity fills in.
type SemanticSkillMatcher struct {
	inner    SkillProvider
	embedder Embedder
	cache    map[string][]float32 // skill name → embedded triggers concatenation
}

// NewSemanticSkillMatcher wraps a SkillProvider with embedding-based matching.
func NewSemanticSkillMatcher(inner SkillProvider, embedder Embedder) *SemanticSkillMatcher {
	return &SemanticSkillMatcher{
		inner:    inner,
		embedder: embedder,
		cache:    make(map[string][]float32),
	}
}

// Match runs both keyword and semantic matching, blends scores, returns sorted.
func (m *SemanticSkillMatcher) Match(ctx context.Context, text string) ([]SkillMatch, error) {
	keywordMatches, err := m.inner.Match(ctx, text)
	if err != nil {
		return nil, err
	}
	if m.embedder == nil {
		return keywordMatches, nil
	}

	queryVec, err := m.embedder.Embed(ctx, text)
	if err != nil {
		return keywordMatches, nil
	}

	// Build a map of existing keyword scores
	scores := make(map[string]float64)
	skills := make(map[string]*Skill)
	for _, m := range keywordMatches {
		scores[m.Skill.Name] = m.Score
		skills[m.Skill.Name] = m.Skill
	}

	// Also score skills that didn't keyword-match
	allNames, err := m.inner.List(ctx)
	if err == nil {
		for _, name := range allNames {
			if _, alreadyScored := scores[name]; alreadyScored {
				continue
			}
			skill, err := m.inner.Get(ctx, name)
			if err != nil || skill == nil {
				continue
			}
			vec, ok := m.cache[name]
			if !ok {
				combined := skill.Domain
				if skill.Meta.Description != "" {
					combined = skill.Meta.Description
				}
				for _, t := range skill.Triggers {
					combined += " " + t
				}
				v, err := m.embedder.Embed(ctx, combined)
				if err != nil {
					continue
				}
				m.cache[name] = v
				vec = v
			}
			sim := CosineSimilarity(queryVec, vec)
			if sim > 0.5 {
				scores[name] = sim * 0.8 // semantic-only matches capped slightly
				skills[name] = skill
			}
		}
	}

	// Build sorted result
	out := make([]SkillMatch, 0, len(scores))
	for name, score := range scores {
		out = append(out, SkillMatch{Skill: skills[name], Score: score})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out, nil
}

// Pass-through methods for the wrapped provider.
func (m *SemanticSkillMatcher) Load(ctx context.Context, name string) (*Skill, error) {
	return m.inner.Load(ctx, name)
}
func (m *SemanticSkillMatcher) Unload(ctx context.Context, name string) error {
	return m.inner.Unload(ctx, name)
}
func (m *SemanticSkillMatcher) Loaded(ctx context.Context) []string {
	return m.inner.Loaded(ctx)
}
func (m *SemanticSkillMatcher) List(ctx context.Context) ([]string, error) {
	return m.inner.List(ctx)
}
func (m *SemanticSkillMatcher) Get(ctx context.Context, name string) (*Skill, error) {
	return m.inner.Get(ctx, name)
}
