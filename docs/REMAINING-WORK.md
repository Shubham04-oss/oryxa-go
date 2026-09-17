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

- **No live HubSpot test.** Nothing has run against the real HubSpot API.

### Connections

The eight gaps this section used to list (worker loops, non-OAuth2 probing,
the two `ErrInstallationNotFound` paths, the revoke-failure fallback,
`mintErr`, the `ErrNeedsReconnect` skip branch, `maxJSONBytes`, and
`postgres.Open`'s required extensions) were closed in this round with new
tests; none needed a behaviour change. The worker-loop and `httpserver`
tests need no database and were run directly. The `maxJSONBytes` one was
also confirmed to fail with the limit loosened. The five DB-backed tests —
four new ones in `service_integration_test.go` plus
`TestOpenRequiredExtensions` in `postgres` — were only read against the
code, not run: like the rest of those two suites, they need
`CONNECTIONS_TEST_DATABASE_URL` and were left for the lead to run. What is
left is unchanged from before: real KMS,
EventBridge and SQS are still never contacted.

- The **worker loops** — `RunHeartbeat`, `RunCleanup`, `runEvery`, `jitter` —
  are covered in `internal/application/workers_test.go`: draining full
  batches before sleeping, jitter's bounds (including the zero-spread edge
  case), prompt return on cancellation, waiting about the interval between
  short batches, and logging then continuing after an error. `RunHeartbeat`
  and `RunCleanup` are each checked to actually delegate to `runEvery` with
  `CheckHealth`/`CleanUp` as their work, by driving them against the fake
  store (which leaves `ClaimHealthChecks`/`ClaimCredentialDeletions`
  unimplemented on purpose) and reading the logged failure back out.
- `probe`'s **non-OAuth2** branches (client-credentials mint, installation
  identity, key-based identity) are covered by
  `TestHeartbeatNonOAuth2Credentials` in `service_integration_test.go`, each
  with a passing check and a permanent-failure check.
- `ErrInstallationNotFound` → `installation_removed` is exercised on both
  paths that read it: `TestHeartbeatNonOAuth2Credentials` sets
  `fakeOAuth.installationErr` for `workers.go`'s probe, and
  `TestInstallationTokenRenewalFailure` sets `installationTokErr` for
  `token.go`'s renewal.
- The **revocation-failure** path and the `revokeGiveUp` fallback are covered
  by `TestRevocationFailureKeepsCredentialsUntilRevokeGiveUp`: with
  `fakeOAuth.revokeErr` set, a disconnected connection keeps its credentials
  while cleanup retries the revoke, and only past `revokeGiveUp` are they
  deleted unrevoked, with `revoked: false` on the `credentials-deleted` event
  and a logged warning.
- `mintErr` is covered by `TestClientCredentialsMintFailures`: a rejected
  mint during `CreateConnection` leaves nothing behind, and one during a
  client-credentials renewal (driven through `IssueToken`, mirroring
  `TestIssueTokenRenewalFailures`'s OAuth2 case) marks the connection
  `needs_reconnect`.
- `CheckHealth`'s `ErrNeedsReconnect` skip branch is reached by
  `TestHeartbeatNonOAuth2Credentials`'s client-credentials failure case: the
  renewal inside `probe` already records the failure, so the check itself
  must be recorded as skipped rather than failed.
- `httpserver.decode`'s `maxJSONBytes` limit is covered by
  `TestDecodeRejectsAnOversizedBody`, alongside a normal-sized body to show
  the limit is not rejecting everything.
- `postgres.Open` with required extensions is covered by
  `TestOpenRequiredExtensions`, an integration test that skips without
  `CONNECTIONS_TEST_DATABASE_URL` like the rest of this package's `postgres`
  suite: a required extension that is installed is accepted, and a name the
  database does not recognise as installed is refused, naming it.

Still open:

- Real KMS, EventBridge and SQS are never contacted; the e2e stack
  substitutes AES-GCM for KMS, so the production envelope wiring in
  `connections/service.go` is unverified end to end. Its test mounts the
  service but contacts nothing.

## Local branch hygiene

- `feat/rag` is a stale pointer identical to `main`, carrying no RAG work.
- `feat/connections-service` and `feat/connections-work-tracking` are merged
  into `main` but still exist locally.
- `CLAUDE.md` is untracked by design and was left that way.
- The working tree needs `GOPATH` and `GOCACHE` pointed inside the workspace to
  build under a sandbox that cannot write `$HOME`; both are gitignored.
