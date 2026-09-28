# Slack consent loop

*Proposed 2026-09-28, checked against `main` at `403cebf`. Assumes the cross-member fix has landed: room agents answer only from the room, requests without a teamspace wait for their owner, and unattended runs cannot list or decide requests. Onboarding provisions the Slack app and is designed separately.*

## Rules for v1

1. **Native.** Each organization has its own Slack app and bot, reached through `apps`. Composio has no part in this loop.
2. **Slack carries three things:** the entry, the owner's notice and the final answer. All processing happens in Oryxa.
3. **One source of truth.** The interactions ledger holds every request. Slack messages and web-next cards are views of it.
4. **The ask lives in Oryxa's DMs.** Both participants also see it in their own agent chat.
5. **No teamspaces.** Slack messages never route to teamspaces or rooms.
6. **The owner's permission is the default.** A request goes ahead only with the owner's approval, a lease the owner gave, or a bypass.

## 1. What the loop needs from the Slack app

- **Bot scopes:** `app_mentions:read`, `chat:write`, `im:write`, `users:read`, `users:read.email`, `channels:read` and `groups:read`. Job 2 adds `im:history`.
  - No channel-history scope is requested. The bot reads only messages that mention it, and never calls `conversations.replies`.
- **Events:** `app_mention`, `app_uninstalled` and `tokens_revoked`. Job 2 adds `message.im`.
  - Interactivity is on.
  - The App Home Messages tab stays read-only until Job 2.
- **URLs:** Event Subscriptions and Interactivity both point at the organization's hook endpoint, `POST /v1/apps/hooks/{id}`. hooks answers Slack's `url_verification`.
- **Secrets:** onboarding hands them over in one admin-only call, `PUT …/apps/slack/installation`, with the bot token, signing secret, client ID and client secret.
  - apps checks the token with `auth.test`, then seals it in `apps_slack_installs`. There is one install per organization, and it survives its installer leaving.
  - The signing secret goes on the organization-scope endpoint, as `PUT …/oauth-clients/slack` already does.
  - Members' own user tokens (`integrations/slack`) are unchanged.
  - Everything is off until `APPS_SLACK_SURFACE` is set.
- **The 3-second acknowledgement:**
  - hooks verifies the v0 signature and dedupes: on `event_id`, or on a click's body hash.
  - A new surface branch hands the delivery to `apps/internal/slackbot`.
  - slackbot writes the inbound record, a content-free nudge event and the dedupe mark in one transaction, then answers 200.
  - Nothing before that answer calls Slack, a model or another service.

## 2. Router and identity

`agents/primary/internal/slackrouter` turns nudges into durable jobs, the same pattern `interactiondelivery` uses. For each job it reads the verified inbound from apps with a service token, maps identities, and routes it:

| Arrives | Handled by |
|---|---|
| `app_mention`, in a channel or a thread | the **organization agent** |
| `message.im`, a member's DM with the bot (Job 2) | that member's **personal agent**, their primary agent |
| `block_actions` on an owner notice | code: `interactions.Consent` |
| `block_actions` on a write confirmation (Job 2) | code: the thread's pending confirmation |
| group DMs; externally shared channels | refused, with an ephemeral message |
| `app_uninstalled`, `tokens_revoked` | apps ends the install |
| anything else | acknowledged and dropped |

**Identity.**
- apps returns the Slack profiles of the sender and of everyone mentioned, from `users.info`: email, team, title, and the guest, bot and deleted flags. It caches them for a day.
- The router matches the lower-cased email to a member through auth's tables, the way `adksession` reads them, and keeps the match in `slack_identities`.
- Only full members of the install's own team match.
- `auth.member.removed.v1` drops a member's links.

**People who are not members**, including guests and Slack Connect users, get the ephemeral reply "Only members of Acme in Oryxa can ask me". If a named person isn't a member, the asker is told: "Maya isn't in Oryxa, so I didn't ask her." Nothing else is kept, and inbound text is purged after 7 days.

## 3. The loop

