import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowUpRight } from "lucide-react";
import {
	SettingsPageBody,
	SettingsRow,
	SettingsSection,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { Button } from "@/components/ui/button";
import { useWorkspace } from "@/lib/auth/session";

export const Route = createFileRoute("/app/settings/organization/email")({
	component: OrganizationEmailPage,
	head: () => ({ meta: [{ title: "Email settings · The Weirdly ODD" }] }),
});

// Email keeps no preferences of its own: what it sends follows from events,
// the billing contact in Dodo, and each member's switch on each team.
function OrganizationEmailPage() {
	const { organization } = useWorkspace();

	return (
		<WorkspacePage
			description={`What The Weirdly ODD emails people in ${organization.name}, and who decides.`}
			title="Email"
		>
			<SettingsPageBody>
				<SettingsSection
					description="Some email always goes out, because accounts depend on it."
					id="email-always"
					title="Always sent"
				>
					<SettingsRow
						description="Email verification, password resets, and invitations, each with a link that works once."
						title="Account access"
					>
						<StatusBadge tone="neutral">Always on</StatusBadge>
					</SettingsRow>
					<SettingsRow
						description="When someone's role changes or they are removed, that person is told. No one else is emailed."
						title="Membership changes"
					>
						<StatusBadge tone="neutral">To that member</StatusBadge>
					</SettingsRow>
				</SettingsSection>

				<SettingsSection
					description="Billing notices reach the billing contact, not every admin."
					id="email-billing"
					title="Billing"
				>
					<SettingsRow
						description="Failed payments, upcoming renewals, and ended subscriptions go to the billing contact, which owners change in the billing portal."
						title="Billing notices"
					>
						<Button asChild size="sm" variant="ghost">
							<Link to="/app/settings/organization/billing">
								Open billing
								<ArrowUpRight data-icon="inline-end" />
							</Link>
						</Button>
					</SettingsRow>
				</SettingsSection>

				<SettingsSection
					description="Each member decides, team by team."
					id="email-teams"
					title="Team calls"
				>
					<SettingsRow
						description="When an assistant calls a team, its members get an email unless they turned it off for that team."
						title="Team call notices"
					>
						<Button asChild size="sm" variant="ghost">
							<Link to="/app/settings/profile">
								Your preferences
								<ArrowUpRight data-icon="inline-end" />
							</Link>
						</Button>
					</SettingsRow>
				</SettingsSection>
			</SettingsPageBody>
		</WorkspacePage>
	);
}
