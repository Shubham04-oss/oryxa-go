import { describe, expect, it } from "vitest";
import type { CredentialTypeDescription } from "@/lib/connections/client";
import { scopeProblem, splitScopes } from "./scopes";

function method(
	over: Partial<CredentialTypeDescription> = {},
): CredentialTypeDescription {
	return {
		authorizes: true,
		client: true,
		credential_type: "oauth2",
		...over,
	};
}

describe("splitScopes", () => {
	it("takes scopes apart however they were separated", () => {
		expect(splitScopes(" tasks:read, tasks:write\n users:read ")).toEqual([
			"tasks:read",
			"tasks:write",
			"users:read",
		]);
	});

	it("reads nothing out of an empty field", () => {
		expect(splitScopes("   ")).toEqual([]);
	});
});

describe("scopeProblem", () => {
	const asana = method({
		allowed_scopes: ["default", "tasks:read", "tasks:write", "users:read"],
		exclusive_scopes: ["default"],
		required_scopes: ["users:read"],
	});

	it("passes scopes the provider offers", () => {
		expect(scopeProblem(["tasks:read", "users:read"], asana)).toBeUndefined();
	});

	it("names the scopes the provider does not offer", () => {
		expect(scopeProblem(["tasks:read", "wat", "eh"], asana)).toBe(
			"wat and eh are not scopes this tool offers.",
		);
	});

	it("allows an exclusive scope on its own", () => {
		expect(scopeProblem(["default"], asana)).toBeUndefined();
	});

	it("refuses an exclusive scope beside another", () => {
		expect(scopeProblem(["default", "tasks:read"], asana)).toBe(
			"default must be the only scope, because the provider refuses it beside others.",
		);
	});

	it("accepts anything where the provider publishes no ceiling", () => {
		expect(scopeProblem(["whatever"], method())).toBeUndefined();
	});
});
