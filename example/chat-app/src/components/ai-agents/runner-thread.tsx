"use client"

import { cn } from "@/lib/utils"
import { AlertCircle, CheckCircle2, Clock, Loader2 } from "lucide-react"

export interface RunnerThreadProps {
  id: string
  task: string
  status: "pending" | "running" | "completed" | "failed"
  result?: string
  tier?: string
  model?: string
}

export function RunnerThread({
  id: _,
  task,
  status,
  result,
  tier,
  model,
}: RunnerThreadProps) {
  const statusConfig = {
    pending: {
      icon: Clock,
      color: "text-gray-500",
      bg: "bg-gray-50",
      border: "border-gray-200",
      label: "Pending",
    },
    running: {
      icon: Loader2,
      color: "text-blue-500",
      bg: "bg-blue-50",
      border: "border-blue-200",
      label: "Running",
    },
    completed: {
      icon: CheckCircle2,
      color: "text-green-600",
      bg: "bg-green-50",
      border: "border-green-200",
      label: "Completed",
    },
    failed: {
      icon: AlertCircle,
      color: "text-red-600",
      bg: "bg-red-50",
      border: "border-red-200",
      label: "Failed",
    },
  } as const

  const config = statusConfig[status] ?? statusConfig.pending
  const Icon = config.icon

  return (
    <div
      className={cn(
        "rounded-lg border p-4 transition-all",
        config.border,
        config.bg,
        status === "running" && "shadow-md"
      )}
    >
      {/* Header */}
      <div className="mb-3 flex items-start justify-between">
        <div className="flex items-center gap-2">
          <Icon className={cn("h-5 w-5 flex-shrink-0", config.color)} />
          {status === "running" && (
            <Icon className={cn("h-5 w-5 animate-spin", config.color)} />
          )}
          <div>
            <h3 className="font-medium text-gray-900">{task}</h3>
            <p className="text-xs text-gray-600">
              {config.label}
              {tier && ` • ${tier}`}
            </p>
          </div>
        </div>
      </div>

      {/* Progress bar for running status */}
      {status === "running" && (
        <div className="mb-3 h-1 w-full overflow-hidden rounded-full bg-gray-200">
          <div
            className="h-full w-1/3 animate-pulse bg-blue-500"
            style={{
              animation: "pulse 2s cubic-bezier(0.4, 0, 0.6, 1) infinite",
            }}
          />
        </div>
      )}

      {/* Result/Output */}
      {result && (
        <div
          className={cn(
            "max-h-32 overflow-y-auto rounded bg-white p-2 font-mono text-sm text-xs",
            status === "failed" ? "text-red-700" : "text-gray-700"
          )}
        >
          {result}
        </div>
      )}

      {/* Footer metadata */}
      {model && (
        <div className="mt-2 text-xs text-gray-500">Model: {model}</div>
      )}
    </div>
  )
}
