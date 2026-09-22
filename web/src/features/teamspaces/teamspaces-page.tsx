import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import {
	ArrowUpRight,
	Bot,
	BrainCircuit,
	CheckCircle2,
	CircleAlert,
	Clock3,
	FileStack,
	GitPullRequestArrow,
	MessageSquareText,
	Network,
	PauseCircle,
	Pencil,
	Plus,
	RefreshCw,
	ShieldCheck,
	Sparkles,
	Trash2,
	WandSparkles,
} from "lucide-react";
import { QueryError } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import { EmptyState } from "@/components/app/empty-state";
import { StatusBadge } from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import {
	ContributeAgentDialog,
	CreateTeamspaceDialog,
	EditTeamspaceDialog,
	OrganizationAgentDialog,
	PublishEntryDialog,
} from "@/components/teamspaces/teamspace-dialogs";
import { TeamspaceRoom } from "@/components/teamspaces/teamspace-room";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { agentsQueryOptions, type ManagedAgent } from "@/lib/agents/client";
import { ApiError, apiErrorMessage } from "@/lib/api/client";
import { useWorkspace } from "@/lib/auth/session";
import { relativeTime } from "@/lib/format";
import { teamsQueryOptions } from "@/lib/teams/client";
import {
	deleteTeamspace,
	deleteTeamspaceContribution,
	type OrganizationAgent,
	type OrganizationFollowUp,
	type OrganizationSynthesis,
	organizationAgentQueryOptions,
	organizationContextQueryOptions,
	organizationFollowUpsQueryOptions,
	type Teamspace,
	type TeamspaceContribution,
	type TeamspaceEntry,
	teamspaceEntriesQueryOptions,
	teamspaceQueryOptions,
	teamspacesQueryKey,
	teamspacesQueryOptions,
	updateTeamspaceContribution,
} from "@/lib/teamspaces/client";
import {
	canPublishSharedWork,
	safeArtifactHref,
} from "@/lib/teamspaces/policy";

