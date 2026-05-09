package main

import (
	"database/sql"
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
)

// newModeEngine builds a fully-wired agentRuntime using the SDK Runtime.
func newModeEngine(provider ab.LLMProvider, model string, logContext RuntimeLogContext) (*ab.Engine, *agentRuntime, error) {
	return newModeEngineWithDB(provider, model, logContext, nil, nil)
}

func newModeEngineWithDB(provider ab.LLMProvider, model string, logContext RuntimeLogContext, db *sql.DB, threads ab.ThreadProvider) (*ab.Engine, *agentRuntime, error) {
	backendSkillsOnce.Do(func() {
		backendSkillsProvider, backendSkillsErr = loadBackendSkills()
	})
	skills := backendSkillsProvider
	memory, err := loadBackendMemory()
	if err != nil {
		memory = nil
	}

	// Strip routing prefix from model for providers that need a bare model name.
	bareModel := model
	if _, modelOnly := ab.ParseModelRef(model); modelOnly != "" {
		bareModel = modelOnly
	}

	rt := &agentRuntime{
		chatID:    logContext.ChatID,
		modelName: model,
		skills:    skills,
		memory:    memory,
		execCtx:   ab.NewExecutionContext(),
	}

	// Tool registries
	rt.tools = rt.buildToolRegistry()

	// Main engine with all providers
	engine := ab.NewWithDefaults(128_000)
	engine.LLM = provider
	engine.Skills = skills
	engine.Memory = memory
	engine.Tools = rt.tools
	engine.Threads = threads
	rt.engine = engine

	// Modes
	if modes, err := loadBackendModes(); err == nil {
		engine.Modes = modes
	}

	// Subagent engine — same identity and tool awareness as parent so the LLM
	// knows what it can and cannot do within a dispatched task.
	subEngine := ab.New(
		ab.WithLLM(provider),
		ab.WithToolRegistry(rt.buildSubagentToolRegistry()),
		ab.WithPrompt(ab.NewSystemPromptBuilder()),
	)
	subEngine.Prompt.Set(ab.LayerCore, buildCorePrompt(rt))
	rt.subagentEngine = subEngine

	// ── System prompt builder ──
	engine.Prompt.Set(ab.LayerCore, buildCorePrompt(rt))
	engine.Prompt.Set(ab.LayerBehavior, ab.BuildMemoryGuidance(ab.MemoryGuidanceOptions{
		MemoryDir: "memory/",
	}))
	// LayerMemory, LayerSkills, LayerSession → filled by Runtime at orientation

	// ── Conversation store ──
	var convStore ab.ConversationStore
	if db != nil {
		convStore = NewSQLiteConversationStore(db)
	} else {
		convStore = ab.NewInMemoryConversationStore()
	}
	rt.convStore = convStore

	// ── Runtime ──
	runtime := ab.NewRuntime(engine).
		WithMode(logContext.Mode).
		WithModel(bareModel).
		WithMemoryRoots(
			ab.MemoryRoot{Scope: ab.ScopeUser, Path: "/"},
			ab.MemoryRoot{Scope: ab.ScopeProject, Path: "/"},
		).
		WithMemoryEntrypoint(ab.EntrypointName).
		WithMemoryRecaller(&ab.LLMMemoryRecaller{
			Provider: provider,
			Model:    bareModel,
		}).
		WithMaxRecalledMemories(5).
		WithMaxMemoryTokens(8_000).
		WithSafety(ab.NewSafetyChain(
			ab.DefaultDangerousCommandFilter(),
			ab.DefaultSecretLeakFilter(),
		)).
		WithCompactor(&ab.LLMCompactor{
			Provider: provider,
			Model:    bareModel,
			MaxWords: 200,
		}).
		WithMaxSkillTokens(6_000).
		WithThinkingBudget(resolveThinkingBudget(logContext.Mode)).
		WithSessionContext(ab.LocalTimeSessionContext()).
		WithTokenizer(sdktokenizers.NewAutoForModel(bareModel)).
		WithConversationStore(convStore)

	rt.runtime = runtime

	return engine, rt, nil
}

// ── Thinking budget ────────────────────────────────────────────────────────────

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

// ── Mode loader ────────────────────────────────────────────────────────────────

func loadBackendModes() (ab.ModeProvider, error) {
	return ab.LoadModeProviderFromDirs(
		"modes",
		filepath.Join("example", "backend-chat", "modes"),
		filepath.Join("..", "backend-chat", "modes"),
	)
}

// buildCorePrompt defines who this agent is — its stable identity and
// the ground truth about what it can and cannot do.
func buildCorePrompt(rt *agentRuntime) string {
	tools := rt.tools.DescribeAvailable()
	sandboxNote := ""
	if !isSandboxAvailable() {
		sandboxNote = "\nNote: the sandbox is not configured (OPEN_SANDBOX_API_KEY unset). " +
			"bash, code_interpreter, file_write, file_read, glob, and grep run against the local filesystem instead of an isolated container."
	}
	return `You are a capable AI assistant. You help users with writing, coding, analysis, planning, and general questions. You remember context across sessions and can execute multi-step tasks using tools.

## Tools

` + tools + sandboxNote + `

These are the ONLY tools available to you. Do not reference, invent, or pretend to use any tool not listed above.

## How to work on complex tasks

Use **todo_write** to track progress on any task that requires more than two steps:
1. Call todo_write (write) at the start to lay out the steps.
2. Set one item to in_progress before you start it.
3. Mark it completed as soon as it's done — never leave stale in_progress items.
4. The user can see this list; it keeps you accountable and helps them follow along.

Use **glob** to explore the file structure before reading or editing files.
Use **grep** to find definitions, usages, and references across the workspace.
Use **bash** for running commands, tests, builds, and one-off scripts.
Use **file_write** / **file_read** to create and read files in the sandbox.
Use **dispatch-subagents** for fan-out work: independent research tasks, creating multiple files in parallel, or validating from multiple angles at once.

## Generative-UI components

` + describeComponentCatalog() + `
When the user asks to **display, show, render, or visualize** a domain UI, call ` + "`render_component`" + ` with one of the names above. Do NOT write JSX/HTML files via ` + "`file_write`" + ` — the component code already exists in the frontend; you only supply props.

When you need **structured input** from the user (dates, selections, multi-field forms), call ` + "`await_component_input`" + ` — it renders the component AND pauses until the user submits.

**When a tool call is rejected (HIL):** stop, acknowledge briefly, ask what to do instead. Do not retry the same call under a different name or dump the content inline.

## What you cannot do

- Browse the internet or fetch URLs (no web tool is loaded)
- Send emails or messages to external services

If you need a capability you do not have, say so and offer an alternative.

## Language

Respond in the same language the user writes in.`
}
