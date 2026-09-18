import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import {
	CircleAlert,
	CircleCheck,
	Loader2,
	ShieldQuestion,
	Trash2,
} from "lucide-react";
import { type UIEvent, useEffect, useRef, useState } from "react";
import { AppAlert, QueryError } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import { ThreadComposer } from "@/components/app/thread-composer";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError, apiErrorMessage } from "@/lib/api/client";
import { useWorkspace } from "@/lib/auth/session";
import {
	type ConfirmationAnswer,
	deleteThread,
	type Item,
	type RunInput,
	startRun,
	threadQueryKey,
	threadQueryOptions,
	threadsQueryKey,
} from "@/lib/threads/client";
import { takePendingMessage } from "@/lib/threads/pending-message";
import {
	buildTimeline,
	type ConfirmationEntry,
	humanizeToolName,
	type MessageEntry,
	type TimelineEntry,
	type ToolEntry,
} from "@/lib/threads/timeline";

export const Route = createFileRoute("/app/threads/$threadId")({
	component: ThreadConversationPage,
	head: () => ({ meta: [{ title: "Thread · The Weirdly ODD" }] }),
});

function isAbortError(error: unknown): boolean {
	return error instanceof Error && error.name === "AbortError";
}

function runFailureMessage(error: unknown): string {
	if (error instanceof ApiError) {
		if (error.code === "run_in_progress") {
			return "Krude is still answering in this thread.";
		}
		if (error.code === "agents_unavailable") {
			return "Krude isn't available right now.";
		}
	}
	return apiErrorMessage(error, "Krude could not answer. Try again.");
}

function formatJson(value: Record<string, unknown>): string {
	return JSON.stringify(value, null, 2);
}

