import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowUpRight, Trash2 } from "lucide-react";
import { useState } from "react";
import { QueryError } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import {
	SettingsListSkeleton,
	SettingsPageBody,
	SettingsSection,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { EnableToolDialog } from "@/components/tools/enable-tool-dialog";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { apiErrorMessage } from "@/lib/api/client";
import { managesMembers } from "@/lib/auth/client";
import { useMembership, useWorkspace } from "@/lib/auth/session";
import {
	type App,
	appsQueryOptions,
	connectionsQueryKey,
	deleteApp,
	type ProviderDescription,
	providersQueryOptions,
	setAppEnabled,
} from "@/lib/connections/client";
import {
	credentialTypeNames,
	describeTools,
	ToolGlyph,
	type ToolOption,
} from "@/lib/tools/catalog";

export const Route = createFileRoute("/app/settings/organization/tools")({
	component: OrganizationToolsPage,
	head: () => ({ meta: [{ title: "Tool settings · The Weirdly ODD" }] }),
});

// Onboarding keeps the tools picked during setup here until they are enabled.
function readOnboardingPicks(): ReadonlySet<string> {
	try {
		const stored: unknown = JSON.parse(
			sessionStorage.getItem("weirdly-odd:onboarding-tools") ?? "[]",
		);
		return new Set(
			Array.isArray(stored)
				? stored.filter((value): value is string => typeof value === "string")
				: [],
		);
	} catch {
		return new Set();
	}
}

function withEnabled(
	providers: ProviderDescription[],
	apps: App[],
): ProviderDescription[] {
	const known = new Set(providers.map((provider) => provider.provider));
	const retired = [...new Set(apps.map((app) => app.provider))]
		.filter((provider) => !known.has(provider))
		.map((provider) => ({ credential_types: [], provider }));
	return [...providers, ...retired];
}

function OrganizationToolsPage() {
	const { organization } = useWorkspace();
	const isAdmin = managesMembers(useMembership(organization.id)?.role);
	const apps = useQuery(appsQueryOptions(organization.id));
	const providers = useQuery(providersQueryOptions());
	const [picked] = useState(readOnboardingPicks);
	const tools =
		apps.data && providers.data
			? describeTools(withEnabled(providers.data, apps.data))
			: undefined;

	return (
		<WorkspacePage
			description="Choose which tools members can connect and use."
			title="Tools"
		>
			<SettingsPageBody>
				<SettingsSection
					description="Enabling a tool lets members connect their accounts to it. A tool can be enabled more than one way."
					footer={
						<>
							<span>
								{isAdmin
									? "Connections to a disabled tool stop working until it is enabled again."
									: "Organization owners and admins choose the tools."}
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
					{apps.isPending || providers.isPending ? (
						<SettingsListSkeleton />
					) : null}
					{apps.isError || providers.isError ? (
						<div className="settings-section-alert">
							<QueryError
								error={apps.error ?? providers.error}
								fallback="Could not load tools"
								onRetry={apps.isError ? apps.refetch : providers.refetch}
							/>
						</div>
					) : null}
					{tools && apps.data ? (
						<div className="settings-list">
							{tools.map((tool) => (
								<ToolRow
									apps={apps.data.filter(
										(app) => app.provider === tool.provider,
									)}
									isAdmin={isAdmin}
									key={tool.provider}
									picked={picked.has(tool.provider)}
									tool={tool}
								/>
							))}
						</div>
					) : null}
				</SettingsSection>
			</SettingsPageBody>
		</WorkspacePage>
	);
}

interface ToolRowProps {
	apps: App[];
	isAdmin: boolean;
	picked: boolean;
	tool: ToolOption;
}

function ToolRow({ apps, isAdmin, picked, tool }: ToolRowProps) {
	const { organization } = useWorkspace();
	const enabledTypes = new Set(apps.map((app) => app.credential_type));
	const canAddMethod = tool.credentialTypes.some(
		(type) => !enabledTypes.has(type.credential_type),
	);

	return (
		<div className="tool-row">
			<div className="settings-list-row">
				<span aria-hidden="true" className="settings-list-icon">
					<ToolGlyph provider={tool.provider} />
				</span>
				<div className="settings-list-copy">
					<strong>{tool.name}</strong>
					<small>{tool.description}</small>
				</div>
				{apps.length === 0 ? (
					<StatusBadge tone={picked ? "warning" : "neutral"}>
						{picked ? "Picked during setup" : "Not enabled"}
					</StatusBadge>
				) : null}
				{isAdmin && canAddMethod ? (
					<EnableToolDialog
						enabledTypes={enabledTypes}
						organizationId={organization.id}
						tool={tool}
						trigger={
							<Button
								size="sm"
								type="button"
								variant={apps.length > 0 ? "ghost" : "default"}
							>
								{apps.length > 0 ? "Add a way" : "Enable"}
							</Button>
						}
					/>
				) : null}
			</div>
			{apps.map((app) => (
				<AppMethodRow
					app={app}
					isAdmin={isAdmin}
					key={app.id}
					toolName={tool.name}
				/>
			))}
		</div>
	);
}

interface AppMethodRowProps {
	app: App;
	isAdmin: boolean;
	toolName: string;
}

function AppMethodRow({ app, isAdmin, toolName }: AppMethodRowProps) {
	const { organization } = useWorkspace();
	const queryClient = useQueryClient();
	const method = credentialTypeNames[app.credential_type];
	const refresh = () =>
		queryClient.invalidateQueries({
			queryKey: connectionsQueryKey(organization.id),
		});
	const toggle = useMutation({
		mutationFn: (enabled: boolean) =>
			setAppEnabled(organization.id, app.id, enabled),
		onSettled: refresh,
	});
	const enabled =
		toggle.isPending && toggle.variables !== undefined
			? toggle.variables
			: app.status === "active";

	return (
		<div className="settings-list-row tool-method-row">
			<div className="settings-list-copy">
				<strong>{method}</strong>
				<small>{appDetail(app)}</small>
				{toggle.isError ? (
					<small className="settings-list-error">
						{apiErrorMessage(toggle.error, "Could not change the tool")}
					</small>
				) : null}
			</div>
			<StatusBadge tone={enabled ? "active" : "neutral"}>
				{enabled ? "Enabled" : "Disabled"}
			</StatusBadge>
			{isAdmin ? (
				<>
					<Switch
						aria-label={`${toolName} through ${method.toLowerCase()}`}
						checked={enabled}
						disabled={toggle.isPending}
						onCheckedChange={(checked) => toggle.mutate(checked)}
					/>
					<ConfirmDialog
						confirmLabel="Remove"
						description={`Every connection made to ${toolName} through its ${method.toLowerCase()} is disconnected.`}
						onConfirm={async () => {
							await deleteApp(organization.id, app.id);
							await refresh();
						}}
						title={`Stop offering ${toolName} this way?`}
						trigger={
							<Button
								aria-label={`Remove ${toolName} through ${method.toLowerCase()}`}
								size="icon-sm"
								type="button"
								variant="ghost"
							>
								<Trash2 />
							</Button>
						}
					/>
				</>
			) : null}
		</div>
	);
}

function appDetail(app: App): string {
	if (app.settings?.domain) {
		return app.settings.domain;
	}
	if (app.settings?.github_app_slug) {
		return `GitHub App ${app.settings.github_app_slug}`;
	}
	if (app.client_id) {
		return `Client ${app.client_id}`;
	}
	if (app.scopes.length > 0) {
		return app.scopes.join(", ");
	}
	return "Members bring their own credentials";
}
