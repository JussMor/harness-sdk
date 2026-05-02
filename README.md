# autobuild-sdk

A minimal, extensible Go SDK that codifies the **Obvious/Autobuild** orchestration model as composable interfaces. Provider-agnostic, zero external dependencies, pure stdlib.

## Install

```bash
go get github.com/everfaz/autobuild-sdk
```

## Quick Start

```go
package main

import (
    ab "github.com/everfaz/autobuild-sdk"
)

func main() {
    // Wire only what you need — everything is optional.
    engine := ab.New(
        ab.WithMemory(myMemoryImpl),
        ab.WithSandbox(myDockerSandbox),
        ab.WithToolRegistry(buildTools()),
        ab.WithEventBus(ab.NewEventBus()),
    )

    // Check what's available at runtime.
    if engine.HasMemory() {
        // ...
    }
}
```

## Architecture

The SDK defines **interfaces** for every capability. The `Engine` struct composes them but **never implements them** — your code provides the implementations.

```
┌─────────────────────────────────────────────────┐
│                    Engine                        │
│                                                  │
│  ┌──────────┐ ┌──────────┐ ┌───────────────┐   │
│  │ Memory   │ │ Sandbox  │ │ ToolRegistry  │   │
│  └──────────┘ └──────────┘ └───────────────┘   │
│  ┌──────────┐ ┌──────────┐ ┌───────────────┐   │
│  │ Skills   │ │ Threads  │ │ Checkpoints   │   │
│  └──────────┘ └──────────┘ └───────────────┘   │
│  ┌──────────┐ ┌──────────┐ ┌───────────────┐   │
│  │ Plans    │ │ Tasks    │ │ Modes         │   │
│  └──────────┘ └──────────┘ └───────────────┘   │
│  ┌──────────┐ ┌──────────┐                      │
│  │ Workflow │ │ EventBus │                      │
│  └──────────┘ └──────────┘                      │
└─────────────────────────────────────────────────┘
```

## Core Abstractions

### MemoryProvider

Persistent storage with two scopes: **user** (cross-project) and **project** (per-project).

```go
type MemoryProvider interface {
    View(ctx, scope, path) (string, error)
    Create(ctx, scope, path, content) error
    StrReplace(ctx, scope, path, oldStr, newStr) error
    Insert(ctx, scope, path, line, text) error
    Delete(ctx, scope, path) error
    Rename(ctx, scope, oldPath, newPath) error
    List(ctx, scope, path) ([]string, error)
    Search(ctx, scope, query) ([]MemoryEntry, error)
}
```

**Implementation example** — filesystem-backed:

```go
type FSMemory struct {
    UserDir    string
    ProjectDir string
}

func (m *FSMemory) View(ctx context.Context, scope ab.Scope, path string) (string, error) {
    dir := m.UserDir
    if scope == ab.ScopeProject {
        dir = m.ProjectDir
    }
    data, err := os.ReadFile(filepath.Join(dir, path))
    return string(data), err
}
// ... implement remaining methods
```

### SandboxDriver

Command execution and file I/O inside an isolated environment.

```go
type SandboxDriver interface {
    Create(ctx, cfg) (id string, err error)
    Exec(ctx, id, command) (ExecResult, error)
    WriteFile(ctx, id, path, content) error
    ReadFile(ctx, id, path) (string, error)
    Destroy(ctx, id) error
    Status(ctx, id) (SandboxStatus, error)
    IP(ctx, id) (string, error)
}
```

### ToolRegistry

Thread-safe, categorized collection of tools with JSON Schema parameters:

```go
reg := ab.NewToolRegistry()

reg.Register(&ab.Tool{
    Name:        "run-sql",
    Description: "Execute SQL against sheets via DuckDB",
    Category:    ab.ToolCategoryData,
    Parameters: ab.ToolFuncParams{
        Type: "object",
        Properties: map[string]ab.ToolParam{
            "sql": {Type: "string", Description: "The SQL query to execute"},
        },
        Required: []string{"sql"},
    },
    Execute: func(ctx context.Context, sandboxID string, args map[string]any) (string, error) {
        sql := args["sql"].(string)
        // ... your DuckDB logic
        return result, nil
    },
})

// Retrieve
tool := reg.Get("run-sql")

// List by category
dataTools := reg.ByCategory(ab.ToolCategoryData)

// Export as LLM wire format
defs := reg.ToolDefs()
```

