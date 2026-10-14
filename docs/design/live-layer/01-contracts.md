# 01: Contracts and foundation

These are pinned **before** any lane writes code. Every lane builds against them in parallel. To change one, post a `[decision]` in Discussions, get the lead's merge, and every lane rebases.

## 1. Rooms and naming

A **room** is any place work happens:

| `kind` | Today it is | Its `id` |
|---|---|---|
| `thread` | a member's chat with their agent | thread id |
| `teamspace` | a teamspace room | room id |
| `dm` | a direct message | DM id |
| `side` | a side conversation | side conversation id |

**Durable Object names**, deterministic and never guessable on their own:

```
RoomDO      org/{orgId}/room/{kind}/{id}
SandboxDO   org/{orgId}/room/{kind}/{id}/sandbox/{name}   name: "shared" | "private-{memberId}" | "{runId}"
BrowserDO   org/{orgId}/room/{kind}/{id}/browser/{sessionId}
VoiceDO     org/{orgId}/room/{kind}/{id}/voice
```

Knowing a name grants nothing. **Every request is checked against the token** (section 2).

The RoomDO records the room's `teamId` (it's empty for `thread` and `dm`) and its member list. It is the source of truth for which children exist.

## 2. The edge token (Go → edge)

The token is signed by agents with the existing `internal/serviceauth` keys, and the edge verifies it with the public key, the same lookup `serviceauth` already does.

```json
{
  "iss": "agents", "aud": "edge",
  "org": "org_…", "team": "team_…|", "room": {"kind": "thread", "id": "…"},
  "member": "usr_… (whom the agent acts for)", "agent": "agt_…", "run": "run_…",
  "scopes": ["sandbox:exec", "sandbox:files", "browser:drive", "call:join", "call:speak", "log:write"],
  "private": false,
  "exp": "≤ 5 minutes", "jti": "…"
}
```

- The edge refuses a token whose `org`/`room` doesn't match the DO it reaches, an expired token, a reused `jti`, or a missing scope.
- `private: true` is allowed only for the `private-{member}` sandbox of the same `member`.

## 3. The web app → edge

web-next calls the edge through a **service binding**. It forwards the member's session-derived identity (org, member, room) that web-next has already verified. The edge then checks room membership in the RoomDO.

Browsers never talk to the edge directly, except over the three media paths, each with its own short-lived signed URL or token issued by the RoomDO:
- RealtimeKit participant tokens;
- Browser Run live-view URLs;
- the sandbox terminal and preview URLs.

## 4. The edge → Go

The voice pipeline's dispatch to the work agent, and the work agent's results, go to `POST https://api.oryxa.in/v1/edge/...`. These are **not under `/v1/internal`, which the WAF blocks**. They are signed with an edge→agents token: the same format with `iss: edge`, `aud: agents`, using an edge signing key whose public half agents trust.

## 5. The sandbox API (Go `cfsandbox` → SandboxDO)

| Tool, unchanged | Edge call |
|---|---|
| `sandbox_execute_code` | `POST …/sandbox/{name}/exec/code {language, code, timeoutMs}` → `{stdout, stderr, exitCode, results[], durationMs}` |
| `sandbox_execute_command` | `POST …/exec/command {command, cwd, timeoutMs}` → same shape |
| `sandbox_list_files` | `GET …/files?path=` |
| `sandbox_read_files` | `POST …/files/read {paths[]}` (size-capped) |
| `sandbox_write_files` | `POST …/files/write {files[{path, content, encoding}]}` |
| `sandbox_remove_files` | `POST …/files/remove {paths[]}` |
| `sandbox_reset` | `POST …/reset` |
| `sandbox_save_artifact` | `POST …/artifact {path, name}` → copies to R2 under the run (section 7); returns its artifact reference |

New capabilities, which the UI and Layer C use:
- `POST …/preview {port}` → a signed preview URL;
- `POST …/terminal` → a PTY session URL;
- `POST …/snapshot`;
- an event stream of file changes into the room log.

