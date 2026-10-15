# Connector strategy: native big five, Composio long tail, Pipedream fallback

*Plan, 2026-10-14, for the owner to decide on. It turns the owner's decision
of 2026-10-14 into steps; it changes no code. Background:
[integration-providers-2026-10.md](integration-providers-2026-10.md) (who
should serve tools and triggers) and [integrations-plan.md](integrations-plan.md)
(phases 1 and 2, the gates G1–G4).*

## The decision, restated

| Apps | Served by | Why |
|---|---|---|
| **GitHub, Google Drive, Notion, Slack, Gmail** (the big five) | **Oryxa's own integrations**: our sign-in, our token vault, official SDKs | They carry nearly all trigger volume and all of knowledge's indexing. Native calls and events cost nothing per call, and indexing through a broker's proxy is billed per read (Pipedream: ~$120 to backfill 10k emails). |
| Everything else Composio has | **Composio** | $0.0003 a tool call; thousands of apps we'll never build. |
| Apps Composio lacks | **Pipedream Connect** | Fallback only, already built (#205–#210) and on in Pipedream's development environment. |

Google Calendar has a native manifest too (`apps/internal/integrations/googlecalendar`).
It isn't in the big five; this plan leaves it on Composio until a member asks
for calendar indexing.

## 1. Today in production

What the code says is live; the counts need a read-only query I can't run
from a1, so **macbookpro, please fill in the table** (no secrets: counts by
app only).

- **Connections:** every member connection is Composio's, apart from
  Pipedream ones made in its development environment (Pipedream workspace
  members only). `APPS_ORYXA_TOOLKITS` is empty, so no connect goes native.
- **Triggers:** Composio triggers deliver to `apps`, which publishes
  `apps.trigger.received.v1`; batching wakes Krude.
- **Knowledge:** GitHub, Notion, Slack, Drive and Gmail sources read through
  Composio's proxy (`knowledge/internal/sources/*`, `POST /tools/execute/proxy`);
  the apps reader (`sources/apps.go`, #139) reads native connections through
  apps' `/pages`, and has nothing to read yet.

| App | Live Composio connections | Active Composio triggers | Knowledge sources on it | Trigger events last 30 days |
|---|---|---|---|---|
| GitHub | ? | ? | ? | ? |
| Google Drive | ? | ? | ? | ? |
| Notion | ? | ? | ? | ? |
| Slack | ? | ? | ? | ? |
| Gmail | ? | ? | ? | ? |
| All others | ? | ? | n/a | ? |

```sql
-- apps' and knowledge's own tables; read-only.
SELECT toolkit, broker, count(*) FROM apps_connections WHERE status = 'active' GROUP BY 1, 2 ORDER BY 1, 2;
SELECT toolkit, broker, count(*) FROM apps_triggers WHERE status = 'active' GROUP BY 1, 2 ORDER BY 1, 2;
SELECT provider, count(*) FROM knowledge_sources WHERE status = 'active' GROUP BY 1 ORDER BY 1;
```

## 2. Routing

Nothing new is needed: apps already picks one broker per app for **new**
connects (apps/README, "Which broker serves an app"):

1. Oryxa's own sign-in, if `APPS_ORYXA_TOOLKITS` names the app;
2. else Composio, if its catalog has the app;
3. else Pipedream, if this deployment has Pipedream and its catalog has the app.

`APPS_TOOLKIT_BROKERS` (`github=composio,linear=pipedream`) pins an app over
all of that. **A connection keeps the broker it was made through**, and every
Composio path refuses a non-Composio connection, so switching an app never
sends an old connection to the wrong broker.

So turning a toolkit on is one step: add it to `APPS_ORYXA_TOOLKITS` (gate G4).
Turning it off is removing it: new connects go back to Composio at once, and
native connections already made keep working until their members remove them.

## 3. Building the five

One toolkit per PR, on the #141 framework (board item "Native connectors for
the big five"), in this order, easiest and least restricted first:

| # | Toolkit | SDK | Reads (knowledge) | Events | Restricted? |
|---|---|---|---|---|---|
| 1 | GitHub | go-github | repos, READMEs, issues, PRs | webhooks | no |
| 2 | Google Drive | `google.golang.org/api/drive/v3` | files; Docs/Sheets/Slides exported as text | changes API (poll) | **yes** (`drive`) |
| 3 | Notion | REST through safehttp | pages, databases | poll (search by `last_edited_time`) | no |
| 4 | Slack | slack-go | allowed channels' history and threads | Events API | no (Slack's review is only for its directory) |
| 5 | Gmail | `google.golang.org/api/gmail/v1` | messages, threads | history API (+ Pub/Sub push) | **yes** (`gmail.modify`) |

Each passes G1 (`go run ./apps/internal/integration/cmd/check <toolkit>`:
readers, read-only tools, events, cassette tests), G2 review, G3 the owner's
live smoke test with real accounts on the a1 stack, then G4: macbookpro adds
it to `APPS_ORYXA_TOOLKITS` in production with the owner's OAuth client.

Estimate: GitHub ~2 days, Drive ~3, Notion ~2, Slack ~3, Gmail ~3, plus a
day each for G3. About three weeks of build; Gmail and Drive go live only
after Google verification (section 5).

## 4. Moving existing connections

**A Composio connection can't be carried over:** Composio doesn't export
tokens, and our tokens come from our own OAuth clients anyway. Every member
reconnects once per app. The aim is that this is one click and loses nothing.

**Per toolkit, once it passes G4:**

1. **Day 0: new connects go native.** Existing Composio connections keep
   working; nothing changes for their members yet.
2. **Day 0: ask once.** A notice on the app's card and in Settings → Apps,
   plus one email: "Reconnect GitHub to keep it working after <date>. One
   click." It deep-links to the native connect.
3. **On reconnect, keep everything.** The new connection takes over:
   - **knowledge sources** on the old connection are re-pointed to the new
     one, keeping the source's id, placement, sharing and documents, so
     nothing is re-shared and only changed documents are re-indexed. This
     needs one new call in knowledge ("replace a source's connection"),
     owned by whoever holds knowledge; it's the only code this plan asks
     outside apps.
   - **triggers** are recreated as the native toolkit's events with the same
     configuration where a mapping exists (e.g. GitHub `push`, Slack
     `message`); unmapped ones are listed for the member to set up again.
   - **agent grants** naming the app carry over (they name the app, not the
     connection).
   - then the Composio connection is removed, which revokes it at Composio.
4. **Day 30: second notice** to those left.
5. **Day 45: Composio off for that app.** Pin it native with
   `APPS_TOOLKIT_BROKERS`; remaining Composio connections are marked
   expired, their sources pause (documents kept), and their cards say
   "Reconnect". Nothing is deleted.
6. **Day 60: remove the code path** (knowledge's Composio extractor for that
   app), once its count is zero.

Order: GitHub first (no restricted scopes, developers forgive a reconnect),
Notion and Slack next, Drive and Gmail last, after Google's verification.

## 5. What the owner sets up, per toolkit

One callback for all: `APPS_SIGNIN_REDIRECT_URI` =
`https://app.oryxa.in/v1/apps/oauth/callback` (staging: its own origin).
Client ids and secrets go in `APPS_DEFAULT_OAUTH_CLIENTS` through the
secrets script, never in chat.

| Toolkit | Create | Scopes (from each manifest) | Review needed before strangers can connect | Lead time |
|---|---|---|---|---|
| GitHub | OAuth app (Oryxa org) | `repo`, `read:org` | none | a day |
| Notion | Public integration | none (capabilities: read content, read comments, no user emails) | make it public; Notion's review only for its gallery | a day |
| Slack | **A new app** for members' own Slack, separate from the Oryxa bot app | user scopes `channels:read`, `groups:read`, `channels:history`, `groups:history`, `chat:write`, `users:read` | public distribution (no review); Marketplace only for listing | a day |
| Google Drive | Google Cloud OAuth client (shared by all Google toolkits) | `drive` (**restricted**) | **OAuth verification + CASA Tier 2** | 6–10 weeks |
| Gmail | same client | `gmail.modify` (**restricted**) | **OAuth verification + CASA Tier 2** | same review as Drive |

**Why a second Slack app:** the bot app (`apps/internal/slackbot`) is installed
by a workspace admin for the consent loop; members' own connections need user
scopes and each member's own consent. Mixing them would put history scopes on
the bot install. (Board item says to ask in Discussions before reusing it.)

**Google verification and CASA** (both restricted scopes, one review for the
shared client):
- Until verified, the consent screen shows "unverified app" and caps at 100
  users; fine for our org and design partners, not for launch.
- Needs: a verified oryxa.in domain, privacy policy and terms that describe
  Drive and Gmail data, a demo video, and a **CASA Tier 2** assessment by an
  authorized lab (self-scan plus lab validation), repeated yearly.
- Timeline: submit in week 1 of the build; verification 2–4 weeks; CASA 4–8
  weeks in parallel; budget roughly $500–$4,500 for the lab (quotes vary).
- **Cheaper alternative for Drive:** `drive.file` is not restricted, but
  reaches only files a member picks with the Picker. Indexing "my whole
  Drive" needs `drive` (or `drive.readonly`, also restricted).

## 6. Cost

Assumptions are the providers doc's: 50 tool calls and 200 trigger events per
member per day across all apps, and the big five carrying 90% of both once
native. Composio list prices (Pro: $29/month with 100k tool calls and 50k
trigger events, then $0.0003 a call and $0.003 an event). **Replace "today"
with section 1's real numbers.**

| Members | Native big five (our infra) | Composio, long tail only | Composio, if the big five stayed on it |
|---|---|---|---|
| ~50 (today, assumed) | ~$0 extra | **$29** (within Pro) | ~$850 |
| 500 (10×) | ~$20 (workers, polls, storage) | **~$780** | ~$9,000 |
| 5,000 (100×) | ~$200 | **~$9,100** | ~$90,000 |

Native cost is our own: the poll worker, webhook ingress and token vault on
the existing ECS service and Postgres, and Pub/Sub for Gmail push (pennies at
these volumes). The real cost is engineering and Google's yearly review.

**Pipedream:** the Startup plan's $99/month floor (100 external users, 10k
credits) buys apps Composio lacks. While only Pipedream's development
environment is on (free, workspace members only), it costs nothing; moving to
production is worth it only once members ask for such apps. **Recommendation:
keep it in development and off for members until there's demand.**