function ThreadConversationPage() {
	const { threadId } = Route.useParams();
	const { organization } = useWorkspace();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const thread = useQuery(threadQueryOptions(organization.id, threadId));

	const [message, setMessage] = useState("");
	const [liveItems, setLiveItems] = useState<Item[]>([]);
	const [optimisticMessage, setOptimisticMessage] = useState<string>();
	const [runError, setRunError] = useState("");
	const [draftChoices, setDraftChoices] = useState<Record<string, boolean>>({});
	const [submittedAnswers, setSubmittedAnswers] = useState<
		Record<string, boolean>
	>({});
	const abortControllerRef = useRef<AbortController | null>(null);
	const scrollRef = useRef<HTMLDivElement>(null);
	const stickToBottomRef = useRef(true);

	const runMutation = useMutation({
		mutationFn: async (input: RunInput) => {
			setRunError("");
			setLiveItems([]);
			const controller = new AbortController();
			abortControllerRef.current = controller;
			try {
				await startRun(organization.id, threadId, input, {
					onItem: (item) => {
						setLiveItems((previous) => [...previous, item]);
						if (item.type === "message" && item.author === "member") {
							setOptimisticMessage(undefined);
						}
						if (item.type === "run_failed") {
							setRunError(item.error ?? "Krude could not answer.");
						}
					},
					signal: controller.signal,
				});
			} finally {
				abortControllerRef.current = null;
				await queryClient.invalidateQueries({
					queryKey: threadQueryKey(organization.id, threadId),
				});
				// The list's ordering and title can change too, but there is no
				// need to refetch it while nobody is looking at it.
				await queryClient.invalidateQueries({
					queryKey: threadsQueryKey(organization.id),
					refetchType: "inactive",
				});
				setLiveItems([]);
				setOptimisticMessage(undefined);
			}
		},
		onError: (error) => {
			if (!isAbortError(error)) {
				setRunError(runFailureMessage(error));
			}
		},
	});

	// The home composer cannot send a message before its thread exists, so it
	// stashes the draft for the conversation page to pick up and send once.
	// `takePendingMessage` removes it as it is read, so a second invocation
	// (a re-render, or React StrictMode's extra effect pass) finds nothing.
	useEffect(() => {
		const pending = takePendingMessage(threadId);
		if (pending) {
			setOptimisticMessage(pending);
			runMutation.mutate({ message: pending });
		}
	}, [threadId, runMutation.mutate]);

	useEffect(() => {
		const container = scrollRef.current;
		if (container && stickToBottomRef.current) {
			container.scrollTop = container.scrollHeight;
		}
	});

	// Leaving the page mid-run should not leave the stream running for nobody.
	useEffect(() => {
		return () => {
			abortControllerRef.current?.abort();
		};
	}, []);

	function handleScroll(event: UIEvent<HTMLDivElement>) {
		const target = event.currentTarget;
		const distanceFromBottom =
			target.scrollHeight - target.scrollTop - target.clientHeight;
		stickToBottomRef.current = distanceFromBottom < 96;
	}

	function sendMessage(text: string) {
		setMessage("");
		setOptimisticMessage(text);
		runMutation.mutate({ message: text });
	}

	function submitConfirmations(answers: ConfirmationAnswer[]) {
		setDraftChoices({});
		setSubmittedAnswers((previous) => {
			const next = { ...previous };
			for (const answer of answers) {
				next[answer.id] = answer.confirmed;
			}
			return next;
		});
		runMutation.mutate({ confirmations: answers });
	}

	function stop() {
		abortControllerRef.current?.abort();
	}

	if (thread.isPending && liveItems.length === 0) {
		return <ConversationSkeleton />;
	}

	if (thread.isError && !runMutation.isPending && liveItems.length === 0) {
		return (
			<div className="thread-conversation-error">
				<QueryError
					error={thread.error}
					fallback="Could not load this thread"
					onRetry={thread.refetch}
				/>
			</div>
		);
	}

	const history = thread.data?.items ?? [];
	const timeline = buildTimeline([...history, ...liveItems]);
	const memberEchoed = liveItems.some(
		(item) => item.type === "message" && item.author === "member",
	);
	const entries: TimelineEntry[] =
		optimisticMessage !== undefined && !memberEchoed
			? [
					...timeline,
					{
						author: "member",
						id: "optimistic",
						kind: "message",
						pending: false,
						text: optimisticMessage,
					} satisfies MessageEntry,
				]
			: timeline;

	const pendingIds = new Set(thread.data?.pending_confirmations ?? []);
	for (const entry of timeline) {
		if (entry.kind === "confirmation" && entry.status === "pending") {
			pendingIds.add(entry.callId);
		}
	}
	const confirmationEntries = entries.filter(
		(entry): entry is ConfirmationEntry => entry.kind === "confirmation",
	);
	const actionable = confirmationEntries.filter(
		(entry) =>
			pendingIds.has(entry.callId) && !(entry.callId in submittedAnswers),
	);
	const multiple = actionable.length > 1;

	const composerDisabled = runMutation.isPending || actionable.length > 0;
	const composerHint = runMutation.isPending
		? "Krude is answering…"
		: actionable.length > 0
			? `Answer the confirmation${actionable.length > 1 ? "s" : ""} above to continue.`
			: undefined;

	return (
		<div className="thread-conversation">
			<header className="thread-conversation-header">
				<h1>{thread.data?.title || "New thread"}</h1>
				<ConfirmDialog
					confirmLabel="Delete thread"
					description="This conversation and its history are removed for everyone in the organization."
					onConfirm={async () => {
						await deleteThread(organization.id, threadId);
						await queryClient.invalidateQueries({
							queryKey: threadsQueryKey(organization.id),
						});
						await navigate({ to: "/app/threads" });
					}}
					title="Delete this thread?"
					trigger={
						<Button
							aria-label="Delete thread"
							size="icon-sm"
							type="button"
							variant="ghost"
						>
							<Trash2 />
						</Button>
					}
				/>
			</header>

			<div
				className="thread-conversation-timeline"
				onScroll={handleScroll}
				ref={scrollRef}
			>
				<div className="thread-conversation-body">
					{entries.length === 0 ? (
						<p className="thread-conversation-empty">
							Send a message to get started.
						</p>
					) : null}
					{entries.map((entry) => {
						if (entry.kind === "message") {
							return <MessageBlock entry={entry} key={entry.id} />;
						}
						if (entry.kind === "tool") {
							return <ToolBlock entry={entry} key={entry.callId} />;
						}
						const isActionable =
							pendingIds.has(entry.callId) &&
							!(entry.callId in submittedAnswers);
						return (
							<ConfirmationBlock
								actionable={isActionable}
								choice={draftChoices[entry.callId]}
								displayStatus={
									entry.callId in submittedAnswers
										? submittedAnswers[entry.callId]
											? "approved"
											: "declined"
										: entry.status
								}
								entry={entry}
								key={entry.callId}
								multiple={multiple}
								onChoose={(confirmed) =>
									setDraftChoices((previous) => ({
										...previous,
										[entry.callId]: confirmed,
									}))
								}
								onSubmitOne={(confirmed) =>
									submitConfirmations([{ confirmed, id: entry.callId }])
								}
								submitting={runMutation.isPending}
							/>
						);
					})}
					{multiple ? (
						<div className="thread-confirmations-submit">
							<Button
								disabled={
									runMutation.isPending ||
									actionable.some((entry) => !(entry.callId in draftChoices))
								}
								onClick={() =>
									submitConfirmations(
										actionable.map((entry) => ({
											confirmed: draftChoices[entry.callId] ?? false,
											id: entry.callId,
										})),
									)
								}
								type="button"
							>
								Submit answers
							</Button>
						</div>
					) : null}
				</div>
			</div>

			<div className="thread-composer-dock">
				{runError ? <AppAlert>{runError}</AppAlert> : null}
				<ThreadComposer
					disabled={composerDisabled}
					hint={composerHint}
					onChange={setMessage}
					onStop={stop}
					onSubmit={sendMessage}
					streaming={runMutation.isPending}
					value={message}
				/>
			</div>
		</div>
	);
}

