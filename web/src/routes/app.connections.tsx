import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Plug, RefreshCw, Unplug, X } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { AppAlert, QueryError } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import { EmptyState } from "@/components/app/empty-state";

import { WorkspacePage } from "@/components/app/workspace-page";
import { WorkspaceSection } from "@/components/app/workspace-section";
import { ConnectAppDialog } from "@/components/apps/connect-app-dialog";
import { ConnectionSources } from "@/components/apps/connection-sources";
import { ConnectionState } from "@/components/apps/connection-state";
import { ConnectionTriggers } from "@/components/apps/connection-triggers";
import { ToolkitIcon } from "@/components/apps/toolkit-icon";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useToolkitCatalog } from "@/hooks/use-toolkit-catalog";
import {
	appsErrorMessage,
	type Connection,
	canReconnect,
	connect,
	connectionQueryOptions,
	connectionStatusHint,
	connectionsQueryOptions,
	connectReturnNotice,
	connectReturnTo,
	disconnectConnection,
	hasConnectionDetails,
	parseConnectReturn,
	type Toolkit,
	toolkitName,
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
	head: () => ({ meta: [{ title: "Apps & tools · The Weirdly ODD" }] }),
});

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
	const [openId, setOpenId] = useState<string>();
	const handled = useRef(false);
	const panelId = useId();

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

	// A connection that is disconnected, or that stops being active, takes its
	// panel with it rather than leaving an empty one open.
	const open = connections.data?.find(
		(connection) =>
			connection.id === openId && hasConnectionDetails(connection.status),
	);

	let body = null;
	if (connections.isPending) {
		body = <AppsGridSkeleton />;
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
					description="Connect an app so your agents can use its tools, subscribe to its events, and search what it holds."
					icon={Plug}
					title="Nothing connected yet"
				/>
			</Card>
		);
	} else {
		body = (
			<>
				<div className="apps-grid">
					{connections.data.map((connection) => (
						<ConnectionCard
							connection={connection}
							key={connection.id}
							onOpen={() =>
								setOpenId((current) =>
									current === connection.id ? undefined : connection.id,
								)
							}
							open={open?.id === connection.id}
							organizationId={organization.id}
							panelId={panelId}
							toolkit={catalog.get(connection.toolkit)}
						/>
					))}
				</div>
				{open ? (
					<ConnectionPanel
						connection={open}
						id={panelId}
						knowledgeProviders={knowledgeProviders.data ?? []}
						onClose={() => setOpenId(undefined)}
						organizationId={organization.id}
						toolkit={catalog.get(open.toolkit)}
					/>
				) : null}
			</>
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
			description="One place for member-owned connections, event subscriptions, searchable sources, and agent tools."
			title="Apps & tools"
		>
			<div className="workspace-blocks">
				{notice ? <AppAlert tone={notice.tone}>{notice.text}</AppAlert> : null}
				{body}
			</div>
		</WorkspacePage>
	);
}

const placeholders = ["first", "second", "third", "fourth"];

/** Cards standing in for the grid while it loads, so the page keeps its shape. */
function AppsGridSkeleton() {
	return (
		<div aria-busy="true" className="apps-grid">
			{placeholders.map((placeholder) => (
				<Card className="apps-card" key={placeholder}>
					<Skeleton className="size-10 rounded-[10px]" />
					<div className="apps-card-copy">
						<Skeleton className="h-3.5 w-24" />
						<Skeleton className="h-3 w-36" />
					</div>
					<footer className="apps-card-footer">
						<Skeleton className="h-3 w-16" />
					</footer>
				</Card>
			))}
		</div>
	);
}

/** Starts the connect flow again for a connection the member already has. */
function useReconnect(organizationId: string, toolkit: string) {
	return useMutation({
		mutationFn: async () => {
			const { redirect_url } = await connect(organizationId, {
				returnTo: connectReturnTo(window.location.origin),
				toolkit,
			});
			window.location.assign(redirect_url);
		},
	});
}

interface ConnectionCardProps {
	connection: Connection;
	onOpen: () => void;
	open: boolean;
	organizationId: string;
	panelId: string;
	toolkit: Toolkit | undefined;
}

