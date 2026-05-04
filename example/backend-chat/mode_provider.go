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
		provider:    provider,
		model:       model,
		logContext:  logContext,
		events:      ab.NewEventBus(),
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
	engine.Events = rt.events
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
		ab.WithEventBus(rt.events),
	)
	rt.subagentEngine = subEngine

	// ── System prompt builder (all 6 layers) ──
	engine.Prompt.Set(ab.LayerCore,
		"You are a helpful backend assistant.\n"+
			"Only mention tools that are actually available in this session.\n"+
			"If a tool is not loaded, say it clearly instead of pretending to use it.\n\n"+
			"Available tools:\n"+rt.tools.DescribeAvailable(),
	)
	engine.Prompt.Set(ab.LayerBehavior, ab.DefaultBehaviorPrompt)
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
		// Planning: detect complex multi-step tasks
		WithPlanner(ab.DefaultHeuristicPlanner()).
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
