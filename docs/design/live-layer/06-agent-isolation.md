# 06: Isolating the agents (personal, specialist, organization, team)

Status: plan, 2026-10-14, from a read-only map of `agents/primary` on `main`. Paths below are relative to `agents/primary/` unless they say otherwise.

## The problem in one line

Isolation today depends on the **kind of run** (a watched chat, a wake-up, a delivery, a room turn), not on **which agent** is running. All of a member's agents share one identity, one credential, one memory and the same app access.

## What exists today

| Agent | Acts as | Tools | Knowledge and memory | Notes |
|---|---|---|---|---|
| **Primary (personal, "Krude")** | The member's own token when watched, otherwise a per-member agent token (`internal/memberauth/credential.go:43-58`) | Everything (`internal/assembly/assembly.go:282-427`), plus the sub-agents it creates | The member's reach; Memory Bank per org and member (`assembly.go:434`) | The only agent that can create agents and teams |
| **Specialists** | The **same** token as the primary | The **same** toolboxes as the primary (`assembly.go:490`) | The **same** member-wide memory (`internal/threads/run.go:518-523`) | Run on the lite model **without the Bedrock guardrail** (`assembly.go:262`) |
| **Organization agent** (`oag_…`) | A synthetic user with no token (`internal/organizationagents/runtime.go:77`) | None (`internal/agents/organization.go:45`) | None; writes an org-wide synthesis | Its default instructions mention delegation it can't do (`internal/teamspaces/store.go:82`) |
| **Room seats** | n/a | The room toolbox only, enforced by `Confined` (`internal/managedagents/service.go:401-410`) | No memory | Good |
| **Wake-up and delivery runs** | Agent token | Reduced, and can't decide requests (#194) | No memory | Good |
| **Agent teams and workflows** | Member or agent token | Team turns get the **full** agent, because `StartedByTeam` isn't treated as unwatched (`run.go:487-494`) | Team turns have memory | The UI is back (#213) |

## Leaks to fix (from the map)

| # | Severity | Leak | Where | Scenario |
|---|---|---|---|---|
| L1 | **High, live now** | The org synthesis and its follow-ups are readable by **any** org member, even though they're built from **team-only** teamspace posts | `internal/httpserver/server.go:372-373, 1046-1081`; teamspace reads are team-gated at `internal/teamspaces/service.go:405` | Finance plans layoffs in their teamspace, and an engineer reads the summary through `GET …/organization-agent/context` |
| L2 | High (design) | No per-agent identity or app scope: every specialist has the member's full write access to apps, plus browser and sandbox | `assembly.go:490`; the agent token has no agent ID (`internal/memberauth/client.go:90`) | Prompt injection makes a "blog writer" specialist send email from the member's Gmail |
| L3 | Medium | **Fixed by I1 (`internal/runprofile`):** team turns and delegation ran the **full** agent, with memory | `internal/agentteamrooms/service.go:237`, `run.go:487-494`, `internal/agentsystem/service.go:293` | Another agent's output steers a run that has write tools and writes into memory |
| L4 | Medium | One memory pool per member; specialists and the primary read and write each other's memory | `run.go:519` | A specialist's polluted memory changes the primary's behaviour |
| L5 | Medium | Specialists, and the org agent, skip the Bedrock guardrail | `assembly.go:262`, `internal/agents/managed.go:139`, `internal/modelrouter/router.go:48` | Unguarded answers in chats the member is watching |
| L6 | Medium | Member-wide saved files have no org in their key, so they likely cross orgs (inferred) | `internal/s3artifact/service.go:432` (`app/user/user/<file>`) | A member in two orgs sees one org's files in the other |
| L7 | Low | The primary can decide or answer **any** of its member's agents' requests | `internal/interactions/service.go:596, 649, 880` | Breaks per-agent standing |
| L8 | Low | The org agent's instructions and comments promise delegation that doesn't exist | `internal/teamspaces/store.go:82`, `internal/teamspaces/types.go:30-34` | Confusing behaviour and docs |

## The target model

**Isolation decided by *which agent* × *which run*, in one place.**

### 1. Roles, each with its own identity