export function TeamspacesPage({ selectedId }: { selectedId?: string }) {
	const { organization, user } = useWorkspace();
	const navigate = useNavigate({ from: "/app/teamspaces" });
	const spaces = useQuery(teamspacesQueryOptions(organization.id));
	const teams = useQuery(teamsQueryOptions(organization.id));
	const agents = useQuery(agentsQueryOptions(organization.id));
	const organizationAgent = useQuery(
		organizationAgentQueryOptions(organization.id),
	);
	const context = useQuery(organizationContextQueryOptions(organization.id));
	const followUps = useQuery(
		organizationFollowUpsQueryOptions(organization.id, 20),
	);
	const visible = spaces.data ?? [];
	const selectedSummary = selectedId
		? visible.find((space) => space.id === selectedId)
		: visible[0];
	const selectedQuery = useQuery({
		...teamspaceQueryOptions(
			organization.id,
			selectedSummary?.id ?? "not-selected",
		),
		enabled: selectedSummary !== undefined,
	});
	const selected = selectedQuery.data ?? selectedSummary;
	const contextMissing =
		context.error instanceof ApiError && context.error.status === 404;
	const entries = useQuery({
		...teamspaceEntriesQueryOptions(
			organization.id,
			selected?.id ?? "not-selected",
			100,
		),
		enabled: selected !== undefined,
	});
	const canCreate = (teams.data ?? []).some((team) => team.can_manage);
	const refreshing =
		spaces.isFetching || context.isFetching || followUps.isFetching;

	async function refresh() {
		await Promise.all([
			spaces.refetch(),
			organizationAgent.refetch(),
			context.refetch(),
			followUps.refetch(),
			selectedSummary ? selectedQuery.refetch() : Promise.resolve(),
			selected ? entries.refetch() : Promise.resolve(),
		]);
	}

	return (
		<WorkspacePage
			actions={
				<>
					<Button
						disabled={refreshing}
						onClick={refresh}
						type="button"
						variant="ghost"
					>
						<RefreshCw
							aria-hidden="true"
							className={refreshing ? "teamspace-spin" : undefined}
						/>
						Refresh
					</Button>
					{canCreate && teams.data ? (
						<CreateTeamspaceDialog
							onCreated={(created) =>
								navigate({ search: { space: created.id } })
							}
							organizationId={organization.id}
							teams={teams.data}
							trigger={
								<Button type="button">
									<Plus data-icon="inline-start" />
									New Teamspace
								</Button>
							}
						/>
					) : null}
				</>
			}
			description="Where teammates’ primary agents collaborate quietly, and only intentional work becomes shared context."
			title="Teamspaces"
		>
			{context.isError && !contextMissing ? (
				<QueryError
					error={context.error}
					fallback="Could not load organization context"
					onRetry={context.refetch}
				/>
			) : null}
			{followUps.isError ? (
				<QueryError
					error={followUps.error}
					fallback="Could not load organization follow-up proposals"
					onRetry={followUps.refetch}
				/>
			) : null}

			{spaces.isError ? (
				<QueryError
					error={spaces.error}
					fallback="Could not load Teamspaces"
					onRetry={spaces.refetch}
				/>
			) : null}
			{spaces.isPending ? (
				<Card aria-busy="true" className="workspace-panel teamspace-loading">
					<div />
					<div />
					<div />
				</Card>
			) : null}
			{spaces.data?.length === 0 ? (
				<Card className="workspace-panel teamspace-first-space">
					<EmptyState
						action={
							canCreate && teams.data ? (
								<CreateTeamspaceDialog
									onCreated={(created) =>
										navigate({ search: { space: created.id } })
									}
									organizationId={organization.id}
									teams={teams.data}
									trigger={<Button type="button">Create Teamspace</Button>}
								/>
							) : teams.data?.length === 0 ? (
								<Button asChild type="button">
									<Link to="/app/teams">Create a human team</Link>
								</Button>
							) : (
								<Button asChild type="button" variant="outline">
									<Link to="/app/teams">Open People &amp; teams</Link>
								</Button>
							)
						}
						description={
							canCreate
								? "Choose a human team to open its group conversation, agent roster, and shared work."
								: teams.data?.length === 0
									? "Teamspaces belong to human teams. Create a team first, then open its shared room."
									: "A team manager can create a Teamspace for a team you belong to."
						}
						icon={Network}
						title="No shared spaces yet"
					/>
				</Card>
			) : null}
			{spaces.data && spaces.data.length > 0 ? (
				<div
					className="teamspace-cockpit"
					data-single={spaces.data.length === 1}
				>
					{spaces.data.length > 1 ? (
						<TeamspaceList selectedId={selected?.id} spaces={spaces.data} />
					) : null}
					{selectedQuery.isError ? (
						<Card className="workspace-panel teams-panel-empty">
							<QueryError
								error={selectedQuery.error}
								fallback="Could not load this Teamspace"
								onRetry={selectedQuery.refetch}
							/>
						</Card>
					) : selected ? (
						<TeamspaceDetail
							agents={agents.data ?? []}
							entries={entries.data ?? []}
							entriesError={entries.isError ? entries.error : undefined}
							entriesPending={entries.isPending}
							key={selected.id}
							onDeleted={() => navigate({ search: { space: undefined } })}
							organizationId={organization.id}
							space={selected}
							userId={user.id}
						/>
					) : (
						<Card className="workspace-panel teams-panel-empty">
							<p>This Teamspace is unavailable or no longer exists.</p>
						</Card>
					)}
				</div>
			) : null}
			<OrganizationPulse
				agent={organizationAgent.data}
				context={context.data}
				contextMissing={contextMissing}
				followUps={followUps.data ?? []}
				organizationId={organization.id}
			/>
		</WorkspacePage>
	);
}

interface OrganizationPulseProps {
	agent?: OrganizationAgent;
	context?: OrganizationSynthesis;
	contextMissing: boolean;
	followUps: OrganizationFollowUp[];
	organizationId: string;
}

