import { ArtifactCanvas } from "@/components/artifact-canvas"
import { ChatAPI } from "@/features/chat/api"
import type { Artifact } from "@/features/chat/artifact-detector"
import {
  createDetectorState,
  finalizeStream,
  processStreamDelta,
} from "@/features/chat/artifact-detector"
import type {
  BackendMessage,
  ChatMode,
  ProvidersResponse,
  StreamEvent,
  StreamPlanProposed,
  StreamSubagentResult,
} from "@/features/chat/types"
import {
  Bot,
  Brain,
  Check,
  ChevronDown,
  ChevronRight,
  Copy,
  LoaderCircle,
  RefreshCw,
  SendHorizontal,
  Sparkles,
  Square,
  ThumbsDown,
  ThumbsUp,
  User,
} from "lucide-react"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import ReactMarkdown from "react-markdown"
import remarkGfm from "remark-gfm"
import type { SubagentTrace, ToolTrace } from "./tool-trace"
import { ToolTraceCard } from "./tool-trace"

export interface ChatMessage {
  id: string
  content: string
  role: "user" | "assistant"
  model?: string
  pending?: boolean
  thinking?: string
  traces?: Array<ToolTrace>
  plan?: StreamPlanProposed
  subagentResults?: Array<StreamSubagentResult>
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
  backendBaseURL = "http://localhost:9090",
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

  // ── Artifact Canvas state ────────────────────────────────────────────────
  const [activeArtifact, setActiveArtifact] = useState<Artifact | null>(null)
  const [allArtifacts, setAllArtifacts] = useState<Array<Artifact>>([])
  const [isArtifactStreaming, setIsArtifactStreaming] = useState(false)
  const detectorRef = useRef(createDetectorState())

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

      // Preserve streaming-only fields (plan, subagentResults) that aren't
      // persisted in the backend but were accumulated during the stream.
      // The local streaming message has a temporary id (e.g. "local-assistant-…")
      // that won't match the new numeric DB id, so we find the most recent
      // prev assistant message with these fields and graft them onto the
      // LAST assistant message in next (the one we just finalized).
      setMessages((prev) => {
        const prevMap = new Map(prev.map((m) => [m.id, m]))

        // Find the latest prev assistant message carrying streaming-only data.
        let latestStreamFields:
          | Pick<ChatMessage, "plan" | "subagentResults">
          | undefined
        for (let i = prev.length - 1; i >= 0; i--) {
          const m = prev[i]
          if (
            m.role === "assistant" &&
            (m.plan || (m.subagentResults && m.subagentResults.length > 0))
          ) {
            latestStreamFields = {
              plan: m.plan,
              subagentResults: m.subagentResults,
            }
            break
          }
        }

        // Index of the last assistant message in next.
        let lastAssistantIdx = -1
        for (let i = next.length - 1; i >= 0; i--) {
          if (next[i].role === "assistant") {
            lastAssistantIdx = i
            break
          }
        }

        return next.map((m, idx) => {
          // Same-id match (e.g. older messages already persisted with stable ids)
          const existing = prevMap.get(m.id)
          if (existing && (existing.plan || existing.subagentResults)) {
            return {
              ...m,
              plan: existing.plan,
              subagentResults: existing.subagentResults,
            }
          }
          // Graft streaming-only fields onto the freshly-persisted assistant message.
          if (idx === lastAssistantIdx && latestStreamFields) {
            return { ...m, ...latestStreamFields }
          }
          return m
        })
      })

      // Reconstruct artifacts from persisted metadata
      const restored: Array<Artifact> = []
      for (const msg of payload) {
        if (msg.metadata?.artifacts) {
          for (const a of msg.metadata.artifacts) {
            restored.push({
              id: `restored-${msg.id}-${restored.length}`,
              language: a.language,
              content: a.content,
              complete: true,
              title: a.path.split("/").pop() || a.path,
            })
          }
        }
      }
      setAllArtifacts(restored)
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

