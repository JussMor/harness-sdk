# SDK Reference — File-by-File Documentation

Every file in `sdk/` and `sdk/providers/` explained: what it does, the types
it exposes, and how they connect to the rest of the system.

---

## Core files (`sdk/*.go`)

---

### `engine.go`

The central dependency container. An Engine holds all provider references
but has no logic of its own — it is the wiring board.

```go
type Engine struct {
    LLM          LLMProvider
    Memory       MemoryProvider
    Skills       SkillProvider
    Tools        *ToolRegistry
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

**Constructors:**

- `New(opts ...Option) *Engine` — builds with functional options
- `NewWithDefaults(windowSize int) *Engine` — builds with sensible defaults
  (SystemPromptBuilder, ContextBudget, InMemoryObservationStore)

**Presence checks:** `HasMemory()`, `HasSandbox()`, `HasTools()`, `HasSkills()`,
`HasThreads()`, `HasCheckpoints()`, `HasModes()`, `HasEvents()`, `HasLLM()`,
`HasExecution()`, `HasObservations()`, `HasPrompt()`, `HasBudget()`

---

### `options.go`

Functional options for building an Engine. Use these with `New()`.

```go
WithMemory(MemoryProvider)
WithSandbox(SandboxDriver)
WithToolRegistry(*ToolRegistry)
WithSkills(SkillProvider)
WithThreads(ThreadProvider)
WithCheckpoints(CheckpointProvider)
WithModes(ModeProvider)
WithEventBus(EventBus)
WithLLM(LLMProvider)
WithExecution(ExecutionContext)
WithObservations(ObservationStore)
WithPrompt(*SystemPromptBuilder)
WithBudget(*ContextBudget)
```

---

### `runtime.go`

The orchestrator — the most important file in the SDK. Runs the 6-phase
lifecycle over an Engine for every conversation turn.

**The 6 phases:**
```
Orientation  → read memory + skills (cold turns only)
Alignment    → detect planning need, propose plan
Preparation  → checkpoint before mutation
Execution    → agent loop (LLM ↔ tools)
Verification → validate response, retry if needed
Closure      → write memory, clear session observations
```

**Constructor:**
```go
NewRuntime(engine *Engine) *Runtime
```

**All `With*` configuration methods:**

| Method | What it does |
|---|---|
| `WithMode(modeID)` | Activate a specific mode from the ModeProvider |
| `WithSkillThreshold(float64)` | Min SkillMatch.Score to load a skill |
| `WithMaxSkills(n)` | Cap loaded skills per turn |
| `WithObservationFilter(fn)` | What tool results become observations |
| `WithMemoryTrigger(fn)` | Detect explicit memory write intent in messages |
| `WithWellbeing(WellbeingDetector)` | Pre-flight wellness check |
| `WithVerification(VerificationStrategy)` | Validate each response |
| `WithMaxVerifyRetry(n)` | Max retries when verification fails |
| `WithSafety(SafetyFilter)` | Inspect tool calls before execution |
| `WithOutputFilter(OutputFilter)` | Filter final response text |
| `WithMemoryWriter(*InferredMemoryWriter)` | Extract facts post-turn |
| `WithTokenizer(Tokenizer)` | Token counting for budget enforcement |
| `WithConversationStore(ConversationStore)` | Persist conversations |
| `WithPlanner(Planner)` | Drive alignment phase |
| `WithAutoApprovePlan(bool)` | Skip plan approval step |
| `WithSessionContext(SessionContextProvider)` | Inject time/context each turn |
| `WithCompactor(Compactor)` | Summarize dropped history |
| `WithMaxMemoryTokens(n)` | Cap memory injection by token count |
| `WithMemoryRoots(...MemoryRoot)` | Which dirs to read at orientation |
| `WithThinkingBudget(n)` | Enable extended thinking (Claude 3.7+) |

**Entry points:**
```go
Run(ctx, *Conversation, userMessage string) (*RuntimeResult, error)
RunStream(ctx, *Conversation, userMessage string) (<-chan StreamEvent, error)
```

**Result:**
```go
type RuntimeResult struct {
    Response      string
    ThinkingContent string   // extended thinking (if enabled)
    MemoryRead    bool
    MemoryWritten []string   // paths written
    InferredFacts []InferredFact
    SkillsLoaded  []string
    PlanProposed  bool
    RunID         string
    Phase         Phase
    TotalUsage    TokenUsage
}
```

---

### `agent_loop.go`

The inner execution loop — stateless, provider-agnostic. Calls the LLM,
dispatches tool calls, feeds results back, repeats until `stop_reason != tool_calls`.

**Config:**
```go
type AgentLoopConfig struct {
    SystemPrompt string
    Tools        *ToolRegistry
    MaxTurns     int            // default 50
    OnTurn       func(turn int, msgs []ChatMessage)
    OnToolCall   func(call ToolCallEntry) bool  // return false to block
    OnToolResult func(call ToolCallEntry, result ToolResult) ToolResult
    ShouldStop   func(result *AgentLoopResult) bool
    BuildRequest func(systemPrompt string, messages []ChatMessage, tools *ToolRegistry) ChatRequest
    OnError      func(err error, attempt int) bool  // return true to retry
    MaxRetries   int
}
```

**Entry points:**
```go
RunAgentLoop(ctx, llm LLMProvider, cfg AgentLoopConfig, messages []ChatMessage) (*AgentLoopResult, error)
RunAgentLoopWithEngine(ctx, *Engine, modeID string, cfg AgentLoopConfig, messages []ChatMessage) (*AgentLoopResult, error)
DispatchParallel(ctx, calls []ToolCallEntry, dispatcher *ToolDispatcher) []ToolResult
```

**Result:**
```go
type AgentLoopResult struct {
    FinalContent string
    ToolCalls    []ToolCallEntry
    ToolResults  []ToolResult
    Turns        int
    StopReason   string   // "complete", "max_turns", "aborted", "error"
    TotalUsage   TokenUsage
    ReasoningTrace []string
    ThinkingContent string
}
```

---

### `llm.go`

Core LLM interfaces and types.

```go
type LLMProvider interface {
    Chat(ctx, ChatRequest) (*ChatResponse, error)
}