function ConversationSkeleton() {
	return (
		<div className="thread-conversation">
			<div className="thread-conversation-timeline">
				<div className="thread-conversation-body" aria-busy="true">
					<div className="thread-message" data-author="agent">
						<Skeleton className="h-16 w-64 rounded-2xl" />
					</div>
					<div className="thread-message" data-author="member">
						<Skeleton className="h-8 w-40 rounded-2xl" />
					</div>
					<div className="thread-message" data-author="agent">
						<Skeleton className="h-10 w-52 rounded-2xl" />
					</div>
				</div>
			</div>
		</div>
	);
}

function MessageBlock({ entry }: { entry: MessageEntry }) {
	const isMember = entry.author === "member";
	return (
		<div className="thread-message" data-author={isMember ? "member" : "agent"}>
			<div
				aria-live={entry.pending ? "polite" : undefined}
				className="thread-message-bubble"
			>
				{entry.text}
			</div>
			{entry.error ? (
				<p className="thread-message-notice">{entry.error}</p>
			) : null}
		</div>
	);
}

function toolStateLabel(state: ToolEntry["state"]): string {
	if (state === "running") {
		return "Running…";
	}
	return state === "failed" ? "Failed" : "Done";
}

function ToolBlock({ entry }: { entry: ToolEntry }) {
	const detail = entry.state === "failed" ? entry.errorText : entry.resultText;
	return (
		<details className="thread-tool" data-state={entry.state}>
			<summary className="thread-tool-summary">
				{entry.state === "running" ? (
					<Loader2
						aria-hidden="true"
						className="thread-tool-icon animate-spin"
					/>
				) : entry.state === "failed" ? (
					<CircleAlert aria-hidden="true" className="thread-tool-icon" />
				) : (
					<CircleCheck aria-hidden="true" className="thread-tool-icon" />
				)}
				<span>{humanizeToolName(entry.tool)}</span>
				<em>{toolStateLabel(entry.state)}</em>
			</summary>
			{entry.arguments ? (
				<pre className="thread-tool-detail">{formatJson(entry.arguments)}</pre>
			) : null}
			{detail ? <pre className="thread-tool-detail">{detail}</pre> : null}
		</details>
	);
}

interface ConfirmationBlockProps {
	actionable: boolean;
	choice: boolean | undefined;
	displayStatus: "pending" | "approved" | "declined";
	entry: ConfirmationEntry;
	multiple: boolean;
	onChoose: (confirmed: boolean) => void;
	onSubmitOne: (confirmed: boolean) => void;
	submitting: boolean;
}

function ConfirmationBlock({
	actionable,
	choice,
	displayStatus,
	entry,
	multiple,
	onChoose,
	onSubmitOne,
	submitting,
}: ConfirmationBlockProps) {
	return (
		<div className="thread-confirmation" data-status={displayStatus}>
			<div className="thread-confirmation-header">
				<ShieldQuestion aria-hidden="true" />
				<span>
					{entry.tool ? humanizeToolName(entry.tool) : "Confirmation needed"}
				</span>
			</div>
			{entry.hint ? (
				<p className="thread-confirmation-hint">{entry.hint}</p>
			) : null}
			{entry.arguments ? (
				<pre className="thread-confirmation-args">
					{formatJson(entry.arguments)}
				</pre>
			) : null}
			{actionable ? (
				<div className="thread-confirmation-actions">
					<Button
						data-selected={multiple && choice === false}
						disabled={submitting}
						onClick={() => (multiple ? onChoose(false) : onSubmitOne(false))}
						size="sm"
						type="button"
						variant="ghost"
					>
						Decline
					</Button>
					<Button
						data-selected={multiple && choice === true}
						disabled={submitting}
						onClick={() => (multiple ? onChoose(true) : onSubmitOne(true))}
						size="sm"
						type="button"
					>
						Approve
					</Button>
				</div>
			) : (
				<span
					className="thread-confirmation-status"
					data-status={displayStatus}
				>
					{displayStatus === "approved" ? "Approved" : "Declined"}
				</span>
			)}
		</div>
	);
}
