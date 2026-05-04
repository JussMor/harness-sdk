"use client"

import type { ReactNode } from "react"
import { createContext, useContext } from "react"

interface A2UIContextType {
  initialized: boolean
}

const A2UIContext = createContext<A2UIContextType | undefined>(undefined)

export function A2UIProvider({ children }: { children: ReactNode }) {
  return (
    <A2UIContext.Provider value={{ initialized: true }}>
      {children}
    </A2UIContext.Provider>
  )
}

export function useA2UI() {
  const context = useContext(A2UIContext)
  if (!context) {
    throw new Error("useA2UI must be used within A2UIProvider")
  }
  return context
}
