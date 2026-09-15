import { createFileRoute } from "@tanstack/react-router";
import { ArrowUpRight, Check } from "lucide-react";
import {
	SettingsPageBody,
	SettingsSection,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { Button } from "@/components/ui/button";

export const Route = createFileRoute("/app/settings/organization/billing")({
	component: OrganizationBillingPage,
	head: () => ({ meta: [{ title: "Billing · The Weirdly ODD" }] }),
});

function OrganizationBillingPage() {
	return (
		<WorkspacePage
			description="Plan, member allowance, and billing ownership."
			title="Billing"
		>
			<SettingsPageBody>
				<SettingsSection
					description="Only organization owners can change billing."
					footer={
						<Button type="button">
							Manage plan
							<ArrowUpRight data-icon="inline-end" />
						</Button>
					}
					id="billing-plan"
					title="Current plan"
				>
					<div className="settings-plan">
						<div>
							<span className="settings-plan-name">Unpaid</span>
							<StatusBadge tone="neutral">Current</StatusBadge>
						</div>
						<p>For small teams getting the organization ready.</p>
						<ul>
							<li>
								<Check />
								Up to 3 members
							</li>
							<li>
								<Check />
								Organization tools and connections
							</li>
							<li>
								<Check />
								Shared teams and files
							</li>
						</ul>
					</div>
				</SettingsSection>

				<SettingsSection
					description="Receipts and subscription notices go to the owner."
					id="billing-owner"
					title="Billing owner"
				>
					<div className="settings-list-row">
						<span className="settings-avatar" aria-hidden="true">
							Y
						</span>
						<div className="settings-list-copy">
							<strong>You</strong>
							<small>Organization owner</small>
						</div>
						<StatusBadge tone="admin">Owner</StatusBadge>
					</div>
				</SettingsSection>
			</SettingsPageBody>
		</WorkspacePage>
	);
}
