import { ChatMain } from "@/components/chat-main"
import type { SidebarChat } from "@/components/chat-sidebar"
import { ChatSidebar } from "@/components/chat-sidebar"
import { createFileRoute } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo, useState } from "react"

export const Route = createFileRoute("/")({
  component: IndexRoute,
})

const backendBaseURL = "http://localhost:8080"

interface BackendChat {
  id?: number
  title?: string
  updatedAt?: string
}

function toSidebarDate(updatedAt?: string): SidebarChat["date"] {
  if (!updatedAt) {
    return "older"
  }
  const updated = new Date(updatedAt)
  if (Number.isNaN(updated.getTime())) {
    return "older"
  }

  const now = Date.now()
  const diff = now - updated.getTime()
  const oneDay = 24 * 60 * 60 * 1000

  if (diff < oneDay) {
    return "today"
  }
  if (diff < oneDay * 2) {
    return "yesterday"
  }
  if (diff < oneDay * 7) {
    return "7days"
  }
  return "older"
}

function IndexRoute() {
  const [chats, setChats] = useState<Array<SidebarChat>>([])
  const [activeChatID, setActiveChatID] = useState<string | undefined>(
    undefined
  )
  const [isCreatingNewChat, setIsCreatingNewChat] = useState(true)

  const loadChats = useCallback(async () => {
    try {
      const res = await fetch(`${backendBaseURL}/api/chats`)
      if (!res.ok) {
        return
      }

      const payload = (await res.json()) as Array<BackendChat>
      const nextChats = payload
        .filter((chat) => typeof chat?.id === "number")
        .map((chat) => ({
          id: String(chat.id),
          title: String(chat.title || `Chat ${chat.id}`),
          date: toSidebarDate(chat.updatedAt),
        }))

      setChats(nextChats)
      if (!activeChatID && !isCreatingNewChat && nextChats.length > 0) {
        setActiveChatID(nextChats[0].id)
      }
    } catch {
      // keep UI usable when backend is unavailable
    }
  }, [activeChatID, isCreatingNewChat])

  useEffect(() => {
    void loadChats()
  }, [loadChats])

  const handleNewChat = () => {
    setIsCreatingNewChat(true)
    setActiveChatID(undefined)
  }

  const handleChatCreated = (chatId: string) => {
    setIsCreatingNewChat(false)
    setActiveChatID(chatId)
    void loadChats()
  }

  const sidebarChats = useMemo(() => chats, [chats])

  const handleSelectChat = (chatId: string) => {
    setIsCreatingNewChat(false)
    setActiveChatID(chatId)
  }

  return (
    <div className="flex h-screen w-full bg-white">
      {/* Sidebar - 240px */}
      <div className="w-60 overflow-hidden border-r border-gray-200">
        <ChatSidebar
          chats={sidebarChats}
          onNewChat={handleNewChat}
          onSelectChat={handleSelectChat}
          activeId={activeChatID}
        />
      </div>

      {/* Main Content */}
      <div className="flex flex-1 flex-col overflow-hidden">
        <ChatMain
          userName="Toby"
          showGreeting={true}
          backendBaseURL={backendBaseURL}
          activeChatID={activeChatID}
          onChatCreated={handleChatCreated}
        />
      </div>
    </div>
  )
}