type StreamingLLMProvider interface {
    LLMProvider
    ChatStream(ctx, ChatRequest) (<-chan StreamEvent, error)
}

type ChatRequest struct {
    Model          string
    Messages       []ChatMessage
    Tools          []ToolDef
    Temperature    float64
    MaxTokens      int
    ReasoningEffort string      // "low"/"medium"/"high"
    ThinkingBudget int          // >0 enables extended thinking
    Stop           []string
}

type ChatResponse struct {
    Content         string
    Reasoning       string
    ThinkingContent string      // extended thinking blocks
    ToolCalls       []ToolCallEntry
    FinishReason    string
    Usage           TokenUsage
}

type ChatMessage struct {
    Role       Role
    Content    string
    Name       string
    ToolCallID string
    ToolCalls  []ToolCallEntry
    Images     []ImageContent   // vision
}

type ImageContent struct {
    Source    string   // base64 data
    MediaType string   // "image/jpeg", "image/png", etc.
    URL       string   // alternative to base64
}
```

---

### `streaming.go`

SSE streaming infrastructure.

```go
type StreamEvent struct {
    Type     StreamEventType
    Delta    string           // text chunk (StreamEventDelta)
    Thinking string           // thinking chunk (StreamEventThinking)
    ToolCall *ToolCallEntry   // StreamEventToolCall
    ToolResult *ToolResult    // StreamEventToolResult
    Final    *AgentLoopResult // StreamEventDone
    Error    error            // StreamEventError
}

// Event types:
StreamEventDelta        = "delta"
StreamEventThinking     = "thinking"    // extended thinking
StreamEventToolCall     = "tool_call"
StreamEventToolResult   = "tool_result"
StreamEventTurnComplete = "turn_complete"
StreamEventDone         = "done"
StreamEventError        = "error"
```

**Utilities:**
```go
FanOutStream(source <-chan StreamEvent, n int) []<-chan StreamEvent
CollectStream(ctx, source <-chan StreamEvent) (*AgentLoopResult, error)
```

---

### `tool.go`

Tool registry and type definitions.

```go
type Tool struct {
    Name        string
    Description string
    Category    ToolCategory
    Parameters  ToolFuncParams
    Execute     func(ctx, sandboxID string, args map[string]any) (string, error)
}

type ToolRegistry struct { /* ... */ }
func NewToolRegistry() *ToolRegistry
func (r *ToolRegistry) Register(*Tool)
func (r *ToolRegistry) Get(name string) (*Tool, bool)
func (r *ToolRegistry) ListEnabled() []*Tool
func (r *ToolRegistry) Filter(modeID string) *ToolRegistry
func (r *ToolRegistry) AsToolDefs() []ToolDef

