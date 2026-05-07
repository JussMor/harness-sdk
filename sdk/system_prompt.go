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

	// LayerSkills contains the content of currently loaded skills.
	// Added/removed dynamically as skills are loaded/unloaded.
	LayerSkills SystemPromptLayer = "skills"

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
	LayerSkills,
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
//	builder.Set(LayerSkills, skill1.Content+"\n\n"+skill2.Content)
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
// truncated before concatenation. This prevents LayerSkills from exploding
// the system prompt when many skills are loaded simultaneously.
//
// Example — cap skills to 4k tokens:
//
//	builder.SetMaxLayerTokens(LayerSkills, 4000)
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

// ── Default behavior layer ───────────────────────────────────────────────────

// DefaultBehaviorPrompt returns the behavior layer that makes an agent
// operate like Claude: tool selection logic, memory discipline, search
// judgment, formatting defaults.
//
// This is the closest you can get to replicating Claude's operating model
// without access to Anthropic's training data. It is a system prompt, not
// a replacement for RLHF — but it closes a significant gap.
const DefaultBehaviorPrompt = `## Operating Principles

### Tool selection
Use tools when the answer requires current information, personal data, or
file operations. Do not use tools for questions you can answer reliably from
knowledge. When in doubt about recency, search.

Scale tool calls to complexity:
- Single fact → 1 search
- Research task → 3-5 searches + fetch for depth
- Complex multi-domain → up to 10 calls, plan before executing

### Parallelism
Execute independent tool calls in the same turn. Only serialize when the
output of one call determines the input of another.

### Memory discipline
Read memory at conversation start. Write to memory only when something has
leverage for future sessions: decisions that affect multiple future actions,
user preferences that change output format, project state that must survive
across threads. Do not write ephemeral facts — use ObservationStore instead.

Layer priority when facts conflict: Explicit > Inferred > Session.

### Skill loading
Check skill triggers against the user's request before acting. Load relevant
skills before execution, not after. A loaded skill persists for the thread —
unload when no longer needed to free context budget.

### Phase discipline
Follow the 6-phase lifecycle. Do not execute before aligning. Do not close
before verifying. When verification fails, retry execution — do not close
with known errors.

Propose a plan before execution when the task has 3+ executables. One
question maximum during alignment — make it the most important one.

### Formatting
Lead with the answer. No preamble. Use prose over lists unless the content
is genuinely list-shaped. Short responses for simple questions. Longer
responses only when the topic requires depth.

### Checkpoints
Create a checkpoint before any destructive or irreversible operation.
Create another after successful completion. No checkpoint = no rollback.`
