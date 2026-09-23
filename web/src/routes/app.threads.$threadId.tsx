import { useAgentChat } from "@cloudflare/ai-chat/react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useAgent } from "agents/react";
import { FileDown, Inbox, ShieldQuestion, Trash2 } from "lucide-react";
import { type UIEvent, useEffect, useRef, useState } from "react";
import { AppAlert, QueryError } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import { ThreadComposer } from "@/components/app/thread-composer";
import { MessageMarkdown } from "@/components/threads/message-markdown";
import {
	groupToolActivity,
	ToolActivity,
} from "@/components/threads/tool-activity";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { apiErrorMessage } from "@/lib/api/client";
import { getSession } from "@/lib/auth/client";
import { useWorkspace } from "@/lib/auth/session";
import { reopenSocket } from "@/lib/threads/agent-socket";
import {
	chatMessagesToItems,
	reconcileChatMessages,
	sessionExpiredCode,
	type ThreadChatMessage,
	threadAgentName,
} from "@/lib/threads/chat-messages";
import {
	artifactContentUrl,
	type ConfirmationAnswer,
	deleteThread,
	getThread,
	threadQueryKey,
	threadQueryOptions,
	threadsQueryKey,
} from "@/lib/threads/client";
import { takePendingMessage } from "@/lib/threads/pending-message";
import {
	type ArtifactEntry,
	artifactFileName,
	buildTimeline,
	type ConfirmationEntry,
	confirmationHint,
	humanizeToolName,
	isImageArtifact,
	type MessageEntry,
	type TimelineEntry,
} from "@/lib/threads/timeline";

const appsUpdatePreviewLines = 4;

export const Route = createFileRoute("/app/threads/$threadId")({
	component: ThreadConversationPage,
	head: () => ({ meta: [{ title: "Conversation · The Weirdly ODD" }] }),
});

