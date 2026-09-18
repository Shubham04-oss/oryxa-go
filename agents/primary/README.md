# Agents primary

Primary is the agentic system in `/agents`: the prompts, tools and
configuration behind every agent interaction. adk-go is its engine and Gemini
its model. The Cloudflare chat layer, `durable-agent`, talks to it over HTTP,
and the supporting services in `secondary` sit beside it.

Status: the engine is in. adk-go v2.4.0 runs Krude, a Gemini assistant with
dedicated sub-agents, dynamic sub-agents, temporary helpers and toolboxes that
load on demand with their own skills; the AgentCore sandbox and browser are
wired today. A model router sends planning and bulk work to the lite model, a
prompt cache layer holds large stable prefixes on Vertex, and the events
outbox, publisher and dispatcher are in with contracts in
`contracts/events/agents/v1`. `cmd/devui` opens it in ADK's development web UI.
The service itself, mounted in the single `oryxa` binary as `agents`, still
serves only `GET /healthz`; the run and stream API, and the wiring of the
events and cache components into it, land next.

## Agents

`internal/agents` defines Krude and everything it works with. `Root`
assembles it:

```text
krude              chat: the member's assistant
├── planner        single-turn sub-agent: turns multi-step work into a plan
├── researcher     single-turn sub-agent: answers from the live web with Google Search and URL context
├── toolboxes      list_tools, load_tools: sandbox and browser tools, loaded on demand
├── subagents      create_subagent, list_subagents, remove_subagent: specialists Krude writes and reuses in a session
└── helpers        list_helpers, spawn_helper: temporary agents for big or separate work
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
one. Unlike the archived integrations toolboxes, there is no provider
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

`cmd/devui` builds the apps toolbox when `APPS_URL` is set, signing its agent
token requests with a KMS `serviceauth.Signer` naming agents as the caller
(`AUTH_URL` and `AGENTS_PRIMARY_SERVICE_KEY_ID`); when either is missing it
logs a warning, since a run without the member's own token could then not act
for them. An error building the toolbox is logged, and Krude runs without it
rather than failing to start.

The integrations toolboxes that predated this, one per provider from the now
archived integrations service, were archived on 2026-09-17 with the
connections and integrations services; the code is at tag
`archive/connections-and-integrations-2026-09-17`.

**Not yet.** There is no run API yet, so outside tests nothing calls
`memberauth.WithAccessToken`; every development UI run acts through the
member's standing grant instead, and a member who has not granted one cannot
use the apps toolbox. No run starts from a trigger either.

## Models and routing

`Gemini` builds each model on Vertex AI with Application Default Credentials,
so there is no API key to hold: locally ADC is your `gcloud` login, and a
deployment points `GOOGLE_APPLICATION_CREDENTIALS` at its credential
configuration. `internal/modelrouter` routes every role to its tier, lite
wherever it can carry the work:

| Role | Runs on | Default |
| --- | --- | --- |
| `chat` — the member's conversation | main | `gemini-3.8-flash` |
| `research` — grounded web research | main | same; Google Search grounding is not on every lite model |
| `plan` — structured planning | lite | `gemini-3.8-flash-lite` |
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
of every request (a member message or a tool result) and every answer. It is
built and tested but not wired yet, and therefore off; when wired, it turns
streaming off for the model call (a partial answer that the completed one would
block cannot be unsent) and `AGENTS_PRIMARY_GUARDRAIL_FAIL_CLOSED` decides
what a failed screen does.

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

`internal/events` holds the outbox, publisher and dispatcher: events are
recorded in `agents_primary.agents_outbox` with the state they describe and
the dispatcher publishes them at least once, with exponential retry; consumers
deduplicate by `event_id`. The session runtime records `session.created` and
`session.deleted` as sessions come and go, best-effort after the session
service has already succeeded. `cmd/devui` creates the outbox table at
startup, and starts the dispatcher when `EVENTBRIDGE_ENDPOINT` is set — with
dummy credentials for the emulator, like auth — so
`EVENTBRIDGE_ENDPOINT=http://localhost:4566` publishes session events to the
LocalStack bus from `docker-compose.yml`. Without an endpoint the rows wait in
the outbox for the service to drain. `run.completed` and `run.failed` need the
run API and land with it; the pricing table that fills their cost and prices is
already in `internal/pricing`.

