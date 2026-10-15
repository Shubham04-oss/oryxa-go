// Agents mode (DESIGN.md §2): Agent overview, Multi-agent teams and
// Specialists in the canvas, and a specialist or team as focused work. The
// workspace shell renders these two entry points and keeps which item is
// open (useAgentsMode); everything else here reads the agents service.
import { useState } from "react"
import type { ReactNode } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Plus, Settings } from "lucide-react"
import {
  AgentFace,
  AgentTeamRow,
  AgentsNotice,
  SpecialistCard,
  StatusPill,
} from "./agents-parts"
import { agentsErrorMessage } from "./agents-format"
import { openRequests } from "./requests-view-open"
import { SpecialistFocus } from "./specialist-focus"
import { SpecialistForm, SpecialistsSection } from "./specialists-section"
import { AgentTeamFocus } from "./multi-agent-team-focus"
import {
  AgentTeamForm,
  AgentTeamsSection,
  teamBlocker,
} from "./multi-agent-teams-section"
import { toolkitIcon } from "./workspace-tools-data"
import { Button } from "@/components/ui/button"
import { connectionsQueryOptions, toolkitName } from "@/lib/apps/client"
import { FollowUpsByTeamspace } from "./team-synthesis"
import { WorkflowOverview } from "./workflow-overview"
import type { AgentsFocusTarget } from "@/hooks/use-agents-mode"
import type { ScreenContext } from "@/lib/agents/screen-context"
import { afterUpdateAgent } from "@/lib/agents/invalidation"
import { updateAgent } from "@/lib/agents/managed-agents"
import type { ManagedAgent } from "@/lib/agents/managed-agents"
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

