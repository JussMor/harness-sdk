export interface BackendChat {
  id: number
  title: string
  createdAt: string
  updatedAt: string
}

export interface MessageMetadataToolCall {
  name: string
  args?: Record<string, unknown>
  result?: string
  error?: boolean
}

export interface MessageMetadataArtifact {
  path: string
  language: string
  content: string
}

export interface MessageMetadata {
  toolCalls?: Array<MessageMetadataToolCall>
  artifacts?: Array<MessageMetadataArtifact>
}

export interface BackendMessage {
  id: number
  chatId: number
  role: "user" | "assistant"
  content: string
  model?: string
  metadata?: MessageMetadata
  createdAt?: string
}

export interface ChatMode {
  id: string
  name: string
  baseMode?: string
}

export interface ProviderInfo {
  name: string
  enabled: boolean
}

export interface ProvidersResponse {
  default?: string
  providers: Array<ProviderInfo>
}

export interface StreamRequest {
  prompt: string
  mode: string
  provider?: string
  model?: string
  clientRunId?: string
  human_in_loop?: boolean
}

export interface StreamToolCall {
  name?: string
  args?: Record<string, unknown>
}

export interface StreamToolResult {
  name?: string
  content?: string
  error?: boolean
}

export interface StreamDone {
  runId?: string
  messageId?: number
}

export interface StreamSandboxOutput {
  has_rich_output?: boolean
  text?: string
  stdout?: string
  stderr?: string
  language?: string
  results?: Array<Record<string, string>>
  error?: { name?: string; value?: string; traceback?: Array<string> }
}

export type StreamEvent =
  | { type: "delta"; data: { delta?: string } }
  | { type: "thinking"; data: { thinking?: string } }
  | { type: "turn_complete"; data: Record<string, never> }
  | { type: "tool_call"; data: StreamToolCall }
  | { type: "tool_result"; data: StreamToolResult }
  | { type: "sandbox_output"; data: StreamSandboxOutput }
  | { type: "plan_proposed"; data: StreamPlanProposed }
  | { type: "agent_result"; data: StreamSubagentResult }
  | { type: "interrupt_required"; data: StreamInterruptRequest }
  | {
      type: "interrupt_resolved"
      data: { id: string; kind: string; approved?: boolean }
    }
  | { type: "artifact_created"; data: StreamComponentArtifact }
  | { type: "artifact_updated"; data: StreamComponentArtifact }
  | { type: "compaction"; data: StreamCompaction }
  | { type: "plan_mode_changed"; data: StreamPlanMode }
  | { type: "done"; data: StreamDone }
  | {
      type: "error"
      data: { error?: string; category?: string; detail?: string }
    }

// ── Generic interrupt types (new HIL system) ─────────────────────────────────

export type InterruptKind = "approval" | "question" | "form_input"

export interface StreamInterruptRequest {
  id: string
  kind: InterruptKind
  reason?: string
  approval?: {
    tool_call?: {
      id?: string
      name?: string
      /** Raw JSON-encoded argument string as the LLM emitted it. Parse to
       *  inspect specific fields (e.g. `plan` for ExitPlanMode). */
      arguments?: string
      /** Legacy/optional pre-parsed args. Backend currently sends
       *  `arguments` only; kept here for compat. */
      args?: Record<string, unknown>
    }
  }
  question?: { prompt: string; choices?: Array<string>; multi?: boolean }
  form?: { title?: string; schema?: Record<string, unknown>; ui_hint?: string }
}

// ── Component artifact (generative UI) ───────────────────────────────────────

export interface StreamComponentArtifact {
  id: string
  kind: "file" | "component"
  /** Where the frontend should mount this artifact. Defaults to "canvas". */
  placement?: "canvas" | "inline"
  /** When present the rendered component should call onSubmit(data); the
   *  renderer posts the result to /api/interrupts/:token/resolve. */
  interaction?: {
    token: string
    chat_id?: number
  }
  /** Populated when kind === "component". */
  component?: {
    name: string
    catalog_id?: string
    props?: unknown
    a2ui_surface?: unknown
  }
  /** Populated when kind === "file". */
  file?: {
    title?: string
    language?: string
    path?: string
    content?: string
    url?: string
    version?: number
  }
}

export interface StreamPlanProposed {
  id: string
  title: string
  objective: string
  executables: Array<StreamPlanExecutable>
}

// ── Compaction & plan-mode signals ──────────────────────────────────────────

/** Emitted when the runtime had to summarise older history because the
 *  context budget was exceeded. The frontend should surface a brief
 *  "memory compacted" indicator. */
export interface StreamCompaction {
  messages_dropped: number
  overflow_tokens: number
  summary?: string
}

/** Emitted on plan-mode transitions (EnterPlanMode / approved or rejected
 *  ExitPlanMode). The frontend uses this to badge the chat surface and
 *  surface the plan dialog. */
export interface StreamPlanMode {
  state: "entered" | "exited"
  plan?: string
  reason?: string
}

export interface StreamPlanExecutable {
  id: string
  name: string
  description: string
  dependencies: Array<string>
  status: string
}

export interface StreamSubagentResult {
  id: string
  task: string
  output: string
  turns: number
  stop_reason: string
  duration_ms: number
  error?: string
  /** Model used by this subagent if overridden. */
  model?: string
  /** Custom system prompt assigned to this subagent. */
  system_prompt?: string
}

// ── Artifact API types ──────────────────────────────────────────────────────

export interface ArtifactVersion {
  id: number
  artifactId: string
  version: number
  content: string
  r2Url?: string
  createdAt: string
}

export interface ArtifactRecord {
  id: string
  chatId: number
  messageId?: number
  language: string
  title: string
  createdAt: string
  versions?: Array<ArtifactVersion>
  /** Most recent version content, populated by the list endpoint. */
  latestContent?: string
}

export interface ArtifactStorageResponse {
  artifactId: string
  shared: boolean
  data: Record<string, unknown>
}

export interface StreamCallbacks {
  onEvent: (event: StreamEvent) => void
}

// ── Thread types ──────────────────────────────────────────────────────────────

export type ThreadStatus = "active" | "completed" | "failed" | "archived"

export interface Thread {
  id: string
  user_id?: string
  project_id?: string
  mode_id?: string
  status: ThreadStatus
  parent_id?: string
}
