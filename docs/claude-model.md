# Harness SDK — Claude Model Implementation Guide

How to wire the SDK so an agent behaves structurally like Claude:
same lifecycle, same memory discipline, same tool judgment, same
layered system prompt. This is the skeleton. The LLM you plug in
is the brain.

---

## What changed from the original SDK

| Removed | Replaced by | Why |
|---|---|---|
| `WorkflowEngine` + `PlanProvider` | `ExecutionContext` | They always move together — one object owns phase, plan, and todos |
| `Engine.Router ModelRouter` | Duck typing on `LLM` field | `RoutedLLMProvider` already implements both interfaces; separate field was a bug waiting to happen |
| `MemoryProvider.Insert(line int)` | Use `StrReplace` instead | Line numbers go stale the moment the file changes |
| `RunnerTierNano/Mini` as the only tiers | Use `Mode.ModelSettings` directly | Tiers should map to models, not be named opaquely |

New additions:

- `ExecutionContext` — phase + plan + todos unified
- `MemoryLayer` — Explicit > Inferred > Session priority
- `ObservationStore` — session working memory (not permanent)
- `SystemPromptBuilder` — layered prompt assembly
- `ContextBudget` — token budget across context layers
- `DispatchParallel` — parallel tool execution (was documented but not provided)

---

## The full wiring

```go
package main

import (
    "context"
    autobuild "github.com/JussMor/harness-sdk/sdk"
)

func main() {
    ctx := context.Background()

    // 1. Build the engine with defaults
    engine := autobuild.NewWithDefaults(128_000) // 128k context window

    // 2. Wire your LLM — use RoutedLLMProvider for multi-model
    engine.LLM = autobuild.NewRoutedLLMProvider("anthropic", map[string]autobuild.LLMProvider{
        "anthropic": NewAnthropicProvider("claude-sonnet-4-20250514"),
        "ollama":    NewOllamaProvider("llama3"),
    })

    // 3. Wire memory — implement MemoryProvider against your DB
    engine.Memory = NewSurrealDBMemoryProvider(db)

    // 4. Wire skills from disk
    modes, _ := autobuild.LoadModesDir("./modes")
    engine.Modes = autobuild.NewStaticModeProvider(modes)

    skills, _ := autobuild.LoadSkillsDir("./skills")
    engine.Skills = NewStaticSkillProvider(skills)

    // 5. Wire tools
    registry := autobuild.NewToolRegistry()
    registry.Register(&autobuild.Tool{
        Name:     "web_search",
        Category: autobuild.ToolCategoryWeb,
        // ... parameters and Execute func
    })
    engine.Tools = registry

    // 6. Wire sandbox (optional — only needed if tools execute shell commands)
    engine.Sandbox = NewDockerSandbox()

    // 7. Wire checkpoints
    engine.Checkpoints = NewPostgresCheckpointProvider(db)

    // 8. Assemble the system prompt
    orientationPrompt := buildOrientationPrompt(ctx, engine)
    engine.Prompt.Set(autobuild.LayerCore, coreIdentityPrompt)
    engine.Prompt.Set(autobuild.LayerBehavior, autobuild.DefaultBehaviorPrompt)
    engine.Prompt.Set(autobuild.LayerMemory, orientationPrompt)

    // 9. Run
    result, err := autobuild.RunAgentLoopWithEngine(ctx, engine, "balanced", autobuild.AgentLoopConfig{
        MaxTurns: 50,
    }, []autobuild.ChatMessage{
        {Role: autobuild.RoleUser, Content: "Help me refactor the auth module"},
    })
}
```

---

## The 6-phase lifecycle — how to drive it

The `ExecutionContext` is the single object that owns phase, plan, and todos.
You drive it explicitly — the SDK does not auto-advance phases.

