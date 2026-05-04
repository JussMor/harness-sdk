import type { Artifact } from "@/features/chat/artifact-detector"
import { Code, FileText, Maximize2, Minimize2, Pencil, X } from "lucide-react"
import { useCallback, useEffect, useRef, useState } from "react"

export interface ArtifactCanvasProps {
  /** The artifact to render (streaming or complete) */
  artifact: Artifact | null
  /** Whether the stream is still producing content for this artifact */
  isStreaming: boolean
  /** Called when user closes the canvas */
  onClose: () => void
  /** Called when user edits content locally */
  onContentChange?: (artifactId: string, newContent: string) => void
}

export function ArtifactCanvas({
  artifact,
  isStreaming,
  onClose,
  onContentChange,
}: ArtifactCanvasProps) {
  const [isFullscreen, setIsFullscreen] = useState(false)
  const [isEditing, setIsEditing] = useState(false)
  const [editContent, setEditContent] = useState("")
  const contentRef = useRef<HTMLDivElement>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  // Auto-scroll during streaming
  useEffect(() => {
    if (isStreaming && contentRef.current) {
      contentRef.current.scrollTop = contentRef.current.scrollHeight
    }
  }, [artifact?.content, isStreaming])

  // When artifact completes and user hasn't started editing, sync edit content
  useEffect(() => {
    if (artifact?.complete && !isEditing) {
      setEditContent(artifact.content)
    }
  }, [artifact?.complete, artifact?.content, isEditing])

  const handleStartEditing = useCallback(() => {
    if (!artifact || isStreaming) return
    setEditContent(artifact.content)
    setIsEditing(true)
  }, [artifact, isStreaming])

  const handleSaveEdit = useCallback(() => {
    if (!artifact) return
    onContentChange?.(artifact.id, editContent)
    setIsEditing(false)
  }, [artifact, editContent, onContentChange])

  const handleCancelEdit = useCallback(() => {
    setIsEditing(false)
    if (artifact) {
      setEditContent(artifact.content)
    }
  }, [artifact])

  if (!artifact) return null

  const title = artifact.title || `${artifact.language} artifact`
  const langIcon = getLanguageIcon(artifact.language)

  return (
    <aside
      className={`artifact-canvas ${isFullscreen ? "artifact-canvas--fullscreen" : ""}`}
    >
      {/* Header */}
      <header className="artifact-canvas__header">
        <div className="artifact-canvas__title">
          {langIcon}
          <span>{title}</span>
          {isStreaming && (
            <span className="artifact-canvas__streaming-badge">
              streaming...
            </span>
          )}
          {artifact.complete && !isStreaming && (
            <span className="artifact-canvas__complete-badge">complete</span>
          )}
        </div>

        <div className="artifact-canvas__actions">
          {artifact.complete && !isStreaming && !isEditing && (
            <button
              type="button"
              className="artifact-canvas__btn"
              onClick={handleStartEditing}
              title="Edit"
            >
              <Pencil size={14} />
            </button>
          )}
          {isEditing && (
            <>
              <button
                type="button"
                className="artifact-canvas__btn artifact-canvas__btn--save"
                onClick={handleSaveEdit}
              >
                Save
              </button>
              <button
                type="button"
                className="artifact-canvas__btn"
                onClick={handleCancelEdit}
              >
                Cancel
              </button>
            </>
          )}
          <button
            type="button"
            className="artifact-canvas__btn"
            onClick={() => setIsFullscreen(!isFullscreen)}
            title={isFullscreen ? "Minimize" : "Maximize"}
          >
            {isFullscreen ? <Minimize2 size={14} /> : <Maximize2 size={14} />}
          </button>
          <button
            type="button"
            className="artifact-canvas__btn"
            onClick={onClose}
            title="Close"
          >
            <X size={14} />
          </button>
        </div>
      </header>

      {/* Language badge */}
      <div className="artifact-canvas__lang-bar">
        <span className="artifact-canvas__lang-badge">{artifact.language}</span>
        {artifact.content && (
          <span className="artifact-canvas__size">
            {artifact.content.length} chars
          </span>
        )}
      </div>

      {/* Content area */}
      <div className="artifact-canvas__content" ref={contentRef}>
        {isEditing ? (
          <textarea
            ref={textareaRef}
            className="artifact-canvas__editor"
            value={editContent}
            onChange={(e) => setEditContent(e.target.value)}
            spellCheck={false}
          />
        ) : artifact.language === "html" && artifact.complete ? (
          <HtmlPreview content={artifact.content} />
        ) : (
          <pre className="artifact-canvas__pre">
            <code>{artifact.content || " "}</code>
            {isStreaming && <span className="artifact-canvas__cursor">▊</span>}
          </pre>
        )}
      </div>
    </aside>
  )
}

// ── HTML Preview (sandboxed iframe) ──────────────────────────────────────────

function HtmlPreview({ content }: { content: string }) {
  return (
    <iframe
      className="artifact-canvas__iframe"
      srcDoc={content}
      sandbox="allow-scripts"
      title="HTML Preview"
    />
  )
}

// ── Helpers ──────────────────────────────────────────────────────────────────

function getLanguageIcon(language: string) {
  switch (language) {
    case "markdown":
      return <FileText size={14} />
    default:
      return <Code size={14} />
  }
}
