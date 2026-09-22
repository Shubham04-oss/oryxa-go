import { MessageCircle, Send, Sparkles } from "lucide-react";
import { type ReactElement, useEffect, useRef, useState } from "react";
import { AppAlert } from "@/components/app/app-alert";
import { MessageMarkdown } from "@/components/threads/message-markdown";
import { Button } from "@/components/ui/button";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
} from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import {
	getSpecialistThread,
	listSpecialistThreads,
	type ManagedAgent,
	startSpecialistRun,
} from "@/lib/agents/client";
import { apiErrorMessage } from "@/lib/api/client";
import type { Item } from "@/lib/threads/client";
import { SpecialistAvatar } from "./specialist-identity";

interface ConversationLine {
	id: string;
	kind: "member" | "specialist" | "activity";
	text: string;
}

function answerText(text: string): string {
	try {
		const result = JSON.parse(text) as { summary?: unknown; status?: unknown };
		if (typeof result.summary === "string") {
			return result.summary;
		}
	} catch {
		// The model may also return ordinary text; display it as-is.
	}
	return text;
}

export function teamWorkroomPromptLabel(text: string): string | null {
	if (
		text.startsWith("You are ") &&
		text.includes("collaborating in the member's agent team ") &&
		text.includes("Recent group chat follows.")
	) {
		return "Team workroom turn · group context shared with this teammate";
	}
	return null;
}