```go
exec := engine.Execution

// ── Phase 0: Orientation ──────────────────────────────────────────────
// Read memory before anything else.
// This is what prevents redundant questions and wrong assumptions.

memContent, _ := engine.Memory.View(ctx, autobuild.ScopeUser, "/")
projectContent, _ := engine.Memory.View(ctx, autobuild.ScopeProject, "/README.md")
engine.Prompt.Set(autobuild.LayerMemory, memContent+"\n\n"+projectContent)

// Match skills to the user's request
matched, _ := engine.Skills.Match(ctx, userMessage)
var skillContent strings.Builder
for _, m := range matched {
    skill, _ := engine.Skills.Load(ctx, m.Name)
    skillContent.WriteString(skill.Content + "\n\n")
}
engine.Prompt.Set(autobuild.LayerSkills, skillContent.String())

exec.Advance(ctx) // → Alignment

// ── Phase 1: Alignment ────────────────────────────────────────────────
// One question max. Propose a plan for complex tasks.

if isComplexTask(userMessage) {
    plan, _ := exec.Propose(ctx, autobuild.Plan{
        Title:     "Refactor auth module",
        Objective: "Decouple JWT validation from the handler layer",
        Executables: []autobuild.Executable{
            {ID: "e1", Name: "Audit current auth flow", Status: autobuild.ExecStatusPlanned},
            {ID: "e2", Name: "Extract JWT middleware", Status: autobuild.ExecStatusPlanned, Dependencies: []string{"e1"}},
            {ID: "e3", Name: "Update handler tests", Status: autobuild.ExecStatusPlanned, Dependencies: []string{"e2"}},
        },
    })
    _ = plan // present to user for approval
}

exec.Advance(ctx) // → Preparation

// ── Phase 2: Preparation ──────────────────────────────────────────────
// Checkpoint before touching anything. Set todos.

if engine.HasCheckpoints() {
    engine.Checkpoints.Create(ctx, "Before auth refactor")
}

exec.SetTodos([]autobuild.Todo{
    {ID: "t1", Content: "Audit current auth flow", Status: autobuild.TodoStatusInProgress},
    {ID: "t2", Content: "Extract JWT middleware", Status: autobuild.TodoStatusPending},
    {ID: "t3", Content: "Update handler tests", Status: autobuild.TodoStatusPending},
})

exec.Advance(ctx) // → Execution

// ── Phase 3: Execution ────────────────────────────────────────────────
// Run the agent loop. Parallel dispatch for independent tools.
// Update todos and plan executables as work completes.

result, _ := autobuild.RunAgentLoopWithEngine(ctx, engine, "code-agent",
    autobuild.AgentLoopConfig{
        MaxTurns: 50,
        OnToolResult: func(call autobuild.ToolCallEntry, result autobuild.ToolResult) autobuild.ToolResult {
            // Record interesting tool results as observations
            engine.Observations.Record(ctx, autobuild.Observation{
                Source:    call.Name,
                Content:   result.Content,
                Relevance: 0.8,
            })
            return result
        },
    },
    messages,
)

exec.MarkDone("t1")
exec.UpdateExecutable(ctx, "e1", autobuild.ExecStatusCompleted, "Audit complete")
exec.Advance(ctx) // → Verification

// ── Phase 4: Verification ─────────────────────────────────────────────
// Sanity check before closing. If it fails, retry execution.

if verificationFails {
    exec.SetPhase(ctx, autobuild.PhaseExecution) // retry — attempt counter increments
    if exec.Attempt() > 3 {
        // surface to user, don't loop forever
    }
    return
}

exec.Advance(ctx) // → Closure

// ── Phase 5: Closure ──────────────────────────────────────────────────
// Checkpoint. Update memory if something has leverage. Clear session obs.

engine.Checkpoints.Create(ctx, "After auth refactor")

if somethingWorthRemembering {
    engine.Memory.StrReplace(ctx, autobuild.ScopeProject, "/README.md",
        "## Auth", "## Auth\nJWT validation now lives in middleware/jwt.go")
}

engine.Observations.Clear(ctx) // session done
```

---

## Memory layers — how to write correctly

```go
// User said it explicitly → Explicit layer
layeredMem.WriteLayered(ctx, autobuild.ScopeUser, "/profile/work.md",
    "Works at Maxwell Clinic on EverBetter EHR",
    autobuild.MemoryLayerExplicit)

// You inferred it from conversation → Inferred layer
layeredMem.WriteLayered(ctx, autobuild.ScopeUser, "/profile/preferences.md",
    "Prefers short responses with code examples",
    autobuild.MemoryLayerInferred)

// Only relevant this session → ObservationStore, not memory
engine.Observations.Record(ctx, autobuild.Observation{
    Source:  "user_message",
    Content: "User is currently debugging a race condition in the payment flow",
    Tags:    []string{"session", "debugging"},
})

// On conflict, Explicit always wins
entries, _ := layeredMem.SearchLayered(ctx, autobuild.ScopeUser, "work location")
autobuild.SortByPriority(entries) // Explicit first
winning := entries[0]
```

