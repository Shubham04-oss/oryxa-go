import { useEffect, useState } from "react"
import { Monitor, Moon, Sun } from "lucide-react"
import { useTheme } from "next-themes"

type Theme = "system" | "light" | "dark"

const order: Theme[] = ["system", "light", "dark"]
const labels: Record<Theme, string> = {
  system: "System",
  light: "Light",
  dark: "Dark",
}
const icons = { system: Monitor, light: Sun, dark: Moon } as const

function isTheme(value: string | undefined): value is Theme {
  return value === "system" || value === "light" || value === "dark"
}

/**
 * The profile menu's theme choice, as in oryxa-platform: each press moves on
 * from System to Light to Dark and back. next-themes keeps it in this browser.
 */
export function ThemeMenuItem() {
  const { theme, setTheme } = useTheme()
  // The server render cannot know the browser's choice: show System until mounted.
  const [mounted, setMounted] = useState(false)
  useEffect(() => setMounted(true), [])
  const current: Theme = mounted && isTheme(theme) ? theme : "system"
  const next = order[(order.indexOf(current) + 1) % order.length]
  const Icon = icons[current]

  return (
    <button
      type="button"
      role="menuitem"
      aria-label={`Theme: ${labels[current]}. Switch to ${labels[next]}`}
      onClick={() => setTheme(next)}
    >
      <Icon size={17} strokeWidth={1.7} /> Theme: {labels[current]}
    </button>
  )
}
