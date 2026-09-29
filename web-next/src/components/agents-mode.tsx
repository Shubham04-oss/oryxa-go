// Agents mode (DESIGN.md §2): Agent overview, Multi-agent teams and
// Specialists in the canvas, and a specialist or team as focused work. The
// workspace shell renders these two entry points and keeps which item is
// open (useAgentsMode); everything else here reads the agents service.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  AgentFace,
  AgentTeamEntry,
  AgentsNotice,
  SpecialistEntry,
  StatusPill,
  WorkflowRow,
} from "./agents-parts"
import { agentsErrorMessage, plural } from "./agents-format"
import { SpecialistFocus } from "./specialist-focus"
import { SpecialistsSection } from "./specialists-section"
import { AgentTeamFocus } from "./multi-agent-team-focus"
import { AgentTeamsSection } from "./multi-agent-teams-section"
import { PrimaryAgentFiles } from "./primary-agent-files"
import type { PrimaryAgentFileTarget } from "./primary-agent-files"
import type { AgentsFocusTarget } from "@/hooks/use-agents-mode"
import type { ScreenContext } from "@/lib/agents/screen-context"
import { afterUpdateAgent } from "@/lib/agents/invalidation"
import { updateAgent } from "@/lib/agents/managed-agents"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import {
  canDecideAgentRequest,
  canRespondToAgentRequest,
  isUnavailableHere,
  primaryAgent,
  specialistAgents,
} from "@/lib/agents/policy"
import {
  agentRequestsQueryOptions,
  agentTeamsQueryOptions,
  agentWorkflowsQueryOptions,
  agentsQueryOptions,
} from "@/lib/agents/queries"

export type AgentsSection = "agents" | "multi-agents" | "specialists"

const agentsSections: readonly string[] = [
  "agents",
  "multi-agents",
  "specialists",
] satisfies AgentsSection[]

export function isAgentsSection(section: string): section is AgentsSection {
  return agentsSections.includes(section)
}

/** Work items the overview shows before pointing to the sections. */
const OVERVIEW_WORK = 6
/** Workflows the overview lists. */
const OVERVIEW_WORKFLOWS = 5

export function AgentsCanvas({
  section,
  organizationId,
  viewerId,
  primaryThreadId,
  onOpenFile,
  onOpen,
  onNavigate,
}: {
  section: AgentsSection
  /** The signed-in member's organization; without one there is nothing to read. */
  organizationId?: string
  viewerId?: string
  primaryThreadId?: string | null
  onOpenFile?: (file: PrimaryAgentFileTarget) => void
  onOpen: (target: AgentsFocusTarget) => void
  onNavigate: (section: AgentsSection) => void
}) {
  if (!organizationId) {
    return (
      <div className="workspace-agents-page">
        <AgentsNotice title="Your agents appear here once you're signed in.">
          This preview isn't connected to a workspace, so it has no agents,
          teams or specialists to show.
        </AgentsNotice>
      </div>
    )
  }
  if (section === "specialists") {
    return (
      <SpecialistsSection organizationId={organizationId} onOpen={onOpen} />
    )
  }
  if (section === "multi-agents") {
    return <AgentTeamsSection organizationId={organizationId} onOpen={onOpen} />
  }
  return (
    <AgentsOverview
      organizationId={organizationId}
      viewerId={viewerId}
      primaryThreadId={primaryThreadId}
      onOpenFile={onOpenFile}
      onOpen={onOpen}
      onNavigate={onNavigate}
    />
  )
}

export function AgentsFocus({
  target,
  organizationId,
  onBack,
  onOpen,
  onChat,
  getScreenContext,
}: {
  target: AgentsFocusTarget
  organizationId: string
  onBack: () => void
  onOpen: (target: AgentsFocusTarget) => void
  /**
   * Opens the agent panel on the Agents tab with this specialist's chat: a
   * team's Members tab offers it for each active specialist. Without it no
   * Chat button is offered. A specialist's own view needs none: its
   * conversation column is that chat.
   */
  onChat?: (agentId: string) => void
  getScreenContext?: () => ScreenContext
}) {
  return target.kind === "specialist" ? (
    <SpecialistFocus
      key={target.agentId}
      organizationId={organizationId}
      agentId={target.agentId}
      onBack={onBack}
      onOpen={onOpen}
      getScreenContext={getScreenContext}
    />
  ) : (
    <AgentTeamFocus
      key={target.teamId}
      organizationId={organizationId}
      teamId={target.teamId}
      onBack={onBack}
      onOpen={onOpen}
      onChat={onChat}
    />
  )
}

