import {
  Bot,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  CircleCheck,
  LoaderCircle,
  Wrench,
} from "lucide-react"
import { useState } from "react"

export type ToolTraceStatus = "running" | "success" | "error"

export interface SubagentTrace {
  id: string
  task: string
  output?: string
  turns?: number
  stop_reason?: string
  duration_ms?: number
  error?: string
  /** Model used by this subagent if overridden (e.g. "claude-haiku-4-5-20251001"). */
  model?: string
  /** Custom system prompt assigned to this subagent. */
  system_prompt?: string
}

export interface ToolTrace {
  id: string
  name: string
  args?: Record<string, unknown>
  status: ToolTraceStatus
  result?: string
  error?: boolean
  /** When the tool is `dispatch-subagents`, the parsed subagent results. */
  subagents?: Array<SubagentTrace>
}

interface ToolTraceCardProps {
  trace: ToolTrace
}

export function ToolTraceCard({ trace }: ToolTraceCardProps) {
  const [open, setOpen] = useState(false)

  const isSubagentDispatch =
    trace.name === "dispatch-subagents" &&
    Array.isArray(trace.subagents) &&
    trace.subagents.length > 0

  const StatusIcon =
    trace.status === "running"
      ? LoaderCircle
      : trace.status === "error"
        ? CircleAlert
        : CircleCheck

  const statusClass =
    trace.status === "running"
      ? "tool-trace-running"
      : trace.status === "error"
        ? "tool-trace-error"
        : "tool-trace-success"

  const summary = isSubagentDispatch
    ? `${trace.subagents!.length} subagent${trace.subagents!.length === 1 ? "" : "s"}`
    : trace.status === "running"
      ? describeRunning(trace.name, trace.args)
      : trace.error
        ? "Failed"
        : describeDone(trace.name, trace.args)

  return (
    <div className={`tool-trace ${statusClass}`}>
      <button
        type="button"
        className="tool-trace-header"
        onClick={() => setOpen((v) => !v)}
      >
        {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
        {isSubagentDispatch ? <Bot size={13} /> : <Wrench size={13} />}
        <span className="tool-trace-name">{trace.name}</span>
        <span className="tool-trace-summary">{summary}</span>
        <StatusIcon
          size={13}
          className={trace.status === "running" ? "spin" : ""}
        />
      </button>

      {open && (
        <div className="tool-trace-body">
          {trace.args && Object.keys(trace.args).length > 0 && (
            <details className="tool-trace-args">
              <summary>Arguments</summary>
              <pre>{JSON.stringify(trace.args, null, 2)}</pre>
            </details>
          )}

          {isSubagentDispatch && (
            <div className="tool-trace-subagents">
              {trace.subagents!.map((sub) => (
                <SubagentCard key={sub.id} sub={sub} />
              ))}
            </div>
          )}

          {!isSubagentDispatch && trace.result && (
            <details className="tool-trace-result" open>
              <summary>Result</summary>
              <pre>{truncate(trace.result, 4000)}</pre>
            </details>
          )}
        </div>
      )}
    </div>
  )
}

interface SubagentCardProps {
  sub: SubagentTrace
}

function SubagentCard({ sub }: SubagentCardProps) {
  const [open, setOpen] = useState(true)
  const failed = Boolean(sub.error)

  return (
    <div className={`subagent-card ${failed ? "subagent-card-error" : ""}`}>
      <button
        type="button"
        className="subagent-card-header"
        onClick={() => setOpen((v) => !v)}
      >
        {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        <Bot size={12} />
        <span className="subagent-card-id">{sub.id}</span>
        <span className="subagent-card-meta">
          {typeof sub.turns === "number" && `${sub.turns} turns · `}
          {typeof sub.duration_ms === "number" &&
            `${formatDuration(sub.duration_ms)}`}
        </span>
      </button>

      {open && (
        <div className="subagent-card-body">
          {sub.model && (
            <p className="subagent-card-model">
              <strong>Model:</strong> <code>{sub.model}</code>
            </p>
          )}
          {sub.system_prompt && (
            <details className="subagent-card-prompt">
              <summary>System prompt</summary>
              <pre>{sub.system_prompt}</pre>
            </details>
          )}
          <p className="subagent-card-task">
            <strong>Task:</strong> {sub.task}
          </p>
          {sub.error ? (
            <p className="subagent-card-err">Error: {sub.error}</p>
          ) : (
            sub.output && (
              <pre className="subagent-card-output">
                {truncate(sub.output, 2000)}
              </pre>
            )
          )}
        </div>
      )}
    </div>
  )
}

function describeRunning(name: string, args?: Record<string, unknown>): string {
  switch (name) {
    case "bash": {
      const cmd = String(args?.command ?? "").split("\n")[0].slice(0, 60)
      return cmd ? `$ ${cmd}…` : "Running…"
    }
    case "file_write":
      return args?.path ? `Writing ${shortPath(String(args.path))}…` : "Writing…"
    case "file_read":
      return args?.path ? `Reading ${shortPath(String(args.path))}…` : "Reading…"
    case "glob":
      return args?.pattern ? `Globbing ${String(args.pattern)}…` : "Searching…"
    case "grep":
      return args?.pattern ? `Grep "${String(args.pattern).slice(0, 40)}"…` : "Searching…"
    case "todo_write":
      return "Updating tasks…"
    case "code_interpreter":
      return "Executing code…"
    default:
      return "Running…"
  }
}

function describeDone(name: string, args?: Record<string, unknown>): string {
  switch (name) {
    case "bash": {
      const cmd = String(args?.command ?? "").split("\n")[0].slice(0, 50)
      return cmd ? `Ran \`${cmd}\`` : "Done"
    }
    case "file_write":
      return args?.path ? `Wrote ${shortPath(String(args.path))}` : "Done"
    case "file_read":
      return args?.path ? `Read ${shortPath(String(args.path))}` : "Done"
    case "glob":
      return args?.pattern ? `Glob ${String(args.pattern)}` : "Done"
    case "grep":
      return args?.pattern ? `Grep "${String(args.pattern).slice(0, 40)}"` : "Done"
    case "todo_write":
      return "Tasks updated"
    case "code_interpreter":
      return "Code executed"
    default:
      return "Done"
  }
}

function shortPath(p: string): string {
  const parts = p.split("/")
  return parts.length > 2 ? `…/${parts.slice(-2).join("/")}` : p
}

function truncate(value: string, limit: number): string {
  if (value.length <= limit) return value
  return value.slice(0, limit - 3) + "..."
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}
