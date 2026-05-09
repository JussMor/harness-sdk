# harness-sdk — Análisis del SDK

SDK para construir agentes de IA al estilo Claude. Módulo Go: `github.com/everfaz/autobuild-sdk` · Versión: `0.2.0` · Go 1.22+

---

## Estructura del repositorio

```
harness-sdk/
├── sdk/                          # SDK Go core
│   ├── *.go                      # Abstracciones e interfaces
│   └── providers/                # Implementaciones concretas
│       ├── llm/                  # anthropic.go, openai.go, ollama.go
│       ├── memory/               # filesystem.go
│       ├── sandbox/              # opensandbox.go, local.go
│       ├── skills/               # memory.go
│       ├── thread/               # sqlite.go, postgres.go, memory.go
│       ├── tokenizers/           # tiktoken.go, byte.go, auto.go
│       └── embedders/            # voyage.go, local.go
├── example/
│   ├── backend-chat/             # Servidor Go completo (puerto :9090)
│   └── chat-app/                 # Frontend React/Vite (puerto :3000)
├── clients/
│   ├── harness-client/           # Cliente TypeScript
│   └── harness-react/            # Hooks React (useHarness, useArtifacts, useInterrupts)
└── scripts/gen-events/           # Generador de código (tygo)
```

---

## Engine — punto de composición (`engine.go:20`)

Todos los campos son opcionales (opt-in).

```go
type Engine struct {
  Memory       MemoryProvider
  Sandbox      SandboxDriver
  Tools        *ToolRegistry
  Skills       SkillProvider
  Threads      ThreadProvider
  Checkpoints  CheckpointProvider
  Modes        ModeProvider
  Events       EventBus
  LLM          LLMProvider
  Execution    ExecutionContext      // fase + plan + todos
  Observations ObservationStore     // memoria de trabajo (sesión)
  Prompt       *SystemPromptBuilder // prompt en capas
  Budget       *ContextBudget       // límites de tokens
}

New(opts ...Option) *Engine
NewWithDefaults(windowSize int) *Engine  // in-memory, presupuesto automático
```

---

## Ciclo de vida — 6 fases (`execution_context.go:10`)

```
0: PhaseOrientation  → leer memoria, cargar skills, construir prompt
1: PhaseAlignment    → proponer plan si la tarea es compleja
2: PhasePreparation  → checkpoint, budget enforcement, compaction
3: PhaseExecution    → loop LLM ↔ tools (agent_loop.go)
4: PhaseVerification → verificar calidad del output, retry si falla
5: PhaseClosure      → escribir memoria, expirar observaciones
```

`ExecutionContext` interface: `Phase()`, `Advance()`, `SetPhase()`, `Propose()`, `Approve()`, `ActivePlan()`, `Todos()`, `MarkDone()`

---

## Runtime — orquestador (`runtime.go:34`)

```go
Runtime.Run(ctx, conv, userMessage) (*RuntimeResult, error)
Runtime.RunStream(ctx, conv, userMessage) <-chan StreamEvent

// 22 opciones encadenables:
NewRuntime(engine).
  WithMode(modeID).
  WithMemoryRoots(roots...).
  WithMaxMemoryTokens(8_000).
  WithSafety(chain).
  WithOutputFilter(chain).
  WithVerification(chain).
  WithMaxVerifyRetry(1).
  WithCompactor(&EpisodicCompactor{}).
  WithPlanner(&LLMPlanner{}).
  WithAutoApprovePlan(true).
  WithThinkingBudget(n)
```

`RuntimeResult` contiene: `Response`, `Turns`, `Usage`, `StopReason`, `SkillsLoaded`, `MemoryWritten`, `Trace`, `PlanProposed`, `WellbeingSignal`, `VerificationVerdict`

---

## AgentLoop — loop LLM↔tool (`agent_loop.go:132`)

Loop de bajo nivel sin dependencias de Engine. Útil para casos custom.

