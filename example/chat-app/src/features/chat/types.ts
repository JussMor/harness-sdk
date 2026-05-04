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
  | { type: "tool_call"; data: StreamToolCall }
  | { type: "tool_result"; data: StreamToolResult }
  | { type: "sandbox_output"; data: StreamSandboxOutput }
  | { type: "done"; data: StreamDone }
  | { type: "error"; data: { error?: string } }

export interface StreamCallbacks {
  onEvent: (event: StreamEvent) => void
}
