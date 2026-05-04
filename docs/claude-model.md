# Harness SDK — Claude Model Implementation Guide

How to wire the SDK so an agent operates structurally like Claude:
multi-turn conversations, layered prompt assembly, memory discipline,
safety filters, verification, subagents, tracing. The SDK is the
skeleton. The LLM you plug in is the brain.

---

## TL;DR — Multi-turn fast path

```go
engine := autobuild.NewWithDefaults(128_000)
engine.LLM = myLLM
engine.Memory = myMemory
engine.Tools = myTools
engine.Skills = mySkills

runtime := autobuild.NewRuntime(engine).
    WithMode("balanced").
    WithSafety(autobuild.NewSafetyChain(
        autobuild.DefaultDangerousCommandFilter(),
        autobuild.DefaultSecretLeakFilter(),
    )).
    WithVerification(autobuild.CompletionVerification{MinLength: 10})

conv := autobuild.NewConversation("conv-123")

// Turn 1 — cold start: full orientation
result1, _ := runtime.Run(ctx, conv, "Help me refactor auth")

// Turn 2 — warm: reuses loaded skills + memory
result2, _ := runtime.Run(ctx, conv, "Now apply the same pattern to billing")
```

---

## The 6-phase lifecycle (cold start)

```
Run(conv, message)
  │
  ├── Wellbeing check (intercept if high-severity distress)
  │
  ├── Orientation [cold only]
  │     ├── Read memory → LayerMemory
  │     ├── Match + load skills → LayerSkills
  │     └── Surface observations → LayerSession
  │
  ├── Warm refresh [warm turns only]
  │     ├── Re-surface observations
  │     └── Match new skills not yet loaded
  │
  ├── Preparation
  │     ├── Auto-checkpoint
  │     └── Budget enforcement (evict skills, truncate history)
  │
  ├── Execution → Verification (loop with retry)
  │     ├── Safety filter on tool calls
  │     ├── OnToolResult → ObservationFilter → record
  │     └── Verify output — retry if Retry=true
  │
  └── Closure
        ├── Explicit memory triggers ("remember that...")
        ├── Inferred memory writes (LLM extracts persistent facts)
        └── Persist conversation to ConversationStore
```

---

## Multi-turn conversations

```go
conv := autobuild.NewConversation("user-123-thread-456")

// Restore from store if exists
if existing, _ := store.Load(ctx, conv.ID); existing != nil {
    conv = existing
}

// Multi-turn — first cold, rest warm
for _, message := range userInputs {
    result, err := runtime.Run(ctx, conv, message)
    if err != nil { break }
    fmt.Println(result.Response)
}

store.Save(ctx, conv)
```

First call triggers full orientation. Subsequent calls skip memory re-read,
only refresh observations and check for new skill matches.

---

## Memory layers + scopes

```
Layers (priority):              Scopes (boundary):
  Explicit  → user said it       User    → cross-project
  Inferred  → LLM derived it     Project → per-project
  Session   → this conv only     Session → → ObservationStore
```

Conflict resolution: Explicit > Inferred > Session. Inferred only writes
when LLM confidence ≥ threshold.

```go
runtime.WithMemoryWriter(&autobuild.InferredMemoryWriter{
    Provider:      myLLM,
    Model:         "claude-haiku-4-5",
    MaxFacts:      3,
    MinConfidence: 0.7,
})
```

---

## Safety filters

```go
runtime.WithSafety(autobuild.NewSafetyChain(
    autobuild.DefaultDangerousCommandFilter(),
    autobuild.DefaultSecretLeakFilter(),
    // Custom
    autobuild.SafetyFilterFunc(func(ctx context.Context, call autobuild.ToolCallEntry) autobuild.SafetyVerdict {
        if call.Name == "send_email" && containsForbidden(call.Arguments) {
            return autobuild.SafetyVerdict{
                Decision: autobuild.SafetyBlock,
                Reason:   "recipient not in allowlist",
            }
        }
        return autobuild.SafetyVerdict{Decision: autobuild.SafetyAllow}
    }),
))
```

Blocked calls return the reason as the tool result so the LLM can self-correct.

---

## Verification

```go
// Lightweight: completion + min length
runtime.WithVerification(autobuild.CompletionVerification{MinLength: 50})

// Strong: LLM self-check against criteria
runtime.WithVerification(autobuild.CriteriaVerification{
    Provider: myLLM,
    Criteria: []string{
        "Response includes a code example",
        "Response addresses the specific question",
        "No placeholder text like TODO",
    },
})

// Custom: run tests
runtime.WithVerification(autobuild.VerificationStrategyFunc(
    func(ctx context.Context, r *autobuild.AgentLoopResult, c *autobuild.Conversation) autobuild.Verdict {
        if !runTests() {
            return autobuild.Verdict{Pass: false, Reason: "tests failed", Retry: true}
        }
        return autobuild.Verdict{Pass: true}
    },
))

runtime.WithMaxVerifyRetry(2)
```

---

## Alignment + Planner

