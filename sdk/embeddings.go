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

// ── Semantic Memory Search ───────────────────────────────────────────────────

// SemanticMemorySearch wraps a MemoryProvider to add embedding-based retrieval.
// Falls through to the underlying Search when no Embedder is configured.
//
// Use when "user prefers terse responses" should match "user dislikes verbose
// explanations" without requiring exact word overlap.
type SemanticMemorySearch struct {
	Inner    MemoryProvider
	Embedder Embedder

	// Cache of embeddings keyed by content hash to avoid re-embedding.
	cache map[string][]float32
}

// NewSemanticMemorySearch wraps a MemoryProvider with semantic search capability.
func NewSemanticMemorySearch(inner MemoryProvider, embedder Embedder) *SemanticMemorySearch {
	return &SemanticMemorySearch{
		Inner:    inner,
		Embedder: embedder,
		cache:    make(map[string][]float32),
	}
}

// Search performs vector similarity search over all entries.
// Falls back to inner.Search if Embedder is nil or fails.
func (s *SemanticMemorySearch) Search(ctx context.Context, scope Scope, query string) ([]MemoryEntry, error) {
	if s.Embedder == nil {
		return s.Inner.Search(ctx, scope, query)
	}

	// Get all entries as candidates
	all, err := s.Inner.Search(ctx, scope, "")
	if err != nil || len(all) == 0 {
		// Fall back to keyword search if listing fails
		return s.Inner.Search(ctx, scope, query)
	}

	queryVec, err := s.Embedder.Embed(ctx, query)
	if err != nil {
		return s.Inner.Search(ctx, scope, query)
	}

	type scored struct {
		entry MemoryEntry
		score float64
	}
	candidates := make([]scored, 0, len(all))
	for _, entry := range all {
		vec, ok := s.cache[entry.Path]
		if !ok {
			v, err := s.Embedder.Embed(ctx, entry.Content)
			if err != nil {
				continue
			}
			vec = v
			s.cache[entry.Path] = v
		}
		similarity := CosineSimilarity(queryVec, vec)
		candidates = append(candidates, scored{entry: entry, score: similarity})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	results := make([]MemoryEntry, 0, len(candidates))
	for _, c := range candidates {
		if c.score > 0.3 { // similarity threshold
			results = append(results, c.entry)
		}
	}
	return results, nil
}

// Delegate the rest of MemoryProvider to the inner provider.
func (s *SemanticMemorySearch) View(ctx context.Context, scope Scope, path string) (string, error) {
	return s.Inner.View(ctx, scope, path)
}
func (s *SemanticMemorySearch) Create(ctx context.Context, scope Scope, path, content string) error {
	delete(s.cache, path) // invalidate
	return s.Inner.Create(ctx, scope, path, content)
}
func (s *SemanticMemorySearch) StrReplace(ctx context.Context, scope Scope, path, oldStr, newStr string) error {
	delete(s.cache, path) // invalidate
	return s.Inner.StrReplace(ctx, scope, path, oldStr, newStr)
}
func (s *SemanticMemorySearch) Delete(ctx context.Context, scope Scope, path string) error {
	delete(s.cache, path)
	return s.Inner.Delete(ctx, scope, path)
}
func (s *SemanticMemorySearch) Rename(ctx context.Context, scope Scope, oldPath, newPath string) error {
	if vec, ok := s.cache[oldPath]; ok {
		s.cache[newPath] = vec
		delete(s.cache, oldPath)
	}
	return s.Inner.Rename(ctx, scope, oldPath, newPath)
}
func (s *SemanticMemorySearch) List(ctx context.Context, scope Scope, path string) ([]string, error) {
	return s.Inner.List(ctx, scope, path)
}

// ── Hybrid Memory Search ─────────────────────────────────────────────────────

// HybridMemorySearch combines BM25 (keyword) with vector (semantic) search using
// reciprocal rank fusion (RRF) — the standard technique used by production
// retrieval systems (Vespa, Elasticsearch, Pinecone).
//
// Each candidate gets RRF score = Σ 1 / (k + rank_in_each_method)
// where k=60 is a constant that dampens the contribution of low-ranked results.
//
// Hybrid search consistently outperforms either method alone: BM25 captures
// exact terms (acronyms, proper nouns, code identifiers) while embeddings
// capture meaning (paraphrases, synonyms).
type HybridMemorySearch struct {
	Inner    MemoryProvider
	Embedder Embedder

	// K is the RRF constant. Default 60.
	K float64

	// Cache of embeddings to avoid re-embedding on every search.
	cache map[string][]float32
}

// NewHybridMemorySearch wraps a MemoryProvider with hybrid keyword+vector search.
func NewHybridMemorySearch(inner MemoryProvider, embedder Embedder) *HybridMemorySearch {
	return &HybridMemorySearch{
		Inner:    inner,
		Embedder: embedder,
		K:        60,
		cache:    make(map[string][]float32),
	}
}

// Search performs hybrid search: runs BM25 (via inner.Search) and vector
// search in parallel, then fuses results via RRF.
func (h *HybridMemorySearch) Search(ctx context.Context, scope Scope, query string) ([]MemoryEntry, error) {
	// BM25 results from underlying provider
	bm25Results, err := h.Inner.Search(ctx, scope, query)
	if err != nil {
		bm25Results = nil
	}

	// If no embedder, just return BM25 results
	if h.Embedder == nil {
		return bm25Results, nil
	}

	// Vector search: get all candidates, rank by cosine similarity
	allEntries, _ := h.Inner.Search(ctx, scope, "")
	queryVec, err := h.Embedder.Embed(ctx, query)
	if err != nil {
		return bm25Results, nil
	}

	type scored struct {
		entry MemoryEntry
		score float64
	}
	vectorScored := make([]scored, 0, len(allEntries))
	for _, entry := range allEntries {
		vec, ok := h.cache[entry.Path]
		if !ok {
			v, err := h.Embedder.Embed(ctx, entry.Content)
			if err != nil {
				continue
			}
			vec = v
			h.cache[entry.Path] = v
		}
		similarity := CosineSimilarity(queryVec, vec)
		vectorScored = append(vectorScored, scored{entry: entry, score: similarity})
	}
	sort.Slice(vectorScored, func(i, j int) bool {
		return vectorScored[i].score > vectorScored[j].score
	})

	// Reciprocal Rank Fusion
	k := h.K
	if k == 0 {
		k = 60
	}
	rrfScores := make(map[string]float64)
	entries := make(map[string]MemoryEntry)

	for rank, entry := range bm25Results {
		rrfScores[entry.Path] += 1.0 / (k + float64(rank+1))
		entries[entry.Path] = entry
	}
	for rank, vs := range vectorScored {
		rrfScores[vs.entry.Path] += 1.0 / (k + float64(rank+1))
		entries[vs.entry.Path] = vs.entry
	}

	// Sort by fused score
	type fused struct {
		entry MemoryEntry
		score float64
	}
	results := make([]fused, 0, len(rrfScores))
	for path, score := range rrfScores {
		results = append(results, fused{entry: entries[path], score: score})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	out := make([]MemoryEntry, 0, len(results))
	for _, r := range results {
		out = append(out, r.entry)
	}
	return out, nil
}

// Delegate the rest of MemoryProvider to the inner provider.
func (h *HybridMemorySearch) View(ctx context.Context, scope Scope, path string) (string, error) {
	return h.Inner.View(ctx, scope, path)
}
func (h *HybridMemorySearch) Create(ctx context.Context, scope Scope, path, content string) error {
	delete(h.cache, path)
	return h.Inner.Create(ctx, scope, path, content)
}
func (h *HybridMemorySearch) StrReplace(ctx context.Context, scope Scope, path, oldStr, newStr string) error {
	delete(h.cache, path)
	return h.Inner.StrReplace(ctx, scope, path, oldStr, newStr)
}
func (h *HybridMemorySearch) Delete(ctx context.Context, scope Scope, path string) error {
	delete(h.cache, path)
	return h.Inner.Delete(ctx, scope, path)
}
func (h *HybridMemorySearch) Rename(ctx context.Context, scope Scope, oldPath, newPath string) error {
	if vec, ok := h.cache[oldPath]; ok {
		h.cache[newPath] = vec
		delete(h.cache, oldPath)
	}
	return h.Inner.Rename(ctx, scope, oldPath, newPath)
}
func (h *HybridMemorySearch) List(ctx context.Context, scope Scope, path string) ([]string, error) {
	return h.Inner.List(ctx, scope, path)
}
