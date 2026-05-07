import type {
  ArtifactRecord,
  ArtifactStorageResponse,
  ArtifactVersion,
  BackendChat,
  BackendMessage,
  ChatMode,
  ProvidersResponse,
  StreamCallbacks,
  StreamEvent,
  StreamRequest,
  Thread,
  ThreadStatus,
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

    for (;;) {
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

  // ── Artifact endpoints ──────────────────────────────────────────────────────

  async listArtifacts(chatId: number): Promise<Array<ArtifactRecord>> {
    const r = await fetch(`${this.baseURL}/api/chats/${chatId}/artifacts`)
    return readJSON<Array<ArtifactRecord>>(r)
  }

  async createArtifact(
    chatId: number,
    payload: {
      language: string
      title?: string
      content: string
      messageId?: number
    }
  ): Promise<ArtifactRecord> {
    const r = await fetch(`${this.baseURL}/api/chats/${chatId}/artifacts`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    })
    return readJSON<ArtifactRecord>(r)
  }

  async getArtifact(artifactId: string): Promise<ArtifactRecord> {
    const r = await fetch(`${this.baseURL}/api/artifacts/${artifactId}`)
    return readJSON<ArtifactRecord>(r)
  }

  async addArtifactVersion(
    artifactId: string,
    content: string
  ): Promise<ArtifactVersion> {
    const r = await fetch(
      `${this.baseURL}/api/artifacts/${artifactId}/versions`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ content }),
      }
    )
    return readJSON<ArtifactVersion>(r)
  }

  async getArtifactStorage(
    artifactId: string,
    shared: boolean,
    userId?: string
  ): Promise<ArtifactStorageResponse> {
    const params = new URLSearchParams({ shared: String(shared) })
    if (userId) params.set("userId", userId)
    const r = await fetch(
      `${this.baseURL}/api/artifacts/${artifactId}/storage?${params}`
    )
    return readJSON<ArtifactStorageResponse>(r)
  }

  async setArtifactStorage(
    artifactId: string,
    key: string,
    value: unknown,
    shared: boolean,
    userId?: string
  ): Promise<void> {
    await fetch(`${this.baseURL}/api/artifacts/${artifactId}/storage`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ key, value, shared, userId: userId ?? "" }),
    })
  }

  async deleteArtifactStorageKey(
    artifactId: string,
    key: string,
    shared: boolean,
    userId?: string
  ): Promise<void> {
    const params = new URLSearchParams({ shared: String(shared) })
    if (userId) params.set("userId", userId)
    await fetch(
      `${this.baseURL}/api/artifacts/${artifactId}/storage/${key}?${params}`,
      {
        method: "DELETE",
      }
    )
  }

  // ── Thread endpoints ──────────────────────────────────────────────────────────────

  async createThread(opts: {
    userId?: string
    projectId?: string
    modeId?: string
  }): Promise<Thread> {
    const r = await fetch(`${this.baseURL}/api/threads`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(opts),
    })
    return readJSON<Thread>(r)
  }

  async listThreads(
    userId: string,
    status?: ThreadStatus,
    signal?: AbortSignal
  ): Promise<Array<Thread>> {
    const params = new URLSearchParams({ user: userId })
    if (status) params.set("status", status)
    const r = await fetch(`${this.baseURL}/api/threads?${params}`, { signal })
    return readJSON<Array<Thread>>(r)
  }

  async getThread(threadId: string): Promise<Thread> {
    const r = await fetch(`${this.baseURL}/api/threads/${threadId}`)
    return readJSON<Thread>(r)
  }

  async archiveThread(threadId: string): Promise<void> {
    await fetch(`${this.baseURL}/api/threads/${threadId}`, {
      method: "DELETE",
    })
  }

  // ── Human-in-the-loop ────────────────────────────────────────────────────────

  /**
   * Deliver a human approval or rejection to a paused agent loop.
   * @param chatId    The chat whose agent is waiting.
   * @param id        The ApprovalRequest.id from the confirmation_required event.
   * @param approved  true to allow the tool call, false to reject.
   * @param modifiedArgs  Optional JSON string to override tool arguments.
   */
  async confirm(
    chatId: number,
    id: string,
    approved: boolean,
    modifiedArgs?: string
  ): Promise<void> {
    await fetch(`${this.baseURL}/api/confirm`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        chat_id: chatId,
        id,
        approved,
        modified_args: modifiedArgs ?? "",
      }),
    })
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
    case "thinking":
      return { type: "thinking", data: parsed as { thinking?: string } }
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
    case "artifact":
      return {
        type: "artifact",
        data: parsed as {
          id: string
          language: string
          title: string
          version: number
          content: string
          r2Url?: string
        },
      }
    case "plan_proposed":
      return {
        type: "plan_proposed",
        data: parsed as {
          id: string
          title: string
          objective: string
          executables: Array<{
            id: string
            name: string
            description: string
            dependencies: Array<string>
            status: string
          }>
        },
      }
    case "subagent_result":
      return {
        type: "subagent_result",
        data: parsed as {
          id: string
          task: string
          output: string
          turns: number
          stop_reason: string
          duration_ms: number
          error?: string
        },
      }
    case "confirmation_required":
      return {
        type: "confirmation_required",
        data: parsed as {
          id: string
          tool: string
          args: string
          reason: string
        },
      }
    case "confirmation_resolved":
      return {
        type: "confirmation_resolved",
        data: parsed as { id: string; tool: string; approved: boolean },
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
