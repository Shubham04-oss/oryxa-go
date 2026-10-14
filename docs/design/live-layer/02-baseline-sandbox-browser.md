# 02: Baseline A: sandbox and browser on Cloudflare (today's priority)

Goal: Krude's sandbox and browser run **entirely on Cloudflare**, owned by the room's Durable Objects, with every action recorded and stored in R2. The Go agents keep the **same tool names and shapes**. When this is done, AWS AgentCore is removed.

This must be **wired end to end**:

agent tool call → `cfsandbox`/`cfbrowser` → edge token → SandboxDO/BrowserDO under the RoomDO → container or Browser Run → result back to the agent → event in the room log → R2 → visible in the web app → covered by tests

Depends on: the foundation (F1–F6 in [01-contracts.md](01-contracts.md)).

## What exists today (being replaced)

- `agents/primary/internal/agentcoresandbox` provides `sandbox_execute_code`, `_execute_command`, `_list_files`, `_read_files`, `_write_files`, `_remove_files`, `_reset` and `_save_artifact`, on AgentCore Code Interpreter, with a session per conversation.
- `agents/primary/internal/agentcorebrowser` provides `browser_navigate`, `_snapshot`, `_click`, `_fill`, `_screenshot` and `_reset`, on AgentCore Browser over CDP (`cdp.go`, `host.go`).
- Both are wired in `agents/primary/internal/assembly/assembly.go` (around lines 188–219), with config `AGENTS_PRIMARY_SANDBOX_CODE_INTERPRETER_ID`, `…_SANDBOX_SESSION_TIMEOUT_SECONDS`, `AGENTS_PRIMARY_BROWSER_ID` and `…_BROWSER_SESSION_TIMEOUT_SECONDS`.
- IAM `AgentCoreTools` in `deploy/production/compute.tf` (about lines 349–362).
- `s3artifact` stores agent artifacts in S3 (`AGENTS_PRIMARY_ARTIFACT_BUCKET`).

## Work packages (parallel within the stage)

| ID | What | Lane | Owns | Depends on |
|---|---|---|---|---|
| A1 | **SandboxDO**: Sandbox SDK **1.0** (not 0.x, which loses support on 2026-12-31). One container per DO. Image with Python, Node/TS and common CLI tools, plus an instance size per org tier. Implements every endpoint in contract §5, including preview, terminal, snapshot and file-change events. Idle shutdown and a time limit per exec. | EDGE | `edge/src/sandbox/**`, `edge/containers/sandbox/**` | F2 |
| A2 | **BrowserDO**: Browser Run session per DO. Implements contract §6: navigate, snapshot with element refs, click, fill, screenshot to R2, reset. Live view URL, handoff/resume, recording to R2. | EDGE | `edge/src/browser/**` | F2 |
| A3 | **RoomDO children management**: create, list and stop sandboxes and browsers; enforce per-room limits; log every child action; clean up idle children; forward file-change and browser events into the room log. | EDGE | `edge/src/room/**` | F2 |
| A4 | **Go `cfsandbox`**: same tool definitions as `agentcoresandbox` (names, descriptions, schemas copied), calling the edge through `edgeclient` with an edge token per call; maps errors and timeouts the way the AgentCore toolset does. | GO | `agents/primary/internal/cfsandbox/**` | F1, F3 |
| A5 | **Go `cfbrowser`**: same for `agentcorebrowser`. | GO | `agents/primary/internal/cfbrowser/**` | F1, F3 |
| A6 | **Assembly switch**: `AGENTS_PRIMARY_LIVE_BACKEND=cloudflare|agentcore` (default `agentcore` until cutover), selectable **per organization** during rollout; wiring in `assembly.go`; config in `serviceconfig`. | GO | `agents/primary/internal/assembly/assembly.go` (the sandbox/browser block only), `agents/primary/internal/serviceconfig` (new keys) | A4, A5 |
| A7 | **Artifacts to R2**: `s3artifact` gets an endpoint and credentials for R2 (`AGENTS_PRIMARY_ARTIFACT_ENDPOINT`, …); artifacts go under the run's R2 prefix (contract §7); existing S3 artifacts stay readable until migrated. | GO | `agents/primary/internal/s3artifact/**`, its config keys | F4 |
| A8 | **UI: live panes in a room**: a sandbox panel (files, terminal through a signed URL, preview), a browser panel (live view embed, "take over" and "hand back"), and the run log stream. Only what the member may see; private items only for their member. | UI | `web-next/src/components/{live-*,sandbox-*,browser-*}*`, `web-next/src/lib/live/**` | F5, A1, A2 |
| A9 | **Infra**: container image build and push in CI, Browser Run binding, R2 lifecycle, secrets for the edge-token keys and R2 credentials, staging and production wrangler environments. | INFRA | `edge/wrangler.jsonc` envs, `deploy/**`, `.github/workflows/**` | F4 |
| A10 | **Tests**: contract fixtures both sides; end-to-end on staging (agent runs code with a preview the room sees; agent browses, a member takes over, recording in R2); isolation attack tests ([05](05-testing-calibration.md)); chaos (container crash, DO restart mid-exec, browser drop). | TEST + each lane's own unit tests | `test/live/**` | all |
| A11 | **Cutover and removal**: staging → our org → all orgs. Then delete `agentcoresandbox`, `agentcorebrowser`, the AgentCore config keys, the `AgentCoreTools` IAM statement and the switch itself. | INFRA + GO | listed paths | A1–A10 green in production |

## Parallel plan

- **EDGE (A1, A2, A3) and GO (A4, A5, A6, A7) run at the same time** against the contracts. Each side tests against the shared fixtures and doesn't wait for the other.
- **UI (A8) starts as soon as F5 lands,** using fixture data, then switches to the real edge.
- **INFRA (A9) and TEST (A10) run from the start.**

## Done means (all checked in production, our org first)

1. The same prompts and tools work: an agent runs code and files persist for the room's sandbox session, behaving the same as the AgentCore toolset (or better).
2. The room sees the preview and terminal live, and a member's private work stays private.
3. An agent browses; a member takes over at a login and hands back; the recording is in R2.
4. Every action is in the room log and R2, with actor, on-behalf-of, run, timing and cost.
5. Isolation tests pass: wrong org, wrong room, replayed token, missing scope, sandbox reaching metadata or private IPs, browser sessions across members.
6. AgentCore is removed: packages, config and IAM.
