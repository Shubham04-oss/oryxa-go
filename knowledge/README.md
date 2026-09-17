# Knowledge service

The pipes between what an organization holds and the retrieval that reads it:
the sources a member asks to have indexed, the scheduled passes that extract
documents from them, the normalization that makes a document's hash mean
something, and the `oryxa.knowledge` events a retrieval pipeline holds. It
mounts into the single `oryxa` binary like every other service — see
[Mounting it](#mounting-it) and [Run locally](#run-locally).

**No source can run today.** Knowledge's GitHub, Notion and Slack sources read
through the connections service, and were archived with it on 2026-09-17 while
app connections are rebuilt on Composio; the code is at tag
`archive/connections-and-integrations-2026-09-17`. The files source remains,
but it reads as the member who registered it and needs a grant to do that,
which is not built yet — see [Later](#later). The binary therefore mounts no
extractor, and every registration is refused as a provider nothing reads.

Knowledge owns what has been extracted and when. Auth remains the identity and
membership boundary. Files owns the organization's own storage. Everything
knowledge extracts leaves as one event, `knowledge.document.published.v1`,
whichever source it came from, so a retrieval pipeline indexes documents
without knowing where they came from.

```text
member ── registers a source ──▶ knowledge ── reads pages ──▶ files
                                    │
                                    ├── GET  auth  /v1/organizations/{org}
                                    ├── GET  files /v1/organizations/{org}/files
                                    └── oryxa.knowledge ──▶ the bus ──▶ retrieval

auth ── organization deleted, member removed ──▶ knowledge-events queue
```

## Sources are registered, not discovered

A source is one thing to index, such as the organization's own files. A member
registers it with their own credentials and every later run works from what
was stored. That matches what indexing is: a choice, not a crawl of everything
an organization holds.

Everything that would make a later run impossible is therefore checked at
registration, while a member is still present to be told:

| Refused at registration | Because |
| --- | --- |
| A provider nothing reads | Nothing would ever run |
| A target the extractor does not understand | The first pass would fail and keep failing |

## Extraction

A pass either drains a source or stops somewhere it can resume from. Both
halves of resuming matter, and they are not the same:

- **A cursor is saved per page.** A pass that dies carries on where it stopped
  rather than reading the provider again.
- **A watermark is not.** A source that answers newest first gives a watermark
  on page one that is already the newest of the whole window; persisting it
  mid-walk would make the next run ask only for things after it and skip
  everything this pass had not reached. It moves only when the source is
  drained.

For the same reason, only a full pass retracts what it did not see — an
incremental one has not looked at everything, so it has no standing to say
anything is missing. A source is swept in full again, not only on its first
pass, once its last sweep is older than `KNOWLEDGE_FULL_SWEEP_INTERVAL` (a Go
duration, a week by default), so a document deleted at the provider is
eventually noticed even long after a source's first pass.

A document is published when it is first seen and whenever its content hash
changes, and not otherwise, so a pass over an unchanged source publishes
nothing. Normalizing before hashing is what makes that true: a provider that
re-serializes a document, changes its line endings or pads it differently has
not changed it.

**Leases.** A pass can outlast any window worth leasing for, so the lease is
renewed between pages rather than held for the whole walk, and every write
that ends a run is conditional on still holding it. A replica that lost its
claim writes nothing, schedules nothing, and leaves the source alone.

**What goes wrong is told apart by what would fix it.** A source that is
down backs off, doubling each time, and a run records whether trying again
unchanged could do better. A cancelled context is the process going away, not
the source failing, so nothing is recorded against it.

## Sources

| Source | Target | Reads | Paging |
| --- | --- | --- | --- |
| `files` | `{"team": "<id>"}` or `{"purpose": "<purpose>"}`, or nothing | The organization's own stored files | Paged here: files answers its whole listing at once |

The files source is built and tested but not mounted, because it has no grant
to read as. The provider sources that sat beside it — `github`, `notion` and
`slack`, with the paced, retrying HTTP client they shared — are at the archive
tag above.

Only text is indexed. A media type holding none is refused rather than stored
as though bytes were words; HTML is not read yet (see [Later](#later)).

## API

Members authenticate with a bearer token or the `oryxa_access` cookie; a
cookie-authenticated change must come from an origin in `ALLOWED_ORIGINS`. A
source never shows its lease, version or attempt count.

| Method | Path | Who | Purpose |
| --- | --- | --- | --- |
| `GET` | `/healthz` | Anyone | Liveness |
| `GET` | `/v1/sources/providers` | Anyone | What this build can read |
| `GET` | `/v1/organizations/{organizationID}/sources` | Members | The caller's sources; `scope=organization` gives an org admin all of them |
| `POST` | `/v1/organizations/{organizationID}/sources` | Members | Register one: `{provider, target, interval_seconds}` → `201` |
| `GET` | `/v1/organizations/{organizationID}/sources/{sourceID}` | Its creator or an org admin | One source |
| `DELETE` | `/v1/organizations/{organizationID}/sources/{sourceID}` | Its creator or an org admin | Stop indexing it and retract what it held → `204` |
| `GET` | `/v1/organizations/{organizationID}/sources/{sourceID}/runs` | Its creator or an org admin | How recent passes went |
| `GET` | `/v1/organizations/{organizationID}/documents/{documentID}` | Its source's creator or an org admin | A document's body, for what an event was too small to carry |

| Code | Status | Means |
| --- | --- | --- |
| `invalid_request` | 400 | The provider, target or interval is not one knowledge takes |
| `unauthorized` | 401 | No member |
| `forbidden` | 403 | Not the source's creator or an org admin, or an agent choosing what to index |
| `not_found` | 404 | No such source or document — also what one the caller may not see answers |
| `conflict` | 409 | The source is not active |
| `credential_rejected` | 422 | The source refused the credential it was read with |
| `rate_limited` | 429 | Too many requests; retry |
| `provider_unavailable` | 503 | The source, or a service knowledge depends on, did not answer |

## Events

Knowledge publishes source `oryxa.knowledge` through the outbox, in the
envelope every Oryxa publisher uses. Contracts are in
`contracts/events/knowledge/v1`.

| Detail type | When |
| --- | --- |
| `knowledge.document.published.v1` | A document was first seen, or its content changed |
| `knowledge.document.removed.v1` | A document is gone from its source, or its source was retired |
| `knowledge.sync.completed.v1` | A pass finished, with what it did |
| `knowledge.sync.failed.v1` | A pass stopped, with the reason and whether it can resume |
| `knowledge.source.paused.v1` | Nothing publishes it today: provider sources paused when their connection went away |

Every document carries its scope, team and member, because that is who may
read it: a consumer retrieving across an organization must honour it. A body
over `MaxBodyBytes` (128 KiB) is left out and flagged, and the document id
names what to fetch. No event ever carries a credential.

Knowledge consumes:

| Event | What it does |
| --- | --- |
| `auth.organization.deleted.v1` | Retires every source and retracts what they held |
| `auth.member.removed.v1` | Retires the sources that member registered, since extraction reads as them |

Unlike the lifecycle handlers elsewhere in this repo, these are not naturally
idempotent — retracting documents publishes events a retrieval pipeline acts
on — so the envelope's `event_id` is checked before anything is done and
recorded once it succeeds.

## Database

Knowledge uses `/internal/postgreskit`. Other services never read these tables.

| Table | Holds |
| --- | --- |
| `knowledge_sources` | What to index, its placement and owner, its schedule, when it was last swept in full, and the lease one replica claims it under |
| `knowledge_documents` | What came back, with the content hash that is its version and when a pass last saw it |
| `knowledge_cursors` | Where the last pass stopped, and the newest change a completed pass saw |
| `knowledge_sync_runs` | What each pass did, so a member can see why nothing new arrived |
| `knowledge_handled_events` | Envelope ids already acted on, since the bus delivers at least once |
| `knowledge_outbox` | Events written in the same transaction as the change they describe |

## Security

- Knowledge stores no credential. A source is read as the member who
  registered it, under the grant they gave, because files has no service
  identity to accept.
- A source's own error body never reaches an event, a log or the API; only
  the reason a caller can act on does.

## Mounting it

Knowledge mounts into `internal/host` like every other service.
`knowledge.Definition` names it; `knowledge.Migrate` installs its schema;
`knowledge.Mount` reads knowledge's own variables through the host's
environment, borrows the shared Postgres pool and AWS configuration, builds
the auth client and the event publisher, and hands back its handler, the
routes its server registered, its timeouts and its workers:

```go
var definitions = []host.Definition{
	knowledge.Definition,
	// the other services this binary carries
}
```

Underneath, `knowledge.New` is what actually assembles it, and still touches
nothing itself — no query, no goroutine, no listener — which is what
`knowledge.Mount` calls:

```go
service, err := knowledge.New(knowledge.Dependencies{
	Store:      store,
	Directory:  authclient.New(config.AuthURL),
	Grants:     grants,
	Sink:       events.NewPublisher(eventBridge, config.EventBusName),
	Extractors: extractors,
	Logger:     logger,
}, knowledge.Config{AllowedOrigins: config.AllowedOrigins, FullSweepInterval: config.FullSweepInterval})
if err != nil { return err }

if err := service.Migrate(ctx); err != nil { return err }
mux.Handle("/", service.Handler())
for _, worker := range service.Workers() {
	group.Go(func() error { worker(ctx); return nil })
}
```

Building the pool and the AWS clients stays the host's job: a host running
several services shares those rather than having each build its own.
`knowledge.Subscriptions()` says which events the queue it polls must carry.

## Provision

Copy the root `.env.example` to the ignored `.env` and fill in knowledge's
variables. Then run:

```sh
(set -a && . ./.env && go run ./knowledge/infra/events/cmd/provision)
```

The command is repeatable. It creates or updates the `knowledge-events` queue
with its dead-letter queue, and a `knowledge-events` rule on `EVENT_BUS_NAME`
whose target is the queue and which the queue policy admits alone. Put the
printed `queue_url` in `KNOWLEDGE_EVENTS_QUEUE_URL`.

The embedded migrations are in `internal/postgres/migrations/` and are
applied in name order when `oryxa` starts, unless `ORYXA_AUTO_MIGRATE=false`,
or ahead of time with `go run ./cmd/oryxa migrate`. The emulators forget
EventBridge state when they restart, so re-run the provisioner after one.

## Run locally

```sh
docker compose up -d
(set -a && . ./.env && go run ./knowledge/infra/events/cmd/provision)
(set -a && . ./.env && ORYXA_SERVICES=knowledge go run ./cmd/oryxa)
```

Without `KNOWLEDGE_EVENTS_QUEUE_URL` knowledge still starts, but it will not
retire a source when its member or organization goes away.

## Deploy

Knowledge deploys as part of the single `oryxa` image, behind the same public
edge as every service it runs alongside. Its runtime identity needs
`events:PutEvents` on the event bus, and `sqs:ReceiveMessage` and
`sqs:DeleteMessage` on `knowledge-events`.

## Tests

Unit tests need nothing. The store tests need PostgreSQL; each migrates a
throwaway schema and drops it afterwards, so they are safe against a shared
development database.

```sh
go test ./knowledge/...
KNOWLEDGE_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./knowledge/...
```

## Later

- **App sources on Composio.** Reading GitHub, Notion, Slack and the rest
  again, through Composio's connected accounts rather than the archived
  connections service. Composio never hands out a token, so an extractor will
  call through it rather than with a credential of its own.
- **The grant.** `Grants` is an interface with no implementation yet; minting
  a member-scoped token is auth's job, and `agents/primary`'s `memberauth` is
  the working precedent. Until one is supplied, the files source cannot be
  mounted.
- **HTML.** Only plain text, Markdown, CSV and JSON are read. HTML would mean
  promoting `golang.org/x/net` from an indirect dependency, which is a
  deliberate choice rather than an oversight.
