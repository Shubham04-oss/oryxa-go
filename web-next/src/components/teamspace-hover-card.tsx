// A teamspace at a glance, on hover (founder's redesign, 15 Oct 2026, the
// hover card on "Pop-ups · toasts, hover card"): the harbour, its name and
// description, who's in it and its agents, and a parley if one is live. The
// trigger is the sidebar's own item; the card is the library's hover card.
import type { ReactElement } from "react"
import { useQuery } from "@tanstack/react-query"
import { IdentityAvatar, TeamspaceAvatar } from "./workspace-avatar"
import {
  lastMessageLine,
  personIdentity,
  timeAgo,
  unreadCount,
  unreadLabel,
  unreadWords,
} from "./teamspace-format"
import { Badge } from "@/components/ui/badge"
import {
  HoverCard,
  HoverCardContent,
  HoverCardTrigger,
} from "@/components/ui/hover-card"
import { callKey, readCall } from "@/lib/call/client"
import type { Teamspace } from "@/lib/teamspaces/client"
import "./popup.css"

/** How many faces the card shows. */
const FACES = 4

export function TeamspaceHoverCard({
  space,
  viewerId,
  children,
}: {
  space: Teamspace
  /** The member looking, so their own last message reads "You". */
  viewerId?: string
  /** The item that opens the card on hover or focus: the sidebar's own. */
  children: ReactElement
}) {
  return (
    <HoverCard>
      <HoverCardTrigger render={children} />
      <HoverCardContent side="right" align="start" className="popup-card">
        <TeamspaceCard space={space} viewerId={viewerId} />
      </HoverCardContent>
    </HoverCard>
  )
}

/** The card itself: mounted only while it shows, so it reads only then. */
export function TeamspaceCard({
  space,
  viewerId,
}: {
  space: Teamspace
  /** The member looking, so their own last message reads "You". */
  viewerId?: string
}) {
  const room = { kind: "teamspace", id: space.id } as const
  // Whether a parley runs: the room's own call view, read when the card
  // opens. A room the edge refuses or doesn't know shows none.
  const call = useQuery({
    queryKey: callKey(space.organization_id, room),
    queryFn: ({ signal }) => readCall(space.organization_id, room, signal),
    staleTime: 20_000,
    retry: false,
  })
  const people = space.whisperers
  // The teamspace's own agent and the specialists its members brought.
  const agents = 1 + space.contributions.length
  // The room's newest message and what the member hasn't read, when the
  // list carries them (#389).
  const last = space.last_message
  const unread = unreadCount(space)
  return (
    <div className="popup-card-inner">
      <div className="popup-scene" data-scene="harbour" aria-hidden="true" />
      <div className="popup-body">
        <span className="popup-icon" aria-hidden="true">
          <TeamspaceAvatar name={space.name} />
        </span>
        <h3>{space.name}</h3>
        {space.description && <p>{space.description}</p>}
        <div className="popup-card-meta">
          <span>
            {people.length} {people.length === 1 ? "person" : "people"} ·{" "}
            {agents} {agents === 1 ? "agent" : "agents"}
          </span>
          {people.length > 0 && (
            <span className="popup-card-faces" aria-hidden="true">
              {people.slice(0, FACES).map((person) => (
                <IdentityAvatar
                  key={person.user_id}
                  identity={personIdentity(
                    person.user_id,
                    person.user_display_name ?? person.agent_name
                  )}
                />
              ))}
            </span>
          )}
        </div>
        {(last || unread > 0) && (
          <div className="popup-card-last">
            {unread > 0 && (
              <Badge className="popup-card-unread">
                <span aria-hidden="true">{unreadLabel(unread)}</span>
                <span className="sr-only">{unreadWords(unread)}</span>
              </Badge>
            )}
            {last && (
              <>
                <span className="popup-card-last-line">
                  {lastMessageLine(last, viewerId)}
                </span>
                <time dateTime={last.created_at}>
                  {timeAgo(last.created_at)}
                </time>
              </>
            )}
          </div>
        )}
        {call.data && (
          <span className="popup-card-live">
            Parley live · {call.data.participants.length}{" "}
            {call.data.participants.length === 1 ? "person" : "people"} in
          </span>
        )}
      </div>
    </div>
  )
}
