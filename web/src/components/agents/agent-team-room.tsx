import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { MessageCircle, MessagesSquare, Sparkles } from "lucide-react";
import { type UIEvent, useEffect, useRef, useState } from "react";
import { SpecialistConversationDialog } from "@/components/agents/specialist-conversation-dialog";
import { SpecialistAvatar } from "@/components/agents/specialist-identity";
import { AppAlert } from "@/components/app/app-alert";
import { ThreadComposer } from "@/components/app/thread-composer";
import { MessageMarkdown } from "@/components/threads/message-markdown";
import { Button } from "@/components/ui/button";
import {
	type AgentTeam,
	type AgentTeamMessage,
	agentTeamMessagesQueryKey,
	agentTeamMessagesQueryOptions,
	agentTeamThreadsQueryKey,
	agentTeamThreadsQueryOptions,
	type ManagedAgent,
	postAgentTeamMessage,
} from "@/lib/agents/client";
import { apiErrorMessage } from "@/lib/api/client";
import { relativeTime } from "@/lib/format";

interface Seat {
	id: string;
	name: string;
	role: string;
	handle: string;
	active: boolean;
}

// Match the workroom's server handles, including its collision fallback.
export function teamSeats(team: AgentTeam): Seat[] {
	const seats: Seat[] = [
		{
			id: team.coordinator_agent_id,
			name: team.coordinator_name,
			role: "Coordinator",
			handle: "primary",
			active: true,
		},
	];
	const used = new Set(["primary"]);
	for (const member of team.members) {
		let handle = slug(member.name);
		if (!handle || used.has(handle)) handle = slug(member.agent_id);
		used.add(handle);
		seats.push({
			id: member.agent_id,
			name: member.name,
			role: member.role,
			handle,
			active: member.status === "active",
		});
	}
	return seats;
}

function slug(value: string): string {
	return value
		.toLowerCase()
		.replaceAll(" ", "-")
		.replace(/[^a-z0-9-]+/g, "")
		.replace(/^-+|-+$/g, "");
}

function mergeMessages(
	previous: AgentTeamMessage[] = [],
	added: AgentTeamMessage[],
): AgentTeamMessage[] {
	const byId = new Map(previous.map((message) => [message.id, message]));
	for (const message of added) byId.set(message.id, message);
	return [...byId.values()].sort((a, b) => a.seq - b.seq);
}