function runFailureMessage(error: unknown): string {
	return apiErrorMessage(
		error,
		"Your primary agent could not answer. Try again.",
	);
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
	const agent = useAgent({
		agent: "ThreadChatAgent",
		name: threadAgentName(organization.id, threadId),
		// A dropped socket reconnects by itself with whatever cookie the browser
		// holds, and the Worker turns one away whose access cookie has expired.
		// Reading the session refreshes that cookie for the next attempt.
		onClose: (event) => {
			if (event.code !== 1000) void getSession().catch(() => {});
		},
	});

	const [message, setMessage] = useState("");
	const [runError, setRunError] = useState("");
	const [resending, setResending] = useState(false);
	// The last send, so it can go again once a refreshed session has reached
	// the Durable Object — and only once. `expired` holds the Durable Object's
	// word that it could not start, until that send's stream ends.
	const lastSendRef = useRef<{
		body?: object;
		expired: boolean;
		retried: boolean;
	}>({ expired: false, retried: false });
	const [draftChoices, setDraftChoices] = useState<Record<string, boolean>>({});
	const [submittedAnswers, setSubmittedAnswers] = useState<
		Record<string, boolean>
	>({});
	const scrollRef = useRef<HTMLDivElement>(null);
	const stickToBottomRef = useRef(true);
	const chat = useAgentChat<unknown, ThreadChatMessage>({
		agent,
		resume: true,
		getInitialMessages: async ({ url }) => {
			await getSession();
			let stored: ThreadChatMessage[] = [];
			if (url) {
				try {
					const response = await fetch(`${url}/get-messages`, {
						credentials: "include",
					});
					if (response.ok) {
						stored = (await response.json()) as ThreadChatMessage[];
					}
				} catch {
					// An older conversation can still be read from the Go service.
				}
			}
			const existing = await getThread(organization.id, threadId);
			return reconcileChatMessages(stored, existing.items);
		},
		onData: (part) => {
			if (part.type !== "data-oryxa" || part.data.type !== "run_failed") {
				return;
			}
			if (
				part.data.code === sessionExpiredCode &&
				!lastSendRef.current.retried
			) {
				// The socket's cookie expired before Go started the run, so
				// nothing ran; it goes again once this stream ends.
				lastSendRef.current.retried = true;
				lastSendRef.current.expired = true;
				return;
			}
			setRunError(part.data.error ?? "Your primary agent could not answer.");
		},
		onError: (error) => setRunError(runFailureMessage(error)),
		onFinish: ({ isAbort, isDisconnect, isError }) => {
			if (lastSendRef.current.expired) {
				lastSendRef.current.expired = false;
				if (!isAbort && !isDisconnect && !isError) {
					void resendWithFreshSession();
				}
				return;
			}
			void queryClient.invalidateQueries({
				queryKey: threadQueryKey(organization.id, threadId),
			});
			void queryClient.invalidateQueries({
				queryKey: threadsQueryKey(organization.id),
				refetchType: "inactive",
			});
		},
	});
	const running =
		resending ||
		chat.status === "submitted" ||
		chat.status === "streaming" ||
		chat.isServerStreaming;
	useEffect(() => {
		if (!thread.data || running || agent.readyState !== 1) return;
		const merged = reconcileChatMessages(chat.messages, thread.data.items);
		if (merged !== chat.messages) chat.setMessages(merged);
	}, [agent.readyState, chat.messages, chat.setMessages, running, thread.data]);

	// The home composer cannot send a message before its thread exists, so it
	// stashes the draft for the conversation page to pick up and send once.
	// `takePendingMessage` removes it as it is read, so a second invocation
	// (a re-render, or React StrictMode's extra effect pass) finds nothing.
	useEffect(() => {
		const pending = takePendingMessage(threadId);
		if (pending) {
			lastSendRef.current = { expired: false, retried: false };
			void chat
				.sendMessage({ text: pending })
				.catch((error) => setRunError(runFailureMessage(error)));
		}
	}, [threadId, chat.sendMessage]);

	useEffect(() => {
		const container = scrollRef.current;
		if (container && stickToBottomRef.current) {
			container.scrollTop = container.scrollHeight;
		}
	});

	function handleScroll(event: UIEvent<HTMLDivElement>) {
		const target = event.currentTarget;
		const distanceFromBottom =
			target.scrollHeight - target.scrollTop - target.clientHeight;
		stickToBottomRef.current = distanceFromBottom < 96;
	}

	function sendMessage(text: string) {
		setMessage("");
		setRunError("");
		lastSendRef.current = { expired: false, retried: false };
		void chat
			.sendMessage({ text })
			.catch((error) => setRunError(runFailureMessage(error)));
	}

	// Reading the session refreshes the expired cookie, the reopened socket
	// carries it to the Durable Object, and regenerating makes the same
	// request again without adding the member's message twice.
	async function resendWithFreshSession() {
		setResending(true);
		try {
			await getSession();
			await reopenSocket(agent);
			await chat.regenerate({ body: lastSendRef.current.body });
		} catch (error) {
			setRunError(runFailureMessage(error));
		} finally {
			setResending(false);
		}
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
		setRunError("");
		const body = { confirmations: answers };
		lastSendRef.current = { body, expired: false, retried: false };
		void chat
			.sendMessage(
				{ text: " ", metadata: { oryxaConfirmation: true } },
				{ body },
			)
			.catch((error) => setRunError(runFailureMessage(error)));
	}

	function stop() {
		void chat.stop();
	}

	if (thread.isPending && chat.messages.length === 0) {
		return <ConversationSkeleton />;
	}

	if (thread.isError && !running && chat.messages.length === 0) {
		return (
			<div className="thread-conversation-error">
				<QueryError
					error={thread.error}
					fallback="Could not load this conversation"
					onRetry={thread.refetch}
				/>
			</div>
		);
	}

	const timeline = buildTimeline(chatMessagesToItems(chat.messages));
	const entries: TimelineEntry[] = timeline;
	const displayEntries = groupToolActivity(entries);

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

	const composerDisabled = running || actionable.length > 0;
	const composerHint = running
		? "Your primary agent is answering…"
		: actionable.length > 0
			? `Answer the confirmation${actionable.length > 1 ? "s" : ""} above to continue.`
			: undefined;
	const connectionState = running
		? "working"
		: agent.readyState === 1
			? "live"
			: agent.connectionError
				? "disconnected"
				: "connecting";
	const connectionLabel = {
		working: "Working",
		live: "Live",
		disconnected: "Reconnecting",
		connecting: "Connecting",
	}[connectionState];

	return (
		<div className="thread-conversation">
			<header className="thread-conversation-header">
				<div className="thread-conversation-identity">
					<h1>{thread.data?.title || "New conversation"}</h1>
					<output
						className="thread-connection-status"
						data-state={connectionState}
					>
						<span aria-hidden="true" className="thread-connection-dot" />
						{connectionLabel}
					</output>
				</div>
				<ConfirmDialog
					confirmLabel="Delete conversation"
					description="This conversation and its history are removed for everyone in the organization."
					onConfirm={async () => {
						await deleteThread(organization.id, threadId);
						await queryClient.invalidateQueries({
							queryKey: threadsQueryKey(organization.id),
						});
						await navigate({ to: "/app" });
					}}
					title="Delete this conversation?"
					trigger={
						<Button
							aria-label="Delete conversation"
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
					{displayEntries.map((entry) => {
						if (entry.kind === "message") {
							return entry.author === "apps" ? (
								<AppsUpdateBlock entry={entry} key={entry.id} />
							) : (
								<MessageBlock entry={entry} key={entry.id} />
							);
						}
						if (entry.kind === "tool_activity") {
							return <ToolActivity key={entry.id} tools={entry.tools} />;
						}
						if (entry.kind === "artifact") {
							return (
								<ArtifactBlock
									entry={entry}
									key={entry.id}
									organizationId={organization.id}
									threadId={threadId}
								/>
							);
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
								submitting={running}
							/>
						);
					})}
					{multiple ? (
						<div className="thread-confirmations-submit">
							<Button
								disabled={
									running ||
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
					streaming={running}
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
				{isMember ? entry.text : <MessageMarkdown text={entry.text} />}
			</div>
			{entry.error ? (
				<p className="thread-message-notice">{entry.error}</p>
			) : null}
		</div>
	);
}

/**
 * A batch of events from the member's connected apps that woke Krude
 * (`author: "apps"`). It reads as a system notice, not a message from
 * either side of the conversation, and starts collapsed since a batch can
 * run long.
 */
function AppsUpdateBlock({ entry }: { entry: MessageEntry }) {
	const [expanded, setExpanded] = useState(false);
	const lines = entry.text.split("\n");
	const hasMore = lines.length > appsUpdatePreviewLines;
	const shown =
		expanded || !hasMore
			? entry.text
			: lines.slice(0, appsUpdatePreviewLines).join("\n");

	return (
		<div className="thread-apps-update">
			<div className="thread-apps-update-label">
				<Inbox aria-hidden="true" />
				<span>From your apps</span>
			</div>
			<div className="thread-apps-update-text">{shown}</div>
			{hasMore ? (
				<Button
					className="thread-apps-update-toggle"
					onClick={() => setExpanded((value) => !value)}
					size="sm"
					type="button"
					variant="ghost"
				>
					{expanded ? "Show less" : "Show more"}
				</Button>
			) : null}
		</div>
	);
}

interface ArtifactBlockProps {
	entry: ArtifactEntry;
	organizationId: string;
	threadId: string;
}

/**
 * A file a run kept. Cookies authenticate the artifacts route on this same
 * origin, so a plain `<img>`/`<a>` fetches it with no token handling here.
 */
function ArtifactBlock({
	entry,
	organizationId,
	threadId,
}: ArtifactBlockProps) {
	const url = artifactContentUrl(
		organizationId,
		threadId,
		entry.name,
		entry.version,
	);
	if (isImageArtifact(entry.name)) {
		return (
			<figure className="thread-artifact">
				<a href={url} rel="noreferrer" target="_blank">
					<img alt={entry.name} className="thread-artifact-thumb" src={url} />
				</a>
				<figcaption className="thread-artifact-name">{entry.name}</figcaption>
			</figure>
		);
	}
	return (
		<a
			className="thread-artifact-download"
			download={artifactFileName(entry.name)}
			href={url}
		>
			<FileDown aria-hidden="true" />
			<span>{entry.name}</span>
		</a>
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
			<p className="thread-confirmation-hint">
				{confirmationHint(entry.tool, entry.hint)}
			</p>
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
