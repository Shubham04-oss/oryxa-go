# Remaining work

Where the connections fixes and the integrations catalogue stand, what is
known to be unfinished, and what is only suspected. Written at the end of the
session that closed the six connections gaps and added HubSpot.

Nothing here is a promise about behaviour. Where something was confirmed by a
command or a document it says so; where it was only noticed in passing it says
that instead.

## Done, and how it was checked

`feat/connections-providers`, six commits.

The connections gaps were verified against a live PostgreSQL rather than by
reading code — which mattered, because the whole `postgres` and `e2e` suite
had been skipping for want of `CONNECTIONS_TEST_DATABASE_URL` and nothing
compensated for it. With a database attached, 297 connections tests run.

| Gap | What was wrong | Fixed in |
| --- | --- | --- |
| `AddHold` vs credential deletion | Unlocked `EXISTS` let a hold land after the deletion cleared them; reproduced 39 of 40 attempts | `7ee9a89` |
| Outbox `Data` validation | nil detail → `23502`, empty → `22P02`, aborting the business transaction | `7ee9a89` |
| `SharesProviderGrant` index | Read every connection of the organization; 1000 rows scanned and discarded | `7ee9a89` |
| Lease left on disconnect | Four paths left a renewal lease set, delaying credential cleanup | `7ee9a89` |
| Outbox claim TTL / ownership | Claims were anonymous, so a stale dispatcher could clear a live one | `2bc041c` |
| Outbox retention | Published rows were never removed | `2bc041c` |

Each has a test that was confirmed to fail before the fix. The single-use
session claim was **not** changed: it is a deliberate trade against exchanging
a code twice, so a test now pins the behaviour and its reasoning.

Two framework pieces for the catalogue: `ProviderBaseURL` for domain-routed
providers, and `Registration.Secret` for providers that issue their own
signing secret. HubSpot is built with triggers and six actions.

## Open: decisions only the owner can make

### 1. How should a provider that cannot sign its deliveries behave?

Five of the 13 providers cannot have a verified delivery, each for a different
reason, all confirmed against provider documentation or OpenAPI specs:

| Provider | Why |
| --- | --- |
| Google Drive | No signature exists. Security is an unguessable channel token; the body is always empty |
| Atlassian (Jira) | Bearer JWT whose algorithm and claims Atlassian does not document |
| Notion | Signs strongly (`X-Notion-Signature`), but zero webhook paths in its OpenAPI spec — subscriptions are dashboard-only and the URL is immutable |
| Confluence | No webhook API at all; descriptor-only |
| Asana | Only the framework gap, which is now closed |

The three options, with their costs:

- **Actions only** for these five. Actions need no delivery verification, so
  Google, Notion and Atlassian get a useful surface now. No security
  compromise. This is the recommendation.
- **Accept Drive's unguessable-token model.** Nango shipped this and it
  produced a security advisory titled "Unverified Google push notifications are
  forwarded to customer webhook endpoints with a valid Nango signature".
- **Reverse-engineer Atlassian's webhook JWT.** Needs a live Jira site to
  capture a delivery, and the result would rest on undocumented behaviour.

This was raised twice and not answered, so nothing was built for those five.

## Open: unresolved research questions

From `docs/research/`, which flags roughly 20 items `[UNCONFIRMED]`. The ones
that block or shape code:

- **HubSpot subscription auth contradicts itself.** The OpenAPI `security`
  block declares OAuth scopes `developers-write`/`private-apps-write`; the prose
  guide says a Developer API key via `?hapikey`. Unresolved.
- **A HubSpot private app cannot manage webhooks through the API at all**
  (documented verbatim: "not currently supported"). The provider is complete
  and tested, but whether an organization can *register* a subscription depends
  on its app type: a legacy public app can, a private app cannot, and a
  project-based app uses a config file.
- **HubSpot's deals path is inconsistent inside its own documentation.** The
  OpenAPI says `/crm/objects/2026-09/0-3`, the prose guide says
  `/crm/objects/2026-09/deals`. This is why no deals action was written.