---

## System prompt layers — assembly order

```
LayerCore      → who the agent is, invariant
LayerBehavior  → DefaultBehaviorPrompt (tool judgment, memory discipline, etc.)
LayerMemory    → read from MemoryProvider at conversation start
LayerSkills    → content of loaded skills (added/removed dynamically)
LayerSession   → current time, thread ID, what user is viewing
LayerMode      → active mode's system.md content
```

```go
prompt := autobuild.NewSystemPromptBuilder()
prompt.Set(autobuild.LayerCore, `You are an engineering assistant for Maxwell Clinic,
working on the EverBetter EHR system. You have deep knowledge of TypeScript,
Go, and healthcare data standards.`)

prompt.Set(autobuild.LayerBehavior, autobuild.DefaultBehaviorPrompt)

// Refresh memory layer at each conversation start
memContent := readRelevantMemory(ctx, engine.Memory)
prompt.Set(autobuild.LayerMemory, memContent)

// Skills added dynamically
prompt.Append(autobuild.LayerSkills, loadedSkill.Content)

// Session context from your platform
prompt.Set(autobuild.LayerSession, fmt.Sprintf(
    "Current time: %s | Thread: %s | User viewing: %s",
    time.Now().Format(time.RFC3339), threadID, viewingArtifact,
))

// Mode applied last in RunAgentLoopWithEngine automatically
finalPrompt := prompt.Build()
```

---

## Multi-model routing — the right way

No separate `Router` field. `RoutedLLMProvider` implements `LLMProvider`.
Duck typing handles the routing automatically in `resolveProvider`.

```go
// Wire once
engine.LLM = autobuild.NewRoutedLLMProvider("anthropic", map[string]autobuild.LLMProvider{
    "anthropic": anthropicProvider,
    "ollama":    ollamaProvider,
})

// Use in mode's system.md frontmatter:
// model: anthropic/claude-sonnet-4-20250514  → routes to anthropic
// model: ollama/llama3                        → routes to ollama
// model: claude-sonnet-4-20250514            → routes to default (anthropic)
```

---

## Context budget — prevent silent overflow

```go
budget := autobuild.DefaultContextBudget(128_000)

skillTokens  := estimateTokens(loadedSkillsContent)
memoryTokens := estimateTokens(memoryContent)
historyTokens := estimateTokens(conversationHistory)

if budget.WouldOverflow(skillTokens, memoryTokens, historyTokens) {
    // Evict oldest skills first
    evict := budget.SkillEvictionCount(len(loadedSkills), avgTokensPerSkill)
    for i := 0; i < evict; i++ {
        engine.Skills.Unload(ctx, oldestSkills[i])
    }
    // If still over, summarize memory or truncate history
}
```

---

## Parallel tool dispatch

```go
// Independent tools → run in parallel
results := dispatcher.DispatchParallel(ctx, independentCalls, sandboxID)

// Dependent tools → run sequentially
for _, call := range dependentCalls {
    result := dispatcher.Dispatch(ctx, call, sandboxID)
    // use result to build next call
}
```

The rule: if result of A does not determine parameters of B, they are independent.

---

## What this gives you vs raw Claude

| Capability | Raw Claude API | SDK + Claude Model |
|---|---|---|
| Phase discipline | Implicit in system prompt | Explicit, enforced by ExecutionContext |
| Memory persistence | Manual | MemoryProvider + LayeredMemory |
| Skill loading | Manual injection | SkillProvider with trigger matching |
| Plan + todos | Manual | ExecutionContext owns both |
| Multi-model routing | Manual | RoutedLLMProvider + duck typing |
| Observation store | None | InMemoryObservationStore |
| Context budget | None | ContextBudget with eviction hints |
| Checkpoint discipline | None | CheckpointProvider in lifecycle |
| Token observability | Usage in response | ReasoningTrace + TotalUsage |

---

## What this does NOT give you

- **RLHF-trained judgment** — the DefaultBehaviorPrompt approximates it,
  but Claude's ability to infer when to use a tool without being told
  comes from training, not configuration.

- **The real system prompt** — Anthropic's actual instructions are not
  public. DefaultBehaviorPrompt is a structural approximation.

- **Model quality** — the SDK is model-agnostic. Swap in a weaker model
  and the quality of decisions drops regardless of the scaffolding.

The SDK gives you the skeleton. The LLM is the brain. The system prompt
is the personality. All three together get you structurally close.
