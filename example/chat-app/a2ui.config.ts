// a2ui React Configuration
// Type definitions for a2ui config with @a2ui/react and @a2ui/web_core

export interface A2UIThemeColors {
  primary: string
  secondary: string
  accent: string
  background: string
  surface: string
  text: string
  textMuted: string
  border: string
  error: string
  success: string
  warning: string
  info: string
}

export interface A2UIConfig {
  version: string
  project: {
    name: string
    description: string
  }
  theme: {
    name: string
    colors: A2UIThemeColors
    typography: Record<string, unknown>
    spacing: Record<string, unknown>
    borderRadius: Record<string, string>
    shadows: Record<string, string>
  }
  components: Record<string, unknown>
  accessibility: Record<string, unknown>
  development: Record<string, unknown>
  paths: Record<string, string>
}

export const config: A2UIConfig = {
  version: "1.0.0",
  project: {
    name: "chat-app",
    description: "AI Chat Application",
  },
  theme: {
    name: "default",
    colors: {
      primary: "#3b82f6",
      secondary: "#8b5cf6",
      accent: "#ec4899",
      background: "#ffffff",
      surface: "#f3f4f6",
      text: "#111827",
      textMuted: "#6b7280",
      border: "#e5e7eb",
      error: "#ef4444",
      success: "#10b981",
      warning: "#f59e0b",
      info: "#06b6d4",
    },
    typography: {
      fontFamily: "inter, system-ui, sans-serif",
      fontSize: {
        xs: "0.75rem",
        sm: "0.875rem",
        base: "1rem",
        lg: "1.125rem",
        xl: "1.25rem",
        "2xl": "1.5rem",
      },
      lineHeight: {
        tight: "1.25",
        normal: "1.5",
        relaxed: "1.75",
      },
    },
    spacing: {
      unit: "4px",
      scale: [0, 1, 2, 4, 8, 12, 16, 24, 32, 48, 64],
    },
    borderRadius: {
      none: "0",
      xs: "2px",
      sm: "4px",
      md: "6px",
      lg: "8px",
      xl: "12px",
      full: "9999px",
    },
    shadows: {
      sm: "0 1px 2px 0 rgb(0 0 0 / 0.05)",
      md: "0 4px 6px -1px rgb(0 0 0 / 0.1)",
      lg: "0 10px 15px -3px rgb(0 0 0 / 0.1)",
      xl: "0 20px 25px -5px rgb(0 0 0 / 0.1)",
    },
  },
  components: {
    button: {
      variants: ["default", "outline", "ghost", "destructive", "secondary"],
      sizes: ["xs", "sm", "md", "lg", "icon", "icon-sm", "icon-lg"],
      transitions: true,
    },
    card: {
      shadow: true,
      rounded: true,
      padding: "md",
    },
    input: {
      bordered: true,
      rounded: true,
      validation: true,
    },
    textarea: {
      resizable: true,
      bordered: true,
      rounded: true,
    },
    dialog: {
      animated: true,
      closeButton: true,
      backdrop: true,
    },
    tooltip: {
      delay: 200,
      position: "auto",
    },
  },
  accessibility: {
    contrastRatio: "WCAG_AA",
    focusStyle: "ring",
    reduceMotion: false,
    ariaLabels: true,
  },
  development: {
    debug: process.env.DEBUG === "true",
    logComponents: false,
    validateProps: true,
    strictMode: true,
  },
  paths: {
    components: "@/components",
    lib: "@/lib",
    hooks: "@/hooks",
    utils: "@/lib/utils",
  },
}

export default config
