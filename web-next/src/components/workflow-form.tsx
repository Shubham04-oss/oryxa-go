import { useId, useRef, useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Plus, Trash2 } from "lucide-react"
import { agentsErrorMessage, cleanLine, runeCount } from "./agents-format"
import { Input } from "./ui/input"
import { Textarea } from "./ui/textarea"
import { ApiError } from "@/lib/api/client"
import type { AgentTeam } from "@/lib/agents/agent-teams"
import {
  createAgentWorkflow,
  updateAgentWorkflow,
} from "@/lib/agents/agent-workflows"
import type {
  AgentWorkflow,
  CreateAgentWorkflowInput,
  WorkflowAssignment,
  WorkflowScheduleState,
  WorkflowStatus,
} from "@/lib/agents/agent-workflows"
import {
  afterCreateAgentWorkflow,
  afterUpdateAgentWorkflow,
} from "@/lib/agents/invalidation"

type Fields = Record<string, string>
type Assignment = WorkflowAssignment & { key: number }

export function WorkflowForm({
  organizationId,
  workflow,
  teams,
  blockedReason,
  onDone,
  onCancel,
}: {
  organizationId: string
  workflow?: AgentWorkflow
  teams: readonly AgentTeam[]
  blockedReason?: string
  onDone: (workflow: AgentWorkflow) => void
  onCancel: () => void
}) {
  const id = useId()
  const client = useQueryClient()
  const lock = useRef(false)
  const nextKey = useRef(workflow?.assignments.length ?? 1)
  const [name, setName] = useState(workflow?.name ?? "")
  const [description, setDescription] = useState(workflow?.description ?? "")
  const [teamId, setTeamId] = useState(workflow?.team_id ?? "")
  const [status, setStatus] = useState<WorkflowStatus>(
    workflow?.status ?? "active"
  )
  const [assignments, setAssignments] = useState<Assignment[]>(
    () =>
      workflow?.assignments.map((entry, key) => ({ ...entry, key })) ?? [
        { key: 0, agent_id: "", task: "", context: "" },
      ]
  )
  const [scheduled, setScheduled] = useState(Boolean(workflow?.schedule))
  const [expression, setExpression] = useState(
    workflow?.schedule?.expression ?? ""
  )
  const [timezone, setTimezone] = useState(
    workflow?.schedule?.timezone ?? "UTC"
  )
  const [scheduleState, setScheduleState] = useState<WorkflowScheduleState>(
    workflow?.schedule?.state ?? "enabled"
  )
  const [errors, setErrors] = useState<Fields>({})
  const [formError, setFormError] = useState("")
  const team = teams.find((entry) => entry.id === teamId)
  const save = useMutation({
    mutationFn: (input: CreateAgentWorkflowInput) =>
      workflow
        ? updateAgentWorkflow(organizationId, workflow.id, {
            name: input.name,
            description: input.description,
            team_id: input.team_id,
            assignments: input.assignments,
            status,
            ...(input.schedule
              ? { schedule: input.schedule }
              : workflow.schedule
                ? { clear_schedule: true as const }
                : {}),
          })
        : createAgentWorkflow(organizationId, input),
    onSuccess: async (saved) => {
      await (workflow
        ? afterUpdateAgentWorkflow(client, organizationId, saved)
        : afterCreateAgentWorkflow(client, organizationId, saved))
      onDone(saved)
    },
    onError: (error, input) => {
      if (error instanceof ApiError && error.code === "name_conflict")
        setErrors({
          name: `You already have a workflow called “${input.name}”.`,
        })
      else
        setFormError(
          agentsErrorMessage(error, "Could not save the workflow. Try again.", {
            not_found:
              "The workflow or selected team no longer exists. Refresh and choose an available team.",
            forbidden: "You can only use your own agent teams.",
            unavailable_here:
              "Workflow scheduling isn't available on this server. Your changes haven't been saved.",
          })
        )
    },
    onSettled: () => {
      lock.current = false
    },
  })

  function changeAssignment(key: number, change: Partial<WorkflowAssignment>) {
    setAssignments((current) =>
      current.map((entry) =>
        entry.key === key ? { ...entry, ...change } : entry
      )
    )
  }
  function fieldError(field: string) {
    return (
      errors[field] && (
        <p
          className="workspace-agents-field-error"
          id={`${id}-${field}-error`}
          role="alert"
        >
          {errors[field]}
        </p>
      )
    )
  }
  function fieldProps(field: string) {
    return {
      "aria-invalid": Boolean(errors[field]) || undefined,
      "aria-describedby": errors[field] ? `${id}-${field}-error` : undefined,
    }
  }
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (lock.current || blockedReason) return
    const fields: CreateAgentWorkflowInput = {
      name: cleanLine(name),
      description: description.trim(),
      team_id: teamId,
      assignments: assignments.map((entry) => ({
        agent_id: entry.agent_id,
        task: entry.task.trim(),
        context: entry.context?.trim() ?? "",
      })),
      ...(scheduled
        ? {
            schedule: {
              expression: expression.trim(),
              timezone: timezone.trim() || "UTC",
              state: scheduleState,
            },
          }
        : {}),
    }
    const found: Fields = {}
    if (!fields.name) found.name = "Give the workflow a name."
    else if (runeCount(fields.name) > 80)
      found.name = "Use at most 80 characters."
    if (runeCount(fields.description ?? "") > 1000)
      found.description = "Use at most 1,000 characters."
    if (!team) found.team = "Choose one of your available agent teams."
    if (!fields.assignments.length || fields.assignments.length > 8)
      found.assignments = "Choose between 1 and 8 assignments."
    const seen = new Set<string>()
    fields.assignments.forEach((entry, index) => {
      const key = assignments[index].key
      if (!team?.members.some((member) => member.agent_id === entry.agent_id))
        found[`agent-${key}`] = "Choose an agent on this team."
      else if (seen.has(entry.agent_id))
        found[`agent-${key}`] = "Each agent can have only one assignment."
      seen.add(entry.agent_id)
      if (!entry.task) found[`task-${key}`] = "Give this agent a task."
      else if (runeCount(entry.task) > 10000)
        found[`task-${key}`] = "Use at most 10,000 characters."
      if (runeCount(entry.context ?? "") > 10000)
        found[`context-${key}`] = "Use at most 10,000 characters."
    })
    if (fields.schedule) {
      if (
        !/^(rate|cron|at)\(.+\)$/.test(fields.schedule.expression) ||
        runeCount(fields.schedule.expression) > 256
      )
        found.expression =
          "Use rate(...), cron(...) or at(...), up to 256 characters."
      try {
        new Intl.DateTimeFormat("en", {
          timeZone: fields.schedule.timezone,
        }).format()
      } catch {
        found.timezone = "Use an IANA timezone, such as UTC or Asia/Kolkata."
      }
    }
    setErrors(found)
    setFormError("")
    if (Object.keys(found).length) return
    lock.current = true
    save.mutate(fields)
  }

  return (
    <form
      className="workspace-agents-form workflow-form"
      aria-label={workflow ? `Edit ${workflow.name}` : "New workflow"}
      onSubmit={submit}
      noValidate
    >
      <div className="workspace-agents-form-intro">
        <h3>{workflow ? `Edit ${workflow.name}` : "New workflow"}</h3>
        <p>
          Assign fixed tasks to your team's agents. Each agent takes one
          assignment.
        </p>
      </div>
      <fieldset disabled={save.isPending} className="workflow-form-fields">
        <label htmlFor={`${id}-name`}>Name</label>
        <Input
          id={`${id}-name`}
          value={name}
          onChange={(event) => setName(event.target.value)}
          {...fieldProps("name")}
          autoComplete="off"
          autoFocus
        />
        {fieldError("name")}
        <label htmlFor={`${id}-description`}>
          Description <span>optional</span>
        </label>
        <Textarea
          id={`${id}-description`}
          value={description}
          onChange={(event) => setDescription(event.target.value)}
          rows={2}
          {...fieldProps("description")}
        />
        {fieldError("description")}
        <label htmlFor={`${id}-team`}>Agent team</label>
        <select
          id={`${id}-team`}
          value={teamId}
          {...fieldProps("team")}
          onChange={(event) => {
            setTeamId(event.target.value)
            setAssignments([
              { key: nextKey.current++, agent_id: "", task: "", context: "" },
            ])
            setErrors({})
          }}
        >
          <option value="">Choose a team</option>
          {!team && teamId && (
            <option value={teamId} disabled>
              Team no longer available
            </option>
          )}
          {teams.map((entry) => (
            <option key={entry.id} value={entry.id}>
              {entry.name}
              {entry.status === "disabled" ? " (paused)" : ""}
            </option>
          ))}
        </select>
        {fieldError("team")}
        {team?.status === "disabled" && (
          <p className="workspace-agents-note">
            You can prepare this workflow while the team is paused. Resume the
            team before running it.
          </p>
        )}
        <fieldset className="workflow-assignment-fields">
          <legend>Assignments</legend>
          {assignments.map((entry, index) => (
            <div className="workflow-assignment" key={entry.key}>
              <div className="workflow-detail-heading">
                <strong>Assignment {index + 1}</strong>
                <button
                  className="workspace-agents-button"
                  type="button"
                  aria-label={`Remove assignment ${index + 1}`}
                  disabled={assignments.length === 1}
                  onClick={() =>
                    setAssignments((current) =>
                      current.filter((item) => item.key !== entry.key)
                    )
                  }
                >
                  <Trash2 size={14} />
                </button>
              </div>
              <label htmlFor={`${id}-agent-${entry.key}`}>
                Agent for assignment {index + 1}
              </label>
              <select
                id={`${id}-agent-${entry.key}`}
                value={entry.agent_id}
                {...fieldProps(`agent-${entry.key}`)}
                onChange={(event) =>
                  changeAssignment(entry.key, { agent_id: event.target.value })
                }
              >
                <option value="">Choose an agent</option>
                {entry.agent_id &&
                  !team?.members.some(
                    (member) => member.agent_id === entry.agent_id
                  ) && (
                    <option value={entry.agent_id} disabled>
                      Agent no longer on this team
                    </option>
                  )}
                {team?.members.map((member) => (
                  <option
                    key={member.agent_id}
                    value={member.agent_id}
                    disabled={assignments.some(
                      (item) =>
                        item.key !== entry.key &&
                        item.agent_id === member.agent_id
                    )}
                  >
                    {member.name}
                    {member.status === "disabled" ? " (paused)" : ""}
                  </option>
                ))}
              </select>
              {fieldError(`agent-${entry.key}`)}
              <label htmlFor={`${id}-task-${entry.key}`}>
                Task for assignment {index + 1}
              </label>
              <Textarea
                id={`${id}-task-${entry.key}`}
                rows={2}
                value={entry.task}
                onChange={(event) =>
                  changeAssignment(entry.key, { task: event.target.value })
                }
                {...fieldProps(`task-${entry.key}`)}
              />
              {fieldError(`task-${entry.key}`)}
              <label htmlFor={`${id}-context-${entry.key}`}>
                Context for assignment {index + 1} <span>optional</span>
              </label>
              <Textarea
                id={`${id}-context-${entry.key}`}
                rows={2}
                value={entry.context ?? ""}
                onChange={(event) =>
                  changeAssignment(entry.key, { context: event.target.value })
                }
                {...fieldProps(`context-${entry.key}`)}
              />
              {fieldError(`context-${entry.key}`)}
            </div>
          ))}
          {fieldError("assignments")}
          <button
            className="workspace-agents-button"
            type="button"
            disabled={
              !team || assignments.length >= Math.min(8, team.members.length)
            }
            onClick={() =>
              setAssignments((current) => [
                ...current,
                { key: nextKey.current++, agent_id: "", task: "", context: "" },
              ])
            }
          >
            <Plus size={14} /> Add assignment
          </button>
        </fieldset>
        {workflow && (
          <>
            <label htmlFor={`${id}-status`}>Workflow status</label>
            <select
              id={`${id}-status`}
              value={status}
              onChange={(event) =>
                setStatus(event.target.value as WorkflowStatus)
              }
            >
              <option value="active">Active</option>
              <option value="disabled">Paused</option>
            </select>
          </>
        )}
        <label className="workflow-checkbox">
          <input
            type="checkbox"
            checked={scheduled}
            onChange={(event) => setScheduled(event.target.checked)}
          />{" "}
          Run on a schedule
        </label>
        {scheduled && (
          <div className="workflow-schedule-fields">
            <label htmlFor={`${id}-expression`}>Schedule expression</label>
            <Input
              id={`${id}-expression`}
              value={expression}
              onChange={(event) => setExpression(event.target.value)}
              placeholder="rate(7 days)"
              {...fieldProps("expression")}
            />
            {fieldError("expression")}
            <label htmlFor={`${id}-timezone`}>Timezone</label>
            <Input
              id={`${id}-timezone`}
              value={timezone}
              onChange={(event) => setTimezone(event.target.value)}
              {...fieldProps("timezone")}
            />
            {fieldError("timezone")}
            <label htmlFor={`${id}-schedule-state`}>Schedule status</label>
            <select
              id={`${id}-schedule-state`}
              value={scheduleState}
              onChange={(event) =>
                setScheduleState(event.target.value as WorkflowScheduleState)
              }
            >
              <option value="enabled">Enabled</option>
              <option value="disabled">Disabled</option>
            </select>
          </div>
        )}
        {!scheduled && workflow?.schedule && (
          <p className="workspace-agents-note">
            Saving removes the current schedule.
          </p>
        )}
      </fieldset>
      {formError && (
        <p className="workspace-agents-error" role="alert">
          {formError}
        </p>
      )}
      {blockedReason && (
        <p className="workspace-agents-note" role="status" id={`${id}-blocked`}>
          {blockedReason}
        </p>
      )}
      <div className="workspace-agents-actions">
        <button
          className="workspace-agents-button"
          type="button"
          onClick={onCancel}
          disabled={save.isPending}
        >
          Cancel
        </button>
        <button
          className="workspace-agents-button"
          data-variant="primary"
          type="submit"
          disabled={save.isPending || Boolean(blockedReason)}
          aria-describedby={blockedReason ? `${id}-blocked` : undefined}
        >
          {save.isPending
            ? "Saving…"
            : workflow
              ? "Save changes"
              : "Create workflow"}
        </button>
      </div>
    </form>
  )
}
