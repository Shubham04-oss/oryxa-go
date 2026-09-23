# Realtime: a Durable Object for every point of contact

Every place a member talks to someone (their agent, a specialist, a team, a
teammate) is a durable chat: one Durable Object per conversation, created on
demand by name, holding its transcript in its own SQLite storage and fanning
out live over WebSockets through the web Worker (`src/server.ts`).

Objects are built on the `agents` SDK. `AIChatAgent` serves conversations
with an agent; `Agent` serves rooms of people. `Agent` already carries
PartyServer's room primitives (connections, `broadcast`, hibernation) and
synced state the `useAgent` hook reads, so there is no separate
`partyserver` dependency.

## Who holds the truth

Go stays the source of truth wherever an agent acts: threads, specialist
runs, teamspace rooms and agent requests keep their permissions, audit trail
and data lifecycle there. The Durable Object is the live layer: the visible
transcript, fan-out, presence, and "typing" / "agent is working" signals.
Conversations no agent acts in (DMs, and the human messages of a side
conversation) live only in the Durable Object's SQLite.

| Object | Name | Who connects | Go behind it | Storage |
|---|---|---|---|---|
| `ThreadChatAgent` | `org~thread` | the member | `POST …/threads/{id}/runs` (SSE) | DO transcript; Go thread canonical |
| agent chat | `org~agent~thread` | the member, own specialists only | `POST …/agents/{id}/runs` (SSE) | DO transcript; Go thread canonical |
| teamspace room | `org~teamspace` | the teamspace's members | `…/teamspaces/{id}/messages`, `/catchup` | Go room canonical; DO live + presence |
| side conversation | `org~teamspace~userA~userB` (ids sorted) | those two members | agent requests: create, decide, respond | DO SQLite for human messages; Go for agent work |
| DM | `org~userA~userB` (ids sorted) | those two members | none | DO SQLite only |

Go runs only a member's own agents (`managedagents` scopes by user). A
teammate's agent is reached through the agent-request flow, which honours its
teamspace contribution's `delegation_mode`: `automatic` runs it, `request`
waits for its owner's approval, so nothing reads another member's apps
unapproved.

## Side conversations

On a teamspace page the left panel is the group room. The right panel's
Agents tab also lists the teammates' agents in that teamspace; picking one
opens the side conversation with that teammate: the two people and both
their agents, about work in that teamspace, without pulling in the room.
When either person (or their agent) asks for something the other's agent
must do, the object files an agent request; the other person sees that
their agent is involved, approves it if their contribution asks for that,
and the result lands in the same conversation for both.

## The Worker's part

`src/server.ts` routes `/agents/<object>/<name>` and never trusts the
client: it checks the name's shape, refuses a cross-site WebSocket or
DELETE, requires the session cookie, asks Go whether this member may open
this object, and only then hands the request to the object with identity
headers it sets itself (any the client sent are overwritten).

| Object | Access check against Go |
|---|---|
| `ThreadChatAgent` | `GET …/threads/{thread}` |
| agent chat | `GET …/agents/{agent}` (own agent) |
| teamspace room | `GET …/teamspaces/{id}`, member of it |
| side conversation | the teamspace check, the caller is one of the two, the other is a member of it |
| DM | the caller is one of the two (`GET /v1/auth/me`), both are members of the organization |

## Lifecycle

- An agent run interrupted by a Durable Object restart is never replayed;
  the next connect reads what Go stored.
- A member leaving or an organization being deleted must also empty the DMs
  and side conversations they were in. Go will call a Worker endpoint to
  purge those objects; it is built with the DMs.

## UI

The right agent panel (DESIGN.md §2) is location-aware. Its tabs, Agents,
DMs and Teams, stay the same everywhere. On Home the Agents tab switches
between the member's own agents. On a teamspace page it also offers that
teamspace's teammates' agents, each opening a side conversation.
