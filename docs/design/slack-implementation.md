# Slack consent loop: implementation plan (2026-09-29)

Owner decisions on the open questions (defaults taken, owner may override): 1a separate bot-only app; 2a DM scopes at slice 4; 3a token rotation off; 4a hold when the run called no tool; 5a refuse a mention naming nobody, with a hint.

**Status (2026-09-29, evening).** The owner's go covers every slice. Lanes and who holds them are in [../LANES.md](../LANES.md).
- **Built, in review:** A (`feat/slack-apps-gateway`), B (`feat/slack-consent-core`) and E (`feat/slack-router`). They are merged together as `slack/base`, and the other Slack lanes branch from it and merge back. The stack goes to main once F's end-to-end test passes.
- **Decided during the build:**
  - E's router posts `ask_ack`, so C must not.
  - C posts bypass answers through `consentpolicy.AnswerFor(reason)`: in the thread for a high-priority channel, as a DM otherwise. It re-checks the channel's mark before posting.
  - A's profiles carry `email_confirmed`. An unconfirmed Slack email never matches a member.
- **B's deviations, accepted for review:**
  - The respond tool's sources are the delivery run's reach (its toolboxes), not the tools it called.
  - `answer()` reads at the owner's peer level.
  - A revoked lease refuses any later answer (`ErrRevoked`).
  - `CreateAsk` may approve in the same transaction, for a bypass or a live lease.
  - B's migrations are 013–015 under interactions' existing lock 7263510703.
- **H is narrowed** so the web lanes in LANES.md can run beside it. It owns new files only: `web-next/src/components/{request-card,requests-view,requests-activity,slack-settings}*`, `web-next/src/lib/agents/agent-requests.ts` and a new `web-next/src/lib/apps/slack.ts`. It adds one line per file to `workspace-shell.tsx` and `agents-mode.tsx`, and its own row to PARITY.md.

This is the plan as the planner wrote it against #125's branch (`4c658cb`). Its paths still hold on main.

## 1. What changed, what is reused

- **S3 dissolves.** Built: `answerguard` Intensity, `guardpolicy.Effective` (org floor, relevance cap), `relevance` judge, check route. Left: `interactions.answer` calls `answerguard.CheckWith` at the owner's level (Request = asker's words, Sources = run's tools). New pure `consentguard` adds the summary rule (no 20-word copy, no links), Slack credential stripping, and Bedrock grounding against the run's tool results (failure holds).
- **Org chart is real.** `Outranks` = `runtime.Above(chart, asker, owner)` via `agents.ContextDirectory` with the asker's agent token. No grant, no bypass. Ownership hints unused in v1.
- **#125 policy.** `ExpireAfter` already caps `Create`; lease TTL = min(60 min, it). `HoldForReview` holds every non-self answer, bypass included; the asker sees a content-free "held" status.
- **hooks reused** (`v0` HMAC, `url_verification`, dedupe) plus a `Surfaces` port sending verified deliveries to `slackbot`, not `Pusher.Parse`. `slackbot` is no registry integration (shipped-manifest tests untouched) and a different Slack app from members' `integrations/slack`.
- **#116 reused.** Slack asks have no teamspace, so `HeldForOwner`; `target_approved` wakes the owner's agent under `approvedReach`. New: for `origin=slack` the waker skips `requester_*` runs; askers get a model-free `threads.Note`.

## 2. Pinned contracts

**Slack ingress.** Static URLs `POST {APPS_PUBLIC_BASE_URL}/v1/apps/slack/events` and `/interactivity` alias the hooks ingress for toolkit `slackbot`'s deployment endpoint (made lazily). Existing `v0` signature; secrets from `APPS_SLACK_APP`.
- Events: `url_verification`, then `event_callback` {`team_id`, `event_id`, `event.type` in `app_mention`, `app_uninstalled`, `tokens_revoked`}; others acked, dropped. Slack: 3 s, retries at once/1/5 min, over 95% failures an hour disables. **Verify:** `app_mention` only where the bot is a member; uninstall payloads.
- Interactivity: form-encoded, JSON in `payload`, 3 s, `chat.update` works, `response_url` unused. **Verify** `team.id`, `user.id`, `container.{channel_id,message_ts}`, `actions[0].{action_id,value}`.
- Ack path: signature, dedupe (`event_id`; click body hash), `team_id`→install, one transaction {`apps_slack_inbound`, outbox nudge, dedupe mark}, 200. No outbound call; 2 s DB deadline; unknown team: 200, drop.
- Bus (`oryxa.apps`, ids only): `apps.slack.message.received.v1`, `apps.slack.action.received.v1` {`inbound_id`, `organization_id`, `team_id`, `received_at`}; `apps.slack.install.changed.v1`.

