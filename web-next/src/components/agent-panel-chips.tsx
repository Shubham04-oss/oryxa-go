import type { ReactNode } from "react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { useCall, useHasCalls } from "./call-provider"
import { countLabel } from "./direct-message-format"
import { useRoomCall } from "@/lib/call/use-room-call"
import type { LiveRoom } from "@/lib/live/client"

type ChipsProps = {
  organizationId: string
  viewerId?: string
  /** The teamspace on the page, whose parley the member may join. */
  teamspace?: { id: string; name: string }
  /** How many requests wait on the member; undefined until read. */
  needs?: number
  onOpenRequests?: () => void
}

/**
 * The strip of chips over the agent's chat (DESIGN.md §2): the member's
 * parley, live in this teamspace or the one they are in, and how many
 * requests need them. A chip shows only what is real; with nothing to say
 * the strip is not drawn. A parley on My workspace needs the organisation's
 * live list, which the edge does not serve yet, so none shows there.
 */
export function AgentPanelChips(props: ChipsProps) {
  // Parleys exist only inside the workspace's CallProvider.
  return useHasCalls() && props.viewerId ? (
    <ChipsWithParley {...props} viewerId={props.viewerId} />
  ) : (
    <ChipStrip needs={needsButton(props)} />
  )
}

function ChipStrip({
  parley,
  needs,
}: {
  parley?: ReactNode
  needs: ReactNode
}) {
  if (!parley && !needs) return null
  return (
    <div
      className="workspace-agent-chips"
      role="group"
      aria-label="For you now"
    >
      {parley}
      {needs}
    </div>
  )
}

function needsButton({ needs, onOpenRequests }: ChipsProps): ReactNode {
  if (needs === undefined || needs === 0 || !onOpenRequests) return null
  return (
    <Button
      variant="outline"
      size="sm"
      className="workspace-agent-chip"
      onClick={onOpenRequests}
      aria-label={`Needs you: ${needs} ${needs === 1 ? "thing" : "things"}, open Requests`}
    >
      Needs you
      <Badge variant="outline" className="workspace-agent-chip-count">
        {countLabel(needs, 99)}
      </Badge>
    </Button>
  )
}

/**
 * With the member's parley: "In the parley · Open" while they are in one,
 * wherever they are; "Parley · Join" on a teamspace whose parley runs
 * without them.
 */
function ChipsWithParley(props: ChipsProps & { viewerId: string }) {
  const { organizationId, viewerId, teamspace } = props
  const { active, join, show } = useCall()
  const room: LiveRoom | undefined = teamspace
    ? { kind: "teamspace", id: teamspace.id }
    : undefined
  const { call } = useRoomCall(organizationId, room, {
    enabled: Boolean(teamspace),
  })
  let parley: ReactNode = null
  if (active?.phase === "in") {
    parley = (
      <Button
        variant="outline"
        size="sm"
        className="workspace-agent-chip"
        data-live="true"
        onClick={() => show("stage")}
      >
        <span className="call-live-dot" aria-hidden="true" />
        In the parley · Open
      </Button>
    )
  } else if (teamspace && room && call && active?.phase !== "joining") {
    parley = (
      <Button
        variant="outline"
        size="sm"
        className="workspace-agent-chip"
        data-live="true"
        aria-label={`Join the parley in ${teamspace.name || "this teamspace"}`}
        onClick={() =>
          void join({
            organizationId,
            room,
            roomName: teamspace.name,
            member: viewerId,
            mic: true,
            camera: false,
          })
        }
      >
        <span className="call-live-dot" aria-hidden="true" />
        Parley · Join
      </Button>
    )
  }
  return <ChipStrip parley={parley} needs={needsButton(props)} />
}