function OrganizationPulse({
	agent,
	context,
	contextMissing,
	followUps = [],
	organizationId,
}: OrganizationPulseProps) {
	return (
		<Card className="teamspace-org-pulse">
			<div className="teamspace-org-mark" aria-hidden="true">
				<BrainCircuit />
			</div>
			<div className="teamspace-org-copy">
				<div className="teamspace-eyebrow">
					<span>Organization context</span>
					{agent ? (
						<StatusBadge
							tone={agent.status === "active" ? "active" : "neutral"}
						>
							{agent.status === "active" ? "Synthesizing" : "Paused"}
						</StatusBadge>
					) : null}
				</div>
				<h2>{agent?.name ?? "Organization agent"}</h2>
				<p>
					{context?.summary ||
						(agent?.description ??
							"Shared publications become a durable, organization-wide brief here.")}
				</p>
				<div className="teamspace-org-meta">
					<span>
						<ShieldCheck aria-hidden="true" /> Shared context only
					</span>
					{context ? (
						<span>
							<Sparkles aria-hidden="true" /> Revision {context.revision}
						</span>
					) : null}
					{context?.updated_at ? (
						<span>
							<Clock3 aria-hidden="true" /> {relativeTime(context.updated_at)}
						</span>
					) : null}
				</div>
				{contextMissing ? (
					<p className="teamspace-context-note">
						No synthesis yet. The first deliberate publication will establish
						it.
					</p>
				) : null}
			</div>
			<div className="teamspace-org-signals">
				<PulseMetric
					icon={CheckCircle2}
					label="Decisions"
					value={context?.decisions.length ?? 0}
				/>
				<PulseMetric
					icon={CircleAlert}
					label="Risks"
					value={context?.risks.length ?? 0}
				/>
				<PulseMetric
					icon={GitPullRequestArrow}
					label="Proposals"
					value={followUps?.length ?? 0}
				/>
				{agent?.can_manage ? (
					<OrganizationAgentDialog
						agent={agent}
						organizationId={organizationId}
						trigger={
							<Button size="sm" type="button" variant="ghost">
								<Pencil aria-hidden="true" /> Configure
							</Button>
						}
					/>
				) : null}
			</div>
			{followUps && followUps.length > 0 ? (
				<div className="teamspace-proposal-strip">
					<WandSparkles aria-hidden="true" />
					<div>
						<strong>{followUps[0].task}</strong>
						<span>
							Proposed only · {followUps[0].rationale} · no work has been
							started
						</span>
					</div>
				</div>
			) : null}
		</Card>
	);
}

function PulseMetric({
	icon: Icon,
	label,
	value,
}: {
	icon: typeof CheckCircle2;
	label: string;
	value: number;
}) {
	return (
		<div className="teamspace-pulse-metric">
			<Icon aria-hidden="true" />
			<strong>{value}</strong>
			<span>{label}</span>
		</div>
	);
}

function TeamspaceList({
	selectedId,
	spaces,
}: {
	selectedId?: string;
	spaces: Teamspace[];
}) {
	return (
		<Card className="workspace-panel teamspace-list-panel">
			<header>
				<span>Shared rooms</span>
				<small>{spaces.length}</small>
			</header>
			<nav aria-label="Teamspaces" className="teamspace-list">
				{spaces.map((space) => (
					<Link
						aria-current={space.id === selectedId ? "page" : undefined}
						className="teamspace-list-item"
						data-active={space.id === selectedId}
						key={space.id}
						search={{ space: space.id }}
						to="/app/teamspaces"
					>
						<span className="teamspace-list-icon" aria-hidden="true">
							{space.status === "active" ? <Network /> : <PauseCircle />}
						</span>
						<span>
							<strong>{space.name}</strong>
							<small>{space.team_name}</small>
						</span>
						<span className="teamspace-list-count">
							{space.whisperers.length}
						</span>
					</Link>
				))}
			</nav>
		</Card>
	);
}

interface TeamspaceDetailProps {
	agents: ManagedAgent[];
	entries: TeamspaceEntry[];
	entriesError?: unknown;
	entriesPending: boolean;
	onDeleted: () => void;
	organizationId: string;
	space: Teamspace;
	userId: string;
}

