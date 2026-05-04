import type { Artifact } from "@/features/chat/artifact-detector"
import type { ArtifactVersion } from "@/features/chat/types"
import {
  ChevronLeft,
  ChevronRight,
  Code,
  FileText,
  Maximize2,
  Minimize2,
  Pencil,
  X,
} from "lucide-react"
import { useCallback, useEffect, useRef, useState } from "react"

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

export function ArtifactCanvas({
  artifact,
  isStreaming,
  versions = [],
  onClose,
  onSaveVersion,
  apiBaseURL = "",
}: ArtifactCanvasProps) {
  const [isFullscreen, setIsFullscreen] = useState(false)
  const [isEditing, setIsEditing] = useState(false)
  const [editContent, setEditContent] = useState("")
  const [isSaving, setIsSaving] = useState(false)
  // Which version index is shown (0 = oldest, versions.length-1 = newest)
  const [versionIndex, setVersionIndex] = useState<number | null>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const iframeRef = useRef<HTMLIFrameElement>(null)

  // Auto-scroll during streaming
  useEffect(() => {
    if (isStreaming && contentRef.current) {
      contentRef.current.scrollTop = contentRef.current.scrollHeight
    }
  }, [artifact?.content, isStreaming])

  // When artifact completes and user hasn't started editing, sync edit content
  useEffect(() => {
    if (artifact?.complete && !isEditing) {
      setEditContent(activeContent)
    }
  }, [artifact?.complete, artifact?.content, isEditing])

  // Reset version index to latest when versions change
  useEffect(() => {
    if (versions.length > 0) {
      setVersionIndex(versions.length - 1)
    }
  }, [versions.length])

  // Storage bridge: handle postMessage from iframe artifacts
  // Mirrors the window.storage API described in Claude's system prompt
  useEffect(() => {
    if (!apiBaseURL || !artifact?.id) return

    const handleMessage = async (ev: MessageEvent) => {
      if (ev.source !== iframeRef.current?.contentWindow) return
      const { type, key, value, shared } = ev.data ?? {}
      if (!type || !key) return

      const artifactId = artifact.id

      try {
        switch (type) {
          case "storage:get": {
            const r = await fetch(
              `${apiBaseURL}/api/artifacts/${artifactId}/storage?shared=${shared ?? false}`
            )
            const body = await r.json()
            const result = body.data?.[key] ?? null
            ev.source?.postMessage({ type: "storage:get:result", key, value: result }, { targetOrigin: "*" })
            break
          }
          case "storage:set": {
            await fetch(`${apiBaseURL}/api/artifacts/${artifactId}/storage`, {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ key, value, shared: shared ?? false }),
            })
            ev.source?.postMessage({ type: "storage:set:result", key, ok: true }, { targetOrigin: "*" })
            break
          }
          case "storage:delete": {
            const params = new URLSearchParams({ shared: String(shared ?? false) })
            await fetch(`${apiBaseURL}/api/artifacts/${artifactId}/storage/${key}?${params}`, {
              method: "DELETE",
            })
            ev.source?.postMessage({ type: "storage:delete:result", key, ok: true }, { targetOrigin: "*" })
            break
          }
          case "storage:list": {
            const r = await fetch(
              `${apiBaseURL}/api/artifacts/${artifactId}/storage?shared=${shared ?? false}`
            )
            const body = await r.json()
            const keys = Object.keys(body.data ?? {})
            ev.source?.postMessage({ type: "storage:list:result", keys }, { targetOrigin: "*" })
            break
          }
        }
      } catch (err) {
        console.warn("storage bridge error", err)
      }
    }

    window.addEventListener("message", handleMessage)
    return () => window.removeEventListener("message", handleMessage)
  }, [artifact?.id, apiBaseURL])

  const handleStartEditing = useCallback(() => {
    if (!artifact || isStreaming) return
    setEditContent(activeContent)
    setIsEditing(true)
    setTimeout(() => textareaRef.current?.focus(), 0)
  }, [artifact, isStreaming])

  const handleSaveEdit = useCallback(async () => {
    if (!artifact) return
    setIsSaving(true)
    try {
      await onSaveVersion?.(artifact.id, editContent)
    } finally {
      setIsSaving(false)
    }
    setIsEditing(false)
  }, [artifact, editContent, onSaveVersion])

  const handleCancelEdit = useCallback(() => {
    setIsEditing(false)
    setEditContent(activeContent)
  }, [artifact])

  if (!artifact) return null

  // Determine which content to display: backend version or streaming content
  const currentVersion = versionIndex !== null ? versions[versionIndex] : null
  const activeContent = currentVersion?.content ?? artifact.content ?? ""
  const totalVersions = versions.length || (artifact.complete ? 1 : 0)
  const displayVersionNum = versionIndex !== null ? versionIndex + 1 : totalVersions

  const title = artifact.title || `${artifact.language} artifact`
  const langIcon = getLanguageIcon(artifact.language)
  const showPreview = artifact.language === "html" && !isEditing

  return (
    <aside className={`artifact-canvas ${isFullscreen ? "artifact-canvas--fullscreen" : ""}`}>
      {/* Header */}
      <header className="artifact-canvas__header">
        <div className="artifact-canvas__title">
          {langIcon}
          <span>{title}</span>
          {isStreaming && (
            <span className="artifact-canvas__streaming-badge">streaming…</span>
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
                className={`artifact-canvas__btn artifact-canvas__btn--save ${isSaving ? "artifact-canvas__btn--loading" : ""}`}
                onClick={handleSaveEdit}
                disabled={isSaving}
              >
                {isSaving ? "Saving…" : "Save"}
              </button>
              <button
                type="button"
                className="artifact-canvas__btn"
                onClick={handleCancelEdit}
                disabled={isSaving}
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

      {/* Language + version bar */}
      <div className="artifact-canvas__lang-bar">
        <span className="artifact-canvas__lang-badge">{artifact.language}</span>
        {activeContent && (
          <span className="artifact-canvas__size">{activeContent.length} chars</span>
        )}

        {/* Version navigator */}
        {totalVersions > 1 && !isEditing && (
          <div className="artifact-canvas__versions">
            <button
              type="button"
              className="artifact-canvas__version-btn"
              disabled={displayVersionNum <= 1}
              onClick={() => setVersionIndex((i) => Math.max(0, (i ?? versions.length - 1) - 1))}
              title="Previous version"
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
              onClick={() =>
                setVersionIndex((i) => Math.min(versions.length - 1, (i ?? 0) + 1))
              }
              title="Next version"
            >
              <ChevronRight size={12} />
            </button>
          </div>
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
        ) : showPreview ? (
          <HtmlPreview ref={iframeRef} content={activeContent} artifactId={artifact.id} />
        ) : (
          <pre className="artifact-canvas__pre">
            <code>{activeContent || " "}</code>
            {isStreaming && <span className="artifact-canvas__cursor">▊</span>}
          </pre>
        )}
      </div>
    </aside>
  )
}

// ── HTML Preview (sandboxed iframe) ───────────────────────────────────────────

interface HtmlPreviewProps {
  content: string
  artifactId: string
  ref?: React.Ref<HTMLIFrameElement>
}

function HtmlPreview({ content, artifactId, ref }: HtmlPreviewProps) {
  // Inject the storage bridge client script into the HTML so artifacts
  // can call window.storage.get/set/delete/list from inside the iframe.
  const bridgedContent = injectStorageBridge(content, artifactId)
  return (
    <iframe
      ref={ref}
      className="artifact-canvas__iframe"
      srcDoc={bridgedContent}
      sandbox="allow-scripts allow-same-origin"
      title="HTML Preview"
    />
  )
}

// Injects a tiny script that translates window.storage API calls
// into postMessage calls to the parent (chat-app), which forwards
// them to the backend storage endpoint.
function injectStorageBridge(html: string, artifactId: string): string {
  const bridge = `
<script>
(function() {
  function req(type, key, value, shared) {
    return new Promise(function(resolve, reject) {
      var id = Math.random().toString(36).slice(2);
      function handler(ev) {
        if (!ev.data || ev.data._id !== id) return;
        window.removeEventListener('message', handler);
        if (ev.data.ok === false) reject(new Error(ev.data.error || 'storage error'));
        else resolve(ev.data.value !== undefined ? ev.data.value : ev.data);
      }
      window.addEventListener('message', handler);
      window.parent.postMessage({ type: type, key: key, value: value, shared: shared, _id: id, artifactId: '${artifactId}' }, '*');
    });
  }
  window.storage = {
    get: function(key, shared) { return req('storage:get', key, undefined, shared); },
    set: function(key, value, shared) { return req('storage:set', key, value, shared); },
    delete: function(key, shared) { return req('storage:delete', key, undefined, shared); },
    list: function(shared) { return req('storage:list', '*', undefined, shared).then(function(r) { return r.keys || []; }); },
  };
})();
</script>`

  // Inject after <head> or at the start
  if (html.includes("<head>")) {
    return html.replace("<head>", "<head>" + bridge)
  }
  return bridge + html
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function getLanguageIcon(language: string) {
  switch (language) {
    case "markdown":
    case "md":
      return <FileText size={14} />
    default:
      return <Code size={14} />
  }
}