The singleton **organization agent** (`organizationagents`) gains a channel turn. Its model still never sees member-private context. The turn:
- **Resolves the people named** and checks their department against the asker's words. Until the owner's org graph exists, a department is the member's teams in the directory, read as the asker.
- **Decides what each owner's agent needs**, using Jev's toolbox judgment. At first that is knowledge only.
- **Pings the owners' personal agents** with interactions filed on the asker's behalf.
- **Wakes the owners** through their notices.
- **Lets code post** only approved, guarded results.

As with synthesis follow-ups, the turn proposes and code acts. Slice 1 needs no model: the owners are the members the message @-mentions.

### A. The canonical ask: two owners, a secret

```text
#infra, asker: "@oryxa ask the dev keys from @raya or @maya (engineering)"
Slack ─app_mention─▶ apps: verify · dedupe · record · nudge ─200 (<3 s)─▶ Slack
router ─▶ organization agent: @raya, @maya ─▶ members by verified email · both in Engineering
  screen (Jev): work · no injection · plausible for the asker · credential request
  consentpolicy: no lease, no bypass ─▶ needs consent
  interactions: one ask ─▶ two requests in parallel (asker's agent ─▶ Raya's, ─▶ Maya's)
requested.v1 ─┬▶ apps ─▶ asker, ephemeral: "Asked Raya and Maya · Open in Oryxa"
              ├▶ apps ─▶ Raya, Maya, DM notice: asker, question,
              │    [Share AI summary only] [Give access for 1 hour] [Deny]
              ├▶ realtime ─▶ Worker ─▶ Oryxa DMs (asker·Raya, asker·Maya): cards, inboxes
              └▶ threads.Note ─▶ each participant's Requests thread
Raya clicks, in Slack or on the DM card ─▶ Consent ─▶ Store.Decide row lock: first click wins
decided.v1 ─▶ chat.update "Approved by Raya at 14:02" · cards · notes · waker: Raya's agent works
Raya's agent ─▶ interactions_respond ─▶ guard: grounding · mode · denied topics · credentials
  secret ─▶ one-time view in DM(asker, Raya); thread: "Raya shared it with @asker in Oryxa"
  otherwise ─▶ apps posts the answer in the thread
first delivered answer settles the ask ─▶ Maya's request closed; her notice: "Answered by Raya"
```

Slice 1 is this same flow with one owner and an answer that isn't a secret.

### Choices as decisions

A request from Slack asks for both `consent:summary` and `consent:access`, so either approval is a subset under `validateDecision`. The decision records `consent` and `decided_by.surface`.

| Button | Decision | Granted | What the answer may be |
|---|---|---|---|
| Share AI summary only | approved | `consent:summary` | A summary: no run of 20 or more words copied from the source, and no source links. For a secret, only where it lives |
| Give access for 1 hour | approved | both | Excerpts. For 60 minutes, an `interaction_leases` row approves the asker's follow-ups in that thread. A secret goes out as a one-time view that expires within the hour |
| Deny | rejected | none | The asker is told, ephemerally |

**Parallel owners.**
- An `interaction_asks` row groups the requests.
- The first request completed with a delivered answer wins.
- Oryxa closes the others with `Close` or `CloseApproved`, giving "answered by Raya" as the reason.
- An agent that answers "not held" (a failed result) wins nothing.

**The screen** runs before anyone is pinged: one Jev request over the asker's words, org role, teams and Slack title. It asks four things:
- Is this work?
- Is it an injection?
- Is it plausible for this asker?
- Is it a credential request?

It fails closed.

**Policy.** `consentpolicy.Decide` is code-owned and shaped like a Cedar request. It answers one of three ways:
- **Deny:** non-members, externally shared channels, a failed screen.
- **Allow:** a live lease, a bypass, or organization-owned material.
- **Needs consent:** everything else.

It holds no stored policies, so PR #109's bypass class (a forbid that errors and silently stops applying) cannot arise. It moves to `internal/authz` once #109 is fixed, with needs-consent modelled as a second action.

**Finding owners without connectors.** v1 asks only owners the asker names. Suggestions from the directory come later. Owners of documents, commits and tickets need the connectors that are on hold.

**First click wins.**
- `Store.Decide`'s existing row lock decides between surfaces, and every surface updates from `decided.v1`.
- Each button carries a ticket: an HMAC that agents mints for the request, the owner and the notice. A leaked signing secret therefore decides nothing.
- A Slack click counts as the owner's own decision only with a valid ticket and a clicker mapped to the request's target. It can consent, never review.

