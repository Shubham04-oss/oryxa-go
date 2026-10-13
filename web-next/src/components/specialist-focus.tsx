// A specialist as focused work (DESIGN.md §3): its chat on the left, through
// its Durable Object (the same chat the agent panel's Agents tab shows for
// it), and its definition, status and teams in the work panel.
import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  AgentFace,
  AgentsWorkColumn,
  ConfirmStep,
  FocusFallback,
  FocusHeader,
  StatusPill,
} from "./agents-parts"
import {
  agentsErrorMessage,
  formatDate,
  statusLabel,
  timeAgo,
} from "./agents-format"
import { SpecialistChat } from "./specialist-chat"
import { SpecialistForm } from "./specialists-section"
import type { AgentsFocusTarget } from "@/hooks/use-agents-mode"
import { useVisibleHeight } from "@/hooks/use-visible-height"
import { afterDeleteAgent, afterUpdateAgent } from "@/lib/agents/invalidation"
import { deleteAgent, updateAgent } from "@/lib/agents/managed-agents"
import type { AgentStatus } from "@/lib/agents/managed-agents"
import { isUnavailableHere } from "@/lib/agents/policy"
import {
  agentTeamsQueryOptions,
  agentsQueryOptions,
} from "@/lib/agents/queries"
import { clearSpecialistChat, specialistKeys } from "@/lib/specialists/client"
import { teamspacesQueryOptions } from "@/lib/teamspaces/queries"
import type { ScreenContext } from "@/lib/agents/screen-context"

const tabs = ["Overview", "Instructions", "Teams"] as const
type Tab = (typeof tabs)[number]

