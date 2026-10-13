import { useCallback, useState } from "react"

/** What Agents mode opens as focused work (DESIGN.md §3). */
export type AgentsFocusTarget =
  { kind: "specialist"; agentId: string } | { kind: "team"; teamId: string }

/**
 * Agents mode's share of the workspace shell: the specialist or multi-agent
 * team open as focused work, if any. The shell hides the left sidebar while
 * one is open.
 */
export function useAgentsMode() {
  const [focus, setFocus] = useState<AgentsFocusTarget | null>(null)
  const open = useCallback((target: AgentsFocusTarget) => setFocus(target), [])
  const close = useCallback(() => setFocus(null), [])
  return { focus, open, close }
}
