import { describe, expect, it } from "vitest";
import { flattenTeams, type TeamSummary, teamTrails, teamTree } from "./client";

function team(name: string, path: string): TeamSummary {
	return {
		can_manage: false,
		created_at: "2026-09-15T00:00:00Z",
		created_by: "user-1",
		id: `team_${name.toLowerCase()}`,
		name,
		organization_id: "org_1",
		path,
		updated_at: "2026-09-15T00:00:00Z",
	};
}

function outline(teams: TeamSummary[]) {
	return flattenTeams(teamTree(teams)).map((node) => [
		node.team.name,
		node.depth,
	]);
}

describe("teamTree", () => {
	it("nests teams under the closest team on their path", () => {
		expect(
			outline([
				team("Web", "org_1.product.web"),
				team("Product", "org_1.product"),
				team("Design", "org_1.product.web.design"),
				team("Ops", "org_1.ops"),
			]),
		).toEqual([
			["Ops", 0],
			["Product", 0],
			["Web", 1],
			["Design", 2],
		]);
	});

	it("keeps a team whose parent the caller cannot see at the top", () => {
		expect(
			outline([team("Ops", "org_1.ops"), team("Child", "org_1.hidden.child")]),
		).toEqual([
			["Child", 0],
			["Ops", 0],
		]);
	});

	it("names each team after the teams above it", () => {
		const trails = teamTrails([
			team("Product", "org_1.product"),
			team("Web", "org_1.product.web"),
		]);
		expect(trails.get("team_web")).toBe("Product / Web");
		expect(trails.get("team_product")).toBe("Product");
	});
});
