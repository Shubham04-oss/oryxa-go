import { queryOptions } from "@tanstack/react-query";
import { z } from "zod";
import {
	type Invitation,
	listInvitations,
	listMembers,
	type Membership,
	type Role,
} from "./client";
import { organizationSchema } from "./schemas";

/** The prefix of everything cached from auth about one organization. */
export function organizationQueryKey(organizationId: string) {
	return ["auth", "organizations", organizationId] as const;
}

export function membersQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => listMembers(organizationId),
		queryKey: [...organizationQueryKey(organizationId), "members"],
	});
}

export function invitationsQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => listInvitations(organizationId),
		queryKey: [...organizationQueryKey(organizationId), "invitations"],
	});
}

/** Auth's handle rule: lowercase letters and digits, anything else one dash. */
export function slugify(value: string): string {
	return value
		.trim()
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-+|-+$/g, "");
}

// An empty handle is taken from the name, as auth does.
export const organizationProfileSchema = organizationSchema
	.extend({ slug: z.string() })
	.transform(({ name, slug }) => ({
		name,
		slug: slugify(slug) || slugify(name),
	}))
	.refine(({ slug }) => slug.length >= 2 && slug.length <= 63, {
		message: "Use 2 to 63 letters or numbers",
		path: ["slug"],
	});

export type OrganizationProfileInput = z.infer<
	typeof organizationProfileSchema
>;

const roleOrder: Record<Role, number> = { admin: 1, member: 2, owner: 0 };

function memberName(member: Pick<Membership, "display_name" | "email">) {
	return member.display_name?.trim() || member.email;
}

/** Owners first, then admins, then members, each group by name. */
export function sortMembers(members: Membership[]): Membership[] {
	return [...members].sort(
		(a, b) =>
			roleOrder[a.role] - roleOrder[b.role] ||
			memberName(a).localeCompare(memberName(b)),
	);
}

/** Whether auth lets a viewer remove `target`; leaving is separate. */
export function canRemoveMember(
	viewerRole: Role | undefined,
	target: Membership,
	viewerId: string,
): boolean {
	if (target.user_id === viewerId) {
		return false;
	}
	return (
		viewerRole === "owner" ||
		(viewerRole === "admin" && target.role === "member")
	);
}

/** Admins can only revoke the member invitations they could have sent. */
export function canRevokeInvitation(
	viewerRole: Role | undefined,
	invitation: Pick<Invitation, "role">,
): boolean {
	return (
		viewerRole === "owner" ||
		(viewerRole === "admin" && invitation.role === "member")
	);
}
