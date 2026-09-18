import {
	useMutation,
	useQueries,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { UsersRound } from "lucide-react";
import type { ReactNode } from "react";
import { AppAlert, QueryError } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import { initials, roleLabel } from "@/components/app/identity";
import {
	SettingsListEmpty,
	SettingsListSkeleton,
	SettingsPageBody,
	SettingsRow,
	SettingsSection,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { ApiError, apiErrorMessage } from "@/lib/api/client";
import {
	agentGrantQueryOptions,
	authorizeAgentGrant,
	revokeAgentGrant,
} from "@/lib/auth/agent-grant";
import {
	authErrorMessage,
	removeMember,
	requestPasswordReset,
	signOut,
} from "@/lib/auth/client";
import {
	organizationQueryOptions,
	reloadSession,
	useWorkspace,
} from "@/lib/auth/session";
import { formatDate } from "@/lib/format";
import {
	setTeamEmailNotices,
	type TeamSummary,
	teamQueryOptions,
	teamsQueryOptions,
} from "@/lib/teams/client";

export const Route = createFileRoute("/app/settings/profile")({
	component: ProfileSettingsPage,
	head: () => ({ meta: [{ title: "Account · The Weirdly ODD" }] }),
});

function ProfileSettingsPage() {
	const { user } = useWorkspace();

	return (
		<WorkspacePage
			description="Your profile, organizations, and email preferences."
			title="Account settings"
		>
			<SettingsPageBody>
				<SettingsSection
					description="Your name and email come from how you signed up."
					id="account-profile"
					title="Profile"
				>
					<SettingsRow
						description="Shown to people in your organizations."
						title="Name"
					>
						<span className="settings-value">
							{user.display_name?.trim() || "Not set"}
						</span>
					</SettingsRow>
					<SettingsRow
						description="Where sign-in links and notices go."
						title="Email"
					>
						<span className="settings-value">{user.email}</span>
					</SettingsRow>
				</SettingsSection>
				<YourOrganizations />
				<TeamCallEmails />
				<AgentAccessSection />
				<SignIn />
			</SettingsPageBody>
		</WorkspacePage>
	);
}

function YourOrganizations() {
	const { organization: current, organizations, user } = useWorkspace();
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const details = useQueries({
		queries: organizations.map((organization) =>
			organizationQueryOptions(organization.id),
		),
	});

	return (
		<SettingsSection
			description="Leaving removes your access, and your personal files and connections in that organization."
			id="account-organizations"
			title="Organizations"
		>
			<div className="settings-list">
				{organizations.map((organization, index) => {
					const role = details[index]?.data?.membership.role;
					return (
						<div className="settings-list-row" key={organization.id}>
							<span
								aria-hidden="true"
								className="settings-list-icon settings-organization-mark"
							>
								{initials(organization.name)}
							</span>
							<div className="settings-list-copy">
								<strong>{organization.name}</strong>
								<small>
									{role ? roleLabel(role) : "Checking your role"}
									{organization.id === current.id ? " · open now" : ""}
								</small>
							</div>
							<ConfirmDialog
								confirmLabel="Leave organization"
								description={`You lose access to ${organization.name} and its teams. Someone would need to invite you back.`}
								explainFailure={(error) =>
									error instanceof ApiError && error.status === 409
										? "You are its only owner. Make someone else an owner first, or delete the organization."
										: undefined
								}
								onConfirm={async () => {
									await removeMember(organization.id, user.id);
									await reloadSession(queryClient);
									await navigate({ to: "/app" });
								}}
								title={`Leave ${organization.name}?`}
								trigger={
									<Button size="sm" type="button" variant="ghost">
										Leave
									</Button>
								}
							/>
						</div>
					);
				})}
			</div>
		</SettingsSection>
	);
}

function TeamCallEmails() {
	const { organization } = useWorkspace();
	const teams = useQuery(teamsQueryOptions(organization.id));
	const mine = teams.data?.filter((team) => team.role !== undefined) ?? [];

	let content: ReactNode;
	if (teams.isPending) {
		content = <SettingsListSkeleton />;
	} else if (teams.isError) {
		content = (
			<div className="settings-section-alert">
				<QueryError
					error={teams.error}
					fallback="Could not load your teams"
					onRetry={teams.refetch}
				/>
			</div>
		);
	} else if (mine.length === 0) {
		content = (
			<SettingsListEmpty>
				You are not on any teams in {organization.name}.
			</SettingsListEmpty>
		);
	} else {
		content = (
			<div className="settings-list">
				{mine.map((team) => (
					<TeamCallEmailRow key={team.id} team={team} />
				))}
			</div>
		);
	}

	return (
		<SettingsSection
			description={`When an assistant calls one of your teams in ${organization.name}, you get an email unless you turn it off here.`}
			id="account-team-emails"
			title="Team call emails"
		>
			{content}
		</SettingsSection>
	);
}

function TeamCallEmailRow({ team }: { team: TeamSummary }) {
	const { organization } = useWorkspace();
	const queryClient = useQueryClient();
	const detail = useQuery(teamQueryOptions(organization.id, team.id));
	const toggle = useMutation({
		mutationFn: (notify: boolean) =>
			setTeamEmailNotices(organization.id, team.id, notify),
		onSettled: () =>
			queryClient.invalidateQueries({
				queryKey: teamQueryOptions(organization.id, team.id).queryKey,
			}),
	});
	const saved = detail.data?.membership?.notify_email ?? false;
	const checked =
		toggle.isPending && toggle.variables !== undefined
			? toggle.variables
			: saved;
	let note = checked ? "Emails you when the team is called" : "No emails";
	if (toggle.isError) {
		note = apiErrorMessage(toggle.error, "Could not save that");
	}

	return (
		<div className="settings-list-row">
			<span aria-hidden="true" className="settings-list-icon">
				<UsersRound />
			</span>
			<div className="settings-list-copy">
				<strong>{team.name}</strong>
				<small className={toggle.isError ? "settings-list-error" : undefined}>
					{note}
				</small>
			</div>
			<Switch
				aria-label={`Team call emails for ${team.name}`}
				checked={checked}
				disabled={detail.isPending || toggle.isPending}
				onCheckedChange={(value) => toggle.mutate(value)}
			/>
		</div>
	);
}

function AgentAccessSection() {
	const grant = useQuery(agentGrantQueryOptions());
	const queryClient = useQueryClient();
	const authorize = useMutation({
		mutationFn: () =>
			authorizeAgentGrant(`${window.location.origin}/app/settings/profile`),
		onSuccess: ({ authorization_url }) => {
			window.location.assign(authorization_url);
		},
	});

	let content: ReactNode;
	if (grant.isPending) {
		content = (
			<SettingsRow description="Checking…" title="Agent access">
				<Skeleton className="h-7 w-20" />
			</SettingsRow>
		);
	} else if (grant.isError) {
		content =
			grant.error instanceof ApiError && grant.error.status === 404 ? (
				<SettingsListEmpty>Not available on this server.</SettingsListEmpty>
			) : (
				<div className="settings-section-alert">
					<QueryError
						error={grant.error}
						fallback="Could not check agent access"
						onRetry={grant.refetch}
					/>
				</div>
			);
	} else if (grant.data.granted) {
		content = (
			<SettingsRow
				description={
					grant.data.created_at
						? `Granted ${formatDate(grant.data.created_at)}.`
						: "Granted."
				}
				title="Agent access"
			>
				<div className="agent-grant-controls">
					<StatusBadge tone="active">On</StatusBadge>
					<ConfirmDialog
						confirmLabel="Revoke access"
						description="Krude can no longer act for you while you're away. It can still help while you're here, and you can grant this again any time."
						onConfirm={async () => {
							await revokeAgentGrant();
							await queryClient.invalidateQueries({
								queryKey: agentGrantQueryOptions().queryKey,
							});
						}}
						title="Revoke agent access?"
						trigger={
							<Button size="sm" type="button" variant="ghost">
								Revoke
							</Button>
						}
					/>
				</div>
			</SettingsRow>
		);
	} else {
		content = (
			<>
				{authorize.isError ? (
					<div className="settings-section-alert">
						<AppAlert>
							{apiErrorMessage(authorize.error, "Could not start that")}
						</AppAlert>
					</div>
				) : null}
				<SettingsRow
					description="Off. Krude will ask you directly whenever it needs something while you're away."
					title="Agent access"
				>
					<Button
						disabled={authorize.isPending}
						onClick={() => authorize.mutate()}
						size="sm"
						type="button"
					>
						{authorize.isPending ? "Redirecting…" : "Turn on"}
					</Button>
				</SettingsRow>
			</>
		);
	}

	return (
		<SettingsSection
			description="When events from your connected apps wake Krude, it acts with your own identity through this grant, and writes still ask you first. You can withdraw it any time."
			id="account-agent-access"
			title="Let Krude act for you while you're away"
		>
			{content}
		</SettingsSection>
	);
}

function SignIn() {
	const { user } = useWorkspace();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const reset = useMutation({
		mutationFn: () => requestPasswordReset(user.email),
	});

	async function leave() {
		// Even if signing out fails, sign-in lets the member choose an account.
		await signOut().catch(() => undefined);
		await navigate({ to: "/auth/sign-in" });
		queryClient.clear();
	}

	return (
		<SettingsSection
			description="How you get into The Weirdly ODD."
			id="account-sign-in"
			title="Sign-in"
		>
			{reset.isError ? (
				<div className="settings-section-alert">
					<AppAlert>
						{authErrorMessage(reset.error, "Could not send a reset link")}
					</AppAlert>
				</div>
			) : null}
			<SettingsRow
				description={
					reset.isSuccess
						? `A reset link is on its way to ${user.email}.`
						: "We email you a link to choose a new password. If you sign in with Google, change it with Google."
				}
				title="Password"
			>
				<Button
					disabled={reset.isPending || reset.isSuccess}
					onClick={() => reset.mutate()}
					size="sm"
					type="button"
					variant="ghost"
				>
					{reset.isPending ? "Sending…" : "Send reset link"}
				</Button>
			</SettingsRow>
			<SettingsRow
				description="Signs you out of this browser."
				title="Sign out"
			>
				<Button onClick={leave} size="sm" type="button" variant="ghost">
					Sign out
				</Button>
			</SettingsRow>
		</SettingsSection>
	);
}