export function SpecialistFocus({
  organizationId,
  agentId,
  onBack,
  onOpen,
  getScreenContext,
}: {
  organizationId: string
  agentId: string
  onBack: () => void
  onOpen: (target: AgentsFocusTarget) => void
  getScreenContext?: () => ScreenContext
}) {
  const queryClient = useQueryClient()
  const agents = useQuery(agentsQueryOptions(organizationId))
  const teams = useQuery(agentTeamsQueryOptions(organizationId))
  // Read only to explain a refused delete in advance: a specialist
  // contributed to a teamspace cannot be deleted (agent_in_use).
  const teamspaces = useQuery(teamspacesQueryOptions(organizationId))
  const [tab, setTab] = useState<Tab>("Overview")
  const [editing, setEditing] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const [composing, setComposing] = useState(false)
  // Whether the delete got as far as clearing the chat.
  const [chatCleared, setChatCleared] = useState(false)
  const visibleHeight = useVisibleHeight(composing)
  const setStatus = useMutation({
    mutationFn: (status: AgentStatus) =>
      updateAgent(organizationId, agentId, { status }),
    onSuccess: (updated) =>
      afterUpdateAgent(queryClient, organizationId, updated),
  })
  // The chat goes first: the Worker reaches its Durable Object only for a
  // specialist Go still has, so a chat left behind could never be cleared
  // (REALTIME.md). A chat that cannot be cleared keeps the specialist.
  const remove = useMutation({
    mutationFn: async () => {
      setChatCleared(false)
      await clearSpecialistChat(organizationId, agentId)
      setChatCleared(true)
      await deleteAgent(organizationId, agentId)
    },
    onSuccess: () => {
      // Leave first: nothing on screen should still read the specialist.
      onBack()
      queryClient.removeQueries({
        queryKey: specialistKeys.agent(organizationId, agentId),
      })
      void afterDeleteAgent(queryClient, organizationId, agentId)
    },
  })
  const agent = agents.data?.find(
    (candidate) => candidate.id === agentId && candidate.kind === "specialist"
  )

  if (!agent) {
    return (
      <FocusFallback
        kind="Specialist"
        onBack={onBack}
        loading={agents.isPending}
        message={
          agents.isError
            ? agentsErrorMessage(
                agents.error,
                "Could not load this specialist.",
                {
                  unavailable_here: "Specialists aren't set up on this server.",
                }
              )
            : "This specialist no longer exists."
        }
        onRetry={
          agents.isError && !isUnavailableHere(agents.error)
            ? () => void agents.refetch()
            : undefined
        }
      />
    )
  }

  const active = agent.status === "active"
  const memberships = (teams.data ?? []).flatMap((team) =>
    team.members
      .filter((member) => member.agent_id === agentId)
      .map((member) => ({ team, role: member.role }))
  )
  const teamNames = memberships.map(({ team }) => team.name).join(", ")
  const spaces = (teamspaces.data ?? []).filter((space) =>
    space.contributions.some(
      (contribution) => contribution.agent_id === agentId
    )
  )
  const spaceNames = spaces.map((space) => space.name).join(", ")
  const statusError =
    setStatus.isError &&
    agentsErrorMessage(setStatus.error, "Could not change its status.", {
      not_found: "This specialist no longer exists.",
    })
  const removeError =
    remove.isError &&
    [
      agentsErrorMessage(remove.error, "Could not delete it. Try again.", {
        agent_in_use: `${agent.name} is on a team or in a teamspace. Take it off first.`,
        not_found: "This specialist no longer exists.",
      }),
      // Go refused after the chat was cleared: the chat started over.
      chatCleared && "Its chat has started over.",
    ]
      .filter(Boolean)
      .join(" ")

  function edit() {
    setTab("Overview")
    setConfirming(false)
    setEditing(true)
  }

  return (
    <div
      className="workspace-focus"
      data-composing={composing ? "true" : "false"}
      style={composing && visibleHeight ? { height: visibleHeight } : undefined}
    >
      <FocusHeader
        title={agent.name}
        kind={`Specialist · ${statusLabel(agent.status)}`}
        avatar={
          <AgentFace agent={agent} className="workspace-agents-focus-avatar" />
        }
        onBack={onBack}
      />

      <main className="workspace-focus-layout">
        <section
          className="workspace-conversation workspace-agents-room"
          aria-label={`Chat with ${agent.name}`}
        >
          <SpecialistChat
            layout="focus"
            organizationId={organizationId}
            specialist={agent}
            onComposingChange={setComposing}
            getScreenContext={getScreenContext}
          />
        </section>

        <AgentsWorkColumn
          label={agent.name}
          tabs={tabs}
          tab={tab}
          onTab={setTab}
          title={
            tab === "Overview"
              ? agent.name
              : tab === "Instructions"
                ? "How it works"
                : "Teams"
          }
          description={
            tab === "Overview"
              ? agent.description || "No description yet."
              : tab === "Instructions"
                ? "It follows these on every run."
                : "The teams it works in, and its role in each."
          }
          status={tab === "Overview" && <StatusPill status={agent.status} />}
        >
          {tab === "Overview" &&
            (editing ? (
              <div className="workspace-work-card">
                <SpecialistForm
                  organizationId={organizationId}
                  agent={agent}
                  onCancel={() => setEditing(false)}
                  onDone={() => setEditing(false)}
                />
              </div>
            ) : (
              <>
                <div className="workspace-work-card">
                  <dl className="workspace-agents-facts">
                    <div>
                      <dt>Status</dt>
                      <dd>{statusLabel(agent.status)}</dd>
                    </div>
                    <div>
                      <dt>Created</dt>
                      <dd>{formatDate(agent.created_at)}</dd>
                    </div>
                    <div>
                      <dt>Last changed</dt>
                      <dd>{timeAgo(agent.updated_at)}</dd>
                    </div>
                    <div>
                      <dt>Teams</dt>
                      <dd>
                        {teams.data
                          ? teamNames || "None yet"
                          : teams.isError
                            ? "Unavailable"
                            : "Loading…"}
                      </dd>
                    </div>
                  </dl>
                </div>
                <div className="workspace-work-card">
                  <div className="workspace-agents-actions" data-align="start">
                    <button
                      className="workspace-agents-button"
                      type="button"
                      onClick={edit}
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
                      ? "Pausing stops it answering: teams skip it, and you can't chat with it until you resume it."
                      : "Paused: teams skip it, and you can't chat with it until you resume it."}
                  </p>
                  {statusError && (
                    <p className="workspace-agents-error" role="alert">
                      {statusError}
                    </p>
                  )}
                  {confirming && (
                    <ConfirmStep
                      question={`Delete ${agent.name}?`}
                      detail="It stops working for you and its chat is cleared. Neither can be restored."
                      blocked={
                        memberships.length
                          ? `It's on ${teamNames}. Take it off ${memberships.length === 1 ? "that team" : "those teams"} first.`
                          : spaces.length
                            ? `It's in ${spaceNames}. Take it out of ${spaces.length === 1 ? "that teamspace" : "those teamspaces"} first.`
                            : undefined
                      }
                      confirmLabel="Delete specialist"
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

          {tab === "Instructions" && (
            <div className="workspace-work-card">
              <p className="workspace-agents-instructions">
                {agent.instructions}
              </p>
              <div className="workspace-agents-actions" data-align="start">
                <button
                  className="workspace-agents-button"
                  type="button"
                  onClick={edit}
                >
                  Edit instructions
                </button>
              </div>
            </div>
          )}

          {tab === "Teams" && (
            <div className="workspace-work-card">
              <h3>Teams</h3>
              {teams.isError ? (
                <p className="workspace-work-empty">
                  {agentsErrorMessage(
                    teams.error,
                    "Could not load your teams.",
                    {
                      unavailable_here:
                        "Multi-agent teams aren't set up on this server.",
                    }
                  )}
                </p>
              ) : !teams.data ? (
                <p className="workspace-work-empty">Loading…</p>
              ) : memberships.length ? (
                <ul className="workspace-agents-links">
                  {memberships.map(({ team, role }) => (
                    <li key={team.id}>
                      <button
                        type="button"
                        aria-label={`Open ${team.name}`}
                        onClick={() =>
                          onOpen({ kind: "team", teamId: team.id })
                        }
                      >
                        <strong>{team.name}</strong>
                        <small>{role}</small>
                      </button>
                      <StatusPill status={team.status} />
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="workspace-work-empty">
                  Not on a team yet. Add it to one from Multi-agent teams.
                </p>
              )}
            </div>
          )}
        </AgentsWorkColumn>
      </main>
    </div>
  )
}
