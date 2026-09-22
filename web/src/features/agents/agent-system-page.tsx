import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	Activity,
	Bot,
	CalendarClock,
	Check,
	ChevronDown,
	ChevronUp,
	CircleDot,
	Clock3,
	GitBranch,
	Inbox,
	MessageCircle,
	Network,
	Pencil,
	Play,
	Plus,
	RefreshCw,
	Send,
	ShieldCheck,
	Sparkles,
	Trash2,
	UsersRound,
	X,
} from "lucide-react";
import { type ReactElement, type ReactNode, useId, useState } from "react";
import {
	AgentDialog,
	AgentTeamDialog,
	AgentWorkflowDialog,
	SpecialistProfileDialog,
} from "@/components/agents/agent-dialogs";
import { AgentTeamRoom } from "@/components/agents/agent-team-room";
import { SpecialistConversationDialog } from "@/components/agents/specialist-conversation-dialog";
import { SpecialistAvatar } from "@/components/agents/specialist-identity";
import { AppAlert, QueryError } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import { EmptyState } from "@/components/app/empty-state";
import {
	SettingsListSkeleton,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import {
	type AgentInteraction,
	type AgentStatus,
	type AgentTeam,
	type AgentWorkflow,
	agentRequestsQueryKey,
	agentRequestsQueryOptions,
	agentsQueryKey,
	agentsQueryOptions,
	agentTeamsQueryKey,
	agentTeamsQueryOptions,
	agentWorkflowsQueryKey,
	agentWorkflowsQueryOptions,
	decideAgentRequest,
	deleteAgent,
	deleteAgentTeam,
	deleteAgentWorkflow,
	type InteractionDecisionInput,
	type InteractionResponseInput,
	type ManagedAgent,
	respondToAgentRequest,
	scheduleLabel,
	startAgentWorkflow,
	titleCaseStatus,
	updateAgent,
	updateAgentTeam,
	updateAgentWorkflow,
	type WorkflowExecution,
	workflowExecutionsQueryOptions,
} from "@/lib/agents/client";
import { apiErrorMessage } from "@/lib/api/client";
import { useWorkspace } from "@/lib/auth/session";
import { relativeTime } from "@/lib/format";

export const agentTabs = [
	"overview",
	"specialists",
	"teams",
	"workflows",
	"requests",
] as const;
export type AgentTab = (typeof agentTabs)[number];
export type RequestBox = "inbox" | "sent";

export function AgentSystemPage({
	box,
	onSearchChange,
	tab,
}: {
	box: RequestBox;
	onSearchChange: (search: { box: RequestBox; tab: AgentTab }) => void;
	tab: AgentTab;
}) {
	return (
		<WorkspacePage
			description="Create specialists, compose teams, run durable workflows, and supervise agent-to-agent requests."
			title="Agent system"
		>
			<Tabs
				onValueChange={(value) =>
					onSearchChange({ box, tab: value as AgentTab })
				}
				value={tab}
			>
				<TabsList aria-label="Agent system sections" className="agent-tabs">
					{agentTabs.map((candidate) => (
						<TabsTrigger
							className="agent-tab"
							key={candidate}
							value={candidate}
						>
							{titleCaseStatus(candidate)}
						</TabsTrigger>
					))}
				</TabsList>
				<TabsContent className="agent-tab-content" value="overview">
					<OverviewTab />
				</TabsContent>
				<TabsContent className="agent-tab-content" value="specialists">
					<SpecialistsTab />
				</TabsContent>
				<TabsContent className="agent-tab-content" value="teams">
					<TeamsTab />
				</TabsContent>
				<TabsContent className="agent-tab-content" value="workflows">
					<WorkflowsTab />
				</TabsContent>
				<TabsContent className="agent-tab-content" value="requests">
					<RequestsTab
						box={box}
						onBoxChange={(nextBox) => onSearchChange({ box: nextBox, tab })}
					/>
				</TabsContent>
			</Tabs>
		</WorkspacePage>
	);
}

function OverviewTab() {
	const { organization } = useWorkspace();
	const agents = useQuery(agentsQueryOptions(organization.id));
	const teams = useQuery(agentTeamsQueryOptions(organization.id));
	const workflows = useQuery(agentWorkflowsQueryOptions(organization.id));
	const requests = useQuery(
		agentRequestsQueryOptions(organization.id, "inbox"),
	);
	const primary = agents.data?.find((agent) => agent.kind === "primary");

	if (agents.isError) {
		return (
			<QueryError
				error={agents.error}
				fallback="Could not load agents"
				onRetry={agents.refetch}
			/>
		);
	}
	if (agents.isPending) {
		return (
			<Card className="workspace-panel">
				<SettingsListSkeleton />
			</Card>
		);
	}

	return (
		<div className="agent-overview">
			<Card className="agent-primary-card">
				<div className="agent-primary-mark" aria-hidden="true">
					<Sparkles />
				</div>
				<div className="agent-primary-copy">
					<span>Your primary agent</span>
					<h2>{primary?.name ?? "Primary agent"}</h2>
					<p>
						{primary?.description ||
							"Coordinates your specialists, teams, and external requests."}
					</p>
				</div>
				{primary ? (
					<AgentDialog
						agent={primary}
						organizationId={organization.id}
						trigger={
							<Button size="sm" type="button" variant="outline">
								<Pencil data-icon="inline-start" />
								Edit identity
							</Button>
						}
					/>
				) : null}
			</Card>

			<div className="agent-metrics">
				<Metric
					icon={Bot}
					label="Specialists"
					value={
						agents.data.filter((agent) => agent.kind === "specialist").length
					}
				/>
				<Metric
					icon={UsersRound}
					label="Teams"
					value={teams.data?.length ?? 0}
				/>
				<Metric
					icon={CalendarClock}
					label="Workflows"
					value={workflows.data?.length ?? 0}
				/>
				<Metric
					icon={Inbox}
					label="Open requests"
					value={
						requests.data?.filter((request) =>
							["pending", "approved"].includes(request.status),
						).length ?? 0
					}
				/>
			</div>

			<div className="agent-overview-grid">
				<Card className="workspace-panel agent-overview-panel">
					<PanelHeader icon={Network} title="Operating model" />
					<div className="agent-flow">
						<FlowStep
							label={primary?.name ?? "Primary"}
							note="Plans, delegates, and reports"
						/>
						<span aria-hidden="true" />
						<FlowStep label="Specialists" note="Execute bounded roles" />
						<span aria-hidden="true" />
						<FlowStep label="Requests" note="Cross-user work stays audited" />
					</div>
				</Card>
				<Card className="workspace-panel agent-overview-panel">
					<PanelHeader icon={Activity} title="Recent requests" />
					{requests.data?.length ? (
						<div className="agent-compact-list">
							{requests.data.slice(0, 4).map((request) => (
								<div className="agent-compact-row" key={request.id}>
									<span>
										<strong>{request.requester.agent_name}</strong>
										<small>{request.purpose}</small>
									</span>
									<Status status={request.status} />
								</div>
							))}
						</div>
					) : (
						<p className="agent-panel-empty">No agent requests yet.</p>
					)}
				</Card>
			</div>
		</div>
	);
}

function Metric({
	icon: Icon,
	label,
	value,
}: {
	icon: typeof Bot;
	label: string;
	value: number;
}) {
	return (
		<Card className="agent-metric">
			<Icon aria-hidden="true" />
			<span>
				<strong>{value}</strong>
				<small>{label}</small>
			</span>
		</Card>
	);
}

function PanelHeader({
	icon: Icon,
	title,
}: {
	icon: typeof Bot;
	title: string;
}) {
	return (
		<header className="agent-panel-header">
			<Icon aria-hidden="true" />
			<h2>{title}</h2>
		</header>
	);
}

function FlowStep({ label, note }: { label: string; note: string }) {
	return (
		<div>
			<strong>{label}</strong>
			<small>{note}</small>
		</div>
	);
}

function SpecialistsTab() {
	const { organization } = useWorkspace();
	const queryClient = useQueryClient();
	const agents = useQuery(agentsQueryOptions(organization.id));
	const teams = useQuery(agentTeamsQueryOptions(organization.id));
	const workflows = useQuery(agentWorkflowsQueryOptions(organization.id));
	const specialists =
		agents.data?.filter((agent) => agent.kind === "specialist") ?? [];
	const status = useMutation({
		mutationFn: ({ agent, next }: { agent: ManagedAgent; next: AgentStatus }) =>
			updateAgent(organization.id, agent.id, { status: next }),
		onSuccess: () =>
			queryClient.invalidateQueries({
				queryKey: agentsQueryKey(organization.id),
			}),
	});

	return (
		<TabSection
			action={
				<AgentDialog
					organizationId={organization.id}
					trigger={
						<Button type="button">
							<Plus data-icon="inline-start" />
							New specialist
						</Button>
					}
				/>
			}
			description="Persistent, reusable agents with strict roles and your shared app connections."
			title="Specialists"
		>
			{agents.isPending ? (
				<Card className="workspace-panel">
					<SettingsListSkeleton />
				</Card>
			) : null}
			{agents.isError ? (
				<QueryError
					error={agents.error}
					fallback="Could not load specialists"
					onRetry={agents.refetch}
				/>
			) : null}
			{agents.data && specialists.length === 0 ? (
				<Card className="specialist-empty-card">
					<div className="specialist-empty-avatars" aria-hidden="true">
						<SpecialistAvatar name="Research" seed="research" size="large" />
						<SpecialistAvatar name="Analysis" seed="analysis" size="large" />
						<SpecialistAvatar name="Writing" seed="writing" size="large" />
					</div>
					<h3>Give a specialist a clear responsibility</h3>
					<p>
						Choose a focus, name the work they own, and refine their guidance as
						they become part of your team.
					</p>
					<AgentDialog
						organizationId={organization.id}
						trigger={
							<Button type="button">
								<Plus data-icon="inline-start" />
								Create specialist
							</Button>
						}
					/>
				</Card>
			) : null}
			{status.isError ? (
				<AppAlert>
					{apiErrorMessage(status.error, "Could not update specialist")}
				</AppAlert>
			) : null}
			{specialists.length ? (
				<div className="agent-card-grid specialist-card-grid">
					{specialists.map((agent) => {
						const memberships = (teams.data ?? []).flatMap((team) => [
							...(team.coordinator_agent_id === agent.id
								? [{ team, role: "Coordinator" }]
								: []),
							...team.members
								.filter((member) => member.agent_id === agent.id)
								.map((member) => ({ team, role: member.role })),
						]);
						const assignments = (workflows.data ?? []).filter((workflow) =>
							workflow.assignments.some(
								(assignment) => assignment.agent_id === agent.id,
							),
						);
						return (
							<Card
								className="agent-entity-card specialist-card"
								key={agent.id}
							>
								<header>
									<SpecialistAvatar
										name={agent.name}
										seed={agent.id}
										size="large"
									/>
									<Status status={agent.status} />
								</header>
								<div className="agent-entity-copy">
									<h3>{agent.name}</h3>
									<p>{agent.description || "Purpose not set yet"}</p>
								</div>
								<div className="specialist-card-context">
									<div>
										<span>Team</span>
										<strong>
											{teams.data
												? memberships.length
													? memberships.map(({ team }) => team.name).join(", ")
													: "Independent"
												: teams.isError
													? "Unavailable"
													: "Loading…"}
										</strong>
									</div>
									<div>
										<span>Workflows</span>
										<strong>
											{workflows.data
												? assignments.length
												: workflows.isError
													? "Unavailable"
													: "—"}
										</strong>
									</div>
								</div>
								<div className="specialist-card-updated">
									Updated {relativeTime(agent.updated_at)}
								</div>
								<footer>
									<div className="agent-toggle">
										<Switch
											aria-label={`${agent.status === "active" ? "Pause" : "Activate"} ${agent.name}`}
											checked={agent.status === "active"}
											disabled={status.isPending}
											onCheckedChange={(checked) =>
												status.mutate({
													agent,
													next: checked ? "active" : "disabled",
												})
											}
										/>
										<span>
											{agent.status === "active" ? "Active" : "Paused"}
										</span>
									</div>
									<div className="specialist-card-actions">
										<SpecialistConversationDialog
											agent={agent}
											organizationId={organization.id}
											trigger={
												<Button
													disabled={agent.status !== "active"}
													size="sm"
													type="button"
												>
													<MessageCircle data-icon="inline-start" />
													Talk
												</Button>
											}
										/>
										<SpecialistProfileDialog
											agent={agent}
											memberships={memberships}
											teamDataState={
												teams.isError
													? "error"
													: teams.data
														? "ready"
														: "loading"
											}
											workflows={assignments}
											workflowDataState={
												workflows.isError
													? "error"
													: workflows.data
														? "ready"
														: "loading"
											}
											trigger={
												<Button size="sm" type="button" variant="outline">
													View profile
												</Button>
											}
										/>
										<AgentDialog
											agent={agent}
											organizationId={organization.id}
											trigger={
												<Button
													aria-label={`Edit ${agent.name}`}
													size="icon-sm"
													type="button"
													variant="ghost"
												>
													<Pencil />
												</Button>
											}
										/>
										<ConfirmDialog
											confirmLabel="Delete specialist"
											confirmation={agent.name}
											description="Its durable identity and future availability are removed. Delete any teams using it first."
											onConfirm={async () => {
												await deleteAgent(organization.id, agent.id);
												await queryClient.invalidateQueries({
													queryKey: agentsQueryKey(organization.id),
												});
											}}
											title={`Delete ${agent.name}?`}
											trigger={
												<Button
													aria-label={`Delete ${agent.name}`}
													size="icon-sm"
													type="button"
													variant="ghost"
												>
													<Trash2 />
												</Button>
											}
										/>
									</div>
								</footer>
							</Card>
						);
					})}
				</div>
			) : null}
		</TabSection>
	);
}

function TeamsTab() {
	const { organization } = useWorkspace();
	const queryClient = useQueryClient();
	const agents = useQuery(agentsQueryOptions(organization.id));
	const teams = useQuery(agentTeamsQueryOptions(organization.id));
	const [selectedTeamId, setSelectedTeamId] = useState("");
	const selectedTeam =
		teams.data?.find((team) => team.id === selectedTeamId) ?? teams.data?.[0];
	const toggle = useMutation({
		mutationFn: ({ team, active }: { active: boolean; team: AgentTeam }) =>
			updateAgentTeam(organization.id, team.id, {
				status: active ? "active" : "disabled",
			}),
		onSuccess: () =>
			queryClient.invalidateQueries({
				queryKey: agentTeamsQueryKey(organization.id),
			}),
	});

	return (
		<TabSection
			action={
				<AgentTeamDialog
					agents={agents.data ?? []}
					organizationId={organization.id}
					trigger={
						<Button
							disabled={
								!agents.data?.some((agent) => agent.kind === "specialist")
							}
							type="button"
						>
							<Plus data-icon="inline-start" />
							New team
						</Button>
					}
				/>
			}
			description="Your primary agent coordinates each team; specialists keep their own instructions and identity."
			title="Agent teams"
		>
			{teams.isPending || agents.isPending ? (
				<Card className="workspace-panel">
					<SettingsListSkeleton />
				</Card>
			) : null}
			{teams.isError ? (
				<QueryError
					error={teams.error}
					fallback="Could not load agent teams"
					onRetry={teams.refetch}
				/>
			) : null}
			{teams.data?.length === 0 ? (
				<Card className="workspace-panel">
					<EmptyState
						description="Create specialists first, then combine the ones that should collaborate."
						icon={UsersRound}
						title="No agent teams yet"
					/>
				</Card>
			) : null}
			{teams.data?.length ? (
				<div className="agent-team-workspace">
					<div className="agent-team-list">
						{teams.data.map((team) => (
							<Card
								className="workspace-panel agent-team-card"
								data-selected={selectedTeam?.id === team.id}
								key={team.id}
							>
								<header>
									<div>
										<span className="agent-eyebrow">
											Coordinated by {team.coordinator_name}
										</span>
										<h3>{team.name}</h3>
										<p>{team.description}</p>
									</div>
									<Status status={team.status} />
								</header>
								<div className="agent-team-members">
									{team.members.map((member) => (
										<div key={member.agent_id}>
											<SpecialistAvatar
												name={member.name}
												seed={member.agent_id}
												size="small"
											/>
											<div>
												<strong>{member.name}</strong>
												<small>{member.role}</small>
											</div>
											<Status status={member.status} />
										</div>
									))}
								</div>
								<footer>
									<Button
										aria-current={
											selectedTeam?.id === team.id ? "true" : undefined
										}
										onClick={() => setSelectedTeamId(team.id)}
										size="sm"
										type="button"
										variant={
											selectedTeam?.id === team.id ? "secondary" : "outline"
										}
									>
										<MessageCircle aria-hidden />
										{selectedTeam?.id === team.id
											? "Open workroom"
											: "Open chat"}
									</Button>
									<div className="agent-toggle">
										<Switch
											aria-label={`${team.status === "active" ? "Pause" : "Activate"} ${team.name}`}
											checked={team.status === "active"}
											disabled={toggle.isPending}
											onCheckedChange={(active) =>
												toggle.mutate({ active, team })
											}
										/>
										<span>
											{team.status === "active" ? "Accepting work" : "Paused"}
										</span>
									</div>
									<AgentTeamDialog
										agents={agents.data ?? []}
										organizationId={organization.id}
										team={team}
										trigger={
											<Button
												aria-label={`Edit ${team.name}`}
												size="icon-sm"
												type="button"
												variant="ghost"
											>
												<Pencil aria-hidden />
											</Button>
										}
									/>
									<ConfirmDialog
										confirmLabel="Delete team"
										confirmation={team.name}
										description="The team is removed. Its specialists remain available."
										onConfirm={async () => {
											await deleteAgentTeam(organization.id, team.id);
											await queryClient.invalidateQueries({
												queryKey: agentTeamsQueryKey(organization.id),
											});
										}}
										title={`Delete ${team.name}?`}
										trigger={
											<Button
												aria-label={`Delete ${team.name}`}
												size="icon-sm"
												type="button"
												variant="ghost"
											>
												<Trash2 />
											</Button>
										}
									/>
								</footer>
							</Card>
						))}
					</div>
					{selectedTeam ? (
						<AgentTeamRoom
							agents={agents.data ?? []}
							key={selectedTeam.id}
							organizationId={organization.id}
							team={selectedTeam}
						/>
					) : null}
				</div>
			) : null}
		</TabSection>
	);
}

function WorkflowsTab() {
	const { organization } = useWorkspace();
	const queryClient = useQueryClient();
	const teams = useQuery(agentTeamsQueryOptions(organization.id));
	const workflows = useQuery(agentWorkflowsQueryOptions(organization.id));
	const toggle = useMutation({
		mutationFn: ({
			active,
			workflow,
		}: {
			active: boolean;
			workflow: AgentWorkflow;
		}) =>
			updateAgentWorkflow(organization.id, workflow.id, {
				status: active ? "active" : "disabled",
			}),
		onSuccess: () =>
			queryClient.invalidateQueries({
				queryKey: agentWorkflowsQueryKey(organization.id),
			}),
	});

	return (
		<TabSection
			action={
				<AgentWorkflowDialog
					organizationId={organization.id}
					teams={teams.data ?? []}
					trigger={
						<Button disabled={!teams.data?.length} type="button">
							<Plus data-icon="inline-start" />
							New workflow
						</Button>
					}
				/>
			}
			description="Durable team assignments that run on demand or through EventBridge Scheduler."
			title="Workflows"
		>
			{workflows.isPending || teams.isPending ? (
				<Card className="workspace-panel">
					<SettingsListSkeleton />
				</Card>
			) : null}
			{workflows.isError ? (
				<QueryError
					error={workflows.error}
					fallback="Could not load workflows"
					onRetry={workflows.refetch}
				/>
			) : null}
			{workflows.data?.length === 0 ? (
				<Card className="workspace-panel">
					<EmptyState
						description="Create an agent team, then define repeatable assignments for its specialists."
						icon={CalendarClock}
						title="No workflows yet"
					/>
				</Card>
			) : null}
			{workflows.data?.length ? (
				<div className="agent-workflow-list">
					{workflows.data.map((workflow) => (
						<WorkflowCard
							key={workflow.id}
							onToggle={(active) => toggle.mutate({ active, workflow })}
							organizationId={organization.id}
							team={teams.data?.find((team) => team.id === workflow.team_id)}
							workflow={workflow}
						/>
					))}
				</div>
			) : null}
		</TabSection>
	);
}

function WorkflowCard({
	onToggle,
	organizationId,
	team,
	workflow,
}: {
	onToggle: (active: boolean) => void;
	organizationId: string;
	team?: AgentTeam;
	workflow: AgentWorkflow;
}) {
	const queryClient = useQueryClient();
	const [expanded, setExpanded] = useState(false);
	const executions = useQuery({
		...workflowExecutionsQueryOptions(organizationId, workflow.id),
		enabled: expanded,
	});
	const start = useMutation({
		mutationFn: () => startAgentWorkflow(organizationId, workflow.id),
		onSuccess: async () => {
			setExpanded(true);
			await queryClient.invalidateQueries({
				queryKey: workflowExecutionsQueryOptions(organizationId, workflow.id)
					.queryKey,
			});
		},
	});
	return (
		<Card className="workspace-panel agent-workflow-card">
			<header>
				<div className="agent-workflow-heading">
					<span aria-hidden="true">
						<GitBranch />
					</span>
					<div>
						<h3>{workflow.name}</h3>
						<p>{workflow.description}</p>
					</div>
				</div>
				<Status status={workflow.status} />
			</header>
			<div className="agent-workflow-meta">
				<span>
					<UsersRound />
					{workflow.assignments.length} assignment
					{workflow.assignments.length === 1 ? "" : "s"}
				</span>
				<span>
					<Clock3 />
					{scheduleLabel(workflow.schedule)}
				</span>
			</div>
			<div className="agent-workflow-assignments">
				{workflow.assignments.map((assignment) => {
					const member = team?.members.find(
						(candidate) => candidate.agent_id === assignment.agent_id,
					);
					return (
						<div
							className="agent-workflow-assignment"
							key={assignment.agent_id}
						>
							<SpecialistAvatar
								name={member?.name ?? "Specialist"}
								seed={assignment.agent_id}
								size="small"
							/>
							<span>
								<strong>{member?.name ?? "Specialist"}</strong>
								<small>{assignment.task}</small>
							</span>
						</div>
					);
				})}
			</div>
			{start.isError ? (
				<div className="agent-inline-alert">
					<AppAlert>
						{apiErrorMessage(start.error, "Could not start workflow")}
					</AppAlert>
				</div>
			) : null}
			<footer>
				<div>
					<Button
						disabled={start.isPending || workflow.status !== "active"}
						onClick={() => start.mutate()}
						size="sm"
						type="button"
					>
						<Play data-icon="inline-start" />
						{start.isPending ? "Starting…" : "Run now"}
					</Button>
					<Button
						aria-expanded={expanded}
						onClick={() => setExpanded((value) => !value)}
						size="sm"
						type="button"
						variant="ghost"
					>
						{expanded ? (
							<ChevronUp data-icon="inline-start" />
						) : (
							<ChevronDown data-icon="inline-start" />
						)}
						Runs
					</Button>
				</div>
				<div>
					<div className="agent-toggle">
						<Switch
							aria-label={`${workflow.status === "active" ? "Pause" : "Activate"} ${workflow.name}`}
							checked={workflow.status === "active"}
							onCheckedChange={onToggle}
						/>
						<span>{workflow.status === "active" ? "Active" : "Paused"}</span>
					</div>
					<ConfirmDialog
						confirmLabel="Delete workflow"
						confirmation={workflow.name}
						description="Its schedule is removed and it cannot run again. Execution history remains auditable."
						onConfirm={async () => {
							await deleteAgentWorkflow(organizationId, workflow.id);
							await queryClient.invalidateQueries({
								queryKey: agentWorkflowsQueryKey(organizationId),
							});
						}}
						title={`Delete ${workflow.name}?`}
						trigger={
							<Button
								aria-label={`Delete ${workflow.name}`}
								size="icon-sm"
								type="button"
								variant="ghost"
							>
								<Trash2 />
							</Button>
						}
					/>
				</div>
			</footer>
			{expanded ? (
				<WorkflowRuns
					error={executions.error}
					executions={executions.data}
					isError={executions.isError}
					isPending={executions.isPending}
					onRetry={executions.refetch}
				/>
			) : null}
		</Card>
	);
}

function WorkflowRuns({
	error,
	executions,
	isError,
	isPending,
	onRetry,
}: {
	error: unknown;
	executions?: WorkflowExecution[];
	isError: boolean;
	isPending: boolean;
	onRetry: () => unknown;
}) {
	return (
		<div className="agent-workflow-runs">
			{isPending ? <p>Loading runs…</p> : null}
			{isError ? (
				<QueryError
					error={error}
					fallback="Could not load workflow runs"
					onRetry={onRetry}
				/>
			) : null}
			{executions?.length === 0 ? <p>No runs yet.</p> : null}
			{executions?.map((execution) => (
				<div key={execution.id}>
					<span
						className="agent-run-icon"
						data-status={execution.status}
						aria-hidden="true"
					>
						{execution.status === "succeeded" ? (
							<Check />
						) : execution.status === "failed" ? (
							<X />
						) : (
							<CircleDot />
						)}
					</span>
					<div>
						<strong>{titleCaseStatus(execution.status)}</strong>
						<small>
							{execution.started_by === "schedule"
								? "Scheduled"
								: "Started manually"}{" "}
							· {relativeTime(execution.created_at)}
						</small>
						{execution.error ? <em>{execution.error}</em> : null}
					</div>
					<Status status={execution.status} />
				</div>
			))}
		</div>
	);
}

function RequestsTab({
	box,
	onBoxChange,
}: {
	box: RequestBox;
	onBoxChange: (box: RequestBox) => void;
}) {
	const { organization } = useWorkspace();
	const requests = useQuery(agentRequestsQueryOptions(organization.id, box));
	return (
		<TabSection
			action={
				<Button
					aria-label="Refresh requests"
					disabled={requests.isFetching}
					onClick={() => requests.refetch()}
					size="icon-sm"
					type="button"
					variant="ghost"
				>
					<RefreshCw />
				</Button>
			}
			description="Every cross-user agent request stays scoped, reviewed, and auditable."
			title="Requests"
		>
			<fieldset className="segmented agent-request-box">
				<legend className="sr-only">Request mailbox</legend>
				<Button
					data-active={box === "inbox"}
					onClick={() => onBoxChange("inbox")}
					size="sm"
					type="button"
					variant="ghost"
				>
					<Inbox data-icon="inline-start" />
					Inbox
				</Button>
				<Button
					data-active={box === "sent"}
					onClick={() => onBoxChange("sent")}
					size="sm"
					type="button"
					variant="ghost"
				>
					<Send data-icon="inline-start" />
					Sent
				</Button>
			</fieldset>
			{requests.isPending ? (
				<Card className="workspace-panel">
					<SettingsListSkeleton />
				</Card>
			) : null}
			{requests.isError ? (
				<QueryError
					error={requests.error}
					fallback="Could not load requests"
					onRetry={requests.refetch}
				/>
			) : null}
			{requests.data?.length === 0 ? (
				<Card className="workspace-panel">
					<EmptyState
						description={
							box === "inbox"
								? "Requests addressed to your agents will appear here for review."
								: "Requests your agents send to others will appear here."
						}
						icon={box === "inbox" ? Inbox : Send}
						title={box === "inbox" ? "Inbox is clear" : "Nothing sent yet"}
					/>
				</Card>
			) : null}
			{requests.data?.length ? (
				<div className="agent-request-list">
					{requests.data.map((request) => (
						<RequestCard
							box={box}
							key={request.id}
							organizationId={organization.id}
							request={request}
						/>
					))}
				</div>
			) : null}
		</TabSection>
	);
}

function RequestCard({
	box,
	organizationId,
	request,
}: {
	box: RequestBox;
	organizationId: string;
	request: AgentInteraction;
}) {
	const other = box === "inbox" ? request.requester : request.target;
	return (
		<Card className="workspace-panel agent-request-card">
			<header>
				<div className="agent-request-party">
					<span aria-hidden="true">
						{other.agent_name.slice(0, 1).toUpperCase()}
					</span>
					<div>
						<strong>{other.agent_name}</strong>
						<small>
							{other.user_display_name ||
								other.user_email ||
								(box === "inbox" ? "Requesting agent" : "Receiving agent")}
						</small>
					</div>
				</div>
				<div>
					<Status status={request.status} />
					<time dateTime={request.created_at}>
						{relativeTime(request.created_at)}
					</time>
				</div>
			</header>
			<div className="agent-request-body">
				<span>{request.purpose}</span>
				<p>{request.task}</p>
				{request.context.length ? (
					<dl>
						{request.context.map((item) => (
							<div key={item.label}>
								<dt>{item.label}</dt>
								<dd>{item.value}</dd>
							</div>
						))}
					</dl>
				) : null}
			</div>
			{request.decision ? (
				<div
					className="agent-request-decision"
					data-decision={request.decision.decision}
				>
					<ShieldCheck aria-hidden="true" />
					<div>
						<strong>{titleCaseStatus(request.decision.decision)}</strong>
						<p>{request.decision.message}</p>
					</div>
				</div>
			) : null}
			{request.result ? (
				<div className="agent-request-result">
					<span>Result</span>
					<p>{request.result.content || request.result.summary}</p>
					{request.result.tools_used.length ? (
						<small>Tools: {request.result.tools_used.join(", ")}</small>
					) : null}
				</div>
			) : null}
			{box === "inbox" && ["pending", "approved"].includes(request.status) ? (
				<footer>
					{request.status === "pending" ? (
						<>
							<RequestDecisionDialog
								decision="rejected"
								organizationId={organizationId}
								request={request}
								trigger={
									<Button type="button" variant="ghost">
										<X data-icon="inline-start" />
										Reject
									</Button>
								}
							/>
							<RequestDecisionDialog
								decision="approved"
								organizationId={organizationId}
								request={request}
								trigger={
									<Button type="button">
										<Check data-icon="inline-start" />
										Approve scope
									</Button>
								}
							/>
						</>
					) : (
						<RequestResponseDialog
							organizationId={organizationId}
							request={request}
							trigger={
								<Button type="button">
									<Send data-icon="inline-start" />
									Respond
								</Button>
							}
						/>
					)}
				</footer>
			) : null}
		</Card>
	);
}

function RequestDecisionDialog({
	decision,
	organizationId,
	request,
	trigger,
}: {
	decision: "approved" | "rejected";
	organizationId: string;
	request: AgentInteraction;
	trigger: ReactElement;
}) {
	const queryClient = useQueryClient();
	const messageId = useId();
	const [open, setOpen] = useState(false);
	const [message, setMessage] = useState("");
	const decide = useMutation({
		mutationFn: (input: InteractionDecisionInput) =>
			decideAgentRequest(organizationId, request.id, input),
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: agentRequestsQueryKey(organizationId, "inbox"),
			});
			setOpen(false);
		},
	});
	const approve = decision === "approved";
	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				setOpen(next);
				if (next) {
					setMessage("");
					decide.reset();
				}
			}}
		>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>
						{approve ? "Approve requested scope?" : "Reject request?"}
					</DialogTitle>
					<DialogDescription>
						{approve
							? "This grants only the tools and permissions explicitly requested. Their own authorization rules still apply."
							: "Tell the requesting agent why this work cannot proceed."}
					</DialogDescription>
				</DialogHeader>
				<div className="dialog-form">
					{decide.isError ? (
						<AppAlert>
							{apiErrorMessage(decide.error, "Could not decide request")}
						</AppAlert>
					) : null}
					<div className="dialog-field">
						<Label htmlFor={messageId}>Message</Label>
						<Textarea
							id={messageId}
							onChange={(event) => setMessage(event.target.value)}
							placeholder={
								approve
									? "Approved within the requested scope."
									: "This request exceeds the agent's role."
							}
							value={message}
						/>
					</div>
					{approve &&
					(request.requested_tools.length ||
						request.requested_permissions.length) ? (
						<div className="agent-request-scope">
							<strong>Requested scope</strong>
							{request.requested_tools.length ? (
								<span>Tools · {request.requested_tools.join(", ")}</span>
							) : null}
							{request.requested_permissions.length ? (
								<span>
									Permissions · {request.requested_permissions.join(", ")}
								</span>
							) : null}
						</div>
					) : null}
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button
							disabled={decide.isPending || !message.trim()}
							onClick={() =>
								decide.mutate({
									allowed_tools: approve ? request.requested_tools : [],
									decision,
									granted_permissions: approve
										? request.requested_permissions
										: [],
									message: message.trim(),
								})
							}
							type="button"
							variant={approve ? "default" : "destructive"}
						>
							{decide.isPending ? "Saving…" : approve ? "Approve" : "Reject"}
						</Button>
					</DialogFooter>
				</div>
			</DialogContent>
		</Dialog>
	);
}