### SkillProvider

On-demand knowledge loading with trigger matching:

```go
skill := &ab.Skill{
    Name:     "writing",
    Domain:   "Documents and writing",
    Triggers: []string{"writing", "document", "rewrite", "clarity"},
    Content:  "Use BLUF-first structure...",
}

// Trigger matching
if skill.MatchesTrigger("create a report document") {
    // load the skill
}
```

### TaskProvider

Reusable workflows with steps, gates, conditions, and triggers:

```go
task := ab.Task{
    Name:        "Data Quality Check",
    Description: "Analyze data quality and generate a report",
    Steps: []ab.Step{
        {
            ID:       "profile",
            Content:  "Explore all sheets and generate quality stats",
            Position: 0,
        },
        {
            ID:       "report",
            Content:  "Create a document with findings",
            Position: 1,
            Gate: &ab.Gate{
                Type:      ab.GateTypeApproval,
                Approvers: []string{"lead@example.com"},
                OnReject:  ab.OnRejectAbort,
            },
        },
    },
    Trigger: &ab.Trigger{
        Type:     ab.TriggerTypeSchedule,
        Enabled:  true,
        RRule:    "FREQ=WEEKLY;BYDAY=MO;BYHOUR=9;BYMINUTE=0",
        Timezone: "America/Guayaquil",
    },
}
```

**Conditions for branching:**

```go
step := ab.Step{
    ID:       "classify",
    Content:  "Classify ticket priority",
    Position: 0,
    Condition: &ab.Condition{
        Field:    "priority",
        Operator: ab.OpEquals,
        Value:    "urgent",
        IfTrue:   "escalate",
        IfFalse:  "auto_resolve",
    },
}
```

### ModeProvider

Execution modes with tool access control:

```go
mode := ab.Mode{
    ID:             "code-reviewer",
    Name:           "Code Reviewer",
    BaseModeID:     ab.BaseModeBalanced,
    PromptStrategy: ab.PromptStrategyAdditions,
    PromptContent:  "Focus exclusively on reviewing code. Do not create artifacts.",
    ToolsMode:      ab.ToolsModeDenylist,
    ToolsList:      []string{"document-operations", "sheet-operations", "delete"},
    ModelSettings:  &ab.ModelSettings{ReasoningEffort: "high"},
}

// Check tool access
mode.IsToolAllowed("computer-ops") // true (not in denylist)
mode.IsToolAllowed("delete")       // false (in denylist)
```

### PlanProvider

DAG-based orchestration with dependency tracking:

```go
plan := ab.Plan{
    Title:     "Implement Auth System",
    Objective: "Add JWT auth to the API",
    Executables: []ab.Executable{
        {ID: "schema", Name: "DB Schema", Status: ab.ExecStatusCompleted},
        {ID: "middleware", Name: "Auth Middleware", Dependencies: []string{"schema"}, Status: ab.ExecStatusNotStarted},
        {ID: "tests", Name: "Integration Tests", Dependencies: []string{"middleware"}, Status: ab.ExecStatusNotStarted},
    },
}

// Get executables ready to dispatch
ready := plan.NextReady() // → [{middleware}] since schema is completed

// Check if blocked
plan.IsBlocked("tests")      // true — middleware not done
plan.IsBlocked("middleware")  // false — schema is done

// Check completion
plan.IsComplete() // false
```

### WorkflowEngine

6-phase lifecycle tracking:

```go
// Phases: Orientation → Alignment → Preparation → Execution → Verification → Closure

// Register a hook that runs before entering Execution phase
workflow.RegisterHook(ab.PhaseExecution, func(ctx context.Context, from, to ab.Phase) error {
    // Ensure checkpoint exists
    if !engine.HasCheckpoints() {
        return errors.New("checkpoint provider required before execution")
    }
    return nil
})

// Advance through phases
workflow.Advance(ctx) // Orientation → Alignment
workflow.Advance(ctx) // Alignment → Preparation
```

### EventBus

Publish/subscribe for inter-component notifications:

```go
bus := ab.NewEventBus()

// Subscribe to runner completions
sub := bus.Subscribe(ab.EventRunnerCompleted, func(e ab.Event) {
    threadID := e.Payload["thread_id"].(string)
    fmt.Printf("Runner %s completed from %s\n", threadID, e.Source)
})

// Publish an event
bus.Publish(ab.Event{
    Type:    ab.EventRunnerCompleted,
    Source:  "th_abc123",
    Payload: map[string]any{"thread_id": "th_xyz", "result": "success"},
})

// Cancel when done
sub.Cancel()
```

### ThreadProvider

Thread lifecycle and runner spawning:

```go
// Spawn a runner
threadID, err := engine.Threads.Spawn(ctx, ab.Runner{
    Tier: ab.RunnerTierMini,
    Task: "Analyze the sales dataset and produce a summary sheet",
    ResourceBundle: []ab.ResourceRef{
        {ID: "sh_abc", Type: "sheet", Description: "Sales Q4 data"},
    },
})

// Send a message between threads
engine.Threads.SendMessage(ctx, ab.Message{
    FromThreadID: "th_parent",
    ToThreadID:   "th_child",
    Content:      "Please also include regional breakdown",
    Delivery:     ab.DeliveryInterjected,
})

// Report objective status from child
engine.Threads.ReportStatus(ctx, "th_parent", ab.ObjectiveReport{
    Status:  ab.ObjectiveStatusSuccess,
    Summary: "Analysis complete, sheet updated",
})
```

### CheckpointProvider

Safety snapshots — required before and after any artifact mutation:

```go
// Before mutation
cp, _ := engine.Checkpoints.Create(ctx, "Before updating sales sheet")

// ... do the work ...

// After mutation
engine.Checkpoints.Create(ctx, "After updating sales sheet — added Q4 column")

// Rollback if something went wrong
engine.Checkpoints.Restore(ctx, cp.ID)
```

## Wiring Everything Together

```go
engine := ab.New(
    ab.WithMemory(myFSMemory),
    ab.WithSandbox(myDockerSandbox),
    ab.WithToolRegistry(myRegistry),
    ab.WithSkills(mySkillLoader),
    ab.WithThreads(myThreadManager),
    ab.WithCheckpoints(mySQLiteCheckpoints),
    ab.WithPlanning(myPlanStore),
    ab.WithTasks(myTaskStore),
    ab.WithModes(myModeRegistry),
    ab.WithWorkflow(myWorkflowTracker),
    ab.WithEventBus(ab.NewEventBus()),
)

// Use only what's wired — nil-safe checks
if engine.HasMemory() {
    content, _ := engine.Memory.View(ctx, ab.ScopeProject, "/README.md")
    fmt.Println(content)
}

if engine.HasTools() {
    for _, tool := range engine.Tools.List() {
        fmt.Printf("Tool: %s (%s)\n", tool.Name, tool.Category)
    }
}
```

## Extension Points

1. **Custom tools** — Register any `ToolExecuteFunc` into the `ToolRegistry`
2. **Custom skills** — Implement `SkillProvider` backed by filesystem, database, or API
3. **Custom modes** — Create modes with prompt overrides and tool restrictions
4. **Custom events** — Define your own `EventType` constants and publish/subscribe
5. **Custom sandbox** — Implement `SandboxDriver` for Docker, Firecracker, SSH, or local exec
6. **Custom LLM** — Implement `LLMProvider` for Anthropic, OpenAI, Ollama, or any backend
7. **Custom routing** — Implement `ModelRouter` to route models to different providers
8. **Custom stop conditions** — Use `ShouldStop` hook to end the loop on budget, time, or content
9. **Custom request building** — Use `BuildRequest` hook to inject context per turn
10. **Custom tool result transformation** — Use `OnToolResult` to truncate, redact, or enrich

---

## LLM Integration

The SDK is **LLM-agnostic**. You bring your own provider — Anthropic, OpenAI, Ollama, Groq, local inference, or anything that speaks chat completions.

### LLMProvider

```go
type LLMProvider interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}
```

A single method. Your implementation wraps whatever HTTP client or SDK you use:

```go
type AnthropicProvider struct{ apiKey string }

func (a *AnthropicProvider) Chat(ctx context.Context, req ab.ChatRequest) (*ab.ChatResponse, error) {
    // Convert req.Messages → Anthropic API format
    // POST https://api.anthropic.com/v1/messages
    // Convert response → *ab.ChatResponse
}
```

