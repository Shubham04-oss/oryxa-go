import { queryOptions } from "@tanstack/react-query";
import { apiPath, apiRequest } from "../api/client";

export type TeamspaceStatus = "active" | "disabled";
export type DelegationMode = "automatic" | "request";
export type EntryKind =
	| "artifact"
	| "decision"
	| "handoff"
	| "result"
	| "update";
/** @deprecated Prefer EntryKind. */
export type TeamspaceEntryKind = EntryKind;

/** The organization-owned agent which synthesizes deliberately shared work. */
export interface OrganizationAgent {
	can_manage: boolean;
	created_at: string;
	description?: string;
	id: string;
	instructions: string;
	organization_id: string;
	revision: number;
	status: TeamspaceStatus;
	updated_at: string;
	name: string;
}

export interface OrganizationAgentUpdateInput {
	description?: string;
	instructions?: string;
	name?: string;
	status?: TeamspaceStatus;
}

/** A safe projection of a teammate's primary agent; no private context leaks here. */
export interface Whisperer {
	agent_id: string;
	agent_name: string;
	mode: "whisperer";
	user_display_name?: string;
	user_id: string;
}

/** A specialist its owner intentionally made available to a Teamspace. */
export interface TeamspaceContribution {
	agent_id: string;
	created_at: string;
	delegation_mode: DelegationMode;
	description?: string;
	name: string;
	owner_user_id: string;
	position: number;
	role: string;
	status: TeamspaceStatus;
}

export interface Teamspace {
	can_manage: boolean;
	contributions: TeamspaceContribution[];
	created_at: string;
	created_by_user_id: string;
	description?: string;
	id: string;
	is_member: boolean;
	name: string;
	organization_agent: OrganizationAgent;
	organization_id: string;
	revision: number;
	status: TeamspaceStatus;
	team_id: string;
	team_name: string;
	updated_at: string;
	whisperers: Whisperer[];
}

export interface CreateTeamspaceInput {
	description?: string;
	name: string;
	teamId: string;
}

export interface UpdateTeamspaceInput {
	description?: string;
	name?: string;
	status?: TeamspaceStatus;
}

export interface TeamspaceContributionInput {
	agentId: string;
	delegationMode: DelegationMode;
	role: string;
}

export interface TeamspaceContributionUpdateInput {
	delegationMode?: DelegationMode;
	role?: string;
}

export interface TeamspaceArtifact {
	media_type?: string;
	name: string;
	uri: string;
}

/** One immutable publication in the shared Teamspace work ledger. */
export interface TeamspaceEntry {
	artifacts: TeamspaceArtifact[];
	author_agent_id: string;
	author_agent_name: string;
	author_user_id: string;
	created_at: string;
	id: string;
	kind: EntryKind;
	organization_id: string;
	summary: string;
	teamspace_id: string;
	title: string;
	tools_used: string[];
}

export interface PublishTeamspaceEntryInput {
	authorAgentId: string;
	artifacts?: TeamspaceArtifact[];
	kind: EntryKind;
	summary: string;
	title: string;
	toolsUsed?: string[];
}

export interface OrganizationSynthesis {
	agent_id: string;
	decisions: string[];
	open_questions: string[];
	organization_id: string;
	revision: number;
	risks: string[];
	source_entry_id?: string;
	summary: string;
	updated_at?: string;
}

export interface OrganizationFollowUp {
	created_at: string;
	id: string;
	organization_id: string;
	rationale: string;
	source_entry_id: string;
	status: string;
	synthesis_revision: number;
	target_agent_id?: string;
	task: string;
	teamspace_id: string;
}

function organizationAgentPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/organization-agent`;
}

function teamspacesPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/teamspaces`;
}

function teamspacePath(organizationId: string, teamspaceId: string): string {
	return `${teamspacesPath(organizationId)}${apiPath`/${teamspaceId}`}`;
}

function withLimit(path: string, limit?: number): string {
	return limit === undefined
		? path
		: `${path}?${new URLSearchParams({ limit: String(limit) })}`;
}

export function getOrganizationAgent(
	organizationId: string,
): Promise<OrganizationAgent> {
	return apiRequest<OrganizationAgent>(organizationAgentPath(organizationId));
}

export function updateOrganizationAgent(
	organizationId: string,
	input: OrganizationAgentUpdateInput,
): Promise<OrganizationAgent> {
	return apiRequest<OrganizationAgent>(organizationAgentPath(organizationId), {
		body: input,
		method: "PATCH",
	});
}

export function getOrganizationContext(
	organizationId: string,
): Promise<OrganizationSynthesis> {
	return apiRequest<OrganizationSynthesis>(
		`${organizationAgentPath(organizationId)}/context`,
	);
}

export async function listOrganizationFollowUps(
	organizationId: string,
	limit?: number,
): Promise<OrganizationFollowUp[]> {
	const { follow_ups } = await apiRequest<{
		follow_ups: OrganizationFollowUp[];
	}>(withLimit(`${organizationAgentPath(organizationId)}/follow-ups`, limit));
	return follow_ups;
}

