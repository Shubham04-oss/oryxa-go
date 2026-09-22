import { queryOptions } from "@tanstack/react-query";
import { apiFetch, apiPath, apiRequest, requestError } from "../api/client";
import type {
	ConfirmationAnswer,
	Item,
	Thread,
	ThreadDetail,
} from "../threads/client";
import { createEventStreamReader } from "../threads/event-stream";

export type AgentKind = "primary" | "specialist";
export type AgentStatus = "active" | "disabled";

export interface ManagedAgent {
	created_at: string;
	description: string;
	id: string;
	instructions: string;
	kind: AgentKind;
	name: string;
	organization_id: string;
	owner_user_id: string;
	revision: number;
	status: AgentStatus;
	updated_at: string;
}

export interface AgentInput {
	description: string;
	instructions: string;
	name: string;
}

export interface AgentUpdateInput {
	description?: string;
	instructions?: string;
	name?: string;
	status?: AgentStatus;
}

export interface AgentTeamMember {
	agent_id: string;
	description?: string;
	name: string;
	position: number;
	role: string;
	status: AgentStatus;
}

export interface AgentTeam {
	coordinator_agent_id: string;
	coordinator_name: string;
	created_at: string;
	description: string;
	id: string;
	members: AgentTeamMember[];
	name: string;
	organization_id: string;
	owner_user_id: string;
	revision: number;
	status: AgentStatus;
	updated_at: string;
}

export interface AgentTeamMessage {
	id: string;
	team_id: string;
	seq: number;
	kind: "member" | "agent";
	author_name: string;
	author_agent_id?: string;
	body: string;
	in_reply_to_seq?: number;
	created_at: string;
}

export interface AgentTeamMemberInput {
	agent_id: string;
	role: string;
}

export interface AgentTeamInput {
	description: string;
	members: AgentTeamMemberInput[];
	name: string;
}

export interface AgentTeamUpdateInput {
	description?: string;
	members?: AgentTeamMemberInput[];
	name?: string;
	status?: AgentStatus;
}

export type WorkflowStatus = "active" | "disabled";
export type WorkflowExecutionStatus =
	| "queued"
	| "running"
	| "succeeded"
	| "needs_confirmation"
	| "failed";

export interface WorkflowAssignment {
	agent_id: string;
	context?: string;
	task: string;
}

export interface WorkflowSchedule {
	expression: string;
	state: "enabled" | "disabled";
	timezone: string;
}

export interface AgentWorkflow {
	assignments: WorkflowAssignment[];
	created_at: string;
	description?: string;
	id: string;
	name: string;
	organization_id: string;
	owner_user_id: string;
	revision: number;
	schedule?: WorkflowSchedule;
	status: WorkflowStatus;
	team_id: string;
	updated_at: string;
}

export interface AgentWorkflowInput {
	assignments: WorkflowAssignment[];
	description: string;
	name: string;
	schedule?: WorkflowSchedule;
	team_id: string;
}

export interface AgentWorkflowUpdateInput {
	assignments?: WorkflowAssignment[];
	clear_schedule?: boolean;
	description?: string;
	name?: string;
	schedule?: WorkflowSchedule;
	status?: WorkflowStatus;
	team_id?: string;
}

export interface WorkflowDelegationResult {
	agent_id: string;
	agent_name: string;
	error?: string;
	output?: string;
	run_id?: string;
	status: string;
	thread_id?: string;
}

export interface WorkflowResult {
	results: WorkflowDelegationResult[];
	team_id: string;
}

export interface WorkflowExecution {
	completed_at?: string;
	created_at: string;
	error?: string;
	id: string;
	organization_id: string;
	owner_user_id: string;
	result?: WorkflowResult;
	started_at?: string;
	started_by: "api" | "schedule";
	status: WorkflowExecutionStatus;
	step_functions_execution_arn?: string;
	workflow_id: string;
}

export interface InteractionParty {
	agent_id: string;
	agent_name: string;
	user_display_name?: string;
	user_email?: string;
	user_id: string;
}

