import { afterEach, describe, expect, it, vi } from "vitest";
import {
	addTeamspaceContribution,
	createTeamspace,
	deleteTeamspace,
	deleteTeamspaceContribution,
	getOrganizationAgent,
	getOrganizationContext,
	getTeamspace,
	listOrganizationFollowUps,
	listTeamspaceEntries,
	listTeamspaces,
	organizationAgentQueryOptions,
	organizationContextQueryOptions,
	organizationFollowUpsQueryOptions,
	publishTeamspaceEntry,
	teamspaceEntriesQueryOptions,
	teamspaceQueryOptions,
	teamspacesQueryKey,
	teamspacesQueryOptions,
	updateOrganizationAgent,
	updateTeamspace,
	updateTeamspaceContribution,
} from "./client";

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
		status,
	});
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("Teamspace API paths", () => {
	it("uses the organization-agent resources and their response envelopes", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(json(200, { id: "oag_1" }))
			.mockResolvedValueOnce(json(200, { id: "oag_1", name: "Atlas" }))
			.mockResolvedValueOnce(json(200, { summary: "Shared state" }))
			.mockResolvedValueOnce(json(200, { follow_ups: [{ id: "ofu_1" }] }));
		vi.stubGlobal("fetch", fetchMock);

		await expect(getOrganizationAgent("org / one")).resolves.toEqual({
			id: "oag_1",
		});
		await expect(
			updateOrganizationAgent("org / one", { name: "Atlas" }),
		).resolves.toEqual({ id: "oag_1", name: "Atlas" });
		await expect(getOrganizationContext("org / one")).resolves.toEqual({
			summary: "Shared state",
		});
		await expect(listOrganizationFollowUps("org / one", 8)).resolves.toEqual([
			{ id: "ofu_1" },
		]);

		expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
			"/v1/organizations/org%20%2F%20one/organization-agent",
			"/v1/organizations/org%20%2F%20one/organization-agent",
			"/v1/organizations/org%20%2F%20one/organization-agent/context",
			"/v1/organizations/org%20%2F%20one/organization-agent/follow-ups?limit=8",
		]);
		expect(fetchMock.mock.calls[1]?.[1]).toEqual(
			expect.objectContaining({
				body: JSON.stringify({ name: "Atlas" }),
				method: "PATCH",
			}),
		);
	});

	it("maps Teamspace create and update fields to the backend contract", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(json(201, { id: "tsp_1" }))
			.mockResolvedValueOnce(json(200, { id: "tsp_1", status: "disabled" }))
			.mockResolvedValueOnce(json(200, { id: "tsp_1" }))
			.mockResolvedValueOnce(json(200, { teamspaces: [{ id: "tsp_1" }] }))
			.mockResolvedValueOnce(new Response(null, { status: 204 }));
		vi.stubGlobal("fetch", fetchMock);

		await createTeamspace("org_1", {
			description: "A shared launch ledger",
			name: "Launch",
			teamId: "team / launch",
		});
		await updateTeamspace("org_1", "tsp / one", { status: "disabled" });
		await getTeamspace("org_1", "tsp / one");
		await expect(listTeamspaces("org_1")).resolves.toEqual([{ id: "tsp_1" }]);
		await deleteTeamspace("org_1", "tsp / one");

		expect(fetchMock.mock.calls[0]?.[0]).toBe(
			"/v1/organizations/org_1/teamspaces",
		);
		expect(fetchMock.mock.calls[0]?.[1]).toEqual(
			expect.objectContaining({
				body: JSON.stringify({
					description: "A shared launch ledger",
					name: "Launch",
					team_id: "team / launch",
				}),
				method: "POST",
			}),
		);
		expect(fetchMock.mock.calls.slice(1).map(([url]) => url)).toEqual([
			"/v1/organizations/org_1/teamspaces/tsp%20%2F%20one",
			"/v1/organizations/org_1/teamspaces/tsp%20%2F%20one",
			"/v1/organizations/org_1/teamspaces",
			"/v1/organizations/org_1/teamspaces/tsp%20%2F%20one",
		]);
		expect(fetchMock.mock.calls[1]?.[1]).toEqual(
			expect.objectContaining({
				body: JSON.stringify({ status: "disabled" }),
				method: "PATCH",
			}),
		);
		expect(fetchMock.mock.calls[4]?.[1]).toEqual(
			expect.objectContaining({ method: "DELETE" }),
		);
	});

	it("keeps contributions and shared entries scoped to their Teamspace", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(json(201, { id: "tsp_1" }))
			.mockResolvedValueOnce(json(200, { id: "tsp_1" }))
			.mockResolvedValueOnce(json(200, { id: "tsp_1" }))
			.mockResolvedValueOnce(json(200, { entries: [{ id: "tse_1" }] }))
			.mockResolvedValueOnce(json(201, { id: "tse_2" }));
		vi.stubGlobal("fetch", fetchMock);

		await addTeamspaceContribution("org_1", "tsp / one", {
			agentId: "agt / reviewer",
			delegationMode: "automatic",
			role: "Review architecture",
		});
		await updateTeamspaceContribution("org_1", "tsp / one", "agt / reviewer", {
			delegationMode: "request",
		});
		await deleteTeamspaceContribution("org_1", "tsp / one", "agt / reviewer");
		await expect(
			listTeamspaceEntries("org_1", "tsp / one", 25),
		).resolves.toEqual([{ id: "tse_1" }]);
		await publishTeamspaceEntry("org_1", "tsp / one", {
			authorAgentId: "agt / reviewer",
			artifacts: [{ name: "Review", uri: "s3://review" }],
			kind: "result",
			summary: "Ready to proceed.",
			title: "Architecture review",
			toolsUsed: ["github.search"],
		});

		const base = "/v1/organizations/org_1/teamspaces/tsp%20%2F%20one";
		expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
			`${base}/agents`,
			`${base}/agents/agt%20%2F%20reviewer`,
			`${base}/agents/agt%20%2F%20reviewer`,
			`${base}/entries?limit=25`,
			`${base}/entries`,
		]);
		expect(fetchMock.mock.calls[0]?.[1]).toEqual(
			expect.objectContaining({
				body: JSON.stringify({
					agent_id: "agt / reviewer",
					delegation_mode: "automatic",
					role: "Review architecture",
				}),
				method: "POST",
			}),
		);
		expect(fetchMock.mock.calls[1]?.[1]).toEqual(
			expect.objectContaining({
				body: JSON.stringify({
					delegation_mode: "request",
					role: undefined,
				}),
				method: "PATCH",
			}),
		);
		expect(fetchMock.mock.calls[2]?.[1]).toEqual(
			expect.objectContaining({ method: "DELETE" }),
		);
		expect(fetchMock.mock.calls[4]?.[1]).toEqual(
			expect.objectContaining({
				body: JSON.stringify({
					author_agent_id: "agt / reviewer",
					artifacts: [{ name: "Review", uri: "s3://review" }],
					kind: "result",
					summary: "Ready to proceed.",
					title: "Architecture review",
					tools_used: ["github.search"],
				}),
				method: "POST",
			}),
		);
	});
});

describe("Teamspace query options", () => {
	it("keeps every organization cache below one Teamspace prefix", () => {
		expect(teamspacesQueryKey("org_1")).toEqual(["teamspaces", "org_1"]);
		expect(organizationAgentQueryOptions("org_1").queryKey).toEqual([
			"teamspaces",
			"org_1",
			"organization-agent",
		]);
		expect(organizationContextQueryOptions("org_1").queryKey).toEqual([
			"teamspaces",
			"org_1",
			"organization-agent",
			"context",
		]);
		expect(organizationFollowUpsQueryOptions("org_1", 8).queryKey).toEqual([
			"teamspaces",
			"org_1",
			"organization-agent",
			"follow-ups",
			8,
		]);
		expect(teamspacesQueryOptions("org_1").queryKey).toEqual([
			"teamspaces",
			"org_1",
			"list",
		]);
		expect(teamspaceQueryOptions("org_1", "tsp_1").queryKey).toEqual([
			"teamspaces",
			"org_1",
			"teamspace",
			"tsp_1",
		]);
		expect(teamspaceEntriesQueryOptions("org_1", "tsp_1", 25).queryKey).toEqual(
			["teamspaces", "org_1", "teamspace", "tsp_1", "entries", 25],
		);
	});
});