**apps internal API** (token audience `apps`, purpose `slack`; `internalCaller` takes a purpose; production must set `APPS_SERVICE_CALLERS`): `GET /v1/internal/apps/slack/inbound/{id}` (7-day purge); `POST …/profiles` (email, name, guest/bot/deleted/stranger; 24 h cache); `GET …/channels/{id}` (`is_private`, `is_shared`, `is_mpim`, `high_priority`; fresh); `POST …/post {organization_id, idempotency_key, target{channel_id, thread_ts|user_id}, template, params}` → `{channel_id, ts}`; `POST …/update`. Templates render only in apps, unfurls off: `owner_notice`, `notice_settled`, `ask_ack`, `answer`, `status`, `refusal`, `private_note`, `bypass_notice`. Posts go only into threads seen in a verified inbound row; idempotent via `apps_slack_posts`; per-org bucket answers 429 + `Retry-After`. Admin (session): `GET|DELETE /v1/organizations/{org}/apps/slack/install` (GET: status + Add-to-Slack URL with single-use state; team bound to another org: 409), `GET /v1/apps/slack/install/callback`, `GET|PUT …/apps/slack/channels` (private, unshared only).

**Consent (agents).**
- `POST …/agent-requests/{id}/consent {"choice":"summary|access|deny"}`: member session only; 409 carries the winner.
- Ticket `t1.<b64 json{r,o,n,c,exp}>.<b64 HMAC-SHA256>`, key `AGENTS_PRIMARY_SLACK_TICKET_KEY`. `interactions.Consent(ticket, clickerUserID, surface)` requires valid ∧ clicker (Slack id→verified email→member) = `o` = target ∧ pending. `Store.Decide`'s row lock picks the winner; a click never reviews.
- `CreateAsk(org, asker, slack{team, channel, thread_ts, trigger_ts}, question, owners)`: primary agents via `managedagents.Primary` (no member token), `organizationPolicy`, key `slack:{team}:{channel}:{ts}:{owner}`; asks tools `["knowledge"]`, permissions `consent:summary`, `consent:access`. summary→approved; access→approved+lease; deny→rejected.
- `agent_interactions` += `ask_id`, `origin`, `consent`, `decided_via` (web|slack|lease|bypass), `decided_reason`; decider kinds += `lease`, `bypass`. `InteractionChanged` += `ask_id`, `origin`, `consent`, `decided_by.{surface,reason}`.
- `interaction_asks(id, org, origin, team_id, channel_id, thread_ts, trigger_ts, asker_user_id, kind, winner_request_id, bypass)`: first delivered answer wins by compare-and-set inside `Respond`; siblings `Close`d "Answered by …"; `CountOpenAsks`.
- `interaction_leases(id, org, asker, owner, channel_id, thread_ts, expires_at, revoked_at)`: a follow-up there is created approved (decider `lease`); screen and guard still run. `DELETE …/agent-leases/{id}` → `agents.interaction.lease.revoked.v1`.
- `consentpolicy.Decide(Facts)` → `Allow|NeedsConsent|Deny{reason}`; bypass = high-priority, private, unshared channel (re-read each use) or `Outranks`; recorded `decided_by{bypass, reason}`, owner told, audited.

