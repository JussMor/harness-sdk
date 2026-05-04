import { ChatAPI } from "@/features/chat/api"
import type {
  BackendMessage,
  ChatMode,
  ProvidersResponse,
  StreamEvent,
} from "@/features/chat/types"
import {
  Bot,
  LoaderCircle,
  SendHorizontal,
  Sparkles,
  Square,
  User,
} from "lucide-react"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

export interface ChatMessage {
  id: string
  content: string
  role: "user" | "assistant"
  model?: string
  pending?: boolean
}

export interface ChatMainProps {
  userName?: string
  showGreeting?: boolean
  backendBaseURL?: string
  activeChatID?: string
  onChatCreated?: (chatId: string) => void
  onChatsChanged?: () => void
}

interface TimelineEvent {
  id: string
  text: string
  level: "info" | "success" | "error"
}

const fallbackModes: Array<ChatMode> = [
  { id: "balanced", name: "Balanced" },
  { id: "analyst", name: "Analyst" },
  { id: "code-agent", name: "Code Agent" },
  { id: "code-reviewer", name: "Code Reviewer" },
  { id: "deep-work", name: "Deep Work" },
]

export function ChatMain({
  userName = "Operator",
  showGreeting = true,
  backendBaseURL = "http://localhost:8080",
  activeChatID,
  onChatCreated,
  onChatsChanged,
}: ChatMainProps) {
  const api = useMemo(() => new ChatAPI(backendBaseURL), [backendBaseURL])

  const [chatID, setChatID] = useState<number | null>(null)
  const [messages, setMessages] = useState<Array<ChatMessage>>([])
  const [input, setInput] = useState("")
  const [isStreaming, setIsStreaming] = useState(false)
  const [statusText, setStatusText] = useState("Ready")
  const [timeline, setTimeline] = useState<Array<TimelineEvent>>([])

  const [modes, setModes] = useState<Array<ChatMode>>(fallbackModes)
  const [selectedMode, setSelectedMode] = useState("balanced")
  const [providers, setProviders] = useState<Array<string>>([])
  const [selectedProvider, setSelectedProvider] = useState("")

  const streamControllerRef = useRef<AbortController | null>(null)
  const listEndRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    listEndRef.current?.scrollIntoView({ behavior: "smooth", block: "end" })
  }, [messages, timeline])

  const pushTimeline = useCallback(
    (text: string, level: TimelineEvent["level"] = "info") => {
      setTimeline((prev) => {
        const event: TimelineEvent = {
          id: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
          text,
          level,
        }
        const next = [...prev, event]
        return next.slice(-12)
      })
    },
    []
  )

  const syncMessages = useCallback(
    async (targetChatID: number, signal?: AbortSignal) => {
      const payload = await api.listMessages(targetChatID, signal)
      const next = payload
        .filter(
          (message) => message.role === "user" || message.role === "assistant"
        )
        .map(toChatMessage)
      setMessages(next)
    },
    [api]
  )

  useEffect(() => {
    const controller = new AbortController()

    const run = async () => {
      try {
        const payload = await api.listModes(controller.signal)
        if (payload.length > 0) {
          setModes(payload)
          if (!payload.some((mode) => mode.id === selectedMode)) {
            setSelectedMode(payload[0].id)
          }
        }
      } catch {
        pushTimeline("Could not load modes, using local defaults", "error")
      }

      try {
        const payload: ProvidersResponse = await api.listProviders(
          controller.signal
        )
        const names = payload.providers
          .filter((provider) => provider.enabled)
          .map((provider) => provider.name)

        setProviders(names)
        if (names.length === 0) {
          setSelectedProvider("")
          return
        }

        if (payload.default && names.includes(payload.default)) {
          setSelectedProvider(payload.default)
          return
        }
        setSelectedProvider(names[0])
      } catch {
        pushTimeline("Could not load providers", "error")
      }
    }

    void run()
    return () => controller.abort()
  }, [api, pushTimeline, selectedMode])

  useEffect(() => {
    if (!activeChatID) {
      setChatID(null)
      setMessages([])
      setTimeline([])
      return
    }

    const parsed = Number(activeChatID)
    if (Number.isNaN(parsed)) {
      setChatID(null)
      setMessages([])
      return
    }

    setChatID(parsed)
    const controller = new AbortController()
    void syncMessages(parsed, controller.signal)
    return () => controller.abort()
  }, [activeChatID, syncMessages])

  useEffect(() => {
    return () => {
      streamControllerRef.current?.abort()
    }
  }, [])

  const stopStream = useCallback(() => {
    if (!streamControllerRef.current) {
      return
    }
    streamControllerRef.current.abort()
    streamControllerRef.current = null
    setIsStreaming(false)
    setStatusText("Canceled")
    pushTimeline("Stream canceled by user", "error")
  }, [pushTimeline])

  const handleStreamEvent = useCallback(
    (pendingAssistantId: string, event: StreamEvent) => {
      if (event.type === "delta") {
        const delta = event.data.delta || ""
        if (!delta) {
          return
        }
        setMessages((prev) =>
          prev.map((message) =>
            message.id === pendingAssistantId
              ? {
                  ...message,
                  content: message.content + delta,
                  pending: true,
                }
              : message
          )
        )
        return
      }

      if (event.type === "tool_call") {
        const toolName = event.data.name || "unknown_tool"
        pushTimeline(`Tool call: ${toolName}`, "info")
        return
      }

      if (event.type === "tool_result") {
        const toolName = event.data.name || "unknown_tool"
        const level: TimelineEvent["level"] = event.data.error
          ? "error"
          : "success"
        pushTimeline(`Tool result: ${toolName}`, level)
        return
      }

      if (event.type === "error") {
        const errorMessage = event.data.error || "Unknown stream error"
        pushTimeline(`Stream error: ${errorMessage}`, "error")
      }
    },
    [pushTimeline]
  )

  const ensureChat = useCallback(async (): Promise<number> => {
    if (chatID) {
      return chatID
    }

    const created = await api.createChat(`Chat ${new Date().toLocaleString()}`)
    setChatID(created.id)
    onChatCreated?.(String(created.id))
    onChatsChanged?.()
    return created.id
  }, [api, chatID, onChatCreated, onChatsChanged])

  const sendPrompt = useCallback(async () => {
    const prompt = input.trim()
    if (!prompt || isStreaming) {
      return
    }

    setInput("")
    setStatusText("Streaming response...")
    setIsStreaming(true)

    const runID = `run-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
    const userMessageID = `local-user-${runID}`
    const assistantMessageID = `local-assistant-${runID}`

    setMessages((prev) => [
      ...prev,
      { id: userMessageID, role: "user", content: prompt },
      {
        id: assistantMessageID,
        role: "assistant",
        content: "",
        model: selectedMode,
        pending: true,
      },
    ])

    const controller = new AbortController()
    streamControllerRef.current = controller

    try {
      const targetChatID = await ensureChat()

      await api.streamChat(
        targetChatID,
        {
          prompt,
          mode: selectedMode,
          provider: selectedProvider || undefined,
          clientRunId: runID,
        },
        {
          onEvent: (event) => handleStreamEvent(assistantMessageID, event),
        },
        controller.signal
      )

      await syncMessages(targetChatID)
      onChatsChanged?.()
      setStatusText("Completed")
      pushTimeline("Stream finished", "success")
    } catch (error) {
      const aborted =
        error instanceof DOMException && error.name === "AbortError"
      if (!aborted) {
        const message =
          error instanceof Error ? error.message : "Unexpected stream error"
        setMessages((prev) =>
          prev.map((entry) =>
            entry.id === assistantMessageID
              ? {
                  ...entry,
                  content:
                    entry.content ||
                    `No se pudo completar la respuesta: ${message}`,
                  pending: false,
                }
              : entry
          )
        )
        setStatusText("Failed")
        pushTimeline(`Request failed: ${message}`, "error")
      }
    } finally {
      streamControllerRef.current = null
      setIsStreaming(false)
      setMessages((prev) =>
        prev.map((entry) =>
          entry.id === assistantMessageID ? { ...entry, pending: false } : entry
        )
      )
    }
  }, [
    api,
    ensureChat,
    handleStreamEvent,
    input,
    isStreaming,
    onChatsChanged,
    pushTimeline,
    selectedMode,
    selectedProvider,
    syncMessages,
  ])

  return (
    <section className="chat-main-root">
      {showGreeting && messages.length === 0 && (
        <header className="chat-main-greeting">
          <p className="chat-main-badge">
            <Sparkles size={14} />
            Stream Runtime Ready
          </p>
          <h1>Hola {userName}, el backend ya esta conectado con SSE real.</h1>
          <p>
            Crea un chat, elige provider y modo, y mira deltas + eventos de
            tools en tiempo real.
          </p>
        </header>
      )}

      <div className="chat-main-toolbar">
        <select
          value={selectedMode}
          onChange={(event) => setSelectedMode(event.target.value)}
          disabled={isStreaming}
          className="chat-main-select"
        >
          {modes.map((mode) => (
            <option key={mode.id} value={mode.id}>
              {mode.name}
            </option>
          ))}
        </select>

        <select
          value={selectedProvider}
          onChange={(event) => setSelectedProvider(event.target.value)}
          disabled={isStreaming || providers.length === 0}
          className="chat-main-select"
        >
          {providers.length === 0 ? (
            <option value="">No provider</option>
          ) : (
            providers.map((provider) => (
              <option key={provider} value={provider}>
                {provider}
              </option>
            ))
          )}
        </select>

        <div className="chat-main-status">
          {isStreaming ? (
            <LoaderCircle className="spin" size={14} />
          ) : (
            <Sparkles size={14} />
          )}
          <span>{statusText}</span>
        </div>
      </div>

      <div className="chat-main-grid">
        <div className="chat-main-feed">
          {messages.map((message) => (
            <article
              key={message.id}
              className={`chat-bubble ${
                message.role === "assistant"
                  ? "chat-bubble-assistant"
                  : "chat-bubble-user"
              }`}
            >
              <div className="chat-bubble-meta">
                {message.role === "assistant" ? (
                  <Bot size={14} />
                ) : (
                  <User size={14} />
                )}
                <span>
                  {message.role === "assistant"
                    ? message.model || "assistant"
                    : "you"}
                </span>
                {message.pending && <LoaderCircle className="spin" size={13} />}
              </div>
              <p>{message.content || "..."}</p>
            </article>
          ))}
          <div ref={listEndRef} />
        </div>

        <aside className="chat-main-events">
          <h2>Live Events</h2>
          {timeline.length === 0 ? (
            <p className="chat-empty-events">
              Tool calls and stream milestones appear here.
            </p>
          ) : (
            timeline.map((event) => (
              <p
                key={event.id}
                className={`chat-event chat-event-${event.level}`}
              >
                {event.text}
              </p>
            ))
          )}
        </aside>
      </div>

      <footer className="chat-main-input-wrap">
        <textarea
          value={input}
          onChange={(event) => setInput(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && !event.shiftKey) {
              event.preventDefault()
              void sendPrompt()
            }
          }}
          className="chat-main-textarea"
          placeholder="Escribe tu prompt. Enter para enviar, Shift+Enter para nueva linea"
          disabled={isStreaming}
        />

        <div className="chat-main-actions">
          <button
            type="button"
            className="chat-btn chat-btn-primary"
            onClick={() => void sendPrompt()}
            disabled={!input.trim() || isStreaming}
          >
            <SendHorizontal size={15} />
            Send
          </button>
          <button
            type="button"
            className="chat-btn chat-btn-muted"
            onClick={stopStream}
            disabled={!isStreaming}
          >
            <Square size={13} />
            Stop
          </button>
        </div>
      </footer>
    </section>
  )
}

function toChatMessage(message: BackendMessage): ChatMessage {
  return {
    id: String(message.id),
    role: message.role,
    content: message.content,
    model: message.model,
    pending: false,
  }
}
