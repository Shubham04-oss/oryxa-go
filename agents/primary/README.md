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
they gave (see [Wake-ups](#wake-ups)). The Cognito agent client is
provisioned in production; exchanging a real member's grant against it has
not yet been exercised live.

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

When `WORKSPACE_URL` is set, the same toolbox also exposes
`directory_find_owners` for reviewed ownership hints and
`directory_is_above` for explicit org-chart authority. The workspace API
filters context to joined members before either tool sees it. The all-in-one
host supplies `WORKSPACE_URL` when the workspace service runs.

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
cannot report using a tool outside the approved set. These grants never
bypass a tool's existing authorization or human-confirmation requirements,
and the run that does the approved work has exactly the toolboxes they allow
(see below).

**What a request may ask to use.** `requested_tools` and `allowed_tools` name
toolboxes of the member asked: `apps`, their connected apps, which approved
work only reads, and `knowledge`, the organization's knowledge search. They
are read in any case and kept spelled as here (`agents.RequestTool`). Any
other value is accepted, as it always was, and grants nothing.
`allowed_tools` must be a subset of `requested_tools`, or the decision is
`400 invalid_request`, and an approval that leaves it out allows none:
nothing grants a tool implicitly.

**Jev chooses what a request made in a teamspace needs.** A request made in
a teamspace that omits `requested_tools` gets them from Jev as it is
created (`interactions.Service.requestedTools`): one Choice over the
requester's own words, the task and its context, and never anything of the
member asked, between `none`, `knowledge`, `apps` and `apps_and_knowledge`.
Its owner's approval then shows what would be read, and allows it or not.
A choice with a confidence below 0.5 (`interactions.ToolsConfidence`) falls
back to requesting knowledge. Over four options, the threshold is a choice
holding at least 62.5% of the probability: the least TypeSafe's guidance
acts on at all, and acting on it
is safe here because it only proposes what the owner approves in person.
No Jev (`TYPESAFE_API_KEY` unset), one that does not answer within 3 seconds,
and one that fails request knowledge as well, and the request is made all the
same. An omitted list outside a teamspace also requests knowledge. An explicit
list, including an empty list for work without lookups, stays as given.
`tools_judged_by` says who chose: `requester`, `jev` (which may have chosen
none), or `none`,
nobody (the knowledge fallback outside a teamspace, or Jev unsure,
unset, silent or failing). Older `none` records may request no tools. A request
made before this was kept has nobody on record, and the field is absent.
Sent again under its `Idempotency-Key`, a request
whose tools Jev chose is compared as it was sent, omitting them. A retry cannot
change between omission and an explicit empty list under the same key.

**Requests inside a teamspace, and who approves them.** A request may name the
teamspace it belongs to (`teamspace_id`), which is how a teamspace side
conversation asks a teammate's agent for something. The ledger then asks the
teamspace, against the live Teams directory, where the target sits
(`teamspaces.Service.RequestSeat`): the requester must be on the teamspace's
team now, the space and the organization agent must be active, and the target
must be a teammate's primary agent — their whisperer seat — or an active
specialist a current teammate contributed there. Anything else is refused
before anything is recorded (see [Agent request API](#agent-request-api)).

A run pinned to one teamspace (see
[Pinning a run to a teamspace](#pinning-a-run-to-a-teamspace)), which is how a
side conversation runs, files its requests in that teamspace whatever the model
names, and refuses to name another.

Every request carries `approval`, set when it is made. The requester only
chooses whether to ask inside a teamspace, and in which; who approves then
follows the target's own standing there, and nothing the requester sends
sets it:

| The target | `approval` | Who decides |
| --- | --- | --- |
| Any agent, in a request made outside a teamspace | `owner` | Only that member, in their own session |
| A specialist contributed to the teamspace with `automatic` delegation | `agent` | The same |
| A teammate's primary agent | `owner` | Only that member, in their own session |
| A specialist contributed with `request` delegation | `owner` | Only that member, in their own session |

The owner's approval therefore guards every request but one made in a
teamspace to a specialist its owner gave `automatic` delegation there, the
one standing rule that lets an agent decide. A requester who leaves
`teamspace_id` out reaches the member, never their agent's own decision. An
older build stored `agent` for every request made outside a teamspace; those
are held all the same (`interactions.HeldForOwner`), and read as `owner`. A
specialist contributed to two teamspaces with different modes is approved as
the teamspace the request names has it, for a requester on that teamspace's
team.

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
older build recorded has no `decided_by`.

An agent's decision reaches the member who asked in fixed words,
`Approved by their agent.` or `Declined by their agent.`, never the message
the agent wrote (`interactions.AgentApproved`, `AgentDeclined`). A run
deciding holds whatever it may reach, and no guard reads a decision (only an
answer is held for review), so what an agent writes there could otherwise
carry what it read of its member's. Its message stays in its own thread,
with the call that carried it. A member deciding in their own session writes
to the member who asked in their own words. The waker tells an agent its owner
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
screen: the member finds it only by listing their requests. A pending request
that expires records no event, so nothing tells the member who asked that a
held request ran out unanswered (reads show it `expired`). Both are follow-ups
that need a Requests screen. An approved request that expires unanswered is
different: Oryxa closes it, failed, within seconds, and the member who asked
is told (see [Wake-ups](#wake-ups)).

**What a delivered request may reach.** The member whose agent is woken is not
there, so the run answers on the reduced agent (see [Wake-ups](#wake-ups)) and
what it may reach follows what their own agent has already allowed:

- **Deciding** is never a delivery's. No run nobody is watching decides a
  request: a new request's delivery starts no run, whoever may decide it, and
  `interactions.Service.Decide` refuses an agent's decision in any run its
  member did not start (`memberauth.RunOrigin` is not `member`): an app
  event, a schedule, a team, the organization agent, or a delivery. A request
  its agent may decide (a specialist with automatic delegation) waits for that
  agent in a conversation its member started, or for the member. A delivery's
  interactions toolbox never offers `interactions_decide`. Nothing another
  member wrote reaches anything of this member's before an approval, and
  approved work always runs on its own, with exactly what the approval allows.
- **Reporting** a rejection or a result back to the member who asked reads
  the request alone: neither their apps nor their knowledge.
- **Doing approved work** runs with exactly the toolboxes its decision's
  `allowed_tools` name, beside the request (`interactiondelivery.approvedReach`):
  `apps`, the read-only apps toolbox (the same one a wake-up gets, never the
  writing one), and `knowledge`, the read-only knowledge, both, or neither
  (`managedagents.DeliveryReads`, `DeliveryApps`, `DeliveryQuiet` or
  `DeliveryReport`). An approval that allows none, or names only tools the
  vocabulary does not know, reaches the request alone. The limit is the run's
  own, not an instruction: what was not allowed is not loaded, and the prompt
  says so. The ledger still checks the response's own account of it, that
  `tools_used` is within `allowed_tools`. Only this run answers
  (`agents.DeliveryWorks`), and it does not decide.

  An approval an agent makes itself therefore costs one more run than it did
  when the deciding run could go on to answer: the approval's event through
  the bus and the agents queue, which wakes the waker as it is recorded
  (well under a second, #97), and that run's own model calls, the first of
  them reading the request again (2 to 37 seconds each, as measured below).
  An approval its member makes in person always took a run of its own.
- No unwatched run may start an interaction of its own: `interactions_create`
  is left out, so a request cannot fan out into requests on other members.
- A delivery is about its one request. Its run gets an interactions toolbox
  of its own (`managedagents.DeliveryReads`, `DeliveryApps`, `DeliveryQuiet`
  and `DeliveryReport`, the reach above) with `interactions_get`,
  `interactions_decide` and `interactions_respond` only: `interactions_inbox`
  and `interactions_sent` are not there, so the model cannot browse the
  member's other requests whatever it makes of the request. Of the three it
  is offered only those its purpose uses, from its first model call
  (`agents.DeliveryTools`): reading and deciding its request in a run
  deciding it, reading and answering it in one doing the approved work, and
  reading it alone in a report. Each tool still refuses, when called, what
  its run is not for. They act on the delivered request alone: the waker starts the run naming it
  (`threads.RunInput.Delivery`, kept in the thread's state, which the model
  never writes), and any other `request_id` is refused, so a run holding the
  member's apps cannot read, decide or answer another of their requests. Nothing a
  delivery does needs them, since its message names the request. No other
  unwatched run has an interactions toolbox at all: an app wake-up, a
  schedule or workflow, and a turn in a room can neither list the member's
  requests nor read, decide or answer any.
- Every delivery run's start is logged at INFO, `interaction delivery run
  started`, with its request, the delivery's kind and purpose, its profile,
  and the toolboxes and interactions tools it was given, by name
  (`interactiondelivery.Config.Toolboxes`); never what any of them holds.
- A delivery run has no `load_artifacts`, which would list the member's own
  files, the `user:` ones every thread of theirs shares, into a run another
  member's words steer.

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
the strict tools. A model answer by itself does not acknowledge target work:
the ledger must actually leave `pending` or `approved`. Every recipient uses
one stable conversation per interaction, recorded earlier revisions settle
before later ones while newer authoritative state supersedes stale work, and
repeat events deduplicate by `event_id`. Runs carry no member access token, do
not read or write long-term memory, and remain subject to the normal tool
authorization and confirmation boundaries.

A delivery run starts with all it may reach already loaded: every toolbox of
its reach has its tools declared, and its skill in the instruction, from the
first model call, and the run has no `list_tools` or `load_tools`, so no
model call goes to finding what it could use (`assembly.Krude.Delivering` for
Krude, `ManagedDelivering` for a specialist). Approved work starts with
what its approval allows of the read-only apps and the read-only knowledge,
and the request's interactions toolbox; deciding a new request, and a
report, with the interactions toolbox alone.

That puts the whole reach on every call. For approved work it is 4.7 KB of
tool declarations and 4.8 KB of skills that the interactions toolbox alone
did not carry, less the 2 KB that `list_tools`, `load_tools` and
`get_user_details` (below) took: about 1,900 tokens more per call, 1,100 more
to decide, and 500 fewer for a report. Against that, every measured delivery
spent a model call on `list_tools`, 2.8 to 37 seconds, and approved work that
reads the member's apps spent another on `load_tools`, each call sending the
whole conversation again. A run's prefix no longer changes midway either, so
when it is large enough for an explicit prompt cache, one cache serves every
call rather than a second after a toolbox loaded. The waker still marks the
interactions toolbox loaded in the delivery's thread
(`threads.RunInput.Toolboxes`), so a member who carries that thread on in
their own chat has it there. App wake-ups and a member's own runs find and
load toolboxes as before; a turn in a room starts with its one toolbox loaded.

The message that wakes the agent says where the request comes from, so the
agent need not look it up. The agent asked to decide it, or to do its work,
is told the agent that asked and the member it asked for, by the name they
chose and their sign-in address, as auth keeps them: auth has no other
handle for a person, and a room's `@handle` exists only inside that room.
Both sides are told the teamspace it was made in (`interactiondelivery.Config`
`Members` and `Teamspaces`). A report to the member who asked names no one:
the one who asked is that member, whom the run's instruction already names.
Each name is one quoted line of at most 60 characters, and the message
treats the names as it does the request, as untrusted data. None of it is
the request's content, which stays behind `interactions_get`. A name that
cannot be read is left out, and the agent is woken all the same.

So a delivery run is not offered `get_user_details` either. Everything it
returns, the name, role, sign-in address and organization of the member the
agent works for, is in the run's instruction from its first call; a delivery
thread is too short for any of it to scroll out of sight; and it never
described the member who asked. Two of three measured runs called it, at 7 to
16 seconds each, and learned nothing their instruction did not say.

The addressed agent's delivery run ends on the call that settles it:
`interactions_respond`, or `interactions_decide` either way. That call sets
ADK's `SkipSummarization`, so no model call follows it. The member it works
for is not there to be told, and nothing is lost: the request holds its
decision, who made it and its result, which both members read (in a
teamspace's side conversation the answer shows as the agent's message, "on
behalf of … approved by …", and a failure as a line), and the call, with
what it recorded, stays in the delivery thread. A closing message was one
more model call, 3 to 37 seconds, restating another member's request to
nobody while the run held its thread and a delivery slot. An approval's work
runs on its own, and a member's own run is unchanged.

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
| `POST /v1/organizations/{org}/threads/{thread}/runs` | Runs Krude on the thread and streams the run. The body may pin the run to one teamspace with `teamspace_id` (see [Pinning a run to a teamspace](#pinning-a-run-to-a-teamspace)) |
| `GET /v1/organizations/{org}/threads/{thread}/artifacts/{name}` | A file the thread's runs kept, its latest version or `?version=` |
| `POST /v1/organizations/{org}/answer-guard/check` | Reads the texts of an answer for personal data before the member is shown it, and says what may be shared of them (see [Checking an answer before it is shown](#checking-an-answer-before-it-is-shown)) |
| `GET /v1/organizations/{org}/preferences` | What the member chose about Krude here: `{"app_updates": true}` until they turn [wake-ups](#wake-ups) off |
| `PUT /v1/organizations/{org}/preferences` | Records the member's own choice, `{"app_updates": false}`; an agent's token is refused like everywhere else |

Every thread says where it came from, as `origin` in its JSON (in the
listing, on creation, and in a thread's detail): `"chat"` is the member's own
conversation with Krude, `"side_conversation"` a thread the web app's side
conversation opened to run the member's own agent on its internal prompt, and
`"wakeup"` the thread their apps' events are reported in (see
[Wake-ups](#wake-ups)), which they did not start. The origin is written to the
thread's session state when it is created, beside the organization it was
started in, and nothing changes it afterwards. A client can start a chat or a
side conversation, and `POST` refuses any other origin, `"wakeup"` included:
only agents makes the wake-up thread, and `?origin=wakeup` lists it. Any other
thread Oryxa opens for the member itself is a chat, and so is a thread with no
origin in its state: every thread from before origins. That includes a member's
"Updates from your apps" thread from before wake-ups had an origin of their
own; the first wake-up for such a member starts a new thread of origin
`"wakeup"` and leaves the old one a past chat that nothing more is added to.
Only the listing tells origins apart: reading, running, deleting or downloading
from a thread by its id works the same for every origin.

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
| `GET /v1/organizations/{org}/agent-requests` | Lists, newest first, the requests the member's agents sent and were sent; `?box=inbox`, `sent` or `all` (the default) says which side |
| `GET /v1/organizations/{org}/agent-requests/inbox` | Lists requests addressed to the member's agents |
| `GET /v1/organizations/{org}/agent-requests/sent` | Lists requests sent by the member's agents |
| `GET /v1/organizations/{org}/agent-requests/activity` | What happened to the member's Slack asks, newest first: asks made and received, how each was decided and where, bypasses, leases, answers and secrets opened; an `answered` entry says `written_by_owner` when the answer was the owner's own reply. Names, states and times, never a question or an answer (`?limit=`) |
| `GET /v1/organizations/{org}/agent-requests/{request}` | Reads one request visible to the member |
| `POST /v1/organizations/{org}/agent-requests/{request}/decision` | The member approves or rejects, in their own session, with narrowed tools and permissions; the only way a request held for its owner is decided. Approving one made in a teamspace reads it again first, with the member's own token |
| `POST /v1/organizations/{org}/agent-requests/{request}/response` | Completes an approved request with the strict result envelope |
| `POST /v1/organizations/{org}/agent-requests/{request}/review` | The member sends, sends masked, or withholds an answer the answer guard held for them, in their own session (see [Answer guard](#answer-guard)) |
| `POST /v1/organizations/{org}/agent-requests/{request}/consent` | The owner's choice of a request that came from Slack, in their own session: `{"choice": "summary" \| "access" \| "deny"}`. `409 already_decided` carries the winner when the button in Slack was clicked first (see [The Slack consent loop](#the-slack-consent-loop)) |
| `POST /v1/organizations/{org}/agent-requests/{request}/reply` | The owner answers in their own words, in their own session: `{"text"}`, trimmed, 1 to 4000 characters. A pending request is approved and completed in one change, and the owner's agent never runs; an answer held for their review is replaced and never sent. `409 already_decided` carries the winner, as `/consent`'s does (see [Reply yourself](#reply-yourself)) |
| `PUT` `GET` `DELETE /v1/organizations/{org}/agent-requests/{request}/secret` | The owner shares a credential the asker asked for (`{"value", "ttl_seconds"}`, 60 to 3600, an hour by default); the member who asked opens it once (a second view is `410 secret_gone`); the owner revokes it. `503 secrets_unavailable` without a store that can seal one |
| `DELETE /v1/organizations/{org}/agent-leases/{lease}` | The owner ends the access they gave an asker in one Slack thread before it runs out; the asker's follow-ups there ask again |

A request's JSON carries `teamspace_id` (absent outside a teamspace) and
`approval` (`agent` or `owner`, see
[Agent interactions toolbox](#agent-interactions-toolbox)) beside its parties,
status, decision and result; a decision carries `decided_by`. All three
listings take `?limit=` and four optional filters: `?teamspace_id=` keeps the
requests made in that teamspace, `?with_user=` those whose other party is
that member, `?origin=` (`agent` or `slack`) those made there, and `?ask_id=`
the requests of one Slack message. `GET .../agent-requests?teamspace_id=T&with_user=U` is how a side
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

### The Slack consent loop

A member @-mentions Oryxa's Slack app in a channel and names whose agents to
ask: "@oryxa ask @raya for the Q3 numbers". Each person named is told in a
direct message, with four buttons: Share answer, Allow for 1 hour, Deny, or
Reply yourself (see [Reply yourself](#reply-yourself)). Their agent answers in
the asker's thread only if they approve, and only after the guards below have
read the answer. The request ledger is the one source of
truth, so what Slack says, what the owner's and the asker's cards in Requests
say, and what their Oryxa DM and agent chats say are all views of it: the owner
may decide on either surface, and the first click wins. The design is
`docs/design/slack-consent-loop.md`, and the plan these packages were built to
is `docs/design/slack-implementation.md`.

**It is off unless `AGENTS_PRIMARY_SLACK_CONSENT` says `on`.** Off, nothing
below runs, though its tables are migrated, and the events it would hear are
acknowledged and dropped. On, the service refuses to start without
`AGENTS_PRIMARY_SLACK_TICKET_KEY`, `AGENTS_PRIMARY_SERVICE_KEY_ID`, `APPS_URL`
and `TEAMS_URL`, each named in the message. apps' side is
`APPS_SLACK_SURFACE` (`apps/README.md`); the deployment order is
`deploy/README.md`, "The Slack bot".

What happens, in order, and where each step is:

| Step | Where |
| --- | --- |
| apps verifies a delivery from Slack, keeps it for seven days and says so on the bus with ids alone: `apps.slack.message.received.v1` for a mention, `apps.slack.action.received.v1` for a click | apps; `internal/inbound` hands each to the router's queue of jobs |
| The router reads the delivery back from apps with a service token, matches the asker and each owner to members (by a *confirmed* Slack email, or, with `AGENTS_PRIMARY_SLACK_LINKED_ONLY` on, by the member's own Slack account link alone: an unlinked asker is refused `not_linked`, an unlinked owner is asked in Oryxa only and the asker told `owner_not_linked`, and an unlinked account's clicks and replies decide nothing; off, it logs who would not pass), and refuses what it must, telling only the asker: `not_member`, `owner_not_member`, `group_dm`, `external_channel`, `no_owner`, `too_many_asks` (an asker who already has 5 asks open, counting each of their mentions refused as a whole in the last hour as one, or a mention that names more than 5 people; a refusal for being at the limit, or because the screen failed, is not counted) | `internal/slackrouter` |
| The words are screened before anyone is asked: credentials in them are masked first (`consentguard.Strip`), then one Jev request judges work, injection, plausibility for this asker, credential request, and whether each owner's department fits. It fails closed: no `TYPESAFE_API_KEY`, or a Jev that fails or is slow, refuses every mention as `screen_failed`. An asker who has given agents no standing grant is told to (`no_grant`). Words that ask for a credential are filed as an ask of kind `secret` | `slackrouter.NewJevScreen`, `NewAskerDirectory` |
| `consentpolicy.Decide` says allow, needs consent or deny for each owner (a bypass needs a channel an administrator marked high priority, or the asker above the owner in the chart) and `CreateAsk` files one request per owner, from the asker's primary agent to theirs, held for the owner. The asker is told, alone, who was asked, with a link to Requests | `internal/interactions` |
| `agents.interaction.requested.v1` reaches the surfaces worker: the owner gets a notice with four buttons, each carrying a ticket (an HMAC of the request, the owner, the notice and the choice; Reply yourself's is of its own kind, below), and a note lands in the asker's agent chat | `internal/surfaces`, `threads.Note` |
| The owner decides: a click reaches the router as a job and becomes `interactions.Consent(ticket, clicker, "slack")`, which decides only for a genuine, unexpired ticket, made for the member who clicked, on a request still pending; the card's `POST .../consent` is the same decision. The ledger's row lock picks the winner, and the loser sees who won. `decided.v1` has the notice updated in place, whichever surface decided | `interactions.Consent`, `surfaces` |
| The owner's agent is woken to do the approved work, and answers with `interactions_respond`, alone and after its reads (see [Agent interactions toolbox](#agent-interactions-toolbox)) | `internal/interactiondelivery` |
| The answer is read twice before its asker does: by the answer guard, at the owner's level, and then by the consent guard (`internal/consentguard`), which masks credentials at every level, holds a copy of 20 or more words of what the run read and any link when only a summary was allowed, and holds an answer that does not stand on what the run's tools returned, is not relevant, is about a denied topic, or could not be checked. Held, it waits in Requests for its owner, and the thread says only that it is held | `interactions.answer` |
| The first delivered answer settles the ask, and the requests that did not answer are closed, their notices saying who did. The answer is posted in the thread with "From Raya's agent, approved by Raya" and how far, or, for a bypass, where the decision's reason says | `interactions`, `surfaces` |
| A choice of "access" leaves a lease: the same asker's follow-ups in that thread for an hour, or less where the organization expires consent sooner, are approved with no click, though the screen and both guards still read them. The owner may revoke it | `interaction_leases` |
| A credential is never posted to Slack. For an ask of kind `secret`, the owner types the value into Oryxa, sealed under `AGENTS_PRIMARY_SECRETS_KMS_KEY_ID`; the asker opens it once, and the thread is told only "Answered privately" | `internal/secretshares` |

A failed request says so, and one that ran out of time says that: the
surfaces post `failed` or `expired`, apps' words, and never why.

What it adds to the service:

- **Workers:** the router's, the surfaces', and the secret store's sweeper,
  which runs whatever is configured, so no secret outlives its hour.
- **Tables** in `agents_primary`, migrated whatever the switch says:
  `interaction_asks` and `interaction_leases`, and columns on
  `agent_interactions` (`ask_id`, `origin`, `consent`, `decided_via`,
  `decided_reason`; migrations 013 to 015 under the ledger's lock);
  `slack_jobs` and `slack_identities` (the router's, lock 7263510719);
  `surface_jobs` and `surface_links` (the surfaces', lock 7263510720); and
  `secret_shares` (lock 7263510721). None holds a message's text: jobs and
  links are ids, an identity is a match, and the ledger holds the words.
- **Forgetting:** a departed member's, or a deleted organization's, jobs,
  matches, links and secrets go with them. A workspace's install ending
  (`apps.slack.install.changed.v1`, state `ended`) has the router's matches and
  jobs and the surfaces' links for it removed. What was said in Slack stays
  there, in the workspace's own hands.
- **One apps client**, signed as `agents` with `AGENTS_PRIMARY_SERVICE_KEY_ID`
  for the audience `apps` and the purpose `slack`, shared by the router and the
  surfaces. Production must allow it: `APPS_SERVICE_CALLERS` names agents' key
  with that purpose, and `/v1/internal` stays blocked at the edge.
- **The acknowledgement's link** is the web app's address for Requests, on the
  first of `AGENTS_ALLOWED_ORIGINS`, which apps must also list in
  `APPS_ALLOWED_ORIGINS`: `/?org=…&view=requests&request=…` for an ask to one
  person, and without `request` for an ask to several. With no origin there is
  no link.

Without `AGENTS_PRIMARY_CONSENT_GUARDRAIL_ID` every answer to a Slack question
is held for its owner as unchecked, since nothing can ground it, and the service
says so at startup. Without `AGENTS_PRIMARY_SECRETS_KMS_KEY_ID` no credential
can be shared, and the three secret routes answer `503`.

The source-quality experiment `AGENTS_PRIMARY_CONSENT_KNOWLEDGE_PASSAGES_ONLY`
defaults to `false`. When enabled, knowledge grounding uses only the explicitly
returned passage text captured from `knowledge_search`; collection/status
metadata is excluded. Full capture remains available for summary copy checks,
and other tools keep their existing sources. Empty knowledge results alone hold
substantive answers as `NO_SOURCES`; another verified tool result can still
support an answer. Thresholds remain unchanged. No deployment enables this flag.
See [the experiment's examples](../../docs/testing/consent-passage-sources.md).

#### Reply yourself

An owner may answer in their own words instead of their agent's: **Reply
yourself** on the notice and on a held answer's review, or
`POST .../agent-requests/{request}/reply` from Requests. It is one answer, not
a conversation.

- **In Slack** the button opens a modal, which Slack allows only with the
  click's `trigger_id`, once and within three seconds, so apps opens it before
  it answers the click: "Reply to Alex, who asked:" and the question, as the
  message showed them, a box, and Send / Cancel. The button's ticket rides in
  the modal's `private_metadata`. Send is checked by apps (an empty reply, or
  one over 4000 characters, is shown as an error in the modal and kept
  nowhere), kept as an inbound record of kind `reply` and announced as a click
  is; the router hands it to `interactions.ReplyFromSlack`.
- **The ticket** is of its own kind, `reply`, made for the stage it was sent at
  (pending on a notice, in review on a review): a reply ticket never decides a
  choice or a review, and no other ticket takes a reply. It must be genuine,
  unexpired, made for the member who sent the reply, and the request still at
  its stage.
- **In the ledger** a pending request is approved (consent `reply`, no tools
  or permissions) and completed with the owner's words in one transaction,
  announced as `decided.v1` and then `completed.v1`. A held answer is dropped,
  never sent, and the owner's words delivered in its place, with the review's
  action `reply`. The row lock picks the first of a reply, a button in Slack,
  the card and a review; the others see who won.
- **The owner's agent never runs:** the inbound consumer wakes nobody for a
  reply's approval, and the waker finds nothing to deliver.
- **Guards:** the owner's words skip the consent guard's grounding and
  relevance checks, which are about what an agent read. The answer guard still
  reads them, at the level that masks secrets, and credentials are stripped as
  in every answer; a reply is never held for its own writer's review.
- **The asker** gets it as any answer: in the thread as "Raya replied:" and
  her words, with nothing of an agent's; in the asking chat for a request made
  in Oryxa. For a question that asked for a secret the thread is told only
  "Answered privately", and the notice has no Reply yourself.
- **The owner's message** becomes "You replied at …" and the words (not the
  words, for a secret), or, when something else was decided first, says what.
- **The JSON** says `result.written_by_owner: true`, `decision.consent: "reply"`
  or `review.action: "reply"`, and both members' activity marks the answer
  `written_by_owner`. Migrations 017 and 018 let the ledger's `consent` column
  say `reply`.

#### Owner-first asks

Some asks need the owner's own judgment, which no search of what they may read
answers: approve this, will you have it done by Friday, what do you think.
When a request is made, Jev judges whether it is one
(`interactions/answer_by.go`): one yes-or-no question over the requester's own
words, the task and its context, and nothing of the member asked. For a request
made in a teamspace that names no tools it is asked in the same call as what
the request needs.

- **`answer_by`** on the request JSON is `"owner"` when Jev's yes is at least
  0.7 (`AnswerByOwnerThreshold`), and `"agent"` otherwise, or when Jev is not
  configured, fails, is late or answers something unusable. A request made
  before migration 019 has none, which reads as `"agent"`; nothing is
  backfilled. An ask of a secret is never asked about: it is `"agent"`.
- **An owner-first request waits for its owner.** It is stored with approval
  `owner` whatever its teamspace seat says, `HeldForOwner` holds it, so no
  agent decides it and its `requested` delivery wakes nobody, and a Slack ask
  is approved by no lease and no bypass (`Bypass` refuses it). The owner's
  agent runs only once the owner picks **Let my agent answer**, which is the
  approval (`…/decision`) or, for a Slack ask, the summary consent. Reply
  yourself and Deny work as for any request.
- **The owner's Slack notice leads with Reply yourself**, then Let my agent
  answer and Deny, with no Allow for 1 hour; surfaces sends apps'
  `owner_notice` `answer_by: "owner"`, and the ledger mints no access ticket
  for it. The asker's side is unchanged: the web reads `answer_by` and the
  status to say whom it waits for.
- **On the bus** every `agents.interaction.*.v1` event carries `answer_by`
  (absent for older requests, read as `"agent"`), so a consumer never takes
  an owner-first request for one an agent may decide.

### Answer guard

Every answer one member's agent gives another member is read, before that
member reads it, by the answer guard (`internal/answerguard`): Amazon Bedrock
Guardrails' standalone `ApplyGuardrail`, on the output side, with the
guardrail `AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID` and
`AGENTS_PRIMARY_ANSWER_GUARDRAIL_VERSION` name (`deploy/production/answer_guard.tf`).
Unset, no answer is checked, as before; one set without the other refuses to
start. `AWS_ENDPOINT_URL_BEDROCK_RUNTIME`, which the SDK honours, points
development at a stand-in.

It reads every text of the result the member who asked would read, each in a
call of its own and all within 5 seconds: `summary`, `content`, and the
`artifacts` references. `tools_used` is not read: it can only name what the
approval allowed, the requester's own vocabulary. Nor is the decision's
`message`, which is written when a request is decided, not answered.

How strictly it reads a request's answer, and whether its relevance check runs
and what it does, follow the settings of the member who gives the answer: what
they chose for answers another member reads, the organization's minimum and the
deployment's ceiling (`interactions.Config.GuardPolicy`, wired to the same
settings the check route reads with; see [How strictly the guard reads an
answer](#how-strictly-the-guard-reads-an-answer)). A member who chose `secrets`
for those has an email address in an answer delivered as written. An answer to a
request made in Slack is read a second time, by the consent guard, before its
asker reads it there (see [The Slack consent loop](#the-slack-consent-loop)).

What it found is read from each finding in the assessments, never from the
response's top-level action: the held kinds are detect-only, so an answer
with nothing else in it comes back with action `NONE` and no outputs, its
findings listed all the same.

| Found | What happens | `guard` |
| --- | --- | --- |
| Nothing | Delivered as given | `{"state":"clear"}` |
| Only kinds that are always masked | Delivered as the guardrail masked it, each value as `{TYPE}` | `{"state":"masked","kinds":[...]}` |
| An email address, phone number or address (`EMAIL`, `PHONE`, `ADDRESS`) | Held in `review` for the addressed member, not delivered | `{"state":"held","kinds":[...]}`, the held kinds |
| A failure, a timeout, anything blocked, or a response that does not add up | Held the same way: it fails closed | `{"state":"held","kinds":["UNCHECKED"]}` |

Always masked: `PASSWORD`, `AWS_ACCESS_KEY`, `AWS_SECRET_KEY`,
`CREDIT_DEBIT_CARD_NUMBER`, `_CVV` and `_EXPIRY`, `PIN`,
`INTERNATIONAL_BANK_ACCOUNT_NUMBER`, `SWIFT_CODE`, `US_BANK_ACCOUNT_NUMBER`,
`US_BANK_ROUTING_NUMBER`, `US_SOCIAL_SECURITY_NUMBER`,
`US_INDIVIDUAL_TAX_IDENTIFICATION_NUMBER`, `US_PASSPORT_NUMBER`, `DRIVER_ID`,
`CA_HEALTH_NUMBER`, `CA_SOCIAL_INSURANCE_NUMBER`,
`UK_NATIONAL_HEALTH_SERVICE_NUMBER`, `UK_NATIONAL_INSURANCE_NUMBER` and
`UK_UNIQUE_TAXPAYER_REFERENCE_NUMBER`. A kind that is always masked but came
back found and not masked is held, since its value is still there. `NAME`,
`AGE`, `USERNAME`, `URL`, `IP_ADDRESS`, `MAC_ADDRESS`, `LICENSE_PLATE` and
`VEHICLE_IDENTIFICATION_NUMBER` are not set up at all: they are too common in
work answers. A failed answer is read like a completed one: its summary is
read by the member who asked too.

A held answer is kept in two versions, as `held`: `held.result`, as the
guardrail left it, with the kinds that are always masked masked; and
`held.masked_result`, with every held value replaced by its kind as well,
such as `{EMAIL}`. A held value is found whatever its case and however its
spaces run; one that cannot be found makes that text its kinds alone, so no
held value survives in the masked version. An answer that could not be
checked has no `masked_result`.

The addressed member reviews it with `POST .../agent-requests/{request}/review`
and `{"action":"send"}`, `"send_masked"` or `"withhold"`
(`interactions.Service.ReviewAsOwner`). Exactly as for a decision, only their
own signed-in session may: the actor must be marked `"member"`, agent tokens
are `403 forbidden`, the member who asked is `403 forbidden`, and anyone else
`404 not_found`. Only a request in `review` is reviewed (`409 invalid_state`
otherwise, and `409 request_expired` past its expiry). `send` delivers
`held.result` and `send_masked` `held.masked_result` (`409 invalid_state` for
an answer that could not be checked); either completes the request, or fails
it for a held failure. Sending the answer to a request made in a teamspace
reads the teamspace again first, with the member's own token, as approving it
did: one that no longer stands there (the member who asked left its team,
say) has its answer withheld by Oryxa,
`{"action":"withhold","decided_by":{"kind":"system"},"reason":"Oryxa withheld this answer because ..."}`,
and the route answers `409 request_closed` saying why. `withhold` ends it
`withheld`, needing nothing to remain, and the member who asked reads no
result. Either way the held versions are dropped, and the review is
kept as `review`:
`{"action":"send","decided_by":{"kind":"member","user_id":"..."},"decided_at":"..."}`.
A request that expires in review is withheld by the waker's poll
(`Store.WithholdExpired`), or, where no events queue runs the waker, by a
sweep of its own every five seconds (`interactions.SweepExpiredReviews`),
with
`{"action":"withhold","decided_by":{"kind":"system"},"decided_at":"...","reason":"expired before review"}`.
Until a sweep records it, a request in review past its expiry already reads
that way to both members, with its expiry as `decided_at`, and cannot be
reviewed (`409 request_expired`).

The addressed member reads all of it in their own signed-in session. Their
agents read that an answer is held, and what for (`guard`), but never `held`:
no run carries the held texts into a model call, its thread or a trace. The
member who asked, and their agents, read a request in `review` as its status
alone (no `result`, `guard` or `held`), a `withheld` one as its status and
`review`, and a delivered one with its `result`, `guard` and `review`; never
`held`.

Each step is the ordinary `agents.interaction.completed.v1` event, carrying
`review`, `withheld`, or `completed` or `failed` as its status, and nothing of
the guard or of any text. `review` wakes nobody: the member reviews in
person. `withheld` wakes the requester's agent, told the answer was withheld
and that there is nothing to read. Nothing else touches a request in review:
the approval's delivery is settled without a run, since the ledger left
`approved`; giving up on approved work, and closing approvals that expired,
touch only `approved` requests.

Nothing the guard found is logged or traced as a value: Bedrock's `match`
holds the raw value by design, and neither it nor any text is ever logged,
published or traced. ADK traces every tool call's arguments and response on
its `execute_tool` span, whatever `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT`
says, and a failed call's error message on the span's status and its exception
event. That would put an answer the guard has yet to read, from
`interactions_respond`'s arguments, into X-Ray, and just as much the mail,
documents and pages every other tool reads for the member. So the process's
trace exporter redacts every tool span, not the `interactions_` tools' alone,
the span of merged calls included (`observability.RedactToolCalls`). The
arguments, the response, an error's message and any attribute not on a short
allowlist of names, ids and kinds are replaced by a marker, with the size of
the arguments and of the response beside it. A tool span keeps its tool's name,
its status and its timing. The exporter also leaves the query string and the
fragment out of every URL on any span, since a directory search puts the
member's words in `?q=` and the client span of that call carries the URL: it
exports the scheme, host and path, and `[redacted]` where a query was. The
guard logs reasons and kinds, and a failed call by its error code alone, since
the error's message could quote what it was sent.

What an agent says in a room is read the same way before the room stores it
(`rooms.Relay.Guard`), since the whole team reads it; what people say there is
their own and is not read. A room has nobody to review an answer before the
team reads it, so nothing there is held: an answer with a held kind is said
with every such value replaced by its kind, as `{EMAIL}`, and one that could
not be checked is not said at all, a fixed line saying so in its place.

What an agent publishes to a teamspace's ledger is read the same way before
the team reads it (`teamspaces.Service.CollaborationPublish`, which
`teamspaces_publish` calls; the guard is given with `teamspaces.WithGuard`).
It uses `CheckWith`, under the member's peer level and the same effective
workspace relevance/regulated settings and deployment ceiling as the check
route (`WithGuardPolicy`). Actual attempted tool names are recorded before
execution on the run's context, never from `tools_used` in the model's
publication. A chat uses the actual user message (the original textual
request when resuming confirmations), with at most six earlier member
messages as context. A side run's prompt also holds shared, untrusted words,
so the Worker sends its clean question and bounded shared context separately
as `publication_request`. Go accepts that content only on a fresh owned
side-origin run, validates its bounds, and keeps it as a serialized string
under `publication_guard_request` in the existing private ADK session state.
Confirmations restore that original value and cannot supply a replacement.
This is metadata, not a table/schema migration, and thread detail never
exposes it. An older side confirmation without it has no authorized Request
and adds the unknown-source marker; the wrapped prompt is never a fallback.
Private agent replies and tool results are never context sent to this judge;
the side context may include peer-agent replies already shared there.
Prior own-thread content and nonempty shared side context both add
`thread_history`, including a first side run with no earlier ADK events.
No additional transcript content is retained. Missing or
overflowed provenance adds `run_sources_unrecorded`, so it cannot bypass the
private-source gate. Tool provenance is bounded, held only for the run and
never stored in thread state. Provider-tool reads behind an agent transfer are
conservatively marked unrecorded. `interactions_get`, whose harmless
classification assumes a pinned delivery, is conservatively marked too:
general owner runs may read other requests. A relevance hold or failed judge
stores no entry. When private sources require judging, the ledger also
requires a nonempty request within the judge's 10,000-rune cap and all six
publication texts together within its 24,000-rune answer cap: it never stores
an omitted middle as though the judge read it. Missing or oversized input is
unchecked without a model call, holds under `hold`, records would-hold under
`shadow`, and leaves `off` unchanged. Rooms still use their existing masking
check without relevance, and
direct member/room-lifted publications still bypass this agent-only path.
It reads the publication as the ledger would store it, so one the ledger would
refuse for what it is (no title, an unsafe artifact location) is refused
before a check is spent on it: the title and the summary, and the artifacts'
names, URIs and media types and the tools used, each list as one text with a
line to an element. What may be shared is the rule a room applies
(`answerguard.Verdict.Shareable`, one function for the room, the ledger and
the check route below): nothing found, as it was; only kinds that are always
masked, masked; a held kind, its value replaced by its kind as `{EMAIL}`, since
a teamspace has nobody to review a publication before the team reads it. The
entry stored is that one, and the tool's result carries what was found beside
it as `guard`, `{"state":"masked","kinds":["PASSWORD"]}`, which is absent where
no guard runs and on every entry read back from the ledger. A publication that
could not be checked, or whose lists no longer hold the lines they were given,
is not stored at all, and the tool fails saying it couldn't be checked, so it
wasn't shared. What a member publishes is their own words and is not read:
`POST .../entries`, and a room message lifted to the ledger, go through
`PublishEntry`, which never asks the guard.

### How strictly the guard reads an answer

The guard reads an answer at one of two levels, and each member chooses their
own (`internal/guardpolicy`). `secrets` masks only what is always masked
(`answerguard.MaskedKinds`, the list above) and leaves an email address, phone
number or address as written; `strict` also holds those
(`answerguard.HeldKinds`). There is no level below `secrets`: what is always
masked is masked whatever anybody chooses.

A level is chosen for each origin. `owner` is an answer the member started
themselves and reads themselves; `peer` is an answer another member reads,
which their agent gave. A member who chose nothing gets `secrets` for `owner`
and `strict` for `peer`. What applies is the stricter of the organization's
minimum for that origin and the member's choice, or the default when they made
none. A choice below the minimum is kept, and shown, and the minimum wins; the
choice applies again if the minimum is lowered. The organization's minimums,
relevance mode and regulated status come from the host's workspace policy
provider through `guardpolicy.NewWorkspacePolicies`, and are never kept here.
A host without the workspace schema uses `policy.Noop`; a successful empty
policy also supplies defaults, with no extra minimum. A policy read failure
instead applies `strict` floors for both origins and regulated handling, with
relevance still under the deployment's ceiling. It is logged once for each
organization's outage; a cancelled caller is not logged as an outage. Saved
choices resume when the policy can be read again. These settings also apply to
agent-request answers while the Slack consent loop is off. A level this code
does not know is read as `strict`.

```text
GET /v1/organizations/{org}/answer-guard/preferences
PUT /v1/organizations/{org}/answer-guard/preferences
{"owner": "secrets" | "strict" | null, "peer": "secrets" | "strict" | null}
```

Both answer with what the member is told about their settings:

```json
{"owner": {"chosen": "strict", "default": "secrets", "floor": "secrets", "effective": "strict"},
 "peer":  {"chosen": null, "default": "strict", "floor": "strict", "effective": "strict"},
 "relevance": "hold", "regulated": false}
```

`chosen` is what the member chose, `null` for nothing; `floor` is their
organization's minimum; `effective` is what applies. A `chosen` below `floor` is
accepted and `effective` is `floor`, so the three differ. On a `PUT` a key left
out leaves that setting as it is, `null` clears it back to its default, and
`{}` changes nothing. A key that is not exactly `owner` or `peer`, a value that
is not exactly `"secrets"`, `"strict"` or `null`, and a body that is not one
small JSON object (4 KiB at most) are `400 invalid_request`, and nothing is
changed. They are the member's own, as a preference is: an agent's token is
`403 forbidden`, no credentials are `401 unauthorized`, a cookie needs an
allowed origin to change anything, and the caller has to be a member of the
organization. Only that member's settings are ever read or changed; nothing in
the path or body names whose they are. A server with no settings kept answers
`503 answer_guard_unavailable`.

`relevance` is how the relevance check (see [The relevance check](#the-relevance-check))
treats an answer that carries private material its question did not ask for:
`hold`, `shadow` (run and record, never hold) or `off`. It is the
organization's own, `hold` when it has none, held to the deployment's ceiling:
`AGENTS_PRIMARY_GUARD_RELEVANCE_CAP`, `shadow` unless set, so the check holds
nobody's answers until a deployment says it may, except for the organizations
`AGENTS_PRIMARY_GUARD_HOLD_ORGS` lists, a comma-separated list of organization
ids. A deployment therefore starts by watching, holds for the organizations it
names once the check has earned it, and then raises the ceiling. `regulated`
says the organization runs the regulated preset, for which the relevance check
also holds health, HR and pay details the question did ask for.

There is no `off` level for personal data, as there is no level below
`secrets`; `off` is only a mode of the relevance check, which holds or records
what the personal data read never looks at.

A member's levels are kept in `agents_primary.guard_preferences`, no row
meaning the defaults, and every change of a level is appended, in the
transaction that made it, to `agents_primary.guard_preference_changes`: the
field, what it was, what it became and when. Two changes to one member's levels
take turns, so each logs what it really replaced. Both are forgotten with the
member, or with the organization: `guardpolicy.Service.Forget` runs beside the
threads' and wake-ups' cleanup. If a member's own levels cannot be read, the
level that applies to their answers is `strict`, with the error.

### The relevance check

Personal data is read by its shape. Whether an answer carries private material
its question did not ask for, such as the reason for a no when only a yes or no
was asked, takes reading the question and the answer together, and is the
relevance judge's (`internal/relevance`, which documents how it decides and how
it is measured). The answer guard runs it after the personal data read, from
`Guard.CheckWith`, for the answers a caller asks it to: the check route below,
and the answers the request ledger records (`interactions.answer`, told the
request in its asker's words and the toolboxes the run could read, and run in
the mode the answer's owner's settings say). `Guard.Check`, which every other
place that reads an answer calls, never runs it.

- **The gate.** A judge runs only for an answer whose `sources`, the tools the
  run called from what the run recorded, may have read something private
  (`relevance.NeedsJudging`); a tool nobody has classified counts as private.
  With none, or only tools that read nothing private, nothing is judged and no
  model is called.
- **The judge.** One call to `AGENTS_PRIMARY_MODEL_LITE` through a Gen AI client
  on the agents' own project, location and credentials, with no retries and a
  6 second bound, shown the texts as the agent gave them. It names categories
  and nothing else; whether to hold is decided in code (`relevance.ShouldHold`),
  applied by the guard itself from what the judge found: for any extra, for a
  judge that could not tell, and, in a regulated organization, for health, HR
  or pay that was asked for. Without a guardrail it does not run, since the
  check lives in the answer guard, and startup says so. A client that cannot be
  built stops the service rather than start without the check.
- **The modes.** In `hold`, an answer the check would hold is held for its
  owner: `state: "held"`, `kinds` with `OFF_TOPIC`, the categories of what it
  carried (`personal_life`, `health`, `hr`, `compensation`, `others_private`,
  `credentials`) and `UNCHECKED` when the judge could not tell, beside any kinds
  the personal data read held it for, and nothing to share at any intensity:
  never a masked version of an answer that is off topic. In `shadow`, the
  answer is exactly what the personal data read made of it, and the check only
  records what it would have held. In `off`, no judge is asked.
- **What is logged.** One line for each answer a judge read, with what
  `relevance.LogFields` names (the decision, its categories, the model, the
  latency, whether anything was cut, why it failed), the mode, whether the
  answer was held, and the organization and member: a warning when the judge
  could not tell. Never a text, the request, the context or a source.

### Checking an answer before it is shown

A side conversation shows a member what a teammate's agent answered. The web
app has the agents service read the answer first, and shows what it may:

```text
POST /v1/organizations/{org}/answer-guard/check
{"texts": ["..."], "origin": "peer", "request": "...",
 "sources": ["tool", ...], "context": ["line", ...]}
```

It is the member's own call, as every route that changes or spends something
is: a cookie needs an allowed origin, an agent's token is `403 forbidden`, no
credentials are `401 unauthorized`, and the caller has to be a member of the
organization. `texts` is 1 to 8 texts of at most 25,000 characters each.
`origin` is optional and exactly `"peer"`, the default, or `"owner"`; `request`
is optional, at most 25,000 characters, and says what was asked; `sources` is
optional, at most 64 names of at most 128 characters, the tools the run that
gave the answer called; `context` is optional, at most 6 lines of at most 1,000
characters of the conversation around the request. The three are for the
relevance check (see [The relevance check](#the-relevance-check)) and are read
by nothing else: none is kept, sent to the guardrail or logged. Anything else,
an unknown field included, is `400 invalid_request`. The body may run to about a
megabyte, since eight texts of 25,000 wide characters are more than the 256 KiB
every other route reads.

The answer is `{"checked": true, "state": "clear" | "masked" | "held",
"kinds": [...], "shareable": [...] | null, "relevance": {"judged": false,
"would_hold": false, "held": false}}`. `shareable` is one text for each text
sent, or `null`, meaning share nothing. `kinds` are entity names, never values,
and `["UNCHECKED"]` when the guard could not read the text, which shares
nothing, as does a verdict that does not add up. `relevance` says whether a
judge read the answer (`judged`), whether the check would hold it (`would_hold`,
recorded in `shadow` too) and whether it did (`held`), and nothing of what the
judge found: an answer held for relevance is `state: "held"`, `shareable:
null` and `kinds` with `OFF_TOPIC` and the categories it carried, whatever
intensity it is read at, so `held: true` always comes with those. The mode comes from the
caller's settings (`relevance`, above): `off` where no settings are kept, so a
check that sends no `sources`, or is made in an organization whose mode is
`off`, or with no judge configured, is `{"judged": false, ...}`. With no
guardrail configured nothing is checked, and it says so: `{"checked": false,
"state": "clear", "kinds": [], "shareable": [the texts as given], "relevance":
{"judged": false, "would_hold": false, "held": false}}`.

How much is taken out follows the caller's own settings for the `origin` (see
[How strictly the guard reads an answer](#how-strictly-the-guard-reads-an-answer)):
the level that applies to them, which is what they chose, or the default, and
never below their organization's minimum. It is read as an intensity
(`answerguard.Intensity`) in one place (`checkLevel`), so that the route does
not change when a level does. Only the caller's own settings are read, in the
organization in the path, and only for a check that reads an answer: one that
is refused, over the limit, or made with no guardrail configured costs no read.
What a member who chose nothing gets, which is also what every member gets
where no settings are kept, is:

| `origin` | Intensity | Masks | Leaves as written | `state` and `kinds` |
| --- | --- | --- | --- | --- |
| `"peer"`, or none | `strict`, what `Verdict.Shareable` does | Kinds that are always masked, and held kinds as `{EMAIL}` | Nothing | As the guard found: `held` with the held kinds when there are any, `masked` with the masked ones |
| `"owner"` | `secrets` | Only kinds that are always masked (`answerguard.MaskedKinds`: passwords, AWS keys, card, bank and government numbers) | `EMAIL`, `PHONE` and `ADDRESS` | Only what was acted on: `masked` with the always-masked kinds, else `clear` with `kinds: []` and the texts as given |

A member who chose `strict` for `owner` has their own answers read as another's
are, an email address held and shared as `{EMAIL}`; one who chose `secrets` for
`peer`, where their organization's minimum allows it, has what others read left
with its email addresses, as the second row has. A minimum overrides a choice
below it. No level reads nothing: the intensity `off` remains in
`answerguard`, but no setting reaches it, and a level nobody knows is read as
`strict`. If the member's levels cannot be read the check does not fail, and
does not read looser: it reads at `strict`, and logs a warning naming the
member, the origin and why, never a text. The information log of a check that
found something names the intensity it was read at.

By default `"owner"` is an answer the member started themselves and reads
themselves, so their own personal data is not masked in it. An answer with only
an email address in it comes back `clear`, and one with an email address and a
password comes back with the password masked and the address intact. Even
there an answer the guard could not read shares nothing, since what could not
be read may hold a secret. The guard reads every text the same way; the
verdict records the texts as they were given and every always-masked kind it
found beside a held one (`Verdict.Given`, `Verdict.SecretKinds`), which is what
`Verdict.At` needs to say what a level comes to. A guardrail that left an
always-masked value in a text (it is no longer set up as it should be) is held
for it, and at `secrets` the answer is then shared as its fully masked version,
held values included, since the guard keeps no version with only the secrets
masked.

A member may make 30 checks a minute, each counted against them in this
process, in the organization they are in. The thirty-first is `429
rate_limited` with `Retry-After`, and never reaches the guard; a member held to
the limit is let in again as their oldest check leaves the minute. A request
that was refused before it was read, malformed or not the member's to make,
counts for nothing, and the limit holds with no guardrail configured too, so
what development lets through is what production will. What was found is
logged by kind, never by value, and no text and nothing of `request`,
`context` or `sources` is ever logged.

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
person or standing in for them. That distinction does not change what the
turn may reach. Every turn in a room answers from the room alone, on the
room's own agent with no memory (`managedagents.RoomSeat`,
`assembly.Krude.Room`): none of the member's apps, knowledge or requests, no
web research, helpers or delegation, whether its person is here or away. What
it cannot answer from the room it files with its member, `room_ask_owner`, as
a request from the member whose message started the exchange, made in the
room, so it is held for the member unless they let that agent decide there.
Everything an agent says in a room passes the answer guard before it is
stored (see [Answer guard](#answer-guard)), and only the team's members read
or write in a room: an organization admin who is not on the team is refused.

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
author id from the model, after the answer guard has read what it publishes
(see [Answer guard](#answer-guard)). Specialists lose this view immediately
when their contribution is withdrawn; primary participation follows live Teams
membership. A run pinned to one teamspace (see
[Pinning a run to a teamspace](#pinning-a-run-to-a-teamspace)) sees, reads and
publishes in that teamspace alone.

Contributing a specialist never transfers its identity, instructions, app
connections, or tool permissions. The specialist still runs as its owner.
`delegation_mode` is the owner's standing answer to whether teammates may ask
it for work here without them, and the request ledger enforces it for every
cross-user request made in the teamspace (`teamspace_id`): a `request`
specialist's requests are held for its owner, who approves or rejects each in
their own session before the specialist is woken, while an `automatic`
specialist decides them itself. A request made in the teamspace to a
teammate's primary agent is always held for its owner. The mode is read again whenever a request is decided or its agent
woken, so switching to `request` holds requests the specialist has not
decided, or has approved but not started. A request made outside any
teamspace is held for its owner whatever the mode (see
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
before their agent registry is cleaned up. A `teams.member.removed.v1` event
withdraws what the person contributed to the team's teamspace and, when the
team has one, records `agents.teamspace.member.removed.v1`, whether or not
they contributed anything, so their side conversations there are purged. The
whisperer roster needs no cleanup because it is derived from Teams on every
read.

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
| `invalid_request` | 400 | The body is not one of the two shapes, or answers something that is not waiting, or its `teamspace_id` is blank, holds whitespace or is over 128 characters |
| `unauthorized` / `forbidden` | 401 / 403 | Auth does not know the caller, or they are not in the organization; an agent's token; a cookie from another origin |
| `not_found` | 404 | Not the member's thread in this organization |
| `run_in_progress` | 409 | Another run is answering the thread |
| `confirmation_pending` | 409 | A message while a tool waits for the member's answer |
| `agents_unavailable` | 503 | This server has no Google Cloud project to run Krude on |

Either body may also carry `screen_context`, what the member had open when
they sent it, so that Krude, or a specialist, can read "this" and "here" with
`get_current_screen` (in the member's own runs only, never an unattended, room
or delivery run):
`{"view": "team/teamspace", "selection": {"kind": "teamspace", "id": "...", "name": "..."}, "visible_items": [{"kind": "...", "id": "...", "name": "..."}]}`.
The `view` is `home`, `team` or `agents`, a slash and a section; at most twelve
items are visible; an item's `kind` holds at most 40 characters, its `id` (its
handle) and `name` at most 120, and none of them a line break. Hints point the
agent at things and authorize nothing: every tool still checks the member. So a
hint that breaks a rule is left out and the run goes on with the rest, rather
than the member's message being refused; the log says how many were left out
and never what they said, and a `view` that is no Oryxa page leaves the whole
snapshot out. The snapshot is ADK temporary state (`temp:`), for that one run:
it is not stored with the thread, and a run without one finds none.

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

**Deploys.** Every run in the process is tracked by one `threads.Drain`. When
the process is told to stop, the host calls the agents service's `Drain`
first (`ORYXA_DRAIN_TIMEOUT`, 20 s by default), alongside draining requests
and before stopping workers:
- no new run starts: `StartRun` answers `threads.ErrDraining`, which the API
  serves as `503 restarting`;
- runs already going finish as they would have;
- a run still going at the deadline is stopped with `threads.ErrInterrupted`.
  It is recorded `interrupted` (not `failed`), its `agents.run.failed.v1`
  says `interrupted: true`, and its stream ends `run_failed` with
  `interrupted: true` and "Oryxa restarted while Krude was working on this…
  Send your message again."

The interaction waker and the wake-up waker give an interrupted or refused
run's work back uncounted, so the next process runs it again. The drain and
the rest of shutdown must fit the container's ECS `stopTimeout`.

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

### Pinning a run to a teamspace

A side conversation runs the member's own agent on a prompt that names the
teamspace it is about. The prompt alone does not tie the run to it: without a
pin, `interactions_create` passes whatever `teamspace_id` the model names
straight to the request ledger, and a model that named another teamspace, by
mistake or because words a teammate wrote in the prompt told it to, would ask
that teamspace's agents for work, and for an `automatic` specialist there
without its owner's approval. So the run can be pinned.

`POST .../threads/{thread}/runs` takes an optional `teamspace_id`: not blank
after trimming, at most 128 characters, and no whitespace inside, or the run is
`400 invalid_request` and never starts. A blank one is refused rather than
read as none, since a run that was meant to be pinned and quietly is not would
answer as though it were; left out, or `null`, the run is not pinned, and
behaves exactly as it always did. It works with a message or with answers to
confirmations, and it pins that run and no other: the thread's next run has
none unless its caller sends one again. The agent-run route
(`.../agents/{agent}/runs`) takes none.

The pin travels on the run's context beside its origin
(`agents.WithPinnedTeamspace`, `threads.RunInput.TeamspaceID`). It is never
written to the thread's state, the run's record or any event, and the model can
neither read nor write it. In a pinned run:

- `interactions_create` files its request in the pinned teamspace when the model
  names none, or names that one (however padded); one that names another is a
  tool error, and nothing is filed, before a token is even asked for;
- `teamspaces_publish` and `teamspaces_entries` do the same with their
  `teamspace_id`, and `teamspaces_context` looks only among the pinned teamspace,
  so a query that names another finds nothing, and one that names none finds the
  pinned one though the agent may work in several;
- `teamspaces_list` shows the pinned teamspace alone, an empty list when the
  agent has no seat in it.

The refusal says the conversation is about one teamspace and never which, so
the pin is not something the model can learn by asking. The pin grants nothing:
it only narrows what the model may name, and the request ledger and the
teamspace service still check the live seat and team membership of the member
the run acts for, exactly as they do without one.

## Events agents consumes

The agents service reads its own queue, `AGENTS_EVENTS_QUEUE_URL`, which `go
run ./agents/primary/infra/events/cmd/provision` creates with the rule that
fills it (`internal/inbound`):

| Detail type | What agents does |
| --- | --- |
| `batching.trigger-batch.ready.v1` | Keeps the batch's triggers, all at once, to wake Krude for its member (see [Wake-ups](#wake-ups)) |
| `agents.interaction.requested.v1` | Persists a delivery job for the addressed target agent; one held for its owner completes without waking it. For a request asked in Slack it also wakes the surfaces worker, which says what the ledger says of it in Slack |
| `agents.interaction.decided.v1` | Wakes the target to finish approved work, or the requester to observe a rejection; the surfaces worker settles the owner's notice |
| `agents.interaction.completed.v1` | Wakes the requester to observe a completed, failed or withheld result; an answer held for review (`review`) wakes nobody, and the surfaces worker still says in the thread that it is held |
| `apps.slack.message.received.v1`, `apps.slack.action.received.v1` | Each is a job for the Slack router: a mention that asks, and a click on a button. They carry ids alone, and the delivery is read back from apps |
| `apps.slack.install.changed.v1` | Only when its state is `ended`: forgets the workspace (see [The Slack consent loop](#the-slack-consent-loop)) |
| `auth.member.removed.v1` | Removes the member's threads, waiting wake-ups, agent definitions, answer guard settings, and cross-user request content in that organization |
| `auth.organization.deleted.v1` | The same for everyone in the organization |
| `teams.member.removed.v1` | Withdraws the person's contributions to the team's teamspace, and records `agents.teamspace.member.removed.v1` when the team has one |
| `teams.team.deleted.v1` | Removes the team's teamspace, recording `agents.teamspace.deleted.v1` |

The three `apps.slack.*` events are subscribed to whether the loop is on or
not, since the rule that delivers them is provisioned apart from any one
process's switches. With `AGENTS_PRIMARY_SLACK_CONSENT` off a process
acknowledges and drops them. **Provision the rule only after the release that
knows them**, with `go run ./agents/primary/infra/events/cmd/provision` or
`deploy/scripts/provision-app.sh`: a consumer that did not know one fails it,
and the queue moves it to the dead-letter queue after five receives. Until the
rule is widened the events are simply not delivered, and nothing fails.
`agents.interaction.lease.revoked.v1` and `agents.interaction.secret.viewed.v1`
are published for whoever wants them, and no service subscribes: a revoked lease
says nothing in Slack until the asker asks again, and a secret is opened only in
Oryxa.

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
size, slow for a large deployment, where a deletion could outlast the five
minutes the consumer holds a message for and run twice (harmlessly, since
forgetting is repeatable).

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
  their own thread. A turn in a room is answered by the room's own agent even
  when its member is reading the room (see
  [A teamspace is a room](#a-teamspace-is-a-room)).
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
completed without a run, since no run nobody watches decides a request; an
`approved` delivery is complete only after it records a final result. The
approval arrives as the `approved` delivery, whose prompt says the owner
approved it when they did. Rejected and completed deliveries notify the requesting
agent. Jobs lease across replicas, retry failed runs three
times, settle recorded revisions in order per request and recipient, and keep handled IDs
for a week. If an earlier run already advanced the interaction, a later or
repeated delivery is settled without running the agent again.

Approved work is never left approved with nobody working on it. When the waker
gives up on an `approved` delivery (its runs failed three times, not counting
a run the waker's own stop cut short, or its agent no longer exists) it closes
the request, failed, with why as the result's summary; and every poll closes,
the same way, the approved requests whose expiry passed unanswered in the last
hour, since none can be answered any more, whatever became of their delivery
(`interactions.Store.CloseApproved` and `CloseExpired`, which fail an approved
request as `Close` does). Each is the ordinary completed event, so the member
who asked hears of it as of any failed result, and a side conversation shows
it as the agent's failure with the reason. Nothing else is closed: a new
request whose deciding run was given up stays pending, for its member to
decide or to expire, and a failed report to the member who asked changes
nothing about the request. An agent its member disabled may be turned on
again, so a delivery asking it to decide or to work is not given up: it waits
for the agent, tried again every minute without counting, until the request's
expiry ends the wait. A report to a disabled requester's agent is dropped, as
it always was.

The poll closes an approval seconds after it runs out, so the hour is only for
the time nothing polls: a deploy, a restart or an outage. An approval that ran
out before that, every one that expired before this was released among them,
is left as it was: it reads as `expired`, as it always has, with no event, and
its `updated_at` does not move, so an old request keeps its place in every
listing and wakes nobody.

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

**Thinking on a delivery.** `AGENTS_PRIMARY_DELIVERY_THINKING` asks the model
for a thinking level on every call of a run that delivers an agent request
(see [Agent interactions toolbox](#agent-interactions-toolbox)): `minimal`,
`low`, `medium` or `high`, in any case. That is how Gemini 3 is told how much
to think, genai's `ThinkingConfig.ThinkingLevel`, set on the delivering
agents' `GenerateContentConfig`, which ADK copies into each of their
requests. A token budget (`ThinkingBudget`) is not offered: it is Gemini
2.5's control, Gemini 3 is tuned by level, and a request may not carry both.
Unset, no request carries a thinking config and each model thinks as it does
by default, as before the setting existed: `gemini-3.8-flash` at medium,
`gemini-3.5-flash-lite` at minimal. A member's own chat, an app wake-up, a
turn in a room, a team room, a workflow and the organization agent never
carry it. Krude answers a request on the main model and a specialist on the
lite one, and Vertex refuses a level a model does not take with `400
INVALID_ARGUMENT`, so the service refuses to start on anything but a level,
and on a level Google documents either model as not taking:
`gemini-3.8-flash`, `gemini-3.7-flash` and `gemini-3.1-pro-preview` take no
`minimal`, and `gemini-3-pro-preview` only `low` and `high`.

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
| `agents.interaction.completed.v1` | The target completed or failed an approved request; its answer was held for review (`review`); or that review ended it: `completed` or `failed` once sent, `withheld` when withheld |
| `agents.interaction.lease.revoked.v1` | An owner ended, before it ran out, the access they gave an asker in one Slack thread: identities and the thread's ids, never a question or an answer |
| `agents.interaction.secret.viewed.v1` | The member who asked opened the one-time secret shared for their request: who and when, never what it held |
| `agents.teamspace.room.message.posted.v1` | A teamspace's room stored a message, a person's or an agent's answer, without its body ([A teamspace is a room](#a-teamspace-is-a-room)) |
| `agents.teamspace.member.removed.v1` | A person left a teamspace: Teams removed them from the team it is bound to, whether or not they had contributed agents to it |

A person leaving a teamspace carries identities only: `organization_id`,
`teamspace_id`, `team_id`, `user_id`, and `removed_at`, when the team
membership ended, which is the Teams event's `removed_at`, or when that event
happened if it has none. It is recorded in the transaction that withdraws the
person's contributions, and only for a team that has a teamspace. A Teams
event delivered again records it again, under a new `event_id` but with the
same `removed_at`, and its consumers must take that as a repeat. Realtime
consumes it, and
`agents.teamspace.deleted.v1`, to have the web app purge the side
conversations the person had in that teamspace, or all of the deleted
teamspace's (`realtime/README.md`).

A request's three events carry who it is between, its `status` and
`revision`, and, since the owner-approval fields reached them, where it was
made and who decides it: `teamspace_id`, left out for a request made outside
a teamspace; `approval`, `agent` or `owner`, always there, since every request
has one (the column is `NOT NULL DEFAULT 'agent'`, so a request stored before
approvals were recorded has `agent`); and `decided_by`, as the API's
`decision.decided_by` shows it (`{"kind": "member" | "agent" | "system" |
"lease" | "bypass", "user_id"?, "agent_id"?, "surface"?, "reason"?}`), once the
request is decided, left out before, and for a decision an older build recorded
without its decider. A request made from Slack also carries `ask_id` (the ask
its message made), `origin` (`slack`; `agent` for every other, and left out of
an older build's), and, once approved, `consent` (`summary` or `access`). An approval
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

### Document editing

Oryxa opens conversation artifacts for viewing first inside the primary agent's work panel. The viewer renders PDF, DOCX, PPTX, XLSX, CSV, Markdown, text and images in the browser without ONLYOFFICE chrome, and reads at most 25 MB of a file to do it (a larger one says so, and is downloaded instead). The file-header Edit button opens [ONLYOFFICE Docs](https://api.onlyoffice.com/docs/docs-api/get-started/basic-concepts/) for **DOCX, XLSX and PPTX**, the formats Docs returns as it was given. Every other type (DOC, ODT, RTF, XLS, ODS, CSV, PPT, ODP, PDF, text) is for viewing and downloading: Docs converts or reworks those when it saves, and a converted file cannot be written back as the file it was, so the editor route answers `mode=edit` for them with `415 unsupported_document` and no callback saves one. The editor gets a signed, expiring URL for the member's exact file version and sends a JWT-signed save callback. Oryxa saves the edited bytes as the next artifact version, typed as the version it replaces; an older editor cannot overwrite a newer version. The callback must return the original file type, so a failed conversion cannot corrupt a file under the wrong extension.

Two secrets are needed, each at least 32 bytes and different from the other (`openssl rand -hex 32` makes one), and neither may be the sample secret this README and `.env.example` once printed:

- `AGENTS_PRIMARY_DOCUMENT_JWT_SECRET` is Docs' own `JWT_SECRET`. It signs the editor's configuration, which expires with the session it opens (a day at most), and verifies each save callback, which Docs signs with an expiry: a callback without one is refused.
- `AGENTS_PRIMARY_DOCUMENT_TICKET_SECRET` is Oryxa's alone and is never sent to Docs. It signs the two URLs Docs is handed: one to read the file (good for an hour) and one to save through (good for a day). A ticket reads or replaces a member's file, so a secret Docs holds must not be able to make one.

Without both, or with one shorter, alike or published, the editor answers `503 document_editor_unavailable` before it reads anything, honours no ticket, and the server logs why once at start-up (`document editing is off`), naming the variable and never its value.

For local development, put the secrets in `.env`, run `docker compose --profile documents up -d documents` from the repository root and start Go with the same `.env`:

```sh
# Docs' JWT_SECRET, which docker-compose.yml reads, and the same value for Go
ORYXA_DOCUMENT_JWT_SECRET=<openssl rand -hex 32>
AGENTS_PRIMARY_DOCUMENT_JWT_SECRET=<the same value>
# Oryxa's own, a second value
AGENTS_PRIMARY_DOCUMENT_TICKET_SECRET=<another openssl rand -hex 32>
AGENTS_PRIMARY_DOCUMENT_SERVER_URL=http://localhost:8082
AGENTS_PRIMARY_DOCUMENT_SERVER_INTERNAL_URL=http://localhost:8082
AGENTS_PRIMARY_DOCUMENT_API_URL=http://host.docker.internal:3001
```

The compose service has no secret of its own to fall back on, and binds `127.0.0.1` only: started without `ORYXA_DOCUMENT_JWT_SECRET`, Docs makes up a secret Oryxa cannot sign with, and the editor reports that it cannot open the document.

`AGENTS_PRIMARY_DOCUMENT_API_URL` must be reachable **from the Docs container**; the example uses the Vite `/v1/` proxy. `AGENTS_PRIMARY_DOCUMENT_SERVER_URL` must be reachable from the member's browser. Use HTTPS for both public URLs in a deployed environment. The Community Edition compose service is for local evaluation; [ONLYOFFICE's licensing FAQ](https://helpcenter.onlyoffice.com/docs/faq/docs-community.aspx) states the AGPL terms for SaaS integrations and says Community Edition has no mobile web version. Production needs an edition and license compatible with Oryxa's distribution and phone editing requirements. Docs is a separate service with its own capacity requirements; it is not included in the existing production Fargate task.

| Variable | Default | Purpose |
| --- | --- | --- |
| `AGENTS_PRIMARY_MODEL` | `gemini-3.5-flash-lite` | Main model: conversation and grounded research |
| `AGENTS_PRIMARY_MODEL_LITE` | `gemini-3.5-flash-lite` | Lite model: planning and bulk single-turn work, and the answer guard's relevance judge (see [The relevance check](#the-relevance-check)) |
| `AGENTS_PRIMARY_DELIVERY_THINKING` | unset | Thinking level asked of the model on every call of a run delivering an agent request, and no other run: `minimal`, `low`, `medium` or `high`. Unset, each model thinks as it does by default. A level either model does not take refuses to start (see [Models and routing](#models-and-routing)) |
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
| `AGENTS_PRIMARY_LIVE_BACKEND` | `agentcore` | Where the sandbox and browser tools run: `agentcore`, or `cloudflare` (the oryxa-edge Worker, docs/design/live-layer) for every organization |
| `AGENTS_PRIMARY_LIVE_CLOUDFLARE_ORGS` | unset | Organization ids, comma-separated, whose sandbox and browser run on Cloudflare while the backend above is still `agentcore`: the rollout |
| `AGENTS_PRIMARY_EDGE_URL` | unset | The oryxa-edge Worker, such as `https://edge.oryxa.in`; required, with `AGENTS_PRIMARY_SERVICE_KEY_ID` to sign edge tokens, when any organization runs on Cloudflare |
| `AGENTS_PRIMARY_EDGE_TOKEN_PUBLIC_KEYS` | unset | JSON array of the base64 DER SPKI P-256 keys the edge signs edge→agents tokens with (more than one while it rotates). Set, it turns on `POST /v1/edge/dispatch`, where a call's voice agent hands work over; the answers go back to `AGENTS_PRIMARY_EDGE_URL` with edge tokens `AGENTS_PRIMARY_SERVICE_KEY_ID` signs, so both are required with it |
| `GOOGLE_CLOUD_PROJECT` | unset | Project whose Vertex AI serves Gemini; the agents require it |
| `GOOGLE_CLOUD_LOCATION` | `global` | Vertex AI location: `global`, or the `us` or `eu` multi-region |
| `GOOGLE_APPLICATION_CREDENTIALS` | unset | ADC credential file; locally `gcloud auth application-default login` supplies ADC |
| `GOOGLE_WORKLOAD_IDENTITY_PROVIDER` | unset | On AWS: the workload identity pool provider's audience that admits the process's role. Set with `GOOGLE_SERVICE_ACCOUNT`, it replaces ADC |
| `GOOGLE_SERVICE_ACCOUNT` | unset | On AWS: the service account the federated role acts as, which holds the Vertex AI roles |
| `APPS_URL` | unset | The apps service's own URL; when set, Krude gets the apps toolbox (see Acting for a member), and the Slack consent loop posts through its internal Slack API. In the binary it defaults to this process when it runs apps, which is the loopback address the loop needs; it must be HTTPS or loopback HTTP |
| `RAG_URL` | unset | Rag's own URL; when set, Krude gets the knowledge toolbox (see Knowledge toolbox). In the binary it defaults to this process when it runs rag |
| `KNOWLEDGE_URL` | unset | Knowledge's own URL; with `RAG_URL`, enables source coverage/freshness and confirmed refresh of existing sources. In the binary it defaults to this process when it runs knowledge |
| `AGENTS_PRIMARY_KNOWLEDGE_MIN_RELEVANCE` | `0` (off) | Reranker relevance below which a knowledge passage is left out, so a question the documents cannot answer comes back empty. On the scale of the reranker rag runs: 0.2 was measured for Google's `semantic-ranker-default`; calibrate before using it with another. No effect without a reranker |
| `AUTH_URL` | unset | Auth's own URL; where a member-absent run exchanges the member's standing grant for an agent token |
| `AGENTS_PRIMARY_SERVICE_KEY_ID` | unset | KMS key agents signs its service tokens with, to ask auth for an agent token on a member's behalf, and to call apps' Slack API. Required with the Slack consent loop on |
| `AGENTS_ALLOWED_ORIGINS` | unset | Origins, comma-separated, whose cookie-authenticated requests may start runs and change threads; the web app's. The first is also the origin the Slack acknowledgement links to |
| `AGENTS_PRIMARY_RUN_TIMEOUT_SECONDS` | `600` | Longest one run may take; 60–3600 seconds |
| `TYPESAFE_API_KEY` | unset | TypeSafe's key, for Jev: a room's judgment of every message, what a request made in a teamspace that omits tools needs (see [Agent interactions toolbox](#agent-interactions-toolbox)), and the screen of what people ask in Slack. Unset, rooms fall back to rules, an omitted request tool list defaults to knowledge, and with the Slack consent loop on every mention is refused and nobody is asked |
| `TYPESAFE_BASE_URL` | `https://api.typesafe.ai` | TypeSafe's API: HTTPS, or plain HTTP to this machine |
| `AGENTS_PRIMARY_GUARDRAIL_ID` | unset | Bedrock guardrail that screens the member's conversation; unset turns screening off |
| `AGENTS_PRIMARY_GUARDRAIL_VERSION` | unset | The guardrail's numbered version, required with its ID; never `DRAFT` |
| `AGENTS_PRIMARY_GUARDRAIL_FAIL_CLOSED` | `false` | Block, rather than pass, what a failed screen could not check |
| `AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID` | unset | Bedrock guardrail the answer guard reads every answer another member will read with (see [Answer guard](#answer-guard)); unset, no answer is checked. Set with its version or not at all |
| `AGENTS_PRIMARY_ANSWER_GUARDRAIL_VERSION` | unset | That guardrail's version, set with its ID |
| `AGENTS_PRIMARY_GUARD_RELEVANCE_CAP` | `shadow` | The most the answer guard's relevance check may do here, whatever an organization's workspace policy asks: `hold`, `shadow` (run and record, never hold) or `off`, in any case. Anything else refuses to start (see [How strictly the guard reads an answer](#how-strictly-the-guard-reads-an-answer)) |
| `AGENTS_PRIMARY_GUARD_HOLD_ORGS` | unset | Organization ids, comma-separated, the ceiling above does not apply to: they may hold. An id is letters, digits, `_` and `-`; anything else, a space-separated list included, refuses to start |
| `AWS_ENDPOINT_URL_BEDROCK_RUNTIME` | unset | Bedrock runtime endpoint override, for a development stand-in of the answer guard's and the consent guard's guardrails |
| `AGENTS_PRIMARY_SLACK_CONSENT` | `off` | `on` or `off`: the Slack consent loop, that is the router, the surfaces worker, the tickets on an owner's buttons and the client for apps' Slack API (see [The Slack consent loop](#the-slack-consent-loop)). On, the ticket key, `AGENTS_PRIMARY_SERVICE_KEY_ID`, `APPS_URL` and `TEAMS_URL` are required, or the service refuses to start. Anything but `on` or `off` refuses to start |
| `AGENTS_PRIMARY_SLACK_LINKED_ONLY` | `false` | Trust only a member's own Slack account link (Sign in with Slack, apps) to say who a Slack user is, never their email: an unlinked asker is refused `not_linked`, an unlinked owner is asked in Oryxa only, and an unlinked account's clicks and replies decide nothing. Off, the email decides and the router logs who would not pass with links alone |
| `AGENTS_PRIMARY_SLACK_TICKET_KEY` | unset | Signs the buttons on an owner's Slack notice: 64 hexadecimal characters, 32 bytes (`openssl rand -hex 32`). Read only with the loop on. Changing it makes every button already posted refuse its click; its owner decides on the request's card instead. Never logged, and never quoted in an error |
| `AGENTS_PRIMARY_SECRETS_KMS_KEY_ID` | unset | KMS key (an ID, ARN or alias) one-time secrets are sealed under, with the encryption context `service=oryxa-agents`. Without it the three secret routes answer `503`, though the store's table is still migrated, swept and forgotten from |
| `AGENTS_PRIMARY_CONSENT_GUARDRAIL_ID` | unset | Bedrock guardrail the consent guard grounds every answer to a Slack question with: contextual grounding and denied topics (`deploy/production/answer_guard.tf`, not the answer guard's guardrail). Set with its version or not at all; without one, every such answer is held as unchecked |
| `AGENTS_PRIMARY_CONSENT_GUARDRAIL_VERSION` | unset | That guardrail's version, set with its ID |
| `AGENTS_PRIMARY_CONSENT_KNOWLEDGE_PASSAGES_ONLY` | `false` | Opt-in passage-only knowledge grounding. Excludes knowledge metadata from grounding, keeps full capture for summary copying and other tool sources, and holds substantive answers with no usable source. Does not change thresholds |
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