/** Requests in the member's inbox that wait on them: to decide or to answer. */
function waitingOn(requests: readonly AgentRequest[], viewerId?: string) {
  return requests.filter((request) =>
    viewerId
      ? canDecideAgentRequest(request, viewerId) ||
        canRespondToAgentRequest(request, viewerId)
      : request.status === "pending" || request.status === "approved"
  )
}

function Stat({
  label,
  value,
  detail,
  query,
}: {
  label: string
  value?: number
  detail?: string
  query: { isPending: boolean; isError: boolean; error: unknown }
}) {
  const unavailable = isUnavailableHere(query.error)
  return (
    <div>
      <dt>{label}</dt>
      <dd aria-busy={query.isPending || undefined}>
        {value !== undefined ? value : query.isPending ? "…" : "–"}
      </dd>
      <small>
        {query.isError
          ? unavailable
            ? "Not set up on this server"
            : "Could not load"
          : detail}
      </small>
    </div>
  )
}

function AgentsOverview({
  organizationId,
  viewerId,
  primaryThreadId,
  onOpenFile,
  onOpen,
  onNavigate,
}: {
  organizationId: string
  viewerId?: string
  primaryThreadId?: string | null
  onOpenFile?: (file: PrimaryAgentFileTarget) => void
  onOpen: (target: AgentsFocusTarget) => void
  onNavigate: (section: AgentsSection) => void
}) {
  const queryClient = useQueryClient()
  const agents = useQuery(agentsQueryOptions(organizationId))
  const teams = useQuery(agentTeamsQueryOptions(organizationId))
  const workflows = useQuery(agentWorkflowsQueryOptions(organizationId))
  const inbox = useQuery(agentRequestsQueryOptions(organizationId, "inbox"))
  const resume = useMutation({
    mutationFn: (agentId: string) =>
      updateAgent(organizationId, agentId, { status: "active" }),
    onSuccess: (updated) =>
      afterUpdateAgent(queryClient, organizationId, updated),
  })

  if (agents.isPending) {
    return (
      <div className="workspace-agents-page">
        <p className="workspace-agents-muted" role="status">
          Loading your agents…
        </p>
      </div>
    )
  }
  if (agents.isError) {
    const unavailable = isUnavailableHere(agents.error)
    return (
      <div className="workspace-agents-page">
        <AgentsNotice
          title={
            unavailable
              ? "Agents aren't set up on this server."
              : agentsErrorMessage(agents.error, "Could not load your agents.")
          }
          alert={!unavailable}
          action={
            !unavailable && (
              <button
                className="workspace-agents-button"
                type="button"
                onClick={() => void agents.refetch()}
              >
                Try again
              </button>
            )
          }
        />
      </div>
    )
  }

  const primary = primaryAgent(agents.data)
  const specialists = specialistAgents(agents.data)
  const pausedSpecialists = specialists.filter(
    (agent) => agent.status !== "active"
  ).length
  const teamList = teams.data ?? []
  const work = [
    ...teamList.map((team) => (
      <AgentTeamEntry
        key={team.id}
        team={team}
        onOpen={() => onOpen({ kind: "team", teamId: team.id })}
      />
    )),
    ...specialists.map((agent) => (
      <SpecialistEntry
        key={agent.id}
        agent={agent}
        onOpen={() => onOpen({ kind: "specialist", agentId: agent.id })}
      />
    )),
  ]
  const teamNames = new Map(teamList.map((team) => [team.id, team.name]))

  return (
    <div className="workspace-agents-page">
      {primary && (
        <section
          className="workspace-agents-primary"
          aria-label="Primary agent"
          data-screen-context-kind="primary_agent"
          data-screen-context-id={primary.id}
          data-screen-context-name={primary.name}
        >
          <AgentFace agent={primary} />
          <div className="workspace-agents-primary-copy">
            <span>Primary agent</span>
            <strong>{primary.name}</strong>
            <small>{primary.description || "Your primary Oryxa agent."}</small>
          </div>
          <StatusPill status={primary.status} />
          {primary.status !== "active" && (
            <div className="workspace-agents-primary-paused">
              <p>
                Paused: it isn't answering or coordinating teams, and no team
                can be built until it's back.
              </p>
              <button
                className="workspace-agents-button"
                data-variant="primary"
                type="button"
                disabled={resume.isPending}
                onClick={() => resume.mutate(primary.id)}
              >
                {resume.isPending ? "Resuming…" : `Resume ${primary.name}`}
              </button>
              {resume.isError && (
                <p className="workspace-agents-error" role="alert">
                  {agentsErrorMessage(resume.error, "Could not resume it.")}
                </p>
              )}
            </div>
          )}
        </section>
      )}

      <dl className="workspace-agents-stats">
        <Stat
          label="Specialists"
          value={specialists.length}
          detail={pausedSpecialists ? `${pausedSpecialists} paused` : undefined}
          query={agents}
        />
        <Stat
          label="Multi-agent teams"
          value={teams.data?.length}
          query={teams}
        />
        <Stat
          label="Workflows"
          value={workflows.data?.length}
          query={workflows}
        />
        <Stat
          label="Requests waiting"
          value={inbox.data && waitingOn(inbox.data, viewerId).length}
          detail="For you to approve or answer"
          query={inbox}
        />
      </dl>

      <PrimaryAgentFiles
        organizationId={organizationId}
        threadId={primaryThreadId}
        onOpenFile={onOpenFile}
      />

      <section aria-labelledby="agents-overview-work">
        <p className="workspace-eyebrow" id="agents-overview-work">
          Agentic work
        </p>
        {teams.isError && (
          <p className="workspace-agents-note">
            {isUnavailableHere(teams.error)
              ? "Multi-agent teams aren't set up on this server."
              : "Your teams could not be loaded."}
          </p>
        )}
        {work.length ? (
          <>
            <div className="workspace-entry-grid workspace-agents-grid">
              {work.slice(0, OVERVIEW_WORK)}
            </div>
            {work.length > OVERVIEW_WORK && (
              <p className="workspace-agents-more">
                Showing {OVERVIEW_WORK} of {work.length}.{" "}
                <button
                  type="button"
                  onClick={() => onNavigate("multi-agents")}
                >
                  All teams
                </button>{" "}
                ·{" "}
                <button type="button" onClick={() => onNavigate("specialists")}>
                  All specialists
                </button>
              </p>
            )}
          </>
        ) : teams.isPending ? (
          <p className="workspace-agents-muted" role="status">
            Loading your teams…
          </p>
        ) : (
          <AgentsNotice
            title="No specialists or teams yet."
            action={
              <button
                className="workspace-agents-button"
                type="button"
                onClick={() => onNavigate("specialists")}
              >
                Create a specialist
              </button>
            }
          >
            Create a specialist for one kind of work, then bring specialists
            together as a multi-agent team.
          </AgentsNotice>
        )}
      </section>

      <section aria-labelledby="agents-overview-workflows">
        <p className="workspace-eyebrow" id="agents-overview-workflows">
          Workflows
        </p>
        {workflows.isPending ? (
          <p className="workspace-agents-muted" role="status">
            Loading your workflows…
          </p>
        ) : workflows.isError ? (
          <p className="workspace-agents-note">
            {isUnavailableHere(workflows.error)
              ? "Workflows aren't set up on this server."
              : "Your workflows could not be loaded."}
          </p>
        ) : workflows.data.length ? (
          <>
            <ul className="workspace-agents-workflows">
              {workflows.data.slice(0, OVERVIEW_WORKFLOWS).map((workflow) => (
                <WorkflowRow
                  organizationId={organizationId}
                  workflow={workflow}
                  teamName={teamNames.get(workflow.team_id)}
                  key={workflow.id}
                />
              ))}
            </ul>
            {workflows.data.length > OVERVIEW_WORKFLOWS && (
              <p className="workspace-agents-more">
                And{" "}
                {plural(
                  workflows.data.length - OVERVIEW_WORKFLOWS,
                  "more workflow"
                )}
                .
              </p>
            )}
          </>
        ) : (
          <p className="workspace-agents-muted">
            No workflows yet. A workflow hands fixed tasks to one of your teams,
            when started or on a schedule; creating one isn't in this app yet.
          </p>
        )}
      </section>
    </div>
  )
}