### ModelRouter (multi-model)

```go
type ModelRouter interface {
    Route(model string) (LLMProvider, error)
}
// "claude-*" → Anthropic, "gpt-*" → OpenAI, "llama-*" → Ollama
```

---

## AgentLoop — Autonomous LLM Execution

This is the **core power** of the SDK. The `AgentLoop` lets the LLM drive everything autonomously:

- **The LLM decides** which tools to call
- **The LLM decides** in what order
- **The LLM decides** when to checkpoint, spawn threads, or stop
- **You define** what it CAN do (tools) and what it SHOULD do (system prompt)

### How It Works

```
┌─────────────────────────────────────────────────────────────┐
│                      AgentLoop                               │
│                                                              │
│   ┌─────────┐     ┌──────────┐     ┌──────────────────┐    │
│   │  Build  │────▶│  Call    │────▶│  Has tool calls? │    │
│   │ Request │     │  LLM    │     │                  │    │
│   └─────────┘     └──────────┘     └────────┬─────────┘    │
│        ▲                                     │              │
│        │                              yes    │    no        │
│        │                              ┌──────┘    │         │
│        │                              ▼           ▼         │
│   ┌────┴───────┐     ┌───────────────────┐  ┌─────────┐   │
│   │  Append   │◀────│  ToolDispatcher   │  │ RETURN  │   │
│   │  Results  │     │  Execute all      │  │ Result  │   │
│   └────────────┘     └───────────────────┘  └─────────┘   │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

1. **Build** — Assembles `ChatRequest` (system prompt + history + tool defs)
2. **Call LLM** — Sends to provider (with retry on error)
3. **Check** — Tool calls? → dispatch. Text only? → done.
4. **Dispatch** — `ToolDispatcher` parses JSON args → finds tool → executes → returns result
5. **Append** — Results added to conversation history
6. **Loop** — Repeat until LLM stops or max turns reached

### Minimal Example (zero Engine dependency)

```go
result, err := ab.RunAgentLoop(ctx, ab.AgentLoopConfig{
    // ── Only Provider is REQUIRED ──
    Provider: myAnthropicClient,

    // ── Everything else is optional ──
    Model:        "claude-sonnet-4-20250514",
    SystemPrompt: "You are a code assistant. Use tools to accomplish tasks.",
    Tools:        myToolRegistry,
    MaxTurns:     20,
}, []ab.ChatMessage{
    {Role: ab.RoleUser, Content: "Create the users table migration and write tests"},
})

fmt.Println(result.StopReason)   // "complete"
fmt.Println(result.TotalTurns)   // 5
fmt.Println(result.FinalContent) // "I've created the migration and tests..."
```

### Full Example (all hooks)

```go
result, err := ab.RunAgentLoop(ctx, ab.AgentLoopConfig{
    Provider:     anthropicLLM,
    Model:        "claude-sonnet-4-20250514",
    SystemPrompt: systemPrompt,
    Tools:        registry,
    Sandbox:      dockerSandbox,
    SandboxID:    "cmp_abc123",
    Events:       eventBus,
    MaxTurns:     30,
    MaxRetries:   5,

    // ── Observe every turn ──
    OnTurn: func(turn int, resp *ab.ChatResponse) bool {
        log.Printf("Turn %d: %d tool calls", turn, len(resp.ToolCalls))
        return true
    },

    // ── Block dangerous tools ──
    OnToolCall: func(call ab.ToolCallEntry) bool {
        if call.Name == "delete" { return false }
        return true
    },

    // ── Transform results ──
    OnToolResult: func(call ab.ToolCallEntry, r ab.ToolResult) ab.ToolResult {
        if len(r.Content) > 10000 {
            r.Content = r.Content[:10000] + "\n...(truncated)"
        }
        return r
    },

    // ── Budget guard ──
    ShouldStop: func(turn int, resp *ab.ChatResponse) bool {
        return totalCost > maxBudget
    },

    // ── Inject fresh context each turn ──
    BuildRequest: func(sys string, msgs []ab.ChatMessage, tools *ab.ToolRegistry) ab.ChatRequest {
        return ab.ChatRequest{
            Model:    "claude-sonnet-4-20250514",
            Messages: append([]ab.ChatMessage{{Role: ab.RoleSystem, Content: sys}}, msgs...),
            Tools:    tools.ToolDefs(),
        }
    },

    // ── Retry with backoff ──
    OnError: func(err error, attempt int) bool {
        time.Sleep(time.Duration(attempt) * time.Second)
        return attempt < 3
    },
}, userMessages)
```

### Convenience (with Engine)

```go
result, err := ab.RunAgentLoopWithEngine(ctx, engine, "code-agent", ab.AgentLoopConfig{
    MaxTurns: 20,
}, userMessages)
// Auto-resolves: Provider, Model, Tools, Sandbox, Events from Engine + Mode
```

### ToolDispatcher

Bridges LLM tool calls to actual execution:

```go
dispatcher := ab.NewToolDispatcher(registry, sandbox)