| Role | Identity | Default reach |
|---|---|---|
| **Personal agent** (one per member) | The agent's own identity (`agt_…`) acting **for** the member | The member's reach. Full tools. Its own memory. Decides only its *own* requests. |
| **Specialist** (member-made) | Its own `agt_…` | **Only what its owner grants**: an allowlist of apps and tools, **read-only by default**, writes granted explicitly. Its own memory. Knowledge limited to the scopes granted. |
| **Organization agent** (one per org) | An org service identity (`oag_…`), never a member token | **Org-scoped knowledge only.** No personal data, ever. Outputs visible only to the audience that could see every input. |
| **Team agent / seat** (per team or room) | A team identity | **Team-scoped** knowledge, the confined toolbox (as `Confined` today), team memory |
| **Sub-agents and helpers** | Inherit the parent agent's identity and **cannot exceed** its reach | A subset of the parent's |

### 2. One run-profile table

A single policy, in code, maps **(role × run mode)** to:
- tools;
- knowledge scopes;
- memory (read and write);
- the guardrail;
- whether it may decide or answer requests;
- the model tier.

Run modes: watched chat, wake-up, delivery, room seat, team turn, workflow, sidekick (Layer C), voice (Layer C).

- **Every run looks up its profile here**, replacing the scattered checks.
- **Team turns count as unwatched** (fixes L3).

### 3. Identity carries the agent

- Agent tokens include **`agent`** (`agt_…`/`oag_…`), **`role`**, and the **granted scopes**, not only the member.
- Apps, rag and the edge (live layer) enforce per-agent scopes.
- The audit trail names the agent, not just the member.
- Fixes L2.

### 4. Data keyed by org, member and agent

- **Memory:** `org/member/agent`, plus an optional, explicit "shared notes" space the member controls (fixes L4).
- **Artifacts:** include the org in every key (fixes L6). With the R2 move ([02](02-baseline-sandbox-browser.md) A7), the run layout already has the org.
- **Threads:** already separated by app, user and session. Keep it that way.

### 5. Guardrails on every agent and model

The answer guard and Bedrock Guardrails wrap the **router**, not just the main model (fixes L5). The guard policy is per agent, not per member.

### 6. The organization agent, done right

- **Its outputs have an audience** (fixes L1):
  - an org-wide synthesis uses **only org-visible inputs**;
  - team teamspace content produces **per-team** syntheses that only that team can read.
- **Its instructions match what it can do** (fixes L8). It becomes the live room's org agent in Layer C (C2), with org-scoped knowledge.

### 7. Request rights per agent

Each agent decides or answers **only its own** requests (fixes L7). The member can still decide anything, as the owner.

### 8. Small details

- **One display-name source,** with renames reaching existing threads.
- **One thread namespace for the primary,** removing the `krude` vs `managed_<agt>` split.
- **The primary's stored instructions** are either honoured or removed from the API.
- **A per-agent model tier,** feeding the routing (Flash, Opus, Sonnet).
- **Slack attribution names the agent:** "From Ada's research agent, approved by Ada".
- **ID prefixes** by role: `agt_`, `oag_`, a team prefix.

## Work packages (parallel by lane)

