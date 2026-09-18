import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import {
	appsErrorMessage,
	type Connection,
	canReconnect,
	connectionStatusLabel,
	connectionStatusTone,
	connectReturnNotice,
	connectReturnTo,
	groupConnectionsByToolkit,
	listConnectionTargets,
	nonConnectableReason,
	parseConnectReturn,
	type Toolkit,
	type TriggerConfigField,
	triggerConfigCapability,
	triggerConfigFieldSubmission,
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

function connection(overrides: Partial<Connection>): Connection {
	return {
		created_at: "2026-09-15T00:00:00Z",
		id: "apc_1",
		member_id: "user-1",
		status: "active",
		toolkit: "github",
		updated_at: "2026-09-15T00:00:00Z",
		...overrides,
	};
}

function toolkit(overrides: Partial<Toolkit>): Toolkit {
	return {
		auth_schemes: ["OAUTH2"],
		categories: [],
		connectable: true,
		description: "Repositories and pull requests",
		name: "GitHub",
		toolkit: "github",
		tools_count: 874,
		triggers_count: 46,
		...overrides,
	};
}

describe("parseConnectReturn", () => {
	it("reads the callback's return params", () => {
		expect(
			parseConnectReturn({ app_connection: "apc_1", status: "active" }),
		).toEqual({ connectionId: "apc_1", status: "active" });
	});

	it("is undefined when this load is not a return trip", () => {
		expect(parseConnectReturn({})).toBeUndefined();
		expect(parseConnectReturn({ status: "active" })).toBeUndefined();
		expect(parseConnectReturn({ app_connection: "apc_1" })).toBeUndefined();
	});
});

describe("connectReturnTo", () => {
	it("points Composio's hosted flow back at the connections page", () => {
		expect(connectReturnTo("https://app.example.com")).toBe(
			"https://app.example.com/app/connections",
		);
	});
});

describe("connectReturnNotice", () => {
	it("welcomes an active connection", () => {
		expect(
			connectReturnNotice({ connectionId: "apc_1", status: "active" }).tone,
		).toBe("success");
	});

	it("explains a connection still finishing", () => {
		expect(
			connectReturnNotice({ connectionId: "apc_1", status: "pending" }).tone,
		).toBe("info");
	});

	it("flags anything else as not finished", () => {
		expect(
			connectReturnNotice({ connectionId: "apc_1", status: "failed" }).tone,
		).toBe("danger");
	});
});

describe("connection status", () => {
	it("labels and tones every status", () => {
		expect(connectionStatusLabel("active")).toBe("Active");
		expect(connectionStatusLabel("pending")).toBe("Connecting…");
		expect(connectionStatusTone("expired")).toBe("warning");
		expect(connectionStatusTone("failed")).toBe("danger");
		expect(connectionStatusTone("active")).toBe("active");
	});

	it("only offers a plain reconnect once a connection has expired", () => {
		expect(canReconnect("expired")).toBe(true);
		expect(canReconnect("active")).toBe(false);
		expect(canReconnect("failed")).toBe(false);
		expect(canReconnect("removed")).toBe(false);
		expect(canReconnect("pending")).toBe(false);
	});
});

describe("nonConnectableReason", () => {
	it("says nothing about a connectable app", () => {
		expect(
			nonConnectableReason(toolkit({ connectable: true })),
		).toBeUndefined();
	});

	it("explains a no-auth app needs no connection", () => {
		expect(
			nonConnectableReason(toolkit({ connectable: false, no_auth: true })),
		).toBe("This app needs no connection.");
	});

	it("explains an app with no OAuth app of Oryxa's own", () => {
		expect(
			nonConnectableReason(toolkit({ connectable: false, no_auth: false })),
		).toBe("Connecting this app isn't set up yet.");
	});
});

describe("groupConnectionsByToolkit", () => {
	it("groups by toolkit, counts active and troubled connections, and drops removed ones", () => {
		const groups = groupConnectionsByToolkit([
			connection({ id: "1", status: "active", toolkit: "slack" }),
			connection({ id: "2", status: "active", toolkit: "github" }),
			connection({ id: "3", status: "expired", toolkit: "github" }),
			connection({ id: "4", status: "removed", toolkit: "github" }),
			connection({ id: "5", status: "failed", toolkit: "notion" }),
		]);

		expect(groups.map((group) => group.toolkit)).toEqual([
			"github",
			"notion",
			"slack",
		]);
		const github = groups.find((group) => group.toolkit === "github");
		expect(github?.connections).toHaveLength(2);
		expect(github?.activeCount).toBe(1);
		expect(github?.attentionCount).toBe(1);
	});

	it("leaves out a toolkit whose every connection was removed", () => {
		const groups = groupConnectionsByToolkit([
			connection({ id: "1", status: "removed", toolkit: "github" }),
		]);
		expect(groups).toEqual([]);
	});
});

describe("listConnectionTargets", () => {
	it("asks the connection's own targets path, not an encoded copy of it", async () => {
		const fetchMock = vi.fn().mockResolvedValue(json(200, { targets: [] }));
		vi.stubGlobal("fetch", fetchMock);

		await listConnectionTargets("org_1", "apc_1");

		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org_1/apps/connections/apc_1/targets",
			expect.objectContaining({ method: "GET" }),
		);
	});
});

