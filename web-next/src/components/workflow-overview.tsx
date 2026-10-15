import { useId, useRef, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ChevronDown, GitBranch, Play, Plus } from "lucide-react"
import { agentsErrorMessage, formatDate, plural } from "./agents-format"
import { AgentsNotice, ConfirmStep, StatusPill } from "./agents-parts"
import { WorkflowForm } from "./workflow-form"
import {
  executionLabel,
  executionStatus,
  WorkflowExecutionDetail,
  WorkflowQueryNotice,
} from "./workflow-execution"
import type { AgentTeam } from "@/lib/agents/agent-teams"
import type { AgentWorkflow } from "@/lib/agents/agent-workflows"
import {
  deleteAgentWorkflow,
  startAgentWorkflow,
} from "@/lib/agents/agent-workflows"
import {
  afterDeleteAgentWorkflow,
  afterStartAgentWorkflow,
} from "@/lib/agents/invalidation"
import { hasExecutionInFlight, isUnavailableHere } from "@/lib/agents/policy"
import {
  agentTeamsQueryOptions,
  agentWorkflowQueryOptions,
  agentWorkflowsQueryOptions,
  workflowExecutionsQueryOptions,
} from "@/lib/agents/queries"
import "./workflow-overview.css"
import { Button } from "@/components/ui/button"
import { ApiError } from "@/lib/api/client"

function workflowRefusal(error: unknown) {
  return (
    isUnavailableHere(error) ||
    (error instanceof ApiError && [401, 403, 404].includes(error.status))
  )
}

function scheduleLabel(workflow: AgentWorkflow) {
  if (!workflow.schedule) return "Runs when started"
  const when = `${workflow.schedule.expression}, ${workflow.schedule.timezone}`
  return workflow.schedule.state === "enabled" ? when : `Schedule off (${when})`
}

/** Why a workflow can't run now, as the server would refuse it. */
function runBlocker(
  workflow: AgentWorkflow,
  team: AgentTeam | undefined
): string | undefined {
  const pausedAgent = team?.members.find(
    (member) =>
      member.status !== "active" &&
      workflow.assignments.some((entry) => entry.agent_id === member.agent_id)
  )
  return workflow.status !== "active"
    ? "This workflow is paused. Edit it to make it active before running."
    : team?.status === "disabled"
      ? "The team is paused. Resume it in Multi-agent teams before running."
      : pausedAgent
        ? `${pausedAgent.name} is paused. Resume it in Specialists before running.`
        : undefined
}

