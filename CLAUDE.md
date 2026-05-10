# harness-sdk — Project guide for Claude

> Internal map of the SDK. Read this first when working in the repo.
> Module: `github.com/everfaz/autobuild-sdk` · Go 1.22+ · Version `0.2.0`

---

## Repo layout

```
harness-sdk/
├── sdk/                          # SDK Go core (one module)
│   ├── *.go                      # abstractions
│   └── providers/                # concrete impls
│       ├── llm/                  # anthropic.go, openai.go, ollama.go
│       ├── memory/               # filesystem.go (memdir on disk)
│       ├── sandbox/              # opensandbox.go + dev/ (in-process)
│       ├── store/                # filesystem.go (ConversationStore)
│       ├── thread/               # sqlite.go, postgres.go, memory.go
│       └── tokenizers/           # tiktoken.go, byte.go, auto.go
├── clients/
│   ├── harness-client/           # TS client (SSE + REST)
│   ├── harness-react/            # React hooks
│   └── tygo.yaml                 # Go → TS type generation
├── example/
│   ├── backend-chat/             # canonical Go server (:9090, SQLite)
│   └── chat-app/                 # React/Vite UI (:3000)
├── scripts/gen-events/           # generates clients/.../events.ts
├── docs/claude-model.md          # implementation deep-dive
├── Makefile                      # `make client`, `make client-types-check`
└── go.work
```

There is **no** central `Skills` provider, `EventBus`, `CheckpointProvider`,
`Verification` module, `ObservationStore`, or standalone `Subagent` module.
Older notes that mention them are stale.

---

## Engine — composition root (`sdk/engine.go`)

```go
type Engine struct {
    Memory  MemoryProvider
    Sandbox SandboxDriver
    Tools   *ToolRegistry
    Threads ThreadProvider
    Modes   ModeProvider
    LLM     LLMProvider
    Prompt  *SystemPromptBuilder
    Budget  *ContextBudget
}

New(opts ...Option) *Engine
NewWithDefaults(windowSize int) *Engine  // sets Prompt + Budget only
```

Every field is opt-in. `Has*()` helpers report what's wired.

Skills, plan mode and sub-agents are **tools**, not engine fields:

- `skill_tool.go`  — `skill` tool: discover/load skills as system context
- `plan_tool.go`   — `enter_plan_mode` / `exit_plan_mode` driven by `PlanController`
- `agent_tool.go`  — `agent` tool: spawn a sub-agent loop

---

## Runtime — orchestrator (`sdk/runtime.go`)

```go
NewRuntime(engine).
  WithMode("balanced").
  WithModel("anthropic/claude-sonnet-4-5").
  WithSafety(safetyChain).
  WithPermissions(permEngine).
  WithTokenizer(tok).
  WithConversationStore(store).
  WithSessionContext(prov).
  WithCompactor(&LLMCompactor{LLM: engine.LLM}).
  WithPlanController(planCtl).
  WithMaxMemoryTokens(8_000).
  WithMemoryRoots(DefaultMemoryRoots...).
  WithThinkingBudget(2048)

runtime.Run(ctx, conv, userMessage)        // (*RuntimeResult, error)
```

There is currently **no separate `RunStream` on Runtime** — for streaming you
either consume the LLM provider directly or use `RunAgentLoopWithEngine` with
a `Streaming` LLM. The example backend bridges `StreamEvent`s to SSE.

`RuntimeResult`: `Response`, `Turns`, `Usage`, `StopReason`, `MemoryWritten`,
`Trace`, `PlanProposed`, `WellbeingSignal`.

---

## AgentLoop — low-level (`sdk/agent_loop.go`)

```go
RunAgentLoop(ctx, AgentLoopConfig, messages) (*AgentLoopResult, error)
RunAgentLoopWithEngine(ctx, engine, modeID, cfg, messages) (*AgentLoopResult, error)
```

- Retry/backoff on transient errors (429, 5xx, timeout, EOF)
- Permanent: 401 / 403 / 400 / `context_length_exceeded`

---

## Layered system prompt (`sdk/system_prompt.go`)

Six layers, assembled in priority order each turn:

```
Core      → invariant identity (set once)
Behavior  → DefaultBehaviorPrompt
Memory    → injected from MemoryProvider on conversation start
Skills    → content of currently loaded skills (via skill_tool)
Session   → ephemeral context (time, current state)
Mode      → active mode overlay (most specific)
```

`Build()`, `BuildWithBudget(tok)`, `Set(layer, content)`, `Append`, `Clear`,
`SetMaxLayerTokens(layer, n)`.

---

## Memory — typed memdir (`sdk/memdir.go`, `sdk/memory.go`, `sdk/memory_tool.go`)

