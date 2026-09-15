import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowUpRight } from "lucide-react";
import {
	SettingsPageBody,
	SettingsSection,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { ToolGlyph, toolOptions } from "@/lib/tools/catalog";

export const Route = createFileRoute("/app/settings/organization/tools")({
	component: OrganizationToolsPage,
	head: () => ({ meta: [{ title: "Tool settings · The Weirdly ODD" }] }),
});

const initiallyEnabled = new Set(["github", "google", "slack"]);

function OrganizationToolsPage() {
	return (
		<WorkspacePage
			description="Choose which tools members can connect and use."
			title="Tools"
		>
			<SettingsPageBody>
				<SettingsSection
					description="Enabling a tool makes its connection flow available to members."
					footer={
						<>
							<span>
								Connections and credentials remain separately controlled.
							</span>
							<Button asChild variant="ghost">
								<Link to="/app/connections">
									Open connections
									<ArrowUpRight data-icon="inline-end" />
								</Link>
							</Button>
						</>
					}
					id="enabled-tools"
					title="Available tools"
				>
					<div className="settings-list">
						{toolOptions.map((tool) => (
							<div className="settings-list-row" key={tool.provider}>
								<span className="settings-list-icon" aria-hidden="true">
									<ToolGlyph provider={tool.provider} />
								</span>
								<div className="settings-list-copy">
									<strong>{tool.name}</strong>
									<small>{tool.description}</small>
								</div>
								<Switch
									aria-label={`Enable ${tool.name}`}
									defaultChecked={initiallyEnabled.has(tool.provider)}
								/>
							</div>
						))}
					</div>
				</SettingsSection>
			</SettingsPageBody>
		</WorkspacePage>
	);
}
