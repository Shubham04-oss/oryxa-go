-- The organization agent's first default text promised delegation it has never
-- had (docs/design/live-layer/06-agent-isolation.md, L8). Rewrite only the
-- agents still carrying that untouched default; an admin's own text stays.

UPDATE agents_primary.organization_agents
SET description = CASE
        WHEN description = 'Coordinates shared organizational context and synthesizes work published by team members and their agents.'
        THEN 'Summarizes the work each team publishes in its teamspace, for that team.'
        ELSE description END,
    instructions = 'Keep each teamspace''s synthesis current from the entries its team publishes: summarize, record decisions, risks and open questions, and propose follow-ups for people to pick up. You have no tools and cannot assign or start work. Preserve attribution, and never assume a member''s identity, connections, or private context.'
WHERE instructions = 'Maintain organization-wide context from shared teamspace entries. Delegate bounded work to member agents, preserve attribution, and never assume a member''s identity, connections, or private context.';
