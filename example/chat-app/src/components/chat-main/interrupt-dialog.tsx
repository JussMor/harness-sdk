// InterruptDialog — polymorphic dialog handling all three InterruptKinds:
// approval, question, form_input. It is intentionally framework-light so any
// shadcn/Radix dialog can wrap it; here we render plain elements so it works
// even without a host UI kit.

import type { StreamInterruptRequest } from "@/features/chat/types"
import { useState } from "react"

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
  const args = request.approval?.tool_call?.args
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