**Surfaces, router, web.**
- `surfaces`: `surface_jobs`, `surface_links(request_id, role, channel_id, ts)`; consumes `agents.interaction.*`, re-reads the ledger: requested→`owner_notice` DM + ephemeral `ask_ack`; decided→`chat.update`; completed→`answer` ("From Raya's agent · approved by Raya (AI summary only)"); held→`status`. Never a secret.
- `slackrouter`: `slack_jobs`, `slack_identities` (lower(email) vs `auth_users`⋈`auth_memberships`; full members of the install's team). Refusals: `not_member`, `owner_not_member`, `group_dm`, `external_channel`, `no_owner`, `too_many_asks` (>5 open).
- realtime: `agents.interaction.*` → `POST {worker}/internal/realtime/requests`, signed as the purge over `"requests."+ts+"."+body`; ids-only body → `DirectMessageAgent(org~a~b).noteRequest` + both `InboxAgent`s; cards read Go as the member; the 10 s poll stays.
- API: `GET …/agent-requests?box=&origin=&ask_id=`, `GET …/agent-requests/activity`; secrets `PUT …/{id}/secret {value, ttl_seconds≤3600}` (owner), `GET` (asker, once), `DELETE`; `agents.interaction.secret.viewed.v1`.

**Migrations** (locks scanned on every branch; 7263510719-21 free): apps `008_slack_surface.sql` (installs, install_flows, channels, inbound, posts, profiles; lock …600) · interactions `013_consent.sql` (NOT VALID checks) + `014_validate_consent.sql` (…703) · `slackrouter/001_router.sql` (…719) · `surfaces/001_surfaces.sql` (…720) · `secretshares/001_secret_shares.sql` (…721).
**Settings:** `apps_slack_installs(team_id PK, one active per org, bot token sealed with the apps vault key, status)`; `apps_slack_channels(org, channel_id, name, high_priority, marked_by)`; rotation off; no new workspace-file fields.

## 3. Work packages (exclusive files, at most 5 a wave)

A and B land their `contracts/events/**` files first; C, D, E import them. Every WP tests against fakes.

**Wave 1: parallel.**
- **A apps gateway.** `apps/internal/{slackbot,hooks,httpserver,serviceconfig,postgres}/**`, `apps/{mount,service}.go`, `apps/README.md`, `contracts/events/apps/v1/slack.go`, `internal/slackfake/**`. Steps: `Surfaces` port and aliases; inbound transaction; Web API client and templates; install flow; internal and admin routes; env `APPS_SLACK_APP`, `APPS_SLACK_SURFACE`, `APPS_SLACK_API_BASE_URL` (loopback only). Tests: signature vectors, replay, form-encoded click, dedupe race, 429. `slackfake`: fake Web API recording calls, injecting `ratelimited`, signing and delivering mentions and clicks.
- **B consent core.** `agents/primary/internal/{interactions,interactiondelivery,httpserver}/**`, `agents/primary/internal/agents/interactions.go` (respond tool passes the run's sources), `contracts/events/agents/v1/**`. Steps: migrations; `CreateAsk`, `Consent`, tickets, leases, settle, bypass decider; `answer()` via `CheckWith`; routes (secrets behind a `SecretShares` interface, nil = 503; B defines the new `httpserver.Config` fields); waker skip; events. Tests: concurrent `Consent`s on Postgres, forged or expired ticket, wrong clicker, lease expiry, sibling close, `HoldForReview`.
- **C surfaces + client.** `agents/primary/internal/{surfaces,appsclient}/**` (`appsclient`: service-token client for §2's internal API, used by C and E), `agents/primary/internal/threads/{notes,items}.go`. Tests: fake apps: 429 retry, update after decide, replay idempotency.
- **D realtime + Worker.** `realtime/**`, `web-next/src/{realtime-requests.ts,server.ts,direct-message-agent.ts,inbox-agent.ts,lib/dms/**}`, `web-next/REALTIME.md`. Tests: prefix signature (a purge signature is refused), replay window, idempotent per (request_id, revision).
- **E router.** `agents/primary/internal/slackrouter/**`: jobs, identity, refusals, `consentpolicy`, `Authority`, consumer-side interfaces for apps, consent and the org chart. Tests: refusal table, bypass re-check, 5-ask cap, guest/deleted/unverified identities.

**Wave 2: integrate.**
- **F wiring + e2e.** `agents/primary/{service.go,README.md}`, `agents/primary/internal/{inbound,serviceconfig}/**`, `test/slack/**`. Wires B, C, E, G, K (migrations, workers, `httpserver.Config`); agents' subscriptions add `oryxa.apps` message/action, realtime's add `agents.interaction.*`. e2e: apps and agents in one process (`internal/host`, auth stub, Postgres, `slackfake`): mention, click, answer, race, hold, lease.
- **G consentguard.** `agents/primary/internal/consentguard/**`, `deploy/production/answer_guard.tf` (contextual grounding, denied topics). Tests: 19/20-word copy, links, key patterns, grounding failure holds.
- **H web.** Narrowed; see the status above. Request card (three choices, held review, secret view), Requests tabs Inbox/Sent/Activity, Slack settings with channel marks. Tests: component tests; decided elsewhere, card settles.
- **I secretshares + deploy.** `agents/primary/internal/secretshares/**`, `deploy/{production,scripts,README.md}` except `answer_guard.tf`. One-time view, expiry, revoke, KMS; tofu per §4. Tests: second view refused, expiry, revoke, nothing plaintext at rest.
- **K screen.** Takes `slackrouter/**`: one Jev request over the asker's words, role, teams and Slack title (work? injection? plausible? credential request?), fail closed; department check from the directory's teams. Tests: ask table, Jev failure refuses.

**Wave 3: slice 4.**
- **L apps DMs.** Takes `apps/internal/slackbot/**`: `message.im`, `im:history`, DM templates, Approve/Decline actions, manifest v2 (partners reinstall).
- **M agent DMs + org material.** Takes `slackrouter/**`, `service.go`; adds `organizationagents/channel.go`, `agents/organization.go`, `threads/run.go`: personal-agent DMs, write confirmations, organization-material answers (`org_answers`, no request).

## 4. The Slack app

Manifest: `apps/internal/slackbot/manifest.yaml`, static and golden-tested; the only way scopes, events and URLs change.
- Bot scopes: `app_mentions:read` (the entry), `chat:write` (notices, answers, updates), `im:write` (open the owner's DM), `users:read` and `users:read.email` (match people to members), `channels:read` and `groups:read` (is a channel private or shared).
- Events `app_mention`, `app_uninstalled`, `tokens_revoked` (last two under `bot_events`: **verify**); interactivity on; request URLs as §2; redirect `{APPS_PUBLIC_BASE_URL}/v1/apps/slack/install/callback`; Messages tab read-only; org deploy, socket mode and token rotation off.
- Manifest keys confirmed. Slack does not verify request URLs on creation, so the owner verifies them after deploy.

**Owner-only steps:**
1. api.slack.com/apps → Create from manifest → paste the file.
2. Put Signing Secret, Client ID and Client Secret into Secrets Manager `oryxa-production/slack-app` as JSON `{"client_id","client_secret","signing_secret"}` (`deploy/scripts/put-secrets.sh` reads `APPS_SLACK_APP` from `.env` or a hidden prompt).
3. After deploy, verify both request URLs in the app's settings.
4. Activate public distribution (beta install link); Marketplace submission later.

**Tofu (code only, applied with the owner later):** `secrets.tf` adds `APPS_SLACK_APP = "slack-app"` and `AGENTS_PRIMARY_SLACK_TICKET_KEY = "slack-ticket-key"` (put-secrets generates it with `openssl rand -hex 32` when absent), gated by `slack_app_ready`. `compute.tf` sets `APPS_SLACK_SURFACE`, `APPS_SERVICE_CALLERS` (agents' key, purpose `slack`; local in `keys.tf`) and the agents switch. `keys.tf` adds the `agents-secrets` KMS key (context `service=oryxa-agents`). After deploy, re-run `provision-app.sh`; the edge's `/v1/internal` block stays.

## 5. Live-run checklist (real workspace)

Runs: slice 1 after F, G, H; slice 2 after I, K; slice 3 after those; slice 4 after L, M.

**Slice 1** (one owner, no secret):
1. Mention in a channel: asker gets an ephemeral ack; owner's DM shows asker, question, three buttons, nothing else.
2. One ask per button: summary only (no 20-word copy, no links, attribution says so); 1 hour (excerpts; notice becomes "Approved by … at …"); deny (asker told ephemerally).
3. Race: click Slack and the web card together: one decision, both settle, loser sees the winner.
4. Altered or expired button value: no decision.
5. Ungrounded answer, and an unrelated one (steer the agent off-topic): held in Requests; thread shows only "held"; owner sends masked or withholds.
6. Follow-up in the thread within the hour: answered, no click; after revoke or 61 minutes: new notice.
7. Delay the ack so Slack retries: one ask, visible in both agent chats and the web inbox.
8. Guest, non-member, group DM, shared channel: refused ephemerally.

**Slice 2:** two owners: first answer wins, other notice "Answered by …", a failed answer wins nothing; a secret appears once, in the Oryxa DM (second view refused; expiry; revoke), never in Slack; department mismatch and a sixth open ask refused.
**Slice 3:** mark a private channel: answer in thread, owner told, Activity row; share or unmark it: consent again; executive in a public channel: answer in DM plus "Answered privately"; no grant: no bypass.
**Slice 4:** DM the bot: answered in DM and agent chat; write shows Approve/Decline; organization question answered from the synthesis, no request; uninstall ends the install.

## 6. Open questions and risks

1. Slack app: (a) new bot-only app, apart from the members' user-token app (recommended); (b) one app for both.
2. `im:history` and `message.im`: (a) add at slice 4, partners reinstall (recommended); (b) request from day one.
3. Token rotation: (a) off in v1 (recommended); (b) on now (irreversible; needs refresh leases).
4. Grounding when the run called no tool: (a) hold (recommended); (b) deliver on relevance alone.
5. A mention naming nobody: (a) refuse with a hint (recommended); (b) route by ownership hints.

Risks: Enterprise Grid installs refused in v1; Slack's app-approval flow may drop `state` (**verify**); a click reaches the other surface in 2–4 s; `APPS_SERVICE_CALLERS` is new in production (`/v1/internal` stays blocked at the edge); one workspace per organization.
