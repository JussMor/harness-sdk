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
  | { type: "tool_call"; data: StreamToolCall }
  | { type: "tool_result"; data: StreamToolResult }
  | { type: "sandbox_output"; data: StreamSandboxOutput }
  | { type: "artifact"; data: StreamArtifact }
  | { type: "plan_proposed"; data: StreamPlanProposed }
  | { type: "subagent_result"; data: StreamSubagentResult }
  | { type: "confirmation_required"; data: ConfirmationRequest }
  | { type: "confirmation_resolved"; data: { id: string; tool: string; approved: boolean } }
  | { type: "done"; data: StreamDone }
  | { type: "error"; data: { error?: string } }

// ── Human-in-the-Loop types ───────────────────────────────────────────────────

export interface ConfirmationRequest {
  id: string
  tool: string
  args: string   // raw JSON string of tool arguments
  reason: string
}

export interface StreamPlanProposed {
  id: string
  title: string
  objective: string
  executables: Array<StreamPlanExecutable>
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

export interface StreamArtifact {
  id: string
  language: string
  title: string
  version: number
  content: string
  r2Url?: string
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