export interface InteractionContextItem {
	label: string;
	value: string;
}

export interface InteractionDecision {
	allowed_tools: string[];
	decided_at: string;
	decision: "approved" | "rejected";
	granted_permissions: string[];
	message: string;
}

export interface InteractionResult {
	artifacts: string[];
	completed_at: string;
	content: string;
	status: "completed" | "failed";
	summary: string;
	tools_used: string[];
}

export type InteractionStatus =
	| "pending"
	| "approved"
	| "rejected"
	| "completed"
	| "failed"
	| "expired";

export interface AgentInteraction {
	context: InteractionContextItem[];
	created_at: string;
	decision?: InteractionDecision;
	expires_at: string;
	id: string;
	organization_id: string;
	purpose: string;
	requested_permissions: string[];
	requested_tools: string[];
	requester: InteractionParty;
	result?: InteractionResult;
	revision: number;
	status: InteractionStatus;
	target: InteractionParty;
	task: string;
	updated_at: string;
}

export interface InteractionDecisionInput {
	allowed_tools?: string[];
	decision: "approved" | "rejected";
	granted_permissions?: string[];
	message: string;
}

export interface InteractionResponseInput {
	artifacts?: string[];
	content: string;
	status: "completed" | "failed";
	summary: string;
	tools_used?: string[];
}

function agentsPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/agents`;
}

function teamsPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/agent-teams`;
}

function workflowsPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/agent-workflows`;
}

function requestsPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/agent-requests`;
}

export async function listAgents(
	organizationId: string,
): Promise<ManagedAgent[]> {
	const { agents } = await apiRequest<{ agents: ManagedAgent[] }>(
		agentsPath(organizationId),
	);
	return agents;
}

/** A specialist run owns its own durable thread, separate from the primary. */
export async function startSpecialistRun(
	organizationId: string,
	agentId: string,
	input: {
		thread_id?: string;
		message?: string;
		confirmations?: ConfirmationAnswer[];
	},
	options: {
		onThread: (id: string) => void;
		onItem: (item: Item) => void;
		signal?: AbortSignal;
	},
): Promise<void> {
	const response = await apiFetch(
		`${agentsPath(organizationId)}${apiPath`/${agentId}`}/runs`,
		{ body: input, method: "POST", signal: options.signal },
	);
	if (!response.ok) {
		throw await requestError(response);
	}
	const threadId = response.headers.get("X-Oryxa-Thread");
	if (threadId) options.onThread(threadId);
	if (!response.body) return;
	const reader = response.body.getReader();
	const parser = createEventStreamReader();
	const deliver = (data: string) => {
		if (data) options.onItem(JSON.parse(data) as Item);
	};
	try {
		for (;;) {
			const { done, value } = await reader.read();
			if (done) {
				for (const event of parser.end()) deliver(event.data);
				return;
			}
			for (const event of parser.push(value)) deliver(event.data);
		}
	} finally {
		reader.releaseLock();
	}
}

export async function listSpecialistThreads(
	organizationId: string,
	agentId: string,
): Promise<Thread[]> {
	const { threads } = await apiRequest<{ threads: Thread[] }>(
		`${agentsPath(organizationId)}${apiPath`/${agentId}`}/threads`,
	);
	return threads;
}

export function getSpecialistThread(
	organizationId: string,
	agentId: string,
	threadId: string,
): Promise<ThreadDetail> {
	return apiRequest<ThreadDetail>(
		`${agentsPath(organizationId)}${apiPath`/${agentId}`}/threads${apiPath`/${threadId}`}`,
	);
}

export function updateAgent(
	organizationId: string,
	agentId: string,
	input: AgentUpdateInput,
): Promise<ManagedAgent> {
	return apiRequest<ManagedAgent>(
		`${agentsPath(organizationId)}${apiPath`/${agentId}`}`,
		{ body: input, method: "PATCH" },
	);
}

export async function deleteAgent(
	organizationId: string,
	agentId: string,
): Promise<void> {
	await apiRequest(`${agentsPath(organizationId)}${apiPath`/${agentId}`}`, {
		method: "DELETE",
	});
}

