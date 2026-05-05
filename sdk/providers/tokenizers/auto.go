package tokenizers

import (
	"strings"
	"sync"

	autobuild "github.com/everfaz/autobuild-sdk"
)

// AutoTokenizer selects the appropriate tokenizer based on the model name.
// It transparently swaps between Tiktoken (CL100K/O200K) and ClaudeTokenizer
// without requiring manual configuration.
//
// Model name → tokenizer:
//
//	gpt-4o*, o1*, o3*       → TiktokenTokenizer (O200K)
//	gpt-4*, gpt-3.5*        → TiktokenTokenizer (CL100K)
//	claude*                 → ClaudeTokenizer (heuristic)
//	other (ollama, etc.)    → ClaudeTokenizer (best generic fallback)
//
// Usage:
//
//	runtime := ab.NewRuntime(engine).
//	    WithTokenizer(tokenizers.NewAuto())
//
// AutoTokenizer caches resolved tokenizers per model so the lookup is O(1)
// after the first call.
type AutoTokenizer struct {
	mu       sync.RWMutex
	resolved map[string]autobuild.Tokenizer

	// CurrentModel hints which tokenizer to use when Count is called without
	// model context. Set this when creating one Tokenizer per turn/conversation.
	CurrentModel string
}

// NewAuto creates an AutoTokenizer with no current model hint.
// Use SetModel(modelName) to bind it to a specific model for accurate counting.
func NewAuto() *AutoTokenizer {
	return &AutoTokenizer{
		resolved: make(map[string]autobuild.Tokenizer),
	}
}

// NewAutoForModel creates an AutoTokenizer pre-bound to a model name.
// Equivalent to NewAuto().SetModel(modelName).
func NewAutoForModel(modelName string) *AutoTokenizer {
	a := NewAuto()
	a.SetModel(modelName)
	return a
}

// SetModel binds the tokenizer to a specific model for subsequent Count calls.
// Safe to call multiple times — the resolution is cached.
func (a *AutoTokenizer) SetModel(modelName string) {
	a.mu.Lock()
	a.CurrentModel = modelName
	a.mu.Unlock()
}

// Count returns the token count for text using the tokenizer that matches
// CurrentModel. If CurrentModel is empty, falls back to ClaudeTokenizer.
func (a *AutoTokenizer) Count(text string) int {
	a.mu.RLock()
	model := a.CurrentModel
	a.mu.RUnlock()

	tok := a.tokenizerFor(model)
	return tok.Count(text)
}

// tokenizerFor selects (and caches) the tokenizer for a given model name.
func (a *AutoTokenizer) tokenizerFor(modelName string) autobuild.Tokenizer {
	if modelName == "" {
		return ClaudeTokenizer{}
	}

	a.mu.RLock()
	if tok, ok := a.resolved[modelName]; ok {
		a.mu.RUnlock()
		return tok
	}
	a.mu.RUnlock()

	tok := selectTokenizer(modelName)
	a.mu.Lock()
	a.resolved[modelName] = tok
	a.mu.Unlock()
	return tok
}

// selectTokenizer picks a Tokenizer based on the model identifier prefix.
func selectTokenizer(modelName string) autobuild.Tokenizer {
	m := strings.ToLower(modelName)

	// OpenAI O200K family (GPT-4o, o1, o3, GPT-5+)
	switch {
	case strings.HasPrefix(m, "gpt-4o"),
		strings.HasPrefix(m, "gpt-5"),
		strings.HasPrefix(m, "o1"),
		strings.HasPrefix(m, "o3"),
		strings.HasPrefix(m, "o4"):
		return NewTiktokenO200K()
	}

	// OpenAI CL100K family (GPT-4, GPT-4-turbo, GPT-3.5)
	if strings.HasPrefix(m, "gpt-4") || strings.HasPrefix(m, "gpt-3.5") {
		return NewTiktoken()
	}

	// Claude family — use heuristic (Anthropic's tokenizer is not public)
	if strings.HasPrefix(m, "claude") {
		return ClaudeTokenizer{}
	}

	// Fallback: ClaudeTokenizer is a reasonable generic approximation
	return ClaudeTokenizer{}
}

// Verify interface
var _ autobuild.Tokenizer = (*AutoTokenizer)(nil)