Mirrors Claude Code's `memdir/` 1:1.

- **Closed taxonomy:** `user | feedback | project | reference`
- **Two scopes:** `ScopeUser` (cross-project) and `ScopeProject`
- **Index file:** `MEMORY.md` per scope
  - Auto-seeded on first `create`
  - Auto-appended `- [Title](file.md) — hook` line on every subsequent `create`
  - Idempotent (skips if `(filename.md)` already in index)
  - Skipped when path itself is `MEMORY.md`
- **Tool:** single multi-op tool `memory` with operations:
  `view`, `create`, `str_replace`, `delete`, `rename`, `list`, `search`, `find_relevant`
- **Read-before-write contract:** `ReadBeforeWriteTracker` requires a `view` (or
  fresh `create`) before `str_replace`/`delete` in the same session. Disable for
  scripted seeding via `MemoryToolConfig.DisableReadBeforeWrite`.
- **Per-turn `<system-reminder>`:** wraps the manifest + per-scope `MEMORY.md`,
  capped at `MaxMemdirEntrypointLines` (200).

`MemoryProvider` interface:

```go
View / Create / StrReplace / Delete / Rename / List / Search
```

Some impls also satisfy `MemoryStater` (used by the tool to do an exists-check
without confusing populated scopes with phantom files).

Filesystem layout (used by `providers/memory/filesystem.go` and the example):

```
memory/
├── user/
│   ├── MEMORY.md
│   └── *.md
└── project/
    ├── MEMORY.md
    └── *.md
```

**Anti-patterns to avoid in the agent:**

- Calling `memory create` with empty `path` → rejected
- Re-calling `create` after a collision → use `view` then `str_replace`
- Shelling out via `bash` to read memory → forbidden by tool prompt
- Storing code patterns / git history as memory → not derivable rule violated

---

## Streaming (`sdk/streaming.go`)

`StreamEvent.Type`:

```
delta · thinking · tool_call · tool_result ·
plan_mode_changed · interrupt_required · interrupt_resolved ·
artifact_created · artifact_updated · agent_result ·
turn_complete · compaction · done · error
```

Helpers: `FanOutStream(ch)`, `CollectStream(ch)`.

In `example/backend-chat/main.go` (~line 651) each type maps to an SSE event
name. The frontend `handleStreamEvent` in `chat-main.tsx` consumes them.

The `error` event MUST be terminal; the backend always emits a final `done`
afterwards so clients can stop waiting. `classifyStreamError` (backend-chat)
maps raw provider errors → `{billing, rate_limit, auth, context_length,
timeout, network, unknown}` with friendly Spanish messages.

---

## Other key files

| File | What it is |
|------|------------|
| `agent_tool.go`         | `agent` tool — sub-agent loop |
| `skill_tool.go`         | `skill` tool — discover/load skills as context |
| `plan_tool.go`          | `enter_plan_mode` / `exit_plan_mode` + `PlanController` |
| `interrupt.go`          | HIL: `Approval`, `Question`, `FormInput` |
| `interrupt_store.go`    | Persistence of pending interrupts |
| `compaction.go`         | `LLMCompactor` / `BulletCompactor` / `EpisodicCompactor` |
| `context_budget.go`     | Skills / memory / history / reserve enforcement |
| `safety.go`             | Pre-dispatch filters + secret leak detection |
| `permissions.go`        | Per-tool / per-path permission engine |
| `reasoning.go`          | Thinking budget for reasoning models |
| `system_reminder.go`    | `<system-reminder>` injection (per-turn dynamic) |
| `replay.go` / `tracing.go` | Trace capture and replay |
| `conversation_store.go` | Persistence of `Conversation` state |
| `session_context.go`    | Pluggable session-context provider |

---

## Bundled providers

| Kind | Path | Implementations |
|------|------|-----------------|
| LLM        | `providers/llm/`        | `anthropic`, `openai` (works for Groq/Together/Mistral via base URL), `ollama` |
| Memory     | `providers/memory/`     | `filesystem` |
| Sandbox    | `providers/sandbox/`    | `opensandbox` (remote/local server), `dev` (in-process) |
| Threads    | `providers/thread/`     | `sqlite`, `postgres`, `memory` |
| Tokenizers | `providers/tokenizers/` | `tiktoken`, `byte`, `auto` |
| Stores     | `providers/store/`      | `filesystem` ConversationStore |

Multi-model routing via `RoutedLLMProvider` (`sdk/llm_router.go`):
`runtime.WithModel("anthropic/claude-sonnet-4-5")` →
`ParseModelRef` → `("anthropic", "claude-sonnet-4-5")`.

