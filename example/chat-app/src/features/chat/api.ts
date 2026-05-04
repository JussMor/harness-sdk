import type {
    BackendChat,
    BackendMessage,
    ChatMode,
    ProvidersResponse,
    StreamCallbacks,
    StreamEvent,
    StreamRequest,
} from "@/features/chat/types"

function trimTrailingSlash(value: string): string {
  return value.replace(/\/+$/, "")
}

async function readJSON<T>(response: Response): Promise<T> {
  if (!response.ok) {
    const body = await response.text()
    throw new Error(body || `HTTP ${response.status}`)
  }
  return (await response.json()) as T
}

export class ChatAPI {
  private baseURL: string

  constructor(baseURL: string) {
    this.baseURL = trimTrailingSlash(baseURL)
  }

  async listChats(signal?: AbortSignal): Promise<Array<BackendChat>> {
    const response = await fetch(`${this.baseURL}/api/chats`, { signal })
    return readJSON<Array<BackendChat>>(response)
  }

  async createChat(title: string): Promise<BackendChat> {
    const response = await fetch(`${this.baseURL}/api/chats`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title }),
    })
    return readJSON<BackendChat>(response)
  }

  async listMessages(
    chatId: number,
    signal?: AbortSignal
  ): Promise<Array<BackendMessage>> {
    const response = await fetch(
      `${this.baseURL}/api/chats/${chatId}/messages`,
      {
        signal,
      }
    )
    return readJSON<Array<BackendMessage>>(response)
  }

  async listModes(signal?: AbortSignal): Promise<Array<ChatMode>> {
    const response = await fetch(`${this.baseURL}/api/modes`, { signal })
    return readJSON<Array<ChatMode>>(response)
  }

  async listProviders(signal?: AbortSignal): Promise<ProvidersResponse> {
    const response = await fetch(`${this.baseURL}/api/providers`, { signal })
    return readJSON<ProvidersResponse>(response)
  }

  async streamChat(
    chatId: number,
    request: StreamRequest,
    callbacks: StreamCallbacks,
    signal?: AbortSignal
  ): Promise<void> {
    const response = await fetch(`${this.baseURL}/api/chats/${chatId}/stream`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(request),
      signal,
    })

    if (!response.ok) {
      const body = await response.text()
      throw new Error(body || `Stream failed (${response.status})`)
    }

    if (!response.body) {
      throw new Error("Streaming is not available in this browser")
    }

    const reader = response.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ""

    while (true) {
      const { value, done } = await reader.read()
      if (done) {
        break
      }
      buffer += decoder.decode(value, { stream: true })
      buffer = emitSSEEvents(buffer, callbacks.onEvent)
    }

    const tail = buffer + decoder.decode()
    emitSSEEvents(tail, callbacks.onEvent)
  }
}

function emitSSEEvents(
  raw: string,
  onEvent: (event: StreamEvent) => void
): string {
  const chunks = raw.split("\n\n")
  const pending = chunks.pop() ?? ""

  for (const chunk of chunks) {
    const event = parseChunk(chunk)
    if (event) {
      onEvent(event)
    }
  }

  return pending
}

function parseChunk(chunk: string): StreamEvent | null {
  const lines = chunk
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean)

  if (lines.length === 0) {
    return null
  }

  const eventLine = lines.find((line) => line.startsWith("event:"))
  const dataLine = lines.find((line) => line.startsWith("data:"))

  if (!eventLine || !dataLine) {
    return null
  }

  const type = eventLine.slice(6).trim()
  const dataText = dataLine.slice(5).trim()

  let parsed: unknown = {}
  try {
    parsed = JSON.parse(dataText)
  } catch {
    parsed = { raw: dataText }
  }

  switch (type) {
    case "delta":
      return { type: "delta", data: parsed as { delta?: string } }
    case "tool_call":
      return {
        type: "tool_call",
        data: parsed as { name?: string; args?: Record<string, unknown> },
      }
    case "tool_result":
      return {
        type: "tool_result",
        data: parsed as { name?: string; content?: string; error?: boolean },
      }
    case "sandbox_output":
      return {
        type: "sandbox_output",
        data: parsed as Record<string, unknown>,
      }
    case "done":
      return {
        type: "done",
        data: parsed as { runId?: string; messageId?: number },
      }
    case "error":
      return { type: "error", data: parsed as { error?: string } }
    default:
      return null
  }
}
