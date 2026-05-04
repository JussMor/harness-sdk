"use client"

import { useEffect, useState } from "react"

import { AIActions, commonActions } from "@/components/ai-actions"
import type { RunnerThreadProps } from "@/components/ai-agents"
import { RunnerThread } from "@/components/ai-agents"
import type { AIPromptInputMode } from "@/components/ai-prompt-input"
import { AIPromptInput } from "@/components/ai-prompt-input"
import type { ChainOfThoughtStep } from "@/components/chain-of-thought"
import { ChainOfThought } from "@/components/chain-of-thought"

export interface ChatMessage {
  id: string
  content: string
  role: "user" | "assistant"
  model?: string
  providerReasoning?: string
  attachments?: Array<string>
  chainOfThought?: boolean
  chainOfThoughtSteps?: Array<ChainOfThoughtStep>
  showContext?: boolean
  parallelAgents?: Array<RunnerThreadProps>
}

export interface ChatMainProps {
  userName?: string
  showGreeting?: boolean
  backendBaseURL?: string
  activeChatID?: string
  onChatCreated?: (chatId: string) => void
}

interface BackendRunner {
  id?: string
  tier?: string
  task?: string
  status?: string
  result?: string
  model?: string
}

interface BackendMessage {
  id?: number | string
  role?: string
  content?: string
  model?: string
}

interface RunOptionsPayload {
  stream?: boolean
  planner?: string
  autoApprovePlan?: boolean
  verification?: string
  verificationMinLen?: number
  maxVerifyRetry?: number
  enableSafety?: boolean
  enableOutputFilter?: boolean
  maxOutputChars?: number
  session?: {
    timezone?: string
    locale?: string
    userName?: string
    surface?: string
  }
}

interface BackendTraceStep {
  id?: string
  type?: "search" | "result" | "action" | "thinking"
  title?: string
  content?: string
  details?: Array<string>
}

const fallbackModes: Array<AIPromptInputMode> = [
  { id: "balanced", name: "Balanced" },
  { id: "analyst", name: "Analyst" },
  { id: "code-agent", name: "Code Agent" },
  { id: "code-reviewer", name: "Code Reviewer" },
  { id: "deep-work", name: "Deep Work" },
]