function RequestResponseDialog({
	organizationId,
	request,
	trigger,
}: {
	organizationId: string;
	request: AgentInteraction;
	trigger: ReactElement;
}) {
	const queryClient = useQueryClient();
	const summaryId = useId();
	const contentId = useId();
	const toolsId = useId();
	const [open, setOpen] = useState(false);
	const [summary, setSummary] = useState("");
	const [content, setContent] = useState("");
	const [tools, setTools] = useState("");
	const respond = useMutation({
		mutationFn: (input: InteractionResponseInput) =>
			respondToAgentRequest(organizationId, request.id, input),
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: agentRequestsQueryKey(organizationId, "inbox"),
			});
			setOpen(false);
		},
	});
	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				setOpen(next);
				if (next) {
					setSummary("");
					setContent("");
					setTools("");
					respond.reset();
				}
			}}
		>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent className="agent-dialog-wide">
				<DialogHeader>
					<DialogTitle>Complete request</DialogTitle>
					<DialogDescription>
						Return only information intentionally shared with{" "}
						{request.requester.agent_name}.
					</DialogDescription>
				</DialogHeader>
				<div className="dialog-form">
					{respond.isError ? (
						<AppAlert>
							{apiErrorMessage(respond.error, "Could not send response")}
						</AppAlert>
					) : null}
					<div className="dialog-field">
						<Label htmlFor={summaryId}>Summary</Label>
						<Input
							id={summaryId}
							onChange={(event) => setSummary(event.target.value)}
							value={summary}
						/>
					</div>
					<div className="dialog-field">
						<Label htmlFor={contentId}>Response</Label>
						<Textarea
							className="agent-response-field"
							id={contentId}
							onChange={(event) => setContent(event.target.value)}
							value={content}
						/>
					</div>
					<div className="dialog-field">
						<Label htmlFor={toolsId}>Tools used (comma separated)</Label>
						<Input
							id={toolsId}
							onChange={(event) => setTools(event.target.value)}
							placeholder="Only tools allowed by the approval"
							value={tools}
						/>
					</div>
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button
							disabled={respond.isPending || !summary.trim() || !content.trim()}
							onClick={() =>
								respond.mutate({
									content: content.trim(),
									status: "completed",
									summary: summary.trim(),
									tools_used: tools
										.split(",")
										.map((tool) => tool.trim())
										.filter(Boolean),
								})
							}
							type="button"
						>
							{respond.isPending ? "Sending…" : "Send response"}
						</Button>
					</DialogFooter>
				</div>
			</DialogContent>
		</Dialog>
	);
}

function TabSection({
	action,
	children,
	description,
	title,
}: {
	action?: ReactNode;
	children: ReactNode;
	description: string;
	title: string;
}) {
	return (
		<section className="agent-section">
			<header>
				<div>
					<h2>{title}</h2>
					<p>{description}</p>
				</div>
				{action}
			</header>
			{children}
		</section>
	);
}

type StatusTone = "active" | "admin" | "danger" | "neutral" | "warning";
function statusTone(status: string): StatusTone {
	if (["active", "approved", "completed", "succeeded"].includes(status))
		return "active";
	if (["failed", "rejected"].includes(status)) return "danger";
	if (["pending", "queued", "running", "needs_confirmation"].includes(status))
		return "warning";
	return "neutral";
}

function Status({ status }: { status: string }) {
	return (
		<StatusBadge tone={statusTone(status)}>
			{titleCaseStatus(status)}
		</StatusBadge>
	);
}