function TeamspaceDetail({
	agents = [],
	entries,
	entriesError,
	entriesPending,
	onDeleted,
	organizationId,
	space,
	userId,
}: TeamspaceDetailProps) {
	const queryClient = useQueryClient();
	const primary = space.whisperers.find(
		(whisperer) => whisperer.user_id === userId,
	);
	const authors = [
		...(primary
			? [
					{
						id: primary.agent_id,
						label: `${primary.agent_name} · my whisperer`,
					},
				]
			: []),
		...space.contributions
			.filter(
				(contribution) =>
					contribution.owner_user_id === userId &&
					contribution.status === "active",
			)
			.map((contribution) => ({
				id: contribution.agent_id,
				label: `${contribution.name} · contributed specialist`,
			})),
	];

	async function remove() {
		await deleteTeamspace(organizationId, space.id);
		await queryClient.invalidateQueries({
			queryKey: teamspacesQueryKey(organizationId),
		});
		onDeleted();
	}

	return (
		<Card className="workspace-panel teamspace-detail">
			<header className="teamspace-detail-header">
				<div>
					<div className="teamspace-eyebrow">
						<span>{space.team_name}</span>
						<StatusBadge
							tone={space.status === "active" ? "active" : "neutral"}
						>
							{space.status === "active" ? "Active" : "Paused"}
						</StatusBadge>
					</div>
					<h2>{space.name}</h2>
					{space.description ? <p>{space.description}</p> : null}
				</div>
				<div className="teamspace-detail-actions">
					{space.can_manage ? (
						<EditTeamspaceDialog
							organizationId={organizationId}
							space={space}
							trigger={
								<Button size="sm" type="button" variant="ghost">
									<Pencil aria-hidden="true" /> Settings
								</Button>
							}
						/>
					) : null}
					{canPublishSharedWork(space, authors.length) ? (
						<PublishEntryDialog
							authors={authors}
							organizationId={organizationId}
							space={space}
							trigger={
								<Button size="sm" type="button">
									<MessageSquareText aria-hidden="true" /> Share work
								</Button>
							}
						/>
					) : space.is_member &&
						space.status === "active" &&
						space.organization_agent.status !== "active" ? (
						<Button
							disabled
							size="sm"
							title="Resume the organization agent before publishing shared work"
							type="button"
						>
							<MessageSquareText aria-hidden="true" /> Sharing paused
						</Button>
					) : null}
				</div>
			</header>

			{space.is_member ? (
				<TeamspaceRoom
					active={space.status === "active"}
					organizationId={organizationId}
					teamspaceId={space.id}
					userId={userId}
				/>
			) : (
				<div className="teamspace-room teamspace-room-unavailable">
					<Network aria-hidden="true" />
					<strong>This conversation belongs to the team</strong>
					<p>Join {space.team_name} to read and participate in its room.</p>
					<Button asChild size="sm" variant="outline">
						<Link to="/app/teams">Open People &amp; teams</Link>
					</Button>
				</div>
			)}

			<div className="teamspace-boundary">
				<ShieldCheck aria-hidden="true" />
				<div>
					<strong>Shared room, private boundaries</strong>
					<span>
						Agents can speak in this room, but their private instructions,
						conversations, and connections stay with their owners. Room
						decisions can enter the shared ledger.
					</span>
				</div>
			</div>

			<section
				className="teamspace-roster teamspace-whisperers"
				aria-labelledby="whisperers-heading"
			>
				<header>
					<div>
						<h3 id="whisperers-heading">Available whisperers</h3>
						<p>One private-side primary agent per active teammate.</p>
					</div>
					<span>{space.whisperers.length} active</span>
				</header>
				<div className="teamspace-presence-grid">
					{space.whisperers.map((whisperer) => (
						<div
							className="teamspace-presence"
							data-owned={whisperer.user_id === userId}
							key={whisperer.user_id}
						>
							<span aria-hidden="true">
								<Bot />
							</span>
							<div>
								<strong>{whisperer.agent_name}</strong>
								<small>
									{whisperer.user_id === userId
										? "Your primary · private-side"
										: `${whisperer.user_display_name || "Teammate"} · whisperer`}
								</small>
							</div>
							<span className="teamspace-presence-dot">Available</span>
						</div>
					))}
				</div>
			</section>

			<section
				className="teamspace-roster teamspace-specialists"
				aria-labelledby="specialists-heading"
			>
				<header>
					<div>
						<h3 id="specialists-heading">Contributed specialists</h3>
						<p>Explicit capabilities with owner-controlled delegation.</p>
					</div>
					{space.is_member && space.status === "active" ? (
						<ContributeAgentDialog
							agents={agents}
							organizationId={organizationId}
							space={space}
							trigger={
								<Button size="sm" type="button" variant="outline">
									<Plus aria-hidden="true" /> Contribute
								</Button>
							}
						/>
					) : null}
				</header>
				{space.contributions.length > 0 ? (
					<div className="teamspace-contributions">
						{space.contributions.map((contribution) => (
							<ContributionRow
								contribution={contribution}
								key={contribution.agent_id}
								organizationId={organizationId}
								space={space}
								canManage={space.can_manage}
								userId={userId}
							/>
						))}
					</div>
				) : (
					<p className="teamspace-section-empty">
						No specialists have been deliberately contributed.
					</p>
				)}
			</section>

			<section className="teamspace-ledger" aria-labelledby="ledger-heading">
				<header>
					<div>
						<h3 id="ledger-heading">Shared work ledger</h3>
						<p>Immutable, attributed publications. Newest first.</p>
					</div>
					<span>{entries.length} entries</span>
				</header>
				{entriesError ? (
					<p className="teamspace-section-error">
						{apiErrorMessage(entriesError, "Could not load shared work")}
					</p>
				) : null}
				{entriesPending ? (
					<p className="teamspace-section-empty">Loading shared work…</p>
				) : null}
				{!entriesPending && !entriesError && entries.length === 0 ? (
					<div className="teamspace-ledger-empty">
						<FileStack aria-hidden="true" />
						<div>
							<strong>Nothing has been made shared yet</strong>
							<span>
								A whisperer can work privately first, then publish the useful
								outcome here.
							</span>
						</div>
					</div>
				) : null}
				<div className="teamspace-entry-list">
					{entries.map((entry) => (
						<LedgerEntry entry={entry} key={entry.id} />
					))}
				</div>
			</section>

			{space.can_manage ? (
				<footer className="teamspace-danger-zone">
					<span>Deleting removes this Teamspace and its shared ledger.</span>
					<ConfirmDialog
						confirmLabel="Delete Teamspace"
						confirmation={space.name}
						description="This permanently removes the shared Teamspace. Private agent work remains untouched."
						onConfirm={remove}
						title={`Delete ${space.name}?`}
						trigger={
							<Button size="sm" type="button" variant="ghost">
								<Trash2 aria-hidden="true" /> Delete
							</Button>
						}
					/>
				</footer>
			) : null}
		</Card>
	);
}