export async function listAgentTeams(
	organizationId: string,
): Promise<AgentTeam[]> {
	const { teams } = await apiRequest<{ teams: AgentTeam[] }>(
		teamsPath(organizationId),
	);
	return teams;
}

export function createAgentTeam(
	organizationId: string,
	input: AgentTeamInput,
): Promise<AgentTeam> {
	return apiRequest<AgentTeam>(teamsPath(organizationId), { body: input });
}

export function updateAgentTeam(
	organizationId: string,
	teamId: string,
	input: AgentTeamUpdateInput,
): Promise<AgentTeam> {
	return apiRequest<AgentTeam>(
		`${teamsPath(organizationId)}${apiPath`/${teamId}`}`,
		{ body: input, method: "PATCH" },
	);
}

export async function deleteAgentTeam(
	organizationId: string,
	teamId: string,
): Promise<void> {
	await apiRequest(`${teamsPath(organizationId)}${apiPath`/${teamId}`}`, {
		method: "DELETE",
	});
}

export async function listAgentTeamMessages(
	organizationId: string,
	teamId: string,
): Promise<AgentTeamMessage[]> {
	const { messages } = await apiRequest<{ messages: AgentTeamMessage[] }>(
		`${teamsPath(organizationId)}${apiPath`/${teamId}`}/messages`,
	);
	return messages;
}

export async function getAgentTeamThreads(
	organizationId: string,
	teamId: string,
): Promise<Record<string, string>> {
	const { threads } = await apiRequest<{ threads: Record<string, string> }>(
		`${teamsPath(organizationId)}${apiPath`/${teamId}`}/agent-threads`,
	);
	return threads;
}

export async function postAgentTeamMessage(
	organizationId: string,
	teamId: string,
	body: string,
): Promise<AgentTeamMessage[]> {
	const { messages } = await apiRequest<{ messages: AgentTeamMessage[] }>(
		`${teamsPath(organizationId)}${apiPath`/${teamId}`}/messages`,
		{ body: { body }, method: "POST" },
	);
	return messages;
}

export async function listAgentWorkflows(
	organizationId: string,
): Promise<AgentWorkflow[]> {
	const { workflows } = await apiRequest<{ workflows: AgentWorkflow[] }>(
		workflowsPath(organizationId),
	);
	return workflows;
}

export function createAgentWorkflow(
	organizationId: string,
	input: AgentWorkflowInput,
): Promise<AgentWorkflow> {
	return apiRequest<AgentWorkflow>(workflowsPath(organizationId), {
		body: input,
	});
}

export function updateAgentWorkflow(
	organizationId: string,
	workflowId: string,
	input: AgentWorkflowUpdateInput,
): Promise<AgentWorkflow> {
	return apiRequest<AgentWorkflow>(
		`${workflowsPath(organizationId)}${apiPath`/${workflowId}`}`,
		{ body: input, method: "PATCH" },
	);
}

export async function deleteAgentWorkflow(
	organizationId: string,
	workflowId: string,
): Promise<void> {
	await apiRequest(
		`${workflowsPath(organizationId)}${apiPath`/${workflowId}`}`,
		{ method: "DELETE" },
	);
}

export function startAgentWorkflow(
	organizationId: string,
	workflowId: string,
): Promise<WorkflowExecution> {
	return apiRequest<WorkflowExecution>(
		`${workflowsPath(organizationId)}${apiPath`/${workflowId}`}/executions`,
		{ method: "POST" },
	);
}

export async function listWorkflowExecutions(
	organizationId: string,
	workflowId: string,
): Promise<WorkflowExecution[]> {
	const { executions } = await apiRequest<{
		executions: WorkflowExecution[];
	}>(
		`${workflowsPath(organizationId)}${apiPath`/${workflowId}`}/executions?limit=20`,
	);
	return executions;
}

