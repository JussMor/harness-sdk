"use client"

import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { cn } from "@/lib/utils"
import { Copy, Eye, EyeOff } from "lucide-react"
import { useState } from "react"

export interface EnvVariable {
  id: string
  name: string
  value?: string
  required: boolean
  isSecret?: boolean
}

export interface AIEnvVarsProps {
  variables: Array<EnvVariable>
  onCopy?: (name: string, value: string) => void
  title?: string
  allowToggleVisibility?: boolean
  className?: string
}

export function AIEnvVars({
  variables,
  onCopy,
  title = "Environment Variables",
  allowToggleVisibility = true,
  className,
}: AIEnvVarsProps) {
  const [visibleSecrets, setVisibleSecrets] = useState<Set<string>>(new Set())

  const toggleVisibility = (name: string) => {
    const newVisible = new Set(visibleSecrets)
    if (newVisible.has(name)) {
      newVisible.delete(name)
    } else {
      newVisible.add(name)
    }
    setVisibleSecrets(newVisible)
  }

  const getMaskedValue = (
    value: string | undefined,
    isSecret: boolean,
    isVisible: boolean
  ) => {
    if (!value) return ""
    if (!isSecret || isVisible) return value
    return "•".repeat(Math.min(value.length, 20))
  }

  return (
    <div className={cn("w-full space-y-4", className)}>
      {/* Header with toggle */}
      <div className="flex items-center justify-between">
        <h2 className="text-base font-semibold text-gray-900">{title}</h2>
        {allowToggleVisibility && (
          <button
            onClick={() => setVisibleSecrets(new Set())}
            className="rounded p-2 hover:bg-gray-100"
            title="Hide all secrets"
          >
            <EyeOff className="h-5 w-5 text-gray-600" />
          </button>
        )}
      </div>

      {/* Variables List */}
      <Card className="overflow-hidden">
        <div className="divide-y">
          {variables.map((envVar) => {
            const isVisible = visibleSecrets.has(envVar.name)
            const displayValue = getMaskedValue(
              envVar.value,
              envVar.isSecret || false,
              isVisible
            )

            return (
              <div
                key={envVar.id}
                className="flex items-center justify-between gap-4 px-4 py-3 hover:bg-gray-50"
              >
                {/* Left: Name and Required Badge */}
                <div className="flex min-w-0 flex-1 items-center gap-3">
                  <span className="font-mono text-sm font-medium text-gray-900">
                    {envVar.name}
                  </span>
                  <span
                    className={cn(
                      "inline-block rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap",
                      envVar.required
                        ? "bg-red-100 text-red-700"
                        : "bg-gray-100 text-gray-700"
                    )}
                  >
                    {envVar.required ? "Required" : "Optional"}
                  </span>
                </div>

                {/* Middle: Value */}
                <div className="flex min-w-0 items-center gap-2">
                  <span className="truncate font-mono text-sm text-gray-600">
                    {displayValue}
                  </span>
                </div>

                {/* Right: Actions */}
                <div className="flex flex-shrink-0 items-center gap-1">
                  {envVar.isSecret && allowToggleVisibility && (
                    <Button
                      variant="ghost"
                      size="icon"
                      onClick={() => toggleVisibility(envVar.name)}
                      className="h-8 w-8"
                      title={isVisible ? "Hide" : "Show"}
                    >
                      {isVisible ? (
                        <Eye className="h-4 w-4 text-gray-600" />
                      ) : (
                        <EyeOff className="h-4 w-4 text-gray-600" />
                      )}
                    </Button>
                  )}
                  {envVar.value && (
                    <Button
                      variant="ghost"
                      size="icon"
                      onClick={() => {
                        navigator.clipboard.writeText(envVar.value || "")
                        onCopy?.(envVar.name, envVar.value || "")
                      }}
                      className="h-8 w-8"
                      title="Copy"
                    >
                      <Copy className="h-4 w-4 text-gray-600" />
                    </Button>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      </Card>
    </div>
  )
}

// Example data
export const exampleEnvVariables: Array<EnvVariable> = [
  {
    id: "1",
    name: "OPENAI_API_KEY",
    value: "sk-proj-xxxxxxxxxxxxxxxxxxxxxxxx",
    required: true,
    isSecret: true,
  },
  {
    id: "2",
    name: "DATABASE_URL",
    value: "postgres://user:pass@localhost:5432/db",
    required: true,
    isSecret: true,
  },
  {
    id: "3",
    name: "DEBUG",
    value: "false",
    required: false,
    isSecret: false,
  },
]