**The guard** (`answerguard`) gains four checks:
- Bedrock's contextual grounding check, against the work run's own tool results. The respond tool reads them from its session. Bedrock takes up to 100,000 characters of source and 5,000 of answer.
- The summary mode.
- The guardrail's denied topics.
- A pass over everything bound for Slack that strips credentials: Bedrock's masked kinds plus fixed key patterns.

An ungrounded answer is held for the owner's review.

### Secrets

Neither the thread nor a bypass ever carries a secret. With "Give access for 1 hour" or a bypass, the answer is sealed (`internal/secrets`, `service=oryxa-agents`) as a one-time view in the Oryxa DM.
- The owner may paste the value in themselves, so it never passes through a model.
- The asker opens it once, in their own session. It is deleted when viewed or when it expires.
- The owner can revoke it before then.

### Bypass

- **Triggers:**
  - a private channel an admin marked high priority, re-checked as still private and unshared every time it is used;
  - `Authority.Outranks(requester, owner)`, an interface whose shape the owner defines. v1 ships it returning false.
- **Effect:** no owner click is needed. The decision is recorded as `decided_by: {kind: "bypass", reason}`, with excerpts allowed. The screen, the guard and the secrets rule still apply.
- **Destination:**
  - a marked channel's answer goes to its thread;
  - an executive asking in a public channel gets the answer in their DM with the bot, and the thread gets "Answered privately".
- **Audit:** the bypass is recorded in the ledger and in Activity, and the owner is told at once on both surfaces.

### B. Organization-owned material (slice 4)

```text
"@oryxa what did we decide on EU pricing?" ─▶ organization agent: screen ─▶ policy: organization-owned
planner answers from the synthesis, citing items ─▶ guard ─▶ thread · org_answers (Activity); no request
```

### C. A member DMs their own agent (slice 4)

```text
DM: "draft a reply to Acme" ─message.im─▶ apps ─▶ router ─▶ their primary agent, stable Slack thread
  (shown in their Oryxa agent chat) · agent token under their standing grant · cannot decide requests
write ─▶ confirmation ─▶ DM notice: hint, arguments, [Approve] [Decline] · first answer wins
final answer ─▶ credentials stripped ─▶ apps posts it in the DM
```

## 4. Data, events and routes (all additive)

**Tables in apps** (`008_slack_surface.sql`):
- `apps_slack_installs`;
- `apps_slack_channels`, the high-priority marks;
- `apps_slack_inbound`: references and actions, with message text purged after 7 days;
- `apps_slack_posts`, for idempotency.

**Tables in agents:**
- `013_consent.sql`: `agent_interactions` gains `ask_id`, `origin`, `consent` and `decided_via`. New tables: `interaction_asks` (the Slack thread, the asker, the kind, the winner and any bypass) and `interaction_leases`.
- `secretshares`: its own table.
- `slackrouter`: `slack_identities` and `slack_jobs`.
- `surfaces`: `surface_jobs`, retried and honouring `Retry-After`, and `surface_links (request_id, role, channel_id, ts)`.

**Links:**

| From | To | Kept in |
|---|---|---|
| Slack thread | its ask | `interaction_asks` |
| an ask | its requests | `ask_id` |
| a Slack notice or answer message | its request | `surface_links` |
| a request | its Oryxa DM | the DM's name, `org~asker~owner` |
| a request | the agent chats | the notes' request id; `interaction_delivery_threads` for the owner's work |

**Events:**
- `apps.slack.message.received.v1` and `apps.slack.action.received.v1`, which carry ids only;
- `apps.slack.install.changed.v1`;
- `InteractionChanged` gains `ask_id`, `origin`, `consent`, and `decided_by.surface` and `decided_by.reason`. `decided_by.kind` gains the values `lease` and `bypass`;
- `agents.interaction.lease.revoked.v1` and `agents.interaction.secret.viewed.v1`.