function ContributionRow({
	canManage,
	contribution,
	organizationId,
	space,
	userId,
}: {
	canManage: boolean;
	contribution: TeamspaceContribution;
	organizationId: string;
	space: Teamspace;
	userId: string;
}) {
	const queryClient = useQueryClient();
	const owned = contribution.owner_user_id === userId;
	const update = useMutation({
		mutationFn: () =>
			updateTeamspaceContribution(
				organizationId,
				space.id,
				contribution.agent_id,
				{
					delegationMode:
						contribution.delegation_mode === "automatic"
							? "request"
							: "automatic",
				},
			),
		onSuccess: () =>
			queryClient.invalidateQueries({
				queryKey: teamspacesQueryKey(organizationId),
			}),
	});
	const remove = useMutation({
		mutationFn: () =>
			deleteTeamspaceContribution(
				organizationId,
				space.id,
				contribution.agent_id,
			),
		onSuccess: () =>
			queryClient.invalidateQueries({
				queryKey: teamspacesQueryKey(organizationId),
			}),
	});

	return (
		<div className="teamspace-contribution">
			<span className="teamspace-list-icon" aria-hidden="true">
				<Sparkles />
			</span>
			<div>
				<strong>{contribution.name}</strong>
				<small>{contribution.role}</small>
			</div>
			<span
				className="teamspace-authority"
				data-automatic={contribution.delegation_mode === "automatic"}
			>
				{contribution.delegation_mode === "automatic"
					? "Automatic · bounded"
					: "Ask owner first"}
			</span>
			{owned || canManage ? (
				<div className="teamspace-contribution-actions">
					{owned && contribution.delegation_mode === "automatic" ? (
						<Button
							disabled={update.isPending}
							onClick={() => update.mutate()}
							size="sm"
							type="button"
							variant="ghost"
						>
							Require request
						</Button>
					) : owned ? (
						<ConfirmDialog
							confirmLabel="Allow bounded automatic work"
							description={
								<>
									The organization agent may start{" "}
									<strong>{contribution.name}</strong>
									without asking you each time, but only for “
									{contribution.role}”. This grants no new tools and shares no
									private context.
								</>
							}
							onConfirm={() => update.mutateAsync()}
							title="Grant standing delegation authority?"
							trigger={
								<Button size="sm" type="button" variant="ghost">
									Allow automatic
								</Button>
							}
						/>
					) : null}
					{canManage && !owned ? (
						<ConfirmDialog
							confirmLabel="Remove from Teamspace"
							description={`This removes ${contribution.name} from shared work. The owner’s specialist remains intact.`}
							onConfirm={() => remove.mutateAsync()}
							title={`Remove ${contribution.name}?`}
							trigger={
								<Button size="icon-sm" type="button" variant="ghost">
									<Trash2 aria-hidden="true" />
									<span className="sr-only">Remove {contribution.name}</span>
								</Button>
							}
						/>
					) : (
						<Button
							disabled={remove.isPending}
							onClick={() => remove.mutate()}
							size="icon-sm"
							type="button"
							variant="ghost"
						>
							<Trash2 aria-hidden="true" />
							<span className="sr-only">Remove {contribution.name}</span>
						</Button>
					)}
				</div>
			) : null}
		</div>
	);
}

