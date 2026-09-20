import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowUpRight, Blocks } from "lucide-react";
import { QueryError } from "@/components/app/app-alert";
import { EmptyState } from "@/components/app/empty-state";
import {
	SettingsListSkeleton,
	SettingsPageBody,
	SettingsSection,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { ToolkitIcon } from "@/components/apps/toolkit-icon";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { useToolkitCatalog } from "@/hooks/use-toolkit-catalog";
import {
	type ConnectionGroup,
	connectionsQueryOptions,
	groupConnectionsByToolkit,
} from "@/lib/apps/client";
import { managesMembers } from "@/lib/auth/client";
import { useMembership, useWorkspace } from "@/lib/auth/session";

export const Route = createFileRoute("/app/settings/organization/tools")({
	component: OrganizationToolsPage,
	head: () => ({ meta: [{ title: "Tool access · The Weirdly ODD" }] }),
});

function toolkitName(toolkit: string): string {
	return toolkit
		.split(/[_-]/)
		.filter(Boolean)
		.map((word) => word.charAt(0).toUpperCase() + word.slice(1))
		.join(" ");
}

function OrganizationToolsPage() {
	const { organization } = useWorkspace();
	const isAdmin = managesMembers(useMembership(organization.id)?.role);

	return (
		<WorkspacePage
			description="Which apps the organization's members have connected."
			title="Tool access"
		>
			<SettingsPageBody>
				{isAdmin ? (
					<AdminAppsSummary organizationId={organization.id} />
				) : (
					<NonAdminNote />
				)}
			</SettingsPageBody>
		</WorkspacePage>
	);
}

function NonAdminNote() {
	return (
		<SettingsSection
			description="Members connect their own apps; there is nothing here to manage."
			id="apps-summary"
			title="Connected apps"
		>
			<Card className="service-placeholder">
				<span aria-hidden="true">
					<Blocks />
				</span>
				<strong>
					Organization owners and admins see who has connected what.
				</strong>
				<Button asChild size="sm" variant="ghost">
					<Link to="/app/connections">
						Connect your own apps
						<ArrowUpRight data-icon="inline-end" />
					</Link>
				</Button>
			</Card>
		</SettingsSection>
	);
}

interface AdminAppsSummaryProps {
	organizationId: string;
}

function AdminAppsSummary({ organizationId }: AdminAppsSummaryProps) {
	const connections = useQuery(
		connectionsQueryOptions(organizationId, { scope: "organization" }),
	);
	const groups = connections.data
		? groupConnectionsByToolkit(connections.data)
		: undefined;
	const catalog = useToolkitCatalog(
		(groups ?? []).map((group) => group.toolkit),
	);

	return (
		<SettingsSection
			description="Each member connects and disconnects their own apps from Apps & tools; every agent uses a member's apps only as that member."
			footer={
				<Button asChild variant="ghost">
					<Link to="/app/connections">
						Open connections
						<ArrowUpRight data-icon="inline-end" />
					</Link>
				</Button>
			}
			id="apps-summary"
			title="Connected apps"
		>
			{connections.isPending ? <SettingsListSkeleton /> : null}
			{connections.isError ? (
				<div className="settings-section-alert">
					<QueryError
						error={connections.error}
						fallback="Could not load connections"
						onRetry={connections.refetch}
					/>
				</div>
			) : null}
			{groups && groups.length === 0 ? (
				<EmptyState
					description="Once a member connects an app, it shows up here."
					icon={Blocks}
					title="No apps connected yet"
				/>
			) : null}
			{groups && groups.length > 0 ? (
				<div className="settings-list">
					{groups.map((group) => (
						<AppGroupRow
							group={group}
							key={group.toolkit}
							name={
								catalog.get(group.toolkit)?.name ?? toolkitName(group.toolkit)
							}
							logo={catalog.get(group.toolkit)?.logo}
						/>
					))}
				</div>
			) : null}
		</SettingsSection>
	);
}

interface AppGroupRowProps {
	group: ConnectionGroup;
	logo: string | undefined;
	name: string;
}

function AppGroupRow({ group, logo, name }: AppGroupRowProps) {
	const total = group.connections.length;
	const detail =
		group.attentionCount > 0
			? `${group.activeCount} active · ${group.attentionCount} need attention`
			: `${group.activeCount} active`;

	return (
		<div className="settings-list-row">
			<ToolkitIcon logo={logo} />
			<div className="settings-list-copy">
				<strong>{name}</strong>
				<small>
					{total} {total === 1 ? "member" : "members"} connected · {detail}
				</small>
			</div>
			<StatusBadge tone={group.attentionCount > 0 ? "warning" : "active"}>
				{group.attentionCount > 0 ? "Needs attention" : "All connected"}
			</StatusBadge>
		</div>
	);
}