## Development UI

ADK's web UI runs the agents locally. Its sessions, conversation events and
state live in PostgreSQL, its artifacts in S3 when a bucket is named (see
[Artifacts](#artifacts)), and searchable memory remains in memory for now.
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

Primary installs no schema of its own this way — `Migrate` is nil, since ADK's
tables live in the separate pool Development UI above opens for them, not the
one this command shares with the rest of `oryxa` — and it deploys as part of
the one `oryxa` image; there is no Dockerfile of its own any more.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `AGENTS_PRIMARY_MODEL` | `gemini-3.8-flash` | Main model: conversation and grounded research |
| `AGENTS_PRIMARY_MODEL_LITE` | `gemini-3.8-flash-lite` | Lite model: planning and bulk single-turn work |
| `AGENTS_PRIMARY_PROMPT_CACHE` | `true` | Use explicit Vertex cached contents for large stable prefixes |
| `AGENTS_PRIMARY_CACHE_TTL_SECONDS` | `3600` | How long a created cache lives; 60–86400 seconds |
| `AGENTS_PRIMARY_CACHE_MIN_TOKENS` | `4096` | Smallest prefix worth an explicit cache; Vertex's floor for these models. Below it, implicit caching applies |
| `AGENTS_PRIMARY_ARTIFACT_BUCKET` | unset | S3 bucket artifacts are kept in; without it they are held in memory and go with the process |
| `EVENT_BUS_NAME` | `default` | EventBridge bus events publish on |
| `EVENTBRIDGE_ENDPOINT` | unset | EventBridge endpoint override for the LocalStack emulator |
| `DATABASE_URL` | unset | PostgreSQL connection for ADK sessions, events and state, and the read of the member's auth tables when a session is created; required by the ADK launcher |
| `AGENTS_PRIMARY_AUTO_MIGRATE` | `true` | Create the `agents_primary` schema and migrate ADK session tables at startup |
| `AWS_REGION` | `ap-south-1` | AWS Region for Bedrock AgentCore; Mumbai supports the AgentCore tools |
| `AGENTS_PRIMARY_SANDBOX_CODE_INTERPRETER_ID` | `aws.codeinterpreter.v1` | Built-in or provisioned AgentCore Code Interpreter identifier |
| `AGENTS_PRIMARY_SANDBOX_SESSION_TIMEOUT_SECONDS` | `900` | Per-conversation sandbox lifetime; 60–28800 seconds |
| `AGENTS_PRIMARY_BROWSER_ID` | `aws.browser.v1` | Built-in or provisioned AgentCore Browser identifier |
| `AGENTS_PRIMARY_BROWSER_SESSION_TIMEOUT_SECONDS` | `900` | Per-conversation browser lifetime; 60–28800 seconds |
| `GOOGLE_CLOUD_PROJECT` | unset | Project whose Vertex AI serves Gemini; the agents require it |
| `GOOGLE_CLOUD_LOCATION` | `global` | Vertex AI location: `global`, or the `us` or `eu` multi-region |
| `GOOGLE_APPLICATION_CREDENTIALS` | unset | ADC credential file for deployments; locally `gcloud auth application-default login` supplies ADC |
| `APPS_URL` | unset | The apps service's own URL; when set, `cmd/devui` builds the apps toolbox (see Acting for a member) |
| `AUTH_URL` | unset | Auth's own URL; where a member-absent run exchanges the member's standing grant for an agent token |
| `AGENTS_PRIMARY_SERVICE_KEY_ID` | unset | KMS key agents signs its service tokens with, to ask auth for an agent token on a member's behalf |

The HTTP server sets no write timeout: agent streams outlive any fixed write
deadline, so each streaming handler sets its own.
