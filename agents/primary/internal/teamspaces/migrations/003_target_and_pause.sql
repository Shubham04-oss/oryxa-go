-- What a team is working towards, and who paused its teamspace. All four are
-- nullable and added only, so a release that does not know them reads and
-- writes teamspaces as before.
--
-- target_date is the day the team is aiming at, and target_label what it is
-- ("Launch"); a manager sets or clears either. paused_by_user_id and paused_at
-- are set when a teamspace's status becomes disabled and cleared when it
-- becomes active again. A teamspace disabled before this migration has
-- neither: nobody recorded who paused it.

ALTER TABLE agents_primary.teamspaces
    ADD COLUMN IF NOT EXISTS target_date date,
    ADD COLUMN IF NOT EXISTS target_label text,
    ADD COLUMN IF NOT EXISTS paused_by_user_id text,
    ADD COLUMN IF NOT EXISTS paused_at timestamptz;