**Routes:**
- `POST …/agent-requests/{id}/consent {"choice"}`, for the owner.
- `GET …/agent-requests/{id}/secret`, once, for the asker.
- `GET|PUT …/apps/slack/channels`, for admins marking channels.
- apps' internal routes, which agents calls with the service purpose `slack`:
  - read an inbound record;
  - post a notice, an answer, a DM or an ephemeral message;
  - update a notice.
- apps renders only from typed templates, and posts answers only into threads it verified.

## 5. The web app

- **The Oryxa DM holds the ask.**
  - `realtime` consumes `agents.interaction.*` and signs a call to the Worker, `POST /internal/realtime/requests`. It reuses the purge's signing scheme with a `requests.` prefix.
  - The Worker calls `DirectMessageAgent.noteRequest` and both people's `InboxAgent`s.
  - The DM stores only a pointer, and the card reads the request from Go as the member.
  - The owner decides and reviews held answers there, and the asker opens a secret there.
- **Agent chats.** `threads.Note` appends a model-free `request` item to each participant's Requests thread. The item carries only the request id.
- **Agents → Requests** has three tabs:
  - Inbox;
  - Sent;
  - Activity: asks, decisions by surface, bypasses, leases and secret views.

  The existing 10-second poll stays as a fallback.
- **Settings → Integrations → Slack** shows the install's status and the high-priority channels.
- **Rooms.** v1 routes nothing into them. People ask in Slack; consent, records and work live in Oryxa.

## 6. Phasing and work packages

1. Three buttons, one @-mentioned owner, a non-secret answer in the thread, both surfaces, both agent chats, and credentials stripped.
2. The canonical ask: parallel owners, the department check, and secrets.
3. Bypass: channel marks, the authority interface, private delivery, telling the owner, and Activity.
4. Job 2 in the bot's DM, and answers from organization-owned material.

After that: remembered consent, N-of-M, claiming, takeover, Cedar, ownership from connectors, and Teams.

