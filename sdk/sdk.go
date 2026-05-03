// Package autobuild provides a minimal, extensible SDK for building AI agents
// that operate like Claude — same lifecycle, same memory discipline, same
// layered prompt assembly. The SDK is the skeleton; the LLM you plug in is
// the brain.
//
// # Architecture
//
// The SDK is opt-in: every provider is optional. You build an [Engine] via
// [New] (or [NewWithDefaults] for sensible defaults) and wire only what you
// need. The [Runtime] orchestrator connects the providers automatically.
//
// # Core abstractions
//
//   - [ExecutionContext]   — phase + plan + todos unified (the 6-phase lifecycle)
//   - [SystemPromptBuilder]— layered system prompt assembly
//   - [MemoryProvider]     — persistent two-scope memory (User / Project)
//   - [ObservationStore]   — session-scoped working memory (not persistent)
//   - [SkillProvider]      — on-demand knowledge loading with scored matching
//   - [ToolRegistry]       — typed tool definitions with JSON Schema
//   - [ModeProvider]       — execution modes with model + tool config
//   - [LLMProvider]        — chat completion backend (use [RoutedLLMProvider]
//     for multi-model)
//   - [CheckpointProvider] — safety snapshots before mutations
//   - [SandboxDriver]      — command execution and file I/O
//   - [EventBus]           — publish/subscribe for inter-component events
//   - [ContextBudget]      — token budget across context layers
//
// # Quick start
//
//	engine := autobuild.NewWithDefaults(128_000)
//	engine.LLM = myLLMProvider
//	engine.Memory = myMemoryProvider
//	engine.Tools = myToolRegistry
//
//	runtime := autobuild.NewRuntime(engine)
//	result, err := runtime.Run(ctx, "What changed in the auth module last week?")
//
// # The 6-phase lifecycle
//
// Every conversation flows through six phases owned by [ExecutionContext]:
//
//	Orientation → Alignment → Preparation → Execution → Verification → Closure
//
// The [Runtime] advances phases automatically based on signals from the LLM
// (tool calls, plan proposals, completion). For full control, use
// [RunAgentLoopWithEngine] directly and drive [ExecutionContext] yourself.
//
// # Layered system prompt
//
// The [SystemPromptBuilder] assembles six layers in priority order:
//
//	Core     → invariant identity (set once)
//	Behavior → operating principles (use [DefaultBehaviorPrompt])
//	Memory   → injected from [MemoryProvider] at conversation start
//	Skills   → content of currently loaded skills
//	Session  → ephemeral context (time, observations, current state)
//	Mode     → active mode's overlay (most specific)
//
// # Memory layers
//
// [MemoryLayer] separates Explicit (user said it directly), Inferred
// (derived from conversation), and Session (this conversation only).
// On conflict, Explicit always wins.
//
// See docs/claude-model.md for the full implementation guide.
package autobuild

// Version is the SemVer of the SDK.
const Version = "0.2.0"
