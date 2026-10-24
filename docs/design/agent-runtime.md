# Agent runtime: one primary agent that runs its own sub-agents

Status: **draft for review**. Nothing here is built yet. It is phase 1 of the
agents plan: the ADK v2.6 upgrade (phase 0) lands first, and everything below
ships behind `AGENTS_PRIMARY_SINGLE_AGENT`, off in production until the owner
turns it on.

## Why

Krude (`agents/primary/internal/agents/agents.go` `Root`) works today through
five overlapping ways of handing work off:

| Mechanism | What it is | Tools it has |
|---|---|---|
| `planner` | fixed ADK sub-agent, lite model, returns a JSON plan | none |
| `researcher` | fixed ADK sub-agent, Google Search + URL context | search only |
| `spawn_helper` | one-shot agent from an instruction and a task (`helpers.go`) | none |
| `create_subagent` | named agent saved in session state, up to 12 (`subagents.go`) | none |
| specialists | persistent agents via the `agent_system` toolbox | their grant |

Two things make it spawn agents for trivial work:

1. **Krude cannot search the web itself.** Vertex AI still refuses a request
   that combines Google Search with function declarations (Vertex grounding
   docs, 2026-10-22; only the Gemini Developer API allows it, in preview).
   So every lookup is a researcher call.
2. **The prompt sends it to the planner** "whenever a request needs more than
   one step" (`instructions/krude.md`), which is nearly always.

Helpers and saved sub-agents have no tools, so they can only think in a fresh
context. They cannot read anything Krude cannot pass them.