function WorkflowDetail({
  organizationId,
  workflowId,
  teams,
  refreshBlocked,
  onDeleted,
  onBusy,
}: {
  organizationId: string
  workflowId: string
  teams: readonly AgentTeam[]
  refreshBlocked: boolean
  onDeleted: () => void
  onBusy: (busy: boolean) => void
}) {
  const client = useQueryClient()
  const workflowQuery = useQuery(
    agentWorkflowQueryOptions(organizationId, workflowId)
  )
  const executions = useQuery(
    workflowExecutionsQueryOptions(organizationId, workflowId)
  )
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [executionId, setExecutionId] = useState<string>()
  const runLock = useRef(false)
  const deleteLock = useRef(false)
  const run = useMutation({
    mutationFn: () => startAgentWorkflow(organizationId, workflowId),
    onSuccess: async (execution) => {
      await afterStartAgentWorkflow(client, organizationId, execution)
      setExecutionId(execution.id)
    },
    // Starting can record a failed execution before returning an error.
    onError: () => {
      void executions.refetch()
    },
    onSettled: () => {
      runLock.current = false
      onBusy(false)
    },
  })
  const remove = useMutation({
    mutationFn: () => deleteAgentWorkflow(organizationId, workflowId),
    onSuccess: async () => {
      await afterDeleteAgentWorkflow(client, organizationId, workflowId)
      onDeleted()
    },
    onSettled: () => {
      deleteLock.current = false
      onBusy(false)
    },
  })
  const workflow = workflowQuery.data
  if (workflowQuery.isPending)
    return (
      <p className="workspace-agents-muted" role="status">
        Loading workflow…
      </p>
    )
  if (
    workflowQuery.isError &&
    (!workflow || workflowRefusal(workflowQuery.error))
  )
    return (
      <div>
        <WorkflowQueryNotice
          error={workflowQuery.error}
          fallback="Could not load this workflow."
          onRetry={() => void workflowQuery.refetch()}
        />
        {editing && (
          <button
            className="workspace-agents-button"
            type="button"
            onClick={() => {
              setEditing(false)
              onBusy(false)
            }}
          >
            Cancel editing
          </button>
        )}
      </div>
    )
  if (!workflow) return null
  const team = teams.find((entry) => entry.id === workflow.team_id)
  const blocked = runBlocker(workflow, team)
  const refreshWarning = workflowQuery.isError && (
    <WorkflowQueryNotice
      error={workflowQuery.error}
      fallback={
        editing
          ? "Could not refresh this workflow. Your draft is kept."
          : "Could not refresh this workflow. Try again before changing it."
      }
      onRetry={() => void workflowQuery.refetch()}
    />
  )
  if (editing)
    return (
      <div className="workflow-editor">
        {refreshWarning}
        <WorkflowForm
          organizationId={organizationId}
          workflow={workflow}
          teams={teams}
          blockedReason={
            workflowQuery.isError || refreshBlocked
              ? "Refresh this workflow before saving changes. Your draft is kept."
              : undefined
          }
          onCancel={() => {
            setEditing(false)
            onBusy(false)
          }}
          onDone={() => {
            setEditing(false)
            onBusy(false)
          }}
        />
      </div>
    )
  return (
    <div className="workflow-detail" aria-label={`${workflow.name} details`}>
      {refreshWarning}
      <div className="workspace-agents-actions" data-align="start">
        <button
          className="workspace-agents-button"
          type="button"
          disabled={
            run.isPending ||
            remove.isPending ||
            deleting ||
            workflowQuery.isError ||
            refreshBlocked ||
            Boolean(blocked) ||
            executions.isError ||
            !executions.data ||
            hasExecutionInFlight(executions.data)
          }
          aria-describedby={
            blocked ? `workflow-${workflowId}-blocked` : undefined
          }
          onClick={() => {
            if (
              runLock.current ||
              workflowQuery.isError ||
              refreshBlocked ||
              executions.isError ||
              !executions.data ||
              hasExecutionInFlight(executions.data)
            )
              return
            runLock.current = true
            onBusy(true)
            run.mutate()
          }}
        >
          <Play size={14} />
          {run.isPending ? "Starting…" : "Run now"}
        </button>
        <button
          className="workspace-agents-button"
          type="button"
          disabled={
            run.isPending ||
            remove.isPending ||
            deleting ||
            workflowQuery.isError ||
            refreshBlocked ||
            !teams.length
          }
          onClick={() => {
            setEditing(true)
            onBusy(true)
            run.reset()
          }}
        >
          Edit workflow
        </button>
        <button
          className="workspace-agents-button"
          data-variant="quiet-danger"
          type="button"
          disabled={
            run.isPending ||
            remove.isPending ||
            deleting ||
            workflowQuery.isError ||
            refreshBlocked
          }
          onClick={() => {
            setDeleting(true)
            run.reset()
            remove.reset()
          }}
        >
          Delete workflow
        </button>
      </div>
      {blocked && (
        <p
          className="workspace-agents-note"
          id={`workflow-${workflowId}-blocked`}
        >
          {blocked}
        </p>
      )}
      {!team && (
        <p className="workspace-agents-note">
          This team isn't available in the current list. Choose a different
          available team when editing.
        </p>
      )}
      {hasExecutionInFlight(executions.data) && (
        <p className="workspace-agents-note">
          An execution is already queued or running.
        </p>
      )}
      {run.isError && (
        <p className="workspace-agents-error" role="alert">
          {agentsErrorMessage(
            run.error,
            "Could not start the workflow. Check the execution history before trying again.",
            {
              workflow_disabled:
                "This workflow is paused. Refresh it and make it active before running.",
              workflow_in_progress:
                "This workflow already has work in progress.",
              forbidden: "You can't run this workflow.",
              unavailable_here:
                "Running workflows isn't available on this server.",
              not_found: "This workflow no longer exists.",
            }
          )}
        </p>
      )}
      {deleting && (
        <ConfirmStep
          question={`Delete “${workflow.name}”?`}
          detail="This removes the workflow, its schedule and recorded executions."
          confirmLabel={`Delete ${workflow.name}`}
          pendingLabel="Deleting…"
          pending={remove.isPending}
          blocked={
            workflowQuery.isError || refreshBlocked
              ? "Refresh this workflow before deleting it."
              : undefined
          }
          error={
            remove.isError
              ? agentsErrorMessage(
                  remove.error,
                  "Could not delete the workflow. Try again.",
                  {
                    unavailable_here:
                      "The workflow schedule can't be removed on this server. The workflow hasn't been deleted.",
                  }
                )
              : undefined
          }
          onCancel={() => {
            if (!remove.isPending) setDeleting(false)
          }}
          onConfirm={() => {
            if (deleteLock.current || workflowQuery.isError || refreshBlocked)
              return
            deleteLock.current = true
            onBusy(true)
            remove.mutate()
          }}
        />
      )}
      {workflow.description && (
        <p className="workflow-description">{workflow.description}</p>
      )}
      <div className="workflow-detail-heading">
        <h4>Assignments</h4>
        <StatusPill status={workflow.status} />
      </div>
      <ul className="workflow-assignment-list">
        {workflow.assignments.map((assignment) => (
          <li key={assignment.agent_id}>
            <strong>
              {team?.members.find(
                (member) => member.agent_id === assignment.agent_id
              )?.name ?? "Assigned agent"}
            </strong>
            <p>{assignment.task}</p>
            {assignment.context && (
              <details>
                <summary>Context</summary>
                <p>{assignment.context}</p>
              </details>
            )}
          </li>
        ))}
      </ul>
      <p className="workspace-agents-note">
        {scheduleLabel(workflow)} · Updated {formatDate(workflow.updated_at)}
      </p>
      <p className="workspace-agents-note">
        Workflow runs keep normal access and approval rules. Actions that need
        your approval wait for it.
      </p>
      <div className="workflow-history">
        <div className="workflow-detail-heading">
          <h4>Executions</h4>
          <button
            className="workspace-agents-button"
            type="button"
            disabled={executions.isFetching}
            onClick={() => void executions.refetch()}
          >
            {executions.isFetching ? "Refreshing…" : "Refresh"}
          </button>
        </div>
        {executionId ? (
          <WorkflowExecutionDetail
            organizationId={organizationId}
            executionId={executionId}
            onBack={() => setExecutionId(undefined)}
          />
        ) : executions.isPending ? (
          <p className="workspace-agents-muted" role="status">
            Loading executions…
          </p>
        ) : executions.isError ? (
          <WorkflowQueryNotice
            error={executions.error}
            fallback="Could not load executions."
            onRetry={() => void executions.refetch()}
          />
        ) : executions.data.length ? (
          <>
            <ol className="workflow-history-list">
              {executions.data.map((execution) => (
                <li key={execution.id}>
                  <button
                    type="button"
                    onClick={() => setExecutionId(execution.id)}
                    aria-label={`View ${executionLabel(execution)} execution`}
                  >
                    <span
                      className="workflow-execution-status"
                      data-status={executionStatus(execution)}
                    >
                      {executionLabel(execution)}
                    </span>
                    <small>
                      {execution.started_by === "schedule"
                        ? "Schedule"
                        : "Run now"}{" "}
                      · {formatDate(execution.created_at)}
                    </small>
                  </button>
                </li>
              ))}
            </ol>
            {executions.data.length >= 20 && (
              <p className="workspace-agents-note">
                Showing the latest 20 executions.
              </p>
            )}
          </>
        ) : (
          <p className="workspace-agents-muted">No executions yet.</p>
        )}
      </div>
    </div>
  )
}