| ID | What | Lane | Owns | Depends on |
|---|---|---|---|---|
| I0 | **Fix L1 now:** gate the org synthesis and follow-ups to what the caller's teams can see, or to org admins until per-team syntheses exist, with a test | GO | `internal/httpserver` (the org-agent routes), `internal/organizationagents` | none, so **can go first** |
| I1 | **The run-profile table:** role × mode → tools, scopes, memory, guard, decide-rights, model tier. Replace the scattered checks, and make team turns unwatched (L3) | GO | `internal/assembly` (the toolbox selection; coordinate with A6, which owns only the sandbox/browser block), `internal/threads/run.go`, a new `internal/runprofile` | none |
| I2 | **Agent identity in tokens:** `agent` and `role` claims, plus per-agent scopes; apps and rag enforce them; audit names the agent (L2) | GO | `internal/memberauth`, the claim checks in `apps/` and `rag/`, `auth/` (the agent grant) | I1 |
| I3 | **Specialist grants:** an owner-set allowlist of apps and tools, read-only default, explicit write grants; enforced by I1 and I2 | GO + UI | `internal/managedagents`, `web-next/src/components/agent-*` (settings) | I1, I2 |
| I4 | **Memory per agent** plus optional shared notes (L4); migrate existing member memory to the primary's namespace | GO | `internal/threads/run.go` (memory keys), the memory bank code | I1 |
| I5 | **Org key on artifacts** (L6); migrate existing keys; aligns with A7 (R2) | GO | `internal/s3artifact` | coordinate with A7 |
| I6 | **Guardrails on every model and agent; guard policy per agent** (L5) | GO | `internal/modelrouter`, `internal/assembly` (guard wiring), `internal/guardpolicy` | I1 |
| I7 | **Organization agent:** per-team syntheses with audiences; org-wide uses only org-visible inputs; instructions match abilities (L1 properly, L8); prepares the C2 role | GO | `internal/organizationagents`, `internal/teamspaces` (defaults, comments) | I0 |
| I8 | **Request rights per agent** (L7) | GO | `internal/interactions` (decide/answer checks) | I2 |
| I9 | **Small details:** one display name with rename propagation, one primary thread namespace, primary instructions, per-agent model tier, Slack attribution, ID prefixes | GO + UI | `internal/managedagents`, `internal/agents`, the Slack templates (apps), web-next agent settings | I1 |
| I10 | **Tests:** a test per leak (L1–L8) that fails today and passes after; role × mode profile tests; cross-agent memory and artifact attacks | TEST + each lane | `test/live/agents/**`, Go tests beside each change | all |

## Status and tests per leak (I10, 2026-10-14)

I0–I9 are merged; I9's single primary thread namespace is deferred until A7 lands (see #255). Each leak has a Go test that fails on the code before its fix:

| Leak | Fixed by | Test |
|---|---|---|
| L1 | I0 #218, I7 #249 | `httpserver.TestOrganizationSynthesisAudiences`; `organizationagents.TestStoreLifecycle` (DB) |
| L2 | I2 #251, I3 #254 | `apps/internal/application.TestASpecialistRunsOnlyWhatItWasGranted` |
| L3 | I1 #244 | `runprofile.TestOnlyAMembersOwnTurnIsFull`, `TestTheTableIsPinned` |
| L4 | I4 #246 | `adkmemory.TestMemoryDoesNotCrossAgents` |
| L5 | I6 #247 | `assembly.TestTheGuardrailScreensASpecialistAndTheOrganizationAgent` |
| L6 | I5 #248 | `s3artifact.TestMemberFilesStayInTheirOrganization` |
| L7 | I8 #253 | `interactions.TestOnlyTheTargetCanDecideWithinRequestedScope` |
| L8 | I7 #249 | `teamspaces.TestTheOrganizationAgentPromisesOnlyWhatItCanDo` |

Still open:
- the browser and sandbox are not covered by specialist grants;
- files and knowledge do not enforce grants (rag does: a specialist's `corpora` grant, default all, limits what it can list, read, search and write);
- guard policy is per member, not per agent;
- `test/live/agents` attacks across real agents have not been written.

## How it fits the live layer

- It **runs in parallel** with Baselines A and B: different code (`agents/primary` internals vs `edge/`). The one shared file is `assembly.go`: **A6 owns only the sandbox and browser block, and I1 owns toolbox selection.** Agree the split in Discussions before both start.
- It's a **prerequisite for Layer C**: the org agent and sidekicks (C2) and the room-audience guard (C3) need roles, identities and the run-profile table.
- The edge token's `agent`, `member` and `scopes` claims ([01](01-contracts.md) §2) are **the same claims I2 adds**. Define them once, in `contracts/edge/v1`, and use them in both.

## Done means

1. Every leak L1–L8 has a test that failed before and passes now.
2. Every run resolves its tools, scopes, memory, guard and rights from the profile table, with no scattered exceptions.
3. Tokens name the agent; apps, rag and the edge enforce per-agent scopes; the audit trail names the agent.
4. A specialist can't write to an app unless its owner granted it, even under prompt injection.
5. The org agent never uses personal or team-private data in an audience that couldn't see it.
6. Memory and artifacts never cross agents or orgs.