// Categories:
ToolCategoryMemory    = "memory"
ToolCategoryWorkspace = "workspace"
ToolCategorySkills    = "skills"
ToolCategoryPlanning  = "planning"
```

---

### `dispatch.go`

Resolves tool calls from the LLM into executions.

```go
type ToolDispatcher struct { /* ... */ }
func NewToolDispatcher(tools *ToolRegistry, sandbox SandboxDriver) *ToolDispatcher
func (d *ToolDispatcher) Dispatch(ctx, call ToolCallEntry) ToolResult
func (d *ToolDispatcher) AreIndependent(a, b ToolCallEntry) bool
```

---

### `memory.go`

Memory provider interface and key types.

```go
type MemoryProvider interface {
    View(ctx, scope Scope, path string) (string, error)
    Create(ctx, scope Scope, path, content string) error
    StrReplace(ctx, scope Scope, path, oldStr, newStr string) error
    Delete(ctx, scope Scope, path string) error
    Rename(ctx, scope Scope, oldPath, newPath string) error
    List(ctx, scope Scope, path string) ([]string, error)
    Search(ctx, scope Scope, query string) ([]MemoryEntry, error)
}

type MemoryEntry struct {
    Path      string
    Scope     Scope
    Content   string
    Layer     MemoryLayer   // Explicit / Inferred / Session
    Confidence float64
    Source    string        // "user", "inferred", "tool"
    UpdatedAt int64
}

type MemoryRoot struct {
    Scope Scope
    Path  string
    Label string  // injected as "## Label" in LayerMemory
}

// DefaultMemoryRoots reads:
//   user/profile  → "## User profile & preferences"
//   user/facts    → "## Remembered facts"
//   project/      → "## Project context"
var DefaultMemoryRoots []MemoryRoot

// Scopes:
ScopeUser    Scope = "user"
ScopeProject Scope = "project"
```

---

### `memory_layer.go`

Layer priority system for memory. Explicit entries override Inferred,
which override Session.

```go
type MemoryLayer string

const (
    MemoryLayerExplicit MemoryLayer = "explicit"  // user said "remember that"
    MemoryLayerInferred MemoryLayer = "inferred"  // LLM extracted a fact
    MemoryLayerSession  MemoryLayer = "session"   // ephemeral, cleared each turn
)

type LayeredMemoryEntry struct {
    MemoryEntry
    Layer    MemoryLayer
    Priority int
}

type LayeredMemoryProvider interface {
    MemoryProvider
    WriteLayered(ctx, scope Scope, path, content string, layer MemoryLayer) error
    ReadLayered(ctx, scope Scope, path string) (*LayeredMemoryEntry, error)
    SearchLayered(ctx, scope Scope, query string) ([]LayeredMemoryEntry, error)
    ClearSession(ctx) error
}

func SortByPriority(entries []LayeredMemoryEntry)
```

---

### `closure.go`

Extracts persistent facts from a conversation turn and writes them to memory.

```go
type InferredMemoryWriter struct {
    Provider        LLMProvider
    Model           string
    MaxFacts        int        // max facts to extract per turn (default 3)
    MinConfidence   float64    // 0-1, reject low-confidence facts (default 0.75)
    DedupeThreshold float64    // Dice similarity threshold (default 0.6)
}

// Extract asks the LLM to identify persistent facts from the turn.
func (w *InferredMemoryWriter) Extract(ctx, *Conversation, lastResponse string) ([]InferredFact, error)

// WriteWithDedup writes facts, skipping or merging entries that are too
// similar to existing memory (Dice coefficient >= DedupeThreshold).
func (w *InferredMemoryWriter) WriteWithDedup(ctx, MemoryProvider, []InferredFact) ([]InferredFact, error)

type InferredFact struct {
    Content    string
    Confidence float64
    Path       string   // where it was written
    Merged     bool     // true if an existing entry was updated
    Layer      MemoryLayer
}
```

---

### `observation.go`

Ephemeral turn-scoped context that persists within a session but clears
at the end of each turn.

```go
type Observation struct {
    Source    string    // "tool_result", "user_message", etc.
    Content   string
    Relevance float64
    CreatedAt time.Time
}

type ObservationStore interface {
    Record(ctx, Observation) error
    Retrieve(ctx, query string, limit int) ([]Observation, error)
    Expire(ctx) error  // called at closure — clears session data
    Recent(ctx, limit int) ([]Observation, error)
}

func NewInMemoryObservationStore() *InMemoryObservationStore
var DefaultObservationFilter ObservationFilter  // records tool results as observations
```

---

### `skill.go`

Skill loading and matching.

```go
type SkillMeta struct {
    Name        string
    Version     string
    Description string
    Category    string
    Triggers    []string
    Author      string
    Created     string
    Requires    []string   // dependency skill IDs (loaded automatically)
}

type Skill struct {
    Meta    SkillMeta
    Content string   // the full skill prompt injected into LayerSkills
}

