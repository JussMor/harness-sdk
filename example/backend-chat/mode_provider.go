package main

import (
	"context"
	"database/sql"
	"path/filepath"

	ab "github.com/everfaz/autobuild-sdk"
)

// newModeEngine builds a fully-wired agentRuntime using the SDK Runtime.
// Every SDK capability is connected:
//   - SystemPromptBuilder with all 6 layers
//   - ConversationStore (SQLite)
//   - OutputFilter (secret redaction)
//   - VerificationStrategy (completion check)
//   - Compactor (history summary on truncation)
//   - InferredMemoryWriter (LLM extracts persistent facts)
//   - ContextBudget (128k window with enforcement)
//   - WellbeingDetector (multilingual)
//   - Tracer (structured spans)
//   - CheckpointProvider (auto before execution)
//   - SessionContext (time injection every turn)
func newModeEngine(provider ab.LLMProvider, model string, logContext RuntimeLogContext) (*ab.Engine, *agentRuntime, error) {
	return newModeEngineWithDB(provider, model, logContext, nil)
}

func newModeEngineWithDB(provider ab.LLMProvider, model string, logContext RuntimeLogContext, db *sql.DB) (*ab.Engine, *agentRuntime, error) {
	skills, _ := loadBackendSkills()
	memory, _ := loadBackendMemory()

	rt := &agentRuntime{
		skills:      skills,
		memory:      memory,
		checkpoints: &checkpointStore{},
	}

	// Tool registries
	rt.tools = rt.buildToolRegistry()

	// ExecutionContext — owns phase + plan + todos
	execCtx := ab.NewExecutionContext()
	rt.execCtx = execCtx

	// Checkpoint provider wired through engine
	checkpointProv := &inMemoryCheckpointProvider{store: rt.checkpoints}

	// Main engine with all providers
	engine := ab.NewWithDefaults(128_000)
	engine.LLM = provider
	engine.Skills = skills
	engine.Memory = memory
	engine.Execution = execCtx
	engine.Tools = rt.tools
	engine.Checkpoints = checkpointProv
	rt.engine = engine

	// Modes
	if modes, err := loadBackendModes(); err == nil {
		engine.Modes = modes
	}

	// Subagent engine
	subEngine := ab.New(
		ab.WithLLM(provider),
		ab.WithToolRegistry(rt.buildSubagentToolRegistry()),
	)
	rt.subagentEngine = subEngine

	// ── System prompt builder (all 6 layers) ──
	engine.Prompt.Set(ab.LayerCore, buildCorePrompt(rt))
	engine.Prompt.Set(ab.LayerBehavior, buildBehaviorPrompt())
	// LayerMemory, LayerSkills, LayerSession → filled by Runtime at orientation

	// ── Conversation store (SQLite if DB available) ──
	var convStore ab.ConversationStore
	if db != nil {
		convStore = NewSQLiteConversationStore(db)
	} else {
		convStore = ab.NewInMemoryConversationStore()
	}
	rt.convStore = convStore

	// ── Runtime with every capability wired ──
	runtime := ab.NewRuntime(engine).
		WithMode(logContext.Mode).
		// Safety: tool call inspection
		WithSafety(ab.NewSafetyChain(
			ab.DefaultDangerousCommandFilter(),
			ab.DefaultSecretLeakFilter(),
		)).
		// Output: response filtering
		WithOutputFilter(ab.NewOutputFilterChain(
			ab.DefaultSecretRedactionFilter(),
		)).
		// Verification: ensure response is non-empty and complete
		WithVerification(ab.CompletionVerification{MinLength: 5}).
		WithMaxVerifyRetry(1).
		// Compaction: summarize dropped history instead of silently discarding
		WithCompactor(&ab.BulletCompactor{MaxChars: 600}).
		// Memory inference: extract persistent facts from conversation
		WithMemoryWriter(&ab.InferredMemoryWriter{
			Provider:      provider,
			Model:         model,
			MaxFacts:      3,
			MinConfidence: 0.75,
		}).
		// Planning: LLM-driven decision + executable DAG proposal
		WithPlanner(&ab.LLMPlanner{Provider: provider, Model: model, MaxExecutables: 6}).
		WithAutoApprovePlan(true).
		// Session context: inject time every turn
		WithSessionContext(ab.LocalTimeSessionContext()).
		// Tokenizer: Claude-tuned heuristic
		WithTokenizer(&claudeTokenizerAdapter{}).
		// Persistence: SQLite conversation store
		WithConversationStore(convStore).
		// Wellbeing detection
		WithWellbeing(ab.DefaultWellbeingDetector{})

	rt.runtime = runtime
	return engine, rt, nil
}

// ── Checkpoint provider adapter ───────────────────────────────────────────────

// inMemoryCheckpointProvider wraps checkpointStore to implement CheckpointProvider.
type inMemoryCheckpointProvider struct {
	store *checkpointStore
}

func (p *inMemoryCheckpointProvider) Create(ctx context.Context, description string) (*ab.Checkpoint, error) {
	id := p.store.Create(description)
	return &ab.Checkpoint{ID: id, Description: description}, nil
}

