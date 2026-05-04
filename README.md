# Autobuild SDK

A minimal, extensible Go SDK for building AI agents with a structured lifecycle, layered memory, and multi-provider support. Designed to replicate the same operational discipline as Claude — same 6-phase lifecycle, same memory model, same layered prompt assembly.

## Design Principles

- **Opt-in** — Every provider is optional. Wire only what you need.
- **Decoupled** — Core abstractions (Engine, Runtime) have zero coupling to specific providers.
- **Extensible** — All capabilities are defined as interfaces. Bring your own implementations.
- **Claude-compatible** — Implements Claude's 6-phase lifecycle and layered memory model.

## Installation

```bash
go get github.com/everfaz/autobuild-sdk
```

Requires Go 1.22+.

## Quick Start

```go
package main

import (
    "context"
    "fmt"

    sdk "github.com/everfaz/autobuild-sdk"
    "github.com/everfaz/autobuild-sdk/providers/llm"
)

func main() {
    // 1. Create an engine with sensible defaults (128k context window)
    engine := sdk.NewWithDefaults(128_000)

    // 2. Wire your LLM provider
    engine.LLM = llm.NewAnthropic("your-api-key", "claude-sonnet-4-20250514")

    // 3. Build a runtime
    rt := sdk.NewRuntime(engine)

    // 4. Run a conversation
    conv := sdk.NewConversation("my-session")
    result, err := rt.Run(context.Background(), conv, "Hello, what can you do?")
    if err != nil {
        panic(err)
    }
    fmt.Println(result.Response)
}
```

## Architecture

```
┌─────────────────────────────────────────────────┐
│                   Runtime                        │
│  Orchestrates lifecycle, wires providers,        │
│  manages streaming, verification, and memory     │
│                                                  │
│  ┌─────────────────────────────────────────────┐ │
│  │                 Engine                       │ │
│  │  Composition root for all providers:         │ │
│  │  LLM · Memory · Tools · Skills · Sandbox    │ │
│  │  Checkpoints · Modes · Events · Execution   │ │
│  │  Observations · Prompt · Budget             │ │
│  └─────────────────────────────────────────────┘ │
│                                                  │
│  ┌──────────┐ ┌──────────┐ ┌─────────────────┐  │
│  │ Planner  │ │ Safety   │ │ Verification    │  │
│  │          │ │ Filters  │ │ Strategy        │  │
│  └──────────┘ └──────────┘ └─────────────────┘  │
│  ┌──────────┐ ┌──────────┐ ┌─────────────────┐  │
│  │Compactor │ │ Memory   │ │ Output Filter   │  │
│  │          │ │ Writer   │ │                 │  │
│  └──────────┘ └──────────┘ └─────────────────┘  │
└─────────────────────────────────────────────────┘
```

### Engine

The central composition point that wires all providers together:

```go
type Engine struct {
    LLM          LLMProvider
    Memory       MemoryProvider
    Tools        *ToolRegistry
    Skills       SkillProvider
    Sandbox      SandboxDriver
    Threads      ThreadProvider
    Checkpoints  CheckpointProvider
    Modes        ModeProvider
    Events       EventBus
    Execution    ExecutionContext
    Observations ObservationStore
    Prompt       *SystemPromptBuilder
    Budget       *ContextBudget
}
```

Create with functional options or sensible defaults:

```go
// Minimal with defaults
engine := sdk.NewWithDefaults(128_000)

// Custom composition
engine := sdk.New(
    sdk.WithLLM(myLLM),
    sdk.WithToolRegistry(myTools),
    sdk.WithMemory(myMemory),
    sdk.WithSkills(mySkills),
    sdk.WithCheckpoints(myCheckpoints),
    sdk.WithModes(myModes),
    sdk.WithEventBus(myBus),
)
```

### Runtime

Connects every Engine provider into a working agent. Fluent builder API:

```go
rt := sdk.NewRuntime(engine).
    WithPlanner(sdk.NewLLMPlanner(engine.LLM, "claude-sonnet-4-20250514")).
    WithSafety(sdk.NewSafetyChain(
        sdk.DefaultDangerousCommandFilter(),
        sdk.DefaultSecretLeakFilter(),
    )).
    WithOutputFilter(sdk.NewOutputFilterChain(
        sdk.DefaultSecretRedactionFilter(),
    )).
    WithVerification(sdk.CompletionVerification{}).
    WithCompactor(&sdk.BulletCompactor{MaxChars: 2000}).
    WithMemoryWriter(&sdk.InferredMemoryWriter{
        Provider: engine.LLM,
        Model:    "claude-sonnet-4-20250514",
    }).
    WithSessionContext(sdk.LocalTimeSessionContext()).
    WithTokenizer(myTokenizer).
    WithConversationStore(myStore).
    WithMode("balanced").
    WithAutoApprovePlan(true).
    WithMaxVerifyRetry(2).
    WithWellbeing(sdk.DefaultWellbeingDetector())
```

