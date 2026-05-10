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
  StreamComponentArtifact,
  StreamEvent,
  StreamInterruptRequest,
  StreamPlanProposed,
  StreamSubagentResult,
} from "@/features/chat/types"
import { componentCatalog } from "@/lib/component-catalog"
import { ArtifactRenderer } from "@harness/react"
import {
  Bot,
  Brain,
  Check,
  ChevronDown,
  ChevronRight,
  ClipboardList,
  Copy,
  Database,
  LoaderCircle,
  RefreshCw,
  SendHorizontal,
  Shield,
  ShieldAlert,
  Sparkles,
  Square,
  ThumbsDown,
  ThumbsUp,
} from "lucide-react"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import ReactMarkdown from "react-markdown"
import remarkGfm from "remark-gfm"
import { InterruptDialog } from "./interrupt-dialog"
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

  // ── Human-in-the-Loop state ──────────────────────────────────────────────
  const [hilEnabled, setHilEnabled] = useState(true)
  const [pendingInterrupt, setPendingInterrupt] =
    useState<StreamInterruptRequest | null>(null)

  // ── Plan-mode state ──────────────────────────────────────────────────────
  // The agent has entered plan mode (EnterPlanMode tool fired). The flag
  // remains true until a plan_mode_changed event with state === "exited"
  // arrives (approval, rejection, or auto-restore).
  const [inPlanMode, setInPlanMode] = useState(false)

  // ── Compaction signal ────────────────────────────────────────────────────
  // Last compaction event the runtime emitted. When non-null, a transient
  // banner shows "Memory compacted — N messages summarised". Cleared after
  // a few seconds.
  const [lastCompaction, setLastCompaction] = useState<{
    at: number
    dropped: number
    summary?: string
  } | null>(null)

  // ── Generative UI artifacts (component artifacts from the SDK) ─────────────
  const [componentArtifacts, setComponentArtifacts] = useState<
    Array<StreamComponentArtifact>
  >([])
  // Canvas-placement component currently mounted in the side panel.
  const [canvasComponent, setCanvasComponent] =
    useState<StreamComponentArtifact | null>(null)

  const streamControllerRef = useRef<AbortController | null>(null)
  const listEndRef = useRef<HTMLDivElement>(null)

  // ── Artifact Canvas state ────────────────────────────────────────────────
  const [activeArtifact, setActiveArtifact] = useState<Artifact | null>(null)
  const [allArtifacts, setAllArtifacts] = useState<Array<Artifact>>([])
  const [isArtifactStreaming, setIsArtifactStreaming] = useState(false)
  const detectorRef = useRef(createDetectorState())
  // Pending file_write previews indexed by tool name. Populated on tool_call,
  // consumed (and revealed in the canvas) only on tool_result so an interrupt
  // rejection never leaves a phantom artifact in the UI.
  const pendingFilePreviewsRef = useRef<
    Array<{ path: string; content: string }>
  >([])

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

  // Resolve an interactive component's submission by POSTing the user's data
  // to /api/interrupts/:token/resolve. The agent loop, paused via
  // RequestInterrupt on the backend, receives the answer as the tool result.
  const handleInteractionSubmit = useCallback(
    async (interaction: { token: string; chat_id?: number }, data: unknown) => {
      const cid = interaction.chat_id ?? chatID
      if (!cid) return
      try {
        await api.resolveInterrupt(interaction.token, cid, {
          approved: true,
          answer: data,
        })
        setComponentArtifacts((prev) =>
          prev.filter((a) => a.interaction?.token !== interaction.token)
        )
        setCanvasComponent((prev) =>
          prev?.interaction?.token === interaction.token ? null : prev
        )
        pushTimeline("Form submitted", "success")
      } catch (err) {
        pushTimeline(
          `Form submit failed: ${err instanceof Error ? err.message : String(err)}`,
          "error"
        )
      }
    },
    [chatID, pushTimeline]
  )

  const syncMessages = useCallback(
    async (
      targetChatID: number,
      signal?: AbortSignal,
      finalizedAssistantId?: string
    ) => {
      const payload = await api.listMessages(targetChatID, signal)
      const next = payload.map(toChatMessage)

      // Preserve streaming-only fields (plan, subagentResults) that aren't
      // persisted in the backend but were accumulated during the stream.
      // The local streaming message has a temporary id (e.g. "local-assistant-…")
      // that won't match the new numeric DB id, so we find the most recent
      // prev assistant message with these fields and graft them onto the
      // LAST assistant message in next (the one we just finalized).
      setMessages((prev) => {
        const prevMap = new Map(prev.map((m) => [m.id, m]))

        // Only carry streaming-only fields from the assistant message that
        // belongs to the run we're finalizing right now.
        const source = finalizedAssistantId
          ? prev.find((m) => m.id === finalizedAssistantId)
          : undefined
        const streamFields =
          source &&
          source.role === "assistant" &&
          (source.plan ||
            (source.subagentResults && source.subagentResults.length > 0))
            ? {
                plan: source.plan,
                subagentResults: source.subagentResults,
              }
            : undefined

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
          if (idx === lastAssistantIdx && streamFields) {
            return { ...m, ...streamFields }
          }
          return m
        })
      })

      // Reconstruct artifacts from the artifacts API (real DB UUIDs + latest content).
      // This ensures onSaveVersion can target the correct DB record and version
      // navigation works after a page reload.
      try {
        const dbArtifacts = await api.listArtifacts(targetChatID)
        const restored: Array<Artifact> = dbArtifacts.map((a) => ({
          id: a.id,
          language: a.language,
          content: a.latestContent ?? "",
          complete: true,
          title: a.title || `${a.language} artifact`,
        }))
        setAllArtifacts(restored)
        // If the currently-displayed artifact was a temp local one (e.g. "file-…"),
        // replace it with its DB counterpart so save operations have a real ID.
        setActiveArtifact((prev) => {
          if (!prev) return prev
          const match = restored.find(
            (r) =>
              r.id === prev.id ||
              (r.language === prev.language && r.title === prev.title)
          )
          return match ?? prev
        })
      } catch {
        // Non-fatal — keep whatever was already in allArtifacts from the stream.
      }
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

  // Auto-dismiss the "memory compacted" banner after a short window so it
  // doesn't linger across turns. Cleared eagerly when a new event arrives.
  useEffect(() => {
    if (!lastCompaction) return
    const t = setTimeout(() => setLastCompaction(null), 6000)
    return () => clearTimeout(t)
  }, [lastCompaction])

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

        // For file_write, stash the args so we can render the artifact AFTER
        // tool_result fires (i.e. after the user approved the interrupt and
        // the write actually happened). Showing it on tool_call would leak a
        // preview even on rejection.
        if (toolName === "file_write" && event.data.args) {
          const args = event.data.args
          const content = (args.content as string) || ""
          const filePath = (args.path as string) || "file"
          if (content) {
            pendingFilePreviewsRef.current.push({
              path: filePath,
              content,
            })
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

        // Reveal the file_write preview only now — and only on success.
        if (toolName === "file_write" && !event.data.error) {
          const next = pendingFilePreviewsRef.current.shift()
          if (next) {
            const fileArtifact: Artifact = {
              id: `file-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
              language: inferLanguageFromPath(next.path),
              content: next.content,
              complete: true,
              title: next.path.split("/").pop() || next.path,
            }
            setActiveArtifact(fileArtifact)
            setAllArtifacts((prev) => [...prev, fileArtifact])
            setIsArtifactStreaming(false)
          }
        } else if (toolName === "file_write" && event.data.error) {
          // Drop the stashed preview on failure so a later success doesn't
          // surface a stale entry.
          pendingFilePreviewsRef.current.shift()
        }

        // dispatch-subagents — surface every file each subagent wrote so
        // they show up in the canvas alongside files written directly by
        // the parent agent. The backend tracks them via fileWriteTracker
        // and embeds them under `files_created` in the tool result.
        if (
          toolName === "dispatch-subagents" &&
          !event.data.error &&
          event.data.content
        ) {
          try {
            const parsed = JSON.parse(event.data.content) as {
              files_created?: Array<{ path?: string; content?: string }>
            }
            const files = parsed.files_created ?? []
            if (files.length > 0) {
              const newArtifacts: Array<Artifact> = files
                .filter((f) => f && f.path && f.content)
                .map((f) => ({
                  id: `subagent-${Date.now()}-${Math.random()
                    .toString(36)
                    .slice(2, 8)}`,
                  language: inferLanguageFromPath(f.path!),
                  content: f.content!,
                  complete: true,
                  title: f.path!.split("/").pop() || f.path!,
                }))
              if (newArtifacts.length > 0) {
                setAllArtifacts((prev) => [...prev, ...newArtifacts])
                setActiveArtifact(newArtifacts[newArtifacts.length - 1])
                setIsArtifactStreaming(false)
              }
            }
          } catch {
            // tool may have returned a non-JSON error string — ignore
          }
        }

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

      if (event.type === "turn_complete") {
        pushTimeline("LLM turn complete — dispatching tools", "info")
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

      if (event.type === "agent_result") {
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

      if (event.type === "interrupt_required") {
        const req = event.data
        // Form-input interrupts paired with a UIHint (component name) are
        // already rendered as an interactive component artifact — the
        // QuestionnaireForm/PatientIntakeForm modal posts its own resolution
        // via interaction.token. Suppress the generic InterruptDialog so
        // the user only sees one surface and only one resolve fires.
        if (req.kind === "form_input" && req.form?.ui_hint) {
          pushTimeline(
            `Awaiting input: ${req.form.title ?? req.form.ui_hint}`,
            "info"
          )
          return
        }
        const label =
          req.kind === "approval"
            ? `Awaiting approval: ${req.approval?.tool_call?.name ?? "tool"}`
            : req.kind === "question"
              ? "Agent needs clarification"
              : `Awaiting input: ${req.form?.title ?? "form"}`
        pushTimeline(label, "info")
        setPendingInterrupt(req)
        return
      }

      if (event.type === "interrupt_resolved") {
        setPendingInterrupt(null)
        return
      }

      if (
        event.type === "artifact_created" ||
        event.type === "artifact_updated"
      ) {
        const a = event.data
        if (a.kind === "component") {
          const placement = a.placement ?? "canvas"
          if (placement === "canvas") {
            // Mount in the dedicated side panel; close any open file canvas
            // so the two surfaces don't fight for the same slot.
            setCanvasComponent(a)
            setActiveArtifact(null)
            setIsArtifactStreaming(false)
          } else {
            setComponentArtifacts((prev) => {
              const idx = prev.findIndex((x) => x.id === a.id)
              if (idx === -1) return [...prev, a]
              const next = prev.slice()
              next[idx] = a
              return next
            })
          }
          pushTimeline(
            `Rendered ${a.component?.name ?? "component"} (${placement})`,
            "info"
          )
        } else if (a.kind === "file" && a.file) {
          // File artifact — show in the artifact canvas
          const fileArt: Artifact = {
            id: a.id,
            language: a.file.language ?? "",
            content: a.file.content ?? "",
            complete: true,
            title: a.file.title ?? a.file.path ?? "Untitled",
          }
          setActiveArtifact(fileArt)
          setAllArtifacts((prev) => {
            const idx = prev.findIndex((x) => x.id === fileArt.id)
            if (idx === -1) return [...prev, fileArt]
            const next = prev.slice()
            next[idx] = fileArt
            return next
          })
          setIsArtifactStreaming(false)
          pushTimeline(
            `File artifact: ${a.file.title ?? a.file.path ?? "file"}`,
            "info"
          )
        }
        return
      }

      if (event.type === "compaction") {
        // Surface a transient banner + timeline note. Lets the user know
        // why earlier messages might appear summarised in subsequent turns.
        const dropped = event.data.messages_dropped ?? 0
        setLastCompaction({
          at: Date.now(),
          dropped,
          summary: event.data.summary,
        })
        pushTimeline(
          dropped > 0
            ? `Memory compacted — ${dropped} message${dropped === 1 ? "" : "s"} summarised`
            : "Memory compacted",
          "info"
        )
        return
      }

      if (event.type === "plan_mode_changed") {
        const state = event.data.state
        if (state === "entered") {
          setInPlanMode(true)
          pushTimeline("Plan mode engaged — agent will not edit", "info")
        } else {
          setInPlanMode(false)
          pushTimeline(
            event.data.reason
              ? `Plan mode exited: ${event.data.reason}`
              : "Plan mode exited",
            "info"
          )
        }
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
    pendingFilePreviewsRef.current = []
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
          human_in_loop: hilEnabled,
        },
        {
          onEvent: (event) => handleStreamEvent(assistantMessageID, event),
        },
        controller.signal
      )

      await syncMessages(targetChatID, undefined, assistantMessageID)
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
      className={`chat-main-root ${activeArtifact || canvasComponent ? "chat-main-root--with-canvas" : ""}`}
    >
      <div className="chat-main-panel">
        <header className="chat-main-header">
          <span className="chat-main-header-title">
            {activeChatID ? `Chat #${activeChatID}` : "New Chat"}
          </span>
          <span className="chat-main-header-sub">{selectedMode}</span>
          {inPlanMode ? (
            <span
              className="chat-mode-badge chat-mode-badge--plan"
              title="Plan mode — agent is gathering info and will not modify anything until you approve"
            >
              <ClipboardList size={12} />
              Plan mode
            </span>
          ) : null}
          <button
            type="button"
            title={
              hilEnabled
                ? "Human-in-the-loop ON — click to disable"
                : "Human-in-the-loop OFF — click to enable"
            }
            onClick={() => setHilEnabled((v) => !v)}
            disabled={isStreaming}
            className={`chat-hil-toggle ${hilEnabled ? "chat-hil-toggle--on" : ""}`}
          >
            {hilEnabled ? <ShieldAlert size={14} /> : <Shield size={14} />}
          </button>
          <div className="chat-main-status">
            {isStreaming ? (
              <LoaderCircle className="spin" size={14} />
            ) : (
              <Sparkles size={14} />
            )}
            <span>{statusText}</span>
          </div>
        </header>

        {lastCompaction ? (
          <div
            className="chat-compaction-banner"
            role="status"
            aria-live="polite"
          >
            <Database size={14} />
            <span>
              Memory compacted
              {lastCompaction.dropped > 0
                ? ` — ${lastCompaction.dropped} earlier message${
                    lastCompaction.dropped === 1 ? "" : "s"
                  } summarised`
                : ""}
            </span>
            <button
              type="button"
              className="chat-compaction-banner-close"
              onClick={() => setLastCompaction(null)}
              aria-label="Dismiss"
            >
              ×
            </button>
          </div>
        ) : null}

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

          {allArtifacts.length > 0 && (
            <span className="chat-main-artifact-count">
              {allArtifacts.length} artifact{allArtifacts.length > 1 ? "s" : ""}
            </span>
          )}
        </div>

        {pendingInterrupt && (
          <InterruptDialog
            request={pendingInterrupt}
            onResolve={async (response) => {
              if (!chatID) return
              try {
                await api.resolveInterrupt(
                  pendingInterrupt.id,
                  chatID,
                  response
                )
              } finally {
                setPendingInterrupt(null)
              }
            }}
            onCancel={async () => {
              // Dismiss === Reject. Without sending a denial the backend
              // gate stays blocked, the agent loop hangs forever, and the
              // model never gets a tool result.
              if (chatID) {
                try {
                  await api.resolveInterrupt(pendingInterrupt.id, chatID, {
                    approved: false,
                  })
                } catch {
                  // best-effort: still clear the dialog so the user
                  // doesn't get stuck
                }
              }
              setPendingInterrupt(null)
            }}
          />
        )}

        <div className="chat-main-grid">
          <div className="chat-main-feed">
            {showGreeting && messages.length === 0 && (
              <div className="chat-main-greeting">
                <p className="chat-main-badge">
                  <Sparkles size={14} />
                  Stream Runtime Ready
                </p>
                <h1>Hello, {userName}</h1>
                <p>
                  Choose a mode and provider above, then start a conversation.
                </p>
              </div>
            )}

            {messages.map((message) => (
              <article
                key={message.id}
                className={`chat-bubble ${
                  message.role === "assistant"
                    ? "chat-bubble-assistant"
                    : "chat-bubble-user"
                }`}
              >
                {message.role === "user" ? (
                  <div className="chat-bubble-user-inner">
                    <MessageContent message={message} />
                  </div>
                ) : (
                  <>
                    <div className="chat-bubble-meta">
                      <Bot size={14} />
                      <span>{message.model || "assistant"}</span>
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
                  </>
                )}
              </article>
            ))}

            {componentArtifacts
              .filter((a) => a.placement === "inline")
              .map((a) => (
                <div
                  key={a.id}
                  className="chat-bubble chat-bubble-assistant chat-inline-artifact"
                >
                  <ArtifactRenderer
                    artifact={a as never}
                    catalog={componentCatalog}
                    onInteractionSubmit={handleInteractionSubmit}
                  />
                </div>
              ))}

            {allArtifacts.length > 0 && (
              <div className="chat-artifacts-list">
                <div className="chat-artifacts-list__header">
                  <h3 className="chat-artifacts-list__title">
                    Artifacts
                    <span className="chat-artifacts-list__count">
                      {allArtifacts.length}
                    </span>
                  </h3>
                </div>
                <div className="chat-artifacts-list__grid">
                  {allArtifacts.map((artifact) => {
                    const isActive = activeArtifact?.id === artifact.id
                    const title =
                      artifact.title ||
                      `${artifact.language || "text"} artifact`
                    const lang = (artifact.language || "txt").toLowerCase()
                    const lines = artifact.content
                      ? artifact.content.split("\n").length
                      : 0
                    return (
                      <button
                        key={artifact.id}
                        type="button"
                        className={`chat-artifact-card ${isActive ? "chat-artifact-card--active" : ""}`}
                        onClick={() => {
                          setActiveArtifact(artifact)
                          setIsArtifactStreaming(false)
                        }}
                        title={title}
                      >
                        <span
                          className="chat-artifact-card__lang"
                          data-lang={lang}
                        >
                          {lang}
                        </span>
                        <span className="chat-artifact-card__body">
                          <span className="chat-artifact-card__name">
                            {title}
                          </span>
                          <span className="chat-artifact-card__meta">
                            {lines > 0
                              ? `${lines} line${lines === 1 ? "" : "s"}`
                              : "—"}
                            {!artifact.complete ? " · streaming" : ""}
                          </span>
                        </span>
                      </button>
                    )
                  })}
                </div>
              </div>
            )}

            <div ref={listEndRef} />
          </div>
        </div>

        <footer className="chat-main-input-wrap">
          <div className="chat-main-textarea-wrap">
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
              placeholder="Message… (Enter to send, Shift+Enter for new line)"
              disabled={isStreaming}
            />
            <div className="chat-main-actions">
              <button
                type="button"
                className="chat-btn chat-btn-muted"
                onClick={stopStream}
                disabled={!isStreaming}
              >
                <Square size={13} />
                Stop
              </button>
              <button
                type="button"
                className="chat-btn chat-btn-primary"
                onClick={() => void sendPrompt()}
                disabled={!input.trim() || isStreaming}
              >
                <SendHorizontal size={15} />
                Send
              </button>
            </div>
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
            // Persist the new version to the backend (best-effort).
            // Only works for artifacts with real DB UUIDs (not temp local IDs).
            const isDbId = artifactId.length === 36 && artifactId.includes("-")
            if (isDbId) {
              try {
                await api.addArtifactVersion(artifactId, newContent)
              } catch {
                // Non-fatal — local state still reflects the edit.
              }
            }
            // Always update local state so the canvas stays in sync.
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

      {/* Component Canvas — generative-UI components routed with placement="canvas" */}
      {!activeArtifact && canvasComponent && (
        <aside className="artifact-canvas">
          <header className="artifact-canvas__header">
            <div className="artifact-canvas__title">
              <span>{canvasComponent.component?.name ?? "component"}</span>
            </div>
            <div className="artifact-canvas__actions">
              <button
                type="button"
                className="artifact-canvas__btn"
                onClick={() => setCanvasComponent(null)}
                title="Close"
                aria-label="Close component canvas"
              >
                ×
              </button>
            </div>
          </header>
          <div className="artifact-canvas__content" style={{ padding: 16 }}>
            <ArtifactRenderer
              artifact={canvasComponent as never}
              catalog={componentCatalog}
              onInteractionSubmit={handleInteractionSubmit}
            />
          </div>
        </aside>
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

  // Restore subagentResults from dispatch-subagents tool traces so they
  // survive page reload. SubagentTrace and StreamSubagentResult share the
  // same shape so the cast is safe.
  const subagentResults = traces
    .flatMap((t) => t.subagents ?? [])
    .map((s) => s as unknown as StreamSubagentResult)

  return {
    id: String(message.id),
    role: message.role,
    content: message.content,
    model: message.model,
    pending: false,
    traces: traces.length > 0 ? traces : undefined,
    subagentResults: subagentResults.length > 0 ? subagentResults : undefined,
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
          <code className="subagent-card-model">
            {result.model.split("-").slice(0, 2).join("-")}
          </code>
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
