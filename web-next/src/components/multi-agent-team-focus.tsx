// A multi-agent team as focused work (DESIGN.md §3): its conversation on
// the left, and the team's purpose and roster in the work panel.
import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  AgentAvatarStack,
  AgentFace,
  AgentsWorkColumn,
  ConfirmStep,
  FocusFallback,
  FocusHeader,
  StatusPill,
  teamAgents,
} from "./agents-parts"
import {
  agentsErrorMessage,
  formatDate,
  plural,
  statusLabel,
  timeAgo,
} from "./agents-format"
import { AgentTeamForm } from "./multi-agent-teams-section"
import { AgentTeamRoom } from "./multi-agent-team-room"
import type { AgentsFocusTarget } from "@/hooks/use-agents-mode"
import { useVisibleHeight } from "@/hooks/use-visible-height"
import { deleteAgentTeam, updateAgentTeam } from "@/lib/agents/agent-teams"
import type { AgentTeamStatus } from "@/lib/agents/agent-teams"
import {
  afterDeleteAgentTeam,
  afterUpdateAgentTeam,
} from "@/lib/agents/invalidation"
import {
  agentTeamSeats,
  isUnavailableHere,
  primaryAgent,
  specialistAgents,
} from "@/lib/agents/policy"
import {
  agentTeamsQueryOptions,
  agentWorkflowsQueryOptions,
  agentsQueryOptions,
} from "@/lib/agents/queries"

const tabs = ["Overview", "Members"] as const
type Tab = (typeof tabs)[number]

