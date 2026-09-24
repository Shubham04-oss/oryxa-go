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
| `SpecialistChatAgent` | `org~agent` | the member, own specialists only | `POST …/agents/{id}/runs` (SSE), with the `thread_id` the object keeps | DO transcript of the current conversation; Go threads canonical |
| teamspace room | `org~teamspace` | the teamspace's members | `…/teamspaces/{id}/messages`, `/catchup` | Go room canonical; DO live + presence |
| side conversation | `org~teamspace~userA~userB` (ids sorted) | those two members | agent requests: create, decide, respond | DO SQLite for human messages; Go for agent work |
| DM | `org~userA~userB` (ids sorted) | those two members | none | DO SQLite only |

Go runs only a member's own agents (`managedagents` scopes by user). A
teammate's agent is reached through the agent-request flow, which honours its
teamspace contribution's `delegation_mode`: `automatic` runs it, `request`
waits for its owner's approval, so nothing reads another member's apps
unapproved.

## Specialist chats

`SpecialistChatAgent` (`src/specialist-chat-agent.ts`) is the member's chat
with one of their own specialists: one object per specialist, not per
conversation, because Go offers no specialist conversation before its first
run. No route creates one: `POST …/agents/{agent}/runs` without a
`thread_id` allocates it inside the run (`managedagents.Service.startRun`)
and names it only in the answer's `X-Oryxa-Thread`. A page cannot name an
object after a conversation that exists only once a message has gone
through that object, so the object holds one conversation at a time:

- It learns the conversation from the first run's `X-Oryxa-Thread`, sends
  it as `thread_id` on every later run, and keeps it in its state
  (`{ threadId }`, synced to every socket; a page's `setState` is refused),
  so the page can read Go's copy with `getSpecialistThread`.
- Starting over clears the transcript and forgets the conversation, so the
  next message starts a new one: the page's DELETE (the Worker checks the
  specialist is the member's own, then purges over RPC), or useAgentChat's
  `clearHistory()` over the socket. Go has no route that deletes a
  specialist conversation, so nothing here deletes one: the old
  conversation stays in Go and in `listSpecialistThreads`, readable with
  `getSpecialistThread` but not continued by the object.
- A specialist answers with a JSON work result (`status`, `summary`,
  tools, files, suggested hand-offs), so its deltas are raw JSON. The
  object streams one progress marker per draft instead (a `delta` with no
  text, which Go never sends; the timeline shows it as the specialist
  working) and the finished message readable: `text` is the result's
  summary and `result` the parsed result. It stores neither the markers nor
  the raw JSON. Go's history still holds the raw JSON: a page applies
  `readableSpecialistItems` to it before reconciling with the transcript.
- Its run's input is checked as Go checks it before anything is sent:
  without a `thread_id`, Go allocates the conversation before it reads the
  input, so a message it would refuse would leave an empty conversation
  behind.

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
headers it sets itself (every `X-Oryxa-*` header the client sent is
dropped first).

Each object is one entry in the Worker's `realtimeObjects` table: its
binding (routeAgentRequest serves it at the binding's name in kebab case),
the reads a page may GET beside the socket (`get-messages`), a name parser
that reads the raw, undecoded path segment, the access check against Go
with the member's cookie, the identity headers, and, when the page may
delete, either Go's DELETE (sent first; the transcript is purged only once
Go has deleted) or, when Go has nothing to delete, the purge alone behind
the access check. Go's 401, 403, 404 and 503 pass through with Go's code;
any other failure answers 502, and a Go it cannot reach 503.

| Object | Access check against Go |
|---|---|
| `ThreadChatAgent` | `GET …/threads/{thread}` |
| `SpecialistChatAgent` | `GET …/agents/{agent}`: Go loads only the owner's agents (404 otherwise), and it must be a specialist (the primary agent's chat is its threads) |
| teamspace room | `GET …/teamspaces/{id}`, member of it |
| side conversation | the teamspace check, the caller is one of the two, the other is a member of it |
| DM | the caller is one of the two (`GET /v1/auth/me`), both are members of the organization |

## Lifecycle

- An agent run interrupted by a Durable Object restart is never replayed;
  the next connect reads what Go stored.
- Deleting a thread deletes it in Go, then purges its `ThreadChatAgent`.
  A specialist chat can only start over (Go keeps its conversations).
  Deleting a specialist in Go leaves its chat's transcript behind,
  unreachable (the access check answers 404), so a page clears the chat
  before it deletes the specialist.
- A member leaving or an organization being deleted must also empty the DMs
  and side conversations they were in. Go will call a Worker endpoint to
  purge those objects; it is built with the DMs.

## UI

The right agent panel (DESIGN.md §2) is location-aware. Its tabs, Agents,
DMs and Teams, stay the same everywhere. On Home the Agents tab switches
between the member's own agents. On a teamspace page it also offers that
teamspace's teammates' agents, each opening a side conversation.
