"use client"

import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { Copy, RotateCcw, ThumbsDown, ThumbsUp } from "lucide-react"

export interface ActionItem {
  id: string
  icon: React.ReactNode
  label: string
  onClick: () => void | Promise<void>
  variant?: "ghost" | "outline"
  size?: "xs" | "sm" | "lg" | "icon" | "icon-xs" | "icon-sm" | "icon-lg"
  isLoading?: boolean
}

export interface AIActionsProps {
  actions: ActionItem[]
  orientation?: "horizontal" | "vertical"
  className?: string
}

export function AIActions({
  actions,
  orientation = "horizontal",
  className,
}: AIActionsProps) {
  return (
    <div
      className={cn(
        "flex gap-2",
        orientation === "vertical" && "flex-col",
        className
      )}
    >
      {actions.map((action) => (
        <Button
          key={action.id}
          variant={action.variant || "ghost"}
          size={action.size || "sm"}
          onClick={action.onClick}
          disabled={action.isLoading}
          className="gap-2"
          title={action.label}
        >
          {action.icon}
          {orientation === "vertical" && (
            <span className="text-xs">{action.label}</span>
          )}
        </Button>
      ))}
    </div>
  )
}

// Common action presets
export const commonActions = {
  copy: (text: string): ActionItem => ({
    id: "copy",
    icon: <Copy className="h-4 w-4" />,
    label: "Copy",
    onClick: async () => {
      await navigator.clipboard.writeText(text)
    },
  }),
  refresh: (onRefresh: () => void | Promise<void>): ActionItem => ({
    id: "refresh",
    icon: <RotateCcw className="h-4 w-4" />,
    label: "Regenerate",
    onClick: onRefresh,
  }),
  like: (onLike: () => void | Promise<void>): ActionItem => ({
    id: "like",
    icon: <ThumbsUp className="h-4 w-4" />,
    label: "Like",
    onClick: onLike,
  }),
  dislike: (onDislike: () => void | Promise<void>): ActionItem => ({
    id: "dislike",
    icon: <ThumbsDown className="h-4 w-4" />,
    label: "Dislike",
    onClick: onDislike,
  }),
}

// Example usage component
export function AIActionsExample() {
  const handleRefresh = () => {
    console.log("Regenerating...")
  }

  const handleLike = () => {
    console.log("Liked!")
  }

  const handleDislike = () => {
    console.log("Disliked!")
  }

  const actions: ActionItem[] = [
    commonActions.copy(
      "Here's a quick example of how to use React hooks. The useState hook lets you add state to functional components, while useEffect handles side effects like data fetching or subscriptions."
    ),
    commonActions.refresh(handleRefresh),
    commonActions.like(handleLike),
    commonActions.dislike(handleDislike),
  ]

  return (
    <div className="space-y-4">
      <p className="text-sm text-gray-600">
        Here's a quick example of how to use React hooks. The useState hook lets
        you add state to functional components, while useEffect handles side
        effects like data fetching or subscriptions.
      </p>
      <AIActions actions={actions} />
    </div>
  )
}