The Alignment phase decides whether the user's request warrants a structured
Plan. If yes, the Planner proposes one and Runtime registers it on
`engine.Execution.ActivePlan()`. Hooks registered with
`engine.Execution.RegisterHook(PhaseExecution, ...)` can inspect the plan
before Execution begins.

```go
// Heuristic: cheap, no LLM call. Detects multi-step tasks by keyword count.
runtime.WithPlanner(autobuild.DefaultHeuristicPlanner())

// LLM-driven: more accurate, costs one extra call per turn.
runtime.WithPlanner(&autobuild.LLMPlanner{
    Provider:       myLLM,
    Model:          "claude-haiku-4-5",
    MaxExecutables: 5,
})

// Auto-approve plans (skip explicit Approve step)
runtime.WithAutoApprovePlan(true)
```

Inspect the proposed plan in the result:

```go
result, _ := runtime.Run(ctx, conv, "Refactor auth and update billing tests")
if result.PlanProposed != nil {
    for _, exec := range result.PlanProposed.Executables {
        fmt.Printf("- %s (deps: %v)\n", exec.Name, exec.Dependencies)
    }
}
```

---

## Subagents

```go
subs := []autobuild.Subagent{
    {ID: "research-1", Task: "Find papers on agent memory", Engine: subEngine},
    {ID: "research-2", Task: "Survey existing SDKs", Engine: subEngine},
}

results := autobuild.RunSubagentsInParallel(ctx, subs)
for _, r := range results {
    if r.Error != nil { continue }
    fmt.Printf("[%s] %s\n", r.ID, r.Output)
}
```

Isolated context, shared LLM + Memory (read-only), structured results.

---

## Dynamic tool discovery

```go
registry.Register(...)
registry.Hide("rare_tool") // not in default ToolDefs

// Add tool_search as a meta-tool
registry.Register(&autobuild.Tool{
    Name:        "tool_search",
    Description: "Search for available tools",
    Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
        query := args["query"].(string)
        matches := registry.Search(query)
        for _, m := range matches[:3] {
            registry.Reveal(m.Tool.Name)
        }
        return formatMatches(matches), nil
    },
})
```

---

## Budget enforcement (real, not warnings)

```go
// On overflow:
// 1. Evict skills (LRU)
// 2. Truncate oldest non-system messages
// 3. Report remaining overflow as StillOverflow=true

result, _ := runtime.Run(ctx, conv, message)
if result.Enforcement != nil {
    log.Printf("Evicted: %v, dropped: %d",
        result.Enforcement.EvictedSkills,
        result.Enforcement.HistoryDropped)
}
```

---

## Tracing

```go
tracer := autobuild.NewTracer()
ctx = autobuild.WithTracer(ctx, tracer)

runtime.Run(ctx, conv, message)

for _, span := range tracer.Spans() {
    log.Printf("[%s] %s %v %s",
        span.SpanID, span.Name, span.Duration, span.Status)
}
```

Spans propagate parent IDs. Trace IDs span subagents.

---

## Eval suite

```go
suite := &autobuild.EvalSuite{
    Cases: []autobuild.EvalCase{
        {
            Name:  "memory_recall",
            Input: "What's my name?",
            Assertions: []autobuild.Assertion{
                {Type: autobuild.AssertContains, Value: "Juss"},
                {Type: autobuild.AssertMaxTurns, Value: "1"},
            },
        },
        {
            Name:  "tool_use",
            Input: "What's the weather in Quito?",
            Assertions: []autobuild.Assertion{
                {Type: autobuild.AssertToolCalled, Value: "weather"},
            },
        },
    },
}

results, _ := suite.Run(ctx, runtime)
summary := autobuild.Summarize(results)
fmt.Printf("Pass: %.0f%% (%d/%d)\n",
    summary.PassRate*100, summary.Passed, summary.Total)
```

---

## Multilingual triggers

`DefaultMemoryTriggerDetector` and `DefaultWellbeingDetector` work in
English and Spanish out of the box:

```
"recuerda que vivo en Quito"  → MemoryLayerExplicit
"me mudé a Cuenca"            → MemoryLayerExplicit (state change)
"olvida lo de mi trabajo"     → forget trigger
"quiero morir"                → WellbeingSeverityHigh, intercepted
```

---

## Retry policy

`callWithRetry` classifies errors and retries with exponential backoff:

```
Permanent (no retry): 401, 403, 400, 404, content_filter, context_length
Transient (retry):    429, 5xx, timeout, EOF, connection errors
Backoff:              1s → 2s → 4s → 8s → 16s → 30s (cap)
```

---

## Cancellation

`ctx` propagates through every phase. Cancel mid-flight:

```go
ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
defer cancel()
result, err := runtime.Run(ctx, conv, message)
// err == context.DeadlineExceeded if timeout hit
```

---

## Configuration knobs

