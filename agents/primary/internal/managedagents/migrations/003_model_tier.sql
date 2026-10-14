-- The model tier a specialist answers on (I9): modelrouter.Tier. NULL is the
-- role's default, lite, which every existing specialist keeps until its owner
-- changes it. A primary has none: it always answers on main.
ALTER TABLE agents_primary.member_agents ADD COLUMN IF NOT EXISTS model_tier text
    CHECK (model_tier IN ('lite', 'main'));
