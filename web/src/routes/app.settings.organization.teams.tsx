import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { ArrowUpRight, Plus, UsersRound } from "lucide-react";
import type { CSSProperties } from "react";
import { QueryError } from "@/components/app/app-alert";
import {
	SettingsListEmpty,
	SettingsListSkeleton,
	SettingsPageBody,
	SettingsSection,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { CreateTeamDialog } from "@/components/teams/team-dialogs";
import { Button } from "@/components/ui/button";
import { managesMembers } from "@/lib/auth/client";
import { useMembership, useWorkspace } from "@/lib/auth/session";
import { flattenTeams, teamsQueryOptions, teamTree } from "@/lib/teams/client";

export const Route = createFileRoute("/app/settings/organization/teams")({
	component: OrganizationTeamsPage,
	head: () => ({ meta: [{ title: "Team settings · The Weirdly ODD" }] }),
});

function OrganizationTeamsPage() {
	const { organization } = useWorkspace();
	const isOrgAdmin = managesMembers(useMembership(organization.id)?.role);
	const teams = useQuery(teamsQueryOptions(organization.id));
	const navigate = useNavigate();

	return (
		<WorkspacePage
			actions={
				isOrgAdmin ? (
					<CreateTeamDialog
						canCreateTopLevel
						onCreated={(team) =>
							navigate({ search: { team: team.id }, to: "/app/teams" })
						}
						organizationId={organization.id}
						teams={teams.data ?? []}
						trigger={
							<Button type="button">
								<Plus data-icon="inline-start" />
								New team
							</Button>
						}
					/>
				) : undefined
			}
			description={`How ${organization.name} is organized into teams.`}
			title="Teams"
		>
			<SettingsPageBody>
				<SettingsSection
					description="Organization owners and admins manage every team. Team admins manage their own team and the teams nested under it."
					footer={
						<Button asChild variant="ghost">
							<Link to="/app/teams">
								Open teams
								<ArrowUpRight data-icon="inline-end" />
							</Link>
						</Button>
					}
					id="organization-teams"
					title="Organization teams"
				>
					{teams.isPending ? <SettingsListSkeleton /> : null}
					{teams.isError ? (
						<div className="settings-section-alert">
							<QueryError
								error={teams.error}
								fallback="Could not load teams"
								onRetry={teams.refetch}
							/>
						</div>
					) : null}
					{teams.data ? (
						<TeamOutline isOrgAdmin={isOrgAdmin} teams={teams.data} />
					) : null}
				</SettingsSection>
			</SettingsPageBody>
		</WorkspacePage>
	);
}

interface TeamOutlineProps {
	isOrgAdmin: boolean;
	teams: Parameters<typeof teamTree>[0];
}

function TeamOutline({ isOrgAdmin, teams }: TeamOutlineProps) {
	const nodes = flattenTeams(teamTree(teams));
	if (nodes.length === 0) {
		return (
			<SettingsListEmpty>
				{isOrgAdmin
					? "No teams yet. Create one to organize people around their work."
					: "You are not on any teams yet."}
			</SettingsListEmpty>
		);
	}

	return (
		<div className="settings-list">
			{nodes.map(({ depth, team }) => (
				<Link
					className="settings-list-row settings-list-link"
					key={team.id}
					search={{ team: team.id }}
					style={{ "--depth": depth } as CSSProperties}
					to="/app/teams"
				>
					<span aria-hidden="true" className="settings-list-icon">
						<UsersRound />
					</span>
					<div className="settings-list-copy">
						<strong>{team.name}</strong>
						<small>{teamRoleText(team.role)}</small>
					</div>
					{team.can_manage ? (
						<StatusBadge tone="admin">Can manage</StatusBadge>
					) : null}
				</Link>
			))}
		</div>
	);
}

function teamRoleText(role: "admin" | "member" | undefined): string {
	if (role === "admin") {
		return "You are an admin";
	}
	return role === "member" ? "You are a member" : "You are not on this team";
}
