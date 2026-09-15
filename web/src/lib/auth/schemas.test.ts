import { describe, expect, it } from "vitest";
import {
	inviteMemberSchema,
	organizationSchema,
	signInSchema,
	signUpSchema,
} from "./schemas";

describe("auth schemas", () => {
	it("accepts a valid sign in", () => {
		expect(
			signInSchema.safeParse({
				email: "member@example.com",
				password: "password",
			}).success,
		).toBe(true);
	});

	it("applies the auth service password policy to sign up", () => {
		expect(
			signUpSchema.safeParse({
				displayName: "Ravi Shah",
				email: "ravi@example.com",
				password: "Short1!",
			}).success,
		).toBe(false);

		expect(
			signUpSchema.safeParse({
				displayName: "Ravi Shah",
				email: "ravi@example.com",
				password: "LongEnough12!",
			}).success,
		).toBe(true);
	});

	it("trims and validates organization names", () => {
		expect(organizationSchema.parse({ name: "  Odd Works  " })).toEqual({
			name: "Odd Works",
		});
		expect(organizationSchema.safeParse({ name: "   " }).success).toBe(false);
	});

	it("validates member invitations", () => {
		expect(
			inviteMemberSchema.parse({
				email: "  member@example.com ",
				role: "member",
			}),
		).toEqual({ email: "member@example.com", role: "member" });
		expect(
			inviteMemberSchema.safeParse({ email: "member", role: "owner" }).success,
		).toBe(false);
	});
});