Current practice (Anthropic's multi-agent research system and context
engineering notes, Claude Code sub-agents, Cognition's "Multi-Agents: What's
Actually Working", 2026-04) agrees on:

- one agent with good tools is the default; planning happens in its own
  thinking plus a todo list, not in a separate planner;
- a sub-agent is worth its cost (multi-agent runs use ~15x the tokens of a
  chat) only for parallel breadth, for work that would flood the parent's
  context, or for a clean-context review;
- the parent writes the brief; children are read-only, writes stay with the
  parent; depth is limited; results come back condensed.

## Goals

- Krude is **one agent with tools**. Nothing fixed is spawned for it.
- Krude **creates sub-agents itself** when the work warrants it, and manages
  each one's **whole lifecycle**: start it, talk to it back and forth, answer
  its questions, stop it, collect its result.
- Krude keeps **its own context small**: it holds a short record of each
  sub-agent; the full transcript stays with the sub-agent.
- **Every write stays with Krude**, behind the member's confirmation.
- **Invisible to members for now.** No UI and no Durable Object for
  sub-agents; Krude reports in its own words. Lifecycle events go to the
  outbox and to telemetry, not to the browser.
- Plain Go behind thin ADK adapters, so the framework stays replaceable.

## Non-goals (for this phase)

- Specialists, agent teams and workflows. They move onto this runtime in
  phase 2 (a specialist becomes a sub-agent with a saved definition).
- Any frontend change, generative UI (OpenUI / A2UI) or screen awareness.
- Grants and SpiceDB, which another team is changing.

## Design

### 1. Krude's toolset

Removed: `planner`, `researcher`, `list_helpers`, `spawn_helper`,
`create_subagent`, `list_subagents`, `remove_subagent`.

Added:

| Tool | What it does |
|---|---|
| `plan` | Krude's own todo list for the run: set, update and complete steps. Stored in our tables, keyed by thread, not in ADK session state. Replaces the planner. |
| `web_search` | Answers a question from Google Search, reading the links it names, and returns the answer with its sources. Internally a search-only agent wrapped as a tool (ADK `agenttool` over `geminitool.GoogleSearch` and URL context), because Vertex cannot mix it with our tools. It has no persona and no instructions beyond "answer and cite". Delete it when Vertex documents mixing. |
| `spawn_agent` | Starts a sub-agent from a brief (below) and returns its id. |
| `message_agent` | Sends a running or waiting sub-agent a follow-up, a correction or an answer to its question. |
| `agent_status` | The state, progress note and (when done) condensed result of one or all of this run's sub-agents. |
| `stop_agent` | Stops a sub-agent. |

Unchanged: the toolbox catalog (`list_tools` / `load_tools`), memory,
artifacts, screen context, and the confirmation flow.

### 2. The brief

```
spawn_agent({
  objective:   "what to find out or produce",
  output:      "what to return, and its shape",
  context:     "what the sub-agent needs to know; it sees nothing else",
  tools:       ["knowledge", "apps_read", "web_search", "sandbox"],
  files:       ["artifact names to hand over"],
  budget:      { model_calls: 30, minutes: 5 },
  wait:        true | false
})
```

- `tools` is an allowlist chosen from a fixed read-only set: knowledge
  (read-only), apps reads (`apps_connections`, `apps_search_tools`,
  `apps_run_read_tool`), `web_search`, the conversation's sandbox, and the
  browser read-only (navigate and read, no click, fill or type). Anything that
  sends, posts or changes something is never offered.
- The sub-agent runs as the member (the member's token, the member's grants);
  it cannot reach more than Krude can.
- `wait: true` blocks until the sub-agent finishes, asks a question or runs
  out of budget. `wait: false` returns at once; several sub-agents started in
  the same turn run concurrently.

### 3. Lifecycle

```
            spawn_agent
                 │
                 ▼
   ┌──────── running ────────┐
   │   ask_parent │          │ budget / error
   │              ▼          ▼
   │      waiting_on_parent  failed
   │              │ message_agent
   │              ▼
   │           running
   │              │
   ▼              ▼
 stopped        done ──► (expires after TTL)
```

- **running**: working; may report a short progress note.
- **waiting_on_parent**: the sub-agent called `ask_parent(question)` instead
  of guessing. Krude sees the question in `agent_status` (or as the return
  value of a waiting `spawn_agent` / `message_agent`) and answers with
  `message_agent`.
- **done**: returned its result (the `output` shape, condensed; large output
  goes to an artifact and the result names it).
- **failed**: error or budget exhausted, with the reason.
- **stopped**: Krude stopped it, or the parent run ended.
- **TTL**: a sub-agent and its transcript live as long as the thread's
  activity plus a TTL (default 24 h, configurable). After that it is gone and
  `agent_status` says so. A member can always ask Krude to redo work.

`message_agent` on a done sub-agent reopens it (back to running) with its
transcript, so Krude can ask follow-up questions without re-briefing.

### 4. Where it runs and what it stores

- Each sub-agent has its **own ADK session**, a child of the thread
  (`app = krude_sub`, session id `<thread>/<agent>`), so its transcript never
  enters Krude's context and it can be resumed.
- A table `agents_primary.subagents` holds the record Krude sees: id, thread,
  parent run, objective, tools, state, question, progress note, condensed
  result, budget used, timestamps, expiry. Krude's context only ever holds
  this record, never the transcript.
- The runner is plain Go (`internal/subagents`): a pool per process with a
  concurrency cap (default 4 per run, 32 per process), a per-agent budget
  (model calls and wall time), and cancellation when the parent run is
  stopped or drained. ADK v2.6 has no LLM-call cap in a release yet
  (`MaxLLMCalls` is on main only), so we count calls in a model callback.
- **No nesting**: a sub-agent has no `spawn_agent`. It may call `web_search`.
- Sub-agents share the parent conversation's **sandbox and browser** (the edge
  room `thread/<id>` already owns both; `edgeclient/target.go`), read-only for
  the browser.

### 5. Krude's context

- Effort rules go into `krude.md`, replacing the planner and helper
  paragraphs:
  - a simple fact or lookup: answer directly or call `web_search`; never spawn;
  - a comparison or a few independent questions: 2–4 sub-agents in parallel;
  - broad research or a large source: more, each with a distinct objective;
  - stop when the question is answered; do not spawn to double-check a fact
    a tool already returned.
- Thinking level per turn: the run sets `ThinkingConfig` from a cheap signal
  (message length, a loaded toolbox, a pending plan) with `low` as default and
  `high` when Krude has an open plan with several steps. Measured with
  `cmd/agentbench` before it is turned on.
- History compaction: turn on ADK's `compaction.Config` for long threads
  (`threads/run.go` already tolerates `compaction.ErrCompaction`). Check what
  is configured today before changing it.

### 6. Events and telemetry

- Outbox events, one per state change: `agents.subagent.started.v1`,
  `.waiting.v1`, `.completed.v1`, `.failed.v1`, `.stopped.v1`, carrying ids,
  state, budget used and priced usage, never the brief or the result text.
- **Ordering rule for deploys:** the agents inbound consumer returns an error
  on a detail type it does not know (`inbound/consumer.go` ~413), which
  dead-letters it. These events must not be routed to any queue whose
  consumer does not handle them; the EventBridge rules stay as they are until
  a consumer exists.
- Usage: every sub-agent's model calls are counted and priced into the parent
  run's `agents.run.completed.v1`, plus per-sub-agent totals.