function ConnectionCard({
	connection,
	onOpen,
	open,
	organizationId,
	panelId,
	toolkit,
}: ConnectionCardProps) {
	const queryClient = useQueryClient();
	const name = toolkit?.name ?? toolkitName(connection.toolkit);
	const reconnect = useReconnect(organizationId, connection.toolkit);
	const canOpen = hasConnectionDetails(connection.status);
	const hint = connectionStatusHint(connection.status);

	return (
		<Card
			className="apps-card"
			data-open={open ? "true" : undefined}
			data-tone={connection.status}
		>
			<ToolkitIcon logo={toolkit?.logo} size="lg" />
			<div className="apps-card-copy">
				{canOpen ? (
					<button
						aria-controls={panelId}
						aria-expanded={open}
						className="apps-card-open"
						onClick={onOpen}
						type="button"
					>
						{name}
					</button>
				) : (
					<strong className="apps-card-name">{name}</strong>
				)}
				{/* The app says what went wrong better than we can, so its own
				    reason replaces our hint rather than stacking on top of it. */}
				{connection.label ? (
					<small className="apps-card-line">{connection.label}</small>
				) : null}
				{connection.status_reason ? (
					<small className="apps-card-problem">
						{connection.status_reason}
					</small>
				) : (
					hint !== "" && (
						<small className="apps-card-line apps-card-hint">{hint}</small>
					)
				)}
				{reconnect.isError ? (
					<small className="apps-card-problem">
						{appsErrorMessage(reconnect.error, "Could not reconnect")}
					</small>
				) : null}
			</div>
			<footer className="apps-card-footer">
				<ConnectionState status={connection.status} />
				<div className="apps-card-actions">
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
							description={`Your agents stop using this ${name} connection, and its access is revoked at ${name} where it allows it.`}
							onConfirm={async () => {
								await disconnectConnection(organizationId, connection.id);
								await queryClient.invalidateQueries({
									queryKey: connectionsQueryOptions(organizationId).queryKey,
								});
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
				</div>
			</footer>
		</Card>
	);
}

interface ConnectionPanelProps {
	connection: Connection;
	id: string;
	knowledgeProviders: readonly string[];
	onClose: () => void;
	organizationId: string;
	toolkit: Toolkit | undefined;
}

/** What one connection is watching and holding, under the grid it was opened from. */
function ConnectionPanel({
	connection,
	id,
	knowledgeProviders,
	onClose,
	organizationId,
	toolkit,
}: ConnectionPanelProps) {
	const detail = useQuery(
		connectionQueryOptions(organizationId, connection.id),
	);
	const current = detail.data ?? connection;
	const name = toolkit?.name ?? toolkitName(current.toolkit);
	const panel = useRef<HTMLDivElement>(null);

	// Opening a card near the bottom of a long grid should not leave its panel
	// off screen. `nearest` stays put when it is already in view.
	useEffect(() => {
		panel.current?.scrollIntoView({ block: "nearest" });
	}, []);

	return (
		<Card className="apps-panel-card" id={id} ref={panel}>
			<header className="apps-panel-heading">
				<ToolkitIcon logo={toolkit?.logo} />
				<div className="apps-panel-title">
					<strong>
						{name}
						{current.label ? ` · ${current.label}` : ""}
					</strong>
					<ConnectionState status={current.status} />
				</div>
				<Button
					aria-label={`Close ${name}`}
					onClick={onClose}
					size="icon-sm"
					type="button"
					variant="ghost"
				>
					<X />
				</Button>
			</header>
			{detail.isError ? (
				<div className="settings-section-alert">
					<AppAlert>
						{appsErrorMessage(
							detail.error,
							"Could not refresh connection details",
						)}
					</AppAlert>
				</div>
			) : null}
			<div className="apps-panel-body">
				<WorkspaceSection title="Wake my agents when…">
					<ConnectionTriggers
						connectionId={current.id}
						organizationId={organizationId}
						toolkit={current.toolkit}
					/>
				</WorkspaceSection>
				{supportsSourceProvider(knowledgeProviders, current.toolkit) ? (
					<WorkspaceSection title="Searchable knowledge">
						<ConnectionSources
							connectionId={current.id}
							organizationId={organizationId}
							provider={current.toolkit}
						/>
					</WorkspaceSection>
				) : null}
			</div>
		</Card>
	);
}
