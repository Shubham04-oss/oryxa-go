// Durable workflows that hand fixed assignments to one of the member's agent
// teams, run now or on an EventBridge schedule, and the ledger of their
// executions. Handlers are in agents/primary/internal/httpserver/server.go;
// the rules are in agents/primary/internal/agentworkflows.
import { apiRequest } from "@/lib/api/client"
import { organizationPath, pick, withSearch } from "./http"
import type { ReadOptions } from "./http"

export type WorkflowStatus = "active" | "disabled"
export type WorkflowScheduleState = "enabled" | "disabled"

export type WorkflowSchedule = {
  /** `rate(…)`, `cron(…)` or `at(…)`, as EventBridge Scheduler reads them. */
  expression: string
  /** An IANA time zone. */
  timezone: string
  state: WorkflowScheduleState
}

/** The server fills in `timezone` ("UTC") and `state` ("enabled") when left out. */
export type WorkflowScheduleInput = {
  expression: string
  timezone?: string
  state?: WorkflowScheduleState
}

/** One task for one agent on the workflow's team; each agent at most once. */
export type WorkflowAssignment = {
  agent_id: string
  /** Required; up to 10,000 characters. */
  task: string
  /** Up to 10,000 characters. */
  context?: string
}

export type AgentWorkflow = {
  id: string
  organization_id: string
  owner_user_id: string
  name: string
  description?: string
  team_id: string
  status: WorkflowStatus
  assignments: WorkflowAssignment[]
  /** Absent for a workflow that only runs when started. */
  schedule?: WorkflowSchedule
  revision: number
  created_at: string
  updated_at: string
}

export type CreateAgentWorkflowInput = {
  /** 1–80 characters, unique among the caller's workflows. */
  name: string
  /** Up to 1,000 characters. */
  description?: string
  /** One of the caller's agent teams. */
  team_id: string
  /** 1–8 assignments, each for an agent on that team. */
  assignments: WorkflowAssignment[]
  schedule?: WorkflowScheduleInput
}

type UpdateAgentWorkflowFields = {
  name?: string
  description?: string
  team_id?: string
  status?: WorkflowStatus
  /** Replaces every assignment. */
  assignments?: WorkflowAssignment[]
}

/**
 * Send at least one field. The server refuses `schedule` together with
 * `clear_schedule`, so the type does too.
 */
export type UpdateAgentWorkflowInput = UpdateAgentWorkflowFields &
  (
    | { schedule?: WorkflowScheduleInput; clear_schedule?: false }
    | { schedule?: never; clear_schedule: true }
  )

export type WorkflowExecutionStatus =
  "queued" | "running" | "succeeded" | "needs_confirmation" | "failed"

/** What one assignment came to (`agents.SystemDelegationResult`). */
export type WorkflowDelegationResult = {
  agent_id: string
  agent_name: string
  /** The agent's thread for this assignment: open it to answer a confirmation. */
  thread_id?: string
  run_id?: string
  status: "completed" | "needs_confirmation" | "failed"
  output?: string
  error?: string
  pending_confirmations?: string[]
}

export type WorkflowExecutionResult = {
  team_id: string
  results: WorkflowDelegationResult[]
}

export type WorkflowExecution = {
  id: string
  workflow_id: string
  organization_id: string
  owner_user_id: string
  started_by: "api" | "schedule"
  status: WorkflowExecutionStatus
  step_functions_execution_arn?: string
  /** Set once the assignments ran; absent for an execution that failed to start. */
  result?: WorkflowExecutionResult
  error?: string
  created_at: string
  started_at?: string
  completed_at?: string
}

function workflowsPath(organizationId: string, ...segments: string[]) {
  return organizationPath(organizationId, "agent-workflows", ...segments)
}

function assignmentBodies(assignments: WorkflowAssignment[]) {
  return assignments.map((assignment) =>
    pick(assignment, ["agent_id", "task", "context"])
  )
}