## The 6-Phase Lifecycle

Every agent turn follows a structured lifecycle:

| Phase            | Purpose                                                                                            |
| ---------------- | -------------------------------------------------------------------------------------------------- |
| **Orientation**  | Read memory, match skills, surface observations. Cold turns do full read; warm turns refresh only. |
| **Alignment**    | Decide if the task warrants a plan. Planner proposes, user/auto approves.                          |
| **Preparation**  | Create checkpoint, verify context budget, apply eviction if needed.                                |
| **Execution**    | Main LLM ↔ tool loop with safety checks and streaming support.                                     |
| **Verification** | Validate output quality — tests, criteria, or custom strategies.                                   |
| **Closure**      | Write inferred memories, persist conversation state.                                               |

The lifecycle is managed by `ExecutionContext` and advances automatically during `Runtime.Run()`.

## System Prompt Builder

Layered prompt assembly with deterministic priority:

```go
builder := sdk.NewSystemPromptBuilder()
builder.Set(sdk.LayerCore, "You are a helpful assistant.")
builder.Set(sdk.LayerBehavior, sdk.DefaultBehaviorPrompt)
builder.Append(sdk.LayerMemory, memoryContent)
builder.Append(sdk.LayerSkills, skillContent)
builder.Set(sdk.LayerSession, sessionContext)
builder.Set(sdk.LayerMode, modeOverlay)

prompt := builder.Build() // Assembles all layers in order
```

**Layer Priority (injection order):**

1. `LayerCore` — Invariant identity (set once, never changes)
2. `LayerBehavior` — Operating principles and tool selection logic
3. `LayerMemory` — Persistent user/project state
4. `LayerSkills` — Currently loaded skill content
5. `LayerSession` — Ephemeral context (time, observations, state)
6. `LayerMode` — Active mode's overlay (most specific, applied last)

## Provider Interfaces

### LLMProvider

```go
type LLMProvider interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

type StreamingLLMProvider interface {
    LLMProvider
    ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}
```

**Built-in implementations:**

- `providers/llm/anthropic.go` — Anthropic API (Claude models) with tool use support
- `providers/llm/ollama.go` — Ollama for local models

**Multi-model routing:**

```go
router := sdk.NewRoutedLLMProvider("anthropic", map[string]sdk.LLMProvider{
    "anthropic": anthropicProvider,
    "ollama":    ollamaProvider,
})
// Route with "provider/model" format: "anthropic/claude-sonnet-4-20250514"
```

### MemoryProvider

Persistent memory with scoped access:

```go
type MemoryProvider interface {
    View(ctx context.Context, scope Scope, path string) (string, error)
    Create(ctx context.Context, scope Scope, path, content string) error
    StrReplace(ctx context.Context, scope Scope, path, oldStr, newStr string) error
    Delete(ctx context.Context, scope Scope, path string) error
    Rename(ctx context.Context, scope Scope, oldPath, newPath string) error
    List(ctx context.Context, scope Scope, path string) ([]string, error)
    Search(ctx context.Context, scope Scope, query string) ([]MemoryEntry, error)
}
```

**Scopes:** `"user"` (cross-project), `"project"` (per-project), `"session"` (conversation-only)

**Memory Layers (conflict resolution):** Explicit > Inferred > Session

**Built-in:** `providers/memory/filesystem.go` — Markdown files on disk

### SkillProvider

On-demand knowledge loading with trigger-based matching:

```go
type SkillProvider interface {
    Load(ctx context.Context, name string) (*Skill, error)
    Unload(ctx context.Context, name string) error
    Match(ctx context.Context, text string) ([]SkillMatch, error)
    List(ctx context.Context) ([]string, error)
    Get(ctx context.Context, name string) (*Skill, error)
    Loaded(ctx context.Context) []string
}
```

Skills are defined as `SKILL.md` files with YAML frontmatter:

```markdown
---
name: writing
version: 1.0.0
description: Technical writing guidelines
triggers:
  - write
  - document
  - readme
grantedTools:
  - file_write
---

# Writing Skill

Your skill content here...
```

Load from directories:

```go
skills, _ := sdk.LoadSkillsDir("./skills/")
```

### ToolRegistry & Dispatch

```go
registry := sdk.NewToolRegistry()
registry.Register(&sdk.Tool{
    Name:        "read_file",
    Description: "Read the contents of a file",
    Category:    "filesystem",
    Parameters:  sdk.ToolFuncParams{...},
    Execute: func(ctx context.Context, sandboxID string, args map[string]any) (string, error) {
        // implementation
        return content, nil
    },
})

// Dispatch supports parallel execution for independent calls
dispatcher := sdk.NewToolDispatcher(registry, sandbox)
results := dispatcher.DispatchParallel(ctx, toolCalls, sandboxID)
```

### ModeProvider

Execution modes define model settings, allowed tools, and prompt overlays:

```go
type Mode struct {
    ID, Name, Description string
    ModelSettings *ModelSettings
    SystemPrompt  string
    AllowedTools  []string
    DeniedTools   []string
    ToolsMode     ToolsMode  // "allow" | "deny" | "required"
}
```

Modes are defined as markdown files with YAML frontmatter, loaded from directories:

```go
modes, _ := sdk.LoadModeProviderFromDirs("./modes/")
```

### CheckpointProvider

Safety snapshots before mutations:

```go
type CheckpointProvider interface {
    Create(ctx context.Context, description string) (*Checkpoint, error)
    Restore(ctx context.Context, checkpointID string) error
    List(ctx context.Context) ([]*Checkpoint, error)
}
```

### SandboxDriver

Command execution and file I/O:

```go
type SandboxDriver interface {
    Init(ctx context.Context, config SandboxConfig) (string, error)
    Exec(ctx context.Context, sandboxID, command string, timeout time.Duration) ExecResult
    WriteFile(ctx context.Context, sandboxID, path, content string) error
    ReadFile(ctx context.Context, sandboxID, path string) (string, error)
    Cleanup(ctx context.Context, sandboxID string) error
}
```

**Built-in:** `providers/sandbox/local.go` — Local execution (dev-only)

### ConversationStore

Persist conversations across process restarts:

```go
type ConversationStore interface {
    Save(ctx context.Context, conv *Conversation) error
    Load(ctx context.Context, id string) (*Conversation, error)
    Delete(ctx context.Context, id string) error
    List(ctx context.Context) ([]string, error)
}
```

**Built-in:**

- `NewInMemoryConversationStore()` — Thread-safe in-memory map
- `providers/store/filesystem.go` — Markdown files on disk

## Streaming

Full streaming support for real-time responses:

```go
events, err := rt.RunStream(ctx, conv, "Build a REST API")
if err != nil {
    panic(err)
}

for event := range events {
    switch event.Type {
    case sdk.StreamEventDelta:
        fmt.Print(event.Delta) // incremental text
    case sdk.StreamEventToolCall:
        fmt.Printf("Calling tool: %s\n", event.ToolCall.Name)
    case sdk.StreamEventToolResult:
        fmt.Printf("Tool result: %s\n", event.ToolResult.Content)
    case sdk.StreamEventDone:
        fmt.Println("\nDone:", event.Final.StopReason)
    case sdk.StreamEventError:
        fmt.Println("Error:", event.Error)
    }
}
```

**Utilities:**

- `CollectStream()` — Collect full response from stream channel
- `FanOutStream()` — Duplicate stream to multiple consumers
- **Automatic fallback** — If LLM doesn't implement `StreamingLLMProvider`, `RunStream` falls back to sentence-chunked emission

## Planning System

Two built-in planners:

### HeuristicPlanner

Cheap, deterministic. Detects complexity signals in user messages and produces a generic 3-step plan (Analyze → Execute → Verify):

```go
planner := sdk.DefaultHeuristicPlanner()
```

### LLMPlanner

Asks the LLM to decompose the user's request into a concrete DAG of executable steps:

```go
planner := sdk.NewLLMPlanner(llmProvider, "claude-sonnet-4-20250514")
```

Plans are represented as a DAG with dependency tracking:

```go
type Plan struct {
    ID, Title, Objective string
    Executables []Executable  // steps with dependencies
    Approved    bool
}

// DAG helpers
plan.NextReady()           // steps whose dependencies are met
plan.IsComplete()          // all steps completed
plan.IsBlocked(execID)     // check if step is blocked
```

## Subagent System

Fork isolated agents for parallel task execution:

```go
agents := []sdk.Subagent{
    {ID: "tests", Task: "Write unit tests", Engine: engine, MaxTurns: 10},
    {ID: "docs", Task: "Write documentation", Engine: engine, MaxTurns: 10},
}

results := sdk.RunSubagentsInParallel(ctx, agents)
for _, r := range results {
    fmt.Printf("[%s] %s (turns: %d)\n", r.ID, r.Output, r.Turns)
}
```

Subagents are fully isolated: own conversation, own loaded skills, own observation store. Memory access is read-only.

## Safety & Verification

### Safety Filters (Pre-Dispatch)

Inspect tool calls before execution:

```go
safety := sdk.NewSafetyChain(
    sdk.DefaultDangerousCommandFilter(), // blocks rm -rf, dd, format, etc.
    sdk.DefaultSecretLeakFilter(),       // detects API keys, tokens, passwords
)
rt.WithSafety(safety)
```

### Output Filters (Post-Processing)

Filter the final response:

```go
filter := sdk.NewOutputFilterChain(
    sdk.DefaultSecretRedactionFilter(), // redact secrets from responses
)
rt.WithOutputFilter(filter)
```

### Verification Strategies

Validate output quality before closure:

```go
// Always pass (no-op)
sdk.NoOpVerification{}

// Check finish reason
sdk.CompletionVerification{}

// LLM self-check against criteria
sdk.CriteriaVerification{
    Provider: llmProvider,
    Model:    "claude-sonnet-4-20250514",
    Criteria: []string{"Response is accurate", "Code compiles"},
}
```

### Wellbeing Detection

Detect concerning patterns in user messages:

```go
rt.WithWellbeing(sdk.DefaultWellbeingDetector())
// Returns WellbeingSignal with Category, Severity, and Message
```

## Context Budget & Compaction

Automatic token management prevents context overflow:

```go
budget := sdk.DefaultContextBudget(128_000)
// Allocations: Skills 10%, Memory 15%, History 60%, Reserve 15%

// Skill eviction policies
sdk.LRUEvictionPolicy{}                        // least-recently-used first
sdk.TTLEvictionPolicy{MaxIdle: 30 * time.Minute} // unused skills evicted
```

When history exceeds budget, the compactor summarizes old messages:

```go
// Bullet-point summary (no LLM needed)
rt.WithCompactor(&sdk.BulletCompactor{MaxChars: 2000})

// LLM-powered summarization
rt.WithCompactor(&sdk.LLMCompactor{
    Provider: llmProvider,
    Model:    "claude-sonnet-4-20250514",
})
```

## Memory Writer

Automatically extract and persist facts from conversations during the closure phase:

```go
rt.WithMemoryWriter(&sdk.InferredMemoryWriter{
    Provider:      llmProvider,
    Model:         "claude-sonnet-4-20250514",
    MaxFacts:      3,
    MinConfidence: 0.7,
})
```

Extracted facts include confidence scores, source layer classification (Explicit/Inferred/Session), and target scope (user/project).

## Event Bus

Publish/subscribe system for inter-component communication:

```go
bus := sdk.NewEventBus()
sub := bus.Subscribe(sdk.EventPlanProposed, func(e sdk.Event) {
    fmt.Printf("Plan proposed: %s\n", e.Payload["title"])
})
defer sub.Cancel()
```

**Built-in event types:** `EventAgentLoopStarted`, `EventAgentTurnCompleted`, `EventPhaseAdvanced`, `EventPlanProposed`, `EventPlanApproved`, `EventSafetyBlocked`, `EventVerificationPassed`, `EventVerificationFailed`, `EventMemoryWritten`, `EventSubagentStarted`, `EventSubagentCompleted`, and more.

## Tracing

Built-in span-based tracing:

```go
tracer := sdk.NewTracer()
ctx = sdk.WithTracer(ctx, tracer)

ctx, end := sdk.StartSpan(ctx, "my-operation", map[string]any{"key": "value"})
defer end(nil) // pass error if failed
```

## Eval System

Built-in assertion framework for regression testing:

