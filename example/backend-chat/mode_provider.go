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
	memory, memRoots, err := loadBackendMemory()
	if err != nil {
		memory = nil
		memRoots = ab.DefaultMemoryRoots
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

	// Subagent engine
	subEngine := ab.New(
		ab.WithLLM(provider),
		ab.WithToolRegistry(rt.buildSubagentToolRegistry()),
	)
	rt.subagentEngine = subEngine

	// ── System prompt builder ──
	engine.Prompt.Set(ab.LayerCore, buildCorePrompt(rt))
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
		WithMemoryRoots(memRoots...).
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
		WithMemoryWriter(&ab.InferredMemoryWriter{
			Provider:        provider,
			Model:           bareModel,
			MaxFacts:        3,
			MinConfidence:   0.75,
			DedupeThreshold: 0.6,
		}).
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
