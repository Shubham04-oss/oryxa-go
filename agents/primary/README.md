# Agents primary

Primary is the agentic system in `/agents`: the prompts, tools and
configuration behind every agent interaction. adk-go is its engine and Gemini
its model. The Cloudflare chat layer, `durable-agent`, talks to it over HTTP,
and the supporting services in `secondary` sit beside it.

Status: the engine is in. adk-go v2.4.0 runs Krude, a Gemini assistant with
dedicated sub-agents, loadable toolboxes, temporary helpers and skills, and
`cmd/devui` opens it in ADK's development web UI. The service itself,
`cmd/primary`, still serves only `GET /healthz`; the run and stream API and
storage land as their design is agreed.

## Agents

`internal/agents` defines Krude and everything it works with. `Root`
assembles it:

```text
krude              chat: the member's assistant
├── planner        single-turn sub-agent: plans work or a session with teammates
├── researcher     single-turn sub-agent: answers from the live web with Google Search
├── teams          task sub-agent: teams and people (teams tools not wired yet)
├── toolboxes      list_tools, load_tools: sandbox, browser and integrations, loaded on demand
├── helpers        list_helpers, spawn_helper: temporary agents for big or separate work
├── skills         list_skills, load_skill, load_skill_resource: instructions on demand
└── set_nickname   saves the nickname the member gives Krude
```

**Sub-agents.** Krude calls a sub-agent the way it calls a tool, and the result
comes back to it. Single-turn sub-agents run without talking to the member,
and the ones Krude calls in the same model turn run concurrently. The task
sub-agent may ask the member a question and returns its result through
`finish_task`; ADK tells Krude never to call it in parallel with anything else.
A sub-agent sees only the request Krude hands it, and cannot transfer the
conversation to another agent.

**Toolboxes.** A `Toolbox` is a named set of tools, such as one provider's
actions. `list_tools` shows them and `load_tools` loads the ones a task needs
into the session, and their tools are usable from Krude's very next step. The
loader is a native ADK toolset: `Tools` selects the session's loaded toolboxes,
and the toolset's request preprocessing picks up one loaded midway through an
invocation. Every tool in a toolbox must be a declarative ADK tool named after
it, as in `slack_post_message`, or the toolbox will not load.
`Providers` lists Slack, GitHub, Google and Microsoft, but their tools will
come from the integrations service, which is not wired to primary yet. The
`sandbox` and `browser` toolboxes are connected today. Sandbox supplies native ADK function tools for
Python/JavaScript/TypeScript, shell commands and temporary file list/read/write/
remove operations. Execution happens in Bedrock AgentCore Code Interpreter,
never in the primary container. One isolated AgentCore session is reused by one
ADK conversation so its language context and files survive across calls;
different conversations are isolated. Remove and reset require ADK human
confirmation, returned content is bounded before it enters model context, and
active sessions are stopped during graceful shutdown with AWS's TTL as fallback.

Browser supplies navigation, bounded semantic DOM snapshots, selector clicks,
field fills, screenshots and reset through Bedrock AgentCore Browser. It signs
the Browser automation WebSocket with the deployment's AWS credentials and
talks CDP directly to the remote page; no browser executable or browser state
lives in primary. Browser sessions are isolated by ADK conversation and stopped
on reset, expiry replacement or graceful shutdown. Click, fill and reset always
use ADK confirmation. Web content is treated as untrusted data, secret values
are forbidden in model-driven fill calls, common password/OTP/payment/file
fields are rejected again inside the remote page, and screenshots become bounded ADK
PNG artifacts instead of base64 model output. ADK's `load_artifacts` tool is
always available so the model can inspect a screenshot on demand. The ADK
launcher currently keeps those artifacts in memory, so production still needs
a durable artifact service.

**Helpers.** `list_helpers` describes the dedicated sub-agents. `spawn_helper`
builds a temporary single-turn agent from an instruction and a complete task,
runs it, and returns its result; helpers started in the same turn run
concurrently. They get the session's loaded toolboxes and the skills, but no
helper can spawn another, load toolboxes or set the nickname. A helper cannot
reach the member, so its instruction forbids sending, posting or changing
anything: it returns what it prepared for Krude to confirm.

**Skills.** `internal/agents/skills/` holds one folder per skill, each with a
`SKILL.md` whose frontmatter names and describes it. Every request lists the
skills by name and description; Krude loads a skill's full instructions only
when it fits the work. Helpers get the same skills.

**Session state.** Krude is assigned to one member per session, and its
instruction reads these values:

| Key | Set by | Meaning | When missing |
| --- | --- | --- | --- |
| `user_name` | Session creator | The member Krude assists | "the member you are talking with" |
| `user_about` | Session creator | A few words about the member | Left out |
| `org_name` | Session creator | The member's organization | "their organization" |
| `user:agent_nickname` | Krude, via `set_nickname` | What the member calls Krude | Left out |

The `user:` prefix makes the nickname user-scoped state, so it follows the
member into their other sessions. Every value is collapsed to one line of at
most 60 characters before it reaches the prompt, because members write them.
`loaded_tools` holds the session's loaded toolboxes.

Each agent's instruction is a Markdown file in `internal/agents/instructions/`,
embedded at build time. Krude's, `krude.md`, is a Go `text/template` rendered
on every turn. The sub-agents' are ADK templates: `{key}` is replaced with that
session state value, and a turn fails when the key is missing unless it is
written `{key?}`. ADK adds text of its own to what the model sees: an identity
line for Krude and the task agents, `finish_task` rules for the task agents,
and a warning against parallel calls on each task agent's tool.

To add a dedicated sub-agent, give it a constructor beside the others with a
name, the description Krude picks it by, a mode, an instruction and transfers
switched off, add it to `Root`, and name it in `instructions/krude.md`. To add a
skill, add a folder under `skills/`. To add a toolbox, pass it to `Root`.

