import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { MessagesSquare, Sparkles } from "lucide-react";
import { type UIEvent, useEffect, useRef, useState } from "react";
import { SpecialistAvatar } from "@/components/agents/specialist-identity";
import { initials } from "@/components/app/identity";
import { ThreadComposer } from "@/components/app/thread-composer";
import { MessageMarkdown } from "@/components/threads/message-markdown";
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
	active: boolean;
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
	active,
	organizationId,
	teamspaceId,
	userId,
}: TeamspaceRoomProps) {
	const queryClient = useQueryClient();
	const room = useQuery({
		...roomQueryOptions(organizationId, teamspaceId),
		enabled: active,
	});
	const [draft, setDraft] = useState("");
	const [brief, setBrief] = useState<RoomBrief | undefined>(undefined);
	const transcript = useRef<HTMLOListElement>(null);
	const composer = useRef<HTMLTextAreaElement>(null);
	// Follows the conversation while the reader is at the end of it, and stops
	// the moment they scroll back to read something — being yanked away from
	// what you are reading is worse than missing the newest line.
	const [following, setFollowing] = useState(true);
	const messages = room.data?.messages ?? [];
	const seats = room.data?.seats ?? [];
	const peopleHere = seats.filter(
		(seat) => seat.kind === "person" && seat.present,
	).length;
	const agentCount = seats.filter((seat) => seat.kind === "agent").length;
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
					messages: mergeRoomMessages(previous?.messages ?? [], said),
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
		onSuccess: (caught) => {
			setBrief(caught.brief);
			queryClient.setQueryData<RoomRead>(
				roomQueryKey(organizationId, teamspaceId),
				(previous) => ({
					messages: mergeRoomMessages(
						previous?.messages ?? [],
						caught.messages,
					),
					seats: previous?.seats ?? [],
				}),
			);
		},
	});

	function follow(event: UIEvent<HTMLOListElement>) {
		const list = event.currentTarget;
		setFollowing(list.scrollHeight - list.scrollTop - list.clientHeight < 48);
	}

	function send(body: string) {
		if (!active || say.isPending || !body.trim()) return;
		setDraft("");
		say.mutate(body.trim(), { onError: () => setDraft(body) });
	}

	return (
		<section
			aria-labelledby="room-heading"
			className="teamspace-room"
			data-active={active}
		>
			<header>
				<div>
					<span className="teamspace-room-kicker">Group conversation</span>
					<h3 id="room-heading">The room</h3>
					<p>
						People and agents work together here. Mention a teammate to route a
						request; otherwise the room decides who should answer.
					</p>
				</div>
				<div className="teamspace-room-header-actions">
					<span className="teamspace-room-live">
						<span aria-hidden="true" />
						{peopleHere} here · {agentCount} agents
					</span>
					<Button
						disabled={catchUp.isPending || !active}
						onClick={() => catchUp.mutate()}
						size="sm"
						variant="ghost"
					>
						<Sparkles aria-hidden="true" />
						{catchUp.isPending ? "Catching up…" : "Catch me up"}
					</Button>
				</div>
			</header>

			{seats.length > 0 ? (
				<div className="teamspace-room-seats">
					{seats.map((seat) => (
						<button
							aria-label={`Mention @${seat.handle}, ${seat.display_name}${seat.role ? `, ${seat.role}` : ""}`}
							className="teamspace-room-seat"
							data-kind={seat.kind}
							data-present={seat.present}
							disabled={!active}
							key={seat.handle}
							onClick={() => {
								setDraft((value) => mentionRoomSeat(value, seat.handle));
								composer.current?.focus();
							}}
							title={seatTitle(seat)}
							type="button"
						>
							<RoomAvatar seat={seat} />
							<span>
								<strong>{seat.display_name}</strong>
								<small>@{seat.handle}</small>
							</span>
							{seat.kind === "person" && seat.present ? (
								<span
									aria-hidden="true"
									className="teamspace-room-seat-presence"
								/>
							) : null}
						</button>
					))}
				</div>
			) : null}

			{brief ? <RoomBriefPanel brief={brief} /> : null}
			{catchUp.isError ? (
				<p className="teamspace-section-error">
					{apiErrorMessage(catchUp.error, "Could not catch up on this room")}
				</p>
			) : null}

			{!active ? (
				<div className="teamspace-room-paused">
					<strong>This room is paused</strong>
					<span>Resume the Teamspace to read or send new messages.</span>
				</div>
			) : null}
			{active && room.isError ? (
				<p className="teamspace-section-error">
					{apiErrorMessage(room.error, "Could not load this conversation")}
				</p>
			) : null}
			{active && room.isPending ? (
				<p className="teamspace-section-empty">Opening the room…</p>
			) : null}
			{active && !room.isPending && !room.isError && messages.length === 0 ? (
				<div className="teamspace-ledger-empty">
					<MessagesSquare aria-hidden="true" />
					<div>
						<strong>Nobody has said anything yet</strong>
						<span>
							Start with a question, decision, or update. The right teammate can
							answer in this shared chat.
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
							seat={seats.find((seat) => seat.handle === message.author_handle)}
							mine={
								message.author_kind === "person" &&
								message.author_user_id === userId
							}
							speakingFor={speakingFor(seats, message)}
						/>
					))}
				</ol>
			) : null}

			{say.isPending ? (
				<div aria-live="polite" className="teamspace-room-thinking">
					<Sparkles aria-hidden="true" />
					<span>
						<strong>The room is working together…</strong>
						<small>Your message and any agent replies will appear here.</small>
					</span>
				</div>
			) : null}

			<div className="teamspace-room-composer">
				<ThreadComposer
					disabled={say.isPending || !active}
					label="Say something to the room"
					inputRef={composer}
					hint={
						!active
							? "This Teamspace is paused. Resume it to send messages."
							: say.isPending
								? "The room is answering…"
								: say.isError
									? apiErrorMessage(say.error, "That did not get said")
									: undefined
					}
					onChange={setDraft}
					onSubmit={send}
					placeholder="Message the room or @mention a teammate…"
					value={draft}
				/>
			</div>
		</section>
	);
}