- Tracing: ADK v2.6 emits OpenTelemetry GenAI spans for model and tool calls.
  Wire them into `internal/observability`, and add a span per sub-agent with
  the parent run as its parent. Where our traces are exported today is to be
  checked as part of this work.

### 7. Memory: Bedrock AgentCore Memory instead of Vertex Memory Bank

Separate PR in the same phase. The swap sits behind the existing
`adkmemory.Backend` interface (`Write`, `Search`, `Forget`,
`internal/adkmemory/memory.go:92`), so nothing above `adkmemory` changes.

- **Layout:** `actorId = <org>/<user>/<agent | primary>`, `sessionId = thread`,
  one semantic strategy (optionally user preference) with namespace
  `/m/{actorId}/`. Test first that the `/` in the actor id expands into
  namespace segments so `namespacePath=/m/<org>/` lists a whole org; the
  fallback is custom namespace keys (lowercase-hex ids).
- **Write:** one `CreateEvent` per run with the spoken turns (as today's
  `spoken()`), the run id as `clientToken`; skip runs where the member said
  nothing.
- **Search:** `RetrieveMemoryRecords` on the exact namespace.
- **Forget:** list records by namespace prefix and `BatchDeleteMemoryRecords`
  100 at a time. Raw short-term events also hold the conversation: set the
  event expiry to 7 days (the minimum) and delete a member's events on
  Forget; an organization-wide Forget is slow (`DeleteEvent` is 20 TPS) and
  keeps the existing deadline-and-retry contract.
- **Migration:** export Memory Bank's records and import them with
  `BatchCreateMemoryRecords` (no re-extraction), during a short write freeze,
  after a dual-write shadow period. Then remove the Vertex memory engine and
  its GCP grant.
- **Infra:** `aws_bedrockagentcore_memory` and
  `aws_bedrockagentcore_memory_strategy` (hashicorp/aws 6.x), a KMS key, task
  role permissions next to the existing AgentCore statement in
  `deploy/production/compute.tf`, and `AGENTS_PRIMARY_MEMORY_ID` replacing
  the Memory Bank variables. These are owner steps (`tofu apply`).

Memory is distinct from two other stores:

| Store | Holds | Lifetime |
|---|---|---|
| Scratchpad | intermediate files and notes, in the conversation's sandbox workspace | the sandbox's |
| Working knowledge | results worth keeping a while (findings, sub-agent outputs), indexed so later turns and sub-agents can search them | TTL; phase 1b, store to be decided |
| Memory | lasting facts about the member and their preferences | long-lived |

## Rollout

1. ADK v2.6 upgrade merged (phase 0).
2. Runtime behind `AGENTS_PRIMARY_SINGLE_AGENT`: new tools, the sub-agent
   runner and table, the prompt rewrite, telemetry. With the flag off, Krude
   is built exactly as today.
3. `cmd/agentbench`: the same task set on both builds; compare answers,
   tokens, latency and number of agents started.
4. Turn the flag on for one organization (as `AGENTS_PRIMARY_LIVE_CLOUDFLARE_ORGS`
   does for the live layer), then for everyone; remove the old code.
5. AgentCore Memory behind its own setting, with dual-write, then cut over.

## Work packages

| # | Package | Owns |
|---|---|---|
| R1 | `plan` and `web_search` tools, the prompt rewrite, the flag | `internal/agents/{agents.go,research*.go,plan*.go}`, `instructions/krude.md`, `modelrouter` |
| R2 | Sub-agent runner, table, lifecycle tools, events, usage | new `internal/subagents/**`, `internal/agents/subagents*.go`, a migration, `contracts/events/agents/v1` |
| R3 | Telemetry wiring | `internal/observability` use in `threads`, `subagents` |
| R4 | AgentCore Memory backend | `internal/adkmemory/agentcore*.go`, `serviceconfig`, `assembly` memory wiring, `deploy/production` memory pieces |

R1 and R4 can run in parallel; R2 follows R1's tool contracts; R3 follows R2.

## Open questions

1. Working knowledge with a TTL: expiring corpora in the rag service, or a
   store of its own?
2. Default sub-agent TTL (24 h proposed) and concurrency caps (4 per run).
3. Sub-agent model tier: the lite tier, or Krude's own? (Production runs
   `gemini-3.5-flash-lite` for both today.)
4. Memory migration: carry existing memories over (proposed) or start fresh?
5. The browser for sub-agents: read-only as proposed, or no browser at all?
