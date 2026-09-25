// Agents mode → Multi-agent teams: the member's teams (their primary agent
// coordinating some of their specialists), and the form that creates or
// edits one within the agents service's rules.
import { useId, useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Plus } from "lucide-react"
import {
  AgentFace,
  AgentTeamEntry,
  AgentsNotice,
  StatusPill,
} from "./agents-parts"
import {
  agentsErrorMessage,
  cleanLine,
  plural,
  runeCount,
} from "./agents-format"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import type { AgentsFocusTarget } from "@/hooks/use-agents-mode"
import { ApiError } from "@/lib/api/client"
import { createAgentTeam, updateAgentTeam } from "@/lib/agents/agent-teams"
import type {
  AgentTeam,
  AgentTeamMemberInput,
  UpdateAgentTeamInput,
} from "@/lib/agents/agent-teams"
import {
  afterCreateAgentTeam,
  afterUpdateAgentTeam,
} from "@/lib/agents/invalidation"
import type { ManagedAgent } from "@/lib/agents/managed-agents"
import {
  isUnavailableHere,
  primaryAgent,
  specialistAgents,
} from "@/lib/agents/policy"
import {
  agentTeamsQueryOptions,
  agentsQueryOptions,
} from "@/lib/agents/queries"

// agentteams.validate
const NAME_MAX = 80
const PURPOSE_MAX = 1000
const ROLE_MAX = 300
const MEMBERS_MAX = 16

/**
 * Why the member cannot build a team now, as the server would refuse it:
 * a team needs an active primary agent to coordinate it and at least one
 * active specialist.
 */
export function teamBlocker(
  primary: ManagedAgent | undefined,
  specialists: readonly ManagedAgent[]
): string | undefined {
  if (primary && primary.status !== "active") {
    return `${primary.name}, your primary agent, is paused. It coordinates every team, so resume it on Agent overview first.`
  }
  if (!specialists.some((agent) => agent.status === "active")) {
    return "A team needs at least one active specialist. Create one in Specialists first."
  }
  return undefined
}

type Seat = { agentId: string; role: string }
type Errors = {
  name?: string
  description?: string
  roster?: string
  roles?: Partial<Record<string, string>>
}

function firstRunes(value: string, count: number) {
  return [...value].slice(0, count).join("")
}

function rosterOf(team?: AgentTeam): Seat[] {
  return [...(team?.members ?? [])]
    .sort((left, right) => left.position - right.position)
    .map((member) => ({ agentId: member.agent_id, role: member.role }))
}

function rosterKey(seats: readonly Seat[]) {
  return seats.map((seat) => `${seat.agentId}\u0000${seat.role}`).join("\u0001")
}

/**
 * Creates a team, or edits one when `team` is given. An edit sends only
 * what changed; the roster goes only when it changed, since the server
 * re-checks every listed specialist and refuses a paused one.
 */
