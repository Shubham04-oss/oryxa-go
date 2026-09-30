// Agents mode (DESIGN.md §2): the Agent overview, with the primary agent's
// files, and a way on to Requests. The workspace shell renders it in the
// canvas and in the focused Agent overview; everything here reads the agents
// service. Multi-agent teams, workflows and specialists were archived with
// `archive/pre-simplify-2026-09-30`.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { AgentFace, AgentsNotice, StatusPill } from "./agents-parts"
import { agentsErrorMessage } from "./agents-format"
import { openRequests } from "./requests-view-open"
import { PrimaryAgentFiles } from "./primary-agent-files"
import type { PrimaryAgentFileTarget } from "./primary-agent-files"
import { afterUpdateAgent } from "@/lib/agents/invalidation"
import { updateAgent } from "@/lib/agents/managed-agents"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import {
  canDecideAgentRequest,
  canRespondToAgentRequest,
  isUnavailableHere,
  primaryAgent,
} from "@/lib/agents/policy"
import {
  agentRequestsQueryOptions,
  agentsQueryOptions,
} from "@/lib/agents/queries"

export function AgentsCanvas({
  organizationId,
  viewerId,
  primaryThreadId,
  onOpenFile,
}: {
  /** The signed-in member's organization; without one there is nothing to read. */
  organizationId?: string
  viewerId?: string
  primaryThreadId?: string | null
  onOpenFile?: (file: PrimaryAgentFileTarget) => void
}) {
  if (!organizationId) {
    return (
      <div className="workspace-agents-page">
        <AgentsNotice title="Your agents appear here once you're signed in.">
          This preview isn't connected to a workspace, so it has no agents to
          show.
        </AgentsNotice>
      </div>
    )
  }
  return (
    <AgentsOverview
      organizationId={organizationId}
      viewerId={viewerId}
      primaryThreadId={primaryThreadId}
      onOpenFile={onOpenFile}
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
  onOpen,
}: {
  label: string
  value?: number
  detail?: string
  query: { isPending: boolean; isError: boolean; error: unknown }
  /** Where the tile leads: a quiet link under it. */
  onOpen?: { label: string; open: () => void }
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
      {onOpen && (
        <button
          className="workspace-requests-stat-link"
          type="button"
          onClick={onOpen.open}
        >
          {onOpen.label}
        </button>
      )}
    </div>
  )
}

function AgentsOverview({
  organizationId,
  viewerId,
  primaryThreadId,
  onOpenFile,
}: {
  organizationId: string
  viewerId?: string
  primaryThreadId?: string | null
  onOpenFile?: (file: PrimaryAgentFileTarget) => void
}) {
  const queryClient = useQueryClient()
  const agents = useQuery(agentsQueryOptions(organizationId))
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
              <p>Paused: it isn't answering until it's back.</p>
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
          label="Requests waiting"
          value={inbox.data && waitingOn(inbox.data, viewerId).length}
          detail="For you to approve or answer"
          query={inbox}
          onOpen={{ label: "View requests", open: () => openRequests() }}
        />
      </dl>

      <PrimaryAgentFiles
        organizationId={organizationId}
        threadId={primaryThreadId}
        onOpenFile={onOpenFile}
      />
    </div>
  )
}
