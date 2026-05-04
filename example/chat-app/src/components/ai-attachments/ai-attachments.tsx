"use client"

import { Button } from "@/components/ui/button"
import { FileText, Image, Music, Video, X } from "lucide-react"

export interface Attachment {
  id: string
  name: string
  type: string // MIME type
  size?: number
  preview?: string // URL for image previews
  url?: string
}

export interface AIAttachmentsProps {
  attachments: Attachment[]
  onRemove?: (id: string) => void
  variant?: "grid" | "inline" | "list"
  maxColumns?: number
  className?: string
}

function getFileIcon(type: string) {
  if (type.startsWith("image/")) {
    return <Image className="h-4 w-4" />
  }
  if (type.startsWith("audio/")) {
    return <Music className="h-4 w-4" />
  }
  if (type.startsWith("video/")) {
    return <Video className="h-4 w-4" />
  }
  if (type === "application/pdf") {
    return <FileText className="h-4 w-4" />
  }
  if (type.includes("document") || type.includes("word")) {
    return <FileText className="h-4 w-4" />
  }
  return <FileText className="h-4 w-4" />
}

function getMimeTypeLabel(type: string): string {
  const typeMap: Record<string, string> = {
    "image/jpeg": "image/jpeg",
    "image/png": "image/png",
    "application/pdf": "application/pdf",
    "audio/mpeg": "audio/mpeg",
    "video/mp4": "video/mp4",
  }
  return typeMap[type] || type
}

function GridVariant({
  attachments,
  onRemove,
}: {
  attachments: Attachment[]
  onRemove?: (id: string) => void
}) {
  return (
    <div className="grid grid-cols-3 gap-3">
      {attachments.map((attachment) => (
        <div key={attachment.id} className="group relative">
          {attachment.preview && attachment.type.startsWith("image/") ? (
            <img
              src={attachment.preview}
              alt={attachment.name}
              className="aspect-square rounded-lg object-cover"
            />
          ) : (
            <div className="flex aspect-square items-center justify-center rounded-lg bg-gray-100">
              {getFileIcon(attachment.type)}
            </div>
          )}
          {onRemove && (
            <button
              onClick={() => onRemove(attachment.id)}
              className="bg-opacity-60 absolute top-2 right-2 rounded-full bg-gray-900 p-1 opacity-0 transition-opacity group-hover:opacity-100"
            >
              <X className="h-3 w-3 text-white" />
            </button>
          )}
        </div>
      ))}
    </div>
  )
}

function InlineVariant({
  attachments,
  onRemove,
}: {
  attachments: Attachment[]
  onRemove?: (id: string) => void
}) {
  return (
    <div className="flex flex-wrap gap-2">
      {attachments.map((attachment) => (
        <div
          key={attachment.id}
          className="inline-flex items-center gap-2 rounded-full bg-gray-100 px-3 py-1.5"
        >
          <span className="text-gray-600">{getFileIcon(attachment.type)}</span>
          <span className="text-xs font-medium text-gray-700">
            {attachment.name}
          </span>
          {onRemove && (
            <button
              onClick={() => onRemove(attachment.id)}
              className="ml-1 text-gray-400 hover:text-gray-600"
            >
              <X className="h-3 w-3" />
            </button>
          )}
        </div>
      ))}
    </div>
  )
}

function ListVariant({
  attachments,
  onRemove,
}: {
  attachments: Attachment[]
  onRemove?: (id: string) => void
}) {
  return (
    <div className="space-y-2">
      {attachments.map((attachment) => (
        <div
          key={attachment.id}
          className="flex items-center justify-between rounded-lg bg-gray-50 px-4 py-3"
        >
          <div className="flex items-center gap-3">
            <div className="flex h-10 w-10 items-center justify-center rounded bg-gray-200">
              {attachment.preview && attachment.type.startsWith("image/") ? (
                <img
                  src={attachment.preview}
                  alt={attachment.name}
                  className="h-full w-full rounded object-cover"
                />
              ) : (
                getFileIcon(attachment.type)
              )}
            </div>
            <div className="flex flex-col">
              <p className="text-sm font-medium text-gray-900">
                {attachment.name}
              </p>
              <p className="text-xs text-gray-500">
                {getMimeTypeLabel(attachment.type)}
              </p>
            </div>
          </div>
          {onRemove && (
            <Button
              variant="ghost"
              size="icon"
              className="h-8 w-8"
              onClick={() => onRemove(attachment.id)}
            >
              <X className="h-4 w-4" />
            </Button>
          )}
        </div>
      ))}
    </div>
  )
}

export function AIAttachments({
  attachments,
  onRemove,
  variant = "grid",
  className,
}: AIAttachmentsProps) {
  if (attachments.length === 0) {
    return null
  }

  return (
    <div className={className}>
      {variant === "grid" && (
        <GridVariant attachments={attachments} onRemove={onRemove} />
      )}
      {variant === "inline" && (
        <InlineVariant attachments={attachments} onRemove={onRemove} />
      )}
      {variant === "list" && (
        <ListVariant attachments={attachments} onRemove={onRemove} />
      )}
    </div>
  )
}

// Example attachments
export const exampleAttachments: Attachment[] = [
  {
    id: "1",
    name: "photo-1.jpg",
    type: "image/jpeg",
    preview:
      "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 200 200'%3E%3Crect fill='%23666' width='200' height='200'/%3E%3Crect fill='%23999' x='50' y='60' width='100' height='80'/%3E%3C/svg%3E",
  },
  {
    id: "2",
    name: "report.pdf",
    type: "application/pdf",
  },
  {
    id: "3",
    name: "podcast.mp3",
    type: "audio/mpeg",
  },
  {
    id: "4",
    name: "demo.mp4",
    type: "video/mp4",
  },
  {
    id: "5",
    name: "API Documentation",
    type: "text/html",
    url: "https://example.com",
  },
]