function RoomLine({
	message,
	mine,
	seat,
	speakingFor,
}: {
	message: RoomMessage;
	mine: boolean;
	seat?: RoomSeat;
	/** The colleague whose own agent said this, when one did. */
	speakingFor?: { away: boolean; person: RoomSeat };
}) {
	return (
		<li
			className="teamspace-room-line"
			data-kind={message.author_kind}
			data-mine={mine}
		>
			<span className="teamspace-room-line-avatar">
				{seat ? (
					<RoomAvatar seat={seat} />
				) : message.author_kind === "agent" ? (
					// No seat means no agent id to go on; the handle is the only
					// stable thing left, and it is what RoomAvatar falls back to too.
					<SpecialistAvatar
						name={message.author_handle}
						seed={message.author_handle}
						size="small"
					/>
				) : (
					<span aria-hidden="true">{initials(message.author_handle)}</span>
				)}
			</span>
			<div className="teamspace-room-line-main">
				<div className="teamspace-room-line-who">
					<strong>{seat?.display_name ?? `@${message.author_handle}`}</strong>
					<small>@{message.author_handle}</small>
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
				{message.author_kind === "agent" ? (
					<MessageMarkdown text={message.body} />
				) : (
					<p>{message.body}</p>
				)}
			</div>
		</li>
	);
}

function RoomAvatar({ seat }: { seat: RoomSeat }) {
	return seat.kind === "agent" ? (
		<SpecialistAvatar
			name={seat.agent_name ?? seat.display_name}
			seed={seat.agent_id ?? seat.handle}
			size="small"
		/>
	) : (
		<span aria-hidden="true" className="teamspace-person-avatar">
			{initials(seat.display_name)}
		</span>
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
export function mergeRoomMessages(
	held: RoomMessage[],
	arrived: RoomMessage[],
): RoomMessage[] {
	const bySeq = new Map(held.map((message) => [message.seq, message]));
	for (const message of arrived) bySeq.set(message.seq, message);
	return [...bySeq.values()].sort((left, right) => left.seq - right.seq);
}

/** Names a seat in the draft without doubling an @mention already there. */
export function mentionRoomSeat(draft: string, handle: string): string {
	if (draft.toLowerCase().includes(`@${handle.toLowerCase()}`)) return draft;
	return draft.trim() === "" ? `@${handle} ` : `${draft.trimEnd()} @${handle} `;
}

function seatTitle(seat: RoomSeat): string {
	const who =
		seat.kind === "agent"
			? `${seat.agent_name ?? seat.display_name}${seat.speaks_for ? ", primary agent" : ", shared specialist"}`
			: seat.display_name;
	const doing = seat.role ? ` — ${seat.role}` : "";
	if (seat.kind === "agent") return `${who}${doing}`;
	return `${who}${doing} — ${seat.present ? "here now" : "away"}`;
}