func (s *Skill) MatchesTrigger(text string) bool
func (s *Skill) MatchScore(text string) float64

type SkillProvider interface {
    Match(ctx, userMessage string) ([]SkillMatch, error)
    Get(ctx, skillID string) (*Skill, error)
    Load(ctx, skillID string) error
    Unload(ctx, skillID string) error
    List(ctx) ([]*Skill, error)
}
```

---

### `skill_reload.go`

Hot reload for skills — watches a directory for SKILL.md changes.

```go
type SkillReloader struct { /* ... */ }

func NewSkillReloader(dir string, provider ReloadableSkillProvider) *SkillReloader
func (r *SkillReloader) SetPollInterval(d time.Duration)  // default 5s
func (r *SkillReloader) SetOnReload(fn func(loaded, removed []string))
func (r *SkillReloader) Start(ctx context.Context)
func (r *SkillReloader) Stop()

type ReloadableSkillProvider interface {
    SkillProvider
    AddOrReplace(skill *Skill)
    Remove(skillID string)
}
```

Uses mtime polling — no external dependencies. Detects added, changed, and
removed SKILL.md files within the watched directory.

---

### `loader.go`

Parses SKILL.md files from disk.

```go
func LoadSkillsDir(dir string) ([]*Skill, error)
func ParseSkillMarkdown(content string) (*Skill, error)
```

SKILL.md format:
```markdown
# Skill Name
**Version:** 1.0.0
**Triggers:** keyword1, keyword2
**Author:** team
**Description:** What this skill does.
---
The skill prompt injected into LayerSkills...
```

---

### `safety.go`

Input and output filtering for tool calls and responses.

```go
// Input: inspect tool calls before execution
type SafetyFilter interface {
    Inspect(ctx, ToolCallEntry) SafetyVerdict
}
type SafetyVerdict struct {
    Decision SafetyDecision  // Allow / Block / Transform
    Reason   string
    Modified *ToolCallEntry  // used when Decision = Transform
}

// Implementations:
func DefaultDangerousCommandFilter() *DangerousCommandFilter  // blocks rm -rf, dd, etc.
func DefaultSecretLeakFilter() *SecretLeakFilter              // blocks API keys in args

func NewSafetyChain(filters ...SafetyFilter) *SafetyChain     // runs filters in order

// Output: filter response text
type OutputFilter interface {
    Filter(ctx, text string) (string, error)
}

func DefaultSecretRedactionFilter() *SecretRedactionFilter    // redacts keys in output
func NewOutputFilterChain(filters ...OutputFilter) *OutputFilterChain
```

---

### `verification.go`

Validates LLM responses after generation. Failed verdicts trigger retries.

```go
type VerificationStrategy interface {
    Verify(ctx, *AgentLoopResult, *Conversation) Verdict
}

type Verdict struct {
    Pass   bool
    Reason string
}

// Strategies:
NoOpVerification{}                           // always passes
CompletionVerification{MinLength int}         // checks stop_reason + length
LocalVerification{                            // rules-based, 0 LLM calls
    MinLength        int
    MaxLength        int
    MustContain      []string
    MustNotContain   []string
    MustNotBeEmpty   bool
    NoHallucination  bool                     // rejects "as an AI..." responses
}
IntrinsicVerification{MinMarkers int}         // detects self-check markers EN+ES
CriteriaVerification{                         // LLM judge (1 extra call)
    Provider LLMProvider
    Model    string
    Criteria string
}
VerificationChain{Strategies []VerificationStrategy}  // first failure wins
```

---

### `alignment.go`

Phase 1 — decides if a plan is needed and proposes one.

```go
type Planner interface {
    ShouldPlan(ctx, userMessage string, conv *Conversation) (bool, error)
    Propose(ctx, userMessage string, conv *Conversation) (*Plan, error)
}

// Implementations:
type HeuristicPlanner struct {
    Triggers []string   // keywords that imply planning need
}
type LLMPlanner struct {
    Provider        LLMProvider
    Model           string
    MaxExecutables  int
}
```

---

### `plan.go`

Plan and task types used by the alignment phase.

```go
type Plan struct {
    ID          string
    Goal        string
    Tasks       []Task
    ProposedAt  time.Time
    ApprovedAt  *time.Time
}

type Task struct {
    ID          string
    Title       string
    Description string
    Status      TaskStatus  // pending / running / done / failed
    DependsOn   []string    // task IDs that must complete first
}

type ExecutionContext interface {
    Propose(ctx, *Plan) error
    Approve(ctx, planID string) error
    Advance(ctx, taskID string, status TaskStatus) error
    Current(ctx) (*Plan, error)
    Complete(ctx, planID string) error
}

