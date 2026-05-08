package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	ab "github.com/everfaz/autobuild-sdk"
	sdktokenizers "github.com/everfaz/autobuild-sdk/providers/tokenizers"
)

var (
	backendSkillsOnce     sync.Once
	backendSkillsProvider ab.SkillProvider
	backendSkillsErr      error
	skillReloaderOnce     sync.Once
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
	return newModeEngineWithDB(provider, model, logContext, nil, nil)
}

func newModeEngineWithDB(provider ab.LLMProvider, model string, logContext RuntimeLogContext, db *sql.DB, threads ab.ThreadProvider) (*ab.Engine, *agentRuntime, error) {
	backendSkillsOnce.Do(func() {
		backendSkillsProvider, backendSkillsErr = loadBackendSkills()
	})
	skills := backendSkillsProvider
	memory, memRoots, err := loadBackendMemory()
	if err != nil {
		memory = nil
		memRoots = ab.DefaultMemoryRoots
	}

	// Strip routing prefix from model for providers that need a bare model name
	// (e.g. "anthropic/claude-haiku-4-5-20251001" → "claude-haiku-4-5-20251001").
	// The RoutedLLMProvider handles routing via the prefix; internal SDK components
	// like EpisodicCompactor, InferredMemoryWriter, LLMPlanner call Chat() directly
	// and must receive only the bare name.
	bareModel := model
	if _, modelOnly := ab.ParseModelRef(model); modelOnly != "" {
		bareModel = modelOnly
	}

	rt := &agentRuntime{
		chatID:      logContext.ChatID,
		modelName:   model,
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
	engine.Threads = threads
	if checkpointsEnabledForMode(logContext.Mode) {
		engine.Checkpoints = checkpointProv
	}
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
		WithModel(bareModel).
		// Memory roots: labeled dirs matching Claude's profile/facts/project structure
		WithMemoryRoots(memRoots...).
		// Memory token cap: prevent enormous memory from overflowing context
		WithMaxMemoryTokens(8_000).
		// Safety: tool call inspection
		WithSafety(ab.NewSafetyChain(
			ab.DefaultDangerousCommandFilter(),
			ab.DefaultSecretLeakFilter(),
		)).
		// Output: response filtering
		WithOutputFilter(ab.NewOutputFilterChain(
			ab.DefaultSecretRedactionFilter(),
		)).
		// Verification: local check (no extra LLM call)
		WithVerification(ab.VerificationChain{Strategies: []ab.VerificationStrategy{
			ab.LocalVerification{MustNotBeEmpty: true, MinLength: 5, NoHallucination: true},
			ab.CompletionVerification{MinLength: 5},
		}}).
		WithMaxVerifyRetry(1).
		// Compaction: episodic with differential scoring (pre-filters low-importance msgs)
		WithCompactor(&ab.EpisodicCompactor{
			Provider:            provider,
			Model:               bareModel,
			MaxWords:            400,
			ImportanceThreshold: 0.25,
			EpisodeThreshold:    0.65,
		}).
		// Skills: cap LayerSkills at 6k tokens to prevent system prompt overflow
		WithMaxSkillTokens(6_000).
		// Memory inference: extract persistent facts, deduplicated
		WithMemoryWriter(&ab.InferredMemoryWriter{
			Provider:        provider,
			Model:           bareModel,
			MaxFacts:        3,
			MinConfidence:   0.75,
			DedupeThreshold: 0.6,
		}).
		// Planning: LLM-driven decision + executable DAG proposal
		WithPlanner(&ab.LLMPlanner{Provider: provider, Model: bareModel, MaxExecutables: 6}).
		WithAutoApprovePlan(true).
		// Extended thinking for deep-work mode
		WithThinkingBudget(resolveThinkingBudget(logContext.Mode)).
		// Session context: inject time every turn
		WithSessionContext(ab.LocalTimeSessionContext()).
		// Tokenizer: auto-selects per model (gpt-4o→O200K, gpt-4→CL100K, claude→heuristic)
		WithTokenizer(sdktokenizers.NewAutoForModel(bareModel)).
		// Persistence: SQLite conversation store
		WithConversationStore(convStore).
		// Wellbeing detection
		WithWellbeing(ab.DefaultWellbeingDetector{})

	rt.runtime = runtime

	// Skill hot-reload: watch skills directory once for the shared provider
	if backendSkillsErr == nil {
		skillReloaderOnce.Do(func() {
			if reloadable, ok := skills.(ab.ReloadableSkillProvider); ok {
				reloader := ab.NewSkillReloader("./skills", reloadable)
				reloader.SetOnReload(func(loaded, removed []string) {
					log.Printf("skills reloaded: +%v -%v", loaded, removed)
				})
				reloader.Start(context.Background())
			}
		})
	}

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

// ── Thinking budget ───────────────────────────────────────────────────────────

// resolveThinkingBudget returns the thinking token budget for the given mode.
// Only deep-work mode enables extended thinking. Override via THINKING_BUDGET env var.
func resolveThinkingBudget(mode string) int {
	if strings.ToLower(strings.TrimSpace(mode)) != "deep-work" {
		return 0
	}
	if env := os.Getenv("THINKING_BUDGET"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n >= 1024 {
			return n
		}
	}
	return 10_000
}

// ── Mode loader ───────────────────────────────────────────────────────────────

func loadBackendModes() (ab.ModeProvider, error) {
	return ab.LoadModeProviderFromDirs(
		"modes",
		filepath.Join("example", "backend-chat", "modes"),
		filepath.Join("..", "backend-chat", "modes"),
	)
}

func checkpointsEnabledForMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "analyst", "code-reviewer":
		return false
	default:
		return true
	}
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

## Generative-UI components

` + describeComponentCatalog() + `
When the user asks to **display, show, render, or visualize** a domain UI (e.g. a patient chart, medication list, appointment form), call ` + "`render_component`" + ` with one of the names above. Do NOT write JSX/HTML files via ` + "`file_write`" + ` for these cases — the component code already exists in the frontend; you only supply props.

When you need **structured input from the user that's better collected through a UI than a free-form question** (dates, picks, multi-field forms), call ` + "`await_component_input`" + ` instead. That tool renders the component AND pauses you until the user submits — its result is the user's data as JSON, which you reason against on the next turn.

When you determine that collecting structured user input is useful, you may use ` + "`QuestionnaireForm`" + ` and design/order the questions in the way that best fits the user's objective. Use ` + "`type: single|multi|text`" + ` and provide ` + "`options`" + ` whenever choices are helpful.

Use ` + "`file_write`" + ` only when the user explicitly asks for source code, scripts, or document files they want to download or edit.

**When a tool call is rejected by the user (HIL):** stop, acknowledge briefly, and ASK what they want to do instead. Do NOT dump the rejected content into the chat as a fenced code block, do NOT retry the same write under a different filename, and do NOT bypass the rejection by emitting the same content inline. The user's rejection is final until they ask for a different action.

The **dispatch-subagents** tool lets you spawn parallel sub-agents for independent tasks. Each subagent runs its own focused agent loop and returns a structured result. Use it for fan-out work: multiple independent research tasks, creating multiple files in parallel, or validating from multiple angles simultaneously.

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

You have access to persistent memory with the following structure:

**User scope** (persists across all projects):
- ` + "`/profile/`" + ` — your identity, preferences, working style
- ` + "`/facts/`" + ` — things you've told me worth remembering

**Project scope** (specific to this project):
- ` + "`/`" + ` — project decisions, architecture, workflow state

Rules:
- Write preferences and recurring context that matters in future sessions
- Use ` + "`str_replace`" + ` to update existing entries — never create duplicates
- Search before writing to check if a similar entry already exists
- Do NOT write ephemeral facts (currently debugging X, in a hurry) — those are session-only
- "I no longer work at X" → delete or replace the old entry, don't add a new one

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