Every agent runs on the model `Gemini` returns. Gemini runs on Vertex AI and
authenticates with Application Default Credentials, so there is no API key to
hold: locally ADC is your `gcloud` login, and a deployment points
`GOOGLE_APPLICATION_CREDENTIALS` at its credential configuration. Researcher's
Google Search grounding is priced separately from model tokens.

## Development UI

ADK's web UI runs the agents locally. Its sessions, conversation events and
state live in PostgreSQL, while artifacts and searchable memory remain in
memory for now. The session store uses ADK's native relational service through
the repository's instrumented PostgreSQL pool and keeps its tables in the
`agents_primary` schema. Log in for ADC and AWS once, start PostgreSQL, and set
`GOOGLE_CLOUD_PROJECT` in `.env` to a project with the Vertex AI API enabled;
without the database, ADC or the project the command exits at startup. AWS
credentials are resolved lazily, so the UI can start without them. A sandbox
tool call needs `bedrock-agentcore:StartCodeInterpreterSession`,
`bedrock-agentcore:InvokeCodeInterpreter` and
`bedrock-agentcore:StopCodeInterpreterSession` for the configured interpreter.
A browser call needs `bedrock-agentcore:StartBrowserSession`,
`bedrock-agentcore:ConnectBrowserAutomationStream` and
`bedrock-agentcore:StopBrowserSession` for the configured browser. Scope the
workload role to the selected AgentCore resource identifiers and use CloudTrail
for control-plane auditing; do not give the agent AWS credentials as tool input.

```sh
docker compose up -d postgres
gcloud auth application-default login
aws login
cp agents/primary/.env.example agents/primary/.env
(set -a && . agents/primary/.env && go run -C agents/primary ./cmd/devui)
```

Open http://localhost:8087/ui/. The REST API the UI calls is under
http://localhost:8087/api. Sessions the UI creates start with no state, so
Krude assists "the member you are talking with" in "their organization". To try
a member, create a session with state for the UI's `user`, then pick it in the
UI:

```sh
curl -X POST http://localhost:8087/api/apps/krude/users/user/sessions \
  -H 'Content-Type: application/json' \
  -d '{"state":{"user_name":"Asha","user_about":"a product designer","org_name":"Acme"}}'
```

Tell Krude a nickname in the chat and it saves it with `set_nickname`.

Arguments to `devui` replace its web UI defaults and go straight to ADK's
launcher, so `console` chats in the terminal instead:

```sh
(set -a && . agents/primary/.env && go run -C agents/primary ./cmd/devui console)
```

## Module

Primary is its own Go module, `github.com/Shubham04-oss/oryxa-go/agents/primary`.
adk-go releases every few weeks and brings its own dependency tree; a separate
module keeps those upgrades away from the module every other service shares.
`go.mod` replaces the root module with `../..`, which is how primary imports
`/internal/observability`.

The root module's `go test ./...` and `go mod tidy` do not reach primary, so
run its checks with `-C` from the repository root:

```sh
go test -C agents/primary -race ./...
go vet -C agents/primary ./...
go mod tidy -C agents/primary
```

## Run the service

```sh
cp agents/primary/.env.example agents/primary/.env
(set -a && . agents/primary/.env && go run -C agents/primary ./cmd/primary)
curl -i http://localhost:8086/healthz
```

The image builds from the repository root, because the build needs the root
`go.mod` and `/internal`:

```sh
docker build -f agents/primary/Dockerfile -t oryxa-agents-primary:dev .
docker run --rm -p 8086:8080 oryxa-agents-primary:dev
```

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `AGENTS_PRIMARY_ADDRESS` | `:8080` | Listen address |
| `AGENTS_PRIMARY_MODEL` | `gemini-3.8-flash` | Gemini model the agents run on |
| `DATABASE_URL` | unset | PostgreSQL connection for ADK sessions, events and state; required by the ADK launcher |
| `AGENTS_PRIMARY_AUTO_MIGRATE` | `true` | Create the `agents_primary` schema and migrate ADK session tables at startup |
| `AWS_REGION` | `ap-south-1` | AWS Region for Bedrock AgentCore; Mumbai supports AgentCore tools |
| `AGENTS_PRIMARY_SANDBOX_CODE_INTERPRETER_ID` | `aws.codeinterpreter.v1` | Built-in or provisioned AgentCore Code Interpreter identifier |
| `AGENTS_PRIMARY_SANDBOX_SESSION_TIMEOUT_SECONDS` | `900` | Per-conversation sandbox lifetime; 60–28800 seconds |
| `AGENTS_PRIMARY_BROWSER_ID` | `aws.browser.v1` | Built-in or provisioned AgentCore Browser identifier |
| `AGENTS_PRIMARY_BROWSER_SESSION_TIMEOUT_SECONDS` | `900` | Per-conversation browser lifetime; 60–28800 seconds |
| `GOOGLE_CLOUD_PROJECT` | unset | Project whose Vertex AI serves Gemini; the agents require it |
| `GOOGLE_CLOUD_LOCATION` | `global` | Vertex AI location: `global`, or the `us` or `eu` multi-region |
| `GOOGLE_APPLICATION_CREDENTIALS` | unset | ADC credential file for deployments; locally `gcloud auth application-default login` supplies ADC |
| `DEPLOYMENT_ENVIRONMENT` | `development` | Environment on logs and telemetry |
| `LOG_LEVEL` | `info` | Structured log level |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset | OTLP/gRPC export; without it, logs stay on and telemetry export is a no-op |

The HTTP server sets no write timeout: agent streams outlive any fixed write
deadline, so each streaming handler sets its own.
