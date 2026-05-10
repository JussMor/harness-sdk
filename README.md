# harness-sdk

> A minimal, opt-in Go SDK for building AI agents that work like Claude — same
> lifecycle, same memory discipline, same layered prompt assembly. The SDK is
> the skeleton; the LLM you plug in is the brain.

- **Module:** `github.com/everfaz/autobuild-sdk`
- **Go:** 1.22+
- **Version:** `0.2.0`
- **Status:** active development (`claude/sdk-v3` branch)

---

## Why

Most agent libraries ship one opinionated runtime and a fixed tool list.
`harness-sdk` does the opposite: every capability — memory, sandbox, tools,
modes, LLM, threads, tokenizer, conversation store — is an interface you wire
in. The runtime gives you the **shape** (6-phase lifecycle, layered system
prompt, compaction, interrupts, streaming, plan mode) and gets out of the way.

You can run it with:

- one line (`NewWithDefaults` + an `LLMProvider`), or
- a fully wired backend with persistent memory, SQLite threads, an OpenSandbox
  cluster, plan mode, human-in-the-loop interrupts and SSE streaming — see
  [`example/backend-chat`](example/backend-chat/).

---

## Repository layout

```
harness-sdk/
├── sdk/                          # the SDK (one Go module)
│   ├── *.go                      # core abstractions
│   └── providers/                # reference implementations
│       ├── llm/                  # anthropic, openai, ollama
│       ├── memory/               # filesystem (Claude-Code-parity memdir)
│       ├── sandbox/              # opensandbox + dev (in-process)
│       ├── store/                # filesystem ConversationStore
│       ├── thread/               # sqlite, postgres, memory
│       └── tokenizers/           # tiktoken, byte, auto
├── clients/
│   ├── harness-client/           # TS client (SSE + REST)
│   ├── harness-react/            # React hooks
│   └── tygo.yaml                 # Go → TS type generation
├── example/
│   ├── backend-chat/             # canonical Go server (SSE on :9090, SQLite)
│   └── chat-app/                 # React/Vite chat frontend (:3000)
├── scripts/gen-events/           # generates clients/.../events.ts
├── docs/claude-model.md          # implementation notes
├── Makefile                      # `make client` / `make client-types-check`
└── go.work
```

---

## Install

```bash
go get github.com/everfaz/autobuild-sdk
```

For the example backend / frontend:

```bash
git clone https://github.com/JussMor/harness-sdk.git
cd harness-sdk

# Backend (Go)
cd example/backend-chat && go run .
# → http://localhost:9090

# Frontend (React/Vite)
cd ../chat-app && pnpm install && pnpm dev
# → http://localhost:3000
```

---

## Quick start

```go
package main

import (
    "context"
    "log"

    ab "github.com/everfaz/autobuild-sdk"
    "github.com/everfaz/autobuild-sdk/providers/llm"
)

func main() {
    engine := ab.NewWithDefaults(128_000) // 128k context budget
    engine.LLM = llm.NewAnthropic("YOUR_API_KEY", "claude-sonnet-4-5")

    runtime := ab.NewRuntime(engine)

    conv := ab.NewConversation()
    res, err := runtime.Run(context.Background(), conv, "Hello!")
    if err != nil {
        log.Fatal(err)
    }
    log.Println(res.Response)
}
```

For streaming:

```go
events, _ := runtime.RunStream(ctx, conv, "Write a haiku about Go")
for ev := range events {
    switch ev.Type {
    case ab.StreamEventDelta:
        print(ev.Delta)
    case ab.StreamEventToolCall:
        log.Printf("tool: %s", ev.ToolCall.Name)
    case ab.StreamEventDone:
        return
    case ab.StreamEventError:
        log.Println("error:", ev.Error)
    }
}
```

---

## Architecture at a glance

### `Engine` — composition root ([sdk/engine.go](sdk/engine.go))