export async function listAgentRequests(
	organizationId: string,
	box: "inbox" | "sent",
): Promise<AgentInteraction[]> {
	const { requests } = await apiRequest<{ requests: AgentInteraction[] }>(
		`${requestsPath(organizationId)}/${box}?limit=50`,
	);
	return requests;
}

export function decideAgentRequest(
	organizationId: string,
	requestId: string,
	input: InteractionDecisionInput,
): Promise<AgentInteraction> {
	return apiRequest<AgentInteraction>(
		`${requestsPath(organizationId)}${apiPath`/${requestId}`}/decision`,
		{ body: input },
	);
}

export function respondToAgentRequest(
	organizationId: string,
	requestId: string,
	input: InteractionResponseInput,
): Promise<AgentInteraction> {
	return apiRequest<AgentInteraction>(
		`${requestsPath(organizationId)}${apiPath`/${requestId}`}/response`,
		{ body: input },
	);
}

export function agentsQueryKey(organizationId: string) {
	return ["agents", organizationId] as const;
}

export function agentsQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => listAgents(organizationId),
		queryKey: agentsQueryKey(organizationId),
	});
}

export function agentTeamsQueryKey(organizationId: string) {
	return [...agentsQueryKey(organizationId), "teams"] as const;
}

export function agentTeamsQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => listAgentTeams(organizationId),
		queryKey: agentTeamsQueryKey(organizationId),
	});
}

export function agentTeamMessagesQueryKey(
	organizationId: string,
	teamId: string,
) {
	return [...agentTeamsQueryKey(organizationId), teamId, "messages"] as const;
}

export function agentTeamMessagesQueryOptions(
	organizationId: string,
	teamId: string,
) {
	return queryOptions({
		queryFn: () => listAgentTeamMessages(organizationId, teamId),
		queryKey: agentTeamMessagesQueryKey(organizationId, teamId),
		refetchInterval: 5_000,
	});
}

export function agentTeamThreadsQueryKey(
	organizationId: string,
	teamId: string,
) {
	return [
		...agentTeamsQueryKey(organizationId),
		teamId,
		"agent-threads",
	] as const;
}

export function agentTeamThreadsQueryOptions(
	organizationId: string,
	teamId: string,
) {
	return queryOptions({
		queryFn: () => getAgentTeamThreads(organizationId, teamId),
		queryKey: agentTeamThreadsQueryKey(organizationId, teamId),
		refetchInterval: 5_000,
	});
}

export function agentWorkflowsQueryKey(organizationId: string) {
	return [...agentsQueryKey(organizationId), "workflows"] as const;
}

export function agentWorkflowsQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => listAgentWorkflows(organizationId),
		queryKey: agentWorkflowsQueryKey(organizationId),
	});
}

export function workflowExecutionsQueryOptions(
	organizationId: string,
	workflowId: string,
) {
	return queryOptions({
		queryFn: () => listWorkflowExecutions(organizationId, workflowId),
		queryKey: [
			...agentWorkflowsQueryKey(organizationId),
			workflowId,
			"executions",
		] as const,
		refetchInterval: (query) =>
			query.state.data?.some((execution) =>
				["queued", "running"].includes(execution.status),
			)
				? 2_000
				: false,
	});
}

export function agentRequestsQueryKey(
	organizationId: string,
	box: "inbox" | "sent",
) {
	return [...agentsQueryKey(organizationId), "requests", box] as const;
}

export function agentRequestsQueryOptions(
	organizationId: string,
	box: "inbox" | "sent",
) {
	return queryOptions({
		queryFn: () => listAgentRequests(organizationId, box),
		queryKey: agentRequestsQueryKey(organizationId, box),
		refetchInterval: 10_000,
	});
}

export function titleCaseStatus(status: string): string {
	return status
		.split("_")
		.map((part) => `${part.charAt(0).toUpperCase()}${part.slice(1)}`)
		.join(" ");
}

export function scheduleLabel(schedule?: WorkflowSchedule): string {
	if (!schedule) return "Manual only";
	if (schedule.state === "disabled") return "Schedule paused";
	return `${schedule.expression} · ${schedule.timezone}`;
}
