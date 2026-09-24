// A teamspace's teammates in the agent panel's Agents tab (DESIGN.md §2,
// the panel is location-aware): on a teamspace page, everyone else with a
// seat there, each with their agents and how those take a request, and each
// opening the side conversation with them (REALTIME.md, "Side
// conversations"), on one of their agents when that is what was picked.
import { useEffect, useId, useRef } from "react"
import { ChevronRight } from "lucide-react"
import { PersonAvatar, UnreadCount } from "./direct-message"
import { teammatesOf } from "./side-conversation-format"
import type { Teammate } from "./side-conversation-format"
import { agentIdentity } from "./teamspace-format"
import { IdentityAvatar } from "./workspace-avatar"
import type { Teamspace } from "@/lib/teamspaces/client"

export function TeamspaceTeammates({
  space,
  failed,
  viewerId,
  nameOf,
  unreadOf,
  onOpen,
  focusPeerId,
  onFocused,
}: {
  /** The teamspace the page shows, once read. */
  space?: Teamspace
  /** It could not be read. */
  failed: boolean
  viewerId: string
  /** A person's name as the organization's members list has it, when it has it. */
  nameOf: (userId: string) => string | undefined
  /** What the member has not read of their side conversation with someone here. */
  unreadOf: (peerId: string) => number
  /** Opens the side conversation with a teammate, asking one of their agents when one was picked. */
  onOpen: (teammate: Teammate, agentId?: string) => void
  /** Back from a conversation: the teammate whose row takes focus. */
  focusPeerId: string | null
  onFocused: () => void
}) {
  const headingId = useId()
  const sectionRef = useRef<HTMLElement>(null)
  const teammates = space?.is_member ? teammatesOf(space, viewerId, nameOf) : []

  // Back from a conversation, focus returns to the teammate it was with.
  useEffect(() => {
    if (!focusPeerId) return
    const row = Array.from(
      sectionRef.current?.querySelectorAll<HTMLButtonElement>(
        "[data-side-peer]"
      ) ?? []
    ).find((button) => button.dataset.sidePeer === focusPeerId)
    ;(row ?? sectionRef.current)?.focus()
    onFocused()
  }, [focusPeerId, onFocused])

  return (
    <section
      className="workspace-side-teammates"
      aria-labelledby={headingId}
      tabIndex={-1}
      ref={sectionRef}
    >
      <h2 className="workspace-side-teammates-heading" id={headingId}>
        In this teamspace
      </h2>
      {!space ? (
        <p
          className="workspace-side-teammates-note"
          role={failed ? "alert" : "status"}
        >
          {failed
            ? "Couldn't load this teamspace's teammates."
            : "Loading teammates…"}
        </p>
      ) : !space.is_member ? (
        <p className="workspace-side-teammates-note">
          Only members of the {space.team_name} team can talk with teammates
          here.
        </p>
      ) : teammates.length === 0 ? (
        <p className="workspace-side-teammates-note">
          No teammates here yet. They show here once they have an active agent.
        </p>
      ) : (
        <>
          {space.status === "disabled" && (
            <p className="workspace-side-teammates-note">
              {space.name} is paused: its side conversations are read-only.
            </p>
          )}
          <ul className="workspace-side-teammate-list">
            {teammates.map((teammate) => (
              <TeammateRow
                key={teammate.userId}
                teammate={teammate}
                unread={unreadOf(teammate.userId)}
                onOpen={onOpen}
              />
            ))}
          </ul>
        </>
      )}
    </section>
  )
}

function TeammateRow({
  teammate,
  unread,
  onOpen,
}: {
  teammate: Teammate
  unread: number
  onOpen: (teammate: Teammate, agentId?: string) => void
}) {
  return (
    <li
      className="workspace-side-teammate"
      data-unread={unread > 0 || undefined}
    >
      <button
        className="workspace-side-teammate-main"
        type="button"
        data-side-peer={teammate.userId}
        aria-label={`Talk with ${teammate.name}${unread > 0 ? `, ${unread} unread` : ""}`}
        onClick={() => onOpen(teammate)}
      >
        <PersonAvatar id={teammate.userId} name={teammate.name} />
        <span className="workspace-side-teammate-name">{teammate.name}</span>
        {unread > 0 && <UnreadCount count={unread} />}
        <ChevronRight size={15} strokeWidth={1.7} aria-hidden="true" />
      </button>
      <ul
        className="workspace-side-teammate-agents"
        aria-label={`${teammate.name}'s agents`}
      >
        {teammate.agents.map((agent) => {
          const mode = agent.automatic ? "automatic" : "asks first"
          const noun = agent.kind === "primary" ? "agent" : "specialist"
          return (
            <li key={agent.id}>
              <button
                className="workspace-side-teammate-agent"
                type="button"
                aria-label={`Ask ${agent.name}, ${teammate.name}'s ${noun} (${mode})`}
                title={agent.role ? `${agent.name}: ${agent.role}` : undefined}
                onClick={() => onOpen(teammate, agent.id)}
              >
                <IdentityAvatar
                  identity={agentIdentity(agent.id, agent.name)}
                />
                <span>{agent.name}</span>
                <small>{mode}</small>
              </button>
            </li>
          )
        })}
      </ul>
    </li>
  )
}
