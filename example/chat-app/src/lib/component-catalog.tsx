// Component catalog — apps register their domain-specific React components
// here, keyed by name. The Go SDK emits ComponentArtifact events with a
// `name` matching one of these keys, and ArtifactRenderer (from
// @harness/react) looks them up at render time.
//
// This file demonstrates a tiny *healthcare* surface (PatientChart,
// MedicationList, AppointmentForm) but applications should treat it as a
// template: a finance app would register PortfolioCard, OrderTicket, etc.

import type { ComponentCatalog } from "@harness/react"

interface PatientChartProps {
  patientId?: string
  name?: string
  age?: number
  vitals?: { bp?: string; hr?: number; temp?: number }
}

function PatientChart(props: Record<string, unknown>) {
  const p = props as PatientChartProps
  return (
    <div className="space-y-2 rounded-md border p-4">
      <div className="font-medium">Patient #{p.patientId ?? "—"}</div>
      <div className="text-sm">
        {p.name ?? "Unknown"} · {p.age ?? "?"} y/o
      </div>
      {p.vitals && (
        <ul className="space-y-0.5 text-xs">
          {p.vitals.bp && <li>BP: {p.vitals.bp}</li>}
          {p.vitals.hr !== undefined && <li>HR: {p.vitals.hr} bpm</li>}
          {p.vitals.temp !== undefined && <li>Temp: {p.vitals.temp}°C</li>}
        </ul>
      )}
    </div>
  )
}

interface MedicationListProps {
  medications?: Array<{ name: string; dose: string; frequency: string }>
}

function MedicationList(props: Record<string, unknown>) {
  const meds = (props as MedicationListProps).medications ?? []
  return (
    <div className="rounded-md border p-4">
      <div className="mb-2 font-medium">Medications</div>
      <ul className="space-y-1 text-sm">
        {meds.map((m, i) => (
          <li key={i}>
            <span className="font-medium">{m.name}</span> · {m.dose} ·{" "}
            {m.frequency}
          </li>
        ))}
      </ul>
    </div>
  )
}

interface AppointmentFormProps {
  patientId?: string
  defaultDate?: string
  onSubmit?: (data: unknown) => void
}

type QuestionnaireQuestionType = "single" | "multi" | "text"

interface QuestionnaireQuestion {
  id: string
  label: string
  type?: QuestionnaireQuestionType
  options?: Array<string>
  required?: boolean
}

interface QuestionnaireFormProps {
  title?: string
  description?: string
  questions?: Array<QuestionnaireQuestion>
  submitLabel?: string
  onSubmit?: (data: unknown) => void
}

function AppointmentForm(props: Record<string, unknown>) {
  const p = props as AppointmentFormProps
  return (
    <form
      className="space-y-2 rounded-md border p-4"
      onSubmit={(e) => {
        e.preventDefault()
        const fd = new FormData(e.currentTarget)
        const data = {
          patientId: fd.get("patientId")?.toString() ?? p.patientId ?? "",
          when: fd.get("when")?.toString() ?? "",
        }
        p.onSubmit?.(data)
      }}
    >
      <div className="font-medium">Schedule appointment</div>
      <input type="hidden" name="patientId" defaultValue={p.patientId ?? ""} />
      <input
        name="when"
        type="datetime-local"
        defaultValue={p.defaultDate ?? ""}
        className="w-full rounded border p-1 text-sm"
        required
      />
      <button
        type="submit"
        className="rounded bg-primary px-3 py-1 text-sm text-primary-foreground"
      >
        Confirm
      </button>
    </form>
  )
}

function QuestionnaireForm(props: Record<string, unknown>) {
  const p = props as QuestionnaireFormProps
  const questions = p.questions ?? []
  return (
    <form
      className="space-y-3 rounded-md border p-4"
      onSubmit={(e) => {
        e.preventDefault()
        const fd = new FormData(e.currentTarget)
        const answers: Record<string, unknown> = {}
        for (const q of questions) {
          const type = q.type ?? "single"
          if (type === "multi") {
            answers[q.id] = fd.getAll(q.id).map((v) => String(v))
            continue
          }
          answers[q.id] = String(fd.get(q.id) ?? "")
        }
        p.onSubmit?.(answers)
      }}
    >
      <div className="space-y-1">
        <div className="font-medium">{p.title ?? "Questionnaire"}</div>
        {p.description && (
          <p className="text-sm text-muted-foreground">{p.description}</p>
        )}
      </div>

      {questions.map((q) => {
        const type = q.type ?? "single"
        const options = q.options ?? []
        return (
          <fieldset key={q.id} className="space-y-2 rounded border p-3">
            <legend className="px-1 text-sm font-medium">{q.label}</legend>

            {type === "text" && (
              <textarea
                name={q.id}
                className="w-full rounded border p-2 text-sm"
                rows={3}
                required={q.required}
                placeholder="Type your answer"
              />
            )}

            {type === "single" &&
              options.map((opt) => (
                <label key={opt} className="flex items-center gap-2 text-sm">
                  <input
                    type="radio"
                    name={q.id}
                    value={opt}
                    required={q.required}
                  />
                  {opt}
                </label>
              ))}

            {type === "multi" &&
              options.map((opt) => (
                <label key={opt} className="flex items-center gap-2 text-sm">
                  <input type="checkbox" name={q.id} value={opt} />
                  {opt}
                </label>
              ))}
          </fieldset>
        )
      })}

      <button
        type="submit"
        className="rounded bg-primary px-3 py-1 text-sm text-primary-foreground"
      >
        {p.submitLabel ?? "Submit"}
      </button>
    </form>
  )
}

export const componentCatalog: ComponentCatalog = {
  PatientChart: { component: PatientChart },
  MedicationList: { component: MedicationList },
  AppointmentForm: { component: AppointmentForm },
  QuestionnaireForm: { component: QuestionnaireForm },
}
