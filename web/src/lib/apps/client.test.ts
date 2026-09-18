import { describe, expect, it } from "vitest";
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
	nonConnectableReason,
	parseConnectReturn,
	type Toolkit,
	triggerConfigCapability,
} from "./client";

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

	it("treats integer as a number field", () => {
		const capability = triggerConfigCapability({
			properties: { count: { type: "integer" } },
			type: "object",
		});
		expect(capability.kind).toBe("form");
		if (capability.kind !== "form") throw new Error("expected a form");
		expect(capability.fields[0]).toMatchObject({
			name: "count",
			type: "number",
		});
	});

	it("is unsupported when a required property is not a flat type", () => {
		expect(
			triggerConfigCapability({
				properties: { labels: { type: "array" } },
				required: ["labels"],
				type: "object",
			}),
		).toEqual({ kind: "unsupported" });
	});

	it("skips an unsupported optional property rather than failing the whole form", () => {
		const capability = triggerConfigCapability({
			properties: {
				labels: { type: "array" },
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
