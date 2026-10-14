-- What a specialist may reach in its owner's apps (I3): runprofile.Grant as
-- JSON. NULL is the default grant, every app read-only, which is what each
-- existing specialist keeps until its owner changes it. A primary has none.
ALTER TABLE agents_primary.member_agents ADD COLUMN IF NOT EXISTS app_grant jsonb;