func NewExecutionContext() ExecutionContext
```

---

### `compaction.go`

Summarizes dropped conversation history to avoid losing context.

```go
type Compactor interface {
    Compact(ctx, droppedMessages []ChatMessage) (string, error)
}

// Implementations:
type BulletCompactor struct {
    MaxChars int   // truncates to N chars
}
type LLMCompactor struct {
    Provider LLMProvider
    Model    string
}
type EpisodicCompactor struct {
    Provider LLMProvider
    Model    string
    MaxWords int   // default 400
    // Identifies and preserves key moments (decisions, errors, breakthroughs)
    // verbatim; summarizes the rest.
}
```

---

### `context_budget.go`

Token budgeting to prevent context overflow.

```go
type ContextBudget struct {
    TotalTokens   int
    SkillBudget   int
    MemoryBudget  int
    HistoryBudget int
    SystemBudget  int
    ResponseBuffer int
}

func DefaultContextBudget(windowSize int) *ContextBudget
func (b *ContextBudget) WouldOverflow(skillTokens, memoryTokens, historyTokens int) bool
func (b *ContextBudget) Enforce(conv *Conversation, tokenizer Tokenizer) int  // returns dropped count
```

---

### `conversation.go`

The stateful object that accumulates a multi-turn conversation.

```go
type Conversation struct {
    ID            string
    Messages      []ChatMessage
    LoadedSkills  []*Skill
    MemoryRead    bool
    Phase         Phase
    // ...
}

func NewConversation(id string) *Conversation
func (c *Conversation) AppendUser(content string)
func (c *Conversation) AppendAssistant(content string)
func (c *Conversation) AppendTool(callID, name, result string)
func (c *Conversation) IsCold() bool   // true on first turn
func (c *Conversation) Clone() *Conversation
```

---

### `conversation_store.go`

Persists conversations across process restarts.

```go
type ConversationStore interface {
    Save(ctx, *Conversation) error
    Load(ctx, id string) (*Conversation, error)
    Delete(ctx, id string) error
    List(ctx) ([]string, error)
}

func NewInMemoryConversationStore() *InMemoryConversationStore
```

---

### `system_prompt.go`

Builds the system prompt from 6 ordered layers.

```go
type SystemPromptBuilder struct { /* ... */ }

func NewSystemPromptBuilder() *SystemPromptBuilder
func (b *SystemPromptBuilder) Set(layer Layer, content string)
func (b *SystemPromptBuilder) Get(layer Layer) string
func (b *SystemPromptBuilder) Build() string  // concatenates all non-empty layers

// Layers (in order):
LayerCore     Layer = "core"      // identity + tools + cannot-do list
LayerBehavior Layer = "behavior"  // formatting, communication style
LayerMemory   Layer = "memory"    // injected memory content
LayerSkills   Layer = "skills"    // loaded skill prompts
LayerSession  Layer = "session"   // time, session context
LayerMode     Layer = "mode"      // active mode overrides
```

---

### `mode.go`

Mode system — different personalities/capabilities activated per conversation.

```go
type Mode struct {
    Meta          ModeMeta
    SystemPrompt  string
    AllowedTools  []string
    DeniedTools   []string
    ModelSettings ModelSettings
}

type ModeProvider interface {
    Get(ctx, modeID string) (*Mode, error)
    List(ctx) ([]*Mode, error)
    Create(ctx, Mode) (*Mode, error)
    BuiltinModes() []*Mode
}

func NewStaticModeProvider(modes []*Mode) *StaticModeProvider
```

---

### `embeddings.go`

Embedding interface plus semantic search wrappers.

```go
type Embedder interface {
    Embed(ctx, text string) ([]float32, error)
    EmbedBatch(ctx, texts []string) ([][]float32, error)
    Dimensions() int
}

func CosineSimilarity(a, b []float32) float64

// Wrappers:
type SemanticObservationStore struct { /* ... */ }  // observation retrieval by similarity
type SemanticSkillMatcher struct { /* ... */ }       // skill matching by embedding

// Memory search wrappers (both implement MemoryProvider):
type SemanticMemorySearch struct {
    Inner    MemoryProvider
    Embedder Embedder
}
type HybridMemorySearch struct {
    Inner    MemoryProvider
    Embedder Embedder
    K        float64   // RRF constant, default 60
}

func NewSemanticMemorySearch(inner MemoryProvider, embedder Embedder) *SemanticMemorySearch
func NewHybridMemorySearch(inner MemoryProvider, embedder Embedder) *HybridMemorySearch
```

`HybridMemorySearch` runs BM25 (via `inner.Search`) and vector search in parallel,
then fuses results with Reciprocal Rank Fusion (k=60). Use this for production
memory retrieval.

---

### `thread.go`

Thread and project management.

```go
type Thread struct {
    ID        string
    UserID    string       // tenant isolation; empty = single-user
    ProjectID string
    ModeID    string
    Status    ThreadStatus
    ParentID  string
}

