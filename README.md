# harness-sdk

Composable Go SDK for building AI agent backends with formal orchestration, parallel execution, persistent memory, and real-time UI streaming.

## Repository Structure

```
harness-sdk/
├── sdk/                  # Core Go SDK (autobuild-sdk)
├── example/
│   ├── backend-chat/     # Go backend using the SDK
│   └── chat-app/         # React frontend (Vite + TanStack)
└── docs/                 # Architecture & design docs
```

## SDK (`sdk/`)

Provider-agnostic, zero external dependencies, pure Go stdlib. Wire only what you need.

```bash
go get github.com/everfaz/autobuild-sdk
```

```go
engine := ab.New(
    ab.WithLLM(myProvider),
    ab.WithToolRegistry(tools),
    ab.WithMemory(memoryImpl),
    ab.WithWorkflow(workflowImpl),
    ab.WithPlanning(planImpl),
    ab.WithThreads(threadImpl),
    ab.WithEventBus(ab.NewEventBus()),
)
```

### Core Abstractions

| Abstraction     | Interface            | Purpose                                                           |
| --------------- | -------------------- | ----------------------------------------------------------------- |
| **Engine**      | `Engine`             | Composes all providers; opt-in architecture                       |
| **LLM**         | `LLMProvider`        | Provider-agnostic chat completion                                 |
| **Router**      | `RoutedLLMProvider`  | Multi-provider routing (`anthropic/claude-...`, `openai/gpt-...`) |
| **Agent Loop**  | `RunAgentLoop()`     | LLM ↔ tool execution cycle with hooks                             |
| **Tools**       | `ToolRegistry`       | Thread-safe, categorized tool collection with JSON Schema params  |
| **Memory**      | `MemoryProvider`     | Persistent storage (user/project scopes)                          |
| **Skills**      | `SkillProvider`      | On-demand knowledge with trigger matching                         |
| **Modes**       | `ModeProvider`       | Execution profiles with tool access control                       |
| **Plan**        | `PlanProvider`       | DAG of executables with dependency tracking                       |
| **Workflow**    | `WorkflowEngine`     | 6-phase lifecycle (Orientation → Closure)                         |
| **Threads**     | `ThreadProvider`     | Runner spawning and parallel execution                            |
| **Tasks**       | `TaskProvider`       | Reusable workflows with steps, gates, triggers                    |
| **Sandbox**     | `SandboxDriver`      | Isolated command execution and file I/O                           |
| **Events**      | `EventBus`           | Pub/sub for inter-component notifications                         |
| **Checkpoints** | `CheckpointProvider` | Safety snapshots for rollback                                     |
| **Dispatch**    | `ToolDispatcher`     | Resolves LLM tool calls into executions                           |

See [sdk/README.md](sdk/README.md) for full API documentation and code examples.

### Workflow Phases

The SDK defines a 6-phase formal orchestration lifecycle:

```
Orientation → Alignment → Preparation → Execution → Verification → Closure
```

Each phase supports hooks via `RegisterHook()`. Phases advance deterministically; the LLM executes within hooks, not by controlling the phase transitions.

### Plan (DAG Execution)

Plans are directed acyclic graphs of executables:

```go
plan := ab.Plan{
    Executables: []ab.Executable{
        {ID: "a", Name: "Task A"},
        {ID: "b", Name: "Task B", Dependencies: []string{"a"}},
    },
}
ready := plan.NextReady()  // returns tasks whose dependencies are met
```

### Agent Loop

The agent loop handles the LLM ↔ tool cycle:

```go
result, err := ab.RunAgentLoop(ctx, ab.AgentLoopConfig{
    Provider: llm,
    Model:    "anthropic/claude-haiku-4-5-20251001",
    Tools:    registry,
    MaxTurns: 6,
}, messages)
```

## Backend Example (`example/backend-chat/`)

Go HTTP server demonstrating the full SDK integration.

### Features

- LLM-driven parallelism detection (automatic, no manual flags)
- Formal workflow orchestration with Plan execution
- Persistent memory (filesystem-backed, user/project scopes)
- Skill loading from markdown files
- Mode system (balanced, analyst, code-agent, code-reviewer, deep-work)
- SSE streaming for real-time runner updates
- SQLite persistence for chat history
- Multi-provider LLM routing (Anthropic, OpenAI, Ollama)

### Setup

```bash
cd example/backend-chat
cp .env.example .env
# Add your API keys to .env

go run .
# → listening on :8080
```

### API Endpoints

