import { IdentityAvatar } from "./workspace-avatar"
import { fullTime, messageTime, personIdentity } from "./teamspace-format"
import type { AvatarIdentity } from "./workspace-avatar"
import type { ParleyNotes, RoomMessage, RoomSeat } from "@/lib/rooms/client"
import { Badge } from "@/components/ui/badge"

/** "35 min", "under a minute": how long the parley ran, as Parley ended says it. */
export function notesLength(
  durationMs: number | undefined
): string | undefined {
  if (durationMs === undefined || durationMs < 0) return undefined
  const minutes = Math.round(durationMs / 60_000)
  return minutes < 1 ? "under a minute" : `${minutes} min`
}

/**
 * Who owes an action, as the room can seat them: the member ("You"), a
 * person with a seat, or nobody. An owner the room can't seat is shown as
 * nobody (#392: the edge's word, not checked against the team).
 */
export function actionOwner(
  ownerUserId: string | undefined,
  seats: readonly RoomSeat[],
  viewerId: string | undefined
): { userId: string; name: string } | undefined {
  if (!ownerUserId) return undefined
  const seat = seats.find(
    (item) => item.kind === "person" && item.user_id === ownerUserId
  )
  if (!seat) return undefined
  return {
    userId: ownerUserId,
    name: ownerUserId === viewerId ? "You" : seat.display_name,
  }
}

/**
 * A parley's notes in its teamspace's room (TeamspaceHome board,
 * "th-notes"): "Parley notes · 35 min · posted by Krude", then each decision
 * tagged Decision and each action with who owes it, or Unassigned.
 */
export function ParleyNotesCard({
  message,
  notes,
  poster,
  posterName,
  seats,
  viewerId,
}: {
  message: RoomMessage
  notes: ParleyNotes
  poster: AvatarIdentity
  posterName: string
  seats: readonly RoomSeat[]
  viewerId?: string
}) {
  const head = [
    "Parley notes",
    notesLength(notes.duration_ms),
    `posted by ${posterName}`,
  ]
    .filter(Boolean)
    .join(" · ")
  const empty = notes.decisions.length === 0 && notes.actions.length === 0
  return (
    <article
      className="workspace-message workspace-parley-notes"
      data-author={message.author_kind}
      aria-label={`Parley notes, posted by ${posterName}`}
    >
      <span className="workspace-parley-notes-gutter" aria-hidden="true" />
      <div className="workspace-message-body">
        <div className="workspace-parley-notes-card">
          <div className="workspace-parley-notes-head">
            <IdentityAvatar identity={poster} />
            <span className="workspace-parley-notes-title">{head}</span>
            <time
              dateTime={message.created_at}
              title={fullTime(message.created_at)}
            >
              {messageTime(message.created_at)}
            </time>
          </div>
          {empty ? (
            <p className="workspace-parley-notes-empty">
              Nothing was decided or assigned.
            </p>
          ) : (
            <ul
              className="workspace-parley-notes-lines"
              aria-label="Decisions and actions"
            >
              {notes.decisions.map((decision, index) => (
                <li key={`decision-${index}`}>
                  <span className="workspace-parley-notes-text">
                    {decision}
                  </span>
                  <Badge
                    variant="secondary"
                    className="workspace-parley-notes-decision"
                  >
                    Decision
                  </Badge>
                </li>
              ))}
              {notes.actions.map((action, index) => {
                const owner = actionOwner(action.owner_user_id, seats, viewerId)
                return (
                  <li key={`action-${index}`}>
                    {owner && (
                      // The badge names the owner; the face is decoration.
                      <span
                        className="workspace-parley-notes-face"
                        aria-hidden="true"
                      >
                        <IdentityAvatar
                          identity={personIdentity(owner.userId, owner.name)}
                        />
                      </span>
                    )}
                    <span className="workspace-parley-notes-text">
                      {action.text}
                    </span>
                    <Badge
                      variant="outline"
                      className="workspace-parley-notes-owner"
                      data-unassigned={owner ? undefined : "true"}
                    >
                      {owner?.name ?? "Unassigned"}
                    </Badge>
                  </li>
                )
              })}
            </ul>
          )}
        </div>
      </div>
    </article>
  )
}