type ThreadStatus string
const (
    ThreadStatusActive    ThreadStatus = "active"
    ThreadStatusCompleted ThreadStatus = "completed"
    ThreadStatusFailed    ThreadStatus = "failed"
    ThreadStatusArchived  ThreadStatus = "archived"
)

type ThreadProvider interface {
    Create(ctx, projectID, modeID string) (*Thread, error)
    Get(ctx, threadID string) (*Thread, error)
    Archive(ctx, threadID string) error
    SendMessage(ctx, Message) error
}

type MultiUserThreadProvider interface {
    ThreadProvider
    CreateForUser(ctx, userID, projectID, modeID string) (*Thread, error)
    GetForUser(ctx, userID, threadID string) (*Thread, error)  // returns ErrThreadAccessDenied if wrong user
    ListByUser(ctx, userID string, status ThreadStatus) ([]*Thread, error)
}

var ErrThreadAccessDenied = &threadError{"thread: access denied"}
```

---

### `message.go`

Message types for thread inter-communication.

```go
type Message struct {
    ID           string
    FromThreadID string
    ToThreadID   string
    Content      string
    Delivery     DeliveryMode
    CreatedAt    time.Time
}

type DeliveryMode string
const (
    DeliveryInterjected DeliveryMode = "interjected"  // delivers mid-turn
    DeliveryQueued      DeliveryMode = "queued"        // delivers after turn
)
```

---

### `checkpoint.go`

Safety snapshots before mutations.

```go
type Checkpoint struct {
    ID          string
    Description string
    CreatedAt   time.Time
    ProjectID   string
    ThreadID    string
}

type CheckpointProvider interface {
    Create(ctx, description string) (*Checkpoint, error)
    List(ctx) ([]Checkpoint, error)
    Restore(ctx, id string) error
}
```

---

### `sandbox.go`

Sandboxed code execution.

```go
type SandboxDriver interface {
    Create(ctx, SandboxConfig) (sandboxID string, error)
    Exec(ctx, sandboxID, command string) (ExecResult, error)
    WriteFile(ctx, sandboxID, path, content string) error
    ReadFile(ctx, sandboxID, path string) (string, error)
    Destroy(ctx, sandboxID string) error
    Status(ctx, sandboxID string) (SandboxStatus, error)
    IP(ctx, sandboxID string) (string, error)
}

type ExecResult struct {
    Stdout   string
    Stderr   string
    ExitCode int
}

type SandboxConfig struct {
    Image  string
    Env    map[string]string
    Labels map[string]string
}
```

---

### `session_context.go`

Injects contextual information at the start of each turn.

```go
type SessionContextProvider interface {
    Context(ctx context.Context) string
}

func LocalTimeSessionContext() SessionContextProvider
// Injects: current time, day of week, timezone
```

---

### `tracing.go`

Observability hooks — span tracking for debugging and evals.

```go
type Span struct {
    ID        string
    Name      string
    StartedAt time.Time
    EndedAt   *time.Time
    Attrs     map[string]any
    Events    []SpanEvent
}

type Tracer interface {
    StartSpan(ctx, name string) (context.Context, *Span)
    EndSpan(*Span)
    AddEvent(*Span, SpanEvent)
}

func NewInMemoryTracer() *InMemoryTracer
func NoopTracer() *noopTracer
```

---

### `eval.go`

Regression testing for agent behavior.

```go
type EvalCase struct {
    Name     string
    Input    string
    Expected string
    Tags     []string
}

type EvalResult struct {
    Case     EvalCase
    Output   string
    Pass     bool
    Score    float64
    Duration time.Duration
    Error    error
}

type EvalSuite struct {
    Cases []EvalCase
}

func (s *EvalSuite) Run(ctx, llm LLMProvider, model string) []EvalResult
```

---

### `replay.go`

Re-runs recorded conversations for debugging.

```go
type ReplaySession struct {
    Conversation *Conversation
    Events       []ReplayEvent
}

func RecordSession(runtime *Runtime) *ReplayRecorder
func (r *ReplayRecorder) Replay(ctx) (*ReplaySession, error)
```

---

### `event.go`

Event bus for decoupled communication between components.

```go
type EventBus interface {
    Publish(ctx, Event) error
    Subscribe(eventType string, handler func(Event)) (unsubscribe func())
}

type Event struct {
    Type    string
    Payload any
}

