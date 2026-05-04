"use client"

import { Card } from "@/components/ui/card"
import { cn } from "@/lib/utils"
import { Loader } from "lucide-react"

export interface ContextItem {
  id: string
  label: string
  value: string | number
  type?: "text" | "metric" | "tag"
}

export interface AIContextProps {
  items: ContextItem[]
  isLoading?: boolean
  loadingProgress?: number
  title?: string
  className?: string
}

export function AIContext({
  items,
  isLoading = false,
  loadingProgress,
  title = "Context",
  className,
}: AIContextProps) {
  return (
    <div className={cn("w-full space-y-4", className)}>
      {/* Loading state */}
      {isLoading && (
        <div className="flex items-center justify-center gap-3 rounded-lg bg-gray-50 py-8">
          <Loader className="h-5 w-5 animate-spin text-gray-600" />
          {loadingProgress !== undefined && (
            <span className="text-lg font-medium text-gray-700">
              {loadingProgress.toFixed(1)}%
            </span>
          )}
        </div>
      )}

      {/* Context items */}
      {!isLoading && items.length > 0 && (
        <Card className="overflow-hidden">
          <div className="border-b bg-gray-50 px-4 py-3">
            <h3 className="text-sm font-semibold text-gray-900">{title}</h3>
          </div>
          <div className="divide-y">
            {items.map((item) => (
              <div
                key={item.id}
                className="flex items-center justify-between px-4 py-3"
              >
                <label className="text-sm font-medium text-gray-700">
                  {item.label}
                </label>
                {item.type === "tag" ? (
                  <span className="inline-flex items-center rounded-full bg-blue-100 px-3 py-1 text-xs font-medium text-blue-700">
                    {item.value}
                  </span>
                ) : item.type === "metric" ? (
                  <span className="text-lg font-semibold text-gray-900">
                    {item.value}
                  </span>
                ) : (
                  <span className="text-sm text-gray-600">{item.value}</span>
                )}
              </div>
            ))}
          </div>
        </Card>
      )}

      {/* Empty state */}
      {!isLoading && items.length === 0 && (
        <Card className="p-6 text-center">
          <p className="text-sm text-gray-500">No context available</p>
        </Card>
      )}
    </div>
  )
}

// Example data
export const exampleContextItems: ContextItem[] = [
  {
    id: "1",
    label: "Tokens Used",
    value: "2,456",
    type: "metric",
  },
  {
    id: "2",
    label: "Model",
    value: "claude-3-opus",
    type: "tag",
  },
  {
    id: "3",
    label: "Temperature",
    value: "0.7",
    type: "text",
  },
  {
    id: "4",
    label: "Max Tokens",
    value: "4096",
    type: "metric",
  },
]

export const loadingContextExample: Omit<AIContextProps, "items"> = {
  isLoading: true,
  loadingProgress: 31.3,
  title: "Processing Context",
}