```go
suite := &sdk.EvalSuite{
    Cases: []sdk.EvalCase{
        {
            Name:  "greeting",
            Input: "Hello",
            Assertions: []sdk.Assertion{
                {Type: sdk.AssertContains, Expected: "hello"},
                {Type: sdk.AssertNotContains, Expected: "error"},
            },
        },
    },
}

results := suite.Run(ctx, runner)
summary := sdk.Summarize(results)
fmt.Printf("Passed: %d/%d\n", summary.Passed, summary.Total)
```

## Embeddings & Semantic Search

Embedding-powered skill matching and observation retrieval:

```go
embedder := voyage.NewVoyageEmbedder(apiKey, model)

// Semantic skill matching
matcher := sdk.NewSemanticSkillMatcher(skillProvider, embedder)

// Semantic observation store
store := sdk.NewSemanticObservationStore(embedder)
```

## Replay & Snapshots

Capture and compare agent behavior:

```go
snap, _ := sdk.CaptureSnapshot(ctx, runtime, "test-1", "user input")
sdk.SaveSnapshot(snap, "./snapshots/test-1.json")

// Later: compare against new run
loaded, _ := sdk.LoadSnapshot("./snapshots/test-1.json")
diff := sdk.CompareSnapshot(loaded, newResult, "user input")
```

## Project Structure

```
sdk/
├── agent_loop.go        # Core LLM ↔ tool cycle (zero Engine coupling)
├── alignment.go         # Output filters (secret redaction, length, disclaimers)
├── checkpoint.go        # CheckpointProvider interface
├── closure.go           # Closure phase logic
├── compaction.go        # History summarization (Bullet, LLM)
├── context_budget.go    # Token budget enforcement and skill eviction
├── conversation.go      # Conversation struct and message management
├── conversation_store.go# ConversationStore interface + in-memory impl
├── dispatch.go          # Tool dispatcher (serial + parallel)
├── embeddings.go        # Embedder interface and semantic search
├── engine.go            # Engine struct and functional options
├── eval.go              # Assertion framework for regression testing
├── event.go             # EventBus pub/sub system
├── execution_context.go # 6-phase lifecycle management
├── llm.go               # LLMProvider + ChatRequest/ChatResponse types
├── llm_router.go        # Multi-model routing (provider/model format)
├── loader.go            # Skill and mode file parsers
├── memory.go            # MemoryProvider interface and scopes
├── memory_layer.go      # LayeredMemoryProvider and conflict resolution
├── message.go           # ChatMessage, Role types
├── mode.go              # ModeProvider, Mode struct, loaders
├── observation.go       # ObservationStore and wellbeing detection
├── options.go           # Runtime builder methods (With*)
├── plan.go              # Planner interface, LLMPlanner, HeuristicPlanner, DAG
├── reasoning.go         # ReasoningStep and execution trace
├── replay.go            # Snapshot capture and comparison
├── runtime.go           # Runtime orchestrator (Run, RunStream)
├── safety.go            # Safety filters (dangerous commands, secret leaks)
├── sandbox.go           # SandboxDriver interface
├── sdk.go               # Package overview
├── session_context.go   # SessionContextProvider
├── skill.go             # SkillProvider and Skill types
├── streaming.go         # StreamEvent types and utilities
├── subagent.go          # Parallel forked execution
├── system_prompt.go     # Layered prompt builder
├── thread.go            # ThreadProvider interface
├── tool.go              # ToolRegistry, Tool, categories
├── tracing.go           # Span-based tracing
├── verification.go      # Verification strategies
└── providers/
    ├── llm/
    │   ├── anthropic.go     # Anthropic Claude provider
    │   └── ollama.go        # Ollama local models
    ├── memory/
    │   └── filesystem.go    # File-based memory
    ├── store/
    │   └── filesystem.go    # File-based conversation store
    ├── sandbox/
    │   └── local.go         # Local sandbox (dev-only)
    ├── embedders/
    │   └── voyage.go        # Voyage embeddings
    └── tokenizers/
        └── byte.go          # Byte-level tokenizer
```

## Example

See [`example/backend-chat/`](example/backend-chat/) for a full working backend that demonstrates:

- Engine/Runtime wiring with all SDK capabilities
- Mode-aware configuration (analyst, balanced, code-agent, deep-work)
- SQLite-backed conversation persistence
- SSE streaming endpoint
- Plan proposal and execution with subagents
- Built-in eval suite

See [`example/chat-app/`](example/chat-app/) for a React frontend that consumes the SSE stream.

## License

Proprietary — © Everfaz