function WorkflowEntry({
  organizationId,
  workflow,
  teams,
  initiallyOpen,
  refreshBlocked,
  onDeleted,
}: {
  organizationId: string
  workflow: AgentWorkflow
  teams: readonly AgentTeam[]
  initiallyOpen: boolean
  refreshBlocked: boolean
  onDeleted: () => void
}) {
  const id = useId()
  const [open, setOpen] = useState(initiallyOpen)
  const [busy, setBusy] = useState(false)
  const executions = useQuery(
    workflowExecutionsQueryOptions(organizationId, workflow.id, 5)
  )
  const latest = executions.data?.[0]
  const team = teams.find((entry) => entry.id === workflow.team_id)
  const client = useQueryClient()
  const runLock = useRef(false)
  // The row's own Run now (Start, for one that runs only when started), as
  // the board has it; its execution shows once the row opens.
  const run = useMutation({
    mutationFn: () => startAgentWorkflow(organizationId, workflow.id),
    onSuccess: async (execution) => {
      await afterStartAgentWorkflow(client, organizationId, execution)
      setOpen(true)
    },
    onError: () => {
      void executions.refetch()
    },
    onSettled: () => {
      runLock.current = false
    },
  })
  const blocked = runBlocker(workflow, team)
  const cannotRun =
    busy ||
    run.isPending ||
    refreshBlocked ||
    Boolean(blocked) ||
    !executions.data ||
    hasExecutionInFlight(executions.data)
  const runLabel = workflow.schedule ? "Run now" : "Start"
  return (
    <li
      className="workflow-entry"
      data-screen-context-kind="workflow"
      data-screen-context-id={workflow.id}
      data-screen-context-name={workflow.name}
    >
      <button
        className="workflow-entry-toggle"
        type="button"
        aria-label={`${open ? "Close" : "Open"} ${workflow.name} workflow`}
        aria-expanded={open}
        aria-controls={id}
        disabled={busy}
        onClick={() => setOpen(!open)}
      >
        <GitBranch size={16} aria-hidden="true" />
        <span className="workflow-entry-copy">
          <strong>{workflow.name}</strong>
          <small>
            {[
              team?.name,
              scheduleLabel(workflow),
              workflow.status === "disabled" ? "Paused" : undefined,
            ]
              .filter(Boolean)
              .join(" · ")}
          </small>
        </span>
        <span
          className="workflow-entry-latest"
          data-status={latest && executionStatus(latest)}
        >
          {executions.isPending
            ? "Reading runs…"
            : executions.isError
              ? "Runs unavailable"
              : latest
                ? executionLabel(latest)
                : "Not run yet"}
        </span>
        <ChevronDown
          size={15}
          className="workflow-chevron"
          aria-hidden="true"
        />
      </button>
      <Button
        className="workflow-entry-run"
        variant="outline"
        size="sm"
        aria-label={`${runLabel} ${workflow.name}`}
        title={blocked}
        disabled={cannotRun}
        onClick={() => {
          if (runLock.current || cannotRun) return
          runLock.current = true
          run.mutate()
        }}
      >
        <Play aria-hidden="true" />
        {run.isPending ? "Starting…" : runLabel}
      </Button>
      {run.isError && (
        <p className="workspace-agents-error workflow-entry-error" role="alert">
          {agentsErrorMessage(run.error, "Could not start it.")}
        </p>
      )}
      {open && (
        <div id={id} className="workflow-entry-body">
          <WorkflowDetail
            organizationId={organizationId}
            workflowId={workflow.id}
            teams={teams}
            refreshBlocked={refreshBlocked}
            onDeleted={onDeleted}
            onBusy={setBusy}
          />
        </div>
      )}
    </li>
  )
}