| Method | Path                       | Description                                             |
| ------ | -------------------------- | ------------------------------------------------------- |
| `GET`  | `/healthz`                 | Health check                                            |
| `GET`  | `/api/modes`               | List available modes                                    |
| `GET`  | `/api/providers`           | List configured LLM providers                           |
| `POST` | `/api/chats`               | Create a new chat                                       |
| `GET`  | `/api/chats/{id}/messages` | Get chat history                                        |
| `POST` | `/api/chats/{id}/run`      | Execute a prompt (returns assistant response + runners) |
| `GET`  | `/api/chats/{id}/events`   | SSE stream for runner/trace updates                     |

### Execution Flow

```
User prompt
    │
    ▼
shouldUseFormalPlan()          ← LLM classifies: parallel or sequential?
    │
    ├─ parallel=false ────────► RunAgentLoopWithEngine()  (standard agent loop)
    │
    └─ parallel=true ─────────► Workflow orchestration:
                                    │
                                    ├─ PhaseOrientation
                                    ├─ PhaseAlignment
                                    ├─ PhasePreparation    → Hook: Plan.Propose()
                                    ├─ PhaseExecution      → Hook: Spawn runners in parallel
                                    ├─ PhaseVerification   → Hook: Collect results
                                    └─ PhaseClosure
                                            │
                                            ▼
                                    Response with runner summaries
```

### Runner Architecture

Each runner is an independent agent loop with its own tool registry:

```
Runner (th_runner_1)           Runner (th_runner_2)           Runner (th_runner_3)
    │                              │                              │
    ▼                              ▼                              ▼
RunAgentLoop()                 RunAgentLoop()                 RunAgentLoop()
├─ LLM calls                  ├─ LLM calls                  ├─ LLM calls
├─ Tool execution              ├─ Tool execution              ├─ Tool execution
└─ Result                      └─ Result                      └─ Result
```

Runners share:

- Same LLM provider and model
- Same memory provider (can read/write project memory)
- Same skill provider

Runners do NOT share:

- Conversation state
- Sandbox instances

## Frontend Example (`example/chat-app/`)

React 19 + Vite + TanStack Router + shadcn/ui.

### Features

- Real-time runner visualization (RunnerThread cards with status animations)
- SSE integration for live progress updates
- Chain-of-thought trace display
- Mode selector (balanced, analyst, etc.)
- Responsive grid layout for parallel runners

### Setup

```bash
cd example/chat-app
bun install
bun dev
# → http://localhost:3000
```

### Key Components

| Component        | Purpose                                                              |
| ---------------- | -------------------------------------------------------------------- |
| `ChatMain`       | Main chat UI with message history and SSE listeners                  |
| `RunnerThread`   | Visual card for each parallel runner (pending → running → completed) |
| `ChainOfThought` | Trace/reasoning step display                                         |
| `AIPromptInput`  | Input with mode selector                                             |

### Runner Status Flow (UI)

```
pending  →  running  →  completed
   │            │            │
   ▼            ▼            ▼
  gray        blue         green
  Clock     Loader2    CheckCircle2
            (animated)
```

Status normalization maps backend values to UI states:

- `queued`, `planned` → `pending`
- `in_progress` → `running`
- `success`, `done` → `completed`
- `failure`, `error` → `failed`

## Documentation (`docs/`)

| Document                                    | Topic                                          |
| ------------------------------------------- | ---------------------------------------------- |
| [system.md](docs/system.md)                 | Autobuild orchestrator system prompt           |
| [workflow.md](docs/workflow.md)             | Workflow lifecycle phases                      |
| [threads.md](docs/threads.md)               | Thread model and runner architecture           |
| [tools.md](docs/tools.md)                   | Tool catalog and categories                    |
| [memory.md](docs/memory.md)                 | Memory and context patterns                    |
| [modo.md](docs/modo.md)                     | Execution modes (balanced, analyst, deep-work) |
| [automatization.md](docs/automatization.md) | Task automation (steps, gates, triggers)       |
| [more-knowledge.md](docs/more-knowledge.md) | Agent identity and behavior reference          |

## Development

### Prerequisites

- Go 1.22+
- Bun (for frontend)
- Anthropic API key (or OpenAI/Ollama)

### Build & Verify

```bash
# SDK
cd sdk && go build ./...

# Backend
cd example/backend-chat && go build ./...

# Frontend
cd example/chat-app && bun run typecheck && bun run build
```

### Run Everything

```bash
# Terminal 1: Backend
cd example/backend-chat && set -a && source .env && set +a && go run .

# Terminal 2: Frontend
cd example/chat-app && bun dev
```

## License

Private — Everfaz.