## 6. The browser API (Go `cfbrowser` → BrowserDO)

| Tool, unchanged | Edge call |
|---|---|
| `browser_navigate` | `POST …/browser/{session}/navigate {url}` |
| `browser_snapshot` | `POST …/snapshot` → accessibility/DOM snapshot, with element references |
| `browser_click` | `POST …/click {ref}` |
| `browser_fill` | `POST …/fill {ref, value}` |
| `browser_screenshot` | `POST …/screenshot` → stored in R2, returns a reference plus a thumbnail URL |
| `browser_reset` | `POST …/reset` |

New capabilities:
- `POST …/live` → a signed live-view URL;
- `POST …/handoff {reason}` asks a member to take over, and the agent waits;
- `POST …/resume`;
- recording on by default, saved to R2.

The tools' input and output shapes stay exactly as the AgentCore toolsets expose them today (`agents/primary/internal/agentcore{sandbox,browser}/toolset.go`), so prompts and agents don't change.

## 7. R2 layout (bucket `oryxa-runs`, plus `oryxa-runs-staging`)

```
runs/{orgId}/{roomKind}/{roomId}/{runId}/
  manifest.json          who, what, when, model(s), cost, status, links to everything below
  events/{seq:08}.ndjson the run's events in chunks (section 8)
  artifacts/{name}       agent artifacts (what s3artifact saves today)
  media/                 screenshots, browser recordings, previews, snapshots
calls/{orgId}/{roomKind}/{roomId}/{callId}/
  transcript.ndjson  recording/*  notes.md
```

Every read is checked against the caller's org and room. Deletion and retention follow the organization (Layer C, purge).

## 8. The room log (event schema)

One append-only stream per room, kept hot in RoomDO storage and flushed to R2. Every event:

```json
{"seq": 1042, "at": "RFC3339", "org": "…", "room": {"kind": "…", "id": "…"},
 "actor": {"type": "member|agent|system", "id": "…", "onBehalfOf": "usr_…"},
 "run": "run_…|null", "type": "sandbox.exec|sandbox.file.changed|browser.navigate|browser.handoff|call.joined|transcript.segment|…",
 "summary": "short, human-readable", "data": {"…": "type-specific, size-capped"},
 "durationMs": 0, "cost": {"unit": "usd", "amount": 0.0, "model": "…"}, "private": false}
```

- `private: true` events are visible only to their `onBehalfOf` member.
- The web app subscribes through the room for live updates.
- Go can read a run's events back for the agent's context.

## 9. Foundation work packages (these come first)

| ID | What | Lane | Owns |
|---|---|---|---|
| F1 | These contracts as code: Go types in `contracts/edge/v1` and TS types in `edge/src/contracts`, with shared JSON fixtures in `contracts/edge/v1/testdata` | GO + EDGE | those paths |
| F2 | The edge Worker skeleton: `edge/` with wrangler config, the RoomDO plus an empty SandboxDO, BrowserDO and VoiceDO, token verification, the room log, and the R2 writer | EDGE | `edge/**` |
| F3 | The edge-token issuer in Go plus the `edgeclient` HTTP client, with retries and timeouts | GO | `agents/primary/internal/edgeclient`, the `internal/serviceauth` additions |
| F4 | Infrastructure: the R2 buckets, the edge deploy in CI, the staging environment, secrets, the route `edge.oryxa.in`, and the WAF allowance for `/v1/edge/*` | INFRA | `deploy/**`, `.github/workflows/**`, `edge/wrangler.jsonc` env sections |
| F5 | The service binding web-next → edge, plus the room's live-log subscription in the UI | UI | `web-next/wrangler.jsonc`, `web-next/src/lib/live/**` |
| F6 | Test harness skeletons: contract fixtures run on both sides, and an end-to-end runner against staging | TEST | `test/live/**` |

Done means: a Go test issues a token, the edge verifies it, a RoomDO is created, an event is logged and lands in R2, and the web app sees it live, **on staging**.
