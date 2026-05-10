package autobuild

import (
	"strings"
)

// SystemPromptLayer identifies one section of the assembled system prompt.
// Layers are injected in priority order: Core is always present and never
// overridden; Session is the most ephemeral.
//
// Final prompt order (top to bottom):
//
//	Core → Behavior → Memory → Skills → Session → Mode
//
// This mirrors the injection order Claude uses internally:
//   - Core instructions are the invariant foundation
//   - Memory informs without overriding behavior
//   - Skills add domain knowledge
//   - Session adds situational awareness
//   - Mode is the final behavioral overlay
type SystemPromptLayer string

const (
	// LayerCore is the agent's invariant identity and operating principles.
	// Never changes at runtime. Set once at engine initialization.
	// Example: "You are an engineering assistant for Maxwell Clinic..."
	LayerCore SystemPromptLayer = "core"

	// LayerBehavior defines how the agent reasons and acts.
	// Includes: when to search, how to use memory, tool selection logic,
	// formatting rules, what not to do.
	LayerBehavior SystemPromptLayer = "behavior"

	// LayerMemory is populated at conversation start from MemoryProvider.
	// Contains user preferences, project state, and persistent decisions.
	// Refreshed each session — not hardcoded.
	LayerMemory SystemPromptLayer = "memory"

	// LayerSession holds ephemeral situational context for this conversation.
	// Includes: current time, active thread ID, what the user is viewing,
	// recent observations worth surfacing.
	LayerSession SystemPromptLayer = "session"

	// LayerMode is the active mode's prompt content.
	// Applied last — it is the most specific behavioral overlay.
	LayerMode SystemPromptLayer = "mode"
)

// layerOrder defines the injection sequence. Earlier = more foundational.
var layerOrder = []SystemPromptLayer{
	LayerCore,
	LayerBehavior,
	LayerMemory,
	LayerSession,
	LayerMode,
}

// SystemPromptBuilder assembles a layered system prompt.
// Each layer is a named section that can be set independently and
// updated at runtime (e.g. memory refreshed, skills loaded).
//
// Usage:
//
//	builder := NewSystemPromptBuilder()
//	builder.Set(LayerCore, corePrompt)
//	builder.Set(LayerMemory, memoryContent)
//	prompt := builder.Build()
type SystemPromptBuilder struct {
	layers         map[SystemPromptLayer]string
	maxLayerTokens map[SystemPromptLayer]int // per-layer token caps; 0 = unlimited
}

// NewSystemPromptBuilder returns an empty builder.
func NewSystemPromptBuilder() *SystemPromptBuilder {
	return &SystemPromptBuilder{
		layers:         make(map[SystemPromptLayer]string),
		maxLayerTokens: make(map[SystemPromptLayer]int),
	}
}

// SetMaxLayerTokens caps the token count for a specific layer.
// When Build() is called with a tokenizer, layers exceeding their cap are
// truncated before concatenation.
//
// Example — cap memory to 8k tokens:
//
//	builder.SetMaxLayerTokens(LayerMemory, 8000)
func (b *SystemPromptBuilder) SetMaxLayerTokens(layer SystemPromptLayer, maxTokens int) {
	b.maxLayerTokens[layer] = maxTokens
}

// Build assembles all non-empty layers into a single system prompt string.
// Layers are separated by a blank line and injected in canonical order.
// No token enforcement — use BuildWithBudget for token-aware assembly.
func (b *SystemPromptBuilder) Build() string {
	return b.BuildWithBudget(nil)
}

// BuildWithBudget assembles layers like Build, but enforces per-layer token
// caps set via SetMaxLayerTokens. If tok is nil, falls back to Build behavior.
func (b *SystemPromptBuilder) BuildWithBudget(tok Tokenizer) string {
	var parts []string
	for _, layer := range layerOrder {
		content := strings.TrimSpace(b.layers[layer])
		if content == "" {
			continue
		}
		// Apply per-layer token cap if configured and tokenizer is available
		if tok != nil {
			if cap := b.maxLayerTokens[layer]; cap > 0 {
				if tok.Count(content) > cap {
					content = TruncateToTokens(content, cap, tok)
				}
			}
		}
		parts = append(parts, content)
	}
	return strings.Join(parts, "\n\n")
}

// Get returns the content for a layer. Empty if not set.
func (b *SystemPromptBuilder) Get(layer SystemPromptLayer) string {
	return b.layers[layer]
}

// Has returns true if a layer has any non-empty content.
func (b *SystemPromptBuilder) Has(layer SystemPromptLayer) bool {
	return strings.TrimSpace(b.layers[layer]) != ""
}

// Set writes content for the given layer.
// Calling Set on an existing layer replaces it.
func (b *SystemPromptBuilder) Set(layer SystemPromptLayer, content string) {
	b.layers[layer] = strings.TrimSpace(content)
}

// Append adds content to an existing layer without replacing it.
func (b *SystemPromptBuilder) Append(layer SystemPromptLayer, content string) {
	existing := b.layers[layer]
	if existing == "" {
		b.layers[layer] = strings.TrimSpace(content)
	} else {
		b.layers[layer] = existing + "\n\n" + strings.TrimSpace(content)
	}
}

// Clear removes a layer from the prompt.
func (b *SystemPromptBuilder) Clear(layer SystemPromptLayer) {
	delete(b.layers, layer)
}