const entryIcons = {
	artifact: FileStack,
	decision: CheckCircle2,
	handoff: ArrowUpRight,
	result: Sparkles,
	update: MessageSquareText,
};

function LedgerEntry({ entry }: { entry: TeamspaceEntry }) {
	const Icon = entryIcons[entry.kind];
	return (
		<article className="teamspace-entry">
			<div className="teamspace-entry-rail" data-kind={entry.kind}>
				<Icon aria-hidden="true" />
			</div>
			<div className="teamspace-entry-body">
				<header>
					<div>
						<span className="teamspace-entry-kind">{entry.kind}</span>
						<h4>{entry.title}</h4>
					</div>
					<time dateTime={entry.created_at}>
						{relativeTime(entry.created_at)}
					</time>
				</header>
				<p>{entry.summary}</p>
				<footer>
					<span>
						<Bot aria-hidden="true" /> {entry.author_agent_name}
					</span>
					{entry.tools_used.length > 0 ? (
						<span>{entry.tools_used.length} tools declared</span>
					) : null}
					{entry.artifacts.length > 0 ? (
						<span>{entry.artifacts.length} artifacts</span>
					) : null}
				</footer>
				{entry.tools_used.length > 0 ? (
					<ul className="teamspace-tool-list" aria-label="Tools used">
						{entry.tools_used.map((tool) => (
							<li key={tool}>{tool}</li>
						))}
					</ul>
				) : null}
				{entry.artifacts.length > 0 ? (
					<ul className="teamspace-artifact-list" aria-label="Artifacts">
						{entry.artifacts.map((artifact) => {
							const href = safeArtifactHref(artifact.uri);
							return (
								<li key={`${artifact.uri}-${artifact.name}`}>
									{href ? (
										<a href={href} rel="noreferrer" target="_blank">
											{artifact.name} <ArrowUpRight aria-hidden="true" />
										</a>
									) : (
										<span title="Untrusted artifact address was not made clickable">
											{artifact.name}
										</span>
									)}
								</li>
							);
						})}
					</ul>
				) : null}
			</div>
		</article>
	);
}
