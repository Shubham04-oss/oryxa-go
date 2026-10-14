// What the organization agent made of a team's posts (I7): one team's own
// synthesis, beside its room, and the follow-ups it proposed, grouped by the
// teamspace each came from. A team's synthesis is only for its members and
// the organization's owners and admins: anyone else (403) sees no panel.
import { useQuery } from "@tanstack/react-query"
import { timeAgo } from "./agents-format"
import { ApiError } from "@/lib/api/client"
import type {
  OrganizationContext,
  OrganizationFollowUp,
} from "@/lib/agents/organization-agent"
import {
  organizationFollowUpsQueryOptions,
  teamspaceSynthesisQueryOptions,
} from "@/lib/agents/queries"
import { teamspacesQueryOptions } from "@/lib/teamspaces/queries"
import "./team-synthesis.css"

function isHidden(error: unknown): boolean {
  return (
    error instanceof ApiError && [401, 403, 404, 503].includes(error.status)
  )
}

function isEmpty(synthesis: OrganizationContext): boolean {
  return (
    synthesis.revision === 0 ||
    (!synthesis.summary &&
      synthesis.decisions.length === 0 &&
      synthesis.risks.length === 0 &&
      synthesis.open_questions.length === 0)
  )
}

function Points({ title, items }: { title: string; items: string[] }) {
  if (items.length === 0) return null
  return (
    <div className="workspace-synthesis-points">
      <h4>{title}</h4>
      <ul>
        {items.map((item, index) => (
          <li key={`${index}-${item}`}>{item}</li>
        ))}
      </ul>
    </div>
  )
}

function FollowUpItems({ followUps }: { followUps: OrganizationFollowUp[] }) {
  return (
    <ul className="workspace-follow-ups">
      {followUps.map((followUp) => (
        <li key={followUp.id}>
          <strong>{followUp.task}</strong>
          {followUp.rationale && <span>{followUp.rationale}</span>}
          <small>Proposed {timeAgo(followUp.created_at)}</small>
        </li>
      ))}
    </ul>
  )
}

/** A team's synthesis and its follow-ups, on its teamspace's Overview. */
export function TeamSynthesis({
  organizationId,
  teamspaceId,
}: {
  organizationId: string
  teamspaceId: string
}) {
  const synthesis = useQuery(
    teamspaceSynthesisQueryOptions(organizationId, teamspaceId)
  )
  const followUps = useQuery({
    ...organizationFollowUpsQueryOptions(organizationId),
    enabled: synthesis.isSuccess,
  })
  if (synthesis.isPending || (synthesis.isError && isHidden(synthesis.error))) {
    return null
  }
  if (synthesis.isError) {
    return (
      <div className="workspace-work-card">
        <h3>Team summary</h3>
        <p className="workspace-work-empty">Couldn't load the team summary.</p>
      </div>
    )
  }
  const data = synthesis.data
  const ours = (followUps.data ?? []).filter(
    (followUp) => followUp.teamspace_id === teamspaceId
  )
  return (
    <div
      className="workspace-work-card workspace-synthesis"
      aria-label="Team summary"
      role="region"
    >
      <h3>Team summary</h3>
      {isEmpty(data) ? (
        <p className="workspace-work-empty">
          Nothing summarized yet. The organization agent writes this as the team
          posts.
        </p>
      ) : (
        <>
          {data.summary && (
            <p className="workspace-synthesis-summary">{data.summary}</p>
          )}
          <Points title="Decisions" items={data.decisions} />
          <Points title="Risks" items={data.risks} />
          <Points title="Open questions" items={data.open_questions} />
          {data.updated_at && (
            <small className="workspace-synthesis-time">
              Updated {timeAgo(data.updated_at)}
            </small>
          )}
        </>
      )}
      {ours.length > 0 && (
        <div className="workspace-synthesis-points">
          <h4>Proposed follow-ups</h4>
          <FollowUpItems followUps={ours} />
        </div>
      )}
    </div>
  )
}

/** Every follow-up the caller may read, grouped by the teamspace it came from. */
export function groupFollowUps(
  followUps: OrganizationFollowUp[]
): { teamspaceId: string; followUps: OrganizationFollowUp[] }[] {
  const groups = new Map<string, OrganizationFollowUp[]>()
  for (const followUp of followUps) {
    const group = groups.get(followUp.teamspace_id)
    if (group) group.push(followUp)
    else groups.set(followUp.teamspace_id, [followUp])
  }
  return [...groups].map(([teamspaceId, items]) => ({
    teamspaceId,
    followUps: items,
  }))
}

/** On Agent overview: follow-ups from the caller's teamspaces, by teamspace. */
export function FollowUpsByTeamspace({
  organizationId,
  onOpenTeamspace,
}: {
  organizationId: string
  onOpenTeamspace?: (teamspaceId: string) => void
}) {
  const followUps = useQuery(organizationFollowUpsQueryOptions(organizationId))
  const teamspaces = useQuery(teamspacesQueryOptions(organizationId))
  if (!followUps.data || followUps.data.length === 0) return null
  const names = new Map(
    (teamspaces.data ?? []).map((space) => [space.id, space.name])
  )
  return (
    <section aria-labelledby="agents-overview-follow-ups">
      <p className="workspace-eyebrow" id="agents-overview-follow-ups">
        Follow-ups from your teamspaces
      </p>
      <div className="workspace-follow-up-groups">
        {groupFollowUps(followUps.data).map((group) => {
          const name = names.get(group.teamspaceId) ?? "A teamspace"
          return (
            <section
              key={group.teamspaceId}
              className="workspace-work-card"
              aria-label={`Follow-ups from ${name}`}
            >
              <h3>
                {onOpenTeamspace && names.has(group.teamspaceId) ? (
                  <button
                    type="button"
                    onClick={() => onOpenTeamspace(group.teamspaceId)}
                  >
                    {name}
                  </button>
                ) : (
                  name
                )}
                <span>{group.followUps.length}</span>
              </h3>
              <FollowUpItems followUps={group.followUps} />
            </section>
          )
        })}
      </div>
    </section>
  )
}
