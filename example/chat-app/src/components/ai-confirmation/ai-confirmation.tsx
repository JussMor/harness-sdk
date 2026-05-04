"use client"

import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { AlertCircle } from "lucide-react"

export interface AIConfirmationProps {
  title?: string
  message: string
  confirmLabel?: string
  rejectLabel?: string
  onConfirm: () => void | Promise<void>
  onReject: () => void
  isLoading?: boolean
  isDangerous?: boolean
}

export function AIConfirmation({
  title = "Confirm Action",
  message,
  confirmLabel = "Approve",
  rejectLabel = "Reject",
  onConfirm,
  onReject,
  isLoading = false,
  isDangerous = false,
}: AIConfirmationProps) {
  return (
    <Card className="w-full max-w-lg p-6">
      <div className="space-y-4">
        {/* Header */}
        {title && (
          <div className="flex items-start gap-3">
            {isDangerous && (
              <AlertCircle className="h-5 w-5 flex-shrink-0 text-red-500" />
            )}
            <h2 className="text-lg font-semibold text-gray-900">{title}</h2>
          </div>
        )}

        {/* Message */}
        <p className="text-sm text-gray-700">{message}</p>

        {/* Actions */}
        <div className="flex justify-end gap-3 pt-4">
          <Button
            variant="outline"
            onClick={onReject}
            disabled={isLoading}
            className="min-w-24"
          >
            {rejectLabel}
          </Button>
          <Button
            onClick={onConfirm}
            disabled={isLoading}
            className={isDangerous ? "bg-red-600 hover:bg-red-700" : ""}
            variant={isDangerous ? "default" : "default"}
          >
            {isLoading ? (
              <div className="flex items-center gap-2">
                <div className="h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent" />
                <span>Processing...</span>
              </div>
            ) : (
              confirmLabel
            )}
          </Button>
        </div>
      </div>
    </Card>
  )
}

// Example
export const exampleConfirmation: AIConfirmationProps = {
  title: "Delete File",
  message:
    "This tool wants to delete the file /tmp/example.txt. Do you approve this action?",
  confirmLabel: "Approve",
  rejectLabel: "Reject",
  isDangerous: true,
  onConfirm: () => console.log("confirmed"),
  onReject: () => console.log("rejected"),
}