// Dispatch a single call
result := dispatcher.Dispatch(ctx, ab.ToolCallEntry{
    ID:        "call_001",
    Name:      "computer-ops",
    Arguments: `{"command": "ls -la /home/user/project"}`,
}, "sandbox_123")
// result.Content → "total 48\ndrwxr-xr-x ..."

// Dispatch all calls from a response
results := dispatcher.DispatchAll(ctx, resp.ToolCalls, "sandbox_123")

// Convert to messages for next LLM turn
msgs := ab.ToMessages(resp.ToolCalls, results)
```

### What the LLM Controls

With the Autobuild system prompt and 63 tools registered, the LLM **autonomously**:

| Action                       | Tool Called                                                            |
| ---------------------------- | ---------------------------------------------------------------------- |
| Read project memory          | `memory` → `view`                                                      |
| Create safety checkpoints    | `create-checkpoint`                                                    |
| Decompose work into DAG      | `initiative-operations`, `feature-operations`, `executable-operations` |
| Spawn implementation threads | `thread-operations` → `create`                                         |
| Track status transitions     | `executable-operations` → `update`                                     |
| Monitor PRs and CI           | `computer-ops` → `gh pr status`                                        |
| Communicate with children    | `thread-messaging` → `send`                                            |
| Announce merges              | `slack-operations`                                                     |
| Load skills on demand        | `skills-operations` → `load`                                           |
| Decide it's done             | Returns text → loop ends naturally                                     |

**You don't program the workflow. You describe it in the system prompt. The LLM executes it through tool calls. The SDK manages the cycle.**

### Stop Reasons

| Reason      | Meaning                              |
| ----------- | ------------------------------------ |
| `complete`  | LLM returned text with no tool calls |
| `max_turns` | Hit MaxTurns limit (safety net)      |
| `aborted`   | `OnTurn` returned false              |
| `stopped`   | `ShouldStop` returned true           |
| `error`     | LLM call failed after retries        |

---

## Skills, Modes & Interconnection

Everything in the SDK is interconnected through a layered system where **skills inject knowledge**, **modes control access**, and **the LLM navigates all of it through tools**.

### How Skills Work

Skills are packages of domain-specific knowledge (markdown files with YAML frontmatter) that get loaded into the LLM's context. The SDK supports **three strategies** for loading them — you choose which to use (or combine all three):

#### Strategy 1: Auto-match before the loop

Scan the user's request against skill triggers **before** the AgentLoop starts. Matched skills are injected into the system prompt:

```go
// In your prompt builder (before RunAgentLoop)
matched, _ := engine.Skills.Match(ctx, userRequest)
for _, sk := range matched {
    engine.Skills.Load(ctx, sk.Name)
    systemPrompt += "\n## Skill: " + sk.Name + "\n" + sk.Content
}
```

The LLM doesn't even know this happened — it just sees the skill content as part of its instructions.

#### Strategy 2: LLM loads skills on-demand via tool

Register a `skills-operations` tool and the LLM decides when to load skills mid-conversation:

```go
reg.Register(&ab.Tool{
    Name: "skills-operations",
    Execute: func(ctx context.Context, sandboxID string, args map[string]any) (string, error) {
        switch args["operation"].(string) {
        case "load":
            skill, _ := engine.Skills.Load(ctx, args["skillName"].(string))
            return skill.Content, nil  // LLM receives the full skill
        case "list":
            names, _ := engine.Skills.List(ctx)
            return strings.Join(names, "\n"), nil
        }
        return "", nil
    },
})
```

The system prompt tells the LLM what skills exist (§27 in system.md). The LLM calls `skills-operations → load` when it decides it needs one. The skill content comes back as a tool result and the LLM applies it on subsequent turns.

#### Strategy 3: Hybrid (recommended)

- **Auto-match** loads obvious skills at startup (based on user request triggers)
- **Tool `skills-operations`** lets the LLM load additional skills mid-conversation
- **`BuildRequest` hook** can re-evaluate triggers every N turns

```go
ab.RunAgentLoop(ctx, ab.AgentLoopConfig{
    Provider:     llm,
    SystemPrompt: promptWithAutoMatchedSkills,  // strategy 1
    Tools:        registryWithSkillsTool,        // strategy 2 available
    BuildRequest: func(sys string, msgs []ab.ChatMessage, tools *ab.ToolRegistry) ab.ChatRequest {
        // Strategy 3: re-inject skills based on conversation evolution
        lastMsg := msgs[len(msgs)-1].Content
        newSkills, _ := engine.Skills.Match(ctx, lastMsg)
        // ... append new skill content to system prompt
    },
}, messages)
```

### GrantedTools — Skills Expand the Tool Surface

When a skill is loaded, its `GrantedTools` field can unlock additional tools that weren't available before:

```go
// Skill YAML frontmatter:
// grantedTools:
//   - bigquery-internal
//   - run-sql-with-duck-db

