import { createFileRoute } from "@tanstack/react-router";
import {
	SettingsPageBody,
	SettingsRow,
	SettingsSection,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/components/ui/select";

export const Route = createFileRoute("/app/settings/organization/")({
	component: OrganizationGeneralPage,
	head: () => ({ meta: [{ title: "Organization · The Weirdly ODD" }] }),
});

function OrganizationGeneralPage() {
	return (
		<WorkspacePage
			description="Identity, ownership, and defaults for Odd Works."
			title="Organization"
		>
			<SettingsPageBody>
				<SettingsSection
					description="The name and handle shown throughout the workspace."
					footer={
						<>
							<span>Visible to every organization member.</span>
							<Button type="button">Save changes</Button>
						</>
					}
					id="organization-profile"
					title="Profile"
				>
					<SettingsRow
						description="Used in navigation, invitations, and emails."
						htmlFor="organization-name"
						title="Organization name"
					>
						<Input defaultValue="Odd Works" id="organization-name" />
					</SettingsRow>
					<SettingsRow
						description="A short identifier for links and system events."
						htmlFor="organization-handle"
						title="Handle"
					>
						<div className="settings-input-prefix">
							<span>/</span>
							<Input defaultValue="odd-works" id="organization-handle" />
						</div>
					</SettingsRow>
				</SettingsSection>

				<SettingsSection
					description="Applied when new members and teams are created."
					id="workspace-defaults"
					title="Workspace defaults"
				>
					<SettingsRow
						description="Used for activity dates and scheduled work."
						htmlFor="organization-timezone"
						title="Timezone"
					>
						<Select defaultValue="asia-kolkata">
							<SelectTrigger id="organization-timezone">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="asia-kolkata">Asia / Kolkata</SelectItem>
								<SelectItem value="utc">UTC</SelectItem>
								<SelectItem value="america-new-york">
									America / New York
								</SelectItem>
							</SelectContent>
						</Select>
					</SettingsRow>
					<SettingsRow
						description="Sets the first day in organization calendars."
						htmlFor="organization-week-start"
						title="Week starts on"
					>
						<Select defaultValue="monday">
							<SelectTrigger id="organization-week-start">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="monday">Monday</SelectItem>
								<SelectItem value="sunday">Sunday</SelectItem>
							</SelectContent>
						</Select>
					</SettingsRow>
				</SettingsSection>
			</SettingsPageBody>
		</WorkspacePage>
	);
}