export function AgentTeamFocus({
  organizationId,
  teamId,
  onBack,
  onOpen,
  onChat,
}: {
  organizationId: string
  teamId: string
  onBack: () => void
  onOpen: (target: AgentsFocusTarget) => void
  /** Opens a specialist's chat in the agent panel, once it has one. */
  onChat?: (agentId: string) => void
}) {
  const queryClient = useQueryClient()
  const teams = useQuery(agentTeamsQueryOptions(organizationId))
  const agents = useQuery(agentsQueryOptions(organizationId))
  // Read only to explain a refused delete: a workflow that runs the team
  // keeps it from being deleted (team_in_use).
  const workflows = useQuery(agentWorkflowsQueryOptions(organizationId))
  const [tab, setTab] = useState<Tab>("Overview")
  const [editing, setEditing] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const [composing, setComposing] = useState(false)
  const visibleHeight = useVisibleHeight(composing)
  const setStatus = useMutation({
    mutationFn: (status: AgentTeamStatus) =>
      updateAgentTeam(organizationId, teamId, { status }),
    onSuccess: (updated) =>
      afterUpdateAgentTeam(queryClient, organizationId, updated),
  })
  const remove = useMutation({
    mutationFn: () => deleteAgentTeam(organizationId, teamId),
    onSuccess: () => {
      // Leave first, so the conversation stops reading a team that is gone.
      onBack()
      void afterDeleteAgentTeam(queryClient, organizationId, teamId)
    },
  })
  const team = teams.data?.find((candidate) => candidate.id === teamId)

  if (!team) {
    return (
      <FocusFallback
        kind="Multi-agent team"
        onBack={onBack}
        loading={teams.isPending}
        message={
          teams.isError
            ? agentsErrorMessage(teams.error, "Could not load this team.", {
                unavailable_here:
                  "Multi-agent teams aren't set up on this server.",
              })
            : "This team no longer exists."
        }
        onRetry={
          teams.isError && !isUnavailableHere(teams.error)
            ? () => void teams.refetch()
            : undefined
        }
      />
    )
  }

  const active = team.status === "active"
  const primary = agents.data ? primaryAgent(agents.data) : undefined
  const coordinatorPaused = primary?.status === "disabled"
  const seats = agentTeamSeats(team)
  const paused = team.members.filter((member) => member.status !== "active")
  const running = (workflows.data ?? []).filter(
    (workflow) => workflow.team_id === team.id
  )
  const statusError =
    setStatus.isError &&
    agentsErrorMessage(setStatus.error, "Could not change its status.", {
      not_found: "This team no longer exists.",
    })
  const removeError =
    remove.isError &&
    agentsErrorMessage(remove.error, "Could not delete it. Try again.", {
      team_in_use:
        "A workflow still runs this team. Delete that workflow first.",
      not_found: "This team no longer exists.",
    })

  return (
    <div
      className="workspace-focus"
      data-composing={composing ? "true" : "false"}
      style={composing && visibleHeight ? { height: visibleHeight } : undefined}
    >
      <FocusHeader
        title={team.name}
        kind={`Multi-agent team · ${statusLabel(team.status)}`}
        avatar={<AgentAvatarStack agents={teamAgents(team)} max={3} />}
        onBack={onBack}
      />

      <main className="workspace-focus-layout">
        <AgentTeamRoom
          organizationId={organizationId}
          team={team}
          coordinatorPaused={coordinatorPaused}
          onComposingChange={setComposing}
        />

        <AgentsWorkColumn
          label={team.name}
          tabs={tabs}
          tab={tab}
          onTab={setTab}
          title={tab === "Overview" ? team.name : "Who works here"}
          description={
            tab === "Overview"
              ? team.description || "No purpose written yet."
              : `${team.coordinator_name} coordinates; @mention anyone to ask them directly.`
          }
          status={tab === "Overview" && <StatusPill status={team.status} />}
        >
          {tab === "Overview" &&
            (editing ? (
              <div className="workspace-work-card">
                <AgentTeamForm
                  organizationId={organizationId}
                  team={team}
                  specialists={agents.data ? specialistAgents(agents.data) : []}
                  onCancel={() => setEditing(false)}
                  onDone={() => setEditing(false)}
                />
              </div>
            ) : (
              <>
                <div className="workspace-work-card">
                  <dl className="workspace-agents-facts">
                    <div>
                      <dt>Coordinator</dt>
                      <dd>
                        {team.coordinator_name}
                        {coordinatorPaused && " (paused)"}
                      </dd>
                    </div>
                    <div>
                      <dt>Specialists</dt>
                      <dd>
                        {team.members.length}
                        {paused.length > 0 && ` · ${paused.length} paused`}
                      </dd>
                    </div>
                    <div>
                      <dt>Created</dt>
                      <dd>{formatDate(team.created_at)}</dd>
                    </div>
                    <div>
                      <dt>Last changed</dt>
                      <dd>{timeAgo(team.updated_at)}</dd>
                    </div>
                  </dl>
                </div>
                <div className="workspace-work-card">
                  <div className="workspace-agents-actions" data-align="start">
                    <button
                      className="workspace-agents-button"
                      type="button"
                      disabled={!agents.data}
                      onClick={() => {
                        setConfirming(false)
                        setEditing(true)
                      }}
                    >
                      Edit
                    </button>
                    <button
                      className="workspace-agents-button"
                      type="button"
                      disabled={setStatus.isPending}
                      onClick={() =>
                        setStatus.mutate(active ? "disabled" : "active")
                      }
                    >
                      {setStatus.isPending
                        ? active
                          ? "Pausing…"
                          : "Resuming…"
                        : active
                          ? "Pause"
                          : "Resume"}
                    </button>
                    <button
                      className="workspace-agents-button"
                      data-variant="quiet-danger"
                      type="button"
                      aria-expanded={confirming}
                      onClick={() => {
                        remove.reset()
                        setConfirming(true)
                      }}
                    >
                      Delete…
                    </button>
                  </div>
                  <p className="workspace-agents-note">
                    {active
                      ? "Pausing keeps the team and its conversation but stops it taking messages."
                      : "Paused: the conversation is kept, and the team takes no messages until you resume it."}
                  </p>
                  {statusError && (
                    <p className="workspace-agents-error" role="alert">
                      {statusError}
                    </p>
                  )}
                  {confirming && (
                    <ConfirmStep
                      question={`Delete ${team.name}?`}
                      detail="Its conversation goes with it. The specialists stay."
                      blocked={
                        running.length
                          ? `${running.map((workflow) => workflow.name).join(", ")} ${running.length === 1 ? "runs" : "run"} this team. Delete ${running.length === 1 ? "that workflow" : "those workflows"} first.`
                          : undefined
                      }
                      confirmLabel="Delete team"
                      pendingLabel="Deleting…"
                      pending={remove.isPending}
                      error={removeError || undefined}
                      onCancel={() => setConfirming(false)}
                      onConfirm={() => remove.mutate()}
                    />
                  )}
                </div>
              </>
            ))}

          {tab === "Members" && (
            <div className="workspace-work-card">
              <ul className="workspace-agents-members">
                {seats.map((seat) => {
                  const coordinator = seat.handle === "primary"
                  const seatPaused = coordinator
                    ? coordinatorPaused
                    : !seat.active
                  return (
                    <li key={seat.agent_id}>
                      <AgentFace
                        agent={{ id: seat.agent_id, name: seat.name }}
                      />
                      {coordinator ? (
                        <span>
                          <strong>{seat.name}</strong>
                          <small>
                            {seat.role} · @{seat.handle}
                          </small>
                        </span>
                      ) : (
                        <button
                          type="button"
                          aria-label={`Open ${seat.name}`}
                          onClick={() =>
                            onOpen({
                              kind: "specialist",
                              agentId: seat.agent_id,
                            })
                          }
                        >
                          <strong>{seat.name}</strong>
                          <small>
                            {seat.role} · @{seat.handle}
                          </small>
                        </button>
                      )}
                      {seatPaused && <StatusPill status="disabled" />}
                      {onChat && !coordinator && !seatPaused && (
                        <button
                          className="workspace-agents-button"
                          type="button"
                          aria-label={`Chat with ${seat.name}`}
                          onClick={() => onChat(seat.agent_id)}
                        >
                          Chat
                        </button>
                      )}
                    </li>
                  )
                })}
              </ul>
              {paused.length > 0 && (
                <p className="workspace-agents-note">
                  {paused.length === 1
                    ? `${paused[0].name} is paused, so mentions skip it.`
                    : `${plural(paused.length, "specialist")} are paused, so mentions skip them.`}
                </p>
              )}
            </div>
          )}
        </AgentsWorkColumn>
      </main>
    </div>
  )
}