| WP | Slices | Owns |
|---|---|---|
| S1 apps gateway | 1, 3 | `apps/internal/slackbot/**` (new); `apps/internal/hooks/{ingress,ports,service}.go`; `apps/internal/postgres/{slack.go (new),hooks.go}`, `…/migrations/008_slack_surface.sql`; `apps/internal/httpserver/slack.go` (new) plus its mount; `apps/internal/serviceconfig/config.go`; `apps/{mount,service}.go`; `apps/README.md`; `contracts/events/apps/v1/slack.go` (new) |
| S2 consent core | 1–3 | `agents/primary/internal/interactions/{consent,asks,lease}.go` (new), `service.go`, `store.go`, `answers.go`, `migrations/013_consent.sql`; `agents/primary/internal/agents/interactions.go`; `…/interactiondelivery/waker.go`; `…/httpserver/server.go`; `contracts/events/agents/v1/events.go` |
| S3 guard | 1 | `agents/primary/internal/answerguard/**`; `deploy/production/answer_guard.tf` |
| S4 router and organization agent | 1–3 | `agents/primary/internal/slackrouter/**` (new: routing, identity, screen, `consentpolicy`, `Authority`); `…/organizationagents/channel.go` (new); `…/inbound/consumer.go`; `agents/primary/service.go`, which does all of agents' wiring |
| S5 surfaces and notes | 1 | `agents/primary/internal/surfaces/**` (new); `…/threads/{notes.go (new),items.go,threads.go}` |
| S6 realtime relay | 1 | `realtime/internal/{events/consumer.go,requests/** (new)}`; `realtime/README.md` |
| S7 Worker and objects | 1 | `web-next/src/{realtime-requests.ts (new),server.ts,direct-message-agent.ts,inbox-agent.ts}`; `web-next/src/lib/dms/**`; `web-next/REALTIME.md` |
| S8 request UI | 1–3 | `web-next/src/components/{request-card,agents-requests}.tsx` (new), `direct-message.tsx`, `agents-mode.tsx`, `workspace-shell.tsx`, `agent-chat.tsx`; `web-next/src/lib/{agents,threads}/**`; `web-next/PARITY.md` |
| S9 Slack settings | 1, 3 | `web-next/src/components/workspace-slack-settings.tsx` (new), `workspace-settings.tsx`; `web-next/src/lib/apps/**` (1W's files, while 1W is on hold) |
| S10 secret shares | 2 | `agents/primary/internal/secretshares/**` (new); `deploy/production/{keys,compute}.tf` |
| S11 Job 2 and planner | 4 | takes over `slackrouter`, `surfaces` and `slackbot` once their packages are done; `…/agents/organization.go`; `…/threads/run.go` |

**Order:**
- S2 waits for the cross-member fix, which touches the same files.
- Against the contracts above, S1, S3, S5, S6, S7 and S10 run in parallel.
- S4, S8 and S9 integrate last.
- Each slice ends with a live run in a real workspace. For slice 1 that run covers:
  - each button;
  - a race between Slack and the web;
  - an ungrounded answer being held;
  - a follow-up under a lease.

## 7. Risks

- **Slack's terms.** Since May 2025, Slack's API terms name the Marketplace as the only channel for commercial distribution, and that includes "a templated custom app that connects to your product".
  - Per-organization apps created by our CLI fit that description.
  - Internal customer-built apps keep the full rate limits, but Slack doesn't say whether a templated app counts as internal.
  - The deployment-scope endpoint keeps a Marketplace app possible.
- **Rate limits.**
  - Distributed apps outside the Marketplace get `conversations.history` and `conversations.replies` at 1 request a minute and 15 objects. We call neither.
  - `chat.postMessage` allows about one message a second per channel. `chat.update` is Tier 3; `users.info` and ephemeral messages are Tier 4.
  - Mitigations: a per-organization token bucket, honouring `Retry-After`, and at most 5 open asks per asker.
- **Privacy.**
  - Slack keeps what we post under the workspace's own retention and exports: the notice (the asker and their question, which were already in the channel) and the answer.
  - Sources, context, held answers and secrets never go to Slack.
  - Link unfurling is off.
- **Secrets through models.** A secret the owner's agent finds passes through Vertex and Bedrock. Prefer pointing to where it lives, or letting the owner paste the value in.
- **Trust.**
  - Tickets limit what a leaked signing secret can do.
  - Bus signatures are only reported, not enforced. Nudges therefore carry ids only, and every action is re-read from apps.
  - `/v1/internal/*` must be blocked at the edge.
- **Latency.** A click takes about 2–4 seconds to show on the other surface.
- **Grants.**
  - The owner's agent can read only if the owner has given the standing agent grant.
  - The department check needs the asker's grant.

## 8. Owner decisions

**Settled on 2026-09-28:**
- The owner's notice carries the three buttons, the asker and their question, and nothing else.
- A bypass comes from admin-marked private channels, or from authority checked through an interface.
- A bypass answer goes to the thread, or to the executive's DM with a note in the channel. Every bypass is audited, and the owner is told.
- A secret appears only as a one-time, expiring view in Oryxa's DM.
- Onboarding provisions the Slack app.

**Still open:**

| Decision | Recommended default |
|---|---|
| Slack's terms on templated apps | Design partners only for now; start Marketplace review now |
| The shape of authority | The owner defines it; `Outranks` returns false until then |
| What "Give access for 1 hour" grants | Excerpts, plus the same asker's follow-ups in that thread; secrets as a one-time view |
| What work that starts in Slack may read | Knowledge now; Oryxa-brokered apps later; never Composio |
| Who sees a Deny | The asker, ephemerally |
| How answers are attributed in the thread | "From Raya's agent · approved by Raya (AI summary only)" |
| Ungrounded answers | Held for the owner's review |
| Job 2 writes approved from Slack | Allowed, showing the hint and arguments; destructive tools only in Oryxa |
| How a Slack identity is proven | Verified email match; OAuth linking optional |

Sources: [Slack: rate limits and terms for non-Marketplace apps](https://docs.slack.dev/changelog/2025/05/29/rate-limit-changes-for-non-marketplace-apps/) · [Slack: clarification](https://docs.slack.dev/changelog/2025/06/03/rate-limits-clarity/) · [Slack: handling interactions](https://docs.slack.dev/interactivity/handling-user-interaction) · [Bedrock contextual grounding](https://docs.aws.amazon.com/bedrock/latest/userguide/guardrails-contextual-grounding-check.html)
