# The live layer on Cloudflare

Status: plan, 2026-10-14. Nothing here is built yet, and no board items exist.

Oryxa's live layer moves to Cloudflare: the agents' sandbox and browser, calls with voice and video, the agents that take part in calls, and the record of everything that happens in a room. The brain stays on AWS: the Go agents, the models, knowledge, the database and the event bus.

## The rule: wired end to end

Each piece is done only when it works **from the agent's tool call or the member's click, all the way through, in production**:

- the Go agents use it through their existing tool names;
- the edge Worker owns it through a Durable Object;
- the web app shows it;
- every action is recorded;
- isolation holds;
- tests prove each of those.

A component that works on its own but isn't connected isn't done. How long it takes doesn't matter. A half-wired release doesn't happen.

## The rule: parallel by contract

All lanes work at the same time against the contracts in [01-contracts.md](01-contracts.md). Contracts are pinned first, and changed only through a Discussion that every lane reads. Each work package lists the paths it owns, so no two lanes edit the same files.

## Order of work

1. **Contracts and foundation** ([01-contracts.md](01-contracts.md)): naming, the service token, the APIs, the event log, the R2 layout, and the edge Worker skeleton.
2. **Baseline A: sandbox and browser on Cloudflare** ([02-baseline-sandbox-browser.md](02-baseline-sandbox-browser.md)). This is today's priority. It replaces AWS AgentCore entirely.
3. **Baseline B: RealtimeKit** ([03-baseline-realtimekit.md](03-baseline-realtimekit.md)): voice, video and screen share in rooms; the shared transcript; an agent that can join a call, listen and speak.
4. **Layer C: the nuances** ([04-layer-nuances.md](04-layer-nuances.md)). These build on both baselines only after they are wired:
   - sidekicks and private workspaces;
   - the org agent's rules and the room-audience guard;
   - latency calibration;
   - model routing and failover;
   - budgets, isolation hardening, consent and retention.
5. **Testing and calibration** ([05-testing-calibration.md](05-testing-calibration.md)) runs alongside every stage from the first day, not at the end.
6. **Agent isolation** ([06-agent-isolation.md](06-agent-isolation.md)) separates the personal, specialist, organization and team agents by identity, scopes, memory and guard. It runs in parallel with Baselines A and B, and it's a prerequisite for Layer C. Its leak L1 is live now and can be fixed first.

Baselines A and B can be built in parallel once the contracts are pinned. Layer C starts when both are wired.

## Architecture

```
Go agents on AWS (brain: unchanged)
  │  same tool names (sandbox_*, browser_*), new Cloudflare-backed implementations
  │  HTTPS + signed service token
  ▼
oryxa-edge Worker on Cloudflare (new, edge/)
  RoomDO  one per room: the room's control plane
    ├── state, members, permissions
    ├── the run log (hot index; full record in R2)
    ├── the call (RealtimeKit meeting) and the shared transcript
    └── children it creates, lists and shuts down:
          SandboxDO   room/{id}/sandbox/{n}   one Sandbox SDK 1.0 container each
          BrowserDO   room/{id}/browser/{n}   one Browser Run session each
          VoiceDO     room/{id}/voice         the voice agent pipeline
  │  service binding (Worker to Worker)
  ▼
web-next Worker (existing): the chat and room DOs, the UI

R2: the full record of every agent and live run (oryxa-runs bucket, prefixed by organization)
Heavy streams bypass the DOs: call media via RealtimeKit, the browser live view via Browser Run,
terminal bytes straight to the sandbox.
```

**A Durable Object per room.** A 1:1 chat with your agent, a DM, a teamspace room and a side conversation are all rooms. The room coordinates, and its children each hold one container or one session. The room never carries media.

**R2 holds every run.** It's the permanent record: steps, tool inputs and outputs, transcripts, recordings, screenshots, previews, snapshots and agent artifacts. The RoomDO's storage is only the hot index. Agent artifacts move from S3 to R2, which speaks the S3 API, so `s3artifact` needs only a new endpoint.

## Who does what (assigned 2026-10-14)

| Agent | Owns | Scope |
|---|---|---|
| **platform-1** | Baseline A **end to end** ([02](02-baseline-sandbox-browser.md)), plus the shared foundation code: F1 contract types, F2 edge Worker skeleton (`edge/` with RoomDO and the room log), F3 Go `edgeclient` and token issuer | `edge/**` except `edge/src/{call,voice}/**`; `agents/primary/internal/{cfsandbox,cfbrowser,edgeclient}`; `contracts/edge/v1` |
| **integration-2** | Baseline B **end to end** ([03](03-baseline-realtimekit.md)): RealtimeKit calls, transcript, recording, VoiceDO, the dispatch to Go | `edge/src/{call,voice}/**`; `agents/primary/internal/edgedispatch`; its new route file under `agents/primary/internal/httpserver` |
| **frontend** (the rehaul agent) | The **whole web-next rehaul**, including the live-layer screens: F5 (binding and live log), A8 (sandbox and browser panes), B6 (call UI), and the UI parts of I3/I9, built against [01-contracts](01-contracts.md) | `web-next/**` |
| **macbookpro** (lead) | **Agent isolation** ([06](06-agent-isolation.md)) end to end; F4 infrastructure (wrangler, tofu, R2, RealtimeKit app, secrets, staging, CI); F6 and the test and calibration harnesses ([05](05-testing-calibration.md)); review, merge, cutover, AgentCore removal | `deploy/**`, `.github/workflows/**`, `test/live/**`, the `agents/primary` paths listed in 06 |

**Layer C** ([04](04-layer-nuances.md)) starts when Baselines A and B are both wired end to end on staging. It is assigned then.

**Shared files, agreed in Discussions first:**
- `edge/wrangler.jsonc`: platform-1 owns it; integration-2 adds its bindings through a PR to platform-1's file, or after platform-1's skeleton merges.
- `agents/primary/internal/assembly/assembly.go`: platform-1's A6 owns the sandbox/browser block; macbookpro's I1 owns toolbox selection.

**The UI rule during the rehaul:** the baselines are proven end to end through their APIs and the test harness first. The member-facing panes ship inside the rehaul, against the same contracts. A baseline counts as fully done when its panes are live too.

## Definition of done (all in production)

1. An agent runs code in a Cloudflare sandbox with the same tools as today, and the room watches the preview and terminal.
2. An agent browses with the same tools as today. The room watches live, a member takes over at a login and hands back, and a recording is kept.
3. A room call with voice, video and screen share. The org agent answers by voice within the latency target, and a member's sidekick handles a private request.
4. Spoken answers respect the room-audience guard, and recording consent works.
5. Every action is in the room's log and in R2, with who, what, when and cost.
6. Every isolation test passes, and budgets stop runaway use.
7. Voice latency (95th percentile) meets the target, and failover from Gemini to Sonnet works.
8. AWS AgentCore is removed: the packages, the IAM grants and the config.

## Open decisions

- **Edge Worker:** recommended as a separate Worker in `edge/`, joined to web-next by a service binding, so it deploys on its own and holds its own container config. The alternative is adding the DOs to web-next's Worker.
- **Recording:** off by default with opt-in per call, or on with a banner.
- **The voice model at the edge:** Gemini Flash or Live called from the edge, or a Workers AI model.
- **Guests:** whether people outside the organization can join calls in this release.
- **Jurisdiction:** whether EU-only DOs and R2 are needed now or later.
- **Workers plan:** confirm the account is on Workers Paid, which Sandboxes, Browser Run limits and R2 at scale need.