## 7. Risks and rollback

| Risk | Mitigation |
|---|---|
| Google verification is slow or refused | Gmail and Drive stay on Composio meanwhile; nothing breaks. Fallback for Drive: `drive.file` with the Picker. |
| A native toolkit has a bug in production | Remove it from `APPS_ORYXA_TOOLKITS`: new connects go to Composio at once. Native connections keep working, or are expired, and members reconnect. |
| Members don't reconnect | Two notices, then sources pause with documents kept; one click resumes. |
| Token vault holds members' credentials now | KMS-sealed per secret (phase 1), revocation on removal and member leave, daily health checks; reviewed in #141. |
| Rate limits (Gmail and Drive quotas, Notion's 3 req/s, Slack tier 3) | Readers page with cursors and back off; the poll worker spreads sources; per-toolkit limits in each manifest. |
| Composio's old-pricing grace ends 2026-12-31 | The big five off Composio by then removes ~90% of the bill; check our sign-up date. |

## 8. Decisions for the owner

1. **Gmail and Drive scopes:** apply now for the restricted `gmail.modify`
   and `drive` (verification plus CASA, about 6–10 weeks and a yearly
   assessment), or start Drive on `drive.file` (picker only, no review) and
   index Gmail later?
2. **The reconnect deadline:** turn Composio off for each of the five 45 days
   after its native version ships (recommended), or keep both paths
   indefinitely?
3. **Pipedream:** keep it in its development environment only (recommended,
   $0), or pay $99/month for production now?