```go
type Engine struct {
    Memory  MemoryProvider       // typed memdir (user / project scopes)
    Sandbox SandboxDriver        // exec + file I/O
    Tools   *ToolRegistry        // typed tools with JSON Schema
    Threads ThreadProvider       // multi-user thread persistence
    Modes   ModeProvider         // mode overlays (model + tool config)
    LLM     LLMProvider          // chat backend (use RoutedLLMProvider for multi-model)
    Prompt  *SystemPromptBuilder // 6-layer prompt
    Budget  *ContextBudget       // token budget
}
```

Every field is **optional** — nil means the capability is simply unavailable.

### `Runtime` — orchestrator ([sdk/runtime.go](sdk/runtime.go))

```go
NewRuntime(engine).
    WithMode("balanced").
    WithModel("anthropic/claude-sonnet-4-5").
    WithMemoryRoots(ab.DefaultMemoryRoots...).
    WithMaxMemoryTokens(8_000).
    WithCompactor(&ab.LLMCompactor{LLM: engine.LLM}).
    WithPlanController(planCtl).
    WithSafety(safetyChain).
    WithPermissions(permEngine).
    WithConversationStore(store).
    WithThinkingBudget(2048)
```

### Layered system prompt ([sdk/system_prompt.go](sdk/system_prompt.go))

Six layers, assembled in priority order each turn:

```
Core      → invariant identity (set once)
Behavior  → DefaultBehaviorPrompt (operating principles)
Memory    → injected from MemoryProvider on conversation start
Skills    → content of currently loaded skills (via skill_tool)
Session   → ephemeral context (time, current state)
Mode      → active mode overlay (most specific)
```

### Memdir — typed memory ([sdk/memdir.go](sdk/memdir.go), [sdk/memory_tool.go](sdk/memory_tool.go))

Mirrors Claude Code's `memdir/`:

- **Closed taxonomy:** `user | feedback | project | reference`
- **Two scopes:** `user` (cross-project) and `project`
- **Index file:** `MEMORY.md` (auto-seeded + appended on every `create`)
- **Read-before-write contract** enforced by `ReadBeforeWriteTracker`
- **Single multi-op tool** (`memory`) with operations: `view`, `create`,
  `str_replace`, `delete`, `rename`, `list`, `search`, `find_relevant`
- **Per-turn `<system-reminder>`** with the manifest + per-scope `MEMORY.md`

### Streaming ([sdk/streaming.go](sdk/streaming.go))

`StreamEvent` types emitted by `RunStream` (and forwarded as SSE in
`example/backend-chat`):

```
delta · thinking · tool_call · tool_result ·
plan_mode_changed · interrupt_required · interrupt_resolved ·
artifact_created · artifact_updated · agent_result ·
turn_complete · compaction · done · error
```

The error event carries the provider error so the UI can show it instead of
spinning forever.

### Other building blocks

| File                                                          | What it is                                              |
| ------------------------------------------------------------- | ------------------------------------------------------- |
| [`agent_loop.go`](sdk/agent_loop.go)                          | Low-level LLM↔tool loop with retry/backoff              |
| [`agent_tool.go`](sdk/agent_tool.go)                          | The `agent` tool — spawns a sub-agent                   |
| [`skill_tool.go`](sdk/skill_tool.go)                          | Skill discovery + loading as a tool                     |
| [`plan_tool.go`](sdk/plan_tool.go)                            | `enter_plan_mode` / `exit_plan_mode` + `PlanController` |
| [`interrupt.go`](sdk/interrupt.go)                            | HIL: `Approval`, `Question`, `FormInput`                |
| [`compaction.go`](sdk/compaction.go)                          | LLM / Bullet / Episodic compactors                      |
| [`context_budget.go`](sdk/context_budget.go)                  | Skills / memory / history / reserve budget enforcement  |
| [`safety.go`](sdk/safety.go)                                  | Pre-dispatch safety filters + secret leak detection     |
| [`permissions.go`](sdk/permissions.go)                        | Per-tool / per-path permission engine                   |
| [`reasoning.go`](sdk/reasoning.go)                            | Thinking budget for reasoning models                    |
| [`replay.go`](sdk/replay.go) · [`tracing.go`](sdk/tracing.go) | Trace capture and replay                                |
| [`conversation_store.go`](sdk/conversation_store.go)          | Persistence of `Conversation` state                     |

