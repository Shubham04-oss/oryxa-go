import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, ShieldQuestion, Sparkles } from "lucide-react";
import {
	type ReactElement,
	type UIEvent,
	useEffect,
	useRef,
	useState,
} from "react";
import { AppAlert } from "@/components/app/app-alert";
import { ThreadComposer } from "@/components/app/thread-composer";
import { MessageMarkdown } from "@/components/threads/message-markdown";
import {
	groupToolActivity,
	ToolActivity,
} from "@/components/threads/tool-activity";
import { Button } from "@/components/ui/button";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
} from "@/components/ui/dialog";
import { agentsQueryKey } from "@/lib/agents/client";
import { ApiError, apiErrorMessage } from "@/lib/api/client";
import {
	artifactContentUrl,
	type ConfirmationAnswer,
	createThread,
	getThread,
	type Item,
	type RunInput,
	startRun,
	threadQueryOptions,
	threadsQueryKey,
} from "@/lib/threads/client";
import {
	buildTimeline,
	type ConfirmationEntry,
	confirmationHint,
	humanizeToolName,
	type MessageEntry,
	unseenLiveItems,
} from "@/lib/threads/timeline";

const examples = [
	"Research competitors and summarize changes that matter to us",
	"Review product launches for risks and missing decisions",
	"Turn customer feedback into recurring product insights",
];
const creationPrefix =
	"I want to create a persistent specialist to own this work: ";

function creationRequest(request: string): string {
	return `${creationPrefix}${request}`;
}

function runFailureMessage(error: unknown): string {
	if (error instanceof ApiError) {
		if (error.code === "run_in_progress") {
			return "Krude is still answering in this conversation.";
		}
		if (error.code === "agents_unavailable") {
			return "Krude isn't available right now.";
		}
	}
	return apiErrorMessage(error, "Krude could not answer. Try again.");
}

