-- A teamspace's checklist: the tasks its team keeps, ordered by position and
-- then by when they were added. A task belongs to its teamspace and goes with
-- it. Its owner, when it has one, is a member of the team; somebody leaving
-- the team leaves their tasks unowned rather than taking them away.

CREATE TABLE IF NOT EXISTS agents_primary.teamspace_tasks (
    id text PRIMARY KEY,
    teamspace_id text NOT NULL REFERENCES agents_primary.teamspaces(id) ON DELETE CASCADE,
    organization_id text NOT NULL,
    title text NOT NULL,
    owner_user_id text,
    done boolean NOT NULL DEFAULT false,
    position integer NOT NULL CHECK (position >= 0),
    due_date date,
    created_by_user_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- Set when the task was ticked, and cleared when it is unticked.
    done_at timestamptz
);

CREATE INDEX IF NOT EXISTS teamspace_tasks_space_idx
    ON agents_primary.teamspace_tasks (teamspace_id, position, created_at, id);

CREATE INDEX IF NOT EXISTS teamspace_tasks_owner_idx
    ON agents_primary.teamspace_tasks (organization_id, owner_user_id);