export function WorkflowOverview({
  organizationId,
}: {
  organizationId: string
}) {
  const workflows = useQuery(agentWorkflowsQueryOptions(organizationId))
  const teams = useQuery(agentTeamsQueryOptions(organizationId))
  const [creating, setCreating] = useState(false)
  const [createdId, setCreatedId] = useState<string>()
  const unavailable = isUnavailableHere(workflows.error)
  const fatalWorkflows = workflows.isError && workflowRefusal(workflows.error)
  const visibleWorkflows = fatalWorkflows ? undefined : workflows.data
  const refreshBlocked = workflows.isError || teams.isError
  return (
    <section
      className="workflow-overview"
      aria-labelledby="agents-overview-workflows"
    >
      <div className="workflow-overview-toolbar">
        <h2 id="agents-overview-workflows">
          Workflows
          {visibleWorkflows?.length ? ` · ${visibleWorkflows.length}` : ""}
        </h2>
        <Button
          variant="outline"
          size="sm"
          aria-expanded={creating}
          disabled={
            creating ||
            !workflows.data ||
            !teams.data?.length ||
            unavailable ||
            refreshBlocked
          }
          onClick={() => setCreating(true)}
        >
          <Plus aria-hidden="true" /> New workflow
        </Button>
      </div>
      {creating && teams.data && !fatalWorkflows && (
        <WorkflowForm
          organizationId={organizationId}
          teams={teams.data}
          blockedReason={
            refreshBlocked
              ? "Refresh workflows and teams before saving. Your draft is kept."
              : undefined
          }
          onCancel={() => setCreating(false)}
          onDone={(saved) => {
            setCreatedId(saved.id)
            setCreating(false)
          }}
        />
      )}
      {teams.isPending ? (
        <p className="workspace-agents-muted" role="status">
          Loading workflow teams…
        </p>
      ) : teams.isError ? (
        <AgentsNotice
          title={
            isUnavailableHere(teams.error)
              ? "Multi-agent teams aren't set up for workflows here."
              : "Could not load workflow teams."
          }
          alert={!isUnavailableHere(teams.error)}
          action={
            !isUnavailableHere(teams.error) && (
              <button
                className="workspace-agents-button"
                type="button"
                onClick={() => void teams.refetch()}
              >
                Retry workflow teams
              </button>
            )
          }
        />
      ) : (
        !teams.data.length && (
          <p className="workspace-agents-note">
            Create a multi-agent team before creating a workflow.
          </p>
        )
      )}
      {workflows.isError && (
        <WorkflowQueryNotice
          error={workflows.error}
          fallback="Could not load your workflows."
          onRetry={() => void workflows.refetch()}
          optional
        />
      )}
      {workflows.isPending ? (
        <p className="workspace-agents-muted" role="status">
          Loading your workflows…
        </p>
      ) : visibleWorkflows?.length ? (
        <>
          <ul className="workflow-list">
            {visibleWorkflows.map((workflow) => (
              <WorkflowEntry
                key={workflow.id}
                organizationId={organizationId}
                workflow={workflow}
                teams={teams.data ?? []}
                initiallyOpen={workflow.id === createdId}
                refreshBlocked={refreshBlocked}
                onDeleted={() => setCreatedId(undefined)}
              />
            ))}
          </ul>
          <p className="workspace-agents-count">
            {plural(visibleWorkflows.length, "workflow")}
          </p>
        </>
      ) : visibleWorkflows ? (
        <p className="workspace-agents-muted">
          No workflows yet. A workflow hands fixed tasks to one of your teams,
          when started or on a schedule.
        </p>
      ) : null}
    </section>
  )
}