skill, _ := engine.Skills.Load(ctx, "data-migration")
// skill.GrantedTools → ["bigquery-internal", "run-sql-with-duck-db"]
// These tools are now available to the LLM
```

This creates a **progressive disclosure** model: the LLM starts with a base set of tools, and loading skills expands what it can do.

### Modes Control What the LLM Sees

Modes act as **hard filters** on the tool surface. The LLM can only call tools that pass the mode's access control:

```go
// Orchestrator mode: sees all 63 tools
orchestratorMode := ab.Mode{ToolsMode: ""}  // empty = everything allowed

// Code reviewer: blocked from destructive tools
reviewerMode := ab.Mode{
    ToolsMode: ab.ToolsModeDenylist,
    ToolsList: []string{"delete", "document-operations", "sheet-operations"},
}

// Code agent: only sees what it needs
codeAgentMode := ab.Mode{
    ToolsMode: ab.ToolsModeAllowlist,
    ToolsList: []string{"computer-ops", "memory", "thread-operations", "create-checkpoint"},
}
```

`NewChatRequest` applies the filter automatically — the LLM never sees tools outside its mode.

### The Full Interconnection Flow

```
┌─────────────────────────────────────────────────────────────────────┐
│                        BEFORE THE LOOP                               │
│                                                                      │
│  User Request ──▶ SkillProvider.Match() ──▶ auto-load matched skills │
│                                                                      │
│  Mode ──▶ ModelSettings (model, temp) + ToolsMode (allow/deny)       │
│                                                                      │
│  Loaded skills ──▶ GrantedTools ──▶ expand ToolRegistry              │
│                                                                      │
│  MemoryProvider ──▶ project context + user preferences               │
│                                                                      │
│  All together ──▶ BuildSystemPrompt() ──▶ final system prompt        │
│                                                                      │
├─────────────────────────────────────────────────────────────────────┤
│                        INSIDE THE LOOP                               │
│                                                                      │
│  Turn 1: LLM reads prompt → decides what to do                      │
│          → calls memory(view), explore-artifacts                     │
│                                                                      │
│  Turn 2: LLM needs PR monitoring knowledge                          │
│          → calls skills-operations(load, "pr-monitoring")            │
│          → receives skill content as tool result                     │
│                                                                      │
│  Turn 3: LLM applies the skill it just loaded                       │
│          → calls computer-ops("gh pr status")                        │
│          → calls executable-operations(update, "in_review")          │
│                                                                      │
│  Turn N: LLM decides task is complete                                │
│          → returns text without tool calls → loop ends               │
│                                                                      │
└─────────────────────────────────────────────────────────────────────┘
```

### What the LLM Accesses and Who Controls It

| Layer                   | How the LLM accesses it                   | Who controls access              |
| ----------------------- | ----------------------------------------- | -------------------------------- |
| **Memory**              | Tool `memory` (view/create/replace)       | LLM decides when to read/write   |
| **Skills**              | Tool `skills-operations` (load/list)      | LLM decides when to load         |
| **Sandbox**             | Tool `computer-ops` (execute commands)    | LLM decides what to run          |
| **Threads**             | Tool `thread-operations` (spawn/archive)  | LLM decides when to create       |
| **Checkpoints**         | Tool `create-checkpoint`                  | LLM decides when to snapshot     |
| **Plans/DAG**           | Tools `initiative/feature/executable-ops` | LLM builds the DAG               |
| **Tool surface**        | Mode `IsToolAllowed()` filter             | **You control** (allow/denylist) |
| **Skill-granted tools** | `GrantedTools` on loaded skills           | Skill defines, system loads      |
| **Dangerous ops**       | `OnToolCall` hook                         | **You control** (runtime block)  |

### Configurable at Every Level

| Level                       | What you configure              | How                                                   |
| --------------------------- | ------------------------------- | ----------------------------------------------------- |
| **Pre-loop**                | Which skills auto-load          | `engine.Skills.Match(ctx, userRequest)`               |
| **Mode**                    | Which tools the LLM sees        | `ToolsMode: allowlist/denylist`                       |
| **Hook: OnToolCall**        | Block specific calls at runtime | `if call.Name == "delete" { return false }`           |
| **Hook: OnToolResult**      | Transform/redact tool output    | Truncate, sanitize, enrich                            |
| **Hook: BuildRequest**      | Re-inject skills mid-loop       | Re-evaluate triggers per turn                         |
| **Hook: ShouldStop**        | Budget/time/content guards      | `return totalCost > maxBudget`                        |
| **Tool: skills-operations** | LLM self-serves knowledge       | LLM reads skill list from prompt, loads what it needs |

The SDK hardcodes **zero policy**. Every decision about what the LLM can see, do, and access is injected by you.

---

## The Power Model

```
┌──────────────────────────────────────────────────────────┐
│  YOU define:                                              │
│                                                           │
│  1. System Prompt  → What the LLM SHOULD do              │
│  2. Tools          → What the LLM CAN do                 │
│  3. Hooks          → Guardrails (budget, safety, audit)  │
│                                                           │
├──────────────────────────────────────────────────────────┤
│  THE LLM decides:                                         │
│                                                           │
│  1. Which tools to call                                   │
│  2. In what order                                         │
│  3. With what arguments                                   │
│  4. Whether to retry on failure                           │
│  5. When the task is complete                             │
│                                                           │
├──────────────────────────────────────────────────────────┤
│  THE SDK manages:                                         │
│                                                           │
│  1. LLM ↔ Tool dispatch cycle                            │
│  2. JSON parsing and tool resolution                      │
│  3. Error handling and retries                            │
│  4. Token tracking                                        │
│  5. Event emission                                        │
│  6. Conversation history accumulation                     │
│                                                           │
└──────────────────────────────────────────────────────────┘
```

This means:

- **Swap LLM** without changing tools or prompts
- **Swap tools** without changing the loop or prompts
- **Swap prompts** without touching code
- **Add guardrails** without modifying business logic
- **Scale** from single-turn assistant to multi-hour orchestrator by changing MaxTurns + system prompt

---

## Design Decisions

| Decision                        | Rationale                                                                 |
| ------------------------------- | ------------------------------------------------------------------------- |
| Zero external deps              | Consumers bring their own; SDK stays lean and auditable                   |
| Interface-first                 | Every capability is swappable; `Engine` composes, never implements        |
| Functional options              | Extensible config without breaking the API                                |
| LLM-agnostic                    | Single `Chat()` method — wrap any provider in 20 lines                    |
| AgentLoop decoupled from Engine | Core loop needs only `LLMProvider` + `ToolRegistry`; no forced coupling   |
| Hooks over config               | `OnTurn`, `OnToolCall`, `ShouldStop` cover any control flow without flags |
| Standalone module               | Independent versioning and external consumption                           |
| In-memory EventBus              | Reference implementation included; swap for Redis/NATS/etc. in production |

## License

See [LICENSE](../../LICENSE) in the repository root.
# harness-sdk
