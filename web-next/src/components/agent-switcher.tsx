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
 * Quick switch between the member's agents. Today only the primary agent is
 * real; the rest of the row is quiet, disabled placeholders so the layout
 * reads correctly once more agents exist. Placeholders never look like real
 * agents (no avatar, initials, or name).
 */
export function AgentSwitcher({
  agents,
  selectedId,
  onSelect,
  placeholders,
}: {
  agents: AgentSwitcherEntry[]
  selectedId: string
  onSelect: (id: string) => void
  placeholders: number
}) {
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
      {Array.from({ length: Math.max(0, placeholders) }, (_, index) => (
        <button
          className="workspace-switcher-item workspace-agent-slot-empty"
          type="button"
          key={`agent-slot-${index}`}
          disabled
          aria-label="Empty agent slot"
          title="More agents will appear here."
        />
      ))}
    </div>
  )
}
