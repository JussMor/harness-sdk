import { ChatMain } from "@/components/chat-main"
import type { SidebarChat } from "@/components/chat-sidebar"
import { ChatSidebar } from "@/components/chat-sidebar"
import { ChatAPI } from "@/features/chat/api"
import { createFileRoute } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo, useState } from "react"

export const Route = createFileRoute("/")({
  component: IndexRoute,
})

const backendBaseURL =
  import.meta.env.VITE_BACKEND_URL?.trim() || "http://localhost:9090"

function IndexRoute() {
  const api = useMemo(() => new ChatAPI(backendBaseURL), [])
  const [chats, setChats] = useState<Array<SidebarChat>>([])
  const [activeChatID, setActiveChatID] = useState<string | undefined>()
  const [isCreatingNewChat, setIsCreatingNewChat] = useState(true)

  const loadChats = useCallback(async () => {
    try {
      const payload = await api.listChats()
      const nextChats: Array<SidebarChat> = payload
        .map((chat) => ({
          id: String(chat.id),
          title: String(chat.title || `Chat ${chat.id}`),
          updatedAt: String(chat.updatedAt || chat.createdAt || ""),
        }))
        .sort((left, right) => right.updatedAt.localeCompare(left.updatedAt))

      setChats(nextChats)
      if (!activeChatID && !isCreatingNewChat && nextChats.length > 0) {
        setActiveChatID(nextChats[0].id)
      }
    } catch {
      // keep shell visible when backend is down
    }
  }, [activeChatID, api, isCreatingNewChat])

  useEffect(() => {
    void loadChats()
  }, [loadChats])

  const handleNewChat = () => {
    setIsCreatingNewChat(true)
    setActiveChatID(undefined)
  }

  const handleChatCreated = useCallback(
    (chatId: string) => {
      setIsCreatingNewChat(false)
      setActiveChatID(chatId)
      void loadChats()
    },
    [loadChats]
  )

  const handleSelectChat = useCallback((chatId: string) => {
    setIsCreatingNewChat(false)
    setActiveChatID(chatId)
  }, [])

  return (
    <div className="chat-app-shell">
      <aside className="chat-app-sidebar">
        <ChatSidebar
          chats={chats}
          onNewChat={handleNewChat}
          onSelectChat={handleSelectChat}
          activeId={activeChatID}
        />
      </aside>

      <main className="chat-app-main">
        <ChatMain
          userName="Juss"
          showGreeting={true}
          backendBaseURL={backendBaseURL}
          activeChatID={activeChatID}
          onChatCreated={handleChatCreated}
          onChatsChanged={loadChats}
        />
      </main>
    </div>
  )
}
