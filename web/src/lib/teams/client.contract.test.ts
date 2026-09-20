import { afterEach, describe, expect, it, vi } from "vitest";
import {
	addTeamMember,
	changeTeamMemberRole,
	createTeam,
	deleteTeam,
	getTeam,
	listTeamMembers,
	listTeams,
	removeTeamMember,
	renameTeam,
	searchMembers,
	setTeamEmailNotices,
} from "./client";

function json(body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
		status: 200,
	});
}

afterEach(() => vi.unstubAllGlobals());

describe("teams API contracts", () => {
	it("preserves team, membership, notification, and search endpoints", async () => {
		const fetchMock = vi
			.fn()
			.mockImplementation(() =>
				Promise.resolve(json({ members: [], teams: [] })),
			);
		vi.stubGlobal("fetch", fetchMock);

		await expect(listTeams("org_1")).resolves.toEqual([]);
		await getTeam("org_1", "team / one");
		await createTeam("org_1", { name: "Product", parent_id: "team_root" });
		await renameTeam("org_1", "team_1", "Platform");
		await deleteTeam("org_1", "team_1");
		await expect(listTeamMembers("org_1", "team_1")).resolves.toEqual([]);
		await addTeamMember("org_1", "team_1", {
			role: "member",
			user_id: "user_1",
		});
		await changeTeamMemberRole("org_1", "team_1", "user / one", "admin");
		await removeTeamMember("org_1", "team_1", "user_1");
		await setTeamEmailNotices("org_1", "team_1", false);
		await expect(searchMembers("org_1", "Ada Lovelace")).resolves.toEqual([]);

		expect(
			fetchMock.mock.calls.map(([url, init]) => [url, init.method]),
		).toEqual([
			["/v1/organizations/org_1/teams", "GET"],
			["/v1/organizations/org_1/teams/team%20%2F%20one", "GET"],
			["/v1/organizations/org_1/teams", "POST"],
			["/v1/organizations/org_1/teams/team_1", "PATCH"],
			["/v1/organizations/org_1/teams/team_1", "DELETE"],
			["/v1/organizations/org_1/teams/team_1/members", "GET"],
			["/v1/organizations/org_1/teams/team_1/members", "POST"],
			[
				"/v1/organizations/org_1/teams/team_1/members/user%20%2F%20one",
				"PATCH",
			],
			["/v1/organizations/org_1/teams/team_1/members/user_1", "DELETE"],
			["/v1/organizations/org_1/teams/team_1/notification", "PATCH"],
			["/v1/organizations/org_1/members/search?limit=8&q=Ada+Lovelace", "GET"],
		]);
	});
});