export function AgentTeamForm({
  organizationId,
  team,
  specialists,
  onDone,
  onCancel,
}: {
  organizationId: string
  team?: AgentTeam
  specialists: readonly ManagedAgent[]
  onDone: (team: AgentTeam) => void
  onCancel: () => void
}) {
  const queryClient = useQueryClient()
  const id = useId()
  const [name, setName] = useState(team?.name ?? "")
  const [description, setDescription] = useState(team?.description ?? "")
  const [roster, setRoster] = useState<Seat[]>(() => rosterOf(team))
  const [errors, setErrors] = useState<Errors>({})
  const [formError, setFormError] = useState("")
  const byId = new Map(specialists.map((agent) => [agent.id, agent]))
  // Active specialists can join; a paused one shows only while it is on the
  // roster already, so it can be taken off.
  const candidates = specialists.filter(
    (agent) =>
      agent.status === "active" ||
      roster.some((seat) => seat.agentId === agent.id)
  )

  const save = useMutation({
    mutationFn: (input: {
      name: string
      description: string
      members: AgentTeamMemberInput[]
    }) => {
      if (!team) return createAgentTeam(organizationId, input)
      const change: UpdateAgentTeamInput = {}
      if (input.name !== team.name) change.name = input.name
      if (input.description !== team.description)
        change.description = input.description
      const members = input.members.map((member) => ({
        agentId: member.agent_id,
        role: member.role,
      }))
      if (rosterKey(members) !== rosterKey(rosterOf(team)))
        change.members = input.members
      return Object.keys(change).length
        ? updateAgentTeam(organizationId, team.id, change)
        : Promise.resolve(team)
    },
    onSuccess: async (saved) => {
      if (!team) await afterCreateAgentTeam(queryClient, organizationId, saved)
      else if (saved !== team)
        await afterUpdateAgentTeam(queryClient, organizationId, saved)
      onDone(saved)
    },
    onError: (error, input) => {
      if (error instanceof ApiError && error.code === "name_conflict") {
        setErrors({ name: `You already have a team called “${input.name}”.` })
        return
      }
      setFormError(
        agentsErrorMessage(
          error,
          team
            ? "Could not save the team. Try again."
            : "Could not create the team. Try again.",
          {
            not_found: "This team no longer exists.",
            unavailable_here: "Multi-agent teams aren't set up on this server.",
          }
        )
      )
    },
  })

  function toggle(agent: ManagedAgent, selected: boolean) {
    setRoster((current) =>
      selected
        ? [
            ...current,
            {
              agentId: agent.id,
              role: firstRunes(agent.description.trim(), ROLE_MAX),
            },
          ]
        : current.filter((seat) => seat.agentId !== agent.id)
    )
    setErrors((current) => ({ ...current, roster: undefined }))
  }

  function setRole(agentId: string, role: string) {
    setRoster((current) =>
      current.map((seat) =>
        seat.agentId === agentId ? { ...seat, role } : seat
      )
    )
    setErrors((current) => ({
      ...current,
      roles: { ...current.roles, [agentId]: undefined },
    }))
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (save.isPending) return
    const fields = {
      name: cleanLine(name),
      description: description.trim(),
      members: roster.map((seat) => ({
        agent_id: seat.agentId,
        role: cleanLine(seat.role),
      })),
    }
    const found: Errors = {}
    if (!fields.name) found.name = "Give the team a name."
    else if (runeCount(fields.name) > NAME_MAX)
      found.name = `Use at most ${NAME_MAX} characters.`
    if (runeCount(fields.description) > PURPOSE_MAX)
      found.description = "Keep the purpose to 1,000 characters."
    if (fields.members.length === 0)
      found.roster = "Pick at least one specialist."
    else if (fields.members.length > MEMBERS_MAX)
      found.roster = `A team holds at most ${MEMBERS_MAX} specialists.`
    const roles: Record<string, string> = {}
    for (const member of fields.members) {
      const agentName = byId.get(member.agent_id)?.name ?? "This specialist"
      if (!member.role) roles[member.agent_id] = `Give ${agentName} a role.`
      else if (runeCount(member.role) > ROLE_MAX)
        roles[member.agent_id] = `Keep the role to ${ROLE_MAX} characters.`
    }
    if (Object.keys(roles).length) found.roles = roles
    const rosterChanged =
      !team ||
      rosterKey(
        fields.members.map((member) => ({
          agentId: member.agent_id,
          role: member.role,
        }))
      ) !== rosterKey(rosterOf(team))
    const paused = fields.members
      .map((member) => byId.get(member.agent_id))
      .find((agent) => agent && agent.status !== "active")
    if (!found.roster && rosterChanged && paused) {
      found.roster = `${paused.name} is paused. Resume it or take it off the team before changing the roster.`
    }
    setErrors(found)
    setFormError("")
    if (!found.name && !found.description && !found.roster && !found.roles)
      save.mutate(fields)
  }

  return (
    <form
      className="workspace-agents-form"
      onSubmit={submit}
      noValidate
      aria-label={team ? `Edit ${team.name}` : "New multi-agent team"}
    >
      {!team && (
        <div className="workspace-agents-form-intro">
          <h3>New multi-agent team</h3>
          <p>
            Your primary agent coordinates the team; each specialist keeps its
            own instructions and takes the role you give it here.
          </p>
        </div>
      )}
      <label htmlFor={`${id}-name`}>Name</label>
      <Input
        id={`${id}-name`}
        value={name}
        onChange={(event) => {
          setName(event.target.value)
          if (errors.name) setErrors({ ...errors, name: undefined })
        }}
        aria-invalid={Boolean(errors.name) || undefined}
        aria-describedby={errors.name ? `${id}-name-error` : undefined}
        placeholder="e.g. Launch crew"
        autoComplete="off"
        autoFocus
      />
      {errors.name && (
        <p className="workspace-agents-field-error" id={`${id}-name-error`}>
          {errors.name}
        </p>
      )}
      <label htmlFor={`${id}-purpose`}>
        Purpose <span>optional</span>
      </label>
      <Textarea
        id={`${id}-purpose`}
        value={description}
        onChange={(event) => setDescription(event.target.value)}
        aria-invalid={Boolean(errors.description) || undefined}
        aria-describedby={
          errors.description ? `${id}-purpose-error` : undefined
        }
        placeholder="The outcome the team works toward"
        rows={2}
      />
      {errors.description && (
        <p className="workspace-agents-field-error" id={`${id}-purpose-error`}>
          {errors.description}
        </p>
      )}
      <fieldset
        className="workspace-agents-roster"
        aria-describedby={errors.roster ? `${id}-roster-error` : undefined}
      >
        <legend>Specialists</legend>
        {candidates.length === 0 ? (
          <p className="workspace-agents-muted">
            No active specialists to add. Create one in Specialists first.
          </p>
        ) : (
          candidates.map((agent) => {
            const seat = roster.find((entry) => entry.agentId === agent.id)
            const roleError = errors.roles?.[agent.id]
            return (
              <div
                className="workspace-agents-roster-row"
                data-selected={seat ? "true" : "false"}
                key={agent.id}
              >
                <label className="workspace-agents-roster-agent">
                  <input
                    type="checkbox"
                    checked={Boolean(seat)}
                    onChange={(event) => toggle(agent, event.target.checked)}
                  />
                  <AgentFace agent={agent} />
                  <span>
                    <strong>{agent.name}</strong>
                    <small>
                      {agent.status === "active"
                        ? agent.description || "Specialist"
                        : "Paused"}
                    </small>
                  </span>
                  {agent.status !== "active" && (
                    <StatusPill status={agent.status} />
                  )}
                </label>
                {seat && (
                  <>
                    <Input
                      aria-label={`${agent.name}'s role`}
                      value={seat.role}
                      onChange={(event) =>
                        setRole(agent.id, event.target.value)
                      }
                      aria-invalid={Boolean(roleError) || undefined}
                      aria-describedby={
                        roleError ? `${id}-${agent.id}-role-error` : undefined
                      }
                      placeholder="What it owns on this team"
                      autoComplete="off"
                    />
                    {roleError && (
                      <p
                        className="workspace-agents-field-error"
                        id={`${id}-${agent.id}-role-error`}
                      >
                        {roleError}
                      </p>
                    )}
                  </>
                )}
              </div>
            )
          })
        )}
        {errors.roster && (
          <p className="workspace-agents-field-error" id={`${id}-roster-error`}>
            {errors.roster}
          </p>
        )}
      </fieldset>
      {formError && (
        <p className="workspace-agents-error" role="alert">
          {formError}
        </p>
      )}
      <div className="workspace-agents-actions">
        <button
          className="workspace-agents-button"
          type="button"
          onClick={onCancel}
        >
          Cancel
        </button>
        <button
          className="workspace-agents-button"
          data-variant="primary"
          type="submit"
          disabled={save.isPending}
        >
          {team
            ? save.isPending
              ? "Saving…"
              : "Save changes"
            : save.isPending
              ? "Creating…"
              : "Create team"}
        </button>
      </div>
    </form>
  )
}

