// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SpecialistCreationDialog } from "./specialist-creation-dialog";

vi.mock("@tanstack/react-router", () => ({
	Link: ({ children }: { children: React.ReactNode }) => (
		<a href="/conversation">{children}</a>
	),
}));

function json(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
		status,
	});
}

function stream(items: unknown[]): Response {
	return new Response(
		items.map((item) => `data: ${JSON.stringify(item)}\n\n`).join(""),
		{ headers: { "Content-Type": "text/event-stream" } },
	);
}

afterEach(() => vi.unstubAllGlobals());

describe("specialist creation with Krude", () => {
	it("starts a primary conversation and requires approval of the exact creation arguments", async () => {
		HTMLElement.prototype.scrollTo = vi.fn();
		vi.stubGlobal(
			"matchMedia",
			vi.fn(() => ({
				addEventListener: vi.fn(),
				matches: true,
				removeEventListener: vi.fn(),
			})),
		);
		const proposal = {
			name: "Launch reviewer",
			description: "Reviews launches for risk",
			instructions: "Inspect launch plans and report concrete risks.",
		};
		const firstItems = [
			{
				type: "message",
				id: "member-1",
				author: "member",
				text: "I want to create a persistent specialist to own this work: Review product launches",
			},
			{
				type: "tool_call",
				id: "call-event",
				call_id: "call-1",
				tool: "agent_system_create_agent",
				arguments: proposal,
			},
			{
				type: "confirmation",
				id: "confirmation-event",
				call_id: "confirmation-1",
				tool_call_id: "call-1",
				tool: "agent_system_create_agent",
				status: "pending",
				arguments: proposal,
			},
		];
		let detail = {
			id: "thread-1",
			title: "New conversation",
			updated_at: "2026-09-22T00:00:00Z",
			items: [] as unknown[],
			pending_confirmations: [] as string[],
		};
		const requests: { path: string; method: string; body?: unknown }[] = [];
		vi.stubGlobal(
			"fetch",
			vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
				const path = String(input);
				const method = init?.method ?? "GET";
				const body = init?.body ? JSON.parse(String(init.body)) : undefined;
				requests.push({ path, method, body });
				if (path.endsWith("/threads") && method === "POST") {
					return json(
						{
							id: "thread-1",
							title: "New conversation",
							updated_at: detail.updated_at,
						},
						201,
					);
				}
				if (path.endsWith("/threads/thread-1") && method === "GET") {
					return json(detail);
				}
				if (path.endsWith("/threads/thread-1/runs") && method === "POST") {
					if ("message" in body) {
						detail = {
							...detail,
							items: firstItems,
							pending_confirmations: ["confirmation-1"],
						};
						return stream(firstItems);
					}
					const completed = [
						{
							type: "confirmation",
							id: "decision-1",
							call_id: "confirmation-1",
							status: "approved",
							tool: "agent_system_create_agent",
						},
						{
							type: "tool_result",
							id: "result-1",
							call_id: "call-1",
							tool: "agent_system_create_agent",
							text: '{"id":"agent-1"}',
						},
						{
							type: "message",
							id: "answer-1",
							author: "Krude",
							text: "Created Launch reviewer.",
						},
					];
					detail = {
						...detail,
						items: [...firstItems, ...completed],
						pending_confirmations: [],
					};
					return stream(completed);
				}
				if (path.endsWith("/threads") && method === "GET") {
					return json({ threads: [] });
				}
				throw new Error(`Unexpected request: ${method} ${path}`);
			}),
		);

		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		const view = render(
			<QueryClientProvider client={queryClient}>
				<SpecialistCreationDialog
					organizationId="org-1"
					trigger={<button type="button">New specialist</button>}
				/>
			</QueryClientProvider>,
		);
		fireEvent.click(view.getByRole("button", { name: "New specialist" }));
		const input = view.getByRole("textbox", {
			name: "Message Krude about this specialist",
		});
		fireEvent.change(input, { target: { value: "Review product launches" } });
		fireEvent.click(view.getByRole("button", { name: "Send message" }));

		const confirmation = await view.findByText(
			"Review the exact specialist Krude proposes. Approving creates it.",
		);
		expect(view.getByText("Review product launches")).toBeTruthy();
		const card = confirmation.closest(".thread-confirmation");
		expect(card).not.toBeNull();
		expect(card?.textContent).toContain(proposal.instructions);
		expect(
			requests.filter((request) => request.method === "POST"),
		).toHaveLength(2);
		fireEvent.click(
			within(card as HTMLElement).getByRole("button", { name: "Approve" }),
		);

		await waitFor(() => {
			expect(
				requests.filter((request) => request.method === "POST"),
			).toHaveLength(3);
			expect(view.getByText("Created Launch reviewer.")).toBeTruthy();
		});
		expect(requests.filter((request) => request.method === "POST")).toEqual([
			{
				path: "/v1/organizations/org-1/threads",
				method: "POST",
				body: undefined,
			},
			{
				path: "/v1/organizations/org-1/threads/thread-1/runs",
				method: "POST",
				body: {
					message:
						"I want to create a persistent specialist to own this work: Review product launches",
				},
			},
			{
				path: "/v1/organizations/org-1/threads/thread-1/runs",
				method: "POST",
				body: { confirmations: [{ id: "confirmation-1", confirmed: true }] },
			},
		]);
	});
});
