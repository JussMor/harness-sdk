import type { Artifact } from "@/features/chat/artifact-detector"
import type { ArtifactVersion } from "@/features/chat/types"
import {
  ChevronLeft,
  ChevronRight,
  Code,
  Eye,
  FileText,
  Maximize2,
  Minimize2,
  X,
} from "lucide-react"
import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react"

// Lazy-load heavy editors to avoid bundle bloat
const MDEditor = lazy(() => import("@uiw/react-md-editor"))
const MDPreview = lazy(() =>
  import("@uiw/react-md-editor").then((m) => ({ default: m.default.Markdown }))
)

export interface ArtifactCanvasProps {
  artifact: Artifact | null
  isStreaming: boolean
  versions?: ArtifactVersion[]
  onClose: () => void
  onSaveVersion?: (artifactId: string, content: string) => Promise<void>
  apiBaseURL?: string
}

type ViewMode = "preview" | "edit" | "split"

const isMarkdown = (lang: string) => lang === "md" || lang === "markdown"
const isHtml = (lang: string) => lang === "html" || lang === "htm"

export function ArtifactCanvas({
  artifact,
  isStreaming,
  versions = [],
  onClose,
  onSaveVersion,
  apiBaseURL = "",
}: ArtifactCanvasProps) {
  const [isFullscreen, setIsFullscreen] = useState(false)
  const [isSaving, setIsSaving] = useState(false)
  const [versionIndex, setVersionIndex] = useState<number | null>(null)
  const [viewMode, setViewMode] = useState<ViewMode>("preview")
  // Local edit buffer — only for non-streaming artifacts
  const [editContent, setEditContent] = useState("")
  const [isDirty, setIsDirty] = useState(false)
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const autosaveTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Sync version index to latest when new versions arrive
  useEffect(() => {
    if (versions.length > 0) setVersionIndex(versions.length - 1)
  }, [versions.length])

  // Sync edit buffer when artifact changes or version switches
  useEffect(() => {
    if (!isStreaming) {
      setEditContent(activeContent)
      setIsDirty(false)
    }
  }, [artifact?.id, versionIndex, isStreaming])

  // Autosave 2s after last keystroke (markdown only)
  const scheduleAutosave = useCallback(
    (content: string) => {
      if (!artifact?.id || !onSaveVersion) return
      if (autosaveTimer.current) clearTimeout(autosaveTimer.current)
      autosaveTimer.current = setTimeout(async () => {
        setIsSaving(true)
        try {
          await onSaveVersion(artifact.id, content)
          setIsDirty(false)
        } finally {
          setIsSaving(false)
        }
      }, 2000)
    },
    [artifact?.id, onSaveVersion]
  )

  const handleContentChange = useCallback(
    (value: string | undefined) => {
      const next = value ?? ""
      setEditContent(next)
      setIsDirty(true)
      scheduleAutosave(next)
    },
    [scheduleAutosave]
  )

  // Storage bridge for HTML iframe artifacts
  useEffect(() => {
    if (!apiBaseURL || !artifact?.id) return
    const handleMessage = async (ev: MessageEvent) => {
      if (ev.source !== iframeRef.current?.contentWindow) return
      const { type, key, value, shared } = ev.data ?? {}
      if (!type || !key) return
      const id = artifact.id
      try {
        if (type === "storage:get") {
          const r = await fetch(`${apiBaseURL}/api/artifacts/${id}/storage?shared=${shared ?? false}`)
          const body = await r.json()
          ev.source?.postMessage({ type: "storage:get:result", key, value: body.data?.[key] ?? null }, { targetOrigin: "*" })
        } else if (type === "storage:set") {
          await fetch(`${apiBaseURL}/api/artifacts/${id}/storage`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ key, value, shared: shared ?? false }),
          })
          ev.source?.postMessage({ type: "storage:set:result", key, ok: true }, { targetOrigin: "*" })
        } else if (type === "storage:delete") {
          const params = new URLSearchParams({ shared: String(shared ?? false) })
          await fetch(`${apiBaseURL}/api/artifacts/${id}/storage/${key}?${params}`, { method: "DELETE" })
          ev.source?.postMessage({ type: "storage:delete:result", key, ok: true }, { targetOrigin: "*" })
        }
      } catch (e) {
        console.warn("storage bridge error", e)
      }
    }
    window.addEventListener("message", handleMessage)
    return () => window.removeEventListener("message", handleMessage)
  }, [artifact?.id, apiBaseURL])

  if (!artifact) return null

  const currentVersion = versionIndex !== null ? versions[versionIndex] : null
  const activeContent = currentVersion?.content ?? artifact.content ?? ""
  const totalVersions = versions.length || (artifact.complete ? 1 : 0)
  const displayVersionNum = versionIndex !== null ? versionIndex + 1 : totalVersions
  const isMd = isMarkdown(artifact.language)
  const isHtmlArt = isHtml(artifact.language)
  const title = artifact.title || `${artifact.language} artifact`

  return (
    <aside className={`artifact-canvas ${isFullscreen ? "artifact-canvas--fullscreen" : ""}`}>

      {/* Header */}
      <header className="artifact-canvas__header">
        <div className="artifact-canvas__title">
          {isMd ? <FileText size={14} /> : <Code size={14} />}
          <span>{title}</span>
          {isStreaming && <span className="artifact-canvas__streaming-badge">streaming…</span>}
          {isDirty && !isSaving && <span className="artifact-canvas__dirty-badge">unsaved</span>}
          {isSaving && <span className="artifact-canvas__saving-badge">saving…</span>}
        </div>

        <div className="artifact-canvas__actions">
          {/* View mode toggle — only for markdown */}
          {isMd && artifact.complete && !isStreaming && (
            <div className="artifact-canvas__view-toggle">
              <button
                type="button"
                className={`artifact-canvas__toggle-btn ${viewMode === "preview" ? "artifact-canvas__toggle-btn--active" : ""}`}
                onClick={() => setViewMode("preview")}
                title="Preview"
              >
                <Eye size={13} />
              </button>
              <button
                type="button"
                className={`artifact-canvas__toggle-btn ${viewMode === "split" ? "artifact-canvas__toggle-btn--active" : ""}`}
                onClick={() => setViewMode("split")}
                title="Split view"
              >
                <span style={{ fontSize: 10, fontWeight: 600 }}>½</span>
              </button>
              <button
                type="button"
                className={`artifact-canvas__toggle-btn ${viewMode === "edit" ? "artifact-canvas__toggle-btn--active" : ""}`}
                onClick={() => setViewMode("edit")}
                title="Edit source"
              >
                <Code size={13} />
              </button>
            </div>
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

      {/* Lang + version bar */}
      <div className="artifact-canvas__lang-bar">
        <span className="artifact-canvas__lang-badge">{artifact.language}</span>
        {activeContent && (
          <span className="artifact-canvas__size">{activeContent.length} chars</span>
        )}
        {totalVersions > 1 && (
          <div className="artifact-canvas__versions">
            <button
              type="button"
              className="artifact-canvas__version-btn"
              disabled={displayVersionNum <= 1}
              onClick={() => setVersionIndex((i) => Math.max(0, (i ?? versions.length - 1) - 1))}
            >
              <ChevronLeft size={12} />
            </button>
            <span className="artifact-canvas__version-label">
              v{displayVersionNum}/{totalVersions}
            </span>
            <button
              type="button"
              className="artifact-canvas__version-btn"
              disabled={displayVersionNum >= totalVersions}
              onClick={() => setVersionIndex((i) => Math.min(versions.length - 1, (i ?? 0) + 1))}
            >
              <ChevronRight size={12} />
            </button>
          </div>
        )}
      </div>

      {/* Content */}
      <div className="artifact-canvas__content">
        {isStreaming ? (
          // During streaming always show raw text with cursor
          <pre className="artifact-canvas__pre">
            <code>{activeContent || " "}</code>
            <span className="artifact-canvas__cursor">▊</span>
          </pre>
        ) : isMd ? (
          // Markdown: WYSIWYG editor or preview depending on mode
          <Suspense fallback={<div className="artifact-canvas__loading">Loading editor…</div>}>
            {viewMode === "preview" && (
              <div className="artifact-canvas__md-preview" data-color-mode="light">
                <MDPreview source={editContent || activeContent} />
              </div>
            )}
            {viewMode === "edit" && (
              <div data-color-mode="light" className="artifact-canvas__md-editor">
                <MDEditor
                  value={editContent}
                  onChange={handleContentChange}
                  preview="edit"
                  hideToolbar={false}
                  height="100%"
                />
              </div>
            )}
            {viewMode === "split" && (
              <div data-color-mode="light" className="artifact-canvas__md-editor">
                <MDEditor
                  value={editContent}
                  onChange={handleContentChange}
                  preview="live"
                  hideToolbar={false}
                  height="100%"
                />
              </div>
            )}
          </Suspense>
        ) : isHtmlArt ? (
          <HtmlPreview ref={iframeRef} content={activeContent} artifactId={artifact.id} />
        ) : (
          <pre className="artifact-canvas__pre">
            <code>{activeContent || " "}</code>
          </pre>
        )}
      </div>
    </aside>
  )
}

// ── HTML Preview ──────────────────────────────────────────────────────────────

interface HtmlPreviewProps {
  content: string
  artifactId: string
  ref?: React.Ref<HTMLIFrameElement>
}

function HtmlPreview({ content, artifactId, ref }: HtmlPreviewProps) {
  const bridge = `<script>(function(){function req(t,k,v,s){return new Promise(function(res,rej){var id=Math.random().toString(36).slice(2);function h(e){if(!e.data||e.data._id!==id)return;window.removeEventListener('message',h);e.data.ok===false?rej(new Error(e.data.error||'err')):res(e.data.value!==undefined?e.data.value:e.data);}window.addEventListener('message',h);window.parent.postMessage({type:t,key:k,value:v,shared:s,_id:id,artifactId:'${artifactId}'},'*');});}window.storage={get:function(k,s){return req('storage:get',k,undefined,s);},set:function(k,v,s){return req('storage:set',k,v,s);},delete:function(k,s){return req('storage:delete',k,undefined,s);},list:function(s){return req('storage:list','*',undefined,s).then(function(r){return r.keys||[];});}};})();</script>`

  const html = content.includes("<head>")
    ? content.replace("<head>", `<head>${bridge}`)
    : bridge + content

  return (
    <iframe
      ref={ref}
      className="artifact-canvas__iframe"
      srcDoc={html}
      sandbox="allow-scripts allow-same-origin"
      title="HTML Preview"
    />
  )
}

export interface ArtifactCanvasProps {
  /** The artifact to render (streaming or complete) */
  artifact: Artifact | null
  /** Whether the stream is still producing content for this artifact */
  isStreaming: boolean
  /** All persisted versions from the backend (may be empty during streaming) */
  versions?: ArtifactVersion[]
  /** Called when user closes the canvas */
  onClose: () => void
  /** Called when user saves a local edit — triggers a new version on the backend */
  onSaveVersion?: (artifactId: string, content: string) => Promise<void>
  /** Backend API base URL — used by the storage bridge */
  apiBaseURL?: string
}