export function AgentTeamsSection({
  organizationId,
  onOpen,
}: {
  organizationId: string
  onOpen: (target: AgentsFocusTarget) => void
}) {
  const agents = useQuery(agentsQueryOptions(organizationId))
  const teams = useQuery(agentTeamsQueryOptions(organizationId))
  const [creating, setCreating] = useState(false)
  const specialists = agents.data ? specialistAgents(agents.data) : []
  const blocked = agents.data
    ? teamBlocker(primaryAgent(agents.data), specialists)
    : undefined
  const unavailable = isUnavailableHere(teams.error)

  return (
    <div className="workspace-agents-page">
      <div className="workspace-agents-toolbar">
        <span className="workspace-agents-count">
          {teams.data && plural(teams.data.length, "team")}
        </span>
        <button
          className="workspace-agents-button"
          data-variant="primary"
          type="button"
          aria-expanded={creating}
          aria-describedby={blocked ? "agents-team-blocked" : undefined}
          disabled={!agents.data || Boolean(blocked) || unavailable}
          onClick={() => setCreating((open) => !open)}
        >
          <Plus size={16} strokeWidth={1.8} />
          New team
        </button>
      </div>
      {blocked && !unavailable && (
        <p className="workspace-agents-note" id="agents-team-blocked">
          {blocked}
        </p>
      )}

      {creating && !blocked && (
        <AgentTeamForm
          organizationId={organizationId}
          specialists={specialists}
          onCancel={() => setCreating(false)}
          onDone={(created) => {
            setCreating(false)
            onOpen({ kind: "team", teamId: created.id })
          }}
        />
      )}

      {teams.isPending ? (
        <p className="workspace-agents-muted" role="status">
          Loading your teams…
        </p>
      ) : teams.isError ? (
        <AgentsNotice
          title={
            unavailable
              ? "Multi-agent teams aren't set up on this server."
              : "Could not load your teams."
          }
          alert={!unavailable}
          action={
            !unavailable && (
              <button
                className="workspace-agents-button"
                type="button"
                onClick={() => void teams.refetch()}
              >
                Try again
              </button>
            )
          }
        />
      ) : teams.data.length ? (
        <div className="workspace-entry-grid workspace-agents-grid">
          {teams.data.map((team) => (
            <AgentTeamEntry
              key={team.id}
              team={team}
              onOpen={() => onOpen({ kind: "team", teamId: team.id })}
            />
          ))}
        </div>
      ) : (
        !creating && (
          <AgentsNotice title="No multi-agent teams yet.">
            A team is your primary agent working with specialists you choose, in
            one shared conversation.
          </AgentsNotice>
        )
      )}
    </div>
  )
}
