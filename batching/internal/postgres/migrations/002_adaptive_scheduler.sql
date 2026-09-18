-- Adaptive scheduling keeps the rate estimate payload-free and fixes each
-- event's delivery promises at receipt time. Historical traffic can release
-- an event sooner, but it cannot extend these persisted deadlines.

ALTER TABLE batching_triggers
    ADD COLUMN IF NOT EXISTS target_size integer,
    ADD COLUMN IF NOT EXISTS pressure double precision,
    ADD COLUMN IF NOT EXISTS soft_due_at timestamptz,
    ADD COLUMN IF NOT EXISTS hard_due_at timestamptz,
    ADD COLUMN IF NOT EXISTS policy_version text;

UPDATE batching_triggers
SET target_size = COALESCE(target_size, 5),
    pressure = COALESCE(pressure, 1),
    hard_due_at = COALESCE(hard_due_at, recorded_at + interval '2 minutes'),
    soft_due_at = COALESCE(soft_due_at, hard_due_at, recorded_at + interval '2 minutes'),
    policy_version = COALESCE(policy_version, 'fixed-window.v1')
WHERE target_size IS NULL OR pressure IS NULL OR soft_due_at IS NULL
   OR hard_due_at IS NULL OR policy_version IS NULL;

ALTER TABLE batching_triggers
    ALTER COLUMN target_size SET NOT NULL,
    ALTER COLUMN pressure SET NOT NULL,
    ALTER COLUMN soft_due_at SET NOT NULL,
    ALTER COLUMN hard_due_at SET NOT NULL,
    ALTER COLUMN policy_version SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'batching_triggers_target_size_check') THEN
        ALTER TABLE batching_triggers ADD CONSTRAINT batching_triggers_target_size_check CHECK (target_size > 0);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'batching_triggers_pressure_check') THEN
        ALTER TABLE batching_triggers ADD CONSTRAINT batching_triggers_pressure_check CHECK (pressure >= 1);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'batching_triggers_deadline_order_check') THEN
        ALTER TABLE batching_triggers ADD CONSTRAINT batching_triggers_deadline_order_check CHECK (soft_due_at <= hard_due_at);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS batching_triggers_due_idx
    ON batching_triggers (soft_due_at, organization_id, member_id)
    WHERE batched_at IS NULL AND lane = 'digest';

CREATE TABLE IF NOT EXISTS batching_traffic_profiles (
    organization_id text NOT NULL,
    member_id text NOT NULL,
    fast_mass double precision NOT NULL CHECK (fast_mass >= 0),
    slow_mass double precision NOT NULL CHECK (slow_mass >= 0),
    observed_at timestamptz NOT NULL,
    last_event_at timestamptz NOT NULL,
    total_events bigint NOT NULL CHECK (total_events >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, member_id)
);