export function ChatMain({
  userName = "Toby",
  showGreeting = true,
  backendBaseURL = "http://localhost:8080",
  activeChatID,
  onChatCreated,
}: ChatMainProps) {
  const [messages, setMessages] = useState<Array<ChatMessage>>([])
  const [isLoading, setIsLoading] = useState(false)
  const [chatID, setChatID] = useState<number | null>(null)
  const [modes, setModes] = useState<Array<AIPromptInputMode>>(fallbackModes)
  const [selectedMode, setSelectedMode] = useState("balanced")
  const [providers, setProviders] = useState<Array<string>>([])
  const [selectedProvider, setSelectedProvider] = useState("")
  const [streamEnabled, setStreamEnabled] = useState(true)
  const [activeRunID, setActiveRunID] = useState<string | null>(null)
  const [pendingAssistantID, setPendingAssistantID] = useState<string | null>(
    null
  )

  useEffect(() => {
    const controller = new AbortController()

    const loadModes = async () => {
      try {
        const res = await fetch(`${backendBaseURL}/api/modes`, {
          signal: controller.signal,
        })
        if (!res.ok) return

        const payload = (await res.json()) as Array<{
          id?: string
          name?: string
        }>

        const nextModes = payload
          .filter((mode) => mode.id && mode.name)
          .map((mode) => ({
            id: String(mode.id),
            name: String(mode.name),
          }))
          .sort((left, right) => left.name.localeCompare(right.name))

        if (nextModes.length === 0) return

        setModes(nextModes)
        if (!nextModes.some((mode) => mode.id === selectedMode)) {
          setSelectedMode(nextModes[0].id)
        }
      } catch (error) {
        if (error instanceof Error && error.name === "AbortError") {
          return
        }
      }
    }

    void loadModes()

    return () => controller.abort()
  }, [backendBaseURL])

  useEffect(() => {
    const controller = new AbortController()

    const loadProviders = async () => {
      try {
        const res = await fetch(`${backendBaseURL}/api/providers`, {
          signal: controller.signal,
        })
        if (!res.ok) return

        const payload = (await res.json()) as {
          default?: string
          providers?: Array<{ name?: string }>
        }

        const names = (payload.providers || [])
          .map((provider) => String(provider?.name || "").trim())
          .filter(Boolean)

        if (names.length === 0) {
          return
        }

        setProviders(names)
        setSelectedProvider((prev) => {
          if (prev && names.includes(prev)) {
            return prev
          }
          const fallback = String(payload.default || "").trim()
          if (fallback && names.includes(fallback)) {
            return fallback
          }
          return names[0]
        })
      } catch (error) {
        if (error instanceof Error && error.name === "AbortError") {
          return
        }
      }
    }

    void loadProviders()

    return () => controller.abort()
  }, [backendBaseURL])

  useEffect(() => {
    const numericID = Number(activeChatID)
    if (!activeChatID || Number.isNaN(numericID)) {
      setChatID(null)
      setMessages([])
      return
    }

    setChatID(numericID)
    const controller = new AbortController()

    const loadMessages = async () => {
      try {
        const res = await fetch(
          `${backendBaseURL}/api/chats/${numericID}/messages`,
          {
            signal: controller.signal,
          }
        )
        if (!res.ok) {
          return
        }

        const payload = (await res.json()) as Array<BackendMessage>
        const nextMessages = payload
          .filter((msg) => {
            const role = String(msg?.role || "").toLowerCase()
            return (role === "user" || role === "assistant") && !!msg?.content
          })
          .map((msg) => ({
            id: String(
              msg.id || `msg-${Math.random().toString(36).slice(2, 8)}`
            ),
            role: String(msg.role || "assistant").toLowerCase() as
              | "user"
              | "assistant",
            content: String(msg.content || ""),
            model: msg.model ? String(msg.model) : undefined,
          }))

        setMessages((prev) => {
          if (!activeRunID || !pendingAssistantID) {
            return nextMessages
          }

          const pendingAssistant = prev.find(
            (message) => message.id === pendingAssistantID
          )
          if (!pendingAssistant) {
            return nextMessages
          }

          const alreadyIncluded = nextMessages.some(
            (message) => message.id === pendingAssistantID
          )
          if (alreadyIncluded) {
            return nextMessages
          }

          return [...nextMessages, pendingAssistant]
        })
      } catch (error) {
        if (error instanceof Error && error.name === "AbortError") {
          return
        }
      }
    }

    void loadMessages()
    return () => controller.abort()
  }, [activeChatID, activeRunID, backendBaseURL, pendingAssistantID])

  const handleSubmit = async (prompt: string) => {
    const trimmedPrompt = prompt.trim()
    if (!trimmedPrompt) return

    const optimisticUserMessage: ChatMessage = {
      id: `user-${Date.now()}`,
      content: trimmedPrompt,
      role: "user",
    }

    const clientRunId = `run-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
    const nextPendingAssistantID = `assistant-pending-${clientRunId}`
    const pendingAssistantMessage: ChatMessage = {
      id: nextPendingAssistantID,
      content: "Ejecutando subtareas...",
      role: "assistant",
      model: selectedMode,
      chainOfThought: true,
      chainOfThoughtSteps: [],
      parallelAgents: [],
    }

    // Render user message immediately, even if backend is slow/unavailable.
    setMessages((prev) => [
      ...prev,
      optimisticUserMessage,
      pendingAssistantMessage,
    ])
    setActiveRunID(clientRunId)
    setPendingAssistantID(nextPendingAssistantID)
    setIsLoading(true)

    try {
      let activeChatID = chatID
      if (!activeChatID) {
        const createRes = await fetch(`${backendBaseURL}/api/chats`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ title: "Chat App Session" }),
        })

        if (!createRes.ok) {
          throw new Error(`No se pudo crear chat (${createRes.status})`)
        }

        const created = await createRes.json()
        activeChatID = created.id
        setChatID(activeChatID)
        onChatCreated?.(String(activeChatID))
      }

      const controller = new AbortController()
      const timeout = setTimeout(() => controller.abort(), 30000)

      const basePayload = {
        prompt: trimmedPrompt,
        mode: selectedMode,
        provider: selectedProvider || undefined,
        clientRunId,
      }

      let assistantMessage: ChatMessage
      try {
        if (streamEnabled) {
          try {
            const streamRes = await fetch(
              `${backendBaseURL}/api/chats/${activeChatID}/run-stream`,
              {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                  ...basePayload,
                  options: buildRunOptions(userName, true),
                }),
                signal: controller.signal,
              }
            )

            if (!streamRes.ok || !streamRes.body) {
              const errorText = await streamRes.text()
              throw new Error(
                errorText ||
                  `Error en ejecución del chat stream (${streamRes.status})`
              )
            }

            assistantMessage = await consumeStreamedRun(
              streamRes,
              nextPendingAssistantID,
              selectedMode,
              (delta) => {
                setMessages((prev) =>
                  prev.map((message) => {
                    if (message.id !== nextPendingAssistantID) {
                      return message
                    }
                    return {
                      ...message,
                      content: `${message.content || ""}${delta}`,
                    }
                  })
                )
              }
            )
          } catch {
            const runRes = await fetch(
              `${backendBaseURL}/api/chats/${activeChatID}/run`,
              {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                  ...basePayload,
                  options: buildRunOptions(userName, false),
                }),
                signal: controller.signal,
              }
            )

            if (!runRes.ok) {
              const errorText = await runRes.text()
              throw new Error(
                errorText || `Error en ejecución del chat (${runRes.status})`
              )
            }

            const payload = await runRes.json()
            assistantMessage = {
              id: String(payload?.assistant?.id ?? `assistant-${Date.now()}`),
              content:
                payload?.assistant?.content ??
                "No llegó contenido del backend para esta respuesta.",
              model: payload?.assistant?.model,
              providerReasoning:
                typeof payload?.assistant?.reasoning === "string"
                  ? payload.assistant.reasoning
                  : undefined,
              role: "assistant",
              chainOfThought: true,
              chainOfThoughtSteps: buildTraceSteps(payload?.assistant?.trace),
              showContext: true,
              parallelAgents:
                buildParallelAgents(
                  payload?.assistant?.runners,
                  String(payload?.assistant?.model ?? selectedMode)
                ) || [],
            }
          }
        } else {
          const runRes = await fetch(
            `${backendBaseURL}/api/chats/${activeChatID}/run`,
            {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({
                ...basePayload,
                options: buildRunOptions(userName, false),
              }),
              signal: controller.signal,
            }
          )

          if (!runRes.ok) {
            const errorText = await runRes.text()
            throw new Error(
              errorText || `Error en ejecución del chat (${runRes.status})`
            )
          }

          const payload = await runRes.json()
          assistantMessage = {
            id: String(payload?.assistant?.id ?? `assistant-${Date.now()}`),
            content:
              payload?.assistant?.content ??
              "No llegó contenido del backend para esta respuesta.",
            model: payload?.assistant?.model,
            providerReasoning:
              typeof payload?.assistant?.reasoning === "string"
                ? payload.assistant.reasoning
                : undefined,
            role: "assistant",
            chainOfThought: true,
            chainOfThoughtSteps: buildTraceSteps(payload?.assistant?.trace),
            showContext: true,
            parallelAgents:
              buildParallelAgents(
                payload?.assistant?.runners,
                String(payload?.assistant?.model ?? selectedMode)
              ) || [],
          }
        }
      } finally {
        clearTimeout(timeout)
      }

      setMessages((prev) =>
        prev.map((message) => {
          if (message.id !== nextPendingAssistantID) {
            return message
          }

          return {
            ...message,
            ...assistantMessage,
            role: "assistant",
            chainOfThought:
              assistantMessage.chainOfThought ?? message.chainOfThought,
            chainOfThoughtSteps:
              assistantMessage.chainOfThoughtSteps ??
              message.chainOfThoughtSteps,
            parallelAgents:
              assistantMessage.parallelAgents ?? message.parallelAgents,
          }
        })
      )
      setActiveRunID(null)
      setPendingAssistantID(null)
    } catch (error) {
      const extra =
        error instanceof Error && error.name === "AbortError"
          ? " Tiempo de espera agotado (30s)."
          : ""

      const fallback: ChatMessage = {
        id: nextPendingAssistantID,
        content: `No pude obtener respuesta del backend-chat.${extra}`,
        role: "assistant",
      }
      setMessages((prev) =>
        prev.map((message) =>
          message.id === nextPendingAssistantID ? fallback : message
        )
      )
      setActiveRunID(null)
      setPendingAssistantID(null)
    } finally {
      setIsLoading(false)
    }
  }

  const handleFileSelect = (files: Array<File>) => {
    console.log("Files selected:", files)
  }

  return (
    <div className="flex flex-1 flex-col overflow-hidden bg-white">
      {/* Header / Greeting */}
      {showGreeting && messages.length === 0 && (
        <div className="flex flex-1 flex-col items-center justify-center space-y-4 py-12">
          <div className="h-24 w-24 rounded-full bg-linear-to-br from-pink-300 via-purple-300 to-blue-300" />
          <h1 className="text-4xl font-semibold text-gray-900">
            Good Morning, {userName}
          </h1>
          <p className="text-xl text-gray-600">
            How Can I <span className="text-purple-500">Assist You Today?</span>
          </p>
        </div>
      )}

      {/* Messages */}
      <div className="flex-1 space-y-4 overflow-y-auto px-6 py-4">
        {messages.map((message) => (
          <div key={message.id} className="space-y-2">
            {/* Message */}
            <div
              className={`flex ${
                message.role === "user" ? "justify-end" : "justify-start"
              }`}
            >
              <div
                className={`max-w-md rounded-lg px-4 py-2 ${
                  message.role === "user"
                    ? "bg-blue-100 text-gray-900"
                    : "bg-gray-100 text-gray-700"
                }`}
              >
                <p className="text-sm">{message.content}</p>
              </div>
            </div>

            {message.role === "assistant" && (
              <div className="mt-2 ml-2 max-w-4xl space-y-3">
                {message.parallelAgents &&
                  message.parallelAgents.length > 0 && (
                    <div className="rounded-2xl border border-gray-200 bg-white p-3 shadow-sm">
                      <div className="mb-3 text-xs font-semibold tracking-wide text-gray-600 uppercase">
                        Subagent Thread
                      </div>
                      <div className="space-y-3 border-l-2 border-gray-200 pl-3">
                        {message.parallelAgents.map((agent) => (
                          <div key={`${message.id}-${agent.id}`}>
                            <RunnerThread {...agent} />
                          </div>
                        ))}
                      </div>
                    </div>
                  )}

                {message.chainOfThought &&
                  message.chainOfThoughtSteps &&
                  message.chainOfThoughtSteps.length > 0 && (
                    <ChainOfThought
                      steps={message.chainOfThoughtSteps}
                      title="SDK Reasoning Trace"
                    />
                  )}

                {message.providerReasoning && (
                  <div className="rounded-2xl border border-amber-200 bg-amber-50 p-4 shadow-sm">
                    <div className="mb-2 text-sm font-semibold text-amber-900">
                      Provider reasoning
                    </div>
                    <pre className="overflow-x-auto text-sm whitespace-pre-wrap text-amber-950">
                      {message.providerReasoning}
                    </pre>
                  </div>
                )}

                {/* Context */}
                {/* {message.showContext && (
                  <AIContext
                    items={exampleContextItems}
                    title="Response Context"
                  />
                )} */}

                {/* Actions */}
                <div className="flex gap-2">
                  <AIActions
                    actions={[
                      commonActions.copy(message.content),
                      commonActions.like(() => console.log("liked")),
                      commonActions.dislike(() => console.log("disliked")),
                    ]}
                  />
                </div>
              </div>
            )}
          </div>
        ))}

        {isLoading && (
          <div className="flex justify-start">
            <div className="rounded-lg bg-gray-100 px-4 py-3">
              <div className="flex gap-1">
                <div className="h-2 w-2 animate-bounce rounded-full bg-gray-400" />
                <div
                  className="h-2 w-2 animate-bounce rounded-full bg-gray-400"
                  style={{ animationDelay: "0.2s" }}
                />
                <div
                  className="h-2 w-2 animate-bounce rounded-full bg-gray-400"
                  style={{ animationDelay: "0.4s" }}
                />
              </div>
            </div>
          </div>
        )}
      </div>

      {/* Input */}
      <div className="border-t bg-gray-50 px-6 py-4">
        <div className="mb-3 flex flex-wrap items-center gap-3 text-xs text-gray-700">
          <label className="flex items-center gap-2">
            <span>Provider</span>
            <select
              value={selectedProvider}
              onChange={(event) => setSelectedProvider(event.target.value)}
              className="rounded border border-gray-300 bg-white px-2 py-1"
              disabled={isLoading || providers.length === 0}
            >
              {providers.length === 0 && <option value="">default</option>}
              {providers.map((provider) => (
                <option key={provider} value={provider}>
                  {provider}
                </option>
              ))}
            </select>
          </label>

          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={streamEnabled}
              onChange={(event) => setStreamEnabled(event.target.checked)}
              disabled={isLoading}
            />
            <span>Streaming</span>
          </label>
        </div>

        <div className="flex items-start gap-4">
          <div className="flex-1">
            <AIPromptInput
              onSubmit={handleSubmit}
              onFileSelect={handleFileSelect}
              isLoading={isLoading}
              modes={modes}
              selectedMode={selectedMode}
              onModeChange={setSelectedMode}
              placeholder="Ask me anything..."
            />
          </div>
        </div>
      </div>
    </div>
  )
}

function buildRunOptions(
  userName: string,
  streamEnabled: boolean
): RunOptionsPayload {
  return {
    stream: streamEnabled,
    planner: "heuristic",
    autoApprovePlan: true,
    verification: "completion",
    verificationMinLen: 10,
    maxVerifyRetry: 2,
    enableSafety: true,
    enableOutputFilter: true,
    maxOutputChars: 10_000,
    session: {
      locale: "es-EC",
      timezone: "America/Guayaquil",
      userName,
      surface: "chat",
    },
  }
}

async function consumeStreamedRun(
  response: Response,
  pendingAssistantID: string,
  fallbackModel: string,
  onDelta: (delta: string) => void
): Promise<ChatMessage> {
  const decoder = new TextDecoder()
  const reader = response.body?.getReader()
  if (!reader) {
    throw new Error("No se pudo leer stream del backend")
  }

  let buffer = ""
  let finalMessage: ChatMessage | null = null

  const processEventChunk = (chunk: string) => {
    const lines = chunk.split("\n")
    let eventType = ""
    const dataLines: Array<string> = []
    for (const rawLine of lines) {
      const line = rawLine.trimEnd()
      if (line.startsWith("event:")) {
        eventType = line.slice("event:".length).trim()
      } else if (line.startsWith("data:")) {
        dataLines.push(line.slice("data:".length).trim())
      }
    }

    const dataText = dataLines.join("\n")
    if (!dataText) {
      return
    }

    const payload = JSON.parse(dataText) as {
      delta?: string
      error?: string
      message?: {
        id?: string | number
        content?: string
        role?: string
        model?: string
      }
    }

    if (eventType === "error" || payload.error) {
      throw new Error(payload.error || "Error en stream")
    }

    if (eventType === "delta" && typeof payload.delta === "string") {
      onDelta(payload.delta)
      return
    }

    if (eventType === "done") {
      finalMessage = {
        id: String(payload.message?.id || pendingAssistantID),
        role: "assistant",
        content: String(payload.message?.content || ""),
        model: String(payload.message?.model || fallbackModel),
      }
    }
  }

  while (true) {
    const { done, value } = await reader.read()
    if (done) {
      break
    }
    buffer += decoder.decode(value, { stream: true })
    const parts = buffer.split("\n\n")
    buffer = parts.pop() || ""
    for (const part of parts) {
      if (!part.trim()) {
        continue
      }
      processEventChunk(part)
    }
  }

  if (buffer.trim()) {
    processEventChunk(buffer)
  }

  if (!finalMessage) {
    return {
      id: pendingAssistantID,
      role: "assistant",
      content: "",
      model: fallbackModel,
    }
  }

  return finalMessage
}

function toRunnerAgent(
  runner: BackendRunner,
  fallbackModel: string
): RunnerThreadProps {
  return {
    id: String(runner.id || "runner"),
    task: runner.task || "Task",
    status: normalizeRunnerStatus(runner.status),
    result: runner.result,
    tier: runner.tier,
    model: runner.model || fallbackModel,
  }
}

function normalizeRunnerStatus(
  status: string | undefined
): RunnerThreadProps["status"] {
  const value = String(status || "")
    .trim()
    .toLowerCase()

  switch (value) {
    case "queued":
    case "pending":
    case "planned":
      return "pending"
    case "in_progress":
    case "running":
      return "running"
    case "success":
    case "completed":
    case "done":
      return "completed"
    case "failure":
    case "failed":
    case "error":
      return "failed"
    default:
      return "pending"
  }
}

function buildParallelAgents(
  runners: unknown,
  fallbackModel: string
): Array<RunnerThreadProps> | null {
  if (!Array.isArray(runners) || runners.length === 0) {
    return null
  }

  const agents = runners
    .filter(
      (runner): runner is BackendRunner =>
        typeof runner === "object" && runner !== null
    )
    .map((runner) => toRunnerAgent(runner, fallbackModel))

  return agents.length > 0 ? agents : null
}

function buildTraceSteps(trace: unknown): Array<ChainOfThoughtStep> {
  if (!Array.isArray(trace)) {
    return []
  }

  return trace
    .filter(
      (step): step is BackendTraceStep =>
        typeof step === "object" && step !== null
    )
    .map(toTraceStep)
}

function toTraceStep(step: BackendTraceStep): ChainOfThoughtStep {
  return {
    id: String(step.id || `trace-${Date.now()}`),
    type: step.type || "thinking",
    title: step.title || "Trace step",
    content: step.content,
    details: step.details,
  }
}
