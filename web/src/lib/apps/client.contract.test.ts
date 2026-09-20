import { afterEach, describe, expect, it, vi } from "vitest";
import {
	connect,
	createTrigger,
	disconnectConnection,
	getCatalogToolkit,
	listCatalog,
	listCatalogTriggerTypes,
	listConnections,
	listTriggers,
	removeTrigger,
} from "./client";

function json(body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
		status: 200,
	});
}

afterEach(() => vi.unstubAllGlobals());

describe("apps API contracts", () => {
	it("preserves catalog, connection, and trigger endpoints", async () => {
		const fetchMock = vi.fn().mockImplementation((url: string) => {
			if (url.includes("/catalog") && url.includes("/triggers")) {
				return Promise.resolve(json({ triggers: [] }));
			}
			if (url === "/v1/apps/catalog?limit=10&search=gmail") {
				return Promise.resolve(json({ toolkits: [] }));
			}
			if (url.includes("/apps/connections") && !url.endsWith("connection_1")) {
				return Promise.resolve(json({ connections: [] }));
			}
			if (url.includes("/apps/triggers") && !url.endsWith("trigger_1")) {
				return Promise.resolve(json({ triggers: [] }));
			}
			return Promise.resolve(json({}));
		});
		vi.stubGlobal("fetch", fetchMock);

		await expect(listCatalog({ limit: 10, search: "gmail" })).resolves.toEqual({
			toolkits: [],
		});
		await getCatalogToolkit("google / mail");
		await expect(
			listCatalogTriggerTypes("gmail", { limit: 50 }),
		).resolves.toEqual({ triggers: [] });
		await connect("org_1", {
			alias: "Work",
			returnTo: "https://app.example.test/app/connections",
			toolkit: "gmail",
		});
		await expect(
			listConnections("org_1", { scope: "organization", toolkit: "gmail" }),
		).resolves.toEqual([]);
		await disconnectConnection("org_1", "connection_1");
		await expect(listTriggers("org_1", "connection_1")).resolves.toEqual([]);
		await createTrigger("org_1", {
			connectionId: "connection_1",
			trigger: "NEW_EMAIL",
		});
		await removeTrigger("org_1", "trigger_1");

		expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
			"/v1/apps/catalog?limit=10&search=gmail",
			"/v1/apps/catalog/google%20%2F%20mail",
			"/v1/apps/catalog/gmail/triggers?limit=50",
			"/v1/organizations/org_1/apps/connections",
			"/v1/organizations/org_1/apps/connections?scope=organization&toolkit=gmail",
			"/v1/organizations/org_1/apps/connections/connection_1",
			"/v1/organizations/org_1/apps/triggers?connection_id=connection_1",
			"/v1/organizations/org_1/apps/triggers",
			"/v1/organizations/org_1/apps/triggers/trigger_1",
		]);
	});
});
