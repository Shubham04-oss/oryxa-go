import { afterEach, describe, expect, it, vi } from "vitest";
import {
	acceptInvitation,
	changeMemberRole,
	createOrganization,
	deleteOrganization,
	getOrganization,
	getSession,
	inviteMember,
	listInvitations,
	listMembers,
	listReceivedInvitations,
	removeMember,
	requestPasswordReset,
	resendVerification,
	resetPassword,
	revokeInvitation,
	signIn,
	signOut,
	signUp,
	updateOrganization,
	verifyEmail,
} from "./client";

function json(body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
		status: 200,
	});
}

afterEach(() => vi.unstubAllGlobals());

describe("auth and organization API contracts", () => {
	it("preserves every account, organization, member, and invitation endpoint", async () => {
		const fetchMock = vi.fn().mockImplementation(() =>
			Promise.resolve(
				json({
					invitations: [],
					members: [],
					organizations: [],
					user: { id: "user_1" },
				}),
			),
		);
		vi.stubGlobal("fetch", fetchMock);

		await signIn({ email: "ada@example.test", password: "password123" });
		await signUp({
			displayName: "Ada",
			email: "ada@example.test",
			password: "password123",
		});
		await signOut();
		await verifyEmail("verify-token");
		await resendVerification("ada@example.test");
		await requestPasswordReset("ada@example.test");
		await resetPassword("reset-token", "new-password123");
		await getSession();
		await createOrganization({ name: "Odd Works" });
		await getOrganization("org / one");
		await updateOrganization("org_1", { name: "Odd Labs", slug: "odd-labs" });
		await deleteOrganization("org_1");
		await expect(listMembers("org_1")).resolves.toEqual([]);
		await changeMemberRole("org_1", "user / two", "admin");
		await removeMember("org_1", "user_2");
		await expect(listInvitations("org_1")).resolves.toEqual([]);
		await inviteMember("org_1", {
			email: "lin@example.test",
			role: "member",
		});
		await revokeInvitation("org_1", "invitation / one");
		await acceptInvitation("invite-token");
		await expect(listReceivedInvitations()).resolves.toEqual([]);

		expect(
			fetchMock.mock.calls.map(([url, init]) => [url, init.method]),
		).toEqual([
			["/v1/auth/password/login", "POST"],
			["/v1/auth/password/signup", "POST"],
			["/v1/auth/logout", "POST"],
			["/v1/auth/password/verify", "POST"],
			["/v1/auth/password/resend-verification", "POST"],
			["/v1/auth/password/forgot", "POST"],
			["/v1/auth/password/reset", "POST"],
			["/v1/auth/me", "GET"],
			["/v1/organizations", "POST"],
			["/v1/organizations/org%20%2F%20one", "GET"],
			["/v1/organizations/org_1", "PATCH"],
			["/v1/organizations/org_1", "DELETE"],
			["/v1/organizations/org_1/members", "GET"],
			["/v1/organizations/org_1/members/user%20%2F%20two", "PATCH"],
			["/v1/organizations/org_1/members/user_2", "DELETE"],
			["/v1/organizations/org_1/invitations", "GET"],
			["/v1/organizations/org_1/invitations", "POST"],
			["/v1/organizations/org_1/invitations/invitation%20%2F%20one", "DELETE"],
			["/v1/invitations/accept", "POST"],
			["/v1/invitations", "GET"],
		]);
	});
});