```go
RunAgentLoop(ctx, AgentLoopConfig, messages) (*AgentLoopResult, error)
RunAgentLoopWithEngine(ctx, engine, modeID, cfg, messages) (*AgentLoopResult, error)

AgentLoopConfig{
  Provider, SystemPrompt, Model, Tools, Sandbox, SandboxID, Events,
  MaxTurns, MaxRetries,
  OnTurn, OnToolCall, OnToolResult, ShouldStop, OnError, BuildRequest,
}
```

- Retry exponencial: 1s → 2s → 4s → 8s → 16s → 30s (cap)
- Transient errors: 429, 5xx, timeout, EOF
- Permanent errors: 401, 403, 400, context_length_exceeded

---

## Interfaces de providers

### LLMProvider (`llm.go:178`)
```go
interface LLMProvider {
  Chat(ctx, ChatRequest) (*ChatResponse, error)
}
interface StreamingLLMProvider {
  ChatStream(ctx, ChatRequest) (<-chan StreamEvent, error)
}
// Multi-modelo:
RoutedLLMProvider → ParseModelRef("anthropic/claude-sonnet") → ("anthropic", "claude-sonnet")
```
Providers: `providers/llm/anthropic.go`, `openai.go` (compatible con Groq/Together/Mistral), `ollama.go`

### MemoryProvider (`memory.go:25`)
```go
interface MemoryProvider {
  View / Create / StrReplace / Delete / Rename / List / Search
}
// Scopes: ScopeUser (cross-project), ScopeProject, ScopeSession
// Layers: MemoryLayerExplicit > MemoryLayerInferred > MemoryLayerSession
// DefaultMemoryRoots: /profile, /facts (user) + / (project)
```
Impl: `providers/memory/filesystem.go` — archivos en `{root}/user/` y `{root}/project/`

### SandboxDriver (`sandbox.go:7`)
```go
interface SandboxDriver {
  Create(ctx, SandboxConfig) (id, error)
  Exec(ctx, id, command) (ExecResult, error)
  ExecStream(ctx, id, command) (<-chan ExecOutput, error)  // nil = no soportado
  WriteFile / ReadFile / Destroy / Status / IP
}
SandboxConfig{ Image, DefaultCwd, Env, Labels, Volumes []Volume }
Volume{ Name, MountPath, ReadOnly, PVC *PVCVolumeSource, SubPath }
```
- `providers/sandbox/opensandbox.go` → `OpenSandboxDriver` (server local/remoto en :8080)
- `providers/sandbox/local.go` → `LocalSandbox` (tmpdir en host, **solo dev/test**)

### SkillProvider (`skill.go:129`)
```go
interface SkillProvider { Load / Unload / Loaded / Match / List / Get }
Skill{ Meta (YAML frontmatter), Triggers, GrantedTools, Content (markdown) }
MatchScore(text) → 0–1 por trigger hits + especificidad
SkillReloader (skill_reload.go) → polling SHA-1 cada 5s, auto-reload
```

### ToolRegistry (`tool.go:86`)
```go
Tool{ Name, Description, Category, Parameters, Execute func(ctx,sandboxID,args), Hidden }
ToolRegistry: Register / Get / List / ByCategory / Search / Reveal / Hide / ToolDefs
ToolDispatcher: Dispatch / DispatchAll / DispatchParallel / AreIndependent
```
Categorías: `Workspace`, `Compute`, `Data`, `Web`, `Planning`, `Comm`, `Integrations`, `Memory`, `Custom`

### ThreadProvider (`thread.go:39`)
```go
interface ThreadProvider { Create / Get / Archive / SendMessage }
interface MultiUserThreadProvider extends ThreadProvider {
  CreateForUser / GetForUser / ListByUser
}
Thread{ ID, UserID, ProjectID, ModeID, Status, ParentID }
```
Impls: `providers/thread/sqlite.go`, `postgres.go`, `memory.go`