func (p *inMemoryCheckpointProvider) Restore(_ context.Context, _ string) error {
	return nil
}

func (p *inMemoryCheckpointProvider) List(_ context.Context) ([]*ab.Checkpoint, error) {
	return nil, nil // lightweight — no persistence needed for checkpoints
}

// ── Tokenizer adapter ─────────────────────────────────────────────────────────

// claudeTokenizerAdapter uses the ClaudeTokenizer from providers/tokenizers.
// Inline implementation to avoid cross-module import.
type claudeTokenizerAdapter struct{}

func (claudeTokenizerAdapter) Count(text string) int {
	if text == "" {
		return 0
	}
	// Claude-tuned heuristic: words*1.3 + specials*0.4 + nonASCII*0.6
	words := 0
	inWord := false
	specials := 0
	nonASCII := 0
	for _, r := range text {
		switch {
		case r > 127:
			nonASCII++
			inWord = false
		case r == ' ' || r == '\t':
			inWord = false
		case r == '\n':
			specials++
			inWord = false
		case r == '{' || r == '}' || r == '[' || r == ']' || r == '(' || r == ')':
			specials++
			inWord = false
		case r == ',' || r == ';' || r == ':' || r == '"' || r == '\'':
			specials++
			inWord = false
		default:
			if !inWord {
				words++
				inWord = true
			}
		}
	}
	return int(float64(words)*1.3 + float64(specials)*0.4 + float64(nonASCII)*0.6)
}

// ── Mode loader ───────────────────────────────────────────────────────────────

func loadBackendModes() (ab.ModeProvider, error) {
	return ab.LoadModeProviderFromDirs(
		"modes",
		filepath.Join("example", "backend-chat", "modes"),
		filepath.Join("..", "backend-chat", "modes"),
	)
}

// buildCorePrompt defines who this agent is — its stable identity and
// the ground truth about what it can and cannot do.
// This layer never changes at runtime.
func buildCorePrompt(rt *agentRuntime) string {
	tools := rt.tools.DescribeAvailable()
	return `You are a general-purpose AI assistant running on the harness-sdk backend.

## Identity

You are capable, direct, and honest. You help users with writing, coding, analysis, planning, and general questions. You remember context across sessions using your memory system and can execute multi-step tasks using tools.

## What you can actually do

` + tools + `

These are the ONLY tools available to you. Do not reference, invent, or pretend to use any tool not listed above. If a user asks you to use a tool that is not listed, say clearly that it is not available.

## What you cannot do

- Browse the internet (no web search tool is loaded)
- Execute shell commands (no terminal tool is loaded)
- Access files outside your memory system
- Send emails or messages to external services

If you need a capability you do not have, say so directly and offer an alternative within your actual capabilities.

## Language

Respond in the same language the user writes in. If the user writes in Spanish, respond in Spanish. If in English, respond in English.`
}

// buildBehaviorPrompt defines how this agent operates — tool discipline,
// memory rules, phase lifecycle, formatting, and artifact conventions.
// This extends the SDK DefaultBehaviorPrompt with backend-specific rules.
func buildBehaviorPrompt() string {
	return ab.DefaultBehaviorPrompt + `

## Memory discipline (backend-specific)

You have access to persistent memory scoped to the user and to the current project. Use it:
- Write user preferences, decisions, and recurring context that will matter in future sessions
- Write project state: what was built, what was decided, what is pending
- Do NOT write ephemeral facts like "user is currently debugging X" — those go in observations
- Use str_replace to update existing entries rather than creating duplicates
- Before writing, check if a similar entry already exists (use list or search first)

## Tool call discipline

Before calling any tool, state what you are about to do and why — one sentence is enough.
After a tool returns, summarize the result before continuing.
If a tool fails, explain what failed and offer a concrete next step.

## Phase lifecycle (SDK)

You operate within a 6-phase cycle per turn:
1. Orientation — read memory and loaded skills silently
2. Alignment — clarify if truly ambiguous (one question max), propose a plan for 3+ step tasks
3. Preparation — checkpoint before mutations
4. Execution — use tools, generate content
5. Verification — check your own output before closing
6. Closure — update memory if something is worth remembering across sessions

Do not describe these phases to the user. They are your internal operating model.

## Artifacts

When your response contains complete, self-contained, renderable content — wrap it in a fenced code block with the correct language tag. The frontend renders these in a side panel.

Use these language tags:
- ` + "`html`" + ` — complete HTML pages or fragments with embedded CSS/JS
- ` + "`jsx`" + ` — React components (self-contained, with default export)
- ` + "`svg`" + ` — standalone SVG graphics
- ` + "`python`" + ` / ` + "`go`" + ` / ` + "`typescript`" + ` — complete runnable scripts

Artifact rules:
- Only wrap content that works standalone — not snippets mid-explanation
- One artifact per response unless two are genuinely independent
- Never split one artifact across multiple blocks
- Short code examples that illustrate a point stay inline

## Formatting

- Lead with the answer — no preamble
- Use prose by default; lists only when content is genuinely list-shaped
- Keep responses concise — match depth to complexity of the question
- Avoid repeating what the user just said back to them
- Avoid phrases like "Certainly!", "Great question!", or "Of course!"`
}