---

## Example backend-chat (`example/backend-chat/`)

The canonical end-to-end reference. Wires everything in `main.go` + helpers:

| File | Role |
|------|------|
| `main.go`              | HTTP + SSE on `:9090`, SQLite, Centrifugo, R2; `classifyStreamError` |
| `llm_factory.go`       | `RoutedLLMProvider`: anthropic + openai + ollama + Echo fallback |
| `mode_provider.go`     | `newModeEngine`: assembles Engine + Runtime per chat |
| `runner_runtime.go`    | `agentRuntime`, `buildToolRegistry()` (with/without sandbox) |
| `sandbox_provider.go`  | `sandboxManager` singleton; tools: `bash` / `code_interpreter` / `file_read` / `file_write` |
| `memory_provider.go`   | Wires the filesystem memdir under `./memory/` |
| `thread_provider.go`   | SQLite ThreadProvider |
| `interrupt_registry.go`| HIL adapter for SSE interrupts |
| `webhook_handlers.go`  | Async event ingest |

DB: `chat.db` (SQLite). **Never run two backend processes against the same DB**
— SQLite WAL doesn't tolerate two writers and the file will corrupt. Recovery:
`cp chat.db chat.db.bak && sqlite3 chat.db.bak .recover > recovered.sql && rm chat.db && sqlite3 chat.db < recovered.sql`.

### Env vars

| Variable | Purpose |
|----------|---------|
| `ANTHROPIC_API_KEY`           | Anthropic key |
| `OPENAI_API_KEY` / `OPENAI_BASE_URL` | OpenAI-compatible endpoint |
| `OLLAMA_BASE_URL`             | Ollama (default `http://localhost:11434`) |
| `BACKEND_MODEL`               | Default model ref (e.g. `anthropic/claude-sonnet-4-5`) |
| `OPEN_SANDBOX_API_KEY` / `OPEN_SANDBOX_DOMAIN` / `OPEN_SANDBOX_PROTOCOL` / `OPEN_SANDBOX_TTL_SECONDS` | OpenSandbox |
| `CENTRIFUGO_API_URL` / `CENTRIFUGO_API_KEY` | Real-time pub/sub |

---

## Frontend chat-app (`example/chat-app/`)

React + Vite, port `:3000`.

- `features/chat/types.ts`           — wire types (mirrors generated `events.ts`)
- `features/chat/api.ts`             — `adaptSSEEvent` parses SSE → `StreamEvent`
- `components/chat-main/chat-main.tsx` — `handleStreamEvent` consumes events
  - Includes `case "error"` that:
    - sets a dismissible red banner via `streamError` state
    - replaces the empty assistant bubble with `⚠️ <message>`
  - Plus `case "compaction"`, `case "plan_mode_changed"`, etc.

The `StreamEvent` discriminated union uses a `done` literal as last variant —
TypeScript will mark `case "error"` after `case "done"` as unreachable. Put
`error` **before** `done`.

---

## TS clients & code generation

```bash
make client              # tygo + gen-events
make client-types-check  # CI guard — fails on diff
```

Sources of truth:
- `sdk/*.go` → `clients/harness-client/src/generated/types.ts` via tygo (`clients/tygo.yaml`)
- `sdk/streaming.go` → `clients/harness-client/src/generated/events.ts` via `scripts/gen-events`

Always commit regenerated files alongside Go changes that touch exported
streaming/protocol types — CI fails otherwise.

React entrypoint:

```ts
useHarness({ baseUrl, chatId }) → { session, events, lastEvent, send, on }
useArtifacts(session) → Artifact[]
useInterrupts(session) → { requests, respond }
```

---

## Build & test

```bash
# SDK
cd sdk && go build ./... && go test ./...

# example backend
cd example/backend-chat && go build ./... && go run .

# example frontend
cd example/chat-app && pnpm install && pnpm exec tsc --noEmit && pnpm dev
```

The repo uses `go.work`. The SDK and `example/backend-chat` are **separate
modules**; `go build ./...` from the repo root won't work.

---

## Lessons (recurring)

- **SQLite single-writer** — never run two backends against the same `chat.db`.
- **Stream consumers must handle every event type** — silent hangs come from
  missing branches in `handleStreamEvent`.
- **Always emit a terminal `done`** even on error so the client stops waiting.
- **Memory tool: read-first** — `view` before any write; never shell out via
  `bash`; never create with empty `path`.
- **TS discriminated unions:** `error` must come before `done` in the switch
  or TS marks it unreachable.
- **`error` SSE payload** should be `{error, category, detail}` — frontend
  surfaces `category` as a badge and shows `detail` under a `<details>`.