### ModeProvider (`mode.go:120`)
```go
Mode{ ID, Name, BaseModeID, PromptContent, ModelSettings, ToolsMode, ToolsList }
Mode.IsToolAllowed(name) bool
BaseModes: balanced | analyst | deep_work
```

---

## Sistemas auxiliares

### SystemPromptBuilder (`system_prompt.go:74`)
6 capas en orden: `Core → Behavior → Memory → Skills → Session → Mode`
```go
Build() / BuildWithBudget(tok) / Set(layer, content) / Append / Clear / Get
SetMaxLayerTokens(layer, n)
```

### ContextBudget (`context_budget.go:16`)
```
DefaultContextBudget(windowSize) → 10% skills · 15% memory · 60% history · 15% reserve
Enforce() → evict skills LRU + truncate history + EnforcementResult
```

### Compaction (`compaction.go`)
| Impl | Estrategia |
|------|-----------|
| `LLMCompactor` | LLM resume mensajes eliminados (≤200 palabras) |
| `BulletCompactor` | Heurístico offline: últimas 3 respuestas (≤600 chars) |
| `EpisodicCompactor` | Scoring 0–1 por importancia + LLM con transcripción scored |

### Safety (`safety.go`)
```go
// Pre-dispatch:
SafetyFilter → SafetyVerdict{Allow/Block/Transform}
DangerousCommandFilter → bloquea: rm -rf /, dd, mkfs, fork bombs, curl|sh
SecretLeakFilter → bloquea: sk-ant-, ghp_, AKIA, xoxb-...
SafetyChain → primer Block gana, Transforms se acumulan

// Post-generación:
OutputFilter → OutputVerdict{Allow/Block/Transform}
SecretRedactionFilter → [REDACTED]
MaxLengthFilter / DisclaimerFilter

// Bienestar:
DefaultWellbeingDetector → keywords EN+ES (crisis, self-harm, ED)
WellbeingSeverity: None | Low | Medium | High
```

### Human-in-the-Loop (`human_in_loop.go`, `interrupt.go`)
```go
// Moderno:
InterruptKind: Approval | Question | FormInput
InterruptGate.Wait(ctx, req) / Respond(resp)
IssueResolutionToken(id, ttl) → base64url(id||expiry).HMAC-SHA256

// Legacy facade:
ApprovalGate → facade sobre InterruptGate
HumanApprovalFilter → pausa para aprobación (bash/file_write/file_delete por defecto)
```

### Streaming (`streaming.go`)
```go
StreamEvent.Type: delta | thinking | tool_call | tool_result | plan_proposed |
                  interrupt_required | artifact_created | subagent_result | done | error
FanOutStream(ch) → broadcast a múltiples consumidores
CollectStream(ch) → drain a AgentLoopResult completo
```

### Artifacts (`artifact.go`)
```go
ArtifactKind: File | Component
ArtifactPlacement: Canvas | Inline
EmitArtifact(ctx, artifact) → adjuntar a turn de streaming
RequestInterrupt(ctx, req) → pausar para input de usuario (form/pregunta)
```

### Subagents (`subagent.go`)
```go
Subagent{ Task, Engine, MaxTurns(10), Timeout(60s), AllowMemoryWrites(false) }
Run(ctx) → SubagentResult{ Output, Turns, Usage, StopReason, Trace }
SendFollowUp(ctx, message) → conversación persistente
RunSubagentsInParallel(ctx, agents) → resultados en orden de entrada
```

### Observaciones (`observation.go`)
```go
ObservationStore: Record / Relevant(query, limit) / All / Expire / Clear
// Solo scope sesión. Se expiran en PhaseClosure.
// InMemoryObservationStore: keyword matching ponderado por campo Relevance
```

### Verificación (`verification.go`)
```go
VerificationStrategy.Verify(ctx, result, conv) → Verdict{Pass, Retry, Reason}
Impls: NoOpVerification | CompletionVerification | LocalVerification |
       IntrinsicVerification | CriteriaVerification (LLM) | VerificationChain
```