---

## Bundled providers ([sdk/providers/](sdk/providers/))

| Kind           | Implementations                                                                     |
| -------------- | ----------------------------------------------------------------------------------- |
| **LLM**        | `anthropic`, `openai` (works with Groq / Together / Mistral via base URL), `ollama` |
| **Memory**     | `filesystem` — Claude-Code-parity memdir on disk                                    |
| **Sandbox**    | `opensandbox` (remote/local server), `dev` (in-process, dev only)                   |
| **Threads**    | `sqlite`, `postgres`, `memory`                                                      |
| **Tokenizers** | `tiktoken`, `byte`, `auto` (model-aware)                                            |
| **Stores**     | `filesystem` ConversationStore                                                      |

Multi-model routing:

```go
router := ab.NewRoutedLLMProvider().
    Register("anthropic", anthropicProv).
    Register("openai",    openaiProv).
    Register("ollama",    ollamaProv)
engine.LLM = router

// runtime.WithModel("anthropic/claude-sonnet-4-5")
```

---

## TypeScript clients

[`clients/harness-client`](clients/harness-client/) and
[`clients/harness-react`](clients/harness-react/) are generated against the Go
types so the wire protocol has a single source of truth.

```bash
make client              # regenerate types.ts + events.ts
make client-types-check  # CI guard — fails on diff
```

React usage:

```tsx
import { useHarness } from "@everfaz/harness-react";

const { session, events, send } = useHarness({
  baseUrl: "http://localhost:9090",
  chatId,
});
```

---

## Example backend ([`example/backend-chat`](example/backend-chat/))

The canonical end-to-end reference:

- HTTP + SSE on `:9090`
- SQLite for chats, messages, threads, conversations, artifacts
- `RoutedLLMProvider`: anthropic + openai + ollama + Echo fallback
- Filesystem memdir under `./memory/`
- OpenSandbox tools (`bash`, `code_interpreter`, `file_read`, `file_write`)
- Centrifugo pub/sub for real-time UI updates
- R2 / S3 artifact storage
- Friendly error categorisation (billing / rate_limit / auth / context_length / …)

### Environment variables

| Variable                                                                                              | Purpose                                                |
| ----------------------------------------------------------------------------------------------------- | ------------------------------------------------------ |
| `ANTHROPIC_API_KEY`                                                                                   | Anthropic key                                          |
| `OPENAI_API_KEY` / `OPENAI_BASE_URL`                                                                  | OpenAI-compatible endpoint                             |
| `OLLAMA_BASE_URL`                                                                                     | Ollama (default `http://localhost:11434`)              |
| `BACKEND_MODEL`                                                                                       | Default model ref (e.g. `anthropic/claude-sonnet-4-5`) |
| `OPEN_SANDBOX_API_KEY` / `OPEN_SANDBOX_DOMAIN` / `OPEN_SANDBOX_PROTOCOL` / `OPEN_SANDBOX_TTL_SECONDS` | OpenSandbox                                            |
| `CENTRIFUGO_API_URL` / `CENTRIFUGO_API_KEY`                                                           | Real-time pub/sub                                      |

---

## Development

```bash
# build everything
cd sdk             && go build ./... && go test ./...
cd example/backend-chat && go build ./...
cd example/chat-app    && pnpm exec tsc --noEmit

# regenerate TS clients after Go type changes
make client
```

The repo uses a Go workspace (`go.work`); the SDK and `example/backend-chat`
live in separate modules.

---

## License

See repository for current license terms.
