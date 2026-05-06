package embedders

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"strings"
	"sync"

	autobuild "github.com/everfaz/autobuild-sdk"
)

// LocalEmbedder is a bundled embedder that requires no API key or network access.
// It uses a hashing-trick TF-IDF vectorizer with sub-word n-grams.
//
// Performance characteristics:
//   - Quality: lower than transformer models — captures lexical similarity
//     well (paraphrases with word overlap) but misses true semantic similarity
//     (synonyms without overlap).
//   - Latency: ~100µs per text (no network, no model loading)
//   - Best for: offline/air-gapped deployments, dev environments, or as a
//     fallback when the primary embedder is unavailable.
//
// For production semantic search, prefer Voyage or OpenAI embeddings.
//
// Usage:
//
//	embedder := embedders.NewLocal(384)
//	store := autobuild.NewSemanticObservationStore(embedder)
type LocalEmbedder struct {
	dimensions int

	// IDF weights — built incrementally as documents are seen.
	mu           sync.RWMutex
	docCount     int
	docFrequency map[uint32]int // hashed term → number of docs containing it
}

// NewLocal creates a LocalEmbedder with the given vector dimensionality.
// Recommended: 256-512 dimensions. Higher = more discriminating, more memory.
func NewLocal(dimensions int) *LocalEmbedder {
	if dimensions < 16 {
		dimensions = 16
	}
	return &LocalEmbedder{
		dimensions:   dimensions,
		docFrequency: make(map[uint32]int),
	}
}

// Embed returns a vector for a single text.
// The vector is L2-normalized so cosine similarity equals dot product.
func (e *LocalEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	tokens := tokenize(text)
	if len(tokens) == 0 {
		return make([]float32, e.dimensions), nil
	}

	// Update IDF stats with this document
	e.recordDocument(tokens)

	// Build TF vector via hashing trick — accumulate sub-word n-grams
	tf := make(map[uint32]float32)
	for _, tok := range tokens {
		// Unigram
		h := hashFeature(tok) % uint32(e.dimensions)
		tf[h]++
		// Character bigrams for sub-word similarity
		for i := 0; i < len(tok)-1; i++ {
			bigram := tok[i : i+2]
			h := hashFeature("##"+bigram) % uint32(e.dimensions)
			tf[h] += 0.5
		}
	}

	// Apply IDF weighting
	e.mu.RLock()
	N := float64(e.docCount)
	if N == 0 {
		N = 1
	}
	vec := make([]float32, e.dimensions)
	for h, freq := range tf {
		df := float64(e.docFrequency[h])
		if df == 0 {
			df = 1
		}
		idf := math.Log((N + 1) / (df + 1)) // smoothed
		vec[h] = float32(float64(freq) * idf)
	}
	e.mu.RUnlock()

	// L2 normalize so cosine similarity == dot product
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	if norm > 0 {
		invNorm := float32(1.0 / math.Sqrt(norm))
		for i := range vec {
			vec[i] *= invNorm
		}
	}

	return vec, nil
}

// EmbedBatch returns vectors for multiple texts. No batch optimization —
// just runs Embed for each in sequence (fast enough since no I/O).
func (e *LocalEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := e.Embed(ctx, t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// Dimensions returns the vector dimensionality.
func (e *LocalEmbedder) Dimensions() int {
	return e.dimensions
}

// recordDocument updates IDF statistics with a new document.
func (e *LocalEmbedder) recordDocument(tokens []string) {
	if len(tokens) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	seen := make(map[uint32]bool, len(tokens))
	for _, tok := range tokens {
		h := hashFeature(tok) % uint32(e.dimensions)
		if !seen[h] {
			e.docFrequency[h]++
			seen[h] = true
		}
	}
	e.docCount++
}

// tokenize splits text into lowercase words, stripping punctuation.
func tokenize(text string) []string {
	text = strings.ToLower(text)
	var tokens []string
	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return !isAlphaNum(r)
	}) {
		if len(word) > 1 {
			tokens = append(tokens, word)
		}
	}
	return tokens
}

func isAlphaNum(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		(r >= 0x80) // include unicode letters (CJK, accents, etc.)
}

// hashFeature deterministically maps a feature string to a uint32 bucket.
func hashFeature(feature string) uint32 {
	h := sha256.Sum256([]byte(feature))
	return binary.BigEndian.Uint32(h[:4])
}

// Verify interface
var _ autobuild.Embedder = (*LocalEmbedder)(nil)
