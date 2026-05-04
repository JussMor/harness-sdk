"use client"

import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { Compass, History, Library, Plus, UploadCloud } from "lucide-react"

export interface SidebarChat {
  id: string
  title: string
  date: "today" | "yesterday" | "7days" | "older"
}

export interface ChatSidebarProps {
  chats: Array<SidebarChat>
  activeId?: string
  onNewChat: () => void
  onSelectChat?: (chatId: string) => void
}

export function ChatSidebar({
  chats,
  activeId,
  onNewChat,
  onSelectChat,
}: ChatSidebarProps) {
  const groupedChats = {
    today: chats.filter((c) => c.date === "today"),
    yesterday: chats.filter((c) => c.date === "yesterday"),
    "7days": chats.filter((c) => c.date === "7days"),
    older: chats.filter((c) => c.date === "older"),
  }

  const renderGroup = (title: string, items: Array<SidebarChat>) => {
    if (items.length === 0) return null
    return (
      <div key={title} className="space-y-1">
        <p className="px-3 py-2 text-xs font-semibold text-gray-500 uppercase">
          {title}
        </p>
        {items.map((chat) => (
          <button
            key={chat.id}
            onClick={() => onSelectChat?.(chat.id)}
            className={cn(
              "block w-full truncate rounded-lg px-3 py-2 text-left text-sm hover:bg-gray-100",
              activeId === chat.id && "bg-blue-100 text-blue-900"
            )}
          >
            {chat.title}
          </button>
        ))}
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col bg-white">
      {/* Header */}
      <div className="border-b p-4">
        <Button
          onClick={onNewChat}
          className="w-full gap-2 bg-black text-white hover:bg-gray-800"
        >
          <Plus className="h-4 w-4" />
          New Chat
        </Button>
      </div>

      {/* Search */}
      <div className="border-b p-3">
        <input
          type="text"
          placeholder="Search chats..."
          className="w-full rounded-lg border border-gray-200 bg-gray-50 px-3 py-2 text-sm placeholder-gray-400 focus:border-blue-500 focus:ring-1 focus:ring-blue-500 focus:outline-none"
        />
      </div>

      {/* Chats List */}
      <div className="flex-1 space-y-4 overflow-y-auto px-3 py-4">
        {renderGroup("Today", groupedChats.today)}
        {renderGroup("Yesterday", groupedChats.yesterday)}
        {renderGroup("7 Days Ago", groupedChats["7days"])}
        {renderGroup("Older", groupedChats.older)}
      </div>

      {/* Footer */}
      <div className="space-y-2 border-t p-3">
        <button className="flex w-full items-center gap-3 rounded-lg px-3 py-2 text-sm text-gray-700 hover:bg-gray-100">
          <Compass className="h-4 w-4" />
          Explore
        </button>
        <button className="flex w-full items-center gap-3 rounded-lg px-3 py-2 text-sm text-gray-700 hover:bg-gray-100">
          <Library className="h-4 w-4" />
          Library
        </button>
        <button className="flex w-full items-center gap-3 rounded-lg px-3 py-2 text-sm text-gray-700 hover:bg-gray-100">
          <History className="h-4 w-4" />
          History
        </button>
        <button className="flex w-full items-center gap-3 rounded-lg px-3 py-2 text-sm text-gray-700 hover:bg-gray-100">
          <UploadCloud className="h-4 w-4" />
          Upgrade
        </button>
      </div>
    </div>
  )
}
