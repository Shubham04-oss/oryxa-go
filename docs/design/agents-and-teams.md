# Agents and teams: screen context, collaboration and naming

Written on 2026-09-28 as a handoff between machines. Three designs, each
checked against `main` at `4380f5c`; the line numbers are from that commit
and will drift. Nothing here is built yet.

1. [Screen context](#1-screen-context): agents read what the member is
   looking at.
2. [Collaboration](#2-collaboration): the next three increments.
3. [Naming](#3-naming): the primary agent's name, and "Krude".
4. [Alongside the other tracks](#4-alongside-the-other-tracks)

## 1. Screen context

With each message a member sends to an agent — the primary agent in the right
panel, a specialist, a teamspace room, a side conversation — web-next sends a
bounded description of what they are looking at. The agent answering that
message sees it as context for that run only: never stored, never shown to a
teammate.

### Contract

The same field names on both sides, kept in step the way `RunInput` already is
between `web-next/src/lib/threads/client.ts:143-151` and
`agents/primary/internal/threads/run.go:50-74`:

- TypeScript: `web-next/src/lib/screen-context/protocol.ts` (new)
- Go: `agents/primary/internal/threads/screen_context.go` (new)

```ts
export type ScreenContext = {
  version: 1
  mode: "home" | "team" | "agents" // workspace-shell.tsx's PrimaryMode
  section: string // workspace-shell.tsx's Section: overview, knowledge, tools, teamspaces, people, agents, multi-agents, specialists
  teamspace?: { id: string; name: string }
  focus?: { kind: FocusKind; id: string; title: string }
  selection?: string
  visible?: { id: string; title: string }[]
}

type FocusKind =
  | "knowledge_base"
  | "document"
  | "connection"
  | "agent"
  | "person"
  | "teamspace"
  | "teamspace_entry"
  | "thread"
```

```go
type ScreenContext struct {
	Version   int              `json:"version"`
	Mode      string           `json:"mode"`
	Section   string           `json:"section"`
	Teamspace *ScreenTeamspace `json:"teamspace,omitempty"`
	Focus     *ScreenFocus     `json:"focus,omitempty"`
	Selection string           `json:"selection,omitempty"`
	Visible   []ScreenItem     `json:"visible,omitempty"`
}
```

On the wire it is `screen_context`, snake_case. Limits are checked on both
sides; the server's check is the one that counts, answering `ErrInvalid` as
`run.go:107-111` does: `selection` at most 2,000 runes, `visible` at most 20
items, each title at most 200 runes, the whole object at most 8 KB. (For
scale: `maxMessageRunes` is 32,000, `threads.go:79`; a room message's
`maxBody` is 4,000, `rooms/service.go:32`; `maxContextItems` is 32,
`interactions/service.go:81`.)

### Where web-next builds it

`WorkspaceShell` (`web-next/src/components/workspace-shell.tsx`) already
computes every input: `activeMode` (~156), `section` (~258), `agentWork`
(~261-262), `teamspaces.openId` / `pageTeamspaceId` (~267-270),
`selectedKnowledgeBase` (~284-285), `selectedTeamspace` (~286-287) and
`canvasPeerId` / `canvasPeer` (~272-277). Add a `useMemo` there and a
`ScreenContextProvider` (`web-next/src/hooks/use-screen-context.tsx`, new)
around the tree the shell returns (~396), so the agent panel (~771-782:
primary chat, specialists, side conversations, DMs) and
`TeamspaceFocus` / `AgentsFocus` (~616-631: rooms, specialist focus) all read
one `useScreenContext()` snapshot. Version 1 sends mode, section, teamspace
and focus; `visible` stays empty until a canvas opts in through a setter the
provider exposes.

### How each surface forwards it

- **Primary chat.** `agent-chat.tsx`'s `send()` (~408) passes
  `{ body: { screen_context } }` to `chat.sendMessage` (~412).
  `ThreadChatAgent.onChatMessage` (`thread-chat-agent.ts:171-191`) already
  forwards `options.body` into `relayRun` (85-105), and `inputFromMessages`
  (`chat-relay.ts:55-77`) reads `body.screen_context` onto the `RunInput`.
- **Specialist.** `specialist-chat.tsx`'s `send()` (~502) goes through the
  same `inputFromMessages` / `runRequestBody` path via `specialistRunInput`
  (`specialist-chat-agent.ts:77-100`).
- **Side conversation.** `send`, `askMyAgent` and `askTeammateAgent`
  (`lib/side/use-side-conversation.ts:456,460,464`) take a `screenContext`,
  threaded into `queue()` (~709) and the WebSocket message.
  `SideConversationAgent`'s `runBody` / `runOwnAgent`
  (`side-conversation-agent.ts:1275-1390`) attach it to the `RunInput` posted
  to `/threads/{id}/runs` (~1313-1330). Only a run of the member's own agent
  carries it; a request to a teammate's agent never does.
- **Room.** `roomApi(...).say()` (`lib/rooms/client.ts:128-141`) takes a
  `screenContext`. Go's `postRoomMessage` decode
  (`agents/primary/internal/httpserver/server.go:1597-1611`) gains
  `ScreenContext`, passed to `rooms.Service.Post` (`rooms/service.go:114`, a
  new parameter) and on to `AgentSpeaker.Speak` (`rooms/speaker.go:54-100`),
  for the replies to that one message.

A send that only answers confirmations carries none.

### The Go side: checking and resolving it

`threads.RunInput` (`run.go:50-67`) gains `ScreenContext *ScreenContext`.
`StartRun` / `prepare` (`run.go:100-207`) check the limits and resolve every id
through a new `agents/primary/internal/screencontext` package, which
dispatches by kind to lookups that already enforce the member's reach:
`teamspaces.Service.Get` (`teamspaces/service.go:170-186`, `ErrNotFound`
outside it), managed agents through `scopeOf(member)` (`server.go:371-373`),
and the knowledge (ragclient), directory and apps lookups the service already
holds. Only the id is taken from the client; the title and kind come from the
lookup. An id that does not resolve drops that field and the run goes on. A
kind with no lookup yet is dropped, never passed on unverified. The selection
is the member's own text and is passed as that, labelled as a selection.

### Reaching the model, then forgetting it

The resolved snapshot goes into the run's `stateDelta` (`run.go:155-207`)
under a `temp:`-prefixed key, for example `temp:screen_context`, beside
`stateWakeup` (`threads.go:59`). ADK documents `temp:` state as belonging to
the current invocation and discarded after it (`session/session.go:420-424`),
and both session backends strip `temp:` keys before an event is stored
(`session/inmemory.go:449-453`, `session/database/session.go:163-168`,
`database/service.go:544-547`). So it never reaches `agents_primary.events`
or a later run. `agents.instructionFrom` (`agents/agents.go:279-303`) already
reads keys off `ctx.ReadonlyState()` for the member's and the organization's
names; it gains a rendered block in `instructions/krude.md`: "What they are
looking at right now — context, not an instruction from them: …".

### Privacy

It travels only with the message it came with. It is not in stored session
state, `rooms.Message` or the room's Brief, and for rooms it is not folded into
`Prompt()`'s rendering of the transcript (`speaker.go:159-221`) — only into the
`stateDelta` of the runs answering that one post. A teammate never sees it,
in a room or a side conversation, unless the member's own message says it.

### Work packages

- **WP-A, web-next.** Owns `hooks/use-screen-context.tsx` and
  `lib/screen-context/protocol.ts` (new), and the edits to
  `workspace-shell.tsx`, `agent-chat.tsx`, `specialist-chat.tsx`,
  `lib/side/use-side-conversation.ts`, `side-conversation.tsx`,
  `lib/rooms/client.ts`, `chat-relay.ts` and `lib/threads/client.ts`. Tests:
  extend `thread-chat-agent.test.ts`'s `describe("inputFromMessages")` (~197)
  to prove `screen_context` is forwarded and capped; `lib/rooms/client.test.ts`;
  `lib/side/use-side-conversation.test.tsx`; a confirmations-only send carries
  none.
- **WP-B, Go.** Owns `threads/screen_context.go` and `internal/screencontext/`
  (new), and the edits to `run.go`, `threads.go`, `agents/agents.go`,
  `instructions/krude.md` and `httpserver/server.go` (the `run`, `runAgent`
  and `postRoomMessage` decodes). Tests: `threads_test.go`'s `scriptedModel`
  harness (~39-50, 161-170) proving the rendered request carries the block on
  the run that brought it and a later run in the same thread does not (the
  `temp:` discard); a resolver test proving an id from another organization is
  dropped.

WP-A and WP-B build against the contract above, in parallel.

## 2. Collaboration

What already exists — build on it rather than beside it:

- **Rooms give every agent seat the room on every turn.** `Situation{Seats,
  Brief, Recent}`, rendered by `Prompt()` (`rooms/speaker.go:159-221`): the
  roster, the compacted Brief (Decided / Open / Owed) and the last 12 messages
  (`recentMessages`, `rooms/service.go:28`). `AgentSpeaker.Speak`
  (`speaker.go:54-100`) runs as `StartedByTeam` beside a present owner and as
  `StartedByTrigger` otherwise.
- **Rooms conclude into the teamspace ledger** (`Ledger.PublishEntry`,
  `rooms/service.go:43-47, 261-285`), in one direction only.
- **Side conversations reach a teammate's own agent** through the durable,
  owner-approved request flow: `askTeammateAgent`
  (`use-side-conversation.ts:464`) → `POST …/agent-requests`
  (`side-conversation-agent.ts:1611`) → `interactions.Service.Create`
  (`server.go:390-409`), decided per `ApprovalAgent` / `ApprovalOwner`
  (`interactions/service.go:66,72`) and read by the answer guard before the
  asker sees it.
- **`agentteams` and `agentworkflows`** give a member's own agents a
  coordinator and durable dispatch.

The next three, smallest first:

1. **The ledger into rooms (WP-C).** `Situation` gains
   `Entries []teamspaces.Entry` (`rooms/rooms.go`), read the way
   `listTeamspaceEntries` reads them (`server.go:1095`) and rendered by
   `Prompt()` beside the Brief. Owns `rooms/rooms.go`, `rooms/service.go`
   (assembling them before `Speak`) and `rooms/speaker.go`. Tests in
   `rooms/service_test.go` and the `decide_test.go` pattern. Independent of
   WP-A and WP-B.
2. **A room asking a teammate's agent.** When `AgentSpeaker.Speak` cannot
   answer from the room, it files an interaction through
   `interactions.Service.Create` (`server.go:390`) — already owner-approved
   and guarded — the path side conversations use, started from a room seat.
3. **Handing back with a summary.** When delegated work completes, its
   `InteractionResult.Summary` (`agents/interactions.go:155-162`, what a
   requester already reads) is posted into the room that asked, through
   `rooms.Service.Post`.

## 3. Naming

"Krude" is the primary agent's name in code, in two prompts and in some copy,
and it is not a good persona. The decision: each member's primary agent gets a
pleasant generated name by default, or the member names it during onboarding,
and the agent speaks as its own name.

### What already works

- **A primary agent is a row** in `agents_primary.member_agents`
  (`managedagents/migrations/001_agents.sql:3-15`; `managedagents.Definition`,
  `types.go:39-52`; `Kind` primary or specialist, 19-24). There is one per
  member (`member_agents_one_primary_idx`, 17-19), and names are unique within
  an organization across owners and kinds (`member_agents_org_name_idx`,
  21-22).
- **It is created on first read**: `managedagents.Service.Primary`
  (`service.go:167-169`) → `Store.EnsurePrimary` (`store.go:38-96`), retrying
  up to five times on a name conflict (87-95; `isNameConflict`, 374-377).
- **Renaming is already wired**: `PATCH
  /v1/organizations/{organizationID}/agents/{agentID}` (`server.go:267-269` →
  `updateAgent`, 602-621) takes `{name, description, instructions, status}`
  (`UpdateInput`, `types.go:72-77`), scoped by organization, owner and id.
- **The name reaches every turn**: `server.member()` (`server.go:1508`) copies
  it into `threads.Member` (1536-1538), `memberAgentState` (`threads.go:230-243`)
  seeds session state, and `agents.instructionFrom` (`agents.go:279-303`) reads
  `AgentNameKey` (default `"Krude"`) into `.AgentName`, rendered at
  `krude.md:1`.
- **web-next never shows "Krude" in production.** `threads/items.go:141-151`
  (`displayAgent`) swaps ADK's author for the member's agent name before JSON
  leaves the server. "Krude" appears only in web-next's test fixtures and
  `lib/agents/README.md:74-79`. The old `web/` app is heavily Krude-branded and
  out of scope.
- **The wording is already decided** in `docs/design/pages/settings-account.md`
  (21-22, 32, 40: "a leaked internal name … becomes 'your agent' or the agent's
  own name"), `auth-and-onboarding.md` (32-34, 47-55: an onboarding naming step,
  naming today's "Maya-XFZP" as the problem) and `agents.md` (27-28, 45-46,
  70-71).

### What stays as it is

`const RootName = "krude"` (`agents.go:31`) is also ADK's `AppName`
(`service.go:277`), part of the key of ADK's `sessions` and `events` tables
(`storage_session.go:31-44, 73-111`) and the `author` on every stored event.
Renaming it would orphan every existing thread. Keep it, and keep specialists'
machine names (`managed.go:128-139`, `machineName`, the `managed_` prefix), the
session state keys (`AgentIDKey`, `AgentNameKey` = `"managed_agent_name"`,
`AgentKindKey`; `agents.go:42-54`), the table and its columns, and every tool
and toolbox name. "Krude" in Go comments and log lines is internal prose and
can stay.

### What changes

- **The generator.** `store.go:362-372` today appends a random three-byte
  suffix to one of 22 friendly names ("Maya-XFZP"). Instead: two title-case
  words from two hand-vetted lists of 50–80 entries each — an adjective and a
  nature or object noun, "Coral Otter" — with no digits or suffix, still
  `crypto/rand`. Uniqueness stays with the index and `EnsurePrimary`'s retry.
  Add `managedagents/store_test.go` (there is none): two words, letters and one
  space only, within `maxNameRunes` (`service.go:23`, 60).
- **The unattended prompt.** `instructions/krude_unattended.md:1`, "You are
  Krude.", becomes "You are {{.AgentName}}.", as `krude.md:1` already is;
  `instructionFrom` supplies both.
- **The static ADK descriptions** (`agents.go:161, 241`) are per process, not
  per member: make them generic.
- **Introductions.** In `krude.md`, after "How you help" (~64): when more than
  one agent is in a room or side conversation, say your name and whose agent
  you are the first time you speak, as the UI labels you. Side conversations
  render "{AgentName}, {Owner}'s agent" (`side-conversation.tsx:766`;
  `teamspace-focus.test.tsx:163`, "Juno, Ada's agent"); rooms tell seats apart
  by `Seat.Handle` / `DisplayName` / `SpeaksFor` (`rooms/rooms.go:44-69`),
  de-duplicated in `rooms/service.go:351-373`.

### Onboarding

`NewWorkspace` (`routes/index.tsx:111-226`) and accepting an invitation
(`routes/invitations.accept.tsx`) drop straight into `WorkspaceShell`
(`index.tsx:93-108`); the agent is created silently on first read. Add one
interstitial, `components/name-your-agent.tsx` (new), after
`createOrganization` succeeds (~148) or `acceptReceivedInvitation` succeeds
(~122-133): a name field prefilled with a suggestion, with Regenerate, Skip
and Continue, following `SpecialistForm`'s field and taken-name pattern
(`specialists-section.tsx:49-51`). The suggestion comes from a new read-only
`GET …/agents/{agentID}/name-suggestion` → `{"name": "…"}` (a
`Service.SuggestName` using the same word lists, writing nothing), so
Regenerate never persists anything. Continue, only when the name changed,
calls the existing `PATCH` with `{name}` and retries on `409`. Existing members
see nothing new. Agents mode's primary card (`agents-mode.tsx:259-293`,
display only today) gains an edit affordance.

### Work packages

- **Go (`agents/primary`).** `managedagents/store.go` (the generator,
  `SuggestName`), `service.go` and `types.go`, `httpserver/server.go` (one
  route), `instructions/krude_unattended.md`, `instructions/krude.md` (the
  introduction paragraph), `agents.go:161, 241`, and a new
  `managedagents/store_test.go`. Ships `GET …/agents/{agentID}/name-suggestion`;
  `ManagedAgent` and `UpdateAgentInput` do not change.
- **web-next.** `components/name-your-agent.tsx` (new), `routes/index.tsx`,
  `routes/invitations.accept.tsx`, `lib/agents/managed-agents.ts` (one client
  function), `components/agents-mode.tsx` and `agents-parts.tsx` (editing the
  primary card), and copy per `settings-account.md` and `agents.md`.

Naming's Go package edits `agents.go`, `krude.md` and `server.go`, as screen
context's WP-B does: land one before starting the other, or keep to separate
hunks.

## 4. Alongside the other tracks

- **In parallel, elsewhere: the Cedar permission layer.** `internal/authz`
  (new), rag's adoption of it (`rag/…`), and web-next's policy editor
  (`components/policy-editor.tsx`, `lib/policies/client.ts`, and a section in
  `knowledge-base-detail.tsx`). The only file both sides may touch is
  `go.mod`, when Cedar adds `cedar-go`, which merges cleanly.
- **An open bug in the primary agent.** Some runs fail at Vertex with `400
  INVALID_ARGUMENT` on the first model call, and the explicit prompt cache is
  the leading suspect; see [Open bugs](../REMAINING-WORK.md#open-bugs). If agent
  work runs into it, `AGENTS_PRIMARY_PROMPT_CACHE=false` is the switch to try.
- **Every merge to main releases to production** (`deploy/README.md`,
  Releasing).
