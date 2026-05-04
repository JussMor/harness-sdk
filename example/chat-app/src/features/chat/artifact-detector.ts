/**
 * Artifact Detector — detects fenced code blocks in streaming text that should
 * be rendered in the canvas panel instead of inline in chat.
 *
 * An "artifact" is a fenced block (```) with a recognized language tag that
 * indicates rich content the user would want to view/edit in a canvas:
 * markdown, html, jsx, tsx, svg, css, json, yaml, python, javascript, etc.
 */

export interface Artifact {
  /** Unique ID for this artifact instance */
  id: string
  /** Language/type from the opening fence (e.g., "html", "md", "tsx") */
  language: string
  /** The content inside the fences — grows during streaming */
  content: string
  /** Whether the closing fence has been seen */
  complete: boolean
  /** Optional title extracted from a comment on the first line */
  title?: string
}

/** Languages that trigger canvas rendering */
const CANVAS_LANGUAGES = new Set([
  "md",
  "markdown",
  "html",
  "htm",
  "jsx",
  "tsx",
  "svg",
  "css",
  "json",
  "yaml",
  "yml",
  "python",
  "py",
  "javascript",
  "js",
  "typescript",
  "ts",
  "bash",
  "sh",
  "sql",
  "graphql",
  "xml",
  "toml",
  "rust",
  "go",
  "react",
])

export interface DetectorState {
  /** Text that belongs to the chat (outside fenced blocks) */
  chatContent: string
  /** Currently active artifacts (at most one open at a time) */
  artifacts: Array<Artifact>
  /** Index of the currently open/streaming artifact, or -1 */
  activeIndex: number
}

const FENCE_OPEN_RE = /^```(\w+)?\s*$/m
const FENCE_CLOSE_RE = /^```\s*$/m

/**
 * Creates a fresh detector state.
 */
export function createDetectorState(): DetectorState {
  return { chatContent: "", artifacts: [], activeIndex: -1 }
}

/**
 * Process an incremental delta against the current state.
 * Returns the updated state (immutable — always a new object).
 *
 * Call this for every `delta` event from the SSE stream.
 */
export function processStreamDelta(
  state: DetectorState,
  delta: string
): DetectorState {
  // If we're inside an open artifact, content goes there
  if (state.activeIndex >= 0) {
    return appendToArtifact(state, delta)
  }

  // Otherwise, content goes to chat — check if a fence opens
  return appendToChat(state, delta)
}

/**
 * Mark stream as complete. If there's still an open artifact, close it.
 */
export function finalizeStream(state: DetectorState): DetectorState {
  if (state.activeIndex < 0) return state

  const artifacts = [...state.artifacts]
  artifacts[state.activeIndex] = {
    ...artifacts[state.activeIndex],
    complete: true,
  }
  return { ...state, artifacts, activeIndex: -1 }
}

// ── Internal helpers ─────────────────────────────────────────────────────────

function appendToChat(state: DetectorState, delta: string): DetectorState {
  const combined = state.chatContent + delta

  // Check if a fence just opened
  const match = combined.match(FENCE_OPEN_RE)
  if (!match) {
    return { ...state, chatContent: combined }
  }

  const lang = (match[1] || "text").toLowerCase()

  // Only open canvas for recognized languages
  if (!CANVAS_LANGUAGES.has(lang)) {
    return { ...state, chatContent: combined }
  }

  // Split: text before the fence stays in chat, content after goes to artifact
  const fenceStart = combined.indexOf(match[0])
  const fenceEnd = fenceStart + match[0].length
  const chatBefore = combined.slice(0, fenceStart)
  const afterFence = combined.slice(fenceEnd)

  // Remove trailing newline from chat (the fence line itself)
  const cleanChat = chatBefore.replace(/\n$/, "")

  const newArtifact: Artifact = {
    id: `artifact-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
    language: normalizeLanguage(lang),
    content: afterFence.startsWith("\n") ? afterFence.slice(1) : afterFence,
    complete: false,
  }

  const artifacts = [...state.artifacts, newArtifact]
  return {
    chatContent: cleanChat,
    artifacts,
    activeIndex: artifacts.length - 1,
  }
}

function appendToArtifact(state: DetectorState, delta: string): DetectorState {
  const artifact = state.artifacts[state.activeIndex]
  const combined = artifact.content + delta

  // Check if closing fence appeared
  const closeMatch = combined.match(FENCE_CLOSE_RE)
  if (!closeMatch) {
    // Still streaming artifact content
    const artifacts = [...state.artifacts]
    artifacts[state.activeIndex] = { ...artifact, content: combined }
    return { ...state, artifacts }
  }

  // Close the artifact — content before the closing fence
  const closeIdx = combined.indexOf(closeMatch[0])
  const artifactContent = combined.slice(0, closeIdx).replace(/\n$/, "")
  const afterClose = combined.slice(closeIdx + closeMatch[0].length)

  const artifacts = [...state.artifacts]
  artifacts[state.activeIndex] = {
    ...artifact,
    content: artifactContent,
    complete: true,
    title: extractTitle(artifactContent, artifact.language),
  }

  // Anything after the closing fence goes back to chat
  return {
    chatContent: state.chatContent + afterClose,
    artifacts,
    activeIndex: -1,
  }
}

function normalizeLanguage(lang: string): string {
  const map: Record<string, string> = {
    md: "markdown",
    htm: "html",
    js: "javascript",
    ts: "typescript",
    py: "python",
    sh: "bash",
    yml: "yaml",
    react: "tsx",
  }
  return map[lang] || lang
}

function extractTitle(content: string, language: string): string | undefined {
  const firstLine = content.split("\n")[0]?.trim()
  if (!firstLine) return undefined

  // HTML/JSX: <!-- Title -->
  if (language === "html" || language === "jsx" || language === "tsx") {
    const m = firstLine.match(/<!--\s*(.+?)\s*-->/)
    if (m) return m[1]
  }
  // Markdown: # Title
  if (language === "markdown") {
    const m = firstLine.match(/^#\s+(.+)/)
    if (m) return m[1]
  }
  // Python/bash: # Title
  if (language === "python" || language === "bash") {
    const m = firstLine.match(/^#\s+(.+)/)
    if (m) return m[1]
  }
  // JS/TS: // Title
  if (language === "javascript" || language === "typescript") {
    const m = firstLine.match(/^\/\/\s+(.+)/)
    if (m) return m[1]
  }

  return undefined
}