        // Run artifact detection on the delta
        const prevState = detectorRef.current
        const nextState = processStreamDelta(prevState, delta)
        detectorRef.current = nextState

        // Update chat content (text outside artifacts)
        setMessages((prev) =>
          prev.map((message) =>
            message.id === pendingAssistantId
              ? {
                  ...message,
                  content: nextState.chatContent,
                  pending: true,
                }
              : message
          )
        )

        // If an artifact is open/streaming, show it in canvas
        if (nextState.activeIndex >= 0) {
          const art = nextState.artifacts[nextState.activeIndex]
          setActiveArtifact(art)
          setIsArtifactStreaming(true)
        } else if (prevState.activeIndex >= 0 && nextState.activeIndex < 0) {
          // Artifact just closed
          const closedArt = nextState.artifacts[prevState.activeIndex]
          setActiveArtifact(closedArt)
          setIsArtifactStreaming(false)
        }

        setAllArtifacts(nextState.artifacts)
        return
      }

      if (event.type === "thinking") {
        const thinking = event.data.thinking || ""
        if (!thinking) return
        setMessages((prev) =>
          prev.map((message) =>
            message.id === pendingAssistantId
              ? { ...message, thinking: (message.thinking || "") + thinking }
              : message
          )
        )
        return
      }

      if (event.type === "tool_call") {
        const toolName = event.data.name || "unknown_tool"
        const traceId = `trace-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
        const newTrace: ToolTrace = {
          id: traceId,
          name: toolName,
          args: event.data.args,
          status: "running",
        }
        setMessages((prev) =>
          prev.map((message) =>
            message.id === pendingAssistantId
              ? {
                  ...message,
                  traces: [...(message.traces ?? []), newTrace],
                }
              : message
          )
        )
        pushTimeline(`Tool call: ${toolName}`, "info")

        // When file_write is called, show the content in the canvas immediately
        if (toolName === "file_write" && event.data.args) {
          const args = event.data.args
          const content = (args.content as string) || ""
          const filePath = (args.path as string) || "file"
          if (content) {
            const lang = inferLanguageFromPath(filePath)
            const fileArtifact: Artifact = {
              id: `file-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
              language: lang,
              content,
              complete: true,
              title: filePath.split("/").pop() || filePath,
            }
            setActiveArtifact(fileArtifact)
            setAllArtifacts((prev) => [...prev, fileArtifact])
            setIsArtifactStreaming(false)
          }
        }
        return
      }

      if (event.type === "tool_result") {
        const toolName = event.data.name || "unknown_tool"
        const level: TimelineEvent["level"] = event.data.error
          ? "error"
          : "success"
        pushTimeline(`Tool result: ${toolName}`, level)

        const subagents = parseSubagentResult(toolName, event.data.content)

        setMessages((prev) =>
          prev.map((message) => {
            if (message.id !== pendingAssistantId || !message.traces) {
              return message
            }
            // Update the most recent running trace with this name (LIFO).
            const traces = [...message.traces]
            for (let i = traces.length - 1; i >= 0; i--) {
              if (
                traces[i].name === toolName &&
                traces[i].status === "running"
              ) {
                traces[i] = {
                  ...traces[i],
                  status: event.data.error ? "error" : "success",
                  result: event.data.content,
                  error: event.data.error,
                  subagents,
                }
                break
              }
            }
            return { ...message, traces }
          })
        )
        return
      }

      if (event.type === "sandbox_output") {
        // Rich sandbox output (HTML, image, etc.) — show in canvas
        const data = event.data
        if (data.has_rich_output && data.text) {
          pushTimeline("Rich sandbox output received", "success")
        }
        // If there are HTML results from code_interpreter, create an artifact
        const results = (data as Record<string, unknown>).results as
          | Array<Record<string, string>>
          | undefined
        if (results) {
          for (const res of results) {
            if (res["text/html"]) {
              const htmlArtifact: Artifact = {
                id: `sandbox-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
                language: "html",
                content: res["text/html"],
                complete: true,
                title: "Sandbox Output",
              }
              setActiveArtifact(htmlArtifact)
              setAllArtifacts((prev) => [...prev, htmlArtifact])
              setIsArtifactStreaming(false)
              break
            }
          }
        }
        return
      }

      if (event.type === "error") {
        const errorMessage = event.data.error || "Unknown stream error"
        pushTimeline(`Stream error: ${errorMessage}`, "error")
        return
      }

      if (event.type === "plan_proposed") {
        const plan = event.data
        pushTimeline(
          `Plan: ${plan.title} (${plan.executables.length} steps)`,
          "info"
        )
        setMessages((prev) =>
          prev.map((message) =>
            message.id === pendingAssistantId ? { ...message, plan } : message
          )
        )
        return
      }

      if (event.type === "subagent_result") {
        const result = event.data
        const level: TimelineEvent["level"] = result.error ? "error" : "success"
        pushTimeline(`Subagent ${result.id}: ${result.error || "done"}`, level)
        setMessages((prev) =>
          prev.map((message) =>
            message.id === pendingAssistantId
              ? {
                  ...message,
                  subagentResults: [...(message.subagentResults ?? []), result],
                }
              : message
          )
        )
        return
      }

      if (event.type === "done") {
        // Mark stream as done — syncMessages() after streamChat resolves
        // handles full message and artifact restoration from backend.
        pushTimeline("Stream finished", "success")
        return
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

    // Reset artifact detector for the new response
    detectorRef.current = createDetectorState()
    setIsArtifactStreaming(false)

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

      // Finalize any open inline artifact from the text detector,
      // but don't overwrite allArtifacts — syncMessages already restored
      // the authoritative set from backend metadata.
      const finalState = finalizeStream(detectorRef.current)
      detectorRef.current = finalState
      setIsArtifactStreaming(false)
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
    <section
      className={`chat-main-root ${activeArtifact ? "chat-main-root--with-canvas" : ""}`}
    >
      <div className="chat-main-panel">
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
            {allArtifacts.length > 0 && (
              <span className="chat-main-artifact-count">
                {allArtifacts.length} artifact
                {allArtifacts.length > 1 ? "s" : ""}
              </span>
            )}
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
                  {message.pending && (
                    <LoaderCircle className="spin" size={13} />
                  )}
                </div>
                {message.traces && message.traces.length > 0 && (
                  <div className="chat-bubble-traces">
                    {message.traces.map((trace) => (
                      <ToolTraceCard key={trace.id} trace={trace} />
                    ))}
                  </div>
                )}
                {message.plan && (
                  <PlanBlock
                    plan={message.plan}
                    defaultOpen={!!message.pending}
                  />
                )}
                {message.subagentResults &&
                  message.subagentResults.length > 0 && (
                    <SubagentResultsBlock
                      results={message.subagentResults}
                      defaultOpen={!!message.pending}
                    />
                  )}
                {message.thinking && (
                  <ThinkingBlock
                    content={message.thinking}
                    isStreaming={!!message.pending}
                  />
                )}
                <MessageContent message={message} />
                {/* Actions row — only for complete assistant messages */}
                {message.role === "assistant" &&
                  !message.pending &&
                  !isStreaming && (
                    <MessageActions
                      content={message.content}
                      onRetry={() => {
                        const idx = messages.findIndex(
                          (m) => m.id === message.id
                        )
                        const prev = messages
                          .slice(0, idx)
                          .reverse()
                          .find((m) => m.role === "user")
                        if (prev) {
                          setInput(prev.content)
                          setTimeout(() => void sendPrompt(), 0)
                        }
                      }}
                    />
                  )}
              </article>
            ))}
            <div ref={listEndRef} />
          </div>

          <aside className="chat-main-events">
            <h2>Live Events</h2>
            {timeline.length === 0 && allArtifacts.length === 0 ? (
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

            {allArtifacts.length > 0 && (
              <div className="chat-artifacts-list">
                <h3 className="chat-artifacts-list__title">Artifacts</h3>
                {allArtifacts.map((artifact) => (
                  <button
                    key={artifact.id}
                    type="button"
                    className={`chat-artifact-item ${activeArtifact?.id === artifact.id ? "chat-artifact-item--active" : ""}`}
                    onClick={() => {
                      setActiveArtifact(artifact)
                      setIsArtifactStreaming(false)
                    }}
                  >
                    <span className="chat-artifact-item__lang">
                      {artifact.language}
                    </span>
                    <span className="chat-artifact-item__name">
                      {artifact.title || `${artifact.language} artifact`}
                    </span>
                  </button>
                ))}
              </div>
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
      </div>

      {/* Artifact Canvas — rendered alongside the chat when an artifact is active */}
      {activeArtifact && (
        <ArtifactCanvas
          artifact={activeArtifact}
          isStreaming={isArtifactStreaming}
          onClose={() => {
            setActiveArtifact(null)
            setIsArtifactStreaming(false)
          }}
          onSaveVersion={async (artifactId, newContent) => {
            // Update local state immediately
            setAllArtifacts((prev) =>
              prev.map((a) =>
                a.id === artifactId ? { ...a, content: newContent } : a
              )
            )
            setActiveArtifact((prev) =>
              prev && prev.id === artifactId
                ? { ...prev, content: newContent }
                : prev
            )
          }}
        />
      )}
    </section>
  )
}

function toChatMessage(message: BackendMessage): ChatMessage {
  // Restore tool traces from persisted metadata
  const traces: Array<ToolTrace> = (message.metadata?.toolCalls ?? []).map(
    (tc, i) => ({
      id: `restored-${message.id}-${i}`,
      name: tc.name,
      args: tc.args,
      result: tc.result,
      error: tc.error,
      status: tc.error ? ("error" as const) : ("success" as const),
      subagents: parseSubagentResult(tc.name, tc.result),
    })
  )

  return {
    id: String(message.id),
    role: message.role,
    content: message.content,
    model: message.model,
    pending: false,
    traces: traces.length > 0 ? traces : undefined,
  }
}

/**
 * Parse the JSON payload returned by the `dispatch-subagents` tool into a
 * list of SubagentTrace items. Returns undefined when the tool name does not
 * match or the payload cannot be parsed.
 */
function parseSubagentResult(
  toolName: string,
  content?: string
): Array<SubagentTrace> | undefined {
  if (toolName !== "dispatch-subagents" || !content) {
    return undefined
  }
  try {
    const parsed = JSON.parse(content) as {
      results?: Array<SubagentTrace>
    }
    if (Array.isArray(parsed.results)) {
      return parsed.results
    }
  } catch {
    // ignore — tool may have returned a free-form error string
  }
  return undefined
}

/**
 * Infer the artifact language from a file path extension.
 */
function inferLanguageFromPath(filePath: string): string {
  const ext = filePath.split(".").pop()?.toLowerCase() || ""
  const map: Record<string, string> = {
    html: "html",
    htm: "html",
    md: "markdown",
    markdown: "markdown",
    css: "css",
    js: "javascript",
    ts: "typescript",
    jsx: "jsx",
    tsx: "tsx",
    py: "python",
    json: "json",
    yaml: "yaml",
    yml: "yaml",
    svg: "svg",
    xml: "xml",
    sql: "sql",
    sh: "bash",
    bash: "bash",
    go: "go",
    rs: "rust",
    toml: "toml",
  }
  return map[ext] || "text"
}

// ── MessageContent ────────────────────────────────────────────────────────────
// Renders message text as markdown for assistant, plain for user.

function MessageContent({ message }: { message: ChatMessage }) {
  if (!message.content && message.pending) {
    return <p className="chat-bubble-pending">...</p>
  }
  if (!message.content) return null

  if (message.role === "user") {
    return <p className="chat-bubble-text">{message.content}</p>
  }

  // Assistant: render as markdown
  return (
    <div className="chat-bubble-markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          // Code blocks — with copy button
          code({ node, className, children, ...props }) {
            const match = /language-(\w+)/.exec(className || "")
            const lang = match?.[1] || ""
            const code = String(children).replace(/\n$/, "")
            const isBlock = code.includes("\n") || !!lang

            if (!isBlock) {
              return (
                <code className="chat-inline-code" {...props}>
                  {children}
                </code>
              )
            }
            return <CodeBlock lang={lang} code={code} />
          },
          // Links open in new tab
          a({ href, children }) {
            return (
              <a href={href} target="_blank" rel="noopener noreferrer">
                {children}
              </a>
            )
          },
        }}
      >
        {message.content}
      </ReactMarkdown>
    </div>
  )
}

// ── CodeBlock ─────────────────────────────────────────────────────────────────

function CodeBlock({ lang, code }: { lang: string; code: string }) {
  const [copied, setCopied] = useState(false)

  const handleCopy = async () => {
    await navigator.clipboard.writeText(code)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  return (
    <div className="chat-code-block">
      <div className="chat-code-block__header">
        {lang && <span className="chat-code-block__lang">{lang}</span>}
        <button
          type="button"
          className="chat-code-block__copy"
          onClick={handleCopy}
          title="Copy code"
        >
          {copied ? <Check size={12} /> : <Copy size={12} />}
          <span>{copied ? "Copied" : "Copy"}</span>
        </button>
      </div>
      <pre className="chat-code-block__pre">
        <code>{code}</code>
      </pre>
    </div>
  )
}

// ── MessageActions ────────────────────────────────────────────────────────────

function MessageActions({
  content,
  onRetry,
}: {
  content: string
  onRetry: () => void
}) {
  const [copied, setCopied] = useState(false)
  const [feedback, setFeedback] = useState<"up" | "down" | null>(null)

  const handleCopy = async () => {
    await navigator.clipboard.writeText(content)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  return (
    <div className="chat-message-actions">
      <button
        type="button"
        className="chat-action-btn"
        onClick={handleCopy}
        title="Copy response"
      >
        {copied ? <Check size={13} /> : <Copy size={13} />}
      </button>
      <button
        type="button"
        className="chat-action-btn"
        onClick={onRetry}
        title="Retry"
      >
        <RefreshCw size={13} />
      </button>
      <div className="chat-action-divider" />
      <button
        type="button"
        className={`chat-action-btn ${feedback === "up" ? "chat-action-btn--active-good" : ""}`}
        onClick={() => setFeedback(feedback === "up" ? null : "up")}
        title="Good response"
      >
        <ThumbsUp size={13} />
      </button>
      <button
        type="button"
        className={`chat-action-btn ${feedback === "down" ? "chat-action-btn--active-bad" : ""}`}
        onClick={() => setFeedback(feedback === "down" ? null : "down")}
        title="Bad response"
      >
        <ThumbsDown size={13} />
      </button>
    </div>
  )
}

// ── ThinkingBlock ─────────────────────────────────────────────────────────────
// Collapsible display of extended thinking content from deep-work mode.

function ThinkingBlock({
  content,
  isStreaming,
}: {
  content: string
  isStreaming: boolean
}) {
  const [expanded, setExpanded] = useState(false)

  return (
    <div className="chat-thinking-block">
      <button
        type="button"
        className="chat-thinking-toggle"
        onClick={() => setExpanded(!expanded)}
      >
        <Brain size={14} className="chat-thinking-icon" />
        <span>{isStreaming ? "Thinking..." : "Thought process"}</span>
        {isStreaming && <LoaderCircle className="spin" size={12} />}
        <ChevronDown
          size={14}
          className={`chat-thinking-chevron ${expanded ? "chat-thinking-chevron--open" : ""}`}
        />
      </button>
      {expanded && <pre className="chat-thinking-content">{content}</pre>}
    </div>
  )
}

// ── PlanBlock ─────────────────────────────────────────────────────────────────
// Shows the proposed execution plan with its DAG of executables.

function PlanBlock({
  plan,
  defaultOpen = true,
}: {
  plan: StreamPlanProposed
  defaultOpen?: boolean
}) {
  const [expanded, setExpanded] = useState(defaultOpen)

  return (
    <div className="chat-plan-block">
      <button
        type="button"
        className="chat-plan-toggle"
        onClick={() => setExpanded(!expanded)}
      >
        <Sparkles size={14} className="chat-plan-icon" />
        <span>{plan.title || "Execution Plan"}</span>
        <span className="chat-plan-count">
          {plan.executables.length} step
          {plan.executables.length !== 1 ? "s" : ""}
        </span>
        <ChevronDown
          size={14}
          className={`chat-plan-chevron ${expanded ? "chat-plan-chevron--open" : ""}`}
        />
      </button>
      {expanded && (
        <div className="chat-plan-body">
          {plan.objective && (
            <p className="chat-plan-objective">{plan.objective}</p>
          )}
          <ol className="chat-plan-steps">
            {plan.executables.map((exec) => (
              <li key={exec.id} className="chat-plan-step">
                <span className="chat-plan-step-name">{exec.name}</span>
                {exec.description && (
                  <span className="chat-plan-step-desc">
                    {exec.description}
                  </span>
                )}
              </li>
            ))}
          </ol>
        </div>
      )}
    </div>
  )
}

// ── SubagentResultsBlock ──────────────────────────────────────────────────────
// Renders subagent results as they stream in from plan fan-out.

function SubagentResultsBlock({
  results,
  defaultOpen = true,
}: {
  results: Array<StreamSubagentResult>
  defaultOpen?: boolean
}) {
  const [expanded, setExpanded] = useState(defaultOpen)
  const doneCount = results.filter((r) => !r.error).length
  const errorCount = results.filter((r) => r.error).length

  return (
    <div className="chat-subagent-results">
      <button
        type="button"
        className="chat-plan-toggle"
        onClick={() => setExpanded(!expanded)}
      >
        <Bot size={14} className="chat-plan-icon" />
        <span>
          {results.length} subagent{results.length !== 1 ? "s" : ""}
          {doneCount > 0 && ` · ${doneCount} done`}
          {errorCount > 0 && ` · ${errorCount} failed`}
        </span>
        <ChevronDown
          size={14}
          className={`chat-plan-chevron ${expanded ? "chat-plan-chevron--open" : ""}`}
        />
      </button>
      {expanded &&
        results.map((result) => (
          <SubagentResultCard key={result.id} result={result} />
        ))}
    </div>
  )
}

function SubagentResultCard({ result }: { result: StreamSubagentResult }) {
  const [open, setOpen] = useState(false)
  const failed = Boolean(result.error)

  return (
    <div className={`subagent-card ${failed ? "subagent-card-error" : ""}`}>
      <button
        type="button"
        className="subagent-card-header"
        onClick={() => setOpen((v) => !v)}
      >
        {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        <Bot size={12} />
        <span className="subagent-card-id">{result.id}</span>
        {result.model && (
          <code className="subagent-card-model">{result.model.split("-").slice(0, 2).join("-")}</code>
        )}
        <span className="subagent-card-meta">
          {result.turns > 0 && `${result.turns} turns · `}
          {result.duration_ms > 0 && formatDurationMs(result.duration_ms)}
        </span>
      </button>
      {open && (
        <div className="subagent-card-body">
          {result.system_prompt && (
            <details className="subagent-card-prompt">
              <summary>System prompt</summary>
              <pre>{result.system_prompt}</pre>
            </details>
          )}
          <p className="subagent-card-task">
            <strong>Task:</strong> {result.task}
          </p>
          {result.error ? (
            <p className="subagent-card-err">Error: {result.error}</p>
          ) : (
            result.output && (
              <pre className="subagent-card-output">
                {result.output.length > 2000
                  ? result.output.slice(0, 2000) + "..."
                  : result.output}
              </pre>
            )
          )}
        </div>
      )}
    </div>
  )
}

function formatDurationMs(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}
