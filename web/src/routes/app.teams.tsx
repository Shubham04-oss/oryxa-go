import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import {
	LogOut,
	Pencil,
	Plus,
	Trash2,
	UserMinus,
	UserPlus,
	UsersRound,
} from "lucide-react";
import type { CSSProperties } from "react";
import { QueryError } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import { EmptyState } from "@/components/app/empty-state";
import { displayName, initials } from "@/components/app/identity";
import {
	SettingsListEmpty,
	SettingsListSkeleton,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import {
	AddTeamMemberDialog,
	CreateTeamDialog,
	RenameTeamDialog,
} from "@/components/teams/team-dialogs";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { ApiError, apiErrorMessage } from "@/lib/api/client";
import { managesMembers } from "@/lib/auth/client";
import { useMembership, useWorkspace } from "@/lib/auth/session";
import { formatDate } from "@/lib/format";
import {
	changeTeamMemberRole,
	deleteTeam,
	flattenTeams,
	removeTeamMember,
	setTeamEmailNotices,
	type TeamMembership,
	type TeamRole,
	type TeamSummary,
	teamMembersQueryOptions,
	teamQueryOptions,
	teamsQueryKey,
	teamsQueryOptions,
	teamTrails,
	teamTree,
} from "@/lib/teams/client";

interface TeamsSearch {
	team?: string;
}

export const Route = createFileRoute("/app/teams")({
	validateSearch: (search: Record<string, unknown>): TeamsSearch => ({
		team:
			typeof search.team === "string" && search.team !== ""
				? search.team
				: undefined,
	}),
	component: TeamsPage,
	head: () => ({ meta: [{ title: "Teams · The Weirdly ODD" }] }),
});

function TeamsPage() {
	const { organization } = useWorkspace();
	const { team: selectedId } = Route.useSearch();
	const navigate = useNavigate({ from: Route.fullPath });
	const isOrgAdmin = managesMembers(useMembership(organization.id)?.role);
	const teams = useQuery(teamsQueryOptions(organization.id));
	const visible = teams.data ?? [];
	const selected = visible.find((team) => team.id === selectedId);
	const canCreate = isOrgAdmin || visible.some((team) => team.can_manage);

	function select(teamId: string | undefined) {
		return navigate({ search: { team: teamId } });
	}

	return (
		<WorkspacePage
			actions={
				canCreate ? (
					<CreateTeamDialog
						canCreateTopLevel={isOrgAdmin}
						onCreated={(team) => select(team.id)}
						organizationId={organization.id}
						teams={visible}
						trigger={
							<Button type="button">
								<Plus data-icon="inline-start" />
								New team
							</Button>
						}
					/>
				) : undefined
			}
			description="People organized around the work, so assistants reach the right ones."
			title="Teams"
		>
			{teams.isPending ? (
				<Card className="workspace-panel">
					<SettingsListSkeleton />
				</Card>
			) : null}
			{teams.isError ? (
				<QueryError
					error={teams.error}
					fallback="Could not load teams"
					onRetry={teams.refetch}
				/>
			) : null}
			{teams.data?.length === 0 ? (
				<Card className="workspace-panel">
					<EmptyState
						description={
							isOrgAdmin
								? "Create a team to group people, then nest teams inside it as the work grows."
								: "When someone adds you to a team, it shows up here."
						}
						icon={UsersRound}
						title={isOrgAdmin ? "No teams yet" : "You are not on a team yet"}
					/>
				</Card>
			) : null}
			{teams.data && teams.data.length > 0 ? (
				<div className="teams-layout">
					<TeamList selectedId={selected?.id} teams={teams.data} />
					{selected ? (
						<TeamPanel
							isOrgAdmin={isOrgAdmin}
							key={selected.id}
							onDeleted={() => select(undefined)}
							onSelect={select}
							team={selected}
							teams={teams.data}
						/>
					) : (
						<Card className="workspace-panel teams-panel-empty">
							<p>Choose a team to see its people.</p>
						</Card>
					)}
				</div>
			) : null}
		</WorkspacePage>
	);
}

interface TeamListProps {
	selectedId: string | undefined;
	teams: TeamSummary[];
}

function TeamList({ selectedId, teams }: TeamListProps) {
	return (
		<Card className="workspace-panel">
			<nav aria-label="Teams" className="teams-tree">
				{flattenTeams(teamTree(teams)).map(({ depth, team }) => (
					<Link
						aria-current={team.id === selectedId ? "page" : undefined}
						className="teams-tree-item"
						data-active={team.id === selectedId}
						key={team.id}
						search={{ team: team.id }}
						style={{ "--depth": depth } as CSSProperties}
						to="/app/teams"
					>
						<UsersRound aria-hidden="true" />
						<span>{team.name}</span>
						{team.role ? (
							<small>{team.role === "admin" ? "Admin" : "Member"}</small>
						) : null}
					</Link>
				))}
			</nav>
		</Card>
	);
}

interface TeamPanelProps {
	isOrgAdmin: boolean;
	onDeleted: () => void;
	onSelect: (teamId: string) => void;
	team: TeamSummary;
	teams: TeamSummary[];
}

function TeamPanel({
	isOrgAdmin,
	onDeleted,
	onSelect,
	team,
	teams,
}: TeamPanelProps) {
	const { organization, user } = useWorkspace();
	const queryClient = useQueryClient();
	const detail = useQuery(teamQueryOptions(organization.id, team.id));
	const members = useQuery(teamMembersQueryOptions(organization.id, team.id));
	const trail = teamTrails(teams).get(team.id) ?? team.name;
	const canManage = detail.data?.can_manage ?? team.can_manage;
	const membership = detail.data?.membership ?? null;
	const headingId = `team-${team.id}-people`;

	return (
		<Card className="workspace-panel team-detail">
			<header className="team-detail-header">
				<div>
					{trail === team.name ? null : (
						<p className="team-detail-trail">{trail}</p>
					)}
					<h2>{team.name}</h2>
				</div>
				{canManage ? (
					<div className="team-detail-actions">
						<RenameTeamDialog
							organizationId={organization.id}
							team={team}
							trigger={
								<Button size="sm" type="button" variant="ghost">
									<Pencil data-icon="inline-start" />
									Rename
								</Button>
							}
						/>
						<CreateTeamDialog
							canCreateTopLevel={false}
							onCreated={(created) => onSelect(created.id)}
							organizationId={organization.id}
							parentId={team.id}
							teams={teams}
							trigger={
								<Button size="sm" type="button" variant="ghost">
									<Plus data-icon="inline-start" />
									Nested team
								</Button>
							}
						/>
						<ConfirmDialog
							confirmLabel="Delete team"
							confirmation={team.name}
							description={`${team.name} and every team inside it are deleted, with their files and connections.`}
							onConfirm={async () => {
								await deleteTeam(organization.id, team.id);
								onDeleted();
								await queryClient.invalidateQueries({
									queryKey: teamsQueryKey(organization.id),
								});
							}}
							title={`Delete ${team.name}?`}
							trigger={
								<Button
									aria-label={`Delete ${team.name}`}
									size="icon-sm"
									type="button"
									variant="ghost"
								>
									<Trash2 />
								</Button>
							}
						/>
					</div>
				) : null}
			</header>

			{membership ? (
				<EmailNotice membership={membership} teamId={team.id} />
			) : null}
			{!membership && isOrgAdmin ? (
				<p className="team-detail-note">
					You manage this team as an organization admin, without being on it.
				</p>
			) : null}

			<section aria-labelledby={headingId} className="team-detail-section">
				<header>
					<h3 id={headingId}>People</h3>
					{canManage && members.data ? (
						<AddTeamMemberDialog
							memberIds={new Set(members.data.map((member) => member.user_id))}
							organizationId={organization.id}
							teamId={team.id}
							teamName={team.name}
							trigger={
								<Button size="sm" type="button" variant="ghost">
									<UserPlus data-icon="inline-start" />
									Add people
								</Button>
							}
						/>
					) : null}
				</header>
				{members.isPending ? <SettingsListSkeleton /> : null}
				{members.isError ? (
					<div className="settings-section-alert">
						<QueryError
							error={members.error}
							fallback="Could not load the team's people"
							onRetry={members.refetch}
						/>
					</div>
				) : null}
				{members.data?.length === 0 ? (
					<SettingsListEmpty>No one is on this team yet.</SettingsListEmpty>
				) : null}
				{members.data && members.data.length > 0 ? (
					<div className="settings-list">
						{members.data.map((member) => (
							<TeamMemberRow
								canManage={canManage}
								key={member.user_id}
								member={member}
								teamId={team.id}
								teamName={team.name}
								viewerId={user.id}
							/>
						))}
					</div>
				) : null}
			</section>
		</Card>
	);
}

interface EmailNoticeProps {
	membership: TeamMembership;
	teamId: string;
}

function EmailNotice({ membership, teamId }: EmailNoticeProps) {
	const { organization } = useWorkspace();
	const queryClient = useQueryClient();
	const toggle = useMutation({
		mutationFn: (notify: boolean) =>
			setTeamEmailNotices(organization.id, teamId, notify),
		onSettled: () =>
			queryClient.invalidateQueries({
				queryKey: teamQueryOptions(organization.id, teamId).queryKey,
			}),
	});
	const checked =
		toggle.isPending && toggle.variables !== undefined
			? toggle.variables
			: membership.notify_email;

	return (
		<div className="team-detail-notice">
			<div>
				<strong>Email me when this team is called</strong>
				<small className={toggle.isError ? "settings-list-error" : undefined}>
					{toggle.isError
						? apiErrorMessage(toggle.error, "Could not save that")
						: "Only you see this switch."}
				</small>
			</div>
			<Switch
				aria-label="Email me when this team is called"
				checked={checked}
				disabled={toggle.isPending}
				onCheckedChange={(value) => toggle.mutate(value)}
			/>
		</div>
	);
}

interface TeamMemberRowProps {
	canManage: boolean;
	member: TeamMembership;
	teamId: string;
	teamName: string;
	viewerId: string;
}

function TeamMemberRow({
	canManage,
	member,
	teamId,
	teamName,
	viewerId,
}: TeamMemberRowProps) {
	const { organization } = useWorkspace();
	const queryClient = useQueryClient();
	const name = displayName(member);
	const isViewer = member.user_id === viewerId;
	const refresh = () =>
		queryClient.invalidateQueries({ queryKey: teamsQueryKey(organization.id) });
	const changeRole = useMutation({
		mutationFn: (role: TeamRole) =>
			changeTeamMemberRole(organization.id, teamId, member.user_id, role),
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
				<strong>{isViewer ? `${name} (you)` : name}</strong>
				<small>
					{member.display_name ? `${member.email} · ` : ""}
					Added {formatDate(member.joined_at)}
				</small>
				{changeRole.isError ? (
					<small className="settings-list-error">
						{lastAdminProblem(changeRole.error) ??
							apiErrorMessage(changeRole.error, "Could not change the role")}
					</small>
				) : null}
			</div>
			{canManage ? (
				<Select
					disabled={changeRole.isPending}
					onValueChange={(value) => changeRole.mutate(value as TeamRole)}
					value={shownRole}
				>
					<SelectTrigger
						aria-label={`Team role for ${name}`}
						className="settings-role-select"
						size="sm"
					>
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="admin">Admin</SelectItem>
						<SelectItem value="member">Member</SelectItem>
					</SelectContent>
				</Select>
			) : (
				<StatusBadge tone={member.role === "admin" ? "admin" : "neutral"}>
					{member.role === "admin" ? "Admin" : "Member"}
				</StatusBadge>
			)}
			{isViewer || canManage ? (
				<ConfirmDialog
					confirmLabel={isViewer ? "Leave team" : "Remove"}
					description={
						isViewer
							? `You leave ${teamName}. A team admin can add you back.`
							: `${name} leaves ${teamName}.`
					}
					explainFailure={lastAdminProblem}
					onConfirm={async () => {
						await removeTeamMember(organization.id, teamId, member.user_id);
						await refresh();
					}}
					title={
						isViewer ? `Leave ${teamName}?` : `Remove ${name} from ${teamName}?`
					}
					trigger={
						<Button
							aria-label={isViewer ? `Leave ${teamName}` : `Remove ${name}`}
							size="icon-sm"
							type="button"
							variant="ghost"
						>
							{isViewer ? <LogOut /> : <UserMinus />}
						</Button>
					}
				/>
			) : null}
		</div>
	);
}

// Teams refuses, as a conflict, any change that would leave a team without an admin.
function lastAdminProblem(error: unknown): string | undefined {
	return error instanceof ApiError && error.status === 409
		? "A team always needs an admin. Make someone else an admin first."
		: undefined;
}
