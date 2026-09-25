# Agents primary

Primary is the agentic system in `/agents`: the prompts, tools and
configuration behind every agent interaction. adk-go is its engine and Gemini
its model. The web Worker's SQLite-backed `ThreadChatAgent` talks to it over HTTP,
and the supporting services in `secondary` sit beside it.

Status: the engine is in, and members reach it. adk-go v2.4.0 runs Krude, a
Gemini assistant with dedicated sub-agents, dynamic sub-agents, temporary
helpers and toolboxes that load on demand with their own skills; the AgentCore
sandbox and browser are wired today. A model router sends planning and bulk
work to the lite model, a prompt cache layer holds large stable prefixes on
Vertex, and the events outbox, publisher and dispatcher are in with contracts
in `contracts/events/agents/v1`. The service, mounted in the single `oryxa`
binary as `agents`, serves the run API: a member's threads with Krude, each
run streamed as server-sent events with the member's own token on it (see
[Run API](#run-api)). `cmd/devui` still opens the same Krude in ADK's
development web UI. Members also have a persistent, renameable primary-agent
identity and can create persistent specialist agents that run through the same
member-scoped tool, confirmation, artifact, memory and usage pipeline.

## Agents

`internal/agents` defines Krude and everything it works with. `Root`
assembles it:

```text
krude              chat: the member's assistant
├── planner        single-turn sub-agent: turns multi-step work into a plan
├── researcher     single-turn sub-agent: answers from the live web with Google Search and URL context
├── toolboxes      list_tools, load_tools: agent system, sandbox, browser, apps, knowledge, directory, interactions and teamspaces, loaded on demand
├── subagents      create_subagent, list_subagents, remove_subagent: specialists Krude writes and reuses in a session
├── helpers        list_helpers, spawn_helper: temporary agents for big or separate work
└── memory         load_memory: what Krude learned about the member in earlier threads, where a memory bank is configured
```

**Sub-agents.** Krude calls a sub-agent the way it calls a tool, and the result
comes back to it. Single-turn sub-agents run without talking to the member,
and the ones Krude calls in the same model turn run concurrently.
A sub-agent sees only the request Krude hands it, and cannot transfer the
conversation to another agent.

**Native tools.** The researcher's search and link reading are not function
tools: Google Search grounding and URL context are Gemini built-ins, so ADK
puts them on the model request and Gemini invokes them server-side, returning a
grounded answer. Nothing executes in the primary service, and because the
built-ins do not mix with function tools they live on an agent of their own,
with transfers off.

**Toolboxes.** A `Toolbox` is a named set of tools, such as AgentCore's
sandbox or browser. `list_tools` shows them and `load_tools` loads the ones a
task needs into the session, and their tools are usable from Krude's very next
step. Every toolbox carries its skill: how its tools are meant to be used.
While the toolbox is loaded, that skill is part of Krude's system prompt, so a
loaded tool arrives with its instructions and Krude reads them again instead of
guessing. The loader is a native ADK toolset: `Tools` selects the session's
loaded toolboxes, and the toolset's request preprocessing picks up one loaded
midway through an invocation. Every tool in a toolbox must be a declarative ADK
tool named after it, as in `sandbox_execute_code`, or the toolbox will not
load. `loaded_tools` holds the session's loaded toolboxes.

Sandbox supplies native ADK function tools for Python/JavaScript/TypeScript,
shell commands and temporary file list/read/write/remove operations. Execution
happens in Bedrock AgentCore Code Interpreter, never in the primary container.
One isolated AgentCore session is reused by one ADK conversation so its
language context and files survive across calls; different conversations are
isolated. Remove and reset require ADK human confirmation, and active sessions
are stopped during graceful shutdown with AWS's TTL as fallback.

Browser supplies navigation, bounded semantic DOM snapshots, selector clicks,
field fills, screenshots and reset through Bedrock AgentCore Browser. It signs
the Browser automation WebSocket with the deployment's AWS credentials and
talks CDP directly to the remote page; no browser executable or browser state
lives in primary. Browser sessions are isolated by ADK conversation and stopped
on reset, expiry replacement or graceful shutdown. Click, fill and reset always
use ADK confirmation. Web content is treated as untrusted data, secret values
are forbidden in model-driven fill calls, and common password/OTP/payment/file
fields are rejected again inside the remote page. Screenshots become bounded
ADK PNG artifacts instead of base64 model output, kept wherever the artifact
service keeps them (see [Artifacts](#artifacts)).

**Dynamic sub-agents.** `create_subagent` registers a specialist for the rest
of the conversation: Krude writes its name, a one-line description and its
instruction, and from the very next step it can call it by name with a complete
task. `list_subagents` shows them and `remove_subagent` drops one. Each call
runs through ADK's agent tool in its own runner and throwaway session, so a
sub-agent sees only the task, never the conversation, and has no tools; like
helpers, it returns what it prepared for Krude to confirm. The toolset packs
each registered sub-agent into the model request in its request preprocessing,
which ADK runs before every model step, so creating or dropping one takes
effect on the next step rather than the next invocation. Names are lowercase,
at most 30 characters, cannot be reserved names, and cannot collide with a
toolbox tool; a session holds at most 12.

**Helpers.** `list_helpers` describes the dedicated sub-agents. `spawn_helper`
builds a temporary single-turn agent from an instruction and a complete task,
runs it, and returns its result; helpers started in the same turn run
concurrently. No helper can spawn another. A helper cannot reach the member,
so its instruction forbids sending, posting or changing anything: it returns
what it prepared for Krude to confirm.

**Session state.** Krude is assigned to one member per session, and its
instruction reads these values:

| Key | Set by | Meaning | When missing |
| --- | --- | --- | --- |
| `user_name` | Session runtime from auth | The member Krude assists | "the member you are talking with" |
| `user_about` | Session runtime from auth | A few words about the member, from their role | Left out |
| `user_email` | Session runtime from auth | The member's sign-in address | Left out |
| `user_role` | Session runtime from auth | The member's role in the organization | Left out |
| `org_name` | Session runtime from auth | The member's organization | "their organization" |
| `organization_id` | Session runtime from auth | The member's organization ID, for tools that need it | Left out |
| `loaded_tools` | `load_tools` | The toolboxes loaded in this session | Left out |
| `subagents` | `create_subagent`, `remove_subagent` | The dynamic sub-agents of this session | Left out |

The session runtime seeds a member's state on session creation, reading the
member's name and first organization from the auth tables over the same shared
PostgreSQL pool the ADK session service uses. A user who belongs to no
organization, such as the development UI's `user`, keeps the state the caller
sent. Callers can always override any seeded key by setting it themselves.

Every value is collapsed to one line of at most 60 characters before it
reaches the prompt, because members write them.

Each agent's instruction is a Markdown file in `internal/agents/instructions/`,
embedded at build time. Krude's, `krude.md`, is a Go `text/template` rendered
on every turn. The sub-agents' are ADK templates: `{key}` is replaced with that
session state value, and a turn fails when the key is missing unless it is
written `{key?}`. A dynamic sub-agent uses an instruction provider instead, so
braces in the instruction Krude wrote are read as written. ADK adds an identity
line of its own to what the model sees.

To add a dedicated sub-agent, give it a constructor beside the others with a
name, the description Krude picks it by, a mode, an instruction and transfers
switched off, add it to `Root`, and name it in `instructions/krude.md`.

## Acting for a member

An agent acts **as the member it works for, with that member's own
identity** — never as a service impersonating them. `internal/memberauth`
carries that identity from a session to every call a member-authenticated
tool makes.

**Member present.** A chat turn forwards the member's own access token —
the same bearer token auth issues today — onto the run's context with
`memberauth.WithAccessToken`; it is never written to session state or a log.

**Member absent.** A trigger or a schedule runs with nobody watching, so
there is no token to forward. The member has instead, once, granted "agents
may act for me"; `memberauth.TokenSource` exchanges that standing grant for a
short-lived access token by calling auth's `POST /v1/internal/agent-tokens`,
signed with a `serviceauth.Signer` naming agents as the caller. `AuthClient`
implements it, caching each user's token until two minutes before it expires
and safe for concurrent runs asking for the same user at once. It maps
auth's `404` and `409` — no grant, or Cognito refused the stored refresh
token and auth deleted it — to `ErrGrantRequired`, a typed error a tool
result can turn into words the model says to the member, not a crash; `5xx`
and an unreachable auth map to a retryable `ErrUnavailable`.

Either way, the acting member — the session's own user, in the organization
its `organization_id` state names — has to reach the credential that signs
an outgoing MCP request, and a credential provider only ever sees that
request's plain context, never ADK's session state. `memberauth.WrapToolset`
closes that gap: it wraps an MCP toolset so both its tool listing and every
tool's `Run` attach the member — and any access token already on the
context — to the context the call actually uses, right before the call is
made. `CredentialProvider.Credential` reads it back off that context and
sets `Authorization: Bearer <token>` and `X-Oryxa-Organization:
<organization>` on the request: the member's own token when the run carries
one, otherwise an agent token for the session's user.

**Apps toolbox.** `agents.AppsToolbox` builds the one toolbox, named `apps`,
that reaches every app a member has connected through Composio: email,
calendars, CRMs and the rest. Its tools are the apps service's stateless MCP
endpoint at `POST {APPS_URL}/v1/mcp/apps`, wrapped as above, and its skill is
`internal/agents/toolboxes/apps.md`, embedded the way sandbox's and browser's
are. `apps_connections` lists what the member has connected, `apps_search_tools`
finds a toolkit's tools, and `apps_run_read_tool`/`apps_run_write_tool` call
one. A member has one current connection per app, shared by their primary and
all persistent specialists because every run carries that same member
identity; agents neither select account IDs nor own credentials. Unlike the
archived integrations toolboxes, there is no provider
catalogue: a member's connected apps and their tools are discovered live,
through these tools, rather than fetched once at startup, and building the
toolbox makes no network call of its own — the MCP session, and the first
`tools/list`, happen lazily on first use.

Every tool but its three reads — `apps_connections`, `apps_search_tools` and
`apps_run_read_tool` — requires confirmation before it runs, the same
Human-in-the-Loop flow toolbox tools already use, so a write, or a tool the
apps service adds later that this toolbox does not yet know about, always
asks first. The apps service serves every member the same tools, so the
toolbox lists them once, as whichever member first loads it; after that a
loaded toolbox costs no call per model step, and only tool calls go out, each
as its own member. One MCP client serves every member: each request carries
the credential of the run that made it, never another member's. When acting
for the member fails — no grant, no member, auth not answering — the model
gets the reason in plain words, without the request that carried it, and a
loaded toolbox whose tools cannot be listed is left out of the turn rather
than failing it, with `load_tools` saying why.

The service and `cmd/devui` build the apps toolbox when `APPS_URL` is set
(in the binary it defaults to this process when it runs apps), signing agent
token requests with a KMS `serviceauth.Signer` naming agents as the caller
(`AUTH_URL` and `AGENTS_PRIMARY_SERVICE_KEY_ID`); when either is missing it
logs a warning, since a run without the member's own token could then not act
for them. An error building the toolbox is logged, and Krude runs without it
rather than failing to start.

The integrations toolboxes that predated this, one per provider from the now
archived integrations service, were archived on 2026-09-17 with the
connections and integrations services; the code is at tag
`archive/connections-and-integrations-2026-09-17`.

**Member present, in practice.** The run API puts the caller's own access
token — the bearer token, or the web app's `oryxa_access` cookie, which holds
the same token — on every run with `memberauth.WithAccessToken`, so a
member's own turn needs no grant. A development UI run still acts through the
member's standing grant, since ADK's launcher has no member token to forward.

**Member absent.** A wake-up runs with no token of the member's, so every
tool that acts for them goes through `memberauth.TokenSource` and the grant
they gave (see [Wake-ups](#wake-ups)). That grant has never been exchanged
against a provisioned Cognito agent client.

### Knowledge toolbox

`agents.KnowledgeToolbox` builds the `knowledge` toolbox, which searches the
organization's own documents in rag — what the member and their organization
indexed, including what knowledge extracts from their connected apps — so
Krude answers from them and cites them. Its skill is
`internal/agents/toolboxes/knowledge.md`. It has two search tools, and a
read-only indexing-status tool when `KNOWLEDGE_URL` is configured:

- `knowledge_collections` lists the corpora rag lets the member read by
  default: their own and their organization's.
- `knowledge_search` searches up to eight of them at once — the ones named,
  or else those holding documents, fullest first — and merges their best
  passages by score. Each passage carries its document's title, its source
  (a link when the document has one), the section it sits in, and at most
  1,500 characters of text; collections that could not be read come back
  under `unsearched` with the reason.
- `knowledge_sources` lists the sources the member is allowed to inspect,
  optionally narrowed by provider or connection. It reports each registered
  target, status and last completed extraction so Krude can distinguish a
  stale or absent index from evidence that nothing happened. It does not
  register sources or trigger indexing; only knowledge's authorized
  member/connection lifecycle does that.
- `knowledge_sync_source` is available in attended runs only and asks for
  confirmation before moving an existing source's next sweep forward. It
  cannot register arbitrary targets and does not claim ingestion finished.

Both call rag's API (`internal/ragclient`) as the member, with
`memberauth.Bearer`: the member's own token when the run carries it,
otherwise an agent token under their grant, so rag decides what they may
read exactly as it does for them. The service and `cmd/devui` build it when
`RAG_URL` is set, which in the binary defaults to this process when it runs
rag. A team's corpus is searched only when named.

For work about a recent event or an object's full history, Krude uses the
indexed passages for background and reads the pointed-at object through the
apps toolbox for current state. The index is not a live event log: supported
connections register at most ten targets automatically, scheduled sweeps and
trigger-driven sweeps may lag, and other toolkits remain live-read only.

### Organization directory toolbox

When `TEAMS_URL` is set, the service adds a read-only `directory` toolbox to
both primary and specialist agents. Its skill lives at
`internal/agents/toolboxes/directory.md`, and exposes four member-scoped tools:

- `directory_load` returns the current organization roster, the team tree the
  caller may see, visible team memberships, and each person's active agents.
- `directory_find_people` searches by display name, email, or user id.
- `directory_find_teams` searches caller-visible teams by name.
- `directory_find_agents` searches active agents by name, purpose, or owner.

Teams remains the authority for current organization membership and team
visibility. The agents service first asks Teams as the acting member, then
joins only safe agent handles (`id`, owner identity, kind, display name, description)
onto the people Teams returned. Agent instructions and disabled agents never
enter the directory. This ordering also prevents a stale agent record from
revealing a former or otherwise hidden organization member.

The primary agent is listed before a person's specialists. IDs are canonical;
display names may be renamed. Directory reads do not ping or contact anyone,
emit a team-call event, grant permission, or execute a handoff. They identify
the intended person, team, or agent for the communication protocol built on
top of this layer.

### Agent interactions toolbox

When the organization directory is configured, primary and specialist agents
also receive the `interactions` toolbox. It implements the controlled path
from discovery to collaboration without handing one agent a direct runtime
reference to another:

```text
discover target → create request → target decides → target responds → requester observes result
```

An interaction is a persisted JSON contract containing the requester and
target's stable user/agent identities, purpose, bounded task, labelled context,
requested tools and permissions, expiry, decision, and final result. The
requester identity comes from authenticated session state; the model cannot
claim to be another agent. Requests must target an active agent belonging to a
different current member returned by Teams.

The toolbox exposes `interactions_create`, `interactions_inbox`,
`interactions_sent`, `interactions_get`, `interactions_decide`, and
`interactions_respond`. A specialist sees only requests it sent or received;
the member's primary agent may mediate every request belonging to that member.
Approval can only narrow the requested tools and permissions, and a response
cannot report using a tool outside the approved set. These grants describe the
interaction envelope; they never bypass a tool's existing authorization or
human-confirmation requirements, and they are not enforced on the run that
does the approved work either (see below).

**Requests inside a teamspace, and who approves them.** A request may name the
teamspace it belongs to (`teamspace_id`), which is how a teamspace side
conversation asks a teammate's agent for something. The ledger then asks the
teamspace, against the live Teams directory, where the target sits
(`teamspaces.Service.RequestSeat`): the requester must be on the teamspace's
team now, the space and the organization agent must be active, and the target
must be a teammate's primary agent — their whisperer seat — or an active
specialist a current teammate contributed there. Anything else is refused
before anything is recorded (see [Agent request API](#agent-request-api)).

Every request carries `approval`, set when it is made. The requester only
chooses whether to ask inside a teamspace, and in which; who approves then
follows the target's own standing there, and nothing the requester sends
sets it:

| The target | `approval` | Who decides |
| --- | --- | --- |
| Any agent, in a request made outside a teamspace | `agent` | The addressed agent, or its member's primary agent mediating, as before teamspaces |
| A specialist contributed to the teamspace with `automatic` delegation | `agent` | The same |
| A teammate's primary agent | `owner` | Only that member, in their own session |
| A specialist contributed with `request` delegation | `owner` | Only that member, in their own session |

The owner's approval therefore guards the requests made in a teamspace, and
only those. A request made outside any teamspace follows the protocol from
before teamspaces, so the addressed agent decides it even when its owner
holds that agent's requests in a teamspace: a requester who leaves
`teamspace_id` out, or asks in a teamspace where that specialist has
`automatic` delegation, reaches the agent's own decision. Whether requests
made outside a teamspace should be held for their owner too is a policy
decision that is still open. A specialist contributed to two teamspaces with
different modes is approved as the teamspace the request names has it, for a
requester on that teamspace's team.

A request held for its owner is decided only through
`POST .../agent-requests/{request}/decision`, the member's own signed-in call
(`interactions.Service.DecideAsOwner`). What proves it is the member is the
actor's session: `agents.InteractionActor.Session` is `"member"` only on the
API's member path, which refuses agent tokens, and `"agent"` on every actor
the tools build from a run's session state, even in a chat turn the member is
watching. `DecideAsOwner` requires the member's session and `Decide` refuses
it. Agents' tools see `agents.InteractionService`, which has no owner path,
so `interactions_decide` answers `ErrForbidden`, telling the agent its member
decides. The member may also decide a request their agent could. The held
request's `requested` delivery wakes nobody; the member's approval arrives as
the ordinary `approved` delivery, which wakes the addressed agent with the
read-only apps toolbox and a prompt saying its owner approved the request
themself, so it does the approved work and answers with `interactions_respond`
rather than deciding again. Rejection and expiry work as for any request. The
check fails closed: any approval but `agent` holds a request (one this code
does not know included), and so does a request made in a teamspace that names
none, which the ledger also refuses to store.

Every decision records who made it, as `decision.decided_by`:
`{"kind": "member", "user_id": ...}` for the member in their own session,
`{"kind": "agent", "user_id": ..., "agent_id": ...}` for an agent in a run,
and `{"kind": "system"}` when Oryxa closes a request (below). A decision an
older build recorded has no `decided_by`. The waker tells an agent its owner
approved in person only when `decided_by` names that member, never inferred
from `approval`.

**A request is read again before anything acts on it.** Who approves is set
when the request is made, but the teamspace can change after that. So
whenever a request made in a teamspace is acted on, its standing there is read
again, live and as the addressed member sees the organization
(`teamspaces.Service.RequestStanding`): when an agent decides it, when its
member approves it, and before the waker wakes an agent for its `requested` or
`approved` delivery.

- If its owner has since taken deciding back (switched the specialist from
  `automatic` to `request`, say), the request is held for them. A pending
  request is marked `approval: "owner"`. An agent's approval whose work has
  not started is withdrawn, so the request is pending and held again. Either
  is recorded as a new `requested` event carrying `approval: "owner"`, whose
  delivery wakes nobody. The agent is refused and not woken. Switching a mode
  the other way does not release requests already held.
- If it no longer stands (the teamspace is gone or disabled, the member who
  asked left its team, or either agent lost its seat), Oryxa closes it with
  that reason. A pending request is `rejected`, with `decided_by` `system` and
  the reason as the decision's message. An approved one is `failed` before any
  work, with the reason as the result's summary. Each is the ordinary event,
  so the member who asked is told. Deciding it answers `409 request_closed`.
- The member's own approval works whatever standing they gave their agent, as
  long as the space and both seats remain. Rejecting needs nothing to remain.
- Reading the teamspace needs a token of the addressed member's: their own,
  in their session or a chat turn they watch, and otherwise an agent token
  minted under their standing grant (they allowed agents to act for them
  while they are away). When there is none (no grant, or no token exchange:
  `AUTH_URL` and `AGENTS_PRIMARY_SERVICE_KEY_ID` both unset), the request
  cannot be read again, so no agent may decide it. It is held for the member,
  who decides it in their own session, where it is read with their own
  token. An approval already made is still worked: it was read live when it
  was made, and without a token the woken run cannot reach the member's apps
  or knowledge either. When auth or the directory does not answer, nothing
  changes and the delivery is retried.

**Nobody is told a request is held for them yet.** Its `requested` delivery
wakes nobody, nothing notifies the member, and the web app has no Requests
screen: the member finds it only by listing their requests. A request that
expires records no event, so nothing tells the member who asked that a held
request ran out unanswered (reads show it `expired`). Both are follow-ups that
need a Requests screen.

**What a delivered request may reach.** The member whose agent is woken is not
there, so the run answers on the reduced agent (see [Wake-ups](#wake-ups)) and
what it may reach follows what their own agent has already allowed:

- **Deciding** on a new request, and **reporting** a finished one back to its
  own member, run without the apps toolbox at all. Nothing another member
  wrote can read this member's connected apps before their agent has approved
  it. A request held for its owner is never delivered for deciding at all.
- **Doing approved work** runs with the read-only apps toolbox (the same one
  a wake-up gets) and never the writing one. The run is not narrowed to the
  decision's `allowed_tools`: it has the whole read-only toolbox and is only
  told to keep to what was allowed. The ledger checks only the response's own
  account of it, that `tools_used` is within `allowed_tools`.
- No unwatched run may start an interaction of its own: `interactions_create`
  is left out, so a request cannot fan out into requests on other members.
- A delivery is about its one request. Its run gets an interactions toolbox
  of its own (`managedagents.DeliveryReads` and `DeliveryQuiet`, the reach
  above) with `interactions_get`, `interactions_decide` and
  `interactions_respond` only: `interactions_inbox` and `interactions_sent`
  are not there, so the model cannot browse the member's other requests
  whatever it makes of the request. Nothing a delivery does needs them, since
  its message names the request. App wake-ups and a room's stand-in keep the
  toolbox they had.

Each create, decision, and response is committed with an EventBridge outbox
event. The event contains routing identities and lifecycle state, not the task,
context, decision text, or result. Agents' EventBridge rule feeds those events
back through its SQS queue into `internal/interactiondelivery`: requested work
wakes the target, approval wakes it again when a result is still needed, and a
rejection or final result wakes the original requester. The request path never
invokes another user's agent inline. Retries from the same model tool call are
idempotent, and requests expire after one day by default (five minutes to seven
days when explicitly chosen).

The delivery row stores only the event's routing projection. A woken agent
loads the authoritative record through `interactions_get`, treats all of its
cross-user content as untrusted data, and records decisions and results with
the strict tools. Its run starts with the interactions toolbox already loaded
(`threads.RunInput.Toolboxes`), its skill in the instruction, so the first
model call goes straight to `interactions_get` rather than to `list_tools` and
`load_tools`; the run's other toolboxes stay loadable as ever. A model answer by itself does not acknowledge target work:
the ledger must actually leave `pending` or `approved`. Every recipient uses
one stable conversation per interaction, recorded earlier revisions settle
before later ones while newer authoritative state supersedes stale work, and
repeat events deduplicate by `event_id`. Runs carry no member access token, do
not read or write long-term memory, and remain subject to the normal tool
authorization and confirmation boundaries.

## Run API

The `agents` service serves a member's conversations with Krude, called
threads, under their organization. A thread is an ADK session whose user is
the member and whose state names the organization it was started in, so a
member reaches only their own threads, and only there. Every route asks auth
who the caller is, forwarding their bearer token or `oryxa_access` cookie
(`internal/authclient`); a cookie can change nothing unless the request comes
from an origin in `AGENTS_ALLOWED_ORIGINS`, and an agent's own token is
refused: a thread is the member's conversation, and an agent reaches Krude
from inside a run.

| Route | What it does |
| --- | --- |
| `GET /v1/organizations/{org}/threads` | The member's chat threads here, the most recently active first: `{"threads": [...]}`. `?origin=side_conversation` lists their side conversations' threads instead, and `?origin=all` every thread; `?origin=chat` is the default. A blank or unknown `origin` is a `400 invalid_request` |
| `POST /v1/organizations/{org}/threads` | Starts a thread; `201` with it. The body is optional: `{"origin": "side_conversation"}` starts one for a side conversation, while no body, `{}`, or `"origin": "chat"` (or `""`) starts a chat. Any other origin is a `400 invalid_request`. Its state is seeded from the member's auth record in this organization |
| `GET /v1/organizations/{org}/threads/{thread}` | The thread with its history as items, and `pending_confirmations` |
| `DELETE /v1/organizations/{org}/threads/{thread}` | Removes the thread, and the artifacts its runs kept that are not the member's own `user:` ones |
| `POST /v1/organizations/{org}/threads/{thread}/runs` | Runs Krude on the thread and streams the run |
| `GET /v1/organizations/{org}/threads/{thread}/artifacts/{name}` | A file the thread's runs kept, its latest version or `?version=` |
| `GET /v1/organizations/{org}/preferences` | What the member chose about Krude here: `{"app_updates": true}` until they turn [wake-ups](#wake-ups) off |
| `PUT /v1/organizations/{org}/preferences` | Records the member's own choice, `{"app_updates": false}`; an agent's token is refused like everywhere else |

Every thread says where it came from, as `origin` in its JSON (in the
listing, on creation, and in a thread's detail): `"chat"` is the member's own
conversation with Krude, and `"side_conversation"` a thread the web app's
side conversation opened to run the member's own agent on its internal
prompt. The origin is written to the thread's session state when it is
created, beside the organization it was started in, and nothing changes it
afterwards. Threads Oryxa opens for the member itself, such as wake-up
reports, are chats, and so is a thread with no origin in its state: every
thread from before origins. Only the listing tells them apart: reading,
running, deleting or downloading from a thread by its id works the same for
either origin.

### Persistent agent API

The registry creates one primary definition lazily for each member in each
organization. Its friendly generated name is display identity only; the
stable `agt_...` id remains the agent's machine identity, so the member can
rename it without losing conversations. Existing threads refresh that name on
their next run, and streamed/history items show it instead of the machine
name. A member can add specialist agents
with their own purpose and operating instructions. Every registry query is
scoped by both organization and owner.

| Route | What it does |
| --- | --- |
| `GET /v1/organizations/{org}/agents` | Ensures the member's primary definition exists, then lists their primary and specialists |
| `POST /v1/organizations/{org}/agents` | Creates a specialist from `name`, optional `description`, and required `instructions` |
| `GET /v1/organizations/{org}/agents/{agent}` | Reads one of the member's definitions |
| `PATCH /v1/organizations/{org}/agents/{agent}` | Renames it, changes description/instructions, or sets `status` to `active` or `disabled` |
| `DELETE /v1/organizations/{org}/agents/{agent}` | Deletes a specialist. The primary identity cannot be deleted |
| `GET /v1/organizations/{org}/agents/{agent}/threads` | Lists the member's conversations with that specialist |
| `GET /v1/organizations/{org}/agents/{agent}/threads/{thread}` | Reads one specialist conversation and its pending confirmations |
| `POST /v1/organizations/{org}/agents/{agent}/runs` | Starts or continues that agent and streams the run |

The web specialist creation dialog starts a design conversation with Krude.
Before proposing an agent, Krude lists the member's existing specialists,
checks their connected apps, and uses relevant indexed workspace context. It
asks a small set of domain-specific questions about the outcome, audience,
workflow, inputs, tools, outputs, cadence, boundaries, and success criteria.
It can recommend relevant unconnected tools, but labels them as recommendations
and never claims access or connects them itself. Once the important choices
are resolved, Krude presents a working checklist and complete operating model,
then calls `agent_system_create_agent`. The member reviews the exact tool
arguments and confirms before the specialist is persisted. The conversation
remains available in the member's primary threads.

An agent run accepts the same `message` or `confirmations` fields as a primary
thread run, plus optional `thread_id`. Omit it on the first turn; the response
returns the allocated conversation in `X-Oryxa-Thread`, which later turns and
confirmation answers send back. `X-Oryxa-Run` still identifies the individual
run. Specialists use an ADK output schema and finish with a JSON work result:
`status`, `summary`, `tools_used`, `artifacts`, and proposed `handoffs`.
In standalone specialist runs, handoffs remain output suggestions until an
agent deliberately turns one into an `interactions_create` request. The target
still decides what it accepts and what it returns. A member-initiated team
workroom (below) can route a handoff to another member-owned teammate without
using the cross-user interaction API.

### Single-user multi-agent teams

A member can compose persistent specialists into reusable teams. The member's
primary agent is always the sole coordinator; specialists are active workers
with explicit roles, stable identities, their own durable conversations, and
the same member-scoped tools and confirmation boundaries as their standalone
runs. Teams never copy or broaden an agent's instructions.

The `agent_system` toolbox makes the primary agent the conversational control
plane. It can list the member's definitions, help draft a precise specialist,
and create, update, or delete agents and teams only after ADK human
confirmation. Its delegation tool accepts one bounded assignment per selected
team member, starts independent specialists concurrently, and returns each
agent's status, strict JSON output, durable thread/run IDs, and pending
confirmation IDs. A trigger-started run cannot delegate a team, so untrusted
app content cannot fan work out autonomously. Specialists may inspect team
context but cannot read every agent's private operating instructions, mutate
the registry, or coordinate a team.

| Route | What it does |
| --- | --- |
| `GET /v1/organizations/{org}/agent-teams` | Lists the member's persistent teams, coordinator, members, roles and status |
| `POST /v1/organizations/{org}/agent-teams` | Composes active, same-member specialists into a team |
| `GET /v1/organizations/{org}/agent-teams/{team}` | Reads one team inside the member boundary |
| `PATCH /v1/organizations/{org}/agent-teams/{team}` | Changes its name, description, status, or complete ordered membership |
| `DELETE /v1/organizations/{org}/agent-teams/{team}` | Deletes the team but leaves its agents intact |
| `GET /v1/organizations/{org}/agent-teams/{team}/messages` | Reads the latest 200 group-chat lines, or lines after `after_seq` for incremental catch-up |
| `POST /v1/organizations/{org}/agent-teams/{team}/messages` | Posts `{ "body": "..." }` as the member and returns their line plus any agent replies |
| `GET /v1/organizations/{org}/agent-teams/{team}/agent-threads` | Returns this team's agent-to-thread pointers for opening the exact teammate conversation and reviewing pending confirmations |

The workroom is distinct from the human Teamspaces room. An unaddressed member
message goes to the primary coordinator; `@primary` or a specialist's displayed
`@handle` directs it to that agent. A coordinator's explicit handoff line, or
a specialist's structured handoff to a teammate on the live roster, appears in
the transcript and wakes that teammate. Relay is limited to four agent turns
per member message and 90 seconds; unknown or outside-team targets never run.
When the coordinator requests a teammate's contribution, it takes one final
turn to synthesize what was actually said instead of presenting a handoff as a
finished answer.
Each agent keeps a separate durable thread for this team, and its ordinary
member-scoped tools and confirmation rules still apply. An action awaiting
confirmation is **not** auto-approved in group chat: the member reviews it in
that agent's direct conversation. Deleting a team removes its group-chat
messages and team-thread pointers, but not the agents themselves.

An agent that still belongs to a team cannot be deleted; remove it from every
team first. This preserves an explicit team revision and lifecycle event for
every membership change instead of silently cascading membership away.

### Durable team workflows

A member can save a bounded set of team assignments as a durable workflow,
start it through the API, or attach an EventBridge Scheduler `rate`, `cron`, or
one-time `at` expression with an IANA timezone. Both paths start the same Step
Functions Standard state machine. Its callback task contains only workflow,
execution, organization, and owner IDs; the worker loads the current team and
assignments from PostgreSQL and runs the same specialist delegation pipeline
as the primary agent.

Each execution has a durable ledger row and a lease, so an SQS redelivery does
not run a live execution twice. Step Functions waits on the task token while
the model work runs, and its execution ARN is kept as an external correlation
ID. Assignments, provider credentials, task tokens, tool output, and model
results never enter Step Functions, Scheduler, SQS, or EventBridge lifecycle
events. The result ledger stores the strict per-specialist envelopes, thread
and run IDs, used tools, artifacts, and any pending confirmation IDs.

Scheduled work inherits the workflow owner's identity boundary, not their
live browser token. It may use a standing agent grant, but it cannot manufacture
human approval: a write tool that requires confirmation ends the workflow in
`needs_confirmation`, and the member continues it through the ordinary agent
thread API.

| Route | What it does |
| --- | --- |
| `GET /v1/organizations/{org}/agent-workflows` | Lists the member's workflow definitions and schedules |
| `POST /v1/organizations/{org}/agent-workflows` | Creates a workflow for one active team, with one bounded task per selected active specialist |
| `GET /v1/organizations/{org}/agent-workflows/{workflow}` | Reads one member-owned workflow |
| `PATCH /v1/organizations/{org}/agent-workflows/{workflow}` | Changes its metadata, team, assignments, status, or schedule |
| `DELETE /v1/organizations/{org}/agent-workflows/{workflow}` | Removes its external schedule and durable definition |
| `POST /v1/organizations/{org}/agent-workflows/{workflow}/executions` | Starts the Standard workflow and returns `202` with its execution ledger row |
| `GET /v1/organizations/{org}/agent-workflows/{workflow}/executions` | Lists recent executions for that workflow |
| `GET /v1/organizations/{org}/agent-workflow-executions/{execution}` | Reads one execution, including its final strict result or actionable error |

`AGENTS_WORKFLOW_LOCAL=true` swaps only the orchestration adapters for a
development in-process driver, so API and Gemini tests still exercise the
database, workflow service, team delegation, and result schema. It supports
`at(...)` and `rate(...)`; `cron(...)` is rejected explicitly. The local driver
is not durable across a process restart and is never selected by production.

### Agent request API

The same ledger is available to the member through their primary identity,
and a decision made here is the member's own:

| Route | What it does |
| --- | --- |
| `POST /v1/organizations/{org}/agent-requests` | Creates a request, in a teamspace when the body names `teamspace_id`; accepts `Idempotency-Key` for caller retries (below) |
| `GET /v1/organizations/{org}/agent-requests` | Lists, newest first, the requests the member's agents sent and were sent |
| `GET /v1/organizations/{org}/agent-requests/inbox` | Lists requests addressed to the member's agents |
| `GET /v1/organizations/{org}/agent-requests/sent` | Lists requests sent by the member's agents |
| `GET /v1/organizations/{org}/agent-requests/{request}` | Reads one request visible to the member |
| `POST /v1/organizations/{org}/agent-requests/{request}/decision` | The member approves or rejects, in their own session, with narrowed tools and permissions; the only way a request held for its owner is decided. Approving one made in a teamspace reads it again first, with the member's own token |
| `POST /v1/organizations/{org}/agent-requests/{request}/response` | Completes an approved request with the strict result envelope |

A request's JSON carries `teamspace_id` (absent outside a teamspace) and
`approval` (`agent` or `owner`, see
[Agent interactions toolbox](#agent-interactions-toolbox)) beside its parties,
status, decision and result; a decision carries `decided_by`. All three
listings take `?limit=` and two optional filters: `?teamspace_id=` keeps the
requests made in that teamspace, and `?with_user=` those whose other party is
that member. `GET .../agent-requests?teamspace_id=T&with_user=U` is how a side
conversation reads what two teammates asked each other in teamspace T, in both
directions: either of them gets the same list, polling as themself, and a
listing only ever holds requests its caller is a party to. `with_user` naming
the caller, or either filter sent blank, is `400 invalid_request`: a filter
is left out to list everything, never sent empty. Each direction is read, in
order, from an index of its own and merged to the limit
(`agent_interactions_teamspace_pair_idx` serves the side conversation's
poll).

`Idempotency-Key` names one request of the caller's. Sent again, it is
answered from the ledger before anything else is looked up, so a retry gets
back the request it made, `201` as the first time, even after the seat it was
made from has gone. The same key with another target, teamspace, purpose,
task, context, or requested tools or permissions is
`409 idempotency_key_reused`, and never another request.

Creating a request in a teamspace answers with the teamspace's refusal when
there is one, and records nothing: `404 not_found` for a teamspace the caller
cannot see, `403 forbidden` for a caller who is not on its team,
`409 teamspace_disabled` when the space or the organization agent is
disabled, and `400 invalid_request` for a target with no seat there. A body
naming `approval` is `400 invalid_request`, since nothing the requester sends
chooses it. The asking agent must hold a seat there too (the member's own
primary agent, or a specialist they contributed), or it is `403 forbidden`.
Deciding a request addressed to someone else is `403 forbidden` (one the
caller is not a party to is `404 not_found`), one past its expiry
`409 request_expired`, one already decided `409 invalid_state`, and one that
no longer stands in its teamspace `409 request_closed`, with the reason:
Oryxa has closed it.

### A teamspace is a room

A teamspace is read and written as a conversation: the people on the team and
their agents talk in one place, the way a chat room works, rather than
publishing finished work at each other. `POST .../teamspaces/{id}/messages`
says something and answers with everything the room added — the member's own
message, then whatever agents answered it. `GET .../messages?after=` reads
from where you got to, and `GET .../catchup?after=` is what somebody who has
been away is handed.

**Who answers is decided in this order.**

1. **Addressing, which is a rule.** A body naming `@handle` addresses that
   seat. An agent so addressed answers. A *person* so addressed answers for
   themselves when they are reading the room, so nothing runs — an agent
   talking over the person it works for is worse than silence. A person who
   is away has their own agent answer in their place, because standing in for
   them is what it is for. Never the author, by either route.
2. **Judgment, when nobody was named.** One question per message decides
   whether anybody should answer at all and, if so, which seat — the agent
   whose declared role fits what was asked. Somebody asking for a pricing
   sheet reaches the agent of whoever handles pricing, without naming them.
3. **Nothing.** Most messages in a room are people talking, and the room is
   quiet by default rather than eager.

Presence is reading: a member who fetches messages is here for a minute and a
half, which is how the room knows whether an agent is speaking beside its
person or standing in for them. That distinction also decides what the turn
may reach — a stand-in runs unwatched, on the reduced agent, with no memory
(see [Wake-ups](#wake-ups)).

**The brief is how a room stays affordable.** Nobody re-reads a whole room:
an agent taking a turn is given the brief plus the last dozen messages, and
so is the judgment. The brief is rewritten — consolidated, never appended —
when the same judgment says the room has moved past it, with a message-count
backstop so a room that never triggers it still gets one. It is also exactly
what a person who joins late is handed, which is why it reads as prose rather
than as minutes.

**What a room decides reaches the organization.** A message the judgment
calls a decision, a commitment or a fact is published to this teamspace's
ledger under the member's own agent, and from there the organization agent
reads it like any other publication. A room contributes to organization
context by concluding something, not by anybody remembering to publish.

**Bounds.** One posted message causes at most four agent turns, two
consecutive passes end the relay, and the whole thing is bounded in
wall-clock time. An agent with nothing to add says so and is not recorded.

**Every message a room stores is announced**, a person's or an agent's
answer, as `agents.teamspace.room.message.posted.v1`, recorded in the agents
outbox in the transaction that stores the message: the event commits with the
message, and never without it. It carries the organization, the
`teamspace_id` (a room's id is its teamspace's), the message's `id` and `seq`,
and `in_reply_to_seq` for an agent woken by one message in particular; its
`author`, `member` or `agent`, with the member's `user_id` and, for an agent,
its `agent_id` (the member it belongs to is the `user_id`); the
`addressed_handles` it named, for telling someone they were mentioned, left
out when it named none; and `posted_at`. Never the body, nor any summary of
it.

`addressed_handles` are the handles of the room's seats the message named
with `@handle`, written as the room makes them, without the `@` (`["ada"]`),
and never its author's own. Only seats that were in the room count, as when
the room works out who answers: an `@` before anything else, such as an email
address's domain, names nobody, so nothing of what was said reaches the bus
as a handle. The message is stored addressing the same seats, as
`addressed_to`, whoever wrote it.

The events can reach the bus in any order (see [Events](#events)), so a
consumer puts a room's messages in order by `seq`.

### Multi-user, multi-agent teamspaces

A teamspace is the shared-work boundary for one human team from Teams. It is
not owned by the person who creates it: the current team remains its authority,
and every read or mutation resolves the caller against Teams' live directory.
There is one stable `oag_...` organization agent for the whole organization,
not one coordinator per teamspace. Organization owners and admins configure
that agent; team admins configure their spaces.

Every team member's active primary agent remains part of the collaboration in
`whisperer` mode. The teamspace projects its safe handle from the live
directory, but never its instructions, conversations, connections, or
credentials. A whisperer follows the relevant teamspace context, privately
advises its own member, can use that member's specialists for bounded work,
and publishes only deliberate, attributed output into shared space. The
organization agent consumes shared output and maintains the broad organization
context; it does not replace or impersonate members' primary agents.

The on-demand `teamspaces` toolbox makes that participation executable rather
than merely descriptive. `teamspaces_list` shows only spaces where the current
persistent agent is a whisperer or explicit specialist contribution,
`teamspaces_entries` reads the recent attributed ledger, and
`teamspaces_publish` writes as the current session agent without accepting an
author id from the model. Specialists lose this view immediately when their
contribution is withdrawn; primary participation follows live Teams
membership.

Contributing a specialist never transfers its identity, instructions, app
connections, or tool permissions. The specialist still runs as its owner.
`delegation_mode` is the owner's standing answer to whether teammates may ask
it for work here without them, and the request ledger enforces it for every
cross-user request made in the teamspace (`teamspace_id`): a `request`
specialist's requests are held for its owner, who approves or rejects each in
their own session before the specialist is woken, while an `automatic`
specialist decides them itself, as any agent does outside a teamspace. A
request made in the teamspace to a teammate's primary agent is always held for
its owner. The mode is read again whenever a request is decided or its agent
woken, so switching to `request` holds requests the specialist has not
decided, or has approved but not started. A request made outside any
teamspace is not governed by it at all (see
[Agent interactions toolbox](#agent-interactions-toolbox)). Existing per-tool
authorization and human-confirmation requirements remain in force in both
modes. Another member, including a team admin, cannot increase that standing
authority; they can only remove the contribution. The organization agent
records follow-ups as `proposed` only; it does not yet execute them, even when
a contribution has automatic delegation authority. Execution is a separate
authority boundary.

Organization-agent and teamspace lifecycle events contain only routing
identity, status, revision, and contributor user ids. Prompts, whisperer
context, roles, connection data, and model output remain out of EventBridge. A
`teams.team.deleted.v1` event removes the corresponding space through the
agents queue, while a departing member's private contributions are removed
before their agent registry is cleaned up. The whisperer roster needs no
cleanup because it is derived from Teams on every read.

Shared work is an append-only publication ledger. A member can publish only
as their own primary whisperer or one of their active specialists explicitly
contributed to that space. Each entry has a constrained kind, title, summary,
tool-name audit list, and artifact references; the service resolves and stores
the author name itself, so a caller cannot claim another teammate or agent.
EventBridge receives only the entry and author routing ids—the shared content
stays behind the teamspace API.

Each publication wakes the singleton organization agent through the agents
EventBridge/SQS path. The queue stores an idempotent, content-minimal delivery,
and the worker reloads the authoritative entry, teamspace, active participant
handles, organization-agent definition, and preceding synthesis from
PostgreSQL. Publications are processed in order within one organization while
different organizations can run concurrently. Missing, changed, or disabled
sources are dropped safely; retryable model or database failures are leased
and retried before being marked dropped.

The synthesis turn is isolated and temporary. Its model has no member token,
memory, private conversation, app connection, toolbox, or external-action
tool. It returns a strict JSON snapshot—summary, decisions, risks, open
questions, and proposed follow-ups—and the service atomically advances that
snapshot, records proposals, marks the delivery handled, and writes a
content-minimal lifecycle event. Replaying an EventBridge delivery or worker
transaction cannot create a second revision or duplicate proposal.

| Route | What it does |
| --- | --- |
| `GET /v1/organizations/{org}/organization-agent` | Gets or lazily creates the organization's singleton coordinating agent |
| `PATCH /v1/organizations/{org}/organization-agent` | Lets an organization owner or admin change that agent's name, instructions, or lifecycle status |
| `GET /v1/organizations/{org}/organization-agent/context` | Reads the latest durable shared synthesis; an untouched organization returns revision zero |
| `GET /v1/organizations/{org}/organization-agent/follow-ups` | Lists proposed follow-ups; these are auditable proposals, not claims of executed work |
| `GET /v1/organizations/{org}/teamspaces` | Lists spaces whose human teams are currently visible to the member |
| `POST /v1/organizations/{org}/teamspaces` | Creates one shared work space for a team the caller manages and binds the organization agent |
| `GET /v1/organizations/{org}/teamspaces/{space}` | Reads the organization agent, live whisperer roster, and safe contributed-specialist handles |
| `PATCH /v1/organizations/{org}/teamspaces/{space}` | Changes shared metadata or lifecycle status as a team manager |
| `DELETE /v1/organizations/{org}/teamspaces/{space}` | Removes the space without deleting members' private agents |
| `POST /v1/organizations/{org}/teamspaces/{space}/agents` | Contributes one of the caller's active specialists with a role and delegation mode |
| `PATCH /v1/organizations/{org}/teamspaces/{space}/agents/{agent}` | Lets only the specialist owner change its role or standing delegation authority |
| `DELETE /v1/organizations/{org}/teamspaces/{space}/agents/{agent}` | Lets the owner withdraw it or a team manager remove it |
| `GET /v1/organizations/{org}/teamspaces/{space}/entries` | Reads recent attributed shared publications, newest first |
| `POST /v1/organizations/{org}/teamspaces/{space}/entries` | Publishes immutable work as the caller's whisperer or contributed active specialist |

A thread is `{"id", "title", "created_at", "updated_at"}`; its title is the
first line of its first message.

**Runs.** A run's body is a message, `{"message": "..."}`, or the member's
answers to the confirmations the thread is waiting on,
`{"confirmations": [{"id": "...", "confirmed": true}]}` — never both. Every
waiting confirmation is answered in one request, since an unanswered one
would leave its tool neither run nor refused, and a message is refused with
`409 confirmation_pending` until they are. Everything that can refuse a run
happens before the stream opens, so a refusal is an ordinary JSON error:

| Code | Status | When |
| --- | --- | --- |
| `invalid_request` | 400 | The body is not one of the two shapes, or answers something that is not waiting |
| `unauthorized` / `forbidden` | 401 / 403 | Auth does not know the caller, or they are not in the organization; an agent's token; a cookie from another origin |
| `not_found` | 404 | Not the member's thread in this organization |
| `run_in_progress` | 409 | Another run is answering the thread |
| `confirmation_pending` | 409 | A message while a tool waits for the member's answer |
| `agents_unavailable` | 503 | This server has no Google Cloud project to run Krude on |

Once the run starts, the response is `text/event-stream`, with the run's ID in
`X-Oryxa-Run`. Each event is named after the item it carries and its data is
the item as JSON; a `: keep-alive` comment goes out every 15 seconds, so a
proxy does not close a run while Krude works:

| Item | Carries |
| --- | --- |
| `run_started` | `run_id` |
| `message` | `author` (`member`, or the agent), `text`; `error` when the model stopped without answering |
| `delta` | A piece of Krude's answer as it is written; the `message` that follows holds the whole text |
| `tool_call` | `call_id`, `tool`, `arguments` (left out above 16 KiB) |
| `tool_result` | `call_id`, `tool`; `failed` and `error` when it failed |
| `confirmation` | `call_id` (what the answer names), `tool_call_id` (the call it holds back), `tool`, `arguments`, `hint` (the tool's own reason, left out when ADK gives only its default), `status` |
| `artifact` | `artifact` (the file's name) and `version`, for a file the run kept |
| `run_completed` | `run_id`, `usage` |
| `run_failed` | `run_id`, and `error` in words the member can act on |

A run takes its thread before it reads what the thread waits on, and gives
it back without a trace when the request turns out to be invalid: read first,
a run finishing in between could have an answer applied twice, or leave a
confirmation it had just asked for unanswered. It renews its hold within ten
seconds a time, and stops when it cannot for a whole lease.

A thread's history is the same items, without deltas or the run's own, each
with the stored event's `id` so a client can match what it streamed; a
confirmation there says whether it was `approved` or `declined`. A tool's
reply that it is waiting for its confirmation is not shown as its result: the
result comes when it runs.

**One run at a time.** A run holds its thread in `agents_primary.agent_runs`
while it runs, with a lease it renews every third of a minute: a partial
unique index keeps a second running run off the thread across every replica,
and the next run to start abandons one whose replica died once its lease has
run out (`internal/runstore`). Every run is kept there with who started it,
how it ended and what it cost.

**Cost.** Every model Krude and its helpers run on is wrapped
(`internal/usage`), so a run adds up the tokens of every model call it makes —
Krude's own, and those of the planner, researcher, helpers and dynamic
sub-agents, which run inside the tool call that started them. Finishing a run
records its outcome and `agents.run.completed.v1` or `agents.run.failed.v1`
in one transaction, each carrying the totals and a per-model breakdown priced
by `internal/pricing`. A failed run still reports what it spent.

**Deadlines.** A run lasts at most `AGENTS_PRIMARY_RUN_TIMEOUT_SECONDS`, and
its stream sets its own write deadline just past that, since the host sets
none for this service. A member who stops listening stops the run: its context
is the request's.

**Artifacts.** A file a run keeps — a browser screenshot, something the
sandbox saved — shows as an `artifact` item, and the artifacts route serves
it to the thread's member. It is served from the app's own origin, so it can
never run as a page there: every response carries a sandboxing
`Content-Security-Policy` and `nosniff`, and only images, PDFs and plain text
are shown in place; anything else, HTML included, downloads. A `user:` file
is the member's own and reads through any of their threads.

**Durability boundary.** The web Durable Object stores the visible primary-chat
transcript and can resume its stream after a browser reconnect. A Go SSE run is
still not replayed after a Worker restart or deploy: its tools can have side
effects, and what it had stored stays in the ADK thread. Team workflow
executions are durable at the orchestration and ledger boundary and can be
redelivered through the callback queue. An individual model attempt remains
bounded by its run timeout; a retry reclaims the execution only after its
lease expires.

## Events agents consumes

The agents service reads its own queue, `AGENTS_EVENTS_QUEUE_URL`, which `go
run ./agents/primary/infra/events/cmd/provision` creates with the rule that
fills it (`internal/inbound`):

| Detail type | What agents does |
| --- | --- |
| `batching.trigger-batch.ready.v1` | Keeps the batch's triggers, all at once, to wake Krude for its member (see [Wake-ups](#wake-ups)) |
| `agents.interaction.requested.v1` | Persists a delivery job for the addressed target agent; one held for its owner completes without waking it |
| `agents.interaction.decided.v1` | Wakes the target to finish approved work, or the requester to observe a rejection |
| `agents.interaction.completed.v1` | Wakes the requester to observe a completed or failed result |
| `auth.member.removed.v1` | Removes the member's threads, waiting wake-ups, agent definitions, and cross-user request content in that organization |
| `auth.organization.deleted.v1` | The same for everyone in the organization |

A conversation, agent definition, or interaction payload should not outlive
the place it belonged to, so both of auth's events remove them there. Runs'
records and already published routing events stay for billing and audit, but
routing events contain no task or response content. Every handler is safe to
repeat, since the bus delivers at least once. Without a queue, nothing wakes
Krude and member-owned state outlives its place in an organization; the service
says so at startup.

Known limits: ADK files a member's `user:` artifacts under the member alone,
not the organization, so one saved while working in one organization reads
through their threads in another and outlives their place in both. And
finding an organization's threads means listing every thread ADK holds and
filtering by organization, since ADK keeps no index by it: fine at today's
size, slow for a large deployment, where a deletion could outlast the queue's
visibility timeout and run twice (harmlessly, since forgetting is repeatable).

## Wake-ups

A member's connected apps report things — an email arrives, a pull request
opens — as `apps.trigger.received.v1`. Batching holds them per member and
decides when they make one agent turn (see `batching/README.md`), and agents
wakes Krude for each batch it hands over, as `batching.trigger-batch.ready.v1`
(`internal/wakeups`):

- **Batched per member, by batching.** A batch's triggers are kept in
  `agents_primary.wakeup_events` all at once, by their own app events' ids, so
  a wake-up never hands over part of one and a trigger delivered twice is told
  once. Agents' own rule — `AGENTS_WAKEUP_BATCH_SIZE` (1) or
  `AGENTS_WAKEUP_MAX_WAIT_SECONDS` (120) — therefore wakes Krude as soon as a
  batch arrives. One wake-up hands over at most 20, oldest first; the rest
  wait for the next. A trigger that still comes straight from apps, from
  before agents read batches, is acknowledged and left to batching.
- **In one thread of theirs.** Each member has an "Updates from your apps"
  thread per organization, made the first time and made again if they delete
  it. The events arrive there as a message from their apps (`author: apps`),
  not from them, and Krude's answer follows.
- **Answered by the reduced agent, with no memory.** A run a member is not
  watching — an app event, a schedule or durable workflow, the organization
  agent, or an interaction another member's agent addressed here — is answered
  by the reduced agent rather than the one the member talks to, and neither
  reads nor writes memory. Every persistent agent has such a variant, not only
  Krude itself. A team delegation is not unwatched: the member asked for it in
  their own thread.
- **Told, not acted on.** The message hands Krude the events as data, never
  instructions — nothing in them changes what it may do — and asks it to say
  what happened and what needs the member's attention, reading more from
  their apps when that helps, and changing nothing. A write would still stop
  at a confirmation the member finds in the thread.
- **As the member, with the member away, and reaching nothing outside.** The
  run carries no token of theirs, so tools act under the standing grant they
  gave auth, or say they cannot (see [Acting for a member](#acting-for-a-member)).
  It is answered by `agents.UnattendedRoot`: Krude under the same name, told
  it works for someone who is away, with only the organization's knowledge and
  a read-only apps toolbox — no web research, sandbox, browser, sub-agents,
  helpers or saved files. The events are other people's words with nobody
  watching, so nothing in such a run can carry what it reads anywhere they
  could name, or ask for a confirmation that would sit in the thread. The run
  is recorded with `started_by: trigger`.
- **Only for someone still there.** Each batch checks, in auth's tables, that
  its member still belongs to the organization before anything runs; one for
  someone who left is dropped, since the bus can deliver their last events
  after the news that they left.
- **Only for someone who wants it.** A member can turn updates from their apps
  off in each organization (`PUT .../preferences`, "Updates from your apps" in
  their account settings; `agents_primary.wakeup_preferences`, no row meaning
  on). Then nothing new is kept for them, what was waiting is dropped, and a
  batch claimed before they turned it off is dropped when its turn comes; only
  a wake-up already running finishes. Their app connections and triggers are
  untouched, and turning it on again starts from what arrives next.
- **At least once, and never for ever.** A delivery the bus repeats is kept
  once, by the event's own ID. A batch is claimed with a lease just before its
  run, four members' at once; a thread that is busy — the member is in it, or
  it waits for their answer to a tool — puts the batch back for a minute
  without counting it against the events, and a failed run counts against
  each; an event that has failed three times, or waited three days, is dropped
  and logged. Events a wake-up has no room to tell wait for the next rather
  than counting as told. Two wake-ups making a member's thread at once settle
  on whichever was recorded first. A handled event keeps its ID for a week, to
  recognise a late repeat, and loses the app's data at once.

Interaction wake-ups use a separate durable job table because they have a
state machine rather than an app digest. The target's `requested` delivery is
complete only after it records a decision; an `approved` delivery is complete
only after it records a final result. A `requested` delivery for a request
held for its owner is completed without a run, since no agent may decide it;
the owner's approval arrives as the `approved` delivery, whose prompt says the
owner approved it. Rejected and completed deliveries notify the requesting
agent. Jobs lease across replicas, retry failed runs three
times, settle recorded revisions in order per request and recipient, and keep handled IDs
for a week. If an earlier run already advanced the interaction, a later or
repeated delivery is settled without running the agent again.

A delivery starts as soon as it is recorded. The consumer nudges the waker as
it records one, and the waker claims what is due while it has room, four runs
at once per replica, without waiting for the runs it started: a delivery never
waits behind an unrelated run, only for a free slot, and a run that ends
nudges the waker again. The five-second poll is the backstop, for a delivery
released to be retried later and one another replica recorded.

Not yet: nothing skips the wait for something urgent, such as a direct
mention, since that means reading each app's payload; and Krude is only ever
asked to report. What a member would have it do on its own is theirs to
decide.

## Models and routing

`Gemini` builds each model on Vertex AI without an API key to hold: locally
it uses Application Default Credentials, your `gcloud` login, and on AWS the
process signs in with its own AWS identity through workload identity
federation (`internal/gcpauth`, set by `GOOGLE_WORKLOAD_IDENTITY_PROVIDER` and
`GOOGLE_SERVICE_ACCOUNT`); the same credentials reach Memory Bank. `internal/modelrouter` routes every role to its tier, lite
wherever it can carry the work:

| Role | Runs on | Default |
| --- | --- | --- |
| `chat` — the member's conversation | main | `gemini-3.5-flash-lite` |
| `research` — grounded web research | main | same; Google Search grounding is not on every lite model |
| `plan` — structured planning | lite | `gemini-3.5-flash-lite` |
| `work` — helpers and dynamic sub-agents | lite | same |

Set `AGENTS_PRIMARY_MODEL` and `AGENTS_PRIMARY_MODEL_LITE` to change the two
models; a policy missing a role is refused at startup, so a new role cannot
silently get no model.

## Prompt caching

`internal/modelcache` wraps each model. When a request's stable prefix — the
system instruction and tool declarations — reaches
`AGENTS_PRIMARY_CACHE_MIN_TOKENS`, the wrapper puts it in an explicit Vertex
cached content, sends its name with the request and drops the duplicated
prefix, reusing the cache until two minutes before its TTL. Vertex enforces its
own floor: for the flash models that is 4096 tokens, so the default matches it
and a smaller prefix is never sent to a cache that would reject it. Below the
floor, or when Vertex refuses the cache, the request goes out untouched so
Vertex's implicit caching still applies; both kinds of cached tokens come back
in the model's usage metadata. Caches the process created are deleted on
shutdown.

## Guardrails

`internal/guardrail` screens input and output with Amazon Bedrock Guardrails.
The models run on Vertex, not through Bedrock's Converse API, so the
Converse-time `guardrailConfig` and `guardContent` modes cannot apply; the
wrapper uses the standalone `ApplyGuardrail` API, evaluating the newest content
of every request (a member message or a tool result) and every answer.

It is on when `AGENTS_PRIMARY_GUARDRAIL_ID` and a numbered
`AGENTS_PRIMARY_GUARDRAIL_VERSION` are set, and screens the main model: the
member's conversation, where their messages and every tool result — the lite
model's work included — arrive, and where every answer they see comes from.
While it is on, those answers do not stream (a partial answer that the
completed one would block cannot be unsent), so a run's stream carries
Krude's whole message without deltas, and a blocked answer arrives as
"I can't help with that." `AGENTS_PRIMARY_GUARDRAIL_FAIL_CLOSED` decides what
a failed screen does. Usage is counted inside the guardrail, so an answer it
blocks still costs what it spent.

## Measure it

`cmd/agentbench` hands the agents a few tasks and reports what each run cost:
latency, time to first text, tokens, cached tokens and the priced cost. It
needs the same ADC and `GOOGLE_CLOUD_PROJECT` as `cmd/devui`.

```sh
(set -a && . ./.env && go run ./agents/primary/cmd/agentbench -repeat 2)
```

Each task runs in a fresh session with the same member state, so the first
attempt pays for creating a cache and later attempts can hit it. Flags:
`-tasks` picks tasks by name, `-cache=false` runs without explicit caching for
comparison, `-v` logs every agent event, and `-with-notes` registers a notes
fixture toolbox whose skill is long enough to take the prefix over the cache
floor, so `-tasks tools -with-notes` shows a real cache hit (measured: about
21s cold against 7s warm, with 10,344 of 12,204 input tokens cached). The
report's cost is model tokens only: Google Search grounding and, later, tool
calls are priced separately, and token usage inside sub-agent and helper runs
is not visible in the event stream.

## Artifacts

An artifact is a file a conversation produced: a browser screenshot, a file
the sandbox made, anything `save_artifact` keeps. `load_artifacts` reads one
back by name. The sandbox is thrown away with the conversation; an artifact
is not.

`internal/s3artifact` is the durable store, an `artifact.Service` over S3 in
adk-go's own key layout, the one `artifact/gcsartifact` uses:

```text
{appName}/{userID}/{sessionID}/{fileName}/{version}
{appName}/{userID}/user/{fileName}/{version}       # a "user:" filename
```

A filename beginning with `user:` belongs to the member rather than to one
conversation, so every session of theirs sees it; anything else is the
session's own. Saving the same name again adds a version rather than
replacing one: S3 cannot list and write atomically, so a save writes `max+1`
with an `If-None-Match` precondition and retries with a fresh version when
another writer took it, which means a version is never overwritten.

Without `AGENTS_PRIMARY_ARTIFACT_BUCKET` the ADK launcher's in-memory service
is used instead and artifacts go with the process. Create the bucket first —
private, encrypted, and expiring its objects after 30 days, because a
conversation is not an archive:

```sh
(set -a && . ./.env && go run ./agents/primary/infra/artifacts/cmd/provision)
```

Repeatable: a bucket that already exists keeps its objects and has its
settings reapplied. The store's own round trip runs against a real S3 API
when one is named, and is skipped without it:

```sh
AGENTS_PRIMARY_TEST_AWS_ENDPOINT=http://localhost:4566 \
  AGENTS_PRIMARY_TEST_ARTIFACT_BUCKET=oryxa-agent-artifacts \
  go test ./agents/primary/internal/s3artifact/
```

## Memory

Memory is what Krude knows about a member across their threads, as opposed to
session state, which is one conversation's own.

Krude reads it with `load_memory`, a tool it calls with a query when a
question needs what it learned before. ADK also offers `preload_memory`,
which searches on every model request and appends what it finds to the system
instruction; that is a memory read per model step, and it would rewrite the
stable prefix [prompt caching](#prompt-caching) exists to hold, so Krude does
not use it. The tool is only on Krude where a memory service is actually
behind it: without one it would be there to be called and fail every time.

**A run nobody is watching has no memory at all**, neither reading nor
writing. What steers such a run is text from the member's apps, which anyone
who can email them or open an issue wrote (see [Wake-ups](#wake-ups)).
Remembering one would let that text put facts about the member into what
Krude knows, where they would outlive the run and steer later ones; reading
would hand that text what Krude already knows. The member's own runs in the
same thread are remembered as usual.

That boundary is not airtight, and it cannot be: a member's own run on a
thread a wake-up already wrote to answers with that text still in context, so
Krude's own answer can carry a claim the apps made, and the answer is
remembered. What this rules out is app text becoming a memory on its own,
with nobody reading it.

Vertex AI Memory Bank keeps the memories, behind the seam every backend comes
through, `internal/adkmemory`. Without `AGENTS_PRIMARY_MEMORY_AGENT_ENGINE`
there is no backend, the runner is built without a memory service, and Krude
remembers nothing between threads.

**Memory is scoped to an organization, not just a member.** ADK addresses
memory by app name and user alone: `memory.SearchRequest` carries nothing
else, and the runner fills it from the session's own user. But a member can
belong to more than one organization — `adksession` seeds them into the one
named on the session, or their first — and threads are already kept apart by
the organization in their state. A memory service taking ADK's addressing at
its word would answer a question asked in one organization with what it
learned in another.

So nothing in `adkmemory` is addressed by a user alone. A `Backend` is
reached only through a `Scope`, which is one member in one organization and
is invalid without both:

| Type | What it is |
| --- | --- |
| `Scope` | Whose memory a read or write addresses. `Fields` renders it as the key-value map a backend partitions by; a backend addressed by a single string instead, as mem0 is, needs both halves in it and no two scopes rendering the same |
| `Backend` | Where memories are kept. Every call names the scope it acts in, so an implementation cannot answer outside one |
| `Service.For` | The `memory.Service` one run is given. It holds its scope from the moment it is built |

A run's scope is fixed in `internal/threads` before the run starts, from the
member the request already resolved, rather than read off each request. The
view refuses a request naming anyone else instead of answering it from the
scope it happens to hold: a mismatch is a bug above it, and one that would
read another member's memory, so it fails rather than guessing.

**Memory is forgotten with the organization.** `Forget` removes every memory
in an organization — only one member's, when a member is named — and the run
API's own `Forget` calls it before it removes their threads, so the queue
handler that already drops a departed member's threads and wake-ups drops
what Krude learned from them too. Memory distilled from a conversation should
no more outlive the organization it was held in than the conversation does.

`InMemory` is a backend for development and tests, one ADK in-memory service
per scope: the ranking is ADK's and the isolation is this package's. What it
holds goes with the process.

### Memory Bank

Memory Bank does not keep conversations. It reads what was said, extracts the
facts worth keeping, and consolidates them with what it already holds, so a
preference stated once and contradicted later ends up as one fact rather than
two. That extraction is the part worth buying rather than building: retrieval
over an organization's own vectors is what `rag` already does.

`MemoryBank` scopes by Memory Bank's own scope map, `Scope.Fields`. The
service returns a memory only to a request whose scope has the same keys and
values, so the organization is a dimension matched where the memories are
kept, not a convention this service encodes into a user string.

Agent engines are regional, so `AGENTS_PRIMARY_MEMORY_LOCATION` names the
region the engine is in. It defaults to `GOOGLE_CLOUD_LOCATION`, whose own
default is the global endpoint the models are served from, which does not
host engines. The service refuses to start pointed at it, because the
alternative is a resource path that is only wrong on the first run that needs
it.

It talks to the API itself rather than through ADK's `memory/vertexai`, for
two reasons. ADK generates memories from a **Vertex session resource**, and
threads live in this deployment's PostgreSQL, so there is no session in the
agent engine to read; `Write` sends the conversation's own turns instead.
And ADK hardcodes the scope to `{"user_id": …}`, which is the addressing this
package exists to avoid.

Only text is sent. A tool's call and its result are how an answer was reached
rather than something the member said, and they carry whatever the tools
read. A run sends only the turns it added, because Memory Bank re-reads
everything it is given with a model and a thread resent from the beginning
after every run costs more the longer it gets.

Writing is bookkeeping after the answer, like compaction: a run hands its
conversation over once it has finished successfully, and a memory that was
not kept is a log line rather than a failed run. A failed run is not
remembered at all — what it added to the thread is a question and an error.

`Forget` reads the bank and matches the scope here rather than sending a
listing filter. Memory Bank matches that filter by equality on the whole
scope, so it cannot express "every member of this organization", and a filter
string whose exact form decides whether memories are deleted would fail by
leaving them behind — quietly, which is the one failure that matters here.
Forgetting runs when a member leaves or an organization is deleted, never on
a hot path.

### Setting up a memory bank

Memory needs an agent engine with a memory bank, and nothing here creates
one: it is the same shape of dependency as the artifact bucket or the
AgentCore resources, provisioned once for a deployment.

1. **Create an agent engine with a memory bank** in the project, with a
   `context_spec.memory_bank_config`. Its `similarity_search_config` is not
   optional here — retrieval uses similarity search, and a bank without it
   rejects every read. The embedding model defaults to `text-embedding-005`
   and the model that extracts memories to `gemini-2.5-flash`; both are worth
   setting deliberately, since the extraction model is what decides what
   Krude ends up believing about a member. A `ttl_config` expires memories on
   a schedule, which is one more answer to how long a conversation should
   echo.
2. **Put it in a region**, and set `AGENTS_PRIMARY_MEMORY_LOCATION` to that
   region. Agent engines are regional; the global endpoint the models are
   served from does not host them, and the service refuses to start pointed
   at it.
3. **Grant the workload `roles/aiplatform.memoryUser`**, or a custom role
   with `aiplatform.memories.generate`, `aiplatform.memories.retrieve`,
   `aiplatform.memories.list` and `aiplatform.memories.delete`. Google's
   documented minimum for an agent is generate and retrieve alone; that is
   enough to remember and recall, and not enough to forget, which would leave
   a member's memories behind when they leave.
4. **Name the engine** in `AGENTS_PRIMARY_MEMORY_AGENT_ENGINE`. Without it
   memory is off: Krude is built without `load_memory`, the runner without a
   memory service, and nothing is written.

Memory Bank bills storage by the GiB-month and reads and writes by the
operation, on top of the model it runs to extract memories. One member turn
costs one write; `load_memory` costs one read, and only when Krude calls it.

The round trip runs against a real memory bank when one is named, and is
skipped without it. It writes under an organization of its own and forgets it
afterwards:

```sh
GOOGLE_CLOUD_PROJECT=oryxa GOOGLE_CLOUD_LOCATION=us-central1 \
  AGENTS_PRIMARY_TEST_MEMORY_AGENT_ENGINE=1234567890 \
  go test ./agents/primary/internal/adkmemory/ -run TestMemoryBankRoundTrip
```

## Events

Agents primary publishes on EventBridge source `oryxa.agents` in the envelope
every Oryxa publisher uses (`event_id`, `version`, `occurred_at`, `data`).
Contracts live in `/contracts/events/agents/v1`:

| Detail type | When |
| --- | --- |
| `agents.session.created.v1` | A session is created |
| `agents.session.deleted.v1` | A session is deleted |
| `agents.run.completed.v1` | A run finishes, with the model usage it cost |
| `agents.run.failed.v1` | A run ends in an error |
| `agents.agent.created.v1` | A primary identity or specialist is created |
| `agents.agent.updated.v1` | An agent is renamed, reconfigured, enabled or disabled |
| `agents.agent.deleted.v1` | A specialist is deleted |
| `agents.team.created.v1` | A member composes a primary-coordinated specialist team |
| `agents.team.updated.v1` | Team identity, status, roles or membership changes |
| `agents.team.deleted.v1` | A member deletes a team while its agents remain |
| `agents.workflow.created.v1` | A member creates a durable team workflow |
| `agents.workflow.updated.v1` | Workflow configuration, status, or schedule changes |
| `agents.workflow.deleted.v1` | A member deletes a workflow |
| `agents.workflow.execution.queued.v1` | An API or scheduled execution receives a durable ledger row |
| `agents.workflow.execution.started.v1` | A callback worker claims the execution |
| `agents.workflow.execution.completed.v1` | Execution succeeds or stops for member confirmation |
| `agents.workflow.execution.failed.v1` | Execution ends without a result |
| `agents.interaction.requested.v1` | A durable request is ready to route to its target agent |
| `agents.interaction.decided.v1` | The target approved or rejected a request |
| `agents.interaction.completed.v1` | The target completed or failed an approved request |
| `agents.teamspace.room.message.posted.v1` | A teamspace's room stored a message, a person's or an agent's answer, without its body ([A teamspace is a room](#a-teamspace-is-a-room)) |

A request's three events carry who it is between, its `status` and
`revision`, and, since the owner-approval fields reached them, where it was
made and who decides it: `teamspace_id`, left out for a request made outside
a teamspace; `approval`, `agent` or `owner`, always there, since every request
has one (the column is `NOT NULL DEFAULT 'agent'`, so a request stored before
approvals were recorded has `agent`); and `decided_by`, as the API's
`decision.decided_by` shows it (`{"kind": "member" | "agent" | "system",
"user_id"?, "agent_id"?}`), once the request is decided, left out before, and
for a decision an older build recorded without its decider. An approval
withdrawn when its owner takes deciding back takes `decided_by` with it. They
stay `v1`, since consumers ignore fields they do not know. Never the purpose,
task, context, decision's message or result.

`internal/events` holds the outbox, publisher and dispatcher: events are
recorded in `agents_primary.agents_outbox` with the state they describe and
the dispatcher publishes them at least once, with exponential retry; consumers
deduplicate by `event_id`. They are published in no particular order: the
shared dispatcher publishes each batch in parallel, and a failed event later,
and EventBridge keeps no order either. So a consumer orders what it hears by
the order the events carry, a request's `revision` or a room message's `seq`,
never by arrival. The session runtime records `session.created` and
`session.deleted` as sessions come and go, best-effort after the session
service has already succeeded. `cmd/devui` creates the outbox table at
startup, and starts the dispatcher when `EVENTBRIDGE_ENDPOINT` is set — with
dummy credentials for the emulator, like auth — so
`EVENTBRIDGE_ENDPOINT=http://localhost:4566` publishes session events to the
LocalStack bus from `docker-compose.yml`. Without an endpoint the rows wait in
the outbox for the service to drain. The service runs the dispatcher as a
worker, and records `run.completed` and `run.failed` as each run ends (see
[Run API](#run-api)); consumers get the run's totals and a per-model
breakdown, priced by `internal/pricing`.

## Development UI

ADK's web UI runs the agents locally. Its sessions, conversation events and
state live in PostgreSQL, its artifacts in S3 when a bucket is named (see
[Artifacts](#artifacts)), and it runs without memory: a memory is scoped to
the organization it was learned in, and the UI's own `user` belongs to none
(see [Memory](#memory)).
The session store uses ADK's native relational service through
the repository's instrumented PostgreSQL pool and keeps its tables in the
`agents_primary` schema. Log in for ADC and AWS once, start PostgreSQL, and
set `GOOGLE_CLOUD_PROJECT` in `.env` to a project with the Vertex AI API
enabled; without the database, ADC or the project the command exits at
startup. AWS credentials are resolved lazily, so the UI can start without
them. A sandbox tool call needs
`bedrock-agentcore:StartCodeInterpreterSession`,
`bedrock-agentcore:InvokeCodeInterpreter` and
`bedrock-agentcore:StopCodeInterpreterSession` for the configured interpreter.
A browser call needs `bedrock-agentcore:StartBrowserSession`,
`bedrock-agentcore:ConnectBrowserAutomationStream` and
`bedrock-agentcore:StopBrowserSession` for the configured browser. Scope the
workload role to the selected AgentCore resource identifiers; do not give the
agent AWS credentials as tool input.

```sh
docker compose up -d postgres
gcloud auth application-default login
aws login
cp .env.example .env
(set -a && . ./.env && go run ./agents/primary/cmd/devui)
```

Open http://localhost:8087/ui/. The REST API the UI calls is under
http://localhost:8087/api. A session the UI creates for its own `user` starts
with no state, because that user belongs to no organization, so Krude assists
"the member you are talking with" in "their organization". A session created
for a real auth user seeds the member's state from the auth tables. To try a
member, create a session with their user ID as the UI's user, then pick it in
the UI:

```sh
curl -X POST http://localhost:8087/api/apps/krude/users/user-owner/sessions
```

The session for `user-owner` then starts with `user_name`, `user_about`,
`user_email`, `user_role`, `org_name` and `organization_id` read from auth, so
Krude greets the member by name and knows their organization without any
manual state.

Arguments to `devui` replace its web UI defaults and go straight to ADK's
launcher, so `console` chats in the terminal instead:

```sh
(set -a && . ./.env && go run ./agents/primary/cmd/devui console)
```

## Module

Primary is part of the root module, `github.com/Shubham04-oss/oryxa-go`; it
imports `/internal/postgreskit` and, for `memberauth`'s service tokens,
`/internal/serviceauth` the way every other package in the module does.
Primary used to be its own Go module, replacing the root module with `../..`,
so that adk-go's frequent releases and the dependency tree they bring stayed
out of the module every other service shares. That reason no longer holds now
that primary deploys inside the single `oryxa` binary: one binary needs one
build list regardless of how many go.mod files declare it, so keeping adk-go
in a module of its own only meant a second list to keep resolvable.

Run its checks from the repository root, like any other package in the
module:

```sh
go test ./agents/...
go vet ./agents/...
```

## Run the service

Primary mounts in the single `oryxa` binary as the `agents` service, from the
repository root's `.env`:

```sh
(set -a && . ./.env && ORYXA_SERVICES=agents go run ./cmd/oryxa)
curl -i http://localhost:8080/healthz
```

Its migrations install `agents_primary.agent_runs`, the persistent
`agents_primary.member_agents` registry, member-owned team and membership
tables, the `agents_primary.agent_interactions`
request ledger, the content-minimal `agents_primary.interaction_deliveries`
jobs and their stable-thread map, durable workflow definitions and execution
ledger, and the events outbox on
the host's pool, then ADK's session tables. ADK names its tables without a
schema, so they migrate, and the service keeps them, on a pool of its own
whose search_path leads with `agents_primary`
(`postgreskit.Database.WithSchema`), sized by
`AGENTS_PRIMARY_DATABASE_MAX_CONNS` and connecting only when first used.
`AGENTS_PRIMARY_AUTO_MIGRATE` is `cmd/devui`'s alone; the binary migrates
under `ORYXA_AUTO_MIGRATE`.

Without `GOOGLE_CLOUD_PROJECT` there is no model to run Krude on, and the
service mounts anyway, answering every thread route with `503
agents_unavailable` rather than stopping every other service in the process
from starting. With it, the service needs the database and `AUTH_URL`, which
defaults to this process when it runs auth. It deploys as part of the one
`oryxa` image; there is no Dockerfile of its own.

To drive the API locally without Cognito, run the auth stub, whose tokens are
the seeded identities' role names (`test/identities`), and point `AUTH_URL` at
it:

```sh
AUTHSTUB_ADDRESS=127.0.0.1:18080 go run ./test/cmd/authstub
curl -X POST -H 'Authorization: Bearer owner' http://localhost:8080/v1/organizations/<org>/threads
curl -N -X POST -H 'Authorization: Bearer owner' -H 'Content-Type: application/json' \
  -d '{"message":"hello"}' http://localhost:8080/v1/organizations/<org>/threads/<thread>/runs

curl -X POST -H 'Authorization: Bearer owner' -H 'Content-Type: application/json' \
  -d '{"name":"Sales","description":"Owns pipeline work","instructions":"Review CRM opportunities and report risks."}' \
  http://localhost:8080/v1/organizations/<org>/agents

curl -N -X POST -H 'Authorization: Bearer owner' -H 'Content-Type: application/json' \
  -d '{"message":"Review this quarter"}' \
  http://localhost:8080/v1/organizations/<org>/agents/<agent>/runs
```

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `AGENTS_PRIMARY_MODEL` | `gemini-3.5-flash-lite` | Main model: conversation and grounded research |
| `AGENTS_PRIMARY_MODEL_LITE` | `gemini-3.5-flash-lite` | Lite model: planning and bulk single-turn work |
| `AGENTS_PRIMARY_PROMPT_CACHE` | `true` | Use explicit Vertex cached contents for large stable prefixes |
| `AGENTS_PRIMARY_CACHE_TTL_SECONDS` | `3600` | How long a created cache lives; 60–86400 seconds |
| `AGENTS_PRIMARY_CACHE_MIN_TOKENS` | `4096` | Smallest prefix worth an explicit cache; Vertex's floor for these models. Below it, implicit caching applies |
| `AGENTS_PRIMARY_ARTIFACT_BUCKET` | unset | S3 bucket artifacts are kept in; without it they are held in memory and go with the process |
| `AGENTS_PRIMARY_MEMORY_AGENT_ENGINE` | unset | Vertex AI agent engine whose memory bank holds what Krude remembers across a member's threads, scoped to their organization. Its bank needs similarity search configured. Without it Krude remembers nothing between threads (see [Memory](#memory)) |
| `AGENTS_PRIMARY_MEMORY_RECALLED` | `0` (10) | How many memories one search returns. Memory Bank caps it at 100 |
| `AGENTS_PRIMARY_MEMORY_LOCATION` | `GOOGLE_CLOUD_LOCATION` | Region the memory agent engine is in, such as `us-west1`; never `global` |
| `EVENT_BUS_NAME` | `default` | EventBridge bus events publish on |
| `EVENTBRIDGE_ENDPOINT` | unset | EventBridge endpoint override for the LocalStack emulator |
| `DATABASE_URL` | unset | PostgreSQL connection for ADK sessions, events and state, runs, and the read of the member's auth tables when a session is created |
| `AGENTS_PRIMARY_AUTO_MIGRATE` | `true` | `cmd/devui` only: create the `agents_primary` schema and migrate ADK session tables at startup |
| `AWS_REGION` | `ap-south-1` | AWS Region for Bedrock AgentCore; Mumbai supports the AgentCore tools |
| `AGENTS_PRIMARY_SANDBOX_CODE_INTERPRETER_ID` | `aws.codeinterpreter.v1` | Built-in or provisioned AgentCore Code Interpreter identifier |
| `AGENTS_PRIMARY_SANDBOX_SESSION_TIMEOUT_SECONDS` | `900` | Per-conversation sandbox lifetime; 60–28800 seconds |
| `AGENTS_PRIMARY_BROWSER_ID` | `aws.browser.v1` | Built-in or provisioned AgentCore Browser identifier |
| `AGENTS_PRIMARY_BROWSER_SESSION_TIMEOUT_SECONDS` | `900` | Per-conversation browser lifetime; 60–28800 seconds |
| `GOOGLE_CLOUD_PROJECT` | unset | Project whose Vertex AI serves Gemini; the agents require it |
| `GOOGLE_CLOUD_LOCATION` | `global` | Vertex AI location: `global`, or the `us` or `eu` multi-region |
| `GOOGLE_APPLICATION_CREDENTIALS` | unset | ADC credential file; locally `gcloud auth application-default login` supplies ADC |
| `GOOGLE_WORKLOAD_IDENTITY_PROVIDER` | unset | On AWS: the workload identity pool provider's audience that admits the process's role. Set with `GOOGLE_SERVICE_ACCOUNT`, it replaces ADC |
| `GOOGLE_SERVICE_ACCOUNT` | unset | On AWS: the service account the federated role acts as, which holds the Vertex AI roles |
| `APPS_URL` | unset | The apps service's own URL; when set, Krude gets the apps toolbox (see Acting for a member) |
| `RAG_URL` | unset | Rag's own URL; when set, Krude gets the knowledge toolbox (see Knowledge toolbox). In the binary it defaults to this process when it runs rag |
| `KNOWLEDGE_URL` | unset | Knowledge's own URL; with `RAG_URL`, enables source coverage/freshness and confirmed refresh of existing sources. In the binary it defaults to this process when it runs knowledge |
| `AGENTS_PRIMARY_KNOWLEDGE_MIN_RELEVANCE` | `0` (off) | Reranker relevance below which a knowledge passage is left out, so a question the documents cannot answer comes back empty. On the scale of the reranker rag runs: 0.2 was measured for Google's `semantic-ranker-default`; calibrate before using it with another. No effect without a reranker |
| `AUTH_URL` | unset | Auth's own URL; where a member-absent run exchanges the member's standing grant for an agent token |
| `AGENTS_PRIMARY_SERVICE_KEY_ID` | unset | KMS key agents signs its service tokens with, to ask auth for an agent token on a member's behalf |
| `AGENTS_ALLOWED_ORIGINS` | unset | Origins, comma-separated, whose cookie-authenticated requests may start runs and change threads; the web app's |
| `AGENTS_PRIMARY_RUN_TIMEOUT_SECONDS` | `600` | Longest one run may take; 60–3600 seconds |
| `AGENTS_PRIMARY_GUARDRAIL_ID` | unset | Bedrock guardrail that screens the member's conversation; unset turns screening off |
| `AGENTS_PRIMARY_GUARDRAIL_VERSION` | unset | The guardrail's numbered version, required with its ID; never `DRAFT` |
| `AGENTS_PRIMARY_GUARDRAIL_FAIL_CLOSED` | `false` | Block, rather than pass, what a failed screen could not check |
| `AGENTS_PRIMARY_DATABASE_MAX_CONNS` | `8` | Size of the pool the service keeps for ADK's tables, apart from the shared one |
| `AGENTS_EVENTS_QUEUE_URL` | unset | Queue carrying batching's app batches and interaction lifecycle events; without it neither app nor cross-agent events wake persistent agents (see [Wake-ups](#wake-ups)) |
| `AWS_ENDPOINT_URL_SQS` | unset | SQS endpoint override for the LocalStack emulator |
| `AGENTS_WAKEUP_BATCH_SIZE` | `1` | How many of a member's waiting events wake Krude at once; 1–100. Batching already decided when they make a batch, so one wakes Krude as it arrives |
| `AGENTS_WAKEUP_MAX_WAIT_SECONDS` | `120` | Longest a member's oldest waiting event waits before waking Krude anyway; 10–86400 |
| `AGENTS_WORKFLOW_QUEUE_URL` | unset | SQS callback queue polled by the workflow worker |
| `AGENTS_WORKFLOW_STATE_MACHINE_ARN` | unset | Step Functions Standard state machine started by both API and Scheduler |
| `AGENTS_WORKFLOW_SCHEDULER_ROLE_ARN` | unset | Role EventBridge Scheduler assumes to start that state machine |
| `AGENTS_WORKFLOW_SCHEDULE_DLQ_ARN` | unset | DLQ for exhausted Scheduler deliveries |
| `AGENTS_WORKFLOW_SCHEDULER_GROUP` | `default` | EventBridge Scheduler group holding member workflow schedules |
| `AWS_ENDPOINT_URL_SFN` | unset | Step Functions endpoint override for development emulators |
| `AWS_ENDPOINT_URL_SCHEDULER` | unset | EventBridge Scheduler endpoint override for development emulators |
| `AGENTS_WORKFLOW_LOCAL` | `false` | Development-only in-process workflow adapter for HTTP/Gemini tests; not restart-durable and does not support cron |

The host sets no write timeout for this service: a run's stream outlives any
fixed write deadline, so the run route sets its own.
