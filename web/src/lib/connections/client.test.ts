import { describe, expect, it } from "vitest";
import {
	type Connection,
	connectFailure,
	connectionAccess,
	connectionProblem,
	connectMethod,
	connectReturnUri,
	keyCredential,
	placementOptions,
} from "./client";

function connection(overrides: Partial<Connection>): Connection {
	return {
		app_id: "app_1",
		connected_at: "2026-09-15T00:00:00Z",
		created_by: "user-1",
		credential_type: "oauth2",
		default: true,
		health: { state: "healthy" },
		id: "con_1",
		organization_id: "org_1",
		provider: "github",
		provider_account: { id: "1", name: "asha" },
		scope: "member",
		scopes: [],
		status: "active",
		status_changed_at: "2026-09-15T00:00:00Z",
		updated_at: "2026-09-15T00:00:00Z",
		usage: "members",
		...overrides,
	};
}

const viewer = {
	isOrgAdmin: false,
	manageableTeams: [{ id: "team_1", name: "Product" }],
	userId: "user-1",
};

describe("connecting", () => {
	it("asks for a secret only for key-based tools", () => {
		expect(connectMethod("bearer")).toBe("key");
		expect(connectMethod("api_key")).toBe("key");
		expect(connectMethod("oauth2")).toBe("consent");
		expect(connectMethod("installation")).toBe("consent");
	});

	it("puts each secret where connections expects it", () => {
		expect(keyCredential("bearer", "ghp_1")).toEqual({ token: "ghp_1" });
		expect(keyCredential("api_key", "lin_1")).toEqual({ api_key: "lin_1" });
		expect(keyCredential("basic", "secret", "asha")).toEqual({
			password: "secret",
			username: "asha",
		});
	});

	it("comes back to the connections page for the organization that started", () => {
		expect(connectReturnUri("http://localhost:3000", "org_1")).toBe(
			"http://localhost:3000/app/connections?organization=org_1",
		);
	});

	it("offers only the placements the viewer may use", () => {
		expect(
			placementOptions("oauth2", viewer).map((option) => option.value),
		).toEqual(["member", "team:team_1"]);
		expect(
			placementOptions("oauth2_client_credentials", {
				...viewer,
				isOrgAdmin: true,
			}).map((option) => option.value),
		).toEqual(["team:team_1", "organization"]);
	});

	it("explains a connect flow that did not finish", () => {
		expect(connectFailure("provider_denied")).toBe(
			"The provider did not allow the connection.",
		);
		expect(connectFailure("something_new")).toBe(
			"The connection did not finish. Start again.",
		);
	});
});

describe("connectionAccess", () => {
	it("follows connections' ownership rules", () => {
		expect(
			connectionAccess(connection({ member_id: "user-1" }), viewer),
		).toEqual({ change: true, disconnect: true });
		expect(
			connectionAccess(connection({ member_id: "user-2" }), viewer),
		).toEqual({ change: false, disconnect: false });
		expect(
			connectionAccess(connection({ member_id: "user-2" }), {
				...viewer,
				isOrgAdmin: true,
			}),
		).toEqual({ change: false, disconnect: true });
		expect(
			connectionAccess(
				connection({ scope: "team", team_id: "team_1" }),
				viewer,
			),
		).toEqual({ change: true, disconnect: true });
		expect(
			connectionAccess(connection({ scope: "organization" }), viewer),
		).toEqual({ change: false, disconnect: false });
	});

	it("says what stops a connection from working", () => {
		expect(connectionProblem(connection({}))).toBeUndefined();
		expect(connectionProblem(connection({ blocked: "app_disabled" }))).toBe(
			"Its tool is disabled for the organization.",
		);
		expect(
			connectionProblem(
				connection({ reason: "refresh_rejected", status: "needs_reconnect" }),
			),
		).toBe("The provider stopped accepting its access.");
	});
});
