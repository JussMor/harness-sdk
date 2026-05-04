"use client"

import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Bookmark, RotateCcw } from "lucide-react"
import { useState } from "react"

export interface Message {
  id: string
  content: string
  role: "user" | "assistant"
}

export interface Checkpoint {
  id: string
  name?: string
  messageId: string
  timestamp: Date
  messageCount: number
}

export interface AICheckpointProps {
  messages: Message[]
  checkpoints: Checkpoint[]
  onCheckpointCreated?: (messageId: string) => void
  onCheckpointRestore?: (checkpointId: string) => void
}

export function AICheckpoint({
  messages,
  checkpoints,
  onCheckpointCreated,
  onCheckpointRestore,
}: AICheckpointProps) {
  const [showCheckpoints, setShowCheckpoints] = useState(false)

  // Find the message index for each checkpoint
  const checkpointMap = new Map(
    checkpoints.map((cp) => [
      cp.messageId,
      {
        name: cp.name,
        id: cp.id,
        timestamp: cp.timestamp,
      },
    ])
  )

  return (
    <div className="w-full space-y-4">
      {/* Messages with inline checkpoints */}
      <div className="space-y-3">
        {messages.map((message, idx) => {
          const checkpoint = checkpointMap.get(message.id)
          const isLastMessage = idx === messages.length - 1

          return (
            <div key={message.id} className="space-y-2">
              {/* Message */}
              <div
                className={cn(
                  "rounded-lg px-4 py-3",
                  message.role === "user"
                    ? "ml-auto max-w-xs bg-blue-100 text-gray-900"
                    : "mr-auto max-w-sm bg-gray-100 text-gray-700"
                )}
              >
                <p className="text-sm">{message.content}</p>
              </div>

              {/* Checkpoint marker or create button */}
              {checkpoint ? (
                <div className="flex items-center gap-2 px-4">
                  <Bookmark className="h-4 w-4 text-amber-500" />
                  <span className="text-xs font-medium text-gray-600">
                    Checkpoint
                    {checkpoint.name && `: ${checkpoint.name}`}
                  </span>
                </div>
              ) : isLastMessage ? (
                <div className="flex items-center justify-center px-4">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => onCheckpointCreated?.(message.id)}
                    className="gap-2 text-xs"
                  >
                    <Bookmark className="h-3 w-3" />
                    Save checkpoint
                  </Button>
                </div>
              ) : null}
            </div>
          )
        })}
      </div>

      {/* Checkpoints Panel */}
      {checkpoints.length > 0 && (
        <Card className="p-4">
          <button
            onClick={() => setShowCheckpoints(!showCheckpoints)}
            className="flex w-full items-center justify-between text-left"
          >
            <h3 className="text-sm font-semibold text-gray-900">
              Checkpoints ({checkpoints.length})
            </h3>
            <span className="text-xs text-gray-500">
              {showCheckpoints ? "Hide" : "Show"}
            </span>
          </button>

          {showCheckpoints && (
            <div className="mt-3 space-y-2 border-t pt-3">
              {checkpoints.map((checkpoint) => (
                <div
                  key={checkpoint.id}
                  className="flex items-center justify-between rounded-lg bg-gray-50 px-3 py-2"
                >
                  <div className="flex items-center gap-2">
                    <Bookmark className="h-4 w-4 text-amber-500" />
                    <div className="flex flex-col">
                      <p className="text-xs font-medium text-gray-900">
                        {checkpoint.name ||
                          `Checkpoint at message ${checkpoint.messageCount}`}
                      </p>
                      <p className="text-xs text-gray-500">
                        {checkpoint.timestamp.toLocaleTimeString()}
                      </p>
                    </div>
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => onCheckpointRestore?.(checkpoint.id)}
                    className="gap-2"
                  >
                    <RotateCcw className="h-3 w-3" />
                    <span className="text-xs">Restore</span>
                  </Button>
                </div>
              ))}
            </div>
          )}
        </Card>
      )}
    </div>
  )
}

// Helper function
function cn(...classes: (string | undefined | null | false)[]) {
  return classes.filter(Boolean).join(" ")
}

// Example data
export const exampleMessages: Message[] = [
  {
    id: "1",
    content: "What is React?",
    role: "user",
  },
  {
    id: "2",
    content:
      "React is a JavaScript library for building user interfaces with reusable components and efficient rendering.",
    role: "assistant",
  },
  {
    id: "3",
    content: "How does state work?",
    role: "user",
  },
]

export const exampleCheckpoints: Checkpoint[] = [
  {
    id: "cp1",
    name: "React Basics",
    messageId: "2",
    timestamp: new Date(Date.now() - 5 * 60 * 1000),
    messageCount: 2,
  },
]