function scheduleBody(schedule: WorkflowScheduleInput) {
  return pick(schedule, ["expression", "timezone", "state"])
}

/** `GET …/agent-workflows` (server.listAgentWorkflows): the caller's workflows by name. */
export async function listAgentWorkflows(
  organizationId: string,
  { signal }: ReadOptions = {}
): Promise<AgentWorkflow[]> {
  const result = await apiRequest<{ workflows: AgentWorkflow[] }>(
    workflowsPath(organizationId),
    { signal }
  )
  return result.workflows
}

/** `GET …/agent-workflows/{workflowID}` (server.getAgentWorkflow). */
export function getAgentWorkflow(
  organizationId: string,
  workflowId: string,
  { signal }: ReadOptions = {}
): Promise<AgentWorkflow> {
  return apiRequest<AgentWorkflow>(workflowsPath(organizationId, workflowId), {
    signal,
  })
}

/** `POST …/agent-workflows` (server.createAgentWorkflow): 201; a schedule needs EventBridge Scheduler, else 503. */
export function createAgentWorkflow(
  organizationId: string,
  input: CreateAgentWorkflowInput
): Promise<AgentWorkflow> {
  return apiRequest<AgentWorkflow>(workflowsPath(organizationId), {
    method: "POST",
    body: {
      ...pick(input, ["name", "description", "team_id"]),
      assignments: assignmentBodies(input.assignments),
      ...(input.schedule ? { schedule: scheduleBody(input.schedule) } : {}),
    },
  })
}

/** `PATCH …/agent-workflows/{workflowID}` (server.updateAgentWorkflow); `clear_schedule` removes the schedule. */
export function updateAgentWorkflow(
  organizationId: string,
  workflowId: string,
  input: UpdateAgentWorkflowInput
): Promise<AgentWorkflow> {
  return apiRequest<AgentWorkflow>(workflowsPath(organizationId, workflowId), {
    method: "PATCH",
    body: {
      ...pick(input, ["name", "description", "team_id", "status"]),
      ...(input.assignments
        ? { assignments: assignmentBodies(input.assignments) }
        : {}),
      ...(input.schedule ? { schedule: scheduleBody(input.schedule) } : {}),
      ...(input.clear_schedule ? { clear_schedule: true } : {}),
    },
  })
}

/** `DELETE …/agent-workflows/{workflowID}` (server.deleteAgentWorkflow): 204. */
export async function deleteAgentWorkflow(
  organizationId: string,
  workflowId: string
): Promise<void> {
  await apiRequest(workflowsPath(organizationId, workflowId), {
    method: "DELETE",
  })
}

/** `POST …/agent-workflows/{workflowID}/executions` (server.startAgentWorkflow): 202 with the queued execution. */
export function startAgentWorkflow(
  organizationId: string,
  workflowId: string
): Promise<WorkflowExecution> {
  return apiRequest<WorkflowExecution>(
    workflowsPath(organizationId, workflowId, "executions"),
    { method: "POST" }
  )
}

/** `GET …/agent-workflows/{workflowID}/executions[?limit=]` (server.listAgentWorkflowExecutions): newest first; 50 by default, and a limit over 100 is also 50. */
export async function listWorkflowExecutions(
  organizationId: string,
  workflowId: string,
  { limit, signal }: ReadOptions & { limit?: number } = {}
): Promise<WorkflowExecution[]> {
  const result = await apiRequest<{ executions: WorkflowExecution[] }>(
    withSearch(workflowsPath(organizationId, workflowId, "executions"), {
      limit,
    }),
    { signal }
  )
  return result.executions
}

/** `GET …/agent-workflow-executions/{executionID}` (server.getAgentWorkflowExecution): addressed by execution alone. */
export function getWorkflowExecution(
  organizationId: string,
  executionId: string,
  { signal }: ReadOptions = {}
): Promise<WorkflowExecution> {
  return apiRequest<WorkflowExecution>(
    organizationPath(organizationId, "agent-workflow-executions", executionId),
    { signal }
  )
}
