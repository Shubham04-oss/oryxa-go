// Who someone is, beside your DM with them (Messages, DESIGN.md §3): their
// name and role, whether the page knows they are online, a parley with them,
// their agent and the teamspaces you share. Only what Oryxa can say is shown:
// no local time, profile or shared files until the API has them (#322).
import { useId } from "react"
import { useHasCalls } from "./call-provider"
import { ParleyButton } from "./call-surfaces"
import { PersonAvatar } from "./direct-message"
import { roleLabel } from "./direct-message-format"
import type { DmPeer, DmPerson, DmPersonAgent } from "./direct-message-people"
import { usePeerOnline } from "./direct-message-presence"
import { agentIdentity } from "./teamspace-format"
import { IdentityAvatar, TeamspaceAvatar } from "./workspace-avatar"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader } from "@/components/ui/card"
import { Separator } from "@/components/ui/separator"
import { directRoom } from "@/lib/live/rooms"

export function PersonCard({
  organizationId,
  viewerId,
  peer,
  person,
  onAskAgent,
}: {
  organizationId: string
  viewerId: string
  /** The person as the DM names them, even once they have left. */
  peer: DmPeer
  /** Their place in the organization; none once they have left. */
  person?: DmPerson
  /** Ask one of their agents; without it there is no Ask. */
  onAskAgent?: (person: DmPerson, agent: DmPersonAgent) => void
}) {
  const online = usePeerOnline(peer.id)
  const hasCalls = useHasCalls()
  const room = directRoom(viewerId, peer.id)
  const titleId = useId()

  return (
    <Card className="person-card" size="sm" aria-labelledby={titleId}>
      <CardHeader className="person-card-head">
        <PersonAvatar
          id={peer.id}
          name={peer.name}
          online={online}
          className="person-card-avatar"
        />
        <div className="person-card-who">
          <h2 id={titleId}>{peer.name}</h2>
          <p>
            {person ? roleLabel(person.role) : "No longer in this organization"}
          </p>
          {online && <p className="person-card-online">Online</p>}
        </div>
        {person && hasCalls && room && (
          <div className="person-card-actions">
            <ParleyButton
              organizationId={organizationId}
              room={room}
              roomName={peer.name}
              member={viewerId}
            />
          </div>
        )}
      </CardHeader>
      {person && (
        <CardContent className="person-card-body">
          {person.agents.length > 0 && (
            <section aria-label={`${peer.short}'s agent`}>
              <h3>
                {person.agents.length === 1
                  ? `${peer.short}'s agent`
                  : `${peer.short}'s agents`}
              </h3>
              <ul className="person-card-agents">
                {person.agents.map((agent) => (
                  <li key={agent.id}>
                    <IdentityAvatar
                      identity={agentIdentity(agent.id, agent.name)}
                    />
                    <span>
                      <strong>{agent.name}</strong>
                      <small>
                        Answers for {peer.short} in a teamspace you share
                      </small>
                    </span>
                    {onAskAgent && (
                      <Button
                        variant="outline"
                        size="sm"
                        className="person-card-ask"
                        onClick={() => onAskAgent(person, agent)}
                      >
                        Ask {agent.name}
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            </section>
          )}
          {person.agents.length > 0 && <Separator />}
          <section aria-label="Shared teamspaces">
            <h3>Shared teamspaces</h3>
            {person.spaces.length > 0 ? (
              <ul className="person-card-spaces">
                {person.spaces.map((space) => (
                  <li key={space}>
                    <Badge variant="secondary" className="person-card-space">
                      <span aria-hidden="true">
                        <TeamspaceAvatar name={space} />
                      </span>
                      {space}
                    </Badge>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="person-card-quiet">
                You don't share a teamspace yet.
              </p>
            )}
          </section>
        </CardContent>
      )}
    </Card>
  )
}
