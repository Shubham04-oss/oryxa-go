import { queryOptions } from "@tanstack/react-query";
import { z } from "zod";
import { apiPath, apiRequest } from "../api/client";

export type TeamRole = "admin" | "member";

export interface Team {
	created_at: string;
	created_by: string;
	id: string;
	name: string;
	organization_id: string;
	path?: string;
	updated_at: string;
}

export interface TeamSummary extends Team {
	can_manage: boolean;
	/** The caller's role on the team; missing when they are not on it. */
	role?: TeamRole;
}

export interface TeamMembership {
	display_name?: string;
	email: string;
	joined_at: string;
	notify_email: boolean;
	organization_id: string;
	path?: string;
	role: TeamRole;
	team_id: string;
	user_id: string;
}

export interface TeamDetail {
	can_manage: boolean;
	/** The caller's place on the team; null for an organization admin off it. */
	membership: TeamMembership | null;
	team: Team;
}

export interface MemberSearchHit {
	display_name?: string;
	email: string;
	org_role: "owner" | "admin" | "member";
	path?: string;
	teams: { id: string; name: string; role: TeamRole }[];
	user_id: string;
}

export const teamNameSchema = z.object({
	name: z
		.string()
		.trim()
		.min(1, "Enter a team name")
		.max(120, "Team name is too long"),
});

export type TeamNameInput = z.infer<typeof teamNameSchema>;

function teamsPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/teams`;
}

function teamPath(organizationId: string, teamId: string): string {
	return apiPath`/v1/organizations/${organizationId}/teams/${teamId}`;
}

export async function listTeams(
	organizationId: string,
): Promise<TeamSummary[]> {
	const { teams } = await apiRequest<{ teams: TeamSummary[] }>(
		teamsPath(organizationId),
	);
	return teams;
}

export function getTeam(
	organizationId: string,
	teamId: string,
): Promise<TeamDetail> {
	return apiRequest<TeamDetail>(teamPath(organizationId, teamId));
}

export function createTeam(
	organizationId: string,
	input: { name: string; parent_id?: string },
): Promise<Team> {
	return apiRequest<Team>(teamsPath(organizationId), { body: input });
}

export function renameTeam(
	organizationId: string,
	teamId: string,
	name: string,
): Promise<Team> {
	return apiRequest<Team>(teamPath(organizationId, teamId), {
		body: { name },
		method: "PATCH",
	});
}

/** Deletes the team and every team nested under it. */
export async function deleteTeam(
	organizationId: string,
	teamId: string,
): Promise<void> {
	await apiRequest(teamPath(organizationId, teamId), { method: "DELETE" });
}

export async function listTeamMembers(
	organizationId: string,
	teamId: string,
): Promise<TeamMembership[]> {
	const { members } = await apiRequest<{ members: TeamMembership[] }>(
		`${teamPath(organizationId, teamId)}/members`,
	);
	return members;
}

export function addTeamMember(
	organizationId: string,
	teamId: string,
	input: { role: TeamRole; user_id: string },
): Promise<TeamMembership> {
	return apiRequest<TeamMembership>(
		`${teamPath(organizationId, teamId)}/members`,
		{ body: input },
	);
}

export function changeTeamMemberRole(
	organizationId: string,
	teamId: string,
	userId: string,
	role: TeamRole,
): Promise<TeamMembership> {
	return apiRequest<TeamMembership>(
		`${teamPath(organizationId, teamId)}${apiPath`/members/${userId}`}`,
		{ body: { role }, method: "PATCH" },
	);
}

/** Removes someone from the team, or leaves it when `userId` is the caller. */
export async function removeTeamMember(
	organizationId: string,
	teamId: string,
	userId: string,
): Promise<void> {
	await apiRequest(
		`${teamPath(organizationId, teamId)}${apiPath`/members/${userId}`}`,
		{ method: "DELETE" },
	);
}

/** The caller's own switch for team-call emails on this team. */
export function setTeamEmailNotices(
	organizationId: string,
	teamId: string,
	notifyEmail: boolean,
): Promise<TeamMembership> {
	return apiRequest<TeamMembership>(
		`${teamPath(organizationId, teamId)}/notification`,
		{ body: { notify_email: notifyEmail }, method: "PATCH" },
	);
}

export async function searchMembers(
	organizationId: string,
	query: string,
): Promise<MemberSearchHit[]> {
	const search = new URLSearchParams({ limit: "8", q: query });
	const { members } = await apiRequest<{ members: MemberSearchHit[] }>(
		`${apiPath`/v1/organizations/${organizationId}/members/search`}?${search}`,
	);
	return members;
}

/** The prefix of everything cached from teams about one organization. */
export function teamsQueryKey(organizationId: string) {
	return ["teams", organizationId] as const;
}

export function teamsQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => listTeams(organizationId),
		queryKey: [...teamsQueryKey(organizationId), "list"],
	});
}

export function teamQueryOptions(organizationId: string, teamId: string) {
	return queryOptions({
		queryFn: () => getTeam(organizationId, teamId),
		queryKey: [...teamsQueryKey(organizationId), "team", teamId],
	});
}

export function teamMembersQueryOptions(
	organizationId: string,
	teamId: string,
) {
	return queryOptions({
		queryFn: () => listTeamMembers(organizationId, teamId),
		queryKey: [...teamsQueryKey(organizationId), "team", teamId, "members"],
	});
}

export function memberSearchQueryOptions(
	organizationId: string,
	query: string,
) {
	const trimmed = query.trim();
	return queryOptions({
		enabled: trimmed !== "",
		queryFn: () => searchMembers(organizationId, trimmed),
		queryKey: [...teamsQueryKey(organizationId), "member-search", trimmed],
		staleTime: 30_000,
	});
}

export interface TeamNode {
	children: TeamNode[];
	depth: number;
	team: TeamSummary;
}

function closestParent(
	path: string,
	nodes: ReadonlyMap<string, TeamNode>,
): TeamNode | undefined {
	const labels = path.split(".");
	for (let end = labels.length - 1; end > 0; end -= 1) {
		const parent = nodes.get(labels.slice(0, end).join("."));
		if (parent) {
			return parent;
		}
	}
	return undefined;
}

/**
 * Arranges teams by their ltree paths: each sits under the closest team whose
 * path begins its own. Visibility is per team, so a team whose parent the
 * caller cannot see stays at the top.
 */
export function teamTree(teams: TeamSummary[]): TeamNode[] {
	const nodes = new Map<string, TeamNode>();
	for (const team of teams) {
		if (team.path) {
			nodes.set(team.path, { children: [], depth: 0, team });
		}
	}

	const roots: TeamNode[] = [];
	for (const team of teams) {
		const node = (team.path && nodes.get(team.path)) || {
			children: [],
			depth: 0,
			team,
		};
		const parent = team.path ? closestParent(team.path, nodes) : undefined;
		(parent ? parent.children : roots).push(node);
	}

	const settle = (level: TeamNode[], depth: number) => {
		level.sort((a, b) => a.team.name.localeCompare(b.team.name));
		for (const node of level) {
			node.depth = depth;
			settle(node.children, depth + 1);
		}
	};
	settle(roots, 0);
	return roots;
}

/** The tree in reading order, each team once. */
export function flattenTeams(nodes: TeamNode[]): TeamNode[] {
	return nodes.flatMap((node) => [node, ...flattenTeams(node.children)]);
}

/** Each team's name after the visible teams above it: "Product / Web". */
export function teamTrails(teams: TeamSummary[]): Map<string, string> {
	const trails = new Map<string, string>();
	const walk = (nodes: TeamNode[], above: string) => {
		for (const node of nodes) {
			const trail = above ? `${above} / ${node.team.name}` : node.team.name;
			trails.set(node.team.id, trail);
			walk(node.children, trail);
		}
	};
	walk(teamTree(teams), "");
	return trails;
}
