import { IdentityAvatar } from "./workspace-avatar"
import type { AvatarIdentity } from "./workspace-avatar"
import { cn } from "@/lib/utils"

export type AgentSwitcherEntry = {
  id: string
  name?: string
  avatarSeed: string
}

/** Accessible name for an agent whose real name has not loaded yet. */
export const FALLBACK_AGENT_NAME = "Primary agent"

function switcherIdentity(entry: AgentSwitcherEntry): AvatarIdentity {
  const label = entry.name ?? FALLBACK_AGENT_NAME
  return {
    id: entry.avatarSeed,
    name: label,
    initials: label
      .split(/\s+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((part) => part.charAt(0).toUpperCase())
      .join(""),
    kind: "agent",
  }
}

/**
 * Quick switch between the member's agents: the one in use is the named
 * pill, the others avatar-only. With a single agent there is nothing to
 * switch to, so the row is not shown at all.
 */
export function AgentSwitcher({
  agents,
  selectedId,
  onSelect,
}: {
  agents: AgentSwitcherEntry[]
  selectedId: string
  onSelect: (id: string) => void
}) {
  if (agents.length < 2) return null
  return (
    <div
      className="workspace-agent-identity"
      role="group"
      aria-label="Switch agent"
    >
      {agents.map((entry) => {
        const selected = entry.id === selectedId
        const label = entry.name ?? FALLBACK_AGENT_NAME
        return (
          <button
            className={cn("workspace-switcher-item", selected && "is-selected")}
            type="button"
            key={entry.id}
            aria-label={label}
            aria-pressed={selected}
            title={selected ? undefined : label}
            onClick={() => onSelect(entry.id)}
          >
            <IdentityAvatar identity={switcherIdentity(entry)} />
            {selected && entry.name && <span>{entry.name}</span>}
          </button>
        )
      })}
    </div>
  )
}
