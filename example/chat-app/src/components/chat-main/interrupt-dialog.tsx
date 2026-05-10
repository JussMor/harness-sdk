// InterruptDialog — polymorphic dialog handling all three InterruptKinds:
// approval, question, form_input. It is intentionally framework-light so any
// shadcn/Radix dialog can wrap it; here we render plain elements so it works
// even without a host UI kit.

import type { StreamInterruptRequest } from "@/features/chat/types"
import { useState } from "react"
import ReactMarkdown from "react-markdown"
import remarkGfm from "remark-gfm"

export interface InterruptDialogProps {
  request: StreamInterruptRequest
  onResolve: (response: {
    approved?: boolean
    answer?: unknown
    modifiedArgs?: string
  }) => void
  onCancel?: () => void
}

export function InterruptDialog({
  request,
  onResolve,
  onCancel,
}: InterruptDialogProps) {
  if (request.kind === "approval") {
    return (
      <ApprovalView
        request={request}
        onResolve={onResolve}
        onCancel={onCancel}
      />
    )
  }
  if (request.kind === "question") {
    return (
      <QuestionView
        request={request}
        onResolve={onResolve}
        onCancel={onCancel}
      />
    )
  }
  if (request.kind === "form_input") {
    return (
      <FormView request={request} onResolve={onResolve} onCancel={onCancel} />
    )
  }
  return null
}

function ApprovalView({ request, onResolve }: InterruptDialogProps) {
  const tool = request.approval?.tool_call?.name ?? "unknown"
  // Tool args arrive on the SSE wire as a JSON-encoded string under
  // `arguments` (matches sdk.ToolCallEntry.Arguments). Older payloads may
  // expose a pre-parsed `args` object — accept either.
  const rawArgs = request.approval?.tool_call?.arguments
  let parsedArgs: Record<string, unknown> | undefined =
    request.approval?.tool_call?.args
  if (!parsedArgs && typeof rawArgs === "string" && rawArgs.length > 0) {
    try {
      parsedArgs = JSON.parse(rawArgs) as Record<string, unknown>
    } catch {
      parsedArgs = undefined
    }
  }
  const args = parsedArgs

  // Specialise ExitPlanMode → render the plan as markdown with explicit
  // "Approve plan" / "Keep planning" buttons. The agent only escapes plan
  // mode when the user approves, so the dialog is the gate for switching
  // from research → execution.
  if (tool === "ExitPlanMode") {
    const plan = typeof args?.plan === "string" ? args.plan : ""
    return (
      <div className="space-y-3 rounded-md border bg-card p-4">
        <div className="flex items-center gap-2 font-medium">
          <span>Plan ready for review</span>
          <span className="rounded-full bg-amber-100 px-2 py-0.5 text-xs text-amber-900">
            Plan mode
          </span>
        </div>
        <div className="text-sm text-muted-foreground">
          The agent gathered context in plan mode and proposes the steps below.
          Approving exits plan mode and lets the agent execute.
        </div>
        <div className="prose prose-sm dark:prose-invert max-h-80 max-w-none overflow-auto rounded bg-muted p-3">
          {plan ? (
            <ReactMarkdown remarkPlugins={[remarkGfm]}>{plan}</ReactMarkdown>
          ) : (
            <p className="text-muted-foreground">No plan provided.</p>
          )}
        </div>
        <div className="flex justify-end gap-2">
          <button
            className="rounded border px-3 py-1"
            onClick={() => onResolve({ approved: false })}
          >
            Keep planning
          </button>
          <button
            className="rounded bg-primary px-3 py-1 text-primary-foreground"
            onClick={() => onResolve({ approved: true })}
          >
            Approve plan
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-3 rounded-md border bg-card p-4">
      <div className="font-medium">Approval requested</div>
      <div className="text-sm text-muted-foreground">{request.reason}</div>
      <pre className="overflow-auto rounded bg-muted p-2 text-xs">
        {JSON.stringify({ tool, args }, null, 2)}
      </pre>
      <div className="flex justify-end gap-2">
        <button
          className="rounded border px-3 py-1"
          onClick={() => onResolve({ approved: false })}
        >
          Reject
        </button>
        <button
          className="rounded bg-primary px-3 py-1 text-primary-foreground"
          onClick={() => onResolve({ approved: true })}
        >
          Approve
        </button>
      </div>
    </div>
  )
}

function QuestionView({ request, onResolve, onCancel }: InterruptDialogProps) {
  const q = request.question
  const [text, setText] = useState("")
  const [selected, setSelected] = useState<Array<string>>([])

  if (!q) return null

  const submit = () => {
    if (q.choices?.length) {
      const answer = q.multi ? selected : selected[0]
      onResolve({ answer, approved: true })
    } else {
      onResolve({ answer: text, approved: true })
    }
  }

  return (
    <div className="space-y-3 rounded-md border bg-card p-4">
      <div className="font-medium">Agent needs clarification</div>
      <div className="text-sm">{q.prompt}</div>
      {q.choices?.length ? (
        <div className="flex flex-col gap-2">
          {q.choices.map((c) => (
            <label key={c} className="flex items-center gap-2 text-sm">
              <input
                type={q.multi ? "checkbox" : "radio"}
                name="interrupt-choice"
                value={c}
                checked={selected.includes(c)}
                onChange={(e) => {
                  if (q.multi) {
                    setSelected((prev) =>
                      e.target.checked
                        ? [...prev, c]
                        : prev.filter((x) => x !== c)
                    )
                  } else {
                    setSelected([c])
                  }
                }}
              />
              {c}
            </label>
          ))}
        </div>
      ) : (
        <textarea
          className="w-full rounded border p-2 text-sm"
          rows={3}
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder="Type your answer..."
        />
      )}
      <div className="flex justify-end gap-2">
        {onCancel && (
          <button className="rounded border px-3 py-1" onClick={onCancel}>
            Cancel
          </button>
        )}
        <button
          className="rounded bg-primary px-3 py-1 text-primary-foreground"
          onClick={submit}
        >
          Send
        </button>
      </div>
    </div>
  )
}

function FormView({ request, onResolve, onCancel }: InterruptDialogProps) {
  const [json, setJson] = useState("{}")
  const [error, setError] = useState<string | null>(null)

  const submit = () => {
    try {
      const parsed = JSON.parse(json)
      setError(null)
      onResolve({ answer: parsed, approved: true })
    } catch (e) {
      setError((e as Error).message)
    }
  }

  return (
    <div className="space-y-3 rounded-md border bg-card p-4">
      <div className="font-medium">
        {request.form?.title ?? "Provide input"}
      </div>
      <div className="text-xs text-muted-foreground">
        Schema: {JSON.stringify(request.form?.schema ?? {})}
      </div>
      <textarea
        className="w-full rounded border p-2 font-mono text-xs"
        rows={6}
        value={json}
        onChange={(e) => setJson(e.target.value)}
      />
      {error && <div className="text-xs text-destructive">{error}</div>}
      <div className="flex justify-end gap-2">
        {onCancel && (
          <button className="rounded border px-3 py-1" onClick={onCancel}>
            Cancel
          </button>
        )}
        <button
          className="rounded bg-primary px-3 py-1 text-primary-foreground"
          onClick={submit}
        >
          Submit
        </button>
      </div>
    </div>
  )
}
