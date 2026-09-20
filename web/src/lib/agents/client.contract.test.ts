import { afterEach, describe, expect, it, vi } from "vitest";
import {
	createAgentWorkflow,
	deleteAgent,
	deleteAgentTeam,
	deleteAgentWorkflow,
	listAgentTeams,
	listAgentWorkflows,
	listWorkflowExecutions,
	respondToAgentRequest,
	updateAgent,
	updateAgentTeam,
	updateAgentWorkflow,
} from "./client";

function json(body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
		status: 200,
	});
}

afterEach(() => vi.unstubAllGlobals());

describe("remaining agent API contracts", () => {
	it("preserves every mutation and collection endpoint", async () => {
		const fetchMock = vi.fn().mockImplementation(() =>
			Promise.resolve(
				json({
					executions: [],
					teams: [],
					workflows: [],
				}),
			),
		);
		vi.stubGlobal("fetch", fetchMock);

		await updateAgent("org_1", "agent_1", { status: "disabled" });
		await deleteAgent("org_1", "agent_1");
		await expect(listAgentTeams("org_1")).resolves.toEqual([]);
		await updateAgentTeam("org_1", "team_1", { name: "Launch" });
		await deleteAgentTeam("org_1", "team_1");
		await expect(listAgentWorkflows("org_1")).resolves.toEqual([]);
		await createAgentWorkflow("org_1", {
			assignments: [{ agent_id: "agent_1", task: "Review" }],
			description: "Review launches",
			name: "Launch review",
			team_id: "team_1",
		});
		await updateAgentWorkflow("org_1", "workflow_1", { status: "disabled" });
		await deleteAgentWorkflow("org_1", "workflow_1");
		await expect(
			listWorkflowExecutions("org_1", "workflow_1"),
		).resolves.toEqual([]);
		await respondToAgentRequest("org_1", "request_1", {
			content: "Finished",
			status: "completed",
			summary: "Completed safely",
		});

		expect(
			fetchMock.mock.calls.map(([url, init]) => [url, init.method]),
		).toEqual([
			["/v1/organizations/org_1/agents/agent_1", "PATCH"],
			["/v1/organizations/org_1/agents/agent_1", "DELETE"],
			["/v1/organizations/org_1/agent-teams", "GET"],
			["/v1/organizations/org_1/agent-teams/team_1", "PATCH"],
			["/v1/organizations/org_1/agent-teams/team_1", "DELETE"],
			["/v1/organizations/org_1/agent-workflows", "GET"],
			["/v1/organizations/org_1/agent-workflows", "POST"],
			["/v1/organizations/org_1/agent-workflows/workflow_1", "PATCH"],
			["/v1/organizations/org_1/agent-workflows/workflow_1", "DELETE"],
			[
				"/v1/organizations/org_1/agent-workflows/workflow_1/executions?limit=20",
				"GET",
			],
			["/v1/organizations/org_1/agent-requests/request_1/response", "POST"],
		]);
	});
});
