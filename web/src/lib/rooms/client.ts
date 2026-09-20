import { queryOptions } from "@tanstack/react-query";
import { apiPath, apiRequest } from "../api/client";

/** A seat in a room is either a person on the team or somebody's agent. */
export type SeatKind = "agent" | "person";

/** One participant, as the room shows them to everybody else. */
export interface RoomSeat {
	agent_id?: string;
	agent_name?: string;
	display_name: string;
	/** How the seat is addressed in a message: `@ada`. */
	handle: string;
	kind: SeatKind;
	/** Whether the person is reading the room now. Agents are always here. */
	present: boolean;
	/** What this seat is here for, in its owner's words. Routing reads it. */
	role?: string;
	/**
	 * For an agent seat, the person it is the agent of — set only for
	 * somebody's own agent, never for a specialist the team shares.
	 */
	speaks_for?: string;
	user_id: string;
}

/** One thing said in a room, by a person or by an agent speaking for one. */
export interface RoomMessage {
	/** The handles this message named, its author excluded. */
	addressed_to?: string[];
	author_agent_id?: string;
	author_handle: string;
	author_kind: SeatKind;
	author_user_id: string;
	body: string;
	created_at: string;
	id: string;
	in_reply_to_seq?: number;
	room_id: string;
	seq: number;
}

/** What the room amounts to so far: what somebody who was away is handed. */
export interface RoomBrief {
	decided: string[];
	open: string[];
	owed: string[];
	revision: number;
	room_id: string;
	summary: string;
	through_seq: number;
	updated_at: string;
}

export interface RoomRead {
	messages: RoomMessage[];
	seats: RoomSeat[];
}

export interface RoomCatchup {
	brief: RoomBrief;
	messages: RoomMessage[];
}

function roomPath(organizationId: string, teamspaceId: string): string {
	return apiPath`/v1/organizations/${organizationId}/teamspaces/${teamspaceId}`;
}

export async function readRoom(
	organizationId: string,
	teamspaceId: string,
	limit = 200,
): Promise<RoomRead> {
	const query = new URLSearchParams({ limit: String(limit) });
	const read = await apiRequest<RoomRead>(
		`${roomPath(organizationId, teamspaceId)}/messages?${query}`,
	);
	return { messages: read.messages ?? [], seats: read.seats ?? [] };
}

export async function sayInRoom(
	organizationId: string,
	teamspaceId: string,
	body: string,
): Promise<RoomMessage[]> {
	const said = await apiRequest<{ messages: RoomMessage[] }>(
		`${roomPath(organizationId, teamspaceId)}/messages`,
		{ body: { body } },
	);
	return said.messages ?? [];
}

export async function catchUpOnRoom(
	organizationId: string,
	teamspaceId: string,
	afterSeq: number,
): Promise<RoomCatchup> {
	const query = new URLSearchParams({ after: String(afterSeq) });
	const caught = await apiRequest<RoomCatchup>(
		`${roomPath(organizationId, teamspaceId)}/catchup?${query}`,
	);
	return { brief: caught.brief, messages: caught.messages ?? [] };
}

export function roomQueryKey(organizationId: string, teamspaceId: string) {
	return ["teamspaces", organizationId, "room", teamspaceId] as const;
}

/**
 * Reading the room is also how the room learns you are in it, so an open room
 * keeps reading: presence lapses after a minute and a half of silence, and a
 * member the room thinks has walked away gets answered by their own agent.
 */
export function roomQueryOptions(organizationId: string, teamspaceId: string) {
	return queryOptions({
		queryFn: () => readRoom(organizationId, teamspaceId),
		queryKey: roomQueryKey(organizationId, teamspaceId),
		refetchInterval: 5_000,
		refetchIntervalInBackground: false,
	});
}
