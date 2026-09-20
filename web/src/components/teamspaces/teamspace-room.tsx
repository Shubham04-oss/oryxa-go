import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, MessagesSquare, Sparkles } from "lucide-react";
import { type UIEvent, useEffect, useRef, useState } from "react";
import { ThreadComposer } from "@/components/app/thread-composer";
import { Button } from "@/components/ui/button";
import { apiErrorMessage } from "@/lib/api/client";
import { relativeTime } from "@/lib/format";
import {
	catchUpOnRoom,
	type RoomBrief,
	type RoomMessage,
	type RoomRead,
	type RoomSeat,
	roomQueryKey,
	roomQueryOptions,
	sayInRoom,
} from "@/lib/rooms/client";
import { teamspacesQueryKey } from "@/lib/teamspaces/client";

interface TeamspaceRoomProps {
	organizationId: string;
	teamspaceId: string;
	/** The reader, so their own lines are shown as theirs. */
	userId: string;
}

/**
 * The conversation a Teamspace is: the people on the team and their agents
 * talking in one place. Posting is what wakes an agent, so sending blocks
 * until the room has finished answering — the wait is the room thinking, and
 * saying so is better than an optimistic line that is not the whole reply.
 */
export function TeamspaceRoom({
	organizationId,
	teamspaceId,
	userId,
}: TeamspaceRoomProps) {
	const queryClient = useQueryClient();
	const room = useQuery(roomQueryOptions(organizationId, teamspaceId));
	const [draft, setDraft] = useState("");
	const [brief, setBrief] = useState<RoomBrief | undefined>(undefined);
	const transcript = useRef<HTMLOListElement>(null);
	// Follows the conversation while the reader is at the end of it, and stops
	// the moment they scroll back to read something — being yanked away from
	// what you are reading is worse than missing the newest line.
	const [following, setFollowing] = useState(true);
	const messages = room.data?.messages ?? [];
	const seats = room.data?.seats ?? [];
	const latestSeq = messages.at(-1)?.seq ?? 0;

	const lastLine = messages.at(-1)?.id;
	useEffect(() => {
		const list = transcript.current;
		if (!list || !following || !lastLine) return;
		list.scrollTop = list.scrollHeight;
	}, [following, lastLine]);

	const say = useMutation({
		mutationFn: (body: string) => sayInRoom(organizationId, teamspaceId, body),
		onSuccess: (said) => {
			queryClient.setQueryData<RoomRead>(
				roomQueryKey(organizationId, teamspaceId),
				(previous) => ({
					messages: merge(previous?.messages ?? [], said),
					seats: previous?.seats ?? [],
				}),
			);
			// Deciding something in the room publishes it to the ledger beside
			// it. Which message did that is the room's judgment, not the
			// page's, so the ledger is re-read after anything is said.
			queryClient.invalidateQueries({
				queryKey: [
					...teamspacesQueryKey(organizationId),
					"teamspace",
					teamspaceId,
				],
			});
		},
	});

	const catchUp = useMutation({
		mutationFn: () => catchUpOnRoom(organizationId, teamspaceId, latestSeq),
		onSuccess: (caught) => setBrief(caught.brief),
	});

	function follow(event: UIEvent<HTMLOListElement>) {
		const list = event.currentTarget;
		setFollowing(list.scrollHeight - list.scrollTop - list.clientHeight < 48);
	}

	function send(body: string) {
		setDraft("");
		say.mutate(body, { onError: () => setDraft(body) });
	}

	return (
		<section aria-labelledby="room-heading" className="teamspace-room">
			<header>
				<div>
					<h3 id="room-heading">Conversation</h3>
					<p>
						Everyone on this team and their agents. Name somebody with @handle;
						anyone else answers only when the work is plainly theirs.
					</p>
				</div>
				<Button
					disabled={catchUp.isPending}
					onClick={() => catchUp.mutate()}
					size="sm"
					variant="ghost"
				>
					<Sparkles aria-hidden="true" />
					{catchUp.isPending ? "Catching up…" : "Where this has got to"}
				</Button>
			</header>

			{seats.length > 0 ? (
				<div className="teamspace-room-seats">
					{seats.map((seat) => (
						<button
							className="teamspace-room-seat"
							data-kind={seat.kind}
							data-present={seat.present}
							key={seat.handle}
							onClick={() => setDraft((value) => mention(value, seat.handle))}
							title={seatTitle(seat)}
							type="button"
						>
							{seat.kind === "agent" ? <Bot aria-hidden="true" /> : null}@
							{seat.handle}
						</button>
					))}
				</div>
			) : null}

			{brief ? <RoomBriefPanel brief={brief} /> : null}

			{room.isError ? (
				<p className="teamspace-section-error">
					{apiErrorMessage(room.error, "Could not load this conversation")}
				</p>
			) : null}
			{room.isPending ? (
				<p className="teamspace-section-empty">Opening the room…</p>
			) : null}
			{!room.isPending && !room.isError && messages.length === 0 ? (
				<div className="teamspace-ledger-empty">
					<MessagesSquare aria-hidden="true" />
					<div>
						<strong>Nobody has said anything yet</strong>
						<span>
							Say something here and the agent whose work it is will answer.
						</span>
					</div>
				</div>
			) : null}

			{messages.length > 0 ? (
				<ol
					className="teamspace-room-transcript"
					onScroll={follow}
					ref={transcript}
				>
					{messages.map((message) => (
						<RoomLine
							key={message.id}
							message={message}
							mine={
								message.author_kind === "person" &&
								message.author_user_id === userId
							}
							speakingFor={speakingFor(seats, message)}
						/>
					))}
				</ol>
			) : null}

			<div className="teamspace-room-composer">
				<ThreadComposer
					disabled={say.isPending}
					label="Say something to the room"
					hint={
						say.isPending
							? "The room is answering…"
							: say.isError
								? apiErrorMessage(say.error, "That did not get said")
								: undefined
					}
					onChange={setDraft}
					onSubmit={send}
					placeholder="Say something to the room…"
					value={draft}
				/>
			</div>
		</section>
	);
}

