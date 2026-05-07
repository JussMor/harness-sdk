import {
  Archive,
  Clock3,
  GitBranch,
  MessageSquarePlus,
  Search,
} from "lucide-react"
import { useMemo, useState } from "react"

import type { Thread } from "@/features/chat/types"
import { cn } from "@/lib/utils"

export interface SidebarChat {
  id: string
  title: string
  updatedAt: string
}

export interface ChatSidebarProps {
  chats: Array<SidebarChat>
  threads?: Array<Thread>
  activeId?: string
  onNewChat: () => void
  onSelectChat?: (chatId: string) => void
  onArchiveThread?: (threadId: string) => void
}

type ChatGroup = "Today" | "Yesterday" | "Last 7 Days" | "Older"

function computeGroup(updatedAt: string): ChatGroup {
  const updated = new Date(updatedAt)
  if (Number.isNaN(updated.getTime())) {
    return "Older"
  }

  const now = Date.now()
  const diffMs = now - updated.getTime()
  const oneDay = 1000 * 60 * 60 * 24

  if (diffMs < oneDay) {
    return "Today"
  }
  if (diffMs < oneDay * 2) {
    return "Yesterday"
  }
  if (diffMs < oneDay * 7) {
    return "Last 7 Days"
  }
  return "Older"
}

export function ChatSidebar({
  chats,
  threads,
  activeId,
  onNewChat,
  onSelectChat,
  onArchiveThread,
}: ChatSidebarProps) {
  const [query, setQuery] = useState("")

  const visibleChats = useMemo(() => {
    const normalized = query.trim().toLowerCase()
    if (!normalized) {
      return chats
    }
    return chats.filter((chat) => chat.title.toLowerCase().includes(normalized))
  }, [chats, query])

  const groups = useMemo(() => {
    const result: Record<ChatGroup, Array<SidebarChat>> = {
      Today: [],
      Yesterday: [],
      "Last 7 Days": [],
      Older: [],
    }

    for (const chat of visibleChats) {
      result[computeGroup(chat.updatedAt)].push(chat)
    }

    return result
  }, [visibleChats])

  return (
    <section className="sidebar-root">
      <header className="sidebar-header">
        <button className="sidebar-new-chat" onClick={onNewChat} type="button">
          <MessageSquarePlus size={16} />
          New Chat
        </button>
      </header>

      <div className="sidebar-search-wrap">
        <Search className="sidebar-search-icon" size={15} />
        <input
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          className="sidebar-search"
          placeholder="Search chat..."
        />
      </div>

      <div className="sidebar-list">
        {(Object.keys(groups) as Array<ChatGroup>).map((label) => {
          const items = groups[label]
          if (items.length === 0) {
            return null
          }

          return (
            <div key={label} className="sidebar-group">
              <p className="sidebar-group-title">{label}</p>
              {items.map((chat) => (
                <button
                  key={chat.id}
                  onClick={() => onSelectChat?.(chat.id)}
                  type="button"
                  className={cn(
                    "sidebar-chat-item",
                    activeId === chat.id && "sidebar-chat-item-active"
                  )}
                >
                  <Clock3 size={13} />
                  <span>{chat.title}</span>
                </button>
              ))}
            </div>
          )
        })}
      </div>

      {threads && threads.length > 0 && (
        <div className="sidebar-threads">
          <p className="sidebar-group-title">
            <GitBranch size={13} />
            Threads
          </p>
          {threads.map((thread) => (
            <div key={thread.id} className="sidebar-thread-item">
              <span
                className={cn(
                  "sidebar-thread-status",
                  `sidebar-thread-status--${thread.status}`
                )}
              >
                {thread.status}
              </span>
              <span className="sidebar-thread-id">{thread.id.slice(0, 8)}</span>
              {thread.mode_id && (
                <span className="sidebar-thread-mode">{thread.mode_id}</span>
              )}
              {onArchiveThread && thread.status === "active" && (
                <button
                  type="button"
                  className="sidebar-thread-archive"
                  onClick={() => onArchiveThread(thread.id)}
                  title="Archive thread"
                >
                  <Archive size={12} />
                </button>
              )}
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
