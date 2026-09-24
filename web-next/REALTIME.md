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
| `TeamspaceRoomAgent` | `org~teamspace` | the teamspace's team | `…/teamspaces/{id}/messages` (read and post), `/catchup`, as each member | Go room canonical; DO keeps the recent window, fan-out, live presence |
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

## Teamspace rooms

`TeamspaceRoomAgent` (`src/teamspace-room-agent.ts`) is a teamspace's room,
one object per teamspace, shared by everyone on its team who has the room
open; the page's side is `useTeamspaceRoom`, whose `{ messages, send,
presence, status }` is all a page sees. Go keeps the room: who may read it,
what was said, and who is present. The object never decides any of that; it
reads and posts as members, with each member's own cookie (kept in their
socket's attachment, so hibernation keeps it), and fans out what Go
answers. What travels on the socket is in `src/lib/rooms/room-agent.ts`.

- **Reading.** A read is `GET …/messages?after=` as one member. Go returns
  the same messages to every member (`Read` filters nobody), so what one
  member's read returns goes to every socket, each message once, by `seq`.
  The object keeps the latest 200 messages and the highest `seq` read
  through in its storage (SQLite-backed KV), and a socket that opens is
  sent that window once the room has been read. A room with nothing held
  starts from `catchup`, which says where the room ends.
- **Posting.** A post (`room-post`, with the page's own id) goes to Go as
  the socket's member: the author is whoever that cookie is, whatever the
  page sends. `POST …/messages` answers only once the room's agents have (a
  real-model post took 96 s), while Go records the member's message and each
  reply as they happen. So while a post is out the object reads every
  second until the member's message is recorded, then every couple of
  seconds, and each reply reaches every socket as Go records it. The
  answer is merged by `seq` and sent to all of the author's sockets
  (`room-posted`); a socket that reopens is told in its snapshot which of
  its member's posts are still out and what became of those that finished
  in the last two minutes, so a page never waits on an answer it missed.
  The page keeps its "Sending…" until a read shows the message, as before.
- **Scheduling.** Reads are woken by the SDK's schedule (an alarm), never a
  timer, so the object hibernates between them. While a post is out, and
  for two minutes after one, the room is read every couple of seconds;
  otherwise every 12 seconds while at least one tab is visible; not at all
  once none is. Only one tick is set at a time, never less than a second
  after the last. The SDK keeps a schedule's time in whole seconds, rounded
  down, so a tick can run up to a second early; a read due by then is read,
  or the tick would set itself again for the same second and spin.
- **Presence.** Go counts a member present for 90 s after their last read,
  catch-up or post (`Watch`, `presentWithin`), and a present member's agent
  does not answer over them. So the object reads Go as each member with at
  least one visible tab at least every 30 s, and never as a member whose
  tabs are all hidden or closed: the page says `room-visibility` when its
  socket opens and whenever its tab is hidden or shown, and a socket is
  treated as hidden until it says. A member coming back is read at once.
  A hidden tab that opens is sent what the room holds without a read, or
  waits for somebody who is looking. Who has the room open and visible
  right now is the synced state's `here` (by user id, learned from
  `GET /v1/auth/me` with the socket's cookie), beside Go's `seats`; the page
  counts a person present when either says so, so arrival shows at once and
  leaving shows when Go's window lapses, which is also when their agent may
  answer for them.
- **Refusals**, per member: a 401 on a member's read or post tells that
  cookie's sockets `session_expired`, and the object never uses the cookie
  again (the page refreshes the session and reopens its socket; a post
  refused that way goes again once). A 403 or 404 on a read, or a 404 on a
  post, tells the member's sockets `gone` and closes them (code 4404, which
  the SDK does not reopen); a 403 on a post is a member without a seat, not
  gone. Go answers 404 to everyone once a teamspace is deleted, so the other
  members here are asked for the teamspace itself (which marks nobody
  present); once none of them may open it, the object clears what it
  holds. A paused room (400) tells every socket `paused`, and a server
  without rooms (503 `rooms_unavailable`) `unavailable`; either stops the
  reads until a member's read succeeds again, which happens when somebody
  comes back to the room. Any other failure tells the sockets `error` once,
  and the reads go on.
- **Letting go.** The object is a cache of Go's room: ten minutes after the
  last socket closes, it clears what it holds, and the next socket reads
  the room afresh. Go deletes a room with its teamspace and tells nobody,
  so this is also how a deleted teamspace's messages leave the object.
- **A socket that will not open.** A browser is never told why an upgrade
  was refused, so when the socket closes without the page closing it, the
  page asks the Worker (`checkTeamspaceRoom`: a plain GET of the object,
  which makes the socket's own check with Go and, when it lets the member
  in, reaches the object's 204): 401 ends the session's room, 403 or 404 is
  gone, and anything else leaves the socket to keep trying. There is no
  second transport behind the socket: a page never reads or posts to Go's
  room itself.

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
| `TeamspaceRoomAgent` | `GET …/teamspaces/{id}` with `is_member`: Go shows a teamspace to a manager who is not on its team too, and would let them read the room and count as present in it, but the room is the team's (no DELETE: Go deletes the room with its teamspace) |
| side conversation | the teamspace check, the caller is one of the two, the other is a member of it |
| DM | the caller is one of the two (`GET /v1/auth/me`), both are members of the organization |

## Lifecycle

- An agent run interrupted by a Durable Object restart is never replayed;
  the next connect reads what Go stored. A room post cut off the same way
  is not sent again either: the page learns from its reopened socket that
  the room does not know it, and keeps the draft unless the message landed.
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
