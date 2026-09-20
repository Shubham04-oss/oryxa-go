# Organization directory

Use the directory to identify who or what owns work before proposing a
handoff. `directory_load` returns the complete organization roster and the
team tree visible to the member. Prefer the narrower find tools when you have
a name, email, role, team, agent name, or purpose.

- People are canonical by `user_id`; names and email are labels.
- Agents are canonical by `id`; their display names may change.
- Team and member `path` values locate them in the organization tree.
- A person's `agents` lists their primary agent first, followed by specialists.
- Empty teams on a person means no team visible to this member, not proof that
  they belong to no team.

These tools only discover. They do not contact a person, team, or agent and do
not grant permission. Put a proposed contact in the structured `handoffs`
result until the communication layer is available.
