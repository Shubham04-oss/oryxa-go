import { createFileRoute } from "@tanstack/react-router";
import { Plus, ShieldCheck } from "lucide-react";
import {
	SettingsPageBody,
	SettingsRow,
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
import { Switch } from "@/components/ui/switch";

export const Route = createFileRoute("/app/settings/organization/members")({
	component: OrganizationMembersPage,
	head: () => ({ meta: [{ title: "Members · The Weirdly ODD" }] }),
});

function OrganizationMembersPage() {
	return (
		<WorkspacePage
			actions={
				<Button type="button">
					<Plus data-icon="inline-start" />
					Invite member
				</Button>
			}
			description="People, invitations, and organization-wide roles."
			title="Members"
		>
			<SettingsPageBody>
				<SettingsSection
					description="Head admins can manage every organization service."
					id="member-directory"
					title="Directory"
				>
					<div className="settings-list-row">
						<span className="settings-avatar" aria-hidden="true">
							Y
						</span>
						<div className="settings-list-copy">
							<strong>You</strong>
							<small>Organization owner</small>
						</div>
						<StatusBadge icon={ShieldCheck} tone="admin">
							Head admin
						</StatusBadge>
						<StatusBadge tone="active">Active</StatusBadge>
					</div>
				</SettingsSection>

				<SettingsSection
					description="Defaults for invitations created by organization admins."
					id="member-access"
					title="Joining and access"
				>
					<SettingsRow
						description="Let admins invite people from the members page."
						htmlFor="member-invites"
						title="Member invitations"
					>
						<Switch defaultChecked id="member-invites" />
					</SettingsRow>
					<SettingsRow
						description="The organization role assigned after acceptance."
						htmlFor="default-member-role"
						title="Default role"
					>
						<Select defaultValue="member">
							<SelectTrigger id="default-member-role">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="member">Member</SelectItem>
								<SelectItem value="admin">Admin</SelectItem>
							</SelectContent>
						</Select>
					</SettingsRow>
				</SettingsSection>
			</SettingsPageBody>
		</WorkspacePage>
	);
}