export function SpecialistCreationDialog({
	organizationId,
	trigger,
}: {
	organizationId: string;
	trigger: ReactElement;
}) {
	const queryClient = useQueryClient();
	const [open, setOpen] = useState(false);
	const [threadId, setThreadId] = useState<string>();
	const threadIdRef = useRef<string | undefined>(undefined);
	const startedRef = useRef(false);
	const [message, setMessage] = useState("");
	const [optimisticMessage, setOptimisticMessage] = useState<string>();
	const [liveItems, setLiveItems] = useState<Item[]>([]);
	const [runError, setRunError] = useState("");
	const [draftChoices, setDraftChoices] = useState<Record<string, boolean>>({});
	const [submittedAnswers, setSubmittedAnswers] = useState<
		Record<string, boolean>
	>({});
	const abortRef = useRef<AbortController | null>(null);
	const cancelRequestedRef = useRef(false);
	const pendingRef = useRef(false);
	const scrollRef = useRef<HTMLDivElement>(null);
	const stickToBottomRef = useRef(true);
	const openRef = useRef(false);
	const detail = useQuery({
		...threadQueryOptions(organizationId, threadId ?? ""),
		enabled: Boolean(threadId),
	});

	const run = useMutation({
		mutationFn: async (input: RunInput) => {
			setRunError("");
			setLiveItems([]);
			let id = threadIdRef.current;
			if (!id) {
				const created = await createThread(organizationId);
				id = created.id;
				threadIdRef.current = id;
				setThreadId(id);
			}
			if (!openRef.current || cancelRequestedRef.current) {
				if ("message" in input) {
					setMessage(input.message.replace(creationPrefix, ""));
					setOptimisticMessage(undefined);
				}
				cancelRequestedRef.current = false;
				return;
			}
			const controller = new AbortController();
			abortRef.current = controller;
			startedRef.current = true;
			try {
				await startRun(organizationId, id, input, {
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
				abortRef.current = null;
				try {
					const refreshed = await getThread(organizationId, id);
					queryClient.setQueryData(
						threadQueryOptions(organizationId, id).queryKey,
						refreshed,
					);
					setLiveItems([]);
					setOptimisticMessage(undefined);
				} catch {
					// Keep streamed items visible if history cannot be refreshed yet.
				}
				await Promise.all([
					queryClient.invalidateQueries({
						queryKey: agentsQueryKey(organizationId),
					}),
					queryClient.invalidateQueries({
						queryKey: threadsQueryKey(organizationId),
						refetchType: "inactive",
					}),
				]);
			}
		},
		onError: (error, input) => {
			if ("confirmations" in input) {
				setSubmittedAnswers((previous) => {
					const next = { ...previous };
					for (const answer of input.confirmations) delete next[answer.id];
					return next;
				});
			}
			if (
				"message" in input &&
				input.message.startsWith(creationPrefix) &&
				(!threadIdRef.current || error instanceof ApiError)
			) {
				startedRef.current = false;
				setMessage(input.message.slice(creationPrefix.length));
				setOptimisticMessage(undefined);
			}
			if (!(error instanceof Error && error.name === "AbortError")) {
				setRunError(runFailureMessage(error));
			}
		},
		onSettled: () => {
			pendingRef.current = false;
		},
	});

	useEffect(() => {
		const container = scrollRef.current;
		if (open && container && stickToBottomRef.current) {
			container.scrollTop = container.scrollHeight;
		}
	});
	useEffect(() => () => abortRef.current?.abort(), []);

	function changeOpen(next: boolean) {
		openRef.current = next;
		setOpen(next);
		if (!next) abortRef.current?.abort();
	}

	function sendMessage(text: string) {
		if (pendingRef.current) return;
		pendingRef.current = true;
		cancelRequestedRef.current = false;
		stickToBottomRef.current = true;
		setMessage("");
		setOptimisticMessage(text);
		run.mutate({ message: startedRef.current ? text : creationRequest(text) });
	}

	function submitConfirmations(answers: ConfirmationAnswer[]) {
		if (pendingRef.current) return;
		pendingRef.current = true;
		cancelRequestedRef.current = false;
		stickToBottomRef.current = true;
		setDraftChoices({});
		setSubmittedAnswers((previous) => {
			const next = { ...previous };
			for (const answer of answers) next[answer.id] = answer.confirmed;
			return next;
		});
		run.mutate({ confirmations: answers });
	}

	function stop() {
		cancelRequestedRef.current = true;
		abortRef.current?.abort();
	}

	function handleScroll(event: UIEvent<HTMLDivElement>) {
		const container = event.currentTarget;
		stickToBottomRef.current =
			container.scrollHeight - container.scrollTop - container.clientHeight <
			96;
	}

	const history = detail.data?.items ?? [];
	const timeline = buildTimeline([
		...history,
		...unseenLiveItems(history, liveItems),
	]);
	const memberEchoed = liveItems.some(
		(item) => item.type === "message" && item.author === "member",
	);
	const entries = groupToolActivity(
		optimisticMessage !== undefined && !memberEchoed
			? [
					...timeline,
					{
						author: "member",
						id: "optimistic",
						kind: "message" as const,
						pending: false,
						text: optimisticMessage,
					},
				]
			: timeline,
	);
	const pendingIds = new Set(detail.data?.pending_confirmations ?? []);
	for (const item of liveItems) {
		if (item.type === "confirmation" && item.call_id) {
			if (item.status === "pending" || !item.status)
				pendingIds.add(item.call_id);
			else pendingIds.delete(item.call_id);
		}
	}
	const confirmations = timeline.filter(
		(entry): entry is ConfirmationEntry => entry.kind === "confirmation",
	);
	const latestConfirmations = new Map(
		confirmations.map((entry) => [entry.callId, entry]),
	);
	const actionable = [...latestConfirmations.values()].filter(
		(entry) =>
			pendingIds.has(entry.callId) && !(entry.callId in submittedAnswers),
	);
	const multiple = actionable.length > 1;
	const created = timeline.some(
		(entry) =>
			entry.kind === "tool" &&
			entry.tool === "agent_system_create_agent" &&
			entry.state === "done",
	);

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent className="specialist-creation-dialog">
				<DialogHeader className="specialist-creation-header">
					<DialogTitle>Create a specialist with Krude</DialogTitle>
					<DialogDescription>
						Describe the work. Krude will inspect your setup, ask focused
						questions, and design the specialist with you before creation.
					</DialogDescription>
					{threadId ? (
						<Link
							className="specialist-creation-open-link"
							params={{ threadId }}
							to="/app/threads/$threadId"
						>
							Open full conversation <ArrowUpRight aria-hidden="true" />
						</Link>
					) : null}
				</DialogHeader>
				<div
					className="specialist-creation-timeline"
					onScroll={handleScroll}
					ref={scrollRef}
				>
					<div className="specialist-creation-body">
						{entries.length === 0 ? (
							<div className="specialist-creation-intro">
								<Sparkles aria-hidden="true" />
								<strong>What should your specialist own?</strong>
								<p>
									Start with the outcome you want. Krude will explore the
									workflow, tools, audience, boundaries, and measures that shape
									the role.
								</p>
								<div className="specialist-creation-examples">
									{examples.map((example) => (
										<button
											key={example}
											onClick={() => setMessage(example)}
											type="button"
										>
											{example}
										</button>
									))}
								</div>
							</div>
						) : null}
						{entries.map((entry) => {
							if (entry.kind === "message") {
								return <CreationMessage entry={entry} key={entry.id} />;
							}
							if (entry.kind === "tool_activity") {
								return <ToolActivity key={entry.id} tools={entry.tools} />;
							}
							if (entry.kind === "artifact") {
								return threadId ? (
									<a
										className="thread-artifact-download"
										href={artifactContentUrl(
											organizationId,
											threadId,
											entry.name,
											entry.version,
										)}
										key={entry.id}
									>
										{entry.name}
									</a>
								) : null;
							}
							const isActionable =
								pendingIds.has(entry.callId) &&
								latestConfirmations.get(entry.callId) === entry &&
								!(entry.callId in submittedAnswers);
							const displayStatus =
								entry.callId in submittedAnswers
									? submittedAnswers[entry.callId]
										? "approved"
										: "declined"
									: entry.status;
							return (
								<div
									className="thread-confirmation"
									data-status={displayStatus}
									key={`confirmation-${entry.callId}-${entry.status}`}
								>
									<div className="thread-confirmation-header">
										<ShieldQuestion aria-hidden="true" />
										<span>
											{entry.tool
												? humanizeToolName(entry.tool)
												: "Confirmation needed"}
										</span>
									</div>
									<p className="thread-confirmation-hint">
										{entry.tool === "agent_system_create_agent"
											? "Review the exact specialist Krude proposes. Approving creates it."
											: confirmationHint(entry.tool, entry.hint)}
									</p>
									{entry.arguments ? (
										<pre className="thread-confirmation-args">
											{JSON.stringify(entry.arguments, null, 2)}
										</pre>
									) : null}
									{isActionable ? (
										<div className="thread-confirmation-actions">
											<Button
												data-selected={
													multiple && draftChoices[entry.callId] === false
												}
												disabled={run.isPending}
												onClick={() =>
													multiple
														? setDraftChoices((previous) => ({
																...previous,
																[entry.callId]: false,
															}))
														: submitConfirmations([
																{ id: entry.callId, confirmed: false },
															])
												}
												size="sm"
												type="button"
												variant="ghost"
											>
												Decline
											</Button>
											<Button
												data-selected={
													multiple && draftChoices[entry.callId] === true
												}
												disabled={run.isPending}
												onClick={() =>
													multiple
														? setDraftChoices((previous) => ({
																...previous,
																[entry.callId]: true,
															}))
														: submitConfirmations([
																{ id: entry.callId, confirmed: true },
															])
												}
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
											{displayStatus === "approved"
												? "Approved"
												: displayStatus === "declined"
													? "Declined"
													: "Waiting for your decision"}
										</span>
									)}
								</div>
							);
						})}
						{multiple ? (
							<div className="thread-confirmations-submit">
								<Button
									disabled={
										run.isPending ||
										actionable.some((entry) => !(entry.callId in draftChoices))
									}
									onClick={() =>
										submitConfirmations(
											actionable.map((entry) => ({
												confirmed: draftChoices[entry.callId],
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
						{created ? (
							<p className="specialist-creation-success">
								Specialist created. It is now available in your list.
							</p>
						) : null}
					</div>
				</div>
				<div className="specialist-creation-composer">
					{runError || detail.isError ? (
						<AppAlert>
							{runError ||
								"Could not load this conversation. Try reopening it."}
						</AppAlert>
					) : null}
					<ThreadComposer
						disabled={run.isPending || actionable.length > 0}
						hint={
							actionable.length > 0
								? "Review the confirmation above to continue."
								: undefined
						}
						label="Message Krude about this specialist"
						onChange={setMessage}
						onStop={stop}
						onSubmit={sendMessage}
						placeholder="Describe the specialist you need…"
						streaming={run.isPending}
						value={message}
					/>
				</div>
			</DialogContent>
		</Dialog>
	);
}

function CreationMessage({ entry }: { entry: MessageEntry }) {
	const isMember = entry.author === "member";
	const memberText = entry.text.startsWith(creationPrefix)
		? entry.text.slice(creationPrefix.length)
		: entry.text;
	return (
		<div className="thread-message" data-author={isMember ? "member" : "agent"}>
			<div
				aria-live={entry.pending ? "polite" : undefined}
				className="thread-message-bubble"
			>
				{isMember ? memberText : <MessageMarkdown text={entry.text} />}
			</div>
			{entry.error ? (
				<p className="thread-message-notice">{entry.error}</p>
			) : null}
		</div>
	);
}
