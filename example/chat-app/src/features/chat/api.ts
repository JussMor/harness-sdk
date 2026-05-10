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
import { ProtocolVersion, parseSSE } from "@harness/client"

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
      headers: {
        "Content-Type": "application/json",
        "X-Harness-Protocol": ProtocolVersion,
      },
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

    // SSE parsing delegated to @harness/client — single shared parser keeps
    // the chat-app from drifting from the wire format.
    for await (const sse of parseSSE(response.body)) {
      const ev = adaptSSEEvent(sse.event, sse.data)
      if (ev) callbacks.onEvent(ev)
    }
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

  /**
   * Resolve a generic interrupt (approval/question/form_input) using a
   * resolution token issued by the backend.
   */
  async resolveInterrupt(
    token: string,
    chatId: number,
    response: {
      approved?: boolean
      answer?: unknown
      modifiedArgs?: string
    }
  ): Promise<void> {
    const r = await fetch(
      `${this.baseURL}/api/interrupts/${encodeURIComponent(token)}/resolve`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          chat_id: chatId,
          approved: response.approved ?? false,
          answer: response.answer,
          modified_args: response.modifiedArgs ?? "",
        }),
      }
    )
    if (!r.ok) {
      const body = await r.text()
      throw new Error(body || `resolveInterrupt failed (${r.status})`)
    }
  }
}

function adaptSSEEvent(type: string, dataText: string): StreamEvent | null {
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
    case "turn_complete":
      return { type: "turn_complete", data: {} }
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
      // Legacy: backend now emits file artifacts as "artifact_created"
      // with the unified SDK Artifact shape. Map for backward compat.
      return {
        type: "artifact_created",
        data: {
          id: (parsed as Record<string, unknown>).id as string,
          kind: "file" as const,
          placement: "canvas" as const,
          file: {
            title: (parsed as Record<string, unknown>).title as string,
            language: (parsed as Record<string, unknown>).language as string,
            content: (parsed as Record<string, unknown>).content as string,
            url: (parsed as Record<string, unknown>).r2Url as
              | string
              | undefined,
            version: (parsed as Record<string, unknown>).version as
              | number
              | undefined,
          },
        } satisfies import("./types").StreamComponentArtifact,
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
    case "agent_result":
      return {
        type: "agent_result",
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
    case "interrupt_required":
      return {
        type: "interrupt_required",
        data: parsed as import("./types").StreamInterruptRequest,
      }
    case "interrupt_resolved":
      return {
        type: "interrupt_resolved",
        data: parsed as { id: string; kind: string; approved?: boolean },
      }
    case "artifact_created":
      return {
        type: "artifact_created",
        data: parsed as import("./types").StreamComponentArtifact,
      }
    case "artifact_updated":
      return {
        type: "artifact_updated",
        data: parsed as import("./types").StreamComponentArtifact,
      }
    case "compaction":
      return {
        type: "compaction",
        data: parsed as import("./types").StreamCompaction,
      }
    case "plan_mode_changed":
      return {
        type: "plan_mode_changed",
        data: parsed as import("./types").StreamPlanMode,
      }
    case "done":
      return {
        type: "done",
        data: parsed as { runId?: string; messageId?: number },
      }
    case "error":
      return {
        type: "error",
        data: parsed as { error?: string; category?: string; detail?: string },
      }
    default:
      return null
  }
}
