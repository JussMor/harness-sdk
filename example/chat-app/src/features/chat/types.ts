export interface BackendChat {
  id: number
  title: string
  createdAt: string
  updatedAt: string
}

export interface BackendMessage {
  id: number
  chatId: number
  role: "user" | "assistant"
  content: string
  model?: string
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

export type StreamEvent =
  | { type: "delta"; data: { delta?: string } }
  | { type: "tool_call"; data: StreamToolCall }
  | { type: "tool_result"; data: StreamToolResult }
  | { type: "done"; data: StreamDone }
  | { type: "error"; data: { error?: string } }

export interface StreamCallbacks {
  onEvent: (event: StreamEvent) => void
}
