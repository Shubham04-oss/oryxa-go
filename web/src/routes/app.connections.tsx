import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { ChevronDown, ChevronUp, Plug, RefreshCw, Unplug } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { AppAlert, QueryError } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import { EmptyState } from "@/components/app/empty-state";
import {
	SettingsListSkeleton,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { WorkspaceSection } from "@/components/app/workspace-section";
import { ConnectAppDialog } from "@/components/apps/connect-app-dialog";
import { ConnectionSources } from "@/components/apps/connection-sources";
import { ConnectionTriggers } from "@/components/apps/connection-triggers";
import { ToolkitIcon } from "@/components/apps/toolkit-icon";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { useToolkitCatalog } from "@/hooks/use-toolkit-catalog";
import {
	appsErrorMessage,
	type Connection,
	canReconnect,
	connect,
	connectionStatusLabel,
	connectionStatusTone,
	connectionsQueryOptions,
	connectReturnNotice,
	connectReturnTo,
	disconnectConnection,
	parseConnectReturn,
	type Toolkit,
} from "@/lib/apps/client";
import { useWorkspace } from "@/lib/auth/session";
import {
	providersQueryOptions as knowledgeProvidersQueryOptions,
	supportsSourceProvider,
} from "@/lib/knowledge/client";

interface ConnectionsSearch {
	app_connection?: string;
	status?: string;
}

function searchText(value: unknown): string | undefined {
	return typeof value === "string" && value !== "" ? value : undefined;
}

export const Route = createFileRoute("/app/connections")({
	validateSearch: (search: Record<string, unknown>): ConnectionsSearch => ({
		app_connection: searchText(search.app_connection),
		status: searchText(search.status),
	}),
	component: ConnectionsPage,
	head: () => ({ meta: [{ title: "Connections · The Weirdly ODD" }] }),
});

function toolkitName(toolkit: string): string {
	return toolkit
		.split(/[_-]/)
		.filter(Boolean)
		.map((word) => word.charAt(0).toUpperCase() + word.slice(1))
		.join(" ");
}

function ConnectionsPage() {
	const { organization } = useWorkspace();
	const search = Route.useSearch();
	const navigate = useNavigate({ from: Route.fullPath });
	const queryClient = useQueryClient();
	const connections = useQuery(connectionsQueryOptions(organization.id));
	const knowledgeProviders = useQuery(knowledgeProvidersQueryOptions());
	const catalog = useToolkitCatalog(
		(connections.data ?? []).map((connection) => connection.toolkit),
	);
	const [notice, setNotice] = useState<{
		text: string;
		tone: "danger" | "info" | "success";
	}>();
	const handled = useRef(false);

	// Composio sends the member's browser back here once they finish connecting.
	useEffect(() => {
		const result = parseConnectReturn({
			app_connection: search.app_connection,
			status: search.status,
		});
		if (handled.current || !result) {
			return;
		}
		handled.current = true;
		setNotice(connectReturnNotice(result));
		queryClient.invalidateQueries({
			queryKey: connectionsQueryOptions(organization.id).queryKey,
		});
		navigate({ replace: true, search: {} });
	}, [
		navigate,
		organization.id,
		queryClient,
		search.app_connection,
		search.status,
	]);

	let body = null;
	if (connections.isPending) {
		body = <SettingsListSkeleton />;
	} else if (connections.isError) {
		body = (
			<Card className="workspace-panel">
				<div className="settings-section-alert">
					<QueryError
						error={connections.error}
						fallback="Could not load connections"
						onRetry={connections.refetch}
					/>
				</div>
			</Card>
		);
	} else if (connections.data.length === 0) {
		body = (
			<Card className="workspace-panel">
				<EmptyState
					description="Connect an app so Krude can watch it and search what it holds."
					icon={Plug}
					title="Nothing connected yet"
				/>
			</Card>
		);
	} else {
		body = (
			<div className="apps-connections">
				{connections.data.map((connection) => (
					<ConnectionRow
						connection={connection}
						key={connection.id}
						knowledgeProviders={knowledgeProviders.data ?? []}
						organizationId={organization.id}
						toolkit={catalog.get(connection.toolkit)}
					/>
				))}
			</div>
		);
	}

	return (
		<WorkspacePage
			actions={
				<ConnectAppDialog
					organizationId={organization.id}
					trigger={
						<Button size="sm" type="button">
							Connect an app
						</Button>
					}
				/>
			}
			description="Connect apps for Krude to work in, watch for events, and search."
			title="Connections"
		>
			<div className="workspace-blocks">
				{notice ? <AppAlert tone={notice.tone}>{notice.text}</AppAlert> : null}
				{body}
			</div>
		</WorkspacePage>
	);
}

interface ConnectionRowProps {
	connection: Connection;
	knowledgeProviders: readonly string[];
	organizationId: string;
	toolkit: Toolkit | undefined;
}

function ConnectionRow({
	connection,
	knowledgeProviders,
	organizationId,
	toolkit,
}: ConnectionRowProps) {
	const queryClient = useQueryClient();
	const [expanded, setExpanded] = useState(false);
	const name = toolkit?.name ?? toolkitName(connection.toolkit);
	const refresh = () =>
		queryClient.invalidateQueries({
			queryKey: connectionsQueryOptions(organizationId).queryKey,
		});
	const reconnect = useMutation({
		mutationFn: async () => {
			const { redirect_url } = await connect(organizationId, {
				returnTo: connectReturnTo(window.location.origin),
				toolkit: connection.toolkit,
			});
			window.location.assign(redirect_url);
		},
	});
	const canExpand = connection.status === "active";

	return (
		<Card className="workspace-panel">
			<div className="settings-list-row">
				<ToolkitIcon logo={toolkit?.logo} />
				<div className="settings-list-copy">
					<strong>
						{name}
						{connection.label ? ` · ${connection.label}` : ""}
					</strong>
					{connection.status_reason ? (
						<small className="settings-list-error">
							{connection.status_reason}
						</small>
					) : null}
					{reconnect.isError ? (
						<small className="settings-list-error">
							{appsErrorMessage(reconnect.error, "Could not reconnect")}
						</small>
					) : null}
				</div>
				<StatusBadge tone={connectionStatusTone(connection.status)}>
					{connectionStatusLabel(connection.status)}
				</StatusBadge>
				{canReconnect(connection.status) ? (
					<Button
						disabled={reconnect.isPending}
						onClick={() => reconnect.mutate()}
						size="sm"
						type="button"
						variant="ghost"
					>
						<RefreshCw data-icon="inline-start" />
						{reconnect.isPending ? "Reconnecting…" : "Reconnect"}
					</Button>
				) : null}
				{connection.status !== "removed" ? (
					<ConfirmDialog
						confirmLabel="Disconnect"
						description={`Krude stops using this ${name} connection, and its access is revoked at ${name} where it allows it.`}
						onConfirm={async () => {
							await disconnectConnection(organizationId, connection.id);
							await refresh();
						}}
						title={`Disconnect ${name}?`}
						trigger={
							<Button
								aria-label={`Disconnect ${name}`}
								size="icon-sm"
								type="button"
								variant="ghost"
							>
								<Unplug />
							</Button>
						}
					/>
				) : null}
				{canExpand ? (
					<Button
						aria-expanded={expanded}
						aria-label={expanded ? `Collapse ${name}` : `Expand ${name}`}
						onClick={() => setExpanded((current) => !current)}
						size="icon-sm"
						type="button"
						variant="ghost"
					>
						{expanded ? <ChevronUp /> : <ChevronDown />}
					</Button>
				) : null}
			</div>
			{canExpand && expanded ? (
				<div className="apps-connection-details">
					<WorkspaceSection title="Wake Krude when…">
						<ConnectionTriggers
							connectionId={connection.id}
							organizationId={organizationId}
							toolkit={connection.toolkit}
						/>
					</WorkspaceSection>
					{supportsSourceProvider(knowledgeProviders, connection.toolkit) ? (
						<WorkspaceSection title="Searchable by Krude">
							<ConnectionSources
								connectionId={connection.id}
								organizationId={organizationId}
								provider={connection.toolkit}
							/>
						</WorkspaceSection>
					) : null}
				</div>
			) : null}
		</Card>
	);
}
