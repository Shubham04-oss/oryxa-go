import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { ShieldCheck, UserMinus, X } from "lucide-react";
import type { ReactNode } from "react";
import { QueryError } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import { displayName, initials, roleLabel } from "@/components/app/identity";
import { InviteMemberDialog } from "@/components/app/invite-member-dialog";
import {
	SettingsListEmpty,
	SettingsListSkeleton,
	SettingsPageBody,
	SettingsSection,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { Button } from "@/components/ui/button";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/components/ui/select";
import { ApiError, apiErrorMessage } from "@/lib/api/client";
import {
	changeMemberRole,
	type Invitation,
	type Membership,
	managesMembers,
	type Role,
	removeMember,
	revokeInvitation,
} from "@/lib/auth/client";
import {
	canRemoveMember,
	canRevokeInvitation,
	invitationsQueryOptions,
	membersQueryOptions,
	organizationQueryKey,
	sortMembers,
} from "@/lib/auth/organization";
import { useMembership, useWorkspace } from "@/lib/auth/session";
import { billingSummaryQueryOptions } from "@/lib/billing/client";
import { formatDate } from "@/lib/format";

export const Route = createFileRoute("/app/settings/organization/members")({
	component: OrganizationMembersPage,
	head: () => ({ meta: [{ title: "Members · The Weirdly ODD" }] }),
});

function OrganizationMembersPage() {
	const { organization } = useWorkspace();
	const role = useMembership(organization.id)?.role;
	const canManage = managesMembers(role);

	return (
		<WorkspacePage
			actions={
				canManage ? (
					<InviteMemberDialog
						canInviteAdmins={role === "owner"}
						organizationId={organization.id}
						organizationName={organization.name}
					/>
				) : undefined
			}
			description={`People in ${organization.name} and what they can do.`}
			title="Members"
		>
			<SettingsPageBody>
				<MemberDirectory viewerRole={role} />
				{canManage ? <PendingInvitations viewerRole={role} /> : null}
			</SettingsPageBody>
		</WorkspacePage>
	);
}

function MemberDirectory({ viewerRole }: { viewerRole: Role | undefined }) {
	const { organization, user } = useWorkspace();
	const members = useQuery(membersQueryOptions(organization.id));
	const billing = useQuery(billingSummaryQueryOptions(organization.id));
	const limit = billing.data?.entitlements.member_limit;

	let content: ReactNode;
	if (members.isPending) {
		content = <SettingsListSkeleton />;
	} else if (members.isError) {
		content = (
			<div className="settings-section-alert">
				<QueryError
					error={members.error}
					fallback="Could not load members"
					onRetry={members.refetch}
				/>
			</div>
		);
	} else {
		content = (
			<div className="settings-list">
				{sortMembers(members.data).map((member) => (
					<MemberRow
						key={member.user_id}
						member={member}
						viewerId={user.id}
						viewerRole={viewerRole}
					/>
				))}
			</div>
		);
	}

	return (
		<SettingsSection
			description="Owners manage everything, including billing. Admins manage members and teams."
			footer={
				members.data && limit !== undefined ? (
					<span>{memberCount(members.data.length, limit)}</span>
				) : undefined
			}
			id="member-directory"
			title="Directory"
		>
			{content}
		</SettingsSection>
	);
}

function memberCount(count: number, limit: number | null): string {
	const people = count === 1 ? "1 member" : `${count} members`;
	return limit === null ? people : `${people}, of the ${limit} the plan allows`;
}

interface MemberRowProps {
	member: Membership;
	viewerId: string;
	viewerRole: Role | undefined;
}

function MemberRow({ member, viewerId, viewerRole }: MemberRowProps) {
	const { organization } = useWorkspace();
	const queryClient = useQueryClient();
	const name = displayName(member);
	const refresh = () =>
		queryClient.invalidateQueries({
			queryKey: organizationQueryKey(organization.id),
		});
	const changeRole = useMutation({
		mutationFn: (role: Role) =>
			changeMemberRole(organization.id, member.user_id, role),
		onSettled: refresh,
	});
	const shownRole =
		changeRole.isPending && changeRole.variables
			? changeRole.variables
			: member.role;

	return (
		<div className="settings-list-row">
			<span aria-hidden="true" className="settings-avatar">
				{initials(name)}
			</span>
			<div className="settings-list-copy">
				<strong>{member.user_id === viewerId ? `${name} (you)` : name}</strong>
				<small>
					{member.display_name ? `${member.email} · ` : ""}
					Joined {formatDate(member.joined_at)}
				</small>
				{changeRole.isError ? (
					<small className="settings-list-error">
						{lastOwnerProblem(changeRole.error) ??
							apiErrorMessage(changeRole.error, "Could not change the role")}
					</small>
				) : null}
			</div>
			{viewerRole === "owner" ? (
				<Select
					disabled={changeRole.isPending}
					onValueChange={(value) => changeRole.mutate(value as Role)}
					value={shownRole}
				>
					<SelectTrigger
						aria-label={`Role for ${name}`}
						className="settings-role-select"
						size="sm"
					>
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="owner">Owner</SelectItem>
						<SelectItem value="admin">Admin</SelectItem>
						<SelectItem value="member">Member</SelectItem>
					</SelectContent>
				</Select>
			) : (
				<StatusBadge
					icon={member.role === "member" ? undefined : ShieldCheck}
					tone={member.role === "member" ? "neutral" : "admin"}
				>
					{roleLabel(member.role)}
				</StatusBadge>
			)}
			{canRemoveMember(viewerRole, member, viewerId) ? (
				<ConfirmDialog
					confirmLabel="Remove member"
					description={`${name} loses access to ${organization.name} and its teams at once. Their personal files and connections there are removed.`}
					explainFailure={lastOwnerProblem}
					onConfirm={async () => {
						await removeMember(organization.id, member.user_id);
						await refresh();
					}}
					title={`Remove ${name}?`}
					trigger={
						<Button
							aria-label={`Remove ${name}`}
							size="icon-sm"
							type="button"
							variant="ghost"
						>
							<UserMinus />
						</Button>
					}
				/>
			) : null}
		</div>
	);
}

// Auth refuses, as a conflict, any change that would leave no owner.
function lastOwnerProblem(error: unknown): string | undefined {
	return error instanceof ApiError && error.status === 409
		? "An organization always needs an owner. Make someone else an owner first."
		: undefined;
}

function PendingInvitations({ viewerRole }: { viewerRole: Role | undefined }) {
	const { organization } = useWorkspace();
	const invitations = useQuery(invitationsQueryOptions(organization.id));

	let content: ReactNode;
	if (invitations.isPending) {
		content = <SettingsListSkeleton />;
	} else if (invitations.isError) {
		content = (
			<div className="settings-section-alert">
				<QueryError
					error={invitations.error}
					fallback="Could not load invitations"
					onRetry={invitations.refetch}
				/>
			</div>
		);
	} else {
		const pending = invitations.data.filter(
			(invitation) => invitation.status === "pending",
		);
		content =
			pending.length === 0 ? (
				<SettingsListEmpty>No pending invitations.</SettingsListEmpty>
			) : (
				<div className="settings-list">
					{pending.map((invitation) => (
						<InvitationRow
							canRevoke={canRevokeInvitation(viewerRole, invitation)}
							invitation={invitation}
							key={invitation.id}
						/>
					))}
				</div>
			);
	}

	return (
		<SettingsSection
			description="Invitations work for 7 days. Revoking one stops its link from working."
			id="member-invitations"
			title="Pending invitations"
		>
			{content}
		</SettingsSection>
	);
}

interface InvitationRowProps {
	canRevoke: boolean;
	invitation: Invitation;
}

function InvitationRow({ canRevoke, invitation }: InvitationRowProps) {
	const { organization } = useWorkspace();
	const queryClient = useQueryClient();

	return (
		<div className="settings-list-row">
			<span aria-hidden="true" className="settings-avatar">
				{initials(invitation.email)}
			</span>
			<div className="settings-list-copy">
				<strong>{invitation.email}</strong>
				<small>
					Invited as {roleLabel(invitation.role).toLowerCase()} · expires{" "}
					{formatDate(invitation.expires_at)}
				</small>
			</div>
			{canRevoke ? (
				<ConfirmDialog
					confirmLabel="Revoke invitation"
					description={`The link sent to ${invitation.email} stops working.`}
					onConfirm={async () => {
						await revokeInvitation(organization.id, invitation.id);
						await queryClient.invalidateQueries({
							queryKey: invitationsQueryOptions(organization.id).queryKey,
						});
					}}
					title="Revoke this invitation?"
					trigger={
						<Button
							aria-label={`Revoke the invitation for ${invitation.email}`}
							size="icon-sm"
							type="button"
							variant="ghost"
						>
							<X />
						</Button>
					}
				/>
			) : null}
		</div>
	);
}
