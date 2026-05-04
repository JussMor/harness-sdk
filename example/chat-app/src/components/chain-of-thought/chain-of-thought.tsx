"use client"

import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { cn } from "@/lib/utils"
import {
  BookOpen,
  CheckCircle2,
  ChevronDown,
  Lightbulb,
  Search,
} from "lucide-react"
import { useState } from "react"

export interface ChainOfThoughtStep {
  id: string
  type: "search" | "result" | "action" | "thinking"
  title: string
  content?: React.ReactNode
  urls?: Array<string>
  details?: Array<string>
}

export interface ChainOfThoughtProps {
  steps: Array<ChainOfThoughtStep>
  title?: string
  defaultExpanded?: boolean
}

const stepTypeConfig = {
  search: {
    icon: Search,
    color: "text-blue-500",
    bgColor: "bg-blue-50",
  },
  result: {
    icon: BookOpen,
    color: "text-purple-500",
    bgColor: "bg-purple-50",
  },
  action: {
    icon: CheckCircle2,
    color: "text-green-500",
    bgColor: "bg-green-50",
  },
  thinking: {
    icon: Lightbulb,
    color: "text-amber-500",
    bgColor: "bg-amber-50",
  },
}

function ChainOfThoughtStep({
  step,
  isLast,
}: {
  step: ChainOfThoughtStep
  isLast: boolean
}) {
  const [expanded, setExpanded] = useState(true)
  const config = stepTypeConfig[step.type]
  const Icon = config.icon

  return (
    <div className="relative">
      {/* Connection line */}
      {!isLast && (
        <div className="absolute top-10 left-4 h-6 w-0.5 bg-gray-200" />
      )}

      {/* Step content */}
      <div className="flex gap-3">
        {/* Icon */}
        <div
          className={cn(
            "relative z-10 mt-1 flex h-8 w-8 items-center justify-center rounded-full border-2 border-white",
            config.bgColor
          )}
        >
          <Icon className={cn("h-4 w-4", config.color)} />
        </div>

        {/* Step card */}
        <div className="flex-1 pb-5">
          <Card className="overflow-hidden">
            <button
              onClick={() => setExpanded(!expanded)}
              className="flex w-full items-center justify-between gap-3 bg-gray-50 px-3 py-2 hover:bg-gray-100"
            >
              <h4 className="text-sm font-medium text-gray-700">
                {step.title}
              </h4>
              <ChevronDown
                className={cn(
                  "h-4 w-4 transition-transform",
                  expanded && "rotate-180"
                )}
              />
            </button>

            {expanded && (
              <div className="space-y-2 p-3">
                {/* URLs */}
                {step.urls && step.urls.length > 0 && (
                  <div className="flex flex-wrap gap-2">
                    {step.urls.map((url, idx) => (
                      <a
                        key={idx}
                        href={url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="inline-block rounded bg-gray-100 px-2 py-0.5 text-xs text-gray-600 hover:bg-gray-200"
                      >
                        {url}
                      </a>
                    ))}
                  </div>
                )}

                {/* Main content */}
                {step.content && (
                  <div className="text-sm text-gray-700">{step.content}</div>
                )}

                {/* Details/bullets */}
                {step.details && step.details.length > 0 && (
                  <ul className="space-y-1.5">
                    {step.details.map((detail, idx) => (
                      <li
                        key={idx}
                        className="flex gap-2 text-sm text-gray-600"
                      >
                        <span className="text-gray-400">•</span>
                        <span>{detail}</span>
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            )}
          </Card>
        </div>
      </div>
    </div>
  )
}

export function ChainOfThought({
  steps,
  title = "Chain of Thought",
  defaultExpanded = true,
}: ChainOfThoughtProps) {
  const [collapsed, setCollapsed] = useState(!defaultExpanded)

  return (
    <div className="w-full">
      {/* Header */}
      <div className="mb-3 flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Lightbulb className="h-4 w-4 text-amber-500" />
          <h3 className="text-sm font-semibold text-gray-900">{title}</h3>
        </div>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setCollapsed(!collapsed)}
          className="h-8 w-8 p-0"
        >
          <ChevronDown
            className={cn(
              "h-4 w-4 transition-transform",
              collapsed && "-rotate-90"
            )}
          />
        </Button>
      </div>

      {/* Steps */}
      {!collapsed && (
        <div className="space-y-0">
          {steps.map((step, idx) => (
            <ChainOfThoughtStep
              key={step.id}
              step={step}
              isLast={idx === steps.length - 1}
            />
          ))}
        </div>
      )}
    </div>
  )
}

// Demo/Example export
export const exampleChainOfThought: Array<ChainOfThoughtStep> = [
  {
    id: "1",
    type: "search",
    title: "Searching for chocolate chip cookie recipes",
    urls: ["www.allrecipes.com", "www.foodnetwork.com", "www.seriouseats.com"],
  },
  {
    id: "2",
    type: "result",
    title: "Found a highly-rated recipe with 4.8 stars",
    content: "Classic chocolate chip cookies fresh from the oven.",
    details: [
      "This recipe uses brown butter for extra flavor and requires chilling the dough.",
    ],
  },
  {
    id: "3",
    type: "thinking",
    title: "Looking for ingredient substitutions...",
    urls: ["www.kingarthurbaking.com", "www.thekitchn.com"],
  },
]