export function AgentTeamRoom({
	agents,
	organizationId,
	team,
}: {
	agents: ManagedAgent[];
	organizationId: string;
	team: AgentTeam;
}) {
	const queryClient = useQueryClient();
	const messagesKey = agentTeamMessagesQueryKey(organizationId, team.id);
	const room = useQuery(agentTeamMessagesQueryOptions(organizationId, team.id));
	const teamThreads = useQuery(
		agentTeamThreadsQueryOptions(organizationId, team.id),
	);
	const [draft, setDraft] = useState("");
	const [following, setFollowing] = useState(true);
	const transcript = useRef<HTMLOListElement>(null);
	const messages = room.data ?? [];
	const seats = teamSeats(team);
	const lastMessageId = messages.at(-1)?.id;

	useEffect(() => {
		const list = transcript.current;
		if (list && following && lastMessageId) list.scrollTop = list.scrollHeight;
	}, [following, lastMessageId]);

	const post = useMutation({
		mutationFn: (body: string) =>
			postAgentTeamMessage(organizationId, team.id, body),
		onSuccess: (added) => {
			queryClient.setQueryData<AgentTeamMessage[]>(messagesKey, (current) =>
				mergeMessages(current, added),
			);
		},
		onSettled: () => {
			void queryClient.invalidateQueries({ queryKey: messagesKey });
			void queryClient.invalidateQueries({
				queryKey: agentTeamThreadsQueryKey(organizationId, team.id),
			});
		},
	});

	function send(body: string) {
		if (post.isPending || team.status !== "active") return;
		const text = body.trim();
		if (!text) return;
		setDraft("");
		post.mutate(text, { onError: () => setDraft(text) });
	}

	function mention(handle: string) {
		setDraft((current) =>
			current.trim() ? `${current.trimEnd()} @${handle} ` : `@${handle} `,
		);
	}

	function follow(event: UIEvent<HTMLOListElement>) {
		const list = event.currentTarget;
		setFollowing(list.scrollHeight - list.scrollTop - list.clientHeight < 48);
	}

	return (
		<section aria-label={`${team.name} group chat`} className="agent-team-room">
			<header className="agent-team-room-header">
				<div className="agent-team-room-title">
					<span className="agent-eyebrow">Shared workroom</span>
					<h3>{team.name}</h3>
					<p>{team.description}</p>
				</div>
				<div className="agent-team-room-presence">
					<span className="agent-team-room-presence-dot" />
					{team.status === "active" ? "Ready to collaborate" : "Team paused"}
				</div>
			</header>

			<div className="agent-team-room-seats">
				{seats.map((seat) => {
					const agent = agents.find((candidate) => candidate.id === seat.id);
					return (
						<div className="agent-team-room-seat-wrap" key={seat.id}>
							<button
								aria-label={`Mention ${seat.name}, ${seat.role}`}
								className="agent-team-room-seat"
								data-active={seat.active}
								disabled={!seat.active || team.status !== "active"}
								onClick={() => mention(seat.handle)}
								title={`${seat.role} · @${seat.handle}`}
								type="button"
							>
								<SpecialistAvatar
									name={seat.name}
									seed={seat.id}
									size="small"
								/>
								<span>
									<strong>{seat.name}</strong>
									<small>@{seat.handle}</small>
								</span>
							</button>
							{agent?.kind === "specialist" ? (
								<SpecialistConversationDialog
									agent={agent}
									initialThreadId={teamThreads.data?.[seat.id]}
									organizationId={organizationId}
									trigger={
										<Button
											aria-label={`Open conversation with ${seat.name}`}
											disabled={!seat.active || !teamThreads.data?.[seat.id]}
											size="icon-sm"
											title={
												teamThreads.data?.[seat.id]
													? `Open ${seat.name}'s team conversation`
													: "Available after this teammate speaks here"
											}
											type="button"
											variant="ghost"
										>
											<MessageCircle aria-hidden />
										</Button>
									}
								/>
							) : null}
						</div>
					);
				})}
			</div>

			<div className="agent-team-room-conversation">
				{room.isError ? (
					<AppAlert>
						{apiErrorMessage(
							room.error,
							"Could not load this team conversation",
						)}
					</AppAlert>
				) : null}
				{room.isPending ? (
					<p className="agent-team-room-loading">Opening the workroom…</p>
				) : null}
				{!room.isPending && !room.isError && !messages.length ? (
					<div className="agent-team-room-empty">
						<MessagesSquare aria-hidden />
						<strong>Bring the team into the conversation</strong>
						<p>
							Ask the group a question or give it a task. Your primary agent
							coordinates messages without a mention; @mention a specialist to
							ask them directly.
						</p>
						<div className="agent-team-room-starter">
							<Sparkles aria-hidden />
							<span>
								Try: “Plan the next step and ask a teammate to review it.”
							</span>
						</div>
					</div>
				) : null}
				{messages.length ? (
					<ol
						aria-label="Group chat messages"
						className="agent-team-room-transcript"
						onScroll={follow}
						ref={transcript}
					>
						{messages.map((message) => (
							<li data-kind={message.kind} key={message.id}>
								<div className="agent-team-room-message-avatar">
									{message.kind === "agent" ? (
										<SpecialistAvatar
											name={message.author_name}
											seed={message.author_agent_id || message.author_name}
											size="small"
										/>
									) : (
										<span>Y</span>
									)}
								</div>
								<div className="agent-team-room-message-main">
									<div className="agent-team-room-message-byline">
										<strong>{message.author_name}</strong>
										{message.kind === "agent" ? <span>Agent</span> : null}
										<time dateTime={message.created_at}>
											{relativeTime(message.created_at)}
										</time>
									</div>
									{message.kind === "agent" ? (
										<MessageMarkdown text={message.body} />
									) : (
										<p>{message.body}</p>
									)}
								</div>
							</li>
						))}
					</ol>
				) : null}
				{post.isPending ? (
					<div aria-live="polite" className="agent-team-room-thinking">
						<Sparkles aria-hidden /> The team is collaborating…
						<small>Replies and handoffs will appear in this chat.</small>
					</div>
				) : null}
			</div>

			<div className="agent-team-room-composer">
				{post.isError ? (
					<AppAlert>
						{apiErrorMessage(post.error, "The team could not finish this turn")}
					</AppAlert>
				) : null}
				<ThreadComposer
					disabled={post.isPending || team.status !== "active"}
					hint={
						team.status !== "active"
							? "Activate the team to send a message."
							: "Mention a teammate for a direct answer. Agent handoffs stay in this chat; external actions still require your approval."
					}
					label={`Message ${team.name}`}
					onChange={setDraft}
					onSubmit={send}
					placeholder="Ask the team to work together…"
					value={draft}
				/>
			</div>
		</section>
	);
}
