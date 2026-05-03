# Harness SDK — Claude Model Implementation Guide

How to wire the SDK so an agent behaves structurally like Claude:
same lifecycle, same memory discipline, same tool judgment, same
layered system prompt. This is the skeleton. The LLM you plug in
is the brain.

---

## TL;DR — The fast path

```go
engine := autobuild.NewWithDefaults(128_000)
engine.LLM = myLLM
engine.Memory = myMemory
engine.Tools = myTools
engine.Skills = mySkills

runtime := autobuild.NewRuntime(engine)
result, _ := runtime.Run(ctx, "Help me refactor auth")
```

The `Runtime` orchestrator runs the full 6-phase lifecycle, reads memory,
matches and loads skills, surfaces observations, dispatches tools, and
detects memory write triggers — all automatically. Skip to "Manual control"
below if you need fine-grained control.

---

## What changed from the original SDK

| Removed | Replaced by | Why |
|---|---|---|
| `WorkflowEngine` + `PlanProvider` | `ExecutionContext` | They always move together — one object owns phase, plan, and todos |
| `TaskProvider` | `ExecutionContext` (Plan absorbs Task) | Same concept (steps + dependencies), different scope — redundant |
| `Engine.Router ModelRouter` | Duck typing on `LLM` field | `RoutedLLMProvider` already implements both interfaces |
| `MemoryProvider.Insert(line int)` | Use `StrReplace` instead | Line numbers go stale the moment the file changes |
| `Skill.MatchesTrigger` (bool) | `Skill.MatchScore` (float) + `SkillMatch` | Naive substring match doesn't rank — replaced with scored matching |

New additions:

- `Runtime` — the orchestrator that connects every provider automatically
- `ExecutionContext` — phase + plan + todos unified
- `MemoryLayer` — Explicit > Inferred > Session priority
- `ObservationStore` — session working memory (not permanent)
- `SystemPromptBuilder` — layered prompt assembly with `Get`/`Has`/`Set`/`Append`
- `ContextBudget` — token budget across context layers
- `DispatchParallel` — parallel tool execution (was promised, now real)
- `AreIndependent` — heuristic for parallel-safe tool calls
- `SkillMatch` + `MatchScore` — scored skill ranking
- `DefaultObservationFilter` — auto-filters tool results into observations
- `DefaultMemoryTriggerDetector` — recognizes "remember that", "I moved to", etc.
- `Scope.Session` — third memory scope (delegates to ObservationStore)

---

## The Runtime — what it does for you

The `Runtime` is what closes the gap between "bag of providers" and
"working agent". It wires the providers automatically:

```
Run(userMessage)
  │
  ├── Orientation
  │     ├── Read memory → LayerMemory
  │     ├── Match skills → load top N → LayerSkills
  │     └── Surface observations → LayerSession
  │
  ├── Alignment
  │     └── (LLM proposes plan during Execution if needed)
  │
  ├── Preparation
  │     ├── Auto-checkpoint (if CheckpointProvider wired)
  │     └── Verify ContextBudget
  │
  ├── Execution
  │     ├── RunAgentLoopWithEngine
  │     └── OnToolResult → ObservationFilter → record
  │
  ├── Verification
  │     └── (consumer-defined hooks)
  │
  └── Closure
        └── MemoryTriggerDetector → write to memory
```

What the consumer no longer has to write:

- 50+ lines of memory-read-and-inject boilerplate
- Skill match-and-load loop
- Observation filtering on every tool result
- Memory write trigger detection
- Phase advancement logic
- Context budget verification

---

## Manual control — when you need it

If the Runtime's defaults don't fit your case, drive `RunAgentLoopWithEngine`
directly. The Runtime is a wrapper, not a replacement:

```go
engine := autobuild.NewWithDefaults(128_000)
exec := engine.Execution

// Phase 0: Orientation — your way
memContent, _ := engine.Memory.View(ctx, autobuild.ScopeUser, "/profile")
engine.Prompt.Set(autobuild.LayerMemory, memContent)

matches, _ := engine.Skills.Match(ctx, userMessage)
for _, m := range matches[:3] {
    if m.Score < 0.3 { break }
    skill, _ := engine.Skills.Load(ctx, m.Skill.Name)
    engine.Prompt.Append(autobuild.LayerSkills, skill.Content)
}

exec.Advance(ctx)

// Phase 1: Alignment — propose plan if complex
if isComplexTask(userMessage) {
    plan, _ := exec.Propose(ctx, autobuild.Plan{
        Title: "Refactor auth",
        Executables: []autobuild.Executable{ /* ... */ },
    })
    _ = plan
}

exec.Advance(ctx)

// Phase 2: Preparation
engine.Checkpoints.Create(ctx, "Before refactor")

exec.Advance(ctx)

// Phase 3: Execution
result, _ := autobuild.RunAgentLoopWithEngine(ctx, engine, "code-agent",
    autobuild.AgentLoopConfig{
        MaxTurns: 50,
        OnToolResult: func(call autobuild.ToolCallEntry, r autobuild.ToolResult) autobuild.ToolResult {
            obs := autobuild.DefaultObservationFilter(call, r)
            if obs.Content != "" {
                engine.Observations.Record(ctx, obs)
            }
            return r
        },
    },
    messages,
)

exec.Advance(ctx) // Verification
exec.Advance(ctx) // Closure

// Closure: detect memory triggers manually
if layer, content, ok := autobuild.DefaultMemoryTriggerDetector(userMessage); ok {
    engine.Memory.Create(ctx, autobuild.ScopeUser, "/facts/new.md", content)
    _ = layer
}
```

