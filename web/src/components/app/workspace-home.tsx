import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowUpRight, Blocks, Clock3, ListTodo, ScanText } from "lucide-react";
import { useRef, useState } from "react";
import { ThreadComposer } from "@/components/app/thread-composer";
import { WorkspaceSection } from "@/components/app/workspace-section";
import { Skeleton } from "@/components/ui/skeleton";
import { apiErrorMessage } from "@/lib/api/client";
import { useWorkspace } from "@/lib/auth/session";
import { relativeTime } from "@/lib/format";
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
				setError(apiErrorMessage(caught, "Could not start a new thread")),
		});
	}

	return (
		<section className="workspace-launchpad">
			<header className="workspace-launchpad-header">
				<span>{organization.name}</span>
				<h1>What are we working on?</h1>
			</header>

			<ThreadComposer
				disabled={startThread.isPending}
				inputRef={composerRef}
				onChange={setDraft}
				onSubmit={submit}
				value={draft}
			/>
			{error ? <p className="workspace-launchpad-error">{error}</p> : null}

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
					action={<Link to="/app/threads">View all</Link>}
					title="Recent"
				>
					<RecentThreads />
				</WorkspaceSection>
			</div>
		</section>
	);
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
				{apiErrorMessage(threads.error, "Could not load recent threads.")}
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
					<span>{thread.title || "New thread"}</span>
					<small>
						<Clock3 />
						{relativeTime(thread.updated_at)}
					</small>
				</Link>
			))}
		</div>
	);
}
