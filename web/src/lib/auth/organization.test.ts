import { describe, expect, it } from "vitest";
import type { Membership, Role } from "./client";
import {
	canRemoveMember,
	canRevokeInvitation,
	organizationProfileSchema,
	slugify,
	sortMembers,
} from "./organization";

function member(userId: string, role: Role, displayName?: string): Membership {
	return {
		display_name: displayName,
		email: `${userId}@example.com`,
		joined_at: "2026-09-15T00:00:00Z",
		organization_id: "org_1",
		role,
		user_id: userId,
	};
}

describe("organization profile", () => {
	it("derives a handle the way auth does", () => {
		expect(slugify("  Odd Works!  ")).toBe("odd-works");
		expect(slugify("--Weird__Labs--")).toBe("weird-labs");
	});

	it("takes the handle from the name when nothing of it is left", () => {
		expect(
			organizationProfileSchema.parse({ name: " Odd Works ", slug: "!!" }),
		).toEqual({ name: "Odd Works", slug: "odd-works" });
		expect(
			organizationProfileSchema.safeParse({ name: "X", slug: "" }).success,
		).toBe(false);
	});
});

describe("member management", () => {
	it("lists owners, then admins, then members", () => {
		const sorted = sortMembers([
			member("user-3", "member", "Zed"),
			member("user-2", "admin"),
			member("user-4", "member", "Ada"),
			member("user-1", "owner"),
		]);
		expect(sorted.map((entry) => entry.user_id)).toEqual([
			"user-1",
			"user-2",
			"user-4",
			"user-3",
		]);
	});

	it("lets admins remove and revoke only members", () => {
		expect(canRemoveMember("admin", member("user-2", "member"), "user-1")).toBe(
			true,
		);
		expect(canRemoveMember("admin", member("user-2", "admin"), "user-1")).toBe(
			false,
		);
		expect(canRemoveMember("owner", member("user-2", "owner"), "user-1")).toBe(
			true,
		);
		expect(canRemoveMember("owner", member("user-1", "owner"), "user-1")).toBe(
			false,
		);
		expect(
			canRemoveMember("member", member("user-2", "member"), "user-1"),
		).toBe(false);
		expect(canRevokeInvitation("admin", { role: "admin" })).toBe(false);
		expect(canRevokeInvitation("admin", { role: "member" })).toBe(true);
	});
});
