"use client"

import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select"
import { cn } from "@/lib/utils"
import { ArrowUp, Brain, Mic, Paperclip } from "lucide-react"
import { useRef, useState } from "react"

export interface AIPromptInputMode {
  id: string
  name: string
}

export interface AIPromptInputProps {
  placeholder?: string
  onSubmit: (prompt: string) => void | Promise<void>
  onFileSelect?: (files: Array<File>) => void
  isLoading?: boolean
  maxLength?: number
  minHeight?: string
  maxHeight?: string
  showCharCount?: boolean
  allowAttachments?: boolean
  planText?: string
  modes?: Array<AIPromptInputMode>
  selectedMode?: string
  onModeChange?: (mode: string) => void
  className?: string
}

export function AIPromptInput({
  placeholder = "Ask me anything...",
  onSubmit,
  onFileSelect,
  isLoading = false,
  maxLength = 4000,
  minHeight = "120px",
  maxHeight = "200px",
  showCharCount = false,
  allowAttachments = true,
  planText = "Use our faster AI on Pro Plan",
  modes = [{ id: "balanced", name: "Balanced" }],
  selectedMode = "balanced",
  onModeChange,
  className,
}: AIPromptInputProps) {
  const [prompt, setPrompt] = useState("")
  const [isSubmitting, setIsSubmitting] = useState(false)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault()
      handleSubmit()
    }
  }

  const handleSubmit = async () => {
    if (!prompt.trim() || isSubmitting || isLoading) return

    setIsSubmitting(true)
    try {
      await onSubmit(prompt)
      setPrompt("")
      // Reset textarea height
      if (textareaRef.current) {
        textareaRef.current.style.height = "auto"
      }
    } finally {
      setIsSubmitting(false)
    }
  }

  const handleInput = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const value = e.target.value.slice(0, maxLength)
    setPrompt(value)

    // Auto-resize textarea
    if (textareaRef.current) {
      textareaRef.current.style.height = "auto"
      textareaRef.current.style.height = `${Math.min(textareaRef.current.scrollHeight, 200)}px`
    }
  }

  const handleFileInput = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files || [])
    onFileSelect?.(files)
    // Reset file input
    if (fileInputRef.current) {
      fileInputRef.current.value = ""
    }
  }

  return (
    <div className={cn("w-full space-y-4", className)}>
      <div className="rounded-[26px] border border-zinc-200 bg-zinc-100 p-2 shadow-[inset_0_-6px_0_0_rgba(0,0,0,0.04)]">
        <div className="px-4 py-2 text-sm text-zinc-700">
          <span>{planText}</span>
          <span className="mx-2">.</span>
          <button className="font-medium text-zinc-900 hover:underline">
            Upgrade
          </button>
        </div>

        <div className="rounded-[22px] border border-zinc-200 bg-white p-5 shadow-sm">
          <textarea
            ref={textareaRef}
            value={prompt}
            onChange={handleInput}
            onKeyDown={handleKeyDown}
            placeholder={placeholder}
            disabled={isLoading || isSubmitting}
            className="w-full resize-none border-0 bg-transparent text-[18px] leading-7 text-zinc-800 outline-none placeholder:text-zinc-400 disabled:opacity-50"
            style={{
              minHeight,
              maxHeight,
            }}
          />

          <div className="mt-4 flex items-center justify-between gap-3">
            <div className="flex items-center gap-3">
              {allowAttachments && (
                <>
                  <button
                    type="button"
                    onClick={() => fileInputRef.current?.click()}
                    disabled={isLoading || isSubmitting}
                    className="inline-flex h-11 w-11 items-center justify-center rounded-full border border-zinc-200 text-zinc-800 transition hover:bg-zinc-50 disabled:opacity-40"
                    title="Attach files"
                  >
                    <Paperclip className="h-5 w-5" />
                  </button>
                  <input
                    ref={fileInputRef}
                    type="file"
                    multiple
                    className="hidden"
                    onChange={handleFileInput}
                    disabled={isLoading || isSubmitting}
                  />
                </>
              )}

              <div className="inline-flex h-11 items-center gap-2 rounded-full border border-zinc-200 bg-white px-3 shadow-sm">
                <Brain className="h-5 w-5 text-zinc-500" />
                <NativeSelect
                  value={selectedMode}
                  onChange={(e) => onModeChange?.(e.target.value)}
                  disabled={isLoading || isSubmitting}
                  className="min-w-40"
                  aria-label="Select mode"
                >
                  {modes.map((mode) => (
                    <NativeSelectOption key={mode.id} value={mode.id}>
                      {mode.name}
                    </NativeSelectOption>
                  ))}
                </NativeSelect>
              </div>
            </div>

            <div className="flex items-center gap-3">
              <button
                type="button"
                className="inline-flex h-11 w-11 items-center justify-center rounded-full border border-zinc-200 text-zinc-800 transition hover:bg-zinc-50"
                title="Voice input"
              >
                <Mic className="h-5 w-5" />
              </button>

              <button
                type="button"
                onClick={handleSubmit}
                disabled={!prompt.trim() || isLoading || isSubmitting}
                className="inline-flex h-11 w-11 items-center justify-center rounded-full bg-zinc-700 text-white transition hover:bg-zinc-800 disabled:cursor-not-allowed disabled:bg-zinc-300"
                title="Send prompt (Enter)"
              >
                <ArrowUp className="h-5 w-5" />
              </button>
            </div>
          </div>
        </div>
      </div>

      {showCharCount && (
        <div className="flex items-center justify-between px-2">
          <div className="text-xs text-gray-500">
            {prompt.length} / {maxLength}
          </div>
          {maxLength && prompt.length > maxLength * 0.9 && (
            <div className="text-xs text-amber-600">
              {maxLength - prompt.length} characters remaining
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// Example usage
export function AIPromptInputExample() {
  const [responses, setResponses] = useState<Array<string>>([])
  const [selectedMode, setSelectedMode] = useState("balanced")

  const handleSubmit = async (prompt: string) => {
    setResponses((prev) => [...prev, prompt])
    // Simulate API delay
    await new Promise((resolve) => setTimeout(resolve, 500))
  }

  const handleFileSelect = (files: Array<File>) => {
    console.log("Files selected:", files)
  }

  return (
    <div className="w-full space-y-4">
      <AIPromptInput
        onSubmit={handleSubmit}
        onFileSelect={handleFileSelect}
        selectedMode={selectedMode}
        onModeChange={setSelectedMode}
        placeholder="Ask me anything... (Shift+Enter for new line)"
      />
      {responses.length > 0 && (
        <div className="space-y-2">
          <p className="text-sm font-medium text-gray-700">
            Recent prompts ({responses.length})
          </p>
          <div className="space-y-1">
            {responses.map((response, idx) => (
              <div
                key={idx}
                className="rounded bg-gray-50 p-2 text-xs text-gray-600"
              >
                {response}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