export function AgentsCanvas({
  section,
  organizationId,
  viewerId,
  onOpen,
  onNavigate,
  onOpenSettings,
}: {
  section: AgentsSection
  /** The signed-in member's organization; without one there is nothing to read. */
  organizationId?: string
  viewerId?: string
  onOpen: (target: AgentsFocusTarget) => void
  onNavigate: (section: AgentsSection) => void
  /** The primary agent's Settings: what it may read (Settings → Tool access). */
  onOpenSettings?: () => void
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
      onOpen={onOpen}
      onNavigate={onNavigate}
      onOpenSettings={onOpenSettings}
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

/** "Slack, Notion and Drive": the apps, joined as a sentence says them. */
function listed(names: readonly string[]): string {
  if (names.length <= 1) return names.join("")
  return `${names.slice(0, -1).join(", ")} and ${names.at(-1)}`
}

/** An app as its brand writes it ("GitHub"), else as the catalog does. */
function appName(toolkit: string): string {
  return toolkitIcon(toolkit)?.title || toolkitName(toolkit)
}

/**
 * One of the hero's inline numbers. A count Go can't give is never guessed:
 * "…" while it loads, "–" when it couldn't be read.
 */
function Stat({
  label,
  value,
  query,
  onOpen,
}: {
  label: string
  value?: number
  query: { isPending: boolean; isError: boolean }
  /** Where the number leads. */
  onOpen?: () => void
}) {
  const shown = value !== undefined ? value : query.isPending ? "…" : "–"
  return (
    <div className="agents-hero-stat">
      <dt>{label}</dt>
      <dd aria-busy={query.isPending || undefined}>
        {onOpen && value !== undefined ? (
          <button
            type="button"
            aria-label={`${shown} ${label}`}
            onClick={onOpen}
          >
            {shown}
          </button>
        ) : (
          shown
        )}
      </dd>
    </div>
  )
}

/** A section's head: its title and count, and the way to make another. */
function SectionHead({
  id,
  title,
  count,
  action,
}: {
  id: string
  title: string
  count?: number
  action?: ReactNode
}) {
  return (
    <div className="agents-section-head">
      <h2 id={id}>
        {title}
        {count !== undefined && count > 0 && ` · ${count}`}
      </h2>
      {action}
    </div>
  )
}

function AgentsOverview({
  organizationId,
  viewerId,
  onOpen,
  onNavigate,
  onOpenSettings,
}: {
  organizationId: string
  viewerId?: string
  onOpen: (target: AgentsFocusTarget) => void
  onNavigate: (section: AgentsSection) => void
  onOpenSettings?: () => void
}) {
  const queryClient = useQueryClient()
  const agents = useQuery(agentsQueryOptions(organizationId))
  const teams = useQuery(agentTeamsQueryOptions(organizationId))
  const inbox = useQuery(agentRequestsQueryOptions(organizationId, "inbox"))
  const apps = useQuery({
    ...connectionsQueryOptions(organizationId),
    retry: false,
  })
  const [creating, setCreating] = useState<"team" | "specialist" | null>(null)
  const [editing, setEditing] = useState<string | null>(null)
  const resume = useMutation({
    mutationFn: (agentId: string) =>
      updateAgent(organizationId, agentId, { status: "active" }),
    onSuccess: (updated) =>
      afterUpdateAgent(queryClient, organizationId, updated),
  })
  const toggle = useMutation({
    mutationFn: (agent: ManagedAgent) =>
      updateAgent(organizationId, agent.id, {
        status: agent.status === "active" ? "disabled" : "active",
      }),
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
              <Button variant="outline" onClick={() => void agents.refetch()}>
                Try again
              </Button>
            )
          }
        />
      </div>
    )
  }

  const primary = primaryAgent(agents.data)
  const specialists = specialistAgents(agents.data)
  const teamList = teams.data ?? []
  const teamBlocked = teamBlocker(primary, specialists)
  const teamsUnavailable = teams.isError && isUnavailableHere(teams.error)
  // The apps the member connected, as the hero's line names them; a read
  // that fails or isn't served leaves the line out rather than guess.
  const connected = apps.data
    ? [
        ...new Set(
          apps.data
            .filter((connection) => connection.status === "active")
            .map((connection) => appName(connection.toolkit))
        ),
      ]
    : undefined
  const editingAgent = specialists.find((agent) => agent.id === editing)

  return (
    <div className="workspace-agents-page agents-overview">
      {primary && (
        <section
          className="workspace-agents-primary agents-hero"
          aria-label="Primary agent"
          data-screen-context-kind="primary_agent"
          data-screen-context-id={primary.id}
          data-screen-context-name={primary.name}
        >
          <AgentFace agent={primary} />
          <div className="workspace-agents-primary-copy">
            <strong>{primary.name}</strong>
            <small>
              {[
                "Your agent",
                connected === undefined
                  ? undefined
                  : connected.length
                    ? `reads ${listed(connected)}`
                    : "no apps connected yet",
                "suggests, you decide",
              ]
                .filter(Boolean)
                .join(" · ")}
            </small>
          </div>
          <StatusPill status={primary.status} />
          <dl className="agents-hero-stats">
            <Stat
              label="waiting for you"
              value={inbox.data && waitingOn(inbox.data, viewerId).length}
              query={inbox}
              onOpen={() => openRequests()}
            />
            <Stat
              label={specialists.length === 1 ? "specialist" : "specialists"}
              value={specialists.length}
              query={agents}
              onOpen={() => onNavigate("specialists")}
            />
            <Stat
              label={teams.data?.length === 1 ? "team" : "teams"}
              value={teams.data?.length}
              query={teams}
              onOpen={() => onNavigate("multi-agents")}
            />
          </dl>
          {onOpenSettings && (
            <Button variant="outline" onClick={onOpenSettings}>
              <Settings aria-hidden="true" />
              Settings
            </Button>
          )}
          {primary.status !== "active" && (
            <div className="workspace-agents-primary-paused">
              <p>
                Paused: it isn't answering or coordinating teams, and no team
                can be built until it's back.
              </p>
              <Button
                disabled={resume.isPending}
                onClick={() => resume.mutate(primary.id)}
              >
                {resume.isPending ? "Resuming…" : `Resume ${primary.name}`}
              </Button>
              {resume.isError && (
                <p className="workspace-agents-error" role="alert">
                  {agentsErrorMessage(resume.error, "Could not resume it.")}
                </p>
              )}
            </div>
          )}
        </section>
      )}

      {/* The conversation's own files, live work and requests are in the
          agent session's work pane (agent-session-work.tsx), beside it. */}

      <section className="agents-section" aria-labelledby="agents-teams">
        <SectionHead
          id="agents-teams"
          title="Multi-agent teams"
          count={teamList.length}
          action={
            !teamsUnavailable && (
              <Button
                variant="outline"
                size="sm"
                aria-expanded={creating === "team"}
                aria-describedby={
                  teamBlocked ? "agents-overview-team-blocked" : undefined
                }
                disabled={!teams.data || Boolean(teamBlocked)}
                onClick={() =>
                  setCreating((open) => (open === "team" ? null : "team"))
                }
              >
                <Plus aria-hidden="true" />
                New team
              </Button>
            )
          }
        />
        {teamBlocked && !teamsUnavailable && teams.data && (
          <p
            className="workspace-agents-note"
            id="agents-overview-team-blocked"
          >
            {teamBlocked}
          </p>
        )}
        {creating === "team" && !teamBlocked && (
          <AgentTeamForm
            organizationId={organizationId}
            specialists={specialists}
            onCancel={() => setCreating(null)}
            onDone={(created) => {
              setCreating(null)
              onOpen({ kind: "team", teamId: created.id })
            }}
          />
        )}
        {teams.isPending ? (
          <p className="workspace-agents-muted" role="status">
            Loading your teams…
          </p>
        ) : teams.isError ? (
          <p className="workspace-agents-note">
            {teamsUnavailable
              ? "Multi-agent teams aren't set up on this server."
              : "Your teams could not be loaded."}
          </p>
        ) : teamList.length ? (
          <>
            <div className="agents-team-list">
              {teamList.slice(0, OVERVIEW_WORK).map((team) => (
                <AgentTeamRow
                  key={team.id}
                  team={team}
                  onOpen={() => onOpen({ kind: "team", teamId: team.id })}
                />
              ))}
            </div>
            {teamList.length > OVERVIEW_WORK && (
              <p className="workspace-agents-more">
                Showing {OVERVIEW_WORK} of {teamList.length}.{" "}
                <button
                  type="button"
                  onClick={() => onNavigate("multi-agents")}
                >
                  All teams
                </button>
              </p>
            )}
          </>
        ) : (
          creating !== "team" && (
            <p className="workspace-agents-muted">
              No teams yet. A team is {primary?.name ?? "your agent"} working
              with specialists you choose, in one shared conversation.
            </p>
          )
        )}
      </section>

      <section className="agents-section" aria-labelledby="agents-specialists">
        <SectionHead
          id="agents-specialists"
          title="Specialists"
          count={specialists.length}
          action={
            <Button
              variant="outline"
              size="sm"
              aria-expanded={creating === "specialist"}
              onClick={() =>
                setCreating((open) =>
                  open === "specialist" ? null : "specialist"
                )
              }
            >
              <Plus aria-hidden="true" />
              New specialist
            </Button>
          }
        />
        {creating === "specialist" && (
          <SpecialistForm
            organizationId={organizationId}
            onCancel={() => setCreating(null)}
            onDone={(created) => {
              setCreating(null)
              onOpen({ kind: "specialist", agentId: created.id })
            }}
          />
        )}
        {editingAgent && (
          <SpecialistForm
            key={editingAgent.id}
            organizationId={organizationId}
            agent={editingAgent}
            onCancel={() => setEditing(null)}
            onDone={() => setEditing(null)}
          />
        )}
        {toggle.isError && (
          <p className="workspace-agents-error" role="alert">
            {agentsErrorMessage(toggle.error, "Could not change it.")}
          </p>
        )}
        {specialists.length ? (
          <>
            <div className="agents-specialist-grid">
              {specialists.slice(0, OVERVIEW_WORK).map((agent) => (
                <SpecialistCard
                  key={agent.id}
                  agent={agent}
                  busy={toggle.isPending && toggle.variables.id === agent.id}
                  onOpen={() =>
                    onOpen({ kind: "specialist", agentId: agent.id })
                  }
                  onToggle={() => toggle.mutate(agent)}
                  onEdit={() => setEditing(agent.id)}
                />
              ))}
            </div>
            {specialists.length > OVERVIEW_WORK && (
              <p className="workspace-agents-more">
                Showing {OVERVIEW_WORK} of {specialists.length}.{" "}
                <button type="button" onClick={() => onNavigate("specialists")}>
                  All specialists
                </button>
              </p>
            )}
          </>
        ) : (
          creating !== "specialist" && (
            <p className="workspace-agents-muted">
              No specialists yet. A specialist is an agent for one kind of work,
              with its own instructions; then bring specialists together as a
              multi-agent team.
            </p>
          )
        )}
      </section>

      <WorkflowOverview organizationId={organizationId} />

      <FollowUpsByTeamspace organizationId={organizationId} />
    </div>
  )
}