- **Google Drive `watch` max TTL is not published.** The ~24h figure is
  community-reported only.
- **Confluence 3LO paths:** Atlassian's spec uses `/wiki/rest/api/...` while its
  3LO guide writes `/rest/api/space`. The sources conflict.
- **Atlassian dynamic webhooks expire after 30 days** and need a scheduled
  refresh; OAuth2 allows only 5 webhooks per app per user per tenant.

## Open: not yet surveyed at all

Seven providers have connections but have not been checked for whether they can
have a verified delivery or what their actions would be: **Airtable, Box,
Dropbox, Figma, Intercom, Salesforce, Zendesk.**

Surveying them is the same work that already paid off twice — it is what
established that five of the 13 cannot be built, and it caught that Atlassian's
`/rest/api/3/search` is fully removed rather than deprecated and that HubSpot's
current path generation is `2026-09` rather than `v3`. All three would have
been live bugs.

## Open: a cross-cutting problem, fixed in only one service

**The outbox pattern is duplicated across seven services** — `auth`, `billing`,
`files`, `integrations`, `teams`, `agents/primary` and `connections` — and the
anonymous-claim and missing-retention problems found in connections exist in
all of them. Only connections was fixed, because it was the service in scope.

The string form of that fix belongs in `/internal/postgreskit` so all seven
share one implementation. Seven near-identical patches is the worse outcome.
This was flagged and deliberately not started, because it touches services
outside the task.

## Open: test coverage known to be missing

### Integrations

- **No end-to-end test for HubSpot.** The provider and its actions have unit
  tests against fake servers; the e2e stack (`internal/e2e`) covers GitHub,
  Slack, Linear and Microsoft only.
- **No test for the `Registration.Secret` path at the application layer.** The
  store behaviour is tested (replace, keep, read-back), but nothing drives a
  provider that returns a secret through `createWebhookSubscription`.
- **No live HubSpot test.** Nothing has run against the real HubSpot API.

### Connections

These were identified by comparing the implementation against the tests, and
none has been closed:

- The **worker loops** — `RunHeartbeat`, `RunCleanup`, `runEvery`, `jitter` —
  are never exercised; only the inner `CheckHealth` and `CleanUp` are. Batching,
  jitter, cancellation and error logging are unverified.
- `probe` branches for **non-OAuth2** credentials (client-credentials mint,
  installation, key-based identity): the heartbeat test uses an OAuth2
  connection only.
- `ErrInstallationNotFound` → `installation_removed` is unexercised;
  `fakeOAuth.installationErr` and `installationTokErr` are declared but never
  set, so `workers.go` and `token.go` paths that read them never run.
- The **revocation-failure** path and the `revokeGiveUp` fallback are untested:
  `fakeOAuth.revokeErr` is never set, so an OAuth2
  `credentials-deleted` event with `revoked: false` is never produced.
- `mintErr` is never set, so a mint failure during connect or during a
  client-credentials renewal is untested.
- `CheckHealth`'s `ErrNeedsReconnect` skip branch is not reached by any test.
- `httpserver.decode`'s `maxJSONBytes` limit is untested — unknown fields and
  trailing objects are covered, an oversized body is not.
- `postgres.Open` with `POSTGRES_REQUIRED_EXTENSIONS` is untested; the test
  helpers pass none.
- Real KMS, EventBridge and SQS are never contacted; the e2e stack substitutes
  AES-GCM for KMS, so the production envelope wiring in `connections/service.go`
  is unverified end to end. Its test mounts the service but contacts nothing.

## Local branch hygiene

- `feat/rag` is a stale pointer identical to `main`, carrying no RAG work.
- `feat/connections-service` and `feat/connections-work-tracking` are merged
  into `main` but still exist locally.
- `CLAUDE.md` is untracked by design and was left that way.
- The working tree needs `GOPATH` and `GOCACHE` pointed inside the workspace to
  build under a sandbox that cannot write `$HOME`; both are gitignored.
