"use client"

import { useA2UI } from "@/hooks/useA2UI"
import { cn } from "@/lib/utils"

export interface A2UISurfaceProps {
  surfaceId: string
  title?: string
  className?: string
}

export function A2UISurface({ surfaceId, title, className }: A2UISurfaceProps) {
  useA2UI()

  return (
    <div className={cn("w-full space-y-2", className)}>
      {title && (
        <h2 className="text-lg font-semibold text-gray-900">{title}</h2>
      )}
      <div
        data-a2ui-surface={surfaceId}
        className="rounded-lg border border-gray-200 bg-white p-4"
      >
        <p className="text-sm text-gray-600">A2UI Surface: {surfaceId}</p>
      </div>
    </div>
  )
}
