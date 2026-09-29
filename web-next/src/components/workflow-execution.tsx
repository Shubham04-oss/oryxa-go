import { useQuery } from "@tanstack/react-query"
import { ArrowLeft } from "lucide-react"
import {
  agentsErrorMessage,
  formatDate,
  formatTime,
  plural,
  timeAgo,
} from "./agents-format"
import { AgentsNotice } from "./agents-parts"
import type {
  WorkflowExecution,
  WorkflowExecutionStatus,
} from "@/lib/agents/agent-workflows"
import { workflowExecutionQueryOptions } from "@/lib/agents/queries"
import { ApiError } from "@/lib/api/client"
import { isUnavailableHere } from "@/lib/agents/policy"

export const executionWords: Record<WorkflowExecutionStatus, string> = {
  queued: "Queued",
  running: "Running now",
  succeeded: "Succeeded",
  needs_confirmation: "Waiting for your approval",
  failed: "Failed",
}

/** An incomplete assignment is never presented as a successful execution. */
export function executionStatus(
  execution: WorkflowExecution
): WorkflowExecutionStatus {
  if (execution.status === "queued" || execution.status === "running")
    return execution.status
  const results = execution.result?.results
  if (results?.some((result) => result.status === "needs_confirmation"))
    return "needs_confirmation"
  if (results?.some((result) => result.status !== "completed")) return "failed"
  return execution.status
}

export function executionLabel(execution: WorkflowExecution): string {
  const status = executionStatus(execution)
  if (status === "queued" || status === "running") return executionWords[status]
  return `${executionWords[status]} ${timeAgo(execution.completed_at ?? execution.started_at ?? execution.created_at)}`
}

export function WorkflowQueryNotice({
  error,
  fallback,
  onRetry,
  optional = false,
}: {
  error: unknown
  fallback: string
  onRetry: () => void
  optional?: boolean
}) {
  const unavailable =
    isUnavailableHere(error) ||
    (optional && error instanceof ApiError && error.status === 404)
  return (
    <AgentsNotice
      title={
        unavailable
          ? "Workflows aren't set up on this server."
          : agentsErrorMessage(error, fallback, {
              forbidden: "You can’t access this workflow or execution.",
            })
      }
      alert={!unavailable}
      action={
        !unavailable && (
          <button
            className="workspace-agents-button"
            type="button"
            onClick={onRetry}
          >
            Try again
          </button>
        )
      }
    />
  )
}

/** Execution errors may contain provider diagnostics; show only actionable, known refusals. */
function failureMessage(error?: string): string {
  if (
    error ===
    "the member has not allowed agents to act for them while they are away"
  )
    return "Background access isn't allowed. Review agent access in Settings."
  if (error === "agent team is disabled")
    return "The team is paused. Resume it before running again."
  if (error === "one or more workflow assignments did not complete")
    return "One or more assignments did not complete. Review their results below."
  return "This work could not finish. Review the team's agents and access, then try again."
}

const assignmentWords = {
  completed: "Completed",
  needs_input: "Needs your input",
  blocked: "Blocked",
  needs_confirmation: "Waiting for your approval",
  failed: "Failed",
} as const

export function WorkflowExecutionDetail({
  organizationId,
  executionId,
  onBack,
}: {
  organizationId: string
  executionId: string
  onBack: () => void
}) {
  const query = useQuery(
    workflowExecutionQueryOptions(organizationId, executionId)
  )
  const execution = query.data
  const status = execution && executionStatus(execution)
  return (
    <section
      className="workflow-execution-detail"
      aria-label="Execution detail"
      aria-live="polite"
    >
      <button
        className="workspace-agents-button"
        type="button"
        onClick={onBack}
      >
        <ArrowLeft size={14} /> All executions
      </button>
      {query.isPending ? (
        <p className="workspace-agents-muted" role="status">
          Loading execution…
        </p>
      ) : query.isError ? (
        <WorkflowQueryNotice
          error={query.error}
          fallback="Could not load this execution."
          onRetry={() => void query.refetch()}
        />
      ) : (
        execution && (
          <>
            <div className="workflow-detail-heading">
              <h4>Execution</h4>
              <span className="workflow-execution-status" data-status={status}>
                {executionWords[status!]}
              </span>
            </div>
            <dl className="workflow-execution-times">
              <div>
                <dt>Started by</dt>
                <dd>
                  {execution.started_by === "schedule" ? "Schedule" : "Run now"}
                </dd>
              </div>
              <div>
                <dt>Queued</dt>
                <dd>
                  {formatDate(execution.created_at)} ·{" "}
                  {formatTime(execution.created_at)}
                </dd>
              </div>
              {execution.started_at && (
                <div>
                  <dt>Started</dt>
                  <dd>
                    {formatDate(execution.started_at)} ·{" "}
                    {formatTime(execution.started_at)}
                  </dd>
                </div>
              )}
              {execution.completed_at && (
                <div>
                  <dt>Finished</dt>
                  <dd>
                    {formatDate(execution.completed_at)} ·{" "}
                    {formatTime(execution.completed_at)}
                  </dd>
                </div>
              )}
            </dl>
            {status === "queued" && (
              <p className="workspace-agents-note">
                Queued. The assignments haven't finished yet.
              </p>
            )}
            {status === "running" && (
              <p className="workspace-agents-note">
                Assignments are running. This view updates while they work.
              </p>
            )}
            {status === "needs_confirmation" && (
              <p className="workspace-agents-note">
                This assignment needs your approval. It cannot be resumed from
                this view yet.
              </p>
            )}
            {status === "failed" && (
              <p className="workspace-agents-error">
                {failureMessage(execution.error)}
              </p>
            )}
            {execution.result?.results.length ? (
              <ul className="workflow-result-list">
                {execution.result.results.map((result, index) => (
                  <li key={`${result.agent_id}-${index}`}>
                    <div className="workflow-detail-heading">
                      <strong>{result.agent_name || "Assigned agent"}</strong>
                      <span
                        className="workflow-execution-status"
                        data-status={
                          result.status === "completed"
                            ? "succeeded"
                            : result.status
                        }
                      >
                        {assignmentWords[result.status] ?? "Incomplete"}
                      </span>
                    </div>
                    {result.output && (
                      <p className="workflow-result-output">{result.output}</p>
                    )}
                    {result.status === "failed" && (
                      <p className="workspace-agents-error">
                        {failureMessage(result.error)}
                      </p>
                    )}
                    {result.status === "needs_confirmation" && (
                      <p className="workspace-agents-note">
                        {result.pending_confirmations?.length
                          ? plural(
                              result.pending_confirmations.length,
                              "action needs",
                              "actions need"
                            )
                          : "An action needs"}{" "}
                        your approval. It hasn't completed.
                      </p>
                    )}
                  </li>
                ))}
              </ul>
            ) : (
              <p className="workspace-agents-muted">
                No assignment results recorded yet.
              </p>
            )}
          </>
        )
      )}
    </section>
  )
}
