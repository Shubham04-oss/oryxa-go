# Knowledge service

Status: built as a library, not yet wired. The pipes between an organization's
connected apps and the retrieval that reads them: the sources a member asks to
have indexed, the scheduled passes that extract documents from them, the
normalization that makes a document's hash mean something, and the
`oryxa.knowledge` events a retrieval pipeline holds. Not built yet: the
entrypoint, the queue provisioner, and the environment and compose files — see
[Later](#later).

Knowledge owns what has been extracted and when. Connections owns the apps and
credentials, and knowledge asks it for one whenever it reads a provider. Auth
remains the identity and membership boundary. Files owns the organization's
own storage. Everything knowledge extracts leaves as one event,
`knowledge.document.published.v1`, whichever provider it came from, so a
retrieval pipeline indexes documents without knowing a provider.

```text
member ── registers a source ──▶ knowledge ── reads pages ──▶ provider
                                    │
                                    ├── POST connections /v1/internal/connections/{id}/token
                                    ├── GET  auth        /v1/organizations/{org}
                                    ├── GET  files       /v1/organizations/{org}/files
                                    └── oryxa.knowledge ──▶ the bus ──▶ retrieval

auth, connections ── organization deleted, member removed, ──▶ knowledge-events queue
                     connection disconnected, app deleted
```

## Sources are registered, not discovered

A source is one thing to index: a repository, a workspace, a channel, the
organization's own files. A member registers it with their own credentials and
every later run works from what was stored.

This is not a preference. There is no service-authenticated way to list an
organization's connections — the only listing route is member-credentialed,
and even an org admin never sees team-scoped connections without naming a
team. A background worker cannot find out what exists, so it is told. That
also matches what indexing is: a choice, not a crawl of everything an
organization ever connected.

Everything that would make a later run impossible is therefore checked at
registration, while a member is still present to be told:

| Refused at registration | Because |
| --- | --- |
| A provider nothing reads | Nothing would ever run |
| A target the extractor does not understand | The first pass would fail and keep failing |
| A connection that is not active | It cannot be read now and may never be |
| A connection for a different provider | The credential would not fit the API |
| An organization connection reserved for admins | Connections refuses a bare service every purpose but `cleanup`, and extraction runs long after the member has gone |

## Extraction

A pass either drains a source or stops somewhere it can resume from. Both
halves of resuming matter, and they are not the same:

- **A cursor is saved per page.** A pass that dies carries on where it stopped
  rather than reading the provider again.
- **A watermark is not.** Slack and Notion answer newest first, so a watermark
  taken from page one is already the newest of the whole window; persisting it
  mid-walk would make the next run ask only for things after it and skip
  everything this pass had not reached. It moves only when the source is
  drained.

For the same reason, only a full pass retracts what it did not see — an
incremental one has not looked at everything, so it has no standing to say
anything is missing.

A document is published when it is first seen and whenever its content hash
changes, and not otherwise, so a pass over an unchanged source publishes
nothing. Normalizing before hashing is what makes that true: a provider that
re-serializes a document, changes its line endings or pads it differently has
not changed it.

**Leases.** A pass can outlast any window worth leasing for, so the lease is
renewed between pages rather than held for the whole walk, and every write
that ends a run is conditional on still holding it. A replica that lost its
claim writes nothing, schedules nothing, and leaves the source alone.

**What goes wrong is told apart by what would fix it.** A provider that is
down backs the source off, doubling each time. A credential that must be
reconnected pauses the source outright and drops what was held for it, because
trying again unchanged cannot work. A cancelled context is the process going
away, not the source failing, so nothing is recorded against it.

## Providers

| Provider | Target | Reads | Paging |
| --- | --- | --- | --- |
| `github` | `{"repository": "owner/name"}` | Issues and pull requests | Page number, `since` for incremental |
| `notion` | `{"database": "<id>"}`, or nothing for the workspace | Page titles and properties | Notion's own cursor |
| `slack` | `{"channel": "C0123ABC"}` | Channel messages | Slack's cursor, `oldest` for incremental |
| `files` | `{"team": "<id>"}` or `{"purpose": "<purpose>"}`, or nothing | The organization's own stored files | Paged here: files answers its whole listing at once |

Everything resilient is knowledge's own: nothing in this repo paginates,
retries, backs off or reads a rate-limit header. Calls to a host are paced, a
provider asking for a pause is obeyed whether it says so in `Retry-After` or
in when its budget refills, and a pause longer than a minute ends the run
rather than sleeping through it.

Only text is indexed. A media type holding none is refused rather than stored
as though bytes were words; HTML is not read yet (see [Later](#later)).

## API

Members authenticate with a bearer token or the `oryxa_access` cookie; a
cookie-authenticated change must come from an origin in `ALLOWED_ORIGINS`. A
source never shows its lease, version or attempt count.

| Method | Path | Who | Purpose |
| --- | --- | --- | --- |
| `GET` | `/healthz` | Anyone | Liveness |
| `GET` | `/v1/providers` | Anyone | What this build can read |
| `GET` | `/v1/organizations/{organizationID}/sources` | Members | The caller's sources; `scope=organization` gives an org admin all of them |
| `POST` | `/v1/organizations/{organizationID}/sources` | Members | Register one: `{connection_id, provider, target, interval_seconds}` → `201` |
| `GET` | `/v1/organizations/{organizationID}/sources/{sourceID}` | Its creator or an org admin | One source |
| `DELETE` | `/v1/organizations/{organizationID}/sources/{sourceID}` | Its creator or an org admin | Stop indexing it and retract what it held → `204` |
| `GET` | `/v1/organizations/{organizationID}/sources/{sourceID}/runs` | Its creator or an org admin | How recent passes went |
| `GET` | `/v1/organizations/{organizationID}/documents/{documentID}` | Its source's creator or an org admin | A document's body, for what an event was too small to carry |

| Code | Status | Means |
| --- | --- | --- |
| `invalid_request` | 400 | The provider, target or interval is not one knowledge takes |
| `unauthorized` | 401 | No member |
| `forbidden` | 403 | Not the source's creator, not an org admin, or a connection a background reader could never use |
| `scope_required` | 403 | The connection is missing a scope |
| `not_found` | 404 | No such source or document — also what one the caller may not see answers |
| `needs_reconnect` | 409 | The connection must be reconnected first |
| `conflict` | 409 | The connection is not active |
| `credential_rejected` | 422 | The provider refused the connection's credential |
| `rate_limited` | 429 | Too many requests; retry |
| `provider_unavailable` | 503 | The provider, or a service knowledge depends on, did not answer |

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
| `knowledge.source.paused.v1` | A source stopped because what it read through went away |

Every document carries its scope, team and member, because that is who may
read it: a consumer retrieving across an organization must honour it. A body
over `MaxBodyBytes` (128 KiB) is left out and flagged, and the document id
names what to fetch. No event ever carries a credential.

Knowledge consumes:

| Event | What it does |
| --- | --- |
| `connections.connection.disconnected.v1` | Pauses the sources reading through it; documents are kept, since it may come back |
| `connections.connection.needs-reconnect.v1` | The same, before the next pass discovers it the hard way |
| `connections.app.deleted.v1` | Pauses everything reading through that app's connections |
| `auth.organization.deleted.v1` | Retires every source and retracts what they held |
| `auth.member.removed.v1` | Retires the sources that member registered, since extraction reads as them |

Unlike the lifecycle handlers elsewhere in this repo, these are not naturally
idempotent — pausing a source and retracting documents both publish events a
retrieval pipeline acts on — so the envelope's `event_id` is claimed before
anything is done.

## Database

Knowledge uses `/internal/postgreskit`. Other services never read these tables.

| Table | Holds |
| --- | --- |
| `knowledge_sources` | What to index, its placement and owner, its schedule, and the lease one replica claims it under |
| `knowledge_documents` | What came back, with the content hash that is its version and when a pass last saw it |
| `knowledge_cursors` | Where the last pass stopped, and the newest change a completed pass saw |
| `knowledge_sync_runs` | What each pass did, so a member can see why nothing new arrived |
| `knowledge_handled_events` | Envelope ids already acted on, since the bus delivers at least once |
| `knowledge_outbox` | Events written in the same transaction as the change they describe |

## Security

- Knowledge stores no provider credential. It asks connections for one as
  `action`, signing each call with its own KMS key, and holds it only until
  two minutes before it lapses — the window connections renews inside. A
  key-based secret is never held at all.
- Every outbound provider call has a 60-second timeout, an 8 MiB response cap
  and no redirects, so a credential never follows one somewhere it was not
  issued for.
- A provider's own error body never reaches an event, a log or the API; only
  the reason a caller can act on does.
- Reading the organization's own files acts as the member who registered the
  source, under the grant they gave, because files has no service identity to
  accept.

## Mounting it

Knowledge is a library. `knowledge.New` assembles it and touches nothing:

```go
service := knowledge.New(knowledge.Dependencies{
	Store:       store,
	Directory:   authclient.New(config.AuthURL),
	Connections: connectionsclient.New(config.ConnectionsURL, signer),
	Grants:      grants,
	Sink:        events.NewPublisher(eventBridge, config.EventBusName),
	Extractors:  knowledge.Builtin(),
	Logger:      logger,
}, knowledge.Config{AllowedOrigins: config.AllowedOrigins})

if err := service.Migrate(ctx); err != nil { return err }
mux.Handle("/", service.Handler())
for _, worker := range service.Workers() {
	group.Go(func() error { worker(ctx); return nil })
}
```

Building the pool, the AWS clients and the signer stays the host's job: a host
running several services shares those rather than having each build its own.
`knowledge.Subscriptions()` says which events the queue it polls must carry.

Connections must allow knowledge to call it, with the purposes it uses:

```json
CONNECTIONS_SERVICE_CALLERS=[{"service": "knowledge", "key_id": "alias/oryxa-knowledge-identity", "purposes": ["action"]}]
```

This is read once at startup, so connections needs a restart, and the KMS
signing key must exist first. Without it every pass fails at the first token.

## Tests

Unit tests need nothing. The store tests need PostgreSQL; each migrates a
throwaway schema and drops it afterwards, so they are safe against a shared
development database.

```sh
go test ./knowledge/...
KNOWLEDGE_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./knowledge/...
```

## Later

- **The entrypoint.** There is no `cmd/knowledge`, no `.env.example` and no
  compose file, because the services are being consolidated into a single
  binary and that wiring is what changes. `knowledge/service.go` is the shape
  that wiring mounts.
- **The queue provisioner.** `infra/events` — the queue, its dead-letter
  queue, and the EventBridge rule built from `Subscriptions()` — following the
  pattern every other service here uses.
- **The grant.** `Grants` is an interface with no implementation yet; minting
  a member-scoped token is auth's job, and `agents/primary`'s `memberauth` is
  the working precedent. Until one is supplied, a files source says so rather
  than failing somewhere less obvious.
- **HTML.** Only plain text, Markdown, CSV and JSON are read. HTML would mean
  promoting `golang.org/x/net` from an indirect dependency, which is a
  deliberate choice rather than an oversight. Microsoft Graph mail and
  Notion's richer blocks wait on it.
- **Notion's block content.** A page's body is its title and properties;
  rendering block children means a recursive call per page.
- **Incremental breadth.** Notion's search takes no `since` filter, so an
  incremental pass reads the same breadth as a full one. Unchanged hashes mean
  it publishes nothing, but it costs the same to find that out.
- **More providers.** Connections connects 17; four are read here. Box,
  Zendesk, Intercom and Figma already have generated clients.