describe("triggerConfigCapability", () => {
	it("needs nothing when there is no schema", () => {
		expect(triggerConfigCapability(undefined)).toEqual({ kind: "none" });
	});

	it("needs nothing when the schema names no properties", () => {
		expect(triggerConfigCapability({ properties: {}, type: "object" })).toEqual(
			{ kind: "none" },
		);
	});

	it("renders a form for flat string/number/boolean properties", () => {
		const capability = triggerConfigCapability({
			properties: {
				branch: { description: "Watch this branch only", type: "string" },
				repo: { title: "Repository", type: "string" },
				verbose: { type: "boolean" },
			},
			required: ["repo"],
			type: "object",
		});
		expect(capability.kind).toBe("form");
		if (capability.kind !== "form") throw new Error("expected a form");
		const byName = new Map(
			capability.fields.map((field) => [field.name, field]),
		);
		expect(byName.get("repo")).toMatchObject({
			required: true,
			title: "Repository",
			type: "string",
		});
		expect(byName.get("branch")).toMatchObject({
			description: "Watch this branch only",
			required: false,
			title: "Branch",
		});
		expect(byName.get("verbose")).toMatchObject({ type: "boolean" });
	});

	it("keeps integer distinct from number, so a fraction is still refused", () => {
		const capability = triggerConfigCapability({
			properties: { count: { type: "integer" } },
			type: "object",
		});
		expect(capability.kind).toBe("form");
		if (capability.kind !== "form") throw new Error("expected a form");
		expect(capability.fields[0]).toMatchObject({
			name: "count",
			type: "integer",
		});
	});

	// Composio's catalog is written by as many teams as it has apps, so a
	// property arrives in every shape JSON Schema allows. Each of these used
	// to make the whole trigger unaddable.

	it("collects a list of scalars a line at a time", () => {
		const capability = triggerConfigCapability({
			properties: { labels: { items: { type: "string" }, type: "array" } },
			required: ["labels"],
			type: "object",
		});
		expect(capability.kind).toBe("form");
		if (capability.kind !== "form") throw new Error("expected a form");
		expect(capability.fields[0]).toMatchObject({
			name: "labels",
			required: true,
			type: "strings",
		});
	});

	it("collects a list whose items the schema does not describe", () => {
		const capability = triggerConfigCapability({
			properties: { labels: { type: "array" } },
			required: ["labels"],
			type: "object",
		});
		expect(capability.kind).toBe("form");
		if (capability.kind !== "form") throw new Error("expected a form");
		expect(capability.fields[0]).toMatchObject({ type: "strings" });
	});

	it("offers an enum as a choice rather than free text, keeping each value's own type", () => {
		const capability = triggerConfigCapability({
			properties: {
				state: { enum: ["open", "closed", "all"], type: "string" },
			},
			required: ["state"],
			type: "object",
		});
		expect(capability.kind).toBe("form");
		if (capability.kind !== "form") throw new Error("expected a form");
		expect(capability.fields[0]).toMatchObject({
			options: [
				{ label: "open", value: "open" },
				{ label: "closed", value: "closed" },
				{ label: "all", value: "all" },
			],
			type: "select",
		});
	});

	it("offers an integer or boolean enum as a choice too, typed rather than stringified", () => {
		const capability = triggerConfigCapability({
			properties: {
				priority: { enum: [1, 5, 15], type: "integer" },
				verbose: { enum: [true, false], type: "boolean" },
			},
			type: "object",
		});
		expect(capability.kind).toBe("form");
		if (capability.kind !== "form") throw new Error("expected a form");
		const byName = new Map(
			capability.fields.map((field) => [field.name, field]),
		);
		expect(byName.get("priority")).toMatchObject({
			options: [
				{ label: "1", value: 1 },
				{ label: "5", value: 5 },
				{ label: "15", value: 15 },
			],
			type: "select",
		});
		expect(byName.get("verbose")).toMatchObject({
			options: [
				{ label: "true", value: true },
				{ label: "false", value: false },
			],
			type: "select",
		});
	});

	it("offers a list of enum values as a choice too", () => {
		const capability = triggerConfigCapability({
			properties: {
				states: { items: { enum: ["open", "closed"] }, type: "array" },
			},
			type: "object",
		});
		expect(capability.kind).toBe("form");
		if (capability.kind !== "form") throw new Error("expected a form");
		expect(capability.fields[0]).toMatchObject({
			options: [
				{ label: "open", value: "open" },
				{ label: "closed", value: "closed" },
			],
			type: "strings",
		});
	});

	it("treats a nullable property as the type it can hold", () => {
		const capability = triggerConfigCapability({
			properties: { since: { type: ["string", "null"] } },
			required: ["since"],
			type: "object",
		});
		expect(capability.kind).toBe("form");
		if (capability.kind !== "form") throw new Error("expected a form");
		expect(capability.fields[0]).toMatchObject({ type: "string" });
	});

	it("collects a property written as one of several shapes", () => {
		const capability = triggerConfigCapability({
			properties: {
				target: { anyOf: [{ type: "string" }, { type: "integer" }] },
			},
			required: ["target"],
			type: "object",
		});
		expect(capability.kind).toBe("form");
		if (capability.kind !== "form") throw new Error("expected a form");
		expect(capability.fields[0]).toMatchObject({ type: "string" });
	});

	it("is unsupported when a required property is an object nothing can type", () => {
		expect(
			triggerConfigCapability({
				properties: { filter: { type: "object" } },
				required: ["filter"],
				type: "object",
			}),
		).toEqual({ kind: "unsupported" });
	});

	it("is unsupported when a required property is a list of objects", () => {
		expect(
			triggerConfigCapability({
				properties: { rules: { items: { type: "object" }, type: "array" } },
				required: ["rules"],
				type: "object",
			}),
		).toEqual({ kind: "unsupported" });
	});

	it("skips an uncollectable optional property rather than failing the whole form", () => {
		const capability = triggerConfigCapability({
			properties: {
				filter: { type: "object" },
				repo: { type: "string" },
			},
			required: ["repo"],
			type: "object",
		});
		expect(capability.kind).toBe("form");
		if (capability.kind !== "form") throw new Error("expected a form");
		expect(capability.fields.map((field) => field.name)).toEqual(["repo"]);
	});
});