export async function listTeamspaces(
	organizationId: string,
): Promise<Teamspace[]> {
	const { teamspaces } = await apiRequest<{ teamspaces: Teamspace[] }>(
		teamspacesPath(organizationId),
	);
	return teamspaces;
}

export function getTeamspace(
	organizationId: string,
	teamspaceId: string,
): Promise<Teamspace> {
	return apiRequest<Teamspace>(teamspacePath(organizationId, teamspaceId));
}

export function createTeamspace(
	organizationId: string,
	input: CreateTeamspaceInput,
): Promise<Teamspace> {
	return apiRequest<Teamspace>(teamspacesPath(organizationId), {
		body: {
			description: input.description,
			name: input.name,
			team_id: input.teamId,
		},
	});
}

export function updateTeamspace(
	organizationId: string,
	teamspaceId: string,
	input: UpdateTeamspaceInput,
): Promise<Teamspace> {
	return apiRequest<Teamspace>(teamspacePath(organizationId, teamspaceId), {
		body: input,
		method: "PATCH",
	});
}

export async function deleteTeamspace(
	organizationId: string,
	teamspaceId: string,
): Promise<void> {
	await apiRequest(teamspacePath(organizationId, teamspaceId), {
		method: "DELETE",
	});
}

export function addTeamspaceContribution(
	organizationId: string,
	teamspaceId: string,
	input: TeamspaceContributionInput,
): Promise<Teamspace> {
	return apiRequest<Teamspace>(
		`${teamspacePath(organizationId, teamspaceId)}/agents`,
		{
			body: {
				agent_id: input.agentId,
				delegation_mode: input.delegationMode,
				role: input.role,
			},
		},
	);
}

export function updateTeamspaceContribution(
	organizationId: string,
	teamspaceId: string,
	agentId: string,
	input: TeamspaceContributionUpdateInput,
): Promise<Teamspace> {
	return apiRequest<Teamspace>(
		`${teamspacePath(organizationId, teamspaceId)}${apiPath`/agents/${agentId}`}`,
		{
			body: {
				delegation_mode: input.delegationMode,
				role: input.role,
			},
			method: "PATCH",
		},
	);
}

export function deleteTeamspaceContribution(
	organizationId: string,
	teamspaceId: string,
	agentId: string,
): Promise<Teamspace> {
	return apiRequest<Teamspace>(
		`${teamspacePath(organizationId, teamspaceId)}${apiPath`/agents/${agentId}`}`,
		{ method: "DELETE" },
	);
}

export async function listTeamspaceEntries(
	organizationId: string,
	teamspaceId: string,
	limit?: number,
): Promise<TeamspaceEntry[]> {
	const { entries } = await apiRequest<{ entries: TeamspaceEntry[] }>(
		withLimit(`${teamspacePath(organizationId, teamspaceId)}/entries`, limit),
	);
	return entries;
}

export function publishTeamspaceEntry(
	organizationId: string,
	teamspaceId: string,
	input: PublishTeamspaceEntryInput,
): Promise<TeamspaceEntry> {
	return apiRequest<TeamspaceEntry>(
		`${teamspacePath(organizationId, teamspaceId)}/entries`,
		{
			body: {
				author_agent_id: input.authorAgentId,
				artifacts: input.artifacts,
				kind: input.kind,
				summary: input.summary,
				title: input.title,
				tools_used: input.toolsUsed,
			},
		},
	);
}

/** The prefix of all Teamspace data cached for one organization. */
export function teamspacesQueryKey(organizationId: string) {
	return ["teamspaces", organizationId] as const;
}

export function organizationAgentQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => getOrganizationAgent(organizationId),
		queryKey: [
			...teamspacesQueryKey(organizationId),
			"organization-agent",
		] as const,
	});
}

export function organizationSynthesisQueryOptions(organizationId: string) {
	return organizationContextQueryOptions(organizationId);
}

export function organizationContextQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => getOrganizationContext(organizationId),
		queryKey: [
			...teamspacesQueryKey(organizationId),
			"organization-agent",
			"context",
		] as const,
	});
}

export function organizationFollowUpsQueryOptions(
	organizationId: string,
	limit = 20,
) {
	return queryOptions({
		queryFn: () => listOrganizationFollowUps(organizationId, limit),
		queryKey: [
			...teamspacesQueryKey(organizationId),
			"organization-agent",
			"follow-ups",
			limit,
		] as const,
	});
}

export function teamspacesQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => listTeamspaces(organizationId),
		queryKey: [...teamspacesQueryKey(organizationId), "list"] as const,
	});
}

export function teamspaceQueryOptions(
	organizationId: string,
	teamspaceId: string,
) {
	return queryOptions({
		queryFn: () => getTeamspace(organizationId, teamspaceId),
		queryKey: [
			...teamspacesQueryKey(organizationId),
			"teamspace",
			teamspaceId,
		] as const,
	});
}

export function teamspaceEntriesQueryOptions(
	organizationId: string,
	teamspaceId: string,
	limit = 100,
) {
	return queryOptions({
		queryFn: () => listTeamspaceEntries(organizationId, teamspaceId, limit),
		queryKey: [
			...teamspacesQueryKey(organizationId),
			"teamspace",
			teamspaceId,
			"entries",
			limit,
		] as const,
	});
}
