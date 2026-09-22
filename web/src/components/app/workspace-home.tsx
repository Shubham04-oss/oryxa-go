import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import {
	ArrowRight,
	ArrowUpRight,
	Blocks,
	Bot,
	Clock3,
	ListTodo,
	Network,
	ScanText,
	Workflow,
} from "lucide-react";
import { useRef, useState } from "react";
import { ThreadComposer } from "@/components/app/thread-composer";
import { useWorkspaceSearch } from "@/components/app/workspace-search";
import { WorkspaceSection } from "@/components/app/workspace-section";
import { Skeleton } from "@/components/ui/skeleton";
import {
	agentsQueryOptions,
	agentTeamsQueryOptions,
	agentWorkflowsQueryOptions,
} from "@/lib/agents/client";
import { apiErrorMessage } from "@/lib/api/client";
import { useWorkspace } from "@/lib/auth/session";
import { relativeTime } from "@/lib/format";
import { teamspacesQueryOptions } from "@/lib/teamspaces/client";
import {
	createThread,
	threadsQueryKey,
	threadsQueryOptions,
} from "@/lib/threads/client";
import { stashPendingMessage } from "@/lib/threads/pending-message";

const starters = [
	{
		description: "Turn an idea into clear next steps",
		icon: ListTodo,
		prompt: "Help me plan a project from the first step.",
		title: "Plan something",
		tone: "warm",
	},
	{
		description: "Bring in a file, repository, or decision",
		icon: ScanText,
		prompt: "Help me review something and surface what matters.",
		title: "Review something",
		tone: "rose",
	},
	{
		description: "Work across the apps already connected",
		icon: Blocks,
		prompt: "Help me work with one of my connected tools.",
		title: "Use a tool",
		tone: "sage",
	},
] as const;

export function WorkspaceHome() {
	const { organization } = useWorkspace();
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const composerRef = useRef<HTMLTextAreaElement>(null);
	const search = useWorkspaceSearch();
	const [draft, setDraft] = useState("");
	const [error, setError] = useState("");

	const startThread = useMutation({
		mutationFn: async (message: string) => {
			const thread = await createThread(organization.id);
			// The thread does not exist yet when the member hits send, so its
			// first message rides along to the conversation page instead.
			stashPendingMessage(thread.id, message);
			return thread;
		},
		onSuccess: async (thread) => {
			setDraft("");
			await queryClient.invalidateQueries({
				queryKey: threadsQueryKey(organization.id),
			});
			await navigate({
				params: { threadId: thread.id },
				to: "/app/threads/$threadId",
			});
		},
	});

	function applyStarter(prompt: string) {
		setDraft(prompt);
		requestAnimationFrame(() => composerRef.current?.focus());
	}

	function submit(message: string) {
		setError("");
		startThread.mutate(message, {
			onError: (caught) =>
				setError(apiErrorMessage(caught, "Could not start a new conversation")),
		});
	}

	return (
		<section className="workspace-launchpad">
			<header className="workspace-launchpad-header">
				<span>Solo workspace · {organization.name}</span>
				<h1>What do you want to move forward?</h1>
				<p>
					Start privately with your primary agent, coordinate specialists, or
					publish deliberate work into a Teamspace.
				</p>
			</header>

			<ThreadComposer
				disabled={startThread.isPending}
				inputRef={composerRef}
				onChange={setDraft}
				onSubmit={submit}
				value={draft}
			/>
			{error ? <p className="workspace-launchpad-error">{error}</p> : null}

			<WorkModes />

			<div className="workspace-home-panels">
				<WorkspaceSection title="Start here">
					<div className="workspace-naked-list">
						{starters.map((starter) => {
							const Icon = starter.icon;
							return (
								<button
									className="workspace-starter-row"
									data-tone={starter.tone}
									key={starter.title}
									onClick={() => applyStarter(starter.prompt)}
									type="button"
								>
									<Icon aria-hidden="true" />
									<span>
										<strong>{starter.title}</strong>
										<small>{starter.description}</small>
									</span>
									<ArrowUpRight aria-hidden="true" />
								</button>
							);
						})}
					</div>
				</WorkspaceSection>

				<WorkspaceSection
					action={
						<button
							className="workspace-section-action"
							onClick={() => search.open("conversations")}
							type="button"
						>
							View all
						</button>
					}
					title="Recent"
				>
					<RecentThreads />
				</WorkspaceSection>
			</div>
		</section>
	);
}