function configField(
	overrides: Partial<TriggerConfigField> = {},
): TriggerConfigField {
	return {
		name: "count",
		required: false,
		title: "Count",
		type: "string",
		...overrides,
	};
}

describe("triggerConfigFieldSubmission", () => {
	it("leaves an untouched, optional field out of the config", () => {
		expect(triggerConfigFieldSubmission(configField(), undefined)).toEqual({
			kind: "absent",
		});
	});

	it("sends a whole number for an integer field", () => {
		expect(
			triggerConfigFieldSubmission(configField({ type: "integer" }), "5"),
		).toEqual({ kind: "value", value: 5 });
	});

	it("blocks a fraction typed into an integer field", () => {
		const result = triggerConfigFieldSubmission(
			configField({ title: "Retry count", type: "integer" }),
			"5.5",
		);
		expect(result).toEqual({
			kind: "blocked",
			problem: "Retry count must be a whole number.",
		});
	});

	it("blocks non-numeric text typed into an integer field", () => {
		expect(
			triggerConfigFieldSubmission(configField({ type: "integer" }), "abc")
				.kind,
		).toBe("blocked");
	});

	it("sends a number field's value as typed, decimals included", () => {
		expect(
			triggerConfigFieldSubmission(configField({ type: "number" }), "5.5"),
		).toEqual({ kind: "value", value: 5.5 });
	});

	it("sends a select's chosen value as its own type, not a string", () => {
		const field = configField({
			options: [
				{ label: "1", value: 1 },
				{ label: "5", value: 5 },
			],
			type: "select",
		});
		expect(triggerConfigFieldSubmission(field, 5)).toEqual({
			kind: "value",
			value: 5,
		});
		expect(
			triggerConfigFieldSubmission(configField({ type: "select" }), true),
		).toEqual({ kind: "value", value: true });
	});

	it("does not mistake a select's false or 0 for an untouched field", () => {
		const field = configField({
			options: [
				{ label: "On", value: true },
				{ label: "Off", value: false },
			],
			required: true,
			type: "select",
		});
		expect(triggerConfigFieldSubmission(field, false)).toEqual({
			kind: "value",
			value: false,
		});
	});

	it("sends a matching line's typed value for a list with enum options", () => {
		const field = configField({
			options: [
				{ label: "1", value: 1 },
				{ label: "2", value: 2 },
			],
			type: "strings",
		});
		expect(triggerConfigFieldSubmission(field, "1\n2\nother")).toEqual({
			kind: "value",
			// A line naming an option sends its typed number, not the string it
			// was typed as; a line naming nothing known is passed through as-is.
			value: [1, 2, "other"],
		});
	});
});

describe("appsErrorMessage", () => {
	it("uses apps' own copy for a known code", () => {
		expect(
			appsErrorMessage(new ApiError("x", 422, "not_connectable"), "fallback"),
		).toBe("This app can't be connected here yet.");
	});

	it("falls back to the generic wording otherwise", () => {
		expect(
			appsErrorMessage(
				new ApiError("internal server error", 500),
				"Could not save",
			),
		).toBe("Could not save");
	});
});
