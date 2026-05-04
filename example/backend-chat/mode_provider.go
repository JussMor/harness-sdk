package main

import (
	"path/filepath"

	ab "github.com/everfaz/autobuild-sdk"
)

// newModeEngine builds an agentRuntime wired to the SDK Runtime.
// This replaces the old version that used WithPlanning + WithWorkflow (removed).
func newModeEngine(provider ab.LLMProvider, model string, logContext RuntimeLogContext) (*ab.Engine, *agentRuntime, error) {
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

	// Build tool registries
	rt.tools = rt.buildToolRegistry()

	// ExecutionContext — single source of truth for phase + plan + todos
	execCtx := ab.NewExecutionContext()
	rt.execCtx = execCtx

	// Main engine
	engine := ab.NewWithDefaults(128_000)
	engine.LLM = provider
	engine.Skills = skills
	engine.Memory = memory
	engine.Events = rt.events
	engine.Execution = execCtx
	engine.Tools = rt.tools
	rt.engine = engine

	// Modes
	if modes, err := loadBackendModes(); err == nil {
		engine.Modes = modes
	}

	// Subagent engine (stripped down — no memory/skills overhead)
	subEngine := ab.New(
		ab.WithLLM(provider),
		ab.WithToolRegistry(rt.buildSubagentToolRegistry()),
		ab.WithEventBus(rt.events),
	)
	rt.subagentEngine = subEngine

	// Runtime — the 6-phase orchestrator
	runtime := ab.NewRuntime(engine).
		WithMode(logContext.Mode).
		WithSafety(ab.NewSafetyChain(
			ab.DefaultDangerousCommandFilter(),
			ab.DefaultSecretLeakFilter(),
		)).
		WithSessionContext(ab.LocalTimeSessionContext()).
		WithPlanner(ab.DefaultHeuristicPlanner()).
		WithAutoApprovePlan(true)
	rt.runtime = runtime

	// Core system prompt
	engine.Prompt.Set(ab.LayerCore,
		"You are a helpful backend assistant.\n"+
			"Only mention tools that are actually available in this session.\n"+
			"If a tool is not loaded, say it clearly instead of pretending to use it.\n\n"+
			"Available tools:\n"+rt.tools.DescribeAvailable(),
	)

	return engine, rt, nil
}

func loadBackendModes() (ab.ModeProvider, error) {
	return ab.LoadModeProviderFromDirs(
		"modes",
		filepath.Join("example", "backend-chat", "modes"),
		filepath.Join("..", "backend-chat", "modes"),
	)
}