---

## Memory layers — how to write correctly

```go
// User said it explicitly → Explicit layer (top priority)
layeredMem.WriteLayered(ctx, autobuild.ScopeUser, "/profile/work.md",
    "Works at Maxwell Clinic on EverBetter EHR",
    autobuild.MemoryLayerExplicit)

// You inferred from conversation → Inferred layer
layeredMem.WriteLayered(ctx, autobuild.ScopeUser, "/profile/preferences.md",
    "Prefers short responses with code examples",
    autobuild.MemoryLayerInferred)

// Only this session → ObservationStore, not memory
engine.Observations.Record(ctx, autobuild.Observation{
    Source:  "user_message",
    Content: "Currently debugging a race condition in payment flow",
})

// On conflict, Explicit always wins
entries, _ := layeredMem.SearchLayered(ctx, autobuild.ScopeUser, "work")
autobuild.SortByPriority(entries) // Explicit first
```

---

## System prompt layers — assembly order

```
LayerCore      → who the agent is, invariant
LayerBehavior  → DefaultBehaviorPrompt (set automatically by Runtime)
LayerMemory    → injected from MemoryProvider at orientation
LayerSkills    → content of loaded skills (added by Runtime)
LayerSession   → time, observations, current state (added by Runtime)
LayerMode      → active mode's overlay (applied last)
```

The Runtime fills LayerBehavior, LayerMemory, LayerSkills, and LayerSession.
You fill LayerCore once at startup.

---

## Multi-model routing — the right way

No separate `Router` field. Duck typing handles it:

```go
engine.LLM = autobuild.NewRoutedLLMProvider("anthropic",
    map[string]autobuild.LLMProvider{
        "anthropic": anthropicProvider,
        "ollama":    ollamaProvider,
    })

// In mode's system.md frontmatter:
// model: anthropic/claude-sonnet-4-20250514  → routes to anthropic
// model: ollama/llama3                        → routes to ollama
```

---

## Parallel tool dispatch

```go
calls := response.ToolCalls

if autobuild.AreIndependent(calls) {
    results := dispatcher.DispatchParallel(ctx, calls, sandboxID)
    _ = results
} else {
    results := dispatcher.DispatchAll(ctx, calls, sandboxID)
    _ = results
}
```

The rule: if the result of A does not feed parameters of B, they are independent.

---

## Customizing Runtime behavior

```go
runtime := autobuild.NewRuntime(engine).
    WithMode("code-agent").
    WithMaxSkills(5).            // load up to 5 skills per turn
    WithSkillThreshold(0.5).      // require higher relevance
    WithObservationFilter(myFilter).
    WithMemoryTrigger(myDetector)
```

- `ObservationFilter` decides which tool results become observations
- `MemoryTriggerDetector` decides when to write to memory based on user message

---

## What this gives you vs raw LLM

| Capability | Raw LLM API | SDK + Runtime |
|---|---|---|
| Phase discipline | Implicit in prompt | Explicit, enforced by ExecutionContext |
| Memory persistence | None | MemoryProvider with auto-read at orientation |
| Skill loading | Manual | Auto-match + load via Runtime |
| Plan + todos | Manual | ExecutionContext owns both |
| Multi-model routing | Manual | RoutedLLMProvider + duck typing |
| Observation store | None | Auto-filtered from tool results |
| Context budget | None | ContextBudget with overflow warnings |
| Checkpoint discipline | None | Auto-checkpoint in Preparation |
| Parallel tools | Manual goroutines | DispatchParallel + AreIndependent |
| Memory write triggers | None | DefaultMemoryTriggerDetector |
| Token observability | Usage in response | ReasoningTrace + TotalUsage |

---

## What this does NOT give you

- **RLHF-trained judgment** — `DefaultBehaviorPrompt` approximates it,
  but the model's ability to infer when to use a tool comes from training.
- **Anthropic's actual system prompt** — not public.
- **Model quality** — swap a weaker model and decisions get worse regardless
  of scaffolding.

The SDK is the skeleton. The LLM is the brain. The system prompt is the
personality. All three together get you structurally close.