export function SpecialistConversationDialog({
	agent,
	initialThreadId,
	organizationId,
	trigger,
}: {
	agent: ManagedAgent;
	initialThreadId?: string;
	organizationId: string;
	trigger: ReactElement;
}) {
	const [open, setOpen] = useState(false);
	const [message, setMessage] = useState("");
	const [lines, setLines] = useState<ConversationLine[]>([]);
	const [pending, setPending] = useState<{ id: string; hint: string }[]>([]);
	const [choices, setChoices] = useState<Record<string, boolean>>({});
	const [activity, setActivity] = useState("");
	const [running, setRunning] = useState(false);
	const [loadingHistory, setLoadingHistory] = useState(false);
	const [skipHistory, setSkipHistory] = useState(false);
	const [error, setError] = useState("");
	const threadIdRef = useRef("");
	const abortRef = useRef<AbortController | null>(null);
	const bottomRef = useRef<HTMLDivElement>(null);
	const sequenceRef = useRef(0);

	useEffect(() => {
		bottomRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
	});
	useEffect(() => () => abortRef.current?.abort(), []);
	useEffect(() => {
		if (!open || skipHistory || threadIdRef.current) return;
		let current = true;
		setLoadingHistory(true);
		void (async () => {
			const threadId =
				initialThreadId ||
				(await listSpecialistThreads(organizationId, agent.id))[0]?.id;
			if (!current || !threadId) return;
			const detail = await getSpecialistThread(
				organizationId,
				agent.id,
				threadId,
			);
			if (!current || threadIdRef.current) return;
			threadIdRef.current = detail.id;
			const restored = detail.items
				.filter(
					(item): item is Item & { text: string } =>
						item.type === "message" &&
						typeof item.text === "string" &&
						item.text.length > 0,
				)
				.map((item, index): ConversationLine => {
					const workroomLabel =
						item.author === "member"
							? teamWorkroomPromptLabel(item.text)
							: null;
					return {
						id: `stored-${index}`,
						kind: workroomLabel
							? "activity"
							: item.author === "member"
								? "member"
								: "specialist",
						text:
							workroomLabel ||
							(item.author === "member" ? item.text : answerText(item.text)),
					};
				});
			setLines(restored);
			setChoices({});
			setPending(
				detail.items.flatMap((item) =>
					item.type === "confirmation" &&
					item.call_id &&
					detail.pending_confirmations.includes(item.call_id)
						? [
								{
									id: item.call_id,
									hint:
										item.hint ||
										"Allow this specialist to continue with the requested action?",
								},
							]
						: [],
				),
			);
		})()
			.catch((caught) => {
				if (current)
					setError(
						apiErrorMessage(
							caught,
							initialThreadId
								? "Could not open this team's conversation. Try again."
								: "Could not load previous specialist messages. You can still start a new conversation.",
						),
					);
			})
			.finally(() => {
				if (current) setLoadingHistory(false);
			});
		return () => {
			current = false;
		};
	}, [open, skipHistory, agent.id, initialThreadId, organizationId]);

	function append(kind: ConversationLine["kind"], text: string) {
		sequenceRef.current += 1;
		setLines((previous) => [
			...previous,
			{ id: String(sequenceRef.current), kind, text },
		]);
	}

	function handleItem(item: Item) {
		switch (item.type) {
			case "message":
				if (item.author !== "member" && item.text) {
					append("specialist", answerText(item.text));
				}
				break;
			case "tool_call":
				setActivity(`Using ${item.tool || "a tool"}…`);
				append("activity", `Started ${item.tool || "a tool"}`);
				break;
			case "tool_result":
				append(
					"activity",
					item.failed
						? `${item.tool || "Tool"} failed${item.error ? `: ${item.error}` : ""}`
						: `${item.tool || "Tool"} completed`,
				);
				setActivity("Preparing a response…");
				break;
			case "confirmation":
				if (item.call_id) {
					const id = item.call_id;
					setPending((previous) =>
						previous.some((entry) => entry.id === id)
							? previous
							: [
									...previous,
									{
										id,
										hint:
											item.hint ||
											"Allow this specialist to continue with the requested action?",
									},
								],
					);
				}
				setActivity("Waiting for your decision");
				break;
			case "run_failed":
				setError(item.error || "The specialist could not finish this run.");
				setActivity("");
				break;
			case "delta":
				setActivity("Writing a response…");
				break;
			case "run_started":
				setActivity("Working on your request…");
				break;
			case "run_completed":
				setActivity("");
				break;
			default:
				break;
		}
	}

	async function run(input: {
		message?: string;
		confirmations?: { id: string; confirmed: boolean }[];
	}): Promise<boolean> {
		const controller = new AbortController();
		let accepted = false;
		abortRef.current = controller;
		setRunning(true);
		setError("");
		setActivity("Working on your request…");
		try {
			await startSpecialistRun(
				organizationId,
				agent.id,
				{ ...input, thread_id: threadIdRef.current || undefined },
				{
					onThread: (id) => {
						accepted = true;
						threadIdRef.current = id;
					},
					onItem: handleItem,
					signal: controller.signal,
				},
			);
		} catch (caught) {
			if (!(caught instanceof Error && caught.name === "AbortError")) {
				setError(
					apiErrorMessage(
						caught,
						"The specialist could not answer. Try again.",
					),
				);
			}
		} finally {
			if (abortRef.current === controller) abortRef.current = null;
			setRunning(false);
			setActivity("");
		}
		return accepted;
	}

	function send() {
		const text = message.trim();
		if (
			!text ||
			running ||
			loadingHistory ||
			pending.length ||
			(initialThreadId && !threadIdRef.current && !skipHistory)
		)
			return;
		append("member", text);
		setMessage("");
		void run({ message: text });
	}

	function decide(id: string, confirmed: boolean) {
		if (running) return;
		if (pending.length > 1) {
			setChoices((previous) => ({ ...previous, [id]: confirmed }));
			return;
		}
		if (pending.some((entry) => entry.id === id)) {
			submitDecisions([{ id, confirmed }]);
		}
	}

	function submitDecisions(answers: { id: string; confirmed: boolean }[]) {
		if (running || answers.length !== pending.length) return;
		const submitted = pending;
		setPending([]);
		setChoices({});
		append(
			"activity",
			answers.length === 1
				? answers[0].confirmed
					? "You approved the requested action"
					: "You declined the requested action"
				: `You submitted ${answers.length} action decisions`,
		);
		void run({ confirmations: answers }).then((accepted) => {
			// A pre-stream rejection means the server did not receive the choices.
			// Restore all of them so the member can try again.
			if (!accepted) {
				setPending((current) => [
					...submitted.filter(
						(entry) => !current.some((waiting) => waiting.id === entry.id),
					),
					...current,
				]);
			}
		});
	}

	return (
		<Dialog
			onOpenChange={(next) => {
				setOpen(next);
				if (!next) abortRef.current?.abort();
			}}
			open={open}
		>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent className="specialist-conversation-dialog">
				<DialogHeader>
					<div className="specialist-conversation-heading">
						<DialogTitle className="specialist-conversation-title">
							<SpecialistAvatar name={agent.name} seed={agent.id} />
							<span>{agent.name}</span>
						</DialogTitle>
						{threadIdRef.current ? (
							<Button
								disabled={running || loadingHistory}
								onClick={() => {
									setSkipHistory(true);
									threadIdRef.current = "";
									setLines([]);
									setPending([]);
									setChoices({});
									setError("");
								}}
								size="sm"
								type="button"
								variant="ghost"
							>
								New conversation
							</Button>
						) : null}
					</div>
					<DialogDescription>{agent.description}</DialogDescription>
				</DialogHeader>
				<div className="specialist-conversation">
					<div
						className="specialist-conversation-stream"
						role="log"
						aria-label={`${agent.name} conversation`}
					>
						{loadingHistory ? (
							<div className="specialist-conversation-activity">
								Loading previous conversation…
							</div>
						) : null}
						{lines.length === 0 && !loadingHistory ? (
							<div className="specialist-conversation-welcome">
								<Sparkles aria-hidden />
								<strong>Start with the work you need done</strong>
								<p>
									Ask directly, or describe a task for your primary agent to
									coordinate later. This specialist can use only the tools
									available to you and will ask before confirmed actions.
								</p>
							</div>
						) : null}
						{lines.map((line) => (
							<div
								className="specialist-conversation-line"
								data-kind={line.kind}
								key={line.id}
							>
								{line.kind === "specialist" ? (
									<MessageMarkdown text={line.text} />
								) : (
									line.text
								)}
							</div>
						))}
						{activity ? (
							<div
								className="specialist-conversation-activity"
								aria-live="polite"
							>
								<MessageCircle aria-hidden /> {activity}
							</div>
						) : null}
						{pending.map((confirmation) => (
							<div
								className="specialist-conversation-confirmation"
								key={confirmation.id}
							>
								<strong>Action needs your decision</strong>
								<p>{confirmation.hint}</p>
								<div>
									<Button
										aria-pressed={choices[confirmation.id] === false}
										data-selected={choices[confirmation.id] === false}
										disabled={running}
										onClick={() => decide(confirmation.id, false)}
										size="sm"
										type="button"
										variant="outline"
									>
										Decline
									</Button>
									<Button
										aria-pressed={choices[confirmation.id] === true}
										data-selected={choices[confirmation.id] === true}
										disabled={running}
										onClick={() => decide(confirmation.id, true)}
										size="sm"
										type="button"
									>
										Approve
									</Button>
								</div>
							</div>
						))}
						{pending.length > 1 ? (
							<Button
								disabled={
									running ||
									!pending.every((entry) => choices[entry.id] !== undefined)
								}
								onClick={() =>
									submitDecisions(
										pending.map((entry) => ({
											id: entry.id,
											confirmed: choices[entry.id],
										})),
									)
								}
								type="button"
							>
								Submit decisions
							</Button>
						) : null}
						<div ref={bottomRef} />
					</div>
					{error ? <AppAlert>{error}</AppAlert> : null}
					<div className="specialist-conversation-composer">
						<Textarea
							aria-label={`Message ${agent.name}`}
							disabled={
								running ||
								loadingHistory ||
								pending.length > 0 ||
								(initialThreadId && !threadIdRef.current && !skipHistory) ||
								agent.status !== "active"
							}
							onChange={(event) => setMessage(event.target.value)}
							onKeyDown={(event) => {
								if (event.key === "Enter" && !event.shiftKey) {
									event.preventDefault();
									send();
								}
							}}
							placeholder={`Ask ${agent.name} to help with…`}
							rows={2}
							value={message}
						/>
						<Button
							aria-label={`Send message to ${agent.name}`}
							disabled={
								!message.trim() ||
								running ||
								loadingHistory ||
								pending.length > 0 ||
								(initialThreadId && !threadIdRef.current && !skipHistory) ||
								agent.status !== "active"
							}
							onClick={send}
							size="icon"
							type="button"
						>
							<Send aria-hidden />
						</Button>
					</div>
					<small>
						Conversation history is saved in your workspace. External actions
						still need confirmation.
					</small>
				</div>
			</DialogContent>
		</Dialog>
	);
}
