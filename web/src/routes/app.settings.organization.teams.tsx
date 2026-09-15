import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowUpRight, Plus, UsersRound } from "lucide-react";
import {
	SettingsPageBody,
	SettingsRow,
	SettingsSection,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";

export const Route = createFileRoute("/app/settings/organization/teams")({
	component: OrganizationTeamsPage,
	head: () => ({ meta: [{ title: "Team settings · The Weirdly ODD" }] }),
});

const teams = [
	{ members: "4 members", name: "Product" },
	{ members: "3 members", name: "Operations" },
];

function OrganizationTeamsPage() {
	return (
		<WorkspacePage
			actions={
				<Button type="button">
					<Plus data-icon="inline-start" />
					New team
				</Button>
			}
			description="Team creation, visibility, and admin controls."
			title="Teams"
		>
			<SettingsPageBody>
				<SettingsSection
					description="Teams organize members, tools, and shared work."
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
					<div className="settings-list">
						{teams.map((team) => (
							<div className="settings-list-row" key={team.name}>
								<span className="settings-list-icon" aria-hidden="true">
									<UsersRound />
								</span>
								<div className="settings-list-copy">
									<strong>{team.name}</strong>
									<small>{team.members}</small>
								</div>
								<StatusBadge tone="active">Active</StatusBadge>
							</div>
						))}
					</div>
				</SettingsSection>

				<SettingsSection
					description="Organization admins retain management access to every team."
					id="team-access"
					title="Admin access"
				>
					<SettingsRow
						description="Allow team admins to invite existing organization members."
						htmlFor="team-admin-invites"
						title="Team admin invitations"
					>
						<Switch defaultChecked id="team-admin-invites" />
					</SettingsRow>
				</SettingsSection>
			</SettingsPageBody>
		</WorkspacePage>
	);
}
