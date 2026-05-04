"use client"

import { Card } from "@/components/ui/card"
import { cn } from "@/lib/utils"
import { Briefcase, ChevronDown } from "lucide-react"
import { useState } from "react"

export interface AgentTool {
  id: string
  name: string
  description?: string
  schema?: Record<string, unknown>
}

export interface AgentProps {
  name: string
  model: string
  instructions: string
  tools: Array<AgentTool>
  outputSchema?: string
  icon?: React.ReactNode
}

export function Agent({
  name,
  model,
  instructions,
  tools,
  outputSchema,
  icon,
}: AgentProps) {
  const [expandedTools, setExpandedTools] = useState<Set<string>>(new Set())

  const toggleTool = (toolId: string) => {
    const newExpanded = new Set(expandedTools)
    if (newExpanded.has(toolId)) {
      newExpanded.delete(toolId)
    } else {
      newExpanded.add(toolId)
    }
    setExpandedTools(newExpanded)
  }

  return (
    <div className="w-full space-y-4">
      {/* Header */}
      <div className="flex items-center gap-3">
        {icon || <Briefcase className="h-5 w-5 text-slate-600" />}
        <div>
          <h2 className="text-lg font-semibold text-gray-900">{name}</h2>
          <p className="text-sm text-gray-500">{model}</p>
        </div>
      </div>

      {/* Instructions */}
      <Card className="space-y-3 p-4">
        <h3 className="text-sm font-semibold text-gray-900">Instructions</h3>
        <p className="text-sm leading-relaxed whitespace-pre-wrap text-gray-600">
          {instructions}
        </p>
      </Card>

      {/* Tools */}
      {tools.length > 0 && (
        <Card className="overflow-hidden">
          <div className="space-y-0">
            <div className="border-b bg-gray-50 px-4 py-3">
              <h3 className="text-sm font-semibold text-gray-900">Tools</h3>
            </div>
            <div className="divide-y">
              {tools.map((tool) => (
                <div key={tool.id} className="bg-white">
                  <button
                    onClick={() => toggleTool(tool.id)}
                    className="flex w-full items-center justify-between gap-3 px-4 py-3 hover:bg-gray-50"
                  >
                    <div className="flex-1 text-left">
                      <p className="text-sm font-medium text-gray-900">
                        {tool.name}
                      </p>
                      {tool.description && (
                        <p className="text-xs text-gray-500">
                          {tool.description}
                        </p>
                      )}
                    </div>
                    <ChevronDown
                      className={cn(
                        "h-4 w-4 flex-shrink-0 text-gray-400 transition-transform",
                        expandedTools.has(tool.id) && "rotate-180"
                      )}
                    />
                  </button>

                  {expandedTools.has(tool.id) && tool.schema && (
                    <div className="border-t bg-gray-50 px-4 py-3">
                      <pre className="overflow-x-auto rounded bg-gray-900 p-3 text-xs text-gray-100">
                        <code>{JSON.stringify(tool.schema, null, 2)}</code>
                      </pre>
                    </div>
                  )}
                </div>
              ))}
            </div>
          </div>
        </Card>
      )}

      {/* Output Schema */}
      {outputSchema && (
        <Card className="space-y-3 p-4">
          <h3 className="text-sm font-semibold text-gray-900">Output Schema</h3>
          <pre className="overflow-x-auto rounded bg-gray-900 p-3 text-xs text-gray-100">
            <code>{outputSchema}</code>
          </pre>
        </Card>
      )}
    </div>
  )
}

// Example agent
export const exampleAgent: AgentProps = {
  name: "Research Assistant",
  model: "claude-3-opus",
  instructions:
    "You are a helpful research assistant. Search the web for information and provide accurate, well-sourced answers.",
  tools: [
    {
      id: "web-search",
      name: "Search the web for current information",
      description: "Query the web for real-time information and research",
      schema: {
        type: "object",
        properties: {
          query: { type: "string", description: "Search query" },
          limit: {
            type: "number",
            description: "Max results to return",
          },
        },
        required: ["query"],
      },
    },
    {
      id: "read-file",
      name: "Read a file from the filesystem",
      description: "Access local files for context and reference",
      schema: {
        type: "object",
        properties: {
          path: { type: "string", description: "File path to read" },
          encoding: {
            type: "string",
            description: "File encoding (utf-8, etc)",
          },
        },
        required: ["path"],
      },
    },
  ],
  outputSchema: `interface ResearchResult {
  answer: string;
  sources: string[];
  confidence: number;
}`,
}
