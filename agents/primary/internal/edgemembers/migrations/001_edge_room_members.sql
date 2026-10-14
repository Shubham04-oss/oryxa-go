-- Who agents has made a member of which room on the oryxa-edge Worker
-- (live-layer A18). Go is the source of truth for room membership and pushes
-- it to the edge; this table is what it pushed, so that when a member leaves
-- a team or the organization, every room it let them into is told.
--
-- A row is one member of one room. room_id is the edge's id: a thread's
-- session, a teamspace's id, or a direct message's or side conversation's
-- id built from its two members (contracts/edge/v1 DirectRoomID,
-- SideRoomID). teamspace_id is set for a teamspace's room and its side
-- conversations, so leaving the teamspace's team finds them all.
CREATE SCHEMA IF NOT EXISTS agents_primary;

CREATE TABLE IF NOT EXISTS agents_primary.edge_room_members (
    organization_id TEXT NOT NULL,
    room_kind TEXT NOT NULL CHECK (room_kind IN ('thread', 'teamspace', 'dm', 'side')),
    room_id TEXT NOT NULL,
    member_id TEXT NOT NULL,
    teamspace_id TEXT NOT NULL DEFAULT '',
    admitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, room_kind, room_id, member_id)
);

CREATE INDEX IF NOT EXISTS edge_room_members_member_idx
    ON agents_primary.edge_room_members (organization_id, member_id);