```go
runtime.
    WithMode("code-agent").
    WithSkillThreshold(0.5).
    WithMaxSkills(5).
    WithObservationFilter(myFilter).
    WithMemoryTrigger(myTrigger).
    WithWellbeing(myWellbeingDetector).
    WithSafety(myChain).
    WithVerification(myStrategy).
    WithMemoryWriter(&autobuild.InferredMemoryWriter{...}).
    WithTokenizer(realTokenizer). // replace HeuristicTokenizer
    WithConversationStore(myStore).
    WithMaxVerifyRetry(3).
    WithSessionContext(myContextProvider).
    WithOutputFilter(myOutputFilter)
```

---

## Session context — time, location, user identity

Every turn, the agent needs to know who the user is, where they are, what
time it is. Without this, search queries use stale dates and recommendations
ignore geography.

```go
runtime.WithSessionContext(autobuild.SessionContextProviderFunc(
    func(ctx context.Context, conv *autobuild.Conversation) (*autobuild.SessionContext, error) {
        user := lookupUser(conv.ThreadID)
        return &autobuild.SessionContext{
            Now:         time.Now(),
            Timezone:    user.Timezone,
            Locale:      "es-EC",
            UserName:    user.Name,
            UserCity:    user.City,
            UserCountry: user.Country,
            Surface:     "chat",
        }, nil
    },
))

// Or for single-user apps:
runtime.WithSessionContext(autobuild.StaticSessionContext(autobuild.SessionContext{
    UserName: "Juss", UserCity: "La Concordia", UserCountry: "Ecuador",
    Timezone: "America/Guayaquil", Locale: "es-EC",
}))

// Bare minimum (just time):
runtime.WithSessionContext(autobuild.LocalTimeSessionContext())
```

Rendered into LayerSession of the system prompt every turn.

---

## Output filter — protecting the user from the LLM

Symmetric counterpart of `WithSafety` (which inspects tool calls).
`WithOutputFilter` inspects the LLM's final response before returning.

```go
runtime.WithOutputFilter(autobuild.NewOutputFilterChain(
    autobuild.DefaultSecretRedactionFilter(),       // redact leaked tokens
    &autobuild.MaxLengthFilter{MaxChars: 10_000},   // hard size cap
    &autobuild.DisclaimerFilter{
        Triggers:   []string{"diagnosis", "prescription", "lawsuit"},
        Disclaimer: "*Not professional advice. Consult a qualified expert.*",
    },
))
```

Block: response replaced, `StopReason="output_blocked"`. Transform: text replaced.

---

## Streaming

For chat UIs that need token-by-token output:

```go
events, err := runtime.RunStream(ctx, conv, "Explain monads")

for ev := range events {
    switch ev.Type {
    case autobuild.StreamEventDelta:
        fmt.Print(ev.Delta)
    case autobuild.StreamEventDone:
        fmt.Println()
    case autobuild.StreamEventError:
        return ev.Error
    }
}
```

Falls back to sentence-chunked emission if the LLM provider doesn't
implement `StreamingLLMProvider` — same UX, no provider lock-in.

---

## Semantic memory + skills (embeddings)

Keyword matching undermatches when users phrase things differently than
the trigger list. Wrap with embeddings:

```go
embedder := myEmbedder // implements autobuild.Embedder

// Semantic skill matching alongside keyword match
engine.Skills = autobuild.NewSemanticSkillMatcher(rawSkillProvider, embedder)

// Semantic observation retrieval
engine.Observations = autobuild.NewSemanticObservationStore(embedder)
```

Both wrappers fall back to keyword matching if the embedder errors —
safe to deploy without breaking working systems.

---

## Replay + snapshot testing

Detect drift between SDK or model versions:

```go
// Capture at known-good state
snap, _ := autobuild.CaptureSnapshot(ctx, runtime, "auth-refactor", "Help me refactor auth")
autobuild.SaveSnapshot(snap, "testdata/snapshots/auth-refactor.json")

// Later, compare
saved, _ := autobuild.LoadSnapshot("testdata/snapshots/auth-refactor.json")
conv := autobuild.NewConversation("test")
result, _ := runtime.Run(ctx, conv, saved.Input)
diff := autobuild.CompareSnapshot(saved, result, saved.Input)
if !diff.ResponseNormalized {
    fmt.Printf("Drift: %d chars, %d turns\n", diff.LengthDelta, diff.TurnsDelta)
}

// Full conversation replay
replay := &autobuild.Replay{
    Runtime:     runtime,
    CompareMode: autobuild.ReplayCompareNormalized,
}
result, _ := replay.Run(ctx, originalConversation)
fmt.Printf("Drift: %d/%d turns\n", result.TotalDrift, len(result.Turns))
```

---

## What still does NOT exist

- **RLHF-trained judgment** — `DefaultBehaviorPrompt` approximates it.
- **Anthropic's actual system prompt** — not public.
- **Real tokenizer bundled** — `HeuristicTokenizer` is chars/4. Bring your own
  via `WithTokenizer` (tiktoken, claude-tokenizer).
- **Embedder bundled** — `Embedder` interface exists; bring your own
  (Voyage, OpenAI, local).

The SDK is the skeleton. The LLM is the brain. The system prompt is the
personality. Together they get structurally close to Claude — but the model
quality determines the ceiling.