func NewInMemoryEventBus() *InMemoryEventBus
```

---

### `subagent.go`

Dispatch sub-agents that run as separate agent loops.

```go
type SubagentSpec struct {
    Name         string
    SystemPrompt string
    Task         string
    Tools        []string
}

func RunSubagentsInParallel(ctx, engine *Engine, specs []SubagentSpec) ([]SubagentResult, error)
```

---

### `reasoning.go`

Captures model reasoning traces.

```go
type ReasoningTrace struct {
    Turn     int
    Thinking string   // extended thinking content
    Summary  string
}
```

---

### `llm_router.go`

Routes requests to different providers based on model prefix or rules.

```go
type RoutedLLMProvider struct { /* ... */ }
func NewRoutedProvider(providers map[string]LLMProvider) *RoutedLLMProvider
func (r *RoutedLLMProvider) Chat(ctx, ChatRequest) (*ChatResponse, error)
func (r *RoutedLLMProvider) ChatStream(ctx, ChatRequest) (<-chan StreamEvent, error)
// Routes by model prefix: "anthropic/..." → anthropic, "openai/..." → openai
```

---

### `sdk.go`

Package doc and global version constant.

---

## Providers (`sdk/providers/`)

---

### `providers/llm/anthropic.go`

Full Anthropic API implementation.

- `NewAnthropic(apiKey, defaultModel string) *Anthropic`
- `Chat` + `ChatStream` with real SSE
- Tool use: `tool_use` / `tool_result` multi-turn batching
- System prompt caching (`cache_control: ephemeral` on SystemBlocks)
- Message-level caching (marks last assistant turn)
- Extended thinking: `thinking` block + `thinking_delta` SSE parsing
- Vision: `image` content blocks (base64 + URL)
- Error classification: 429 rate limit, 401 auth, 400 bad request

---

### `providers/llm/openai.go`

OpenAI Chat Completions API — also compatible with any OpenAI-spec endpoint.

- `NewOpenAI(baseURL, apiKey, defaultModel string) *OpenAI`
- `Chat` + `ChatStream` with real SSE
- Tool call delta accumulation by index
- Vision: `image_url` content array with base64 data URI support
- `stream_options.include_usage` for token counts during streaming
- `ReasoningEffort` passthrough for o1/o3 models
- Works with: OpenAI, Groq, Together, OpenRouter, Mistral, DeepSeek, Anyscale

---

### `providers/llm/ollama.go`

Ollama local model provider.

- `NewOllama(baseURL, defaultModel string) *Ollama`
- `Chat` + `ChatStream` via NDJSON line streaming
- Native tool calling
- Vision: base64 images array

---

### `providers/memory/filesystem.go`

Filesystem-backed memory provider with BM25 search.

- `NewFilesystem(root string) (*FilesystemMemory, error)`
- `NewLayeredFilesystem(root string) (*LayeredFilesystemMemory, error)`
- `Search` uses BM25 (k1=1.2, b=0.75) ranked retrieval
- `LayeredFilesystemMemory`: YAML frontmatter (`---\nlayer: explicit\n---`)
- `SearchLayered`: returns entries sorted by Explicit > Inferred > Session
- `ClearSession`: no-op (session lives in ObservationStore)
- Thread-safe with a read-write mutex

---

### `providers/embedders/voyage.go`

Voyage AI embeddings provider.

- `NewVoyage(apiKey, model string) *VoyageEmbedder`
- `Embed(ctx, text) ([]float32, error)`
- `EmbedBatch(ctx, texts) ([][]float32, error)`
- Models: `voyage-3`, `voyage-3-lite`, `voyage-code-3`

---

### `providers/embedders/local.go`

Bundled TF-IDF embedder — no API key required.

- `NewLocal(dimensions int) *LocalEmbedder`
- Hashing trick + character bigrams for sub-word similarity
- Smoothed IDF weighting + L2 normalization
- ~100µs per text, no network, no model loading
- Best for: offline/dev environments or as fallback

---

### `providers/tokenizers/tiktoken.go`

Exact BPE token counting via tiktoken-go.

- `NewTiktoken() *TiktokenTokenizer` — CL100K (GPT-4, Claude)
- `NewTiktokenO200K() *TiktokenTokenizer` — O200K (GPT-4o, o1)
- Lazy codec load on first call (`sync.Once`)
- Falls back to `ClaudeTokenizer` if codec fails to load

---

### `providers/tokenizers/auto.go`

Automatic tokenizer selection by model name.

- `NewAuto() *AutoTokenizer`
- `NewAutoForModel(modelName string) *AutoTokenizer`
- `SetModel(modelName string)` — rebind without creating a new instance
- gpt-4o / o1 / o3 / o4 → TiktokenO200K
- gpt-4 / gpt-3.5 → TiktokenCL100K
- claude → ClaudeTokenizer
- anything else → ClaudeTokenizer (best generic fallback)
- Cached resolution per model (sync.RWMutex)

---

### `providers/tokenizers/byte.go`

Lightweight heuristic tokenizers — no external deps.

- `HeuristicTokenizer` — `len(text)/4` (fast, ~25% error for non-English)
- `ByteTokenizer` — rune count
- `ClaudeTokenizer` — word heuristic tuned for English (~3% error)

---

### `providers/sandbox/opensandbox.go`

OpenSandbox driver implementation.

- `NewOpenSandbox(cfg OpenSandboxConfig) *OpenSandboxDriver`
- `Create` → `CreateCodeInterpreter()` (default image: code-interpreter)
- `Exec` → `Sandbox.RunCommand()`
- `ExecCode` → `CodeInterpreter.Execute()` with MIME outputs
- `ExecCodeStreaming` → `Execute()` with `ExecutionHandlers`
- `WriteFile` / `ReadFile` → `UploadFile` / `DownloadFile`
- `Destroy` → `Sandbox.Kill()`
- `IP` → `Sandbox.GetEndpoint(8080).Endpoint`
- In-process cache + `ConnectSandbox()` reconnect on cache miss

---

### `providers/sandbox/local.go`

Local subprocess sandbox for development. No isolation — runs commands
directly on the host. Do not use in production.

---

### `providers/store/filesystem.go`

JSON filesystem ConversationStore. One file per conversation ID.

- `NewFilesystemStore(dir string) (*FilesystemStore, error)`

---

### `providers/thread/memory.go`

In-memory and filesystem thread providers.

- `NewInMemoryThreadProvider() *InMemoryThreadProvider`
- `NewFilesystemThreadProvider(dir string) *FilesystemThreadProvider`
- Both implement `ThreadProvider` (not `MultiUserThreadProvider`)
- Filesystem: one JSON file per thread ID

---

### `providers/thread/sqlite.go`

SQLite-backed thread provider with multi-user isolation.

- `OpenSQLite(db *sql.DB) (*SQLiteThreadProvider, error)`
- `OpenSQLiteFile(path string) (*SQLiteThreadProvider, error)` — WAL mode
- Implements `MultiUserThreadProvider`:
  `CreateForUser` / `GetForUser` / `ListByUser`
- `ReadInbox`: marks messages as read atomically (mutex)
- Schema: `threads` + `thread_inbox`, indexes on user, project, to_thread
- Single-writer (SQLite WAL + MaxOpenConns=1)

---

### `providers/thread/postgres.go`

Postgres thread provider for distributed deployments.

- `OpenPostgres(db *sql.DB) (*PostgresThreadProvider, error)`
- Driver-agnostic — bring your own: `pgx`, `lib/pq`, etc.
- `ReadInbox`: uses `SELECT ... FOR UPDATE SKIP LOCKED` for at-most-once delivery
- `$1/$2` placeholders, `TIMESTAMPTZ`, `BIGSERIAL`
- Implements `MultiUserThreadProvider`

---

## Quick reference — which file for what

| I need to... | File |
|---|---|
| Start a conversation | `conversation.go` → `NewConversation` |
| Run a turn | `runtime.go` → `Runtime.Run` / `RunStream` |
| Configure the runtime | `runtime.go` → `With*` methods |
| Add a tool | `tool.go` → `ToolRegistry.Register` |
| Connect an LLM | `llm.go` + `providers/llm/` |
| Persist memory | `memory.go` + `providers/memory/` |
| Search memory | `embeddings.go` → `HybridMemorySearch` |
| Add a skill | `skill.go` + `loader.go` |
| Hot reload skills | `skill_reload.go` → `SkillReloader` |
| Run code in sandbox | `sandbox.go` + `providers/sandbox/` |
| Manage threads/projects | `thread.go` + `providers/thread/` |
| Verify responses | `verification.go` → `VerificationChain` |
| Filter unsafe tool calls | `safety.go` → `SafetyChain` |
| Count tokens | `providers/tokenizers/auto.go` → `AutoTokenizer` |
| Embed text | `providers/embedders/` |
| Stream events | `streaming.go` → `FanOutStream` / `CollectStream` |
| Run evaluations | `eval.go` → `EvalSuite.Run` |
| Replay sessions | `replay.go` |
| Trace execution | `tracing.go` |
| Route to multiple LLMs | `llm_router.go` → `RoutedLLMProvider` |
