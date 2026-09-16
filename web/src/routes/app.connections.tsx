import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import {
	ArrowUpRight,
	KeyRound,
	Pencil,
	Plug,
	RefreshCw,
	Star,
	Unplug,
} from "lucide-react";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { AppAlert, QueryError } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import { EmptyState } from "@/components/app/empty-state";
import {
	SettingsListSkeleton,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import {
	ConnectToolDialog,
	RenameConnectionDialog,
	ReplaceKeyDialog,
} from "@/components/connections/connection-dialogs";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { apiErrorMessage } from "@/lib/api/client";
import { managesMembers } from "@/lib/auth/client";
import {
	selectOrganization,
	useMembership,
	useWorkspace,
} from "@/lib/auth/session";
import {
	type App,
	appsQueryOptions,
	type Connection,
	type ConnectionViewer,
	completeConnection,
	connectFailure,
	connectionAccess,
	connectionProblem,
	connectionsQueryKey,
	connectionsQueryOptions,
	connectMethod,
	connectReturnUri,
	disconnectConnection,
	placementOptions,
	reconnectConnection,
	updateConnection,
} from "@/lib/connections/client";
import { teamsQueryOptions } from "@/lib/teams/client";
import {
	credentialTypeNames,
	toolName as providerName,
	ToolGlyph,
} from "@/lib/tools/catalog";

interface ConnectionsSearch {
	error?: string;
	organization?: string;
	session_id?: string;
}

function searchText(value: unknown): string | undefined {
	return typeof value === "string" && value !== "" ? value : undefined;
}

export const Route = createFileRoute("/app/connections")({
	validateSearch: (search: Record<string, unknown>): ConnectionsSearch => ({
		error: searchText(search.error),
		organization: searchText(search.organization),
		session_id: searchText(search.session_id),
	}),
	component: ConnectionsPage,
	head: () => ({ meta: [{ title: "Connections · The Weirdly ODD" }] }),
});

type Notice = { text: string; tone: "danger" | "success" };

function connectionName(connection: Connection): string {
	const tool = providerName(connection.provider);
	const account =
		connection.provider_account.name ?? connection.provider_account.id;
	return `${tool} (${account})`;
}

function ConnectionsPage() {
	const { organization, organizations, user } = useWorkspace();
	const search = Route.useSearch();
	const navigate = useNavigate({ from: Route.fullPath });
	const queryClient = useQueryClient();
	const isOrgAdmin = managesMembers(useMembership(organization.id)?.role);
	const apps = useQuery(appsQueryOptions(organization.id));
	const connections = useQuery(connectionsQueryOptions(organization.id));
	const teams = useQuery(teamsQueryOptions(organization.id));
	const [notice, setNotice] = useState<Notice>();
	const finished = useRef(false);

	// A provider sends the browser back here to finish connecting.
	useEffect(() => {
		const sessionId = search.session_id;
		if (finished.current || (!sessionId && !search.error)) {
			return;
		}
		finished.current = true;
		const organizationId = search.organization ?? organization.id;
		if (
			organizationId !== organization.id &&
			organizations.some((candidate) => candidate.id === organizationId)
		) {
			selectOrganization(organizationId);
		}
		const clear = () => navigate({ replace: true, search: {} });
		if (search.error || !sessionId) {
			setNotice({ text: connectFailure(search.error ?? ""), tone: "danger" });
			clear();
			return;
		}
		completeConnection(organizationId, sessionId)
			.then(async (connection) => {
				await queryClient.invalidateQueries({
					queryKey: connectionsQueryKey(organizationId),
				});
				setNotice({
					text: `Connected ${connectionName(connection)}.`,
					tone: "success",
				});
			})
			.catch((error: unknown) =>
				setNotice({
					text: apiErrorMessage(
						error,
						"The connection did not finish. Start again.",
					),
					tone: "danger",
				}),
			)
			.finally(clear);
	}, [
		navigate,
		organization.id,
		organizations,
		queryClient,
		search.error,
		search.organization,
		search.session_id,
	]);

	const viewer: ConnectionViewer = {
		isOrgAdmin,
		manageableTeams: (teams.data ?? [])
			.filter((team) => team.can_manage)
			.map((team) => ({ id: team.id, name: team.name })),
		userId: user.id,
	};
	const teamNames = new Map(
		(teams.data ?? []).map((team) => [team.id, team.name]),
	);

	let connected: ReactNode;
	if (connections.isPending) {
		connected = <SettingsListSkeleton />;
	} else if (connections.isError) {
		connected = (
			<div className="settings-section-alert">
				<QueryError
					error={connections.error}
					fallback="Could not load connections"
					onRetry={connections.refetch}
				/>
			</div>
		);
	} else if (connections.data.length === 0) {
		connected = (
			<EmptyState
				description="Connect a tool below so your assistants can work in it."
				icon={Plug}
				title="Nothing connected yet"
			/>
		);
	} else {
		connected = (
			<div className="settings-list">
				{connections.data.map((connection) => (
					<ConnectionRow
						connection={connection}
						key={connection.id}
						teamName={
							connection.team_id ? teamNames.get(connection.team_id) : undefined
						}
						viewer={viewer}
					/>
				))}
			</div>
		);
	}

	const activeApps = (apps.data ?? []).filter((app) => app.status === "active");
	let tools: ReactNode;
	if (apps.isPending) {
		tools = <SettingsListSkeleton />;
	} else if (apps.isError) {
		tools = (
			<div className="settings-section-alert">
				<QueryError
					error={apps.error}
					fallback="Could not load tools"
					onRetry={apps.refetch}
				/>
			</div>
		);
	} else if (activeApps.length === 0) {
		tools = (
			<EmptyState
				action={
					isOrgAdmin ? (
						<Button asChild size="sm">
							<Link to="/app/settings/organization/tools">Enable tools</Link>
						</Button>
					) : undefined
				}
				description={
					isOrgAdmin
						? "Enable a tool so people can connect their accounts to it."
						: "Ask an organization owner or admin to enable the tools you need."
				}
				icon={Plug}
				title="No tools are enabled"
			/>
		);
	} else {
		tools = (
			<div className="settings-list">
				{activeApps.map((app) => (
					<ToolConnectRow
						app={app}
						key={app.id}
						onConnected={(connection) =>
							setNotice({
								text: `Connected ${connectionName(connection)}.`,
								tone: "success",
							})
						}
						viewer={viewer}
					/>
				))}
			</div>
		);
	}

	return (
		<WorkspacePage
			description="Accounts connected for you, your teams, and the organization."
			title="Connections"
		>
			<div className="workspace-blocks">
				{notice ? <AppAlert tone={notice.tone}>{notice.text}</AppAlert> : null}
				<section
					aria-labelledby="connections-connected"
					className="workspace-block"
				>
					<header className="workspace-block-header">
						<h2 id="connections-connected">Connected accounts</h2>
					</header>
					<Card className="workspace-panel">{connected}</Card>
				</section>
				<section
					aria-labelledby="connections-tools"
					className="workspace-block"
				>
					<header className="workspace-block-header">
						<h2 id="connections-tools">Connect a tool</h2>
						{isOrgAdmin ? (
							<Button asChild size="sm" variant="ghost">
								<Link to="/app/settings/organization/tools">
									Manage tools
									<ArrowUpRight data-icon="inline-end" />
								</Link>
							</Button>
						) : null}
					</header>
					<Card className="workspace-panel">{tools}</Card>
				</section>
			</div>
		</WorkspacePage>
	);
}

interface ToolConnectRowProps {
	app: App;
	onConnected: (connection: Connection) => void;
	viewer: ConnectionViewer;
}

function ToolConnectRow({ app, onConnected, viewer }: ToolConnectRowProps) {
	const { organization } = useWorkspace();
	const toolName = providerName(app.provider);
	const placements = placementOptions(app.credential_type, viewer);

	return (
		<div className="settings-list-row">
			<span aria-hidden="true" className="settings-list-icon">
				<ToolGlyph provider={app.provider} />
			</span>
			<div className="settings-list-copy">
				<strong>{toolName}</strong>
				<small>{credentialTypeNames[app.credential_type]}</small>
			</div>
			{placements.length > 0 ? (
				<ConnectToolDialog
					app={app}
					onConnected={onConnected}
					organizationId={organization.id}
					placements={placements}
					toolName={toolName}
					trigger={
						<Button size="sm" type="button">
							Connect
						</Button>
					}
				/>
			) : (
				<small className="settings-list-note">
					Organization admins connect this
				</small>
			)}
		</div>
	);
}

interface ConnectionRowProps {
	connection: Connection;
	teamName: string | undefined;
	viewer: ConnectionViewer;
}

function ConnectionRow({ connection, teamName, viewer }: ConnectionRowProps) {
	const { organization } = useWorkspace();
	const queryClient = useQueryClient();
	const toolName = providerName(connection.provider);
	const account =
		connection.provider_account.name ?? connection.provider_account.id;
	const access = connectionAccess(connection, viewer);
	const problem = connectionProblem(connection);
	const keyBased = connectMethod(connection.credential_type) === "key";
	const refresh = () =>
		queryClient.invalidateQueries({
			queryKey: connectionsQueryKey(organization.id),
		});
	const makeDefault = useMutation({
		mutationFn: () =>
			updateConnection(organization.id, connection.id, { default: true }),
		onSettled: refresh,
	});
	const reconnect = useMutation({
		mutationFn: async () => {
			const { authorization_url } = await reconnectConnection(
				organization.id,
				connection.id,
				connectReturnUri(window.location.origin, organization.id),
			);
			window.location.assign(authorization_url);
		},
	});
	const actionError = makeDefault.error ?? reconnect.error;
	const details = [
		placementName(connection, teamName),
		connection.label ? account : undefined,
		connection.default ? "default" : undefined,
	].filter(Boolean);

	return (
		<div className="settings-list-row">
			<span aria-hidden="true" className="settings-list-icon">
				<ToolGlyph provider={connection.provider} />
			</span>
			<div className="settings-list-copy">
				<strong>
					{toolName} · {connection.label ?? account}
				</strong>
				<small>{details.join(" · ")}</small>
				{problem ? (
					<small className="settings-list-error">{problem}</small>
				) : null}
				{actionError ? (
					<small className="settings-list-error">
						{apiErrorMessage(actionError, "That did not work")}
					</small>
				) : null}
			</div>
			<StatusBadge tone={statusTone(connection)}>
				{statusName(connection)}
			</StatusBadge>
			{access.change &&
			connection.status === "active" &&
			!connection.default ? (
				<Button
					disabled={makeDefault.isPending}
					onClick={() => makeDefault.mutate()}
					size="sm"
					type="button"
					variant="ghost"
				>
					<Star data-icon="inline-start" />
					Make default
				</Button>
			) : null}
			{access.change && keyBased ? (
				<ReplaceKeyDialog
					connection={connection}
					organizationId={organization.id}
					toolName={toolName}
					trigger={
						<Button size="sm" type="button" variant="ghost">
							<KeyRound data-icon="inline-start" />
							Replace key
						</Button>
					}
				/>
			) : null}
			{access.change && !keyBased ? (
				<Button
					disabled={reconnect.isPending}
					onClick={() => reconnect.mutate()}
					size="sm"
					type="button"
					variant="ghost"
				>
					<RefreshCw data-icon="inline-start" />
					Reconnect
				</Button>
			) : null}
			{access.change ? (
				<RenameConnectionDialog
					connection={connection}
					organizationId={organization.id}
					toolName={toolName}
					trigger={
						<Button
							aria-label={`Label the ${toolName} connection`}
							size="icon-sm"
							type="button"
							variant="ghost"
						>
							<Pencil />
						</Button>
					}
				/>
			) : null}
			{access.disconnect ? (
				<ConfirmDialog
					confirmLabel="Disconnect"
					description={`Assistants stop using this ${toolName} account, and its access is revoked where ${toolName} allows it.`}
					onConfirm={async () => {
						await disconnectConnection(organization.id, connection.id);
						await refresh();
					}}
					title={`Disconnect ${toolName}?`}
					trigger={
						<Button
							aria-label={`Disconnect ${toolName}`}
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
	);
}

function placementName(
	connection: Connection,
	teamName: string | undefined,
): string {
	switch (connection.scope) {
		case "team":
			return teamName ? `Team: ${teamName}` : "Team";
		case "organization":
			return "Organization";
		default:
			return "Personal";
	}
}

function statusName(connection: Connection): string {
	if (connection.status === "needs_reconnect") {
		return "Needs reconnecting";
	}
	if (connection.status === "disconnected") {
		return "Disconnected";
	}
	if (connection.blocked) {
		return "Blocked";
	}
	return connection.health.state === "degraded"
		? "Having trouble"
		: "Connected";
}

function statusTone(
	connection: Connection,
): "active" | "danger" | "neutral" | "warning" {
	if (connection.status === "needs_reconnect") {
		return "danger";
	}
	if (connection.status === "disconnected") {
		return "neutral";
	}
	return connection.blocked || connection.health.state === "degraded"
		? "warning"
		: "active";
}