function RoomLine({
	message,
	mine,
	speakingFor,
}: {
	message: RoomMessage;
	mine: boolean;
	/** The colleague whose own agent said this, when one did. */
	speakingFor?: { away: boolean; person: RoomSeat };
}) {
	return (
		<li
			className="teamspace-room-line"
			data-kind={message.author_kind}
			data-mine={mine}
		>
			<div className="teamspace-room-line-who">
				{message.author_kind === "agent" ? <Bot aria-hidden="true" /> : null}
				<strong>@{message.author_handle}</strong>
				{message.author_kind === "agent" ? <span>agent</span> : null}
				{speakingFor ? (
					<em>
						{speakingFor.away ? "standing in for" : "for"} @
						{speakingFor.person.handle}
					</em>
				) : null}
				<time dateTime={message.created_at}>
					{relativeTime(message.created_at)}
				</time>
			</div>
			<p>{message.body}</p>
		</li>
	);
}

/**
 * Whom an agent was speaking for. Somebody's own agent answers beside them
 * when they are here and instead of them when they are not, and a reader who
 * cannot tell which one they are reading has been misled about who said it —
 * the one thing a room full of agents cannot afford. A specialist the team
 * shares speaks for nobody, so it is named plainly and nothing is added.
 */
function speakingFor(
	seats: RoomSeat[],
	message: RoomMessage,
): { away: boolean; person: RoomSeat } | undefined {
	if (message.author_kind !== "agent") return undefined;
	const speaker = seats.find((seat) => seat.handle === message.author_handle);
	if (!speaker?.speaks_for) return undefined;
	const person = seats.find(
		(seat) => seat.kind === "person" && seat.user_id === speaker.speaks_for,
	);
	return person ? { away: !person.present, person } : undefined;
}

function RoomBriefPanel({ brief }: { brief: RoomBrief }) {
	const lists: [string, string[]][] = [
		["Decided", brief.decided ?? []],
		["Still open", brief.open ?? []],
		["Owed", brief.owed ?? []],
	];
	return (
		<div className="teamspace-room-brief">
			<header>
				<strong>Where this has got to</strong>
				<span>
					{brief.summary
						? `through message ${brief.through_seq}`
						: "nothing summarised yet"}
				</span>
			</header>
			{brief.summary ? <p>{brief.summary}</p> : null}
			{lists.map(([heading, values]) =>
				values.length > 0 ? (
					<div key={heading}>
						<span>{heading}</span>
						<ul>
							{values.map((value) => (
								<li key={value}>{value}</li>
							))}
						</ul>
					</div>
				) : null,
			)}
		</div>
	);
}

/** Newest wins, and the room's own sequence decides the order. */
function merge(held: RoomMessage[], arrived: RoomMessage[]): RoomMessage[] {
	const bySeq = new Map(held.map((message) => [message.seq, message]));
	for (const message of arrived) bySeq.set(message.seq, message);
	return [...bySeq.values()].sort((left, right) => left.seq - right.seq);
}

/** Names a seat in the draft without doubling an @mention already there. */
function mention(draft: string, handle: string): string {
	if (draft.toLowerCase().includes(`@${handle.toLowerCase()}`)) return draft;
	return draft.trim() === "" ? `@${handle} ` : `${draft.trimEnd()} @${handle} `;
}

function seatTitle(seat: RoomSeat): string {
	const who =
		seat.kind === "agent"
			? `${seat.agent_name ?? seat.display_name}, ${seat.display_name}'s agent`
			: seat.display_name;
	const doing = seat.role ? ` — ${seat.role}` : "";
	if (seat.kind === "agent") return `${who}${doing}`;
	return `${who}${doing} — ${seat.present ? "here now" : "away"}`;
}
