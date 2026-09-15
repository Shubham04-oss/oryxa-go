import { createFileRoute } from "@tanstack/react-router";
import {
	SettingsPageBody,
	SettingsRow,
	SettingsSection,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";

export const Route = createFileRoute("/app/settings/organization/email")({
	component: OrganizationEmailPage,
	head: () => ({ meta: [{ title: "Email settings · The Weirdly ODD" }] }),
});

function OrganizationEmailPage() {
	return (
		<WorkspacePage
			description="Tune the optional system email sent for Odd Works."
			title="Email preferences"
		>
			<SettingsPageBody>
				<SettingsSection
					description="Choose which organization events should reach head admins."
					footer={<Button type="button">Save changes</Button>}
					id="organization-email-preferences"
					title="Organization updates"
				>
					<SettingsRow
						description="Receive upcoming renewal and subscription status notices."
						htmlFor="email-billing"
						title="Billing updates"
					>
						<Switch defaultChecked id="email-billing" />
					</SettingsRow>
					<SettingsRow
						description="Notify head admins when an organization membership changes."
						htmlFor="email-membership"
						title="Membership updates"
					>
						<Switch defaultChecked id="email-membership" />
					</SettingsRow>
				</SettingsSection>

				<SettingsSection
					description="Members can change this preference for each team."
					id="member-email-defaults"
					title="Member default"
				>
					<SettingsRow
						description="Enable email when a team call asks for a member's attention."
						htmlFor="email-team-calls"
						title="Team call notices"
					>
						<Switch defaultChecked id="email-team-calls" />
					</SettingsRow>
				</SettingsSection>

				<SettingsSection
					description="The system always sends email required to keep accounts usable."
					id="required-system-email"
					title="Required delivery"
				>
					<SettingsRow
						description="Verification, password reset, and invitation links cannot be disabled."
						title="Security and access"
					>
						<StatusBadge tone="neutral">Always on</StatusBadge>
					</SettingsRow>
				</SettingsSection>
			</SettingsPageBody>
		</WorkspacePage>
	);
}