function WorkModes() {
	const { organization } = useWorkspace();
	const search = useWorkspaceSearch();
	const agents = useQuery(agentsQueryOptions(organization.id));
	const teams = useQuery(agentTeamsQueryOptions(organization.id));
	const workflows = useQuery(agentWorkflowsQueryOptions(organization.id));
	const spaces = useQuery(teamspacesQueryOptions(organization.id));
	const activeAgents = agents.data?.filter(
		(agent) => agent.status === "active",
	);
	const activeWorkflows = workflows.data?.filter(
		(workflow) => workflow.status === "active",
	);

	return (
		<section aria-labelledby="work-modes-title" className="workspace-modes">
			<header>
				<div>
					<span>Work modes</span>
					<h2 id="work-modes-title">One product, clear boundaries</h2>
				</div>
				<p>Private by default. Shared only when you choose a shared surface.</p>
			</header>
			<div className="workspace-mode-grid">
				<WorkModeCard
					description="Talk directly with your primary agent. Its conversations and working context remain yours."
					eyebrow="Single user"
					onSelect={() => search.open("conversations")}
					icon={Bot}
					metric={
						agents.isError
							? "Unavailable"
							: countLabel(activeAgents?.length, "active agent")
					}
					title="Solo work"
				/>
				<WorkModeCard
					description="Create specialist teams and durable workflows coordinated under your identity and permissions."
					eyebrow="Multi-agent"
					href="/app/agents"
					icon={Workflow}
					metric={
						teams.isError || workflows.isError
							? "Unavailable"
							: countPair(teams.data?.length, activeWorkflows?.length)
					}
					title="Agent system"
				/>
				<WorkModeCard
					description="Collaborate with teammates through attributed summaries, bounded specialists, and organization context."
					eyebrow="Multi-user"
					href="/app/teamspaces"
					icon={Network}
					metric={
						spaces.isError
							? "Unavailable"
							: countLabel(spaces.data?.length, "Teamspace")
					}
					title="Teamwork"
				/>
			</div>
		</section>
	);
}

function WorkModeCard({
	description,
	eyebrow,
	href,
	icon: Icon,
	metric,
	onSelect,
	title,
}: {
	description: string;
	eyebrow: string;
	href?: "/app/agents" | "/app/teamspaces";
	icon: typeof Bot;
	metric: string;
	/** For a mode with no page of its own, such as conversations. */
	onSelect?: () => void;
	title: string;
}) {
	const inside = (
		<>
			<span className="workspace-mode-icon" aria-hidden="true">
				<Icon />
			</span>
			<div>
				<small>{eyebrow}</small>
				<strong>{title}</strong>
				<p>{description}</p>
			</div>
			<footer>
				<span>{metric}</span>
				<ArrowRight aria-hidden="true" />
			</footer>
		</>
	);

	if (href) {
		return (
			<Link className="workspace-mode-card" to={href}>
				{inside}
			</Link>
		);
	}
	return (
		<button className="workspace-mode-card" onClick={onSelect} type="button">
			{inside}
		</button>
	);
}

function countLabel(count: number | undefined, singular: string): string {
	if (count === undefined) return "Loading…";
	return `${count} ${count === 1 ? singular : `${singular}s`}`;
}

function countPair(teams: number | undefined, workflows: number | undefined) {
	if (teams === undefined || workflows === undefined) return "Loading…";
	return `${teams} teams · ${workflows} active workflows`;
}

function RecentThreads() {
	const { organization } = useWorkspace();
	const threads = useQuery(threadsQueryOptions(organization.id));

	if (threads.isPending) {
		return (
			<div className="workspace-naked-list">
				{["first", "second", "third"].map((row) => (
					<div className="workspace-recent-row" key={row}>
						<Skeleton className="h-3 w-40" />
						<Skeleton className="h-3 w-12" />
					</div>
				))}
			</div>
		);
	}

	if (threads.isError) {
		return (
			<p className="workspace-naked-empty">
				{apiErrorMessage(threads.error, "Could not load recent conversations.")}
			</p>
		);
	}

	if (threads.data.length === 0) {
		return (
			<p className="workspace-naked-empty">
				Nothing yet. Start a thread above.
			</p>
		);
	}

	return (
		<div className="workspace-naked-list">
			{threads.data.slice(0, 3).map((thread) => (
				<Link
					className="workspace-recent-row"
					key={thread.id}
					params={{ threadId: thread.id }}
					to="/app/threads/$threadId"
				>
					<span>{thread.title || "New conversation"}</span>
					<small>
						<Clock3 />
						{relativeTime(thread.updated_at)}
					</small>
				</Link>
			))}
		</div>
	);
}
