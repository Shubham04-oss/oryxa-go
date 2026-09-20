import { describe, expect, it } from "vitest";
import type { Teamspace } from "./client";
import { canPublishSharedWork, safeArtifactHref } from "./policy";

function space(
	overrides: Omit<Partial<Teamspace>, "organization_agent"> & {
		organization_agent?: Partial<Teamspace["organization_agent"]>;
	} = {},
): Teamspace {
	const { organization_agent: organizationAgent, ...spaceOverrides } =
		overrides;
	return {
		can_manage: false,
		contributions: [],
		created_at: "2026-09-19T00:00:00Z",
		created_by_user_id: "user_1",
		id: "tsp_1",
		is_member: true,
		name: "Launch",
		organization_agent: {
			can_manage: false,
			created_at: "2026-09-19T00:00:00Z",
			id: "oag_1",
			instructions: "Synthesize shared work.",
			name: "Atlas",
			organization_id: "org_1",
			revision: 1,
			status: "active",
			updated_at: "2026-09-19T00:00:00Z",
			...organizationAgent,
		},
		organization_id: "org_1",
		revision: 1,
		status: "active",
		team_id: "team_1",
		team_name: "Launch",
		updated_at: "2026-09-19T00:00:00Z",
		whisperers: [],
		...spaceOverrides,
	};
}

describe("safeArtifactHref", () => {
	it.each([
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"http://example.test/report",
		"//evil.example/report",
		"not a url",
	])("keeps an untrusted address non-clickable: %s", (uri) => {
		expect(safeArtifactHref(uri)).toBeUndefined();
	});

	it("allows HTTPS and a first-party absolute path", () => {
		expect(safeArtifactHref("https://files.example/report")).toBe(
			"https://files.example/report",
		);
		expect(safeArtifactHref("/v1/files/report")).toBe("/v1/files/report");
	});
});

describe("canPublishSharedWork", () => {
	it("requires membership, active space and organization agent, and an author", () => {
		expect(canPublishSharedWork(space(), 1)).toBe(true);
		expect(canPublishSharedWork(space({ is_member: false }), 1)).toBe(false);
		expect(canPublishSharedWork(space({ status: "disabled" }), 1)).toBe(false);
		expect(
			canPublishSharedWork(
				space({ organization_agent: { status: "disabled" } }),
				1,
			),
		).toBe(false);
		expect(canPublishSharedWork(space(), 0)).toBe(false);
	});
});
