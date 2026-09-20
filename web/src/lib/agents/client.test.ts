import { afterEach, describe, expect, it, vi } from "vitest";
import {
	agentRequestsQueryKey,
	agentsQueryKey,
	agentTeamsQueryKey,
	agentWorkflowsQueryKey,
	createAgent,
	createAgentTeam,
	decideAgentRequest,
	listAgentRequests,
	listAgents,
	scheduleLabel,
	startAgentWorkflow,
	titleCaseStatus,
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

describe("agent API paths", () => {
	it("lists member-owned agents under the organization", async () => {
		const fetchMock = vi.fn().mockResolvedValue(json(200, { agents: [] }));
		vi.stubGlobal("fetch", fetchMock);

		await expect(listAgents("org / one")).resolves.toEqual([]);

		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org%20%2F%20one/agents",
			expect.objectContaining({ credentials: "include", method: "GET" }),
		);
	});

	it("creates specialists with the strict persisted fields", async () => {
		const fetchMock = vi.fn().mockResolvedValue(json(201, { id: "agt_1" }));
		vi.stubGlobal("fetch", fetchMock);

		await createAgent("org_1", {
			description: "Reviews launch risk",
			instructions: "Return two bounded findings.",
			name: "Launch reviewer",
		});

		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org_1/agents",
			expect.objectContaining({
				body: JSON.stringify({
					description: "Reviews launch risk",
					instructions: "Return two bounded findings.",
					name: "Launch reviewer",
				}),
				method: "POST",
			}),
		);
	});

	it("creates teams from stable agent ids", async () => {
		const fetchMock = vi.fn().mockResolvedValue(json(201, { id: "atm_1" }));
		vi.stubGlobal("fetch", fetchMock);

		await createAgentTeam("org_1", {
			description: "Prepares launches",
			members: [{ agent_id: "agt_sales", role: "Position the launch" }],
			name: "Launch",
		});

		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org_1/agent-teams",
			expect.objectContaining({ method: "POST" }),
		);
	});

	it("starts a workflow through its execution collection", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValue(json(202, { id: "awx_1", status: "queued" }));
		vi.stubGlobal("fetch", fetchMock);

		await startAgentWorkflow("org_1", "awf_1");

		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org_1/agent-workflows/awf_1/executions",
			expect.objectContaining({ method: "POST" }),
		);
	});

	it("separates request inbox and decision transitions", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(json(200, { requests: [] }))
			.mockResolvedValueOnce(json(200, { id: "irq_1", status: "approved" }));
		vi.stubGlobal("fetch", fetchMock);

		await listAgentRequests("org_1", "inbox");
		await decideAgentRequest("org_1", "irq_1", {
			allowed_tools: [],
			decision: "approved",
			granted_permissions: [],
			message: "Approved read-only scope.",
		});

		expect(fetchMock.mock.calls[0]?.[0]).toBe(
			"/v1/organizations/org_1/agent-requests/inbox?limit=50",
		);
		expect(fetchMock.mock.calls[1]?.[0]).toBe(
			"/v1/organizations/org_1/agent-requests/irq_1/decision",
		);
	});
});

describe("agent UI helpers", () => {
	it("keeps every organization cache below one agent prefix", () => {
		expect(agentsQueryKey("org_1")).toEqual(["agents", "org_1"]);
		expect(agentTeamsQueryKey("org_1")).toEqual(["agents", "org_1", "teams"]);
		expect(agentWorkflowsQueryKey("org_1")).toEqual([
			"agents",
			"org_1",
			"workflows",
		]);
		expect(agentRequestsQueryKey("org_1", "sent")).toEqual([
			"agents",
			"org_1",
			"requests",
			"sent",
		]);
	});

	it("labels schedules and machine statuses for people", () => {
		expect(scheduleLabel()).toBe("Manual only");
		expect(
			scheduleLabel({
				expression: "rate(1 day)",
				state: "enabled",
				timezone: "Asia/Kolkata",
			}),
		).toBe("rate(1 day) · Asia/Kolkata");
		expect(titleCaseStatus("needs_confirmation")).toBe("Needs Confirmation");
	});
});