### EventBus (`event.go`)
```go
EventBus: Publish / Subscribe(eventType, fn) → *Subscription / Unsubscribe
// Tipos: AgentLoop*, PhaseAdvanced, Plan*, SafetyBlocked,
//        Verification*, MemoryWritten, Subagent*
```

### Plan/Planner (`plan.go`, `alignment.go`)
```go
Plan{ Executables []Executable, Approved, AutoApprove }
Executable.Status: Planned → Queued → InProgress → Completed|Failed|Blocked|Cancelled
Plan.NextReady() → listos (sin dependencias pendientes)
Planner: HeuristicPlanner (keywords) | LLMPlanner (call extra)
```

---

## Cómo se ensambla en backend-chat

`example/backend-chat/` es el ejemplo canónico de uso completo.

```
main.go           → HTTP en :9090, SQLite, Centrifugo, R2
llm_factory.go    → RoutedLLMProvider: anthropic + openai + ollama + EchoLLM (fallback)
mode_provider.go  → newModeEngine(): ensambla Engine + Runtime por chat
runner_runtime.go → agentRuntime, buildToolRegistry() (con/sin sandbox)
sandbox_provider.go → sandboxManager singleton, tools: bash/code_interpreter/file_read/file_write
```

**Ensamblaje del Engine** (`mode_provider.go:70`):
```go
engine := ab.NewWithDefaults(128_000)
engine.LLM = provider
engine.Skills = skills
engine.Memory = memory
// ... tools, threads, checkpoints, modes, prompt layers
```

**Sandbox** (`sandbox_provider.go:31`):
- `isSandboxAvailable()` → chequea `OPEN_SANDBOX_API_KEY`
- `OpenSandboxDriver` para el servidor en `localhost:8080`
- `LocalSandbox` para dev sin servidor

---

## Dependencias Go (`sdk/go.mod`)

| Paquete | Uso |
|---------|-----|
| `github.com/alibaba/OpenSandbox/sdks/sandbox/go v1.0.0` | Driver sandbox remoto |
| `github.com/dlclark/regexp2 v1.9.0` | Regex avanzado (UTF-16, .NET flavor) |
| `github.com/mattn/go-sqlite3 v1.14.44` | Persistencia local (threads, conversations) |
| `github.com/tiktoken-go/tokenizer v0.3.0` | Conteo de tokens BPE |

---

## Variables de entorno relevantes (backend-chat)

| Variable | Descripción |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Clave Anthropic |
| `OPENAI_API_KEY` | Clave OpenAI |
| `OPENAI_BASE_URL` | Base URL compatible OpenAI |
| `OLLAMA_BASE_URL` | Ollama (default localhost:11434) |
| `BACKEND_MODEL` | Modelo por defecto |
| `OPEN_SANDBOX_API_KEY` | Activa sandbox tools |
| `OPEN_SANDBOX_DOMAIN` | Host del servidor sandbox |
| `OPEN_SANDBOX_PROTOCOL` | `http` o `https` |
| `OPEN_SANDBOX_TTL_SECONDS` | TTL del sandbox (default 21600) |
| `CENTRIFUGO_API_URL` | Pub/sub realtime |
| `CENTRIFUGO_API_KEY` | Auth Centrifugo |

---

## Clients TypeScript

**harness-client** (`clients/harness-client/`):
```ts
connect(options: ConnectOptions): HarnessSession
HarnessSession.on<T>(type, handler) → unsubscribe
HarnessSession.resolveInterrupt(chatId, id, response)
HarnessSession.done() → Promise<void>
```

**harness-react** (`clients/harness-react/`):
```ts
useHarness(options) → { session, events, lastEvent, error, done, send, on }
useArtifacts(session) → Artifact[]
useInterrupts(session) → { requests, respond }
```

Tipos generados automáticamente desde Go via `tygo` (`clients/tygo.yaml`) → `make client-types`
