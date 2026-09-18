# Knowledge service

The pipes between what an organization holds and the retrieval that reads it:
the sources a member asks to have indexed, the scheduled passes that extract
documents from them, the normalization that makes a document's hash mean
something, and the `oryxa.knowledge` events a retrieval pipeline holds. It
mounts into the single `oryxa` binary like every other service — see
[Mounting it](#mounting-it) and [Run locally](#run-locally).

**GitHub, Notion and Slack read through Composio.** Knowledge holds no
credential for any of them: a member connects an app through `apps`, which
owns the connection and its Composio connected account, and a source names
that connection at registration. Every call is proxied through Composio's own
API, never made directly, so this process never sees a token. These three are
mounted only when `COMPOSIO_API_KEY` is set; without it the binary mounts no
extractor for them, and `apps` need not even be running. The files source
reads the organization's own storage as the member who registered it, and
mounts once it has a way to act as one — see
[Sources](#sources).

Knowledge owns what has been extracted and when. Auth remains the identity and
membership boundary; apps owns a member's connections to third-party apps.
Everything knowledge extracts leaves as one event,
`knowledge.document.published.v1`, whichever source it came from, so a
retrieval pipeline indexes documents without knowing where they came from.

```text
member ── registers a source, naming a connection ──▶ knowledge
                                                          │
                                                          ├── GET  auth     /v1/organizations/{org}
                                                          ├── GET  apps     /v1/organizations/{org}/apps/connections/{id}
                                                          ├── POST composio /tools/execute/proxy ──▶ GitHub / Notion / Slack
                                                          └── oryxa.knowledge ──▶ the bus ──▶ retrieval

auth ── organization deleted, member removed ──▶ knowledge-events queue
apps ── connection expired, connection removed ──▶ knowledge-events queue
teams ── team deleted ──▶ knowledge-events queue
```

## The organization's own files index themselves

Files has no connection to announce it, so nothing ever said "start indexing
these". The first **organization-scoped** upload does: knowledge hears
`files.object.created.v1`, and registers the organization's files source once.
Every upload after that finds it already there, so the cost is one lookup and
no token.

Only a shared upload counts. A member's private file and a team's do not start
it, and the source itself asks files for the organization's own scope alone —
which matters more than it looks. A source here is organization-scoped, so
everything it publishes is searchable by the whole organization; files asked
with no scope answers the caller's own private files alongside the shared
ones, which would put one member's private files in front of everybody. It is
read as whoever stored the file, and only an admin can share a file with the
whole organization, so that view is an admin's: it carries the organization's
billing attachments, which files shows admins alone. The source never
publishes those, and refuses a target that asks for them.

A files source registered for a team (`{"team": "<id>"}`) is the team's: what
it reads is searchable by that team, not by the whole organization.

A source a member removed is not registered again. Removing it is them saying
their organization's files should not be indexed, and someone uploading
another file does not undo that.

## Connecting an app indexes what it holds

A member who connects GitHub means Krude to know about their repositories.
They used to have to come back afterwards, find the indexing panel and type a
repository name into a box — and until they did, connecting an app made
nothing searchable at all.

Knowledge hears `apps.connection.connected.v1`, asks apps what the connection
holds, and registers a source for each. It is the member's own work done while
they are away: the sources are theirs, read as them, under the standing grant
they gave auth. A member who never granted gets nothing registered and is no
worse off than before — they can still register a source by hand.

Auth answers every token a grant redeems to as an agent's, and an agent may
not choose what gets indexed. Nothing here is an agent choosing — knowledge
decided, on an event apps published — so that one rule is not applied to it;
every other check is, as the member, and a token acting as anyone else
registers nothing.

Bounded at ten, counting what the connection already has. Every source is a
scheduled pass against the provider for as long as it lives, so a member with
four hundred repositories is choosing which matter rather than having all of
them indexed on their behalf. The rest are left for them, at the same hourly
interval an automatic one takes. A target whose source a member removed is not
registered again: removing it was them saying no.

Nothing about it is essential to the connection, which is already made and
already usable, so most of what can go wrong is reported and dropped rather
than retried: apps or auth being briefly unreachable is worth another
delivery; a member who has not granted, or a refusal that will not change, is
not.

## Reading a source now

A schedule cannot know that a member just fixed a permission, or just changed
something at the provider and wants to see it indexed. `POST .../sync` is them
saying so: it moves the next sweep forward and answers, and the pass itself
runs on the worker that was going to run it anyway, so nothing waits on a
provider.

Unlike a trigger it does not care when the source was last read — a person
asking has a reason the schedule does not know, and refusing them because a
pass ran a minute ago would make the button a lie. A source already due sooner
is left alone rather than pushed back, and a paused one answers
`needs_reconnect`: what is wrong with it is not something hurrying fixes.

## What an app reports is swept at once

A trigger carries the app's own payload, in a shape that differs for every one
of Composio's thousands of triggers. Reading those here would mean a parser
per trigger, each a place to be wrong about scope, about what counts as the
same document, and about what is text — all of which the extractor for that
provider already knows.

So a delivery is not read. `apps.trigger.received.v1` is taken as what it is,
the app saying something in this connection changed, and the sources on that
connection become due now rather than at their next hour. What the trigger
reported is searchable through the same path as everything else, within
seconds instead of up to an interval.

Three rails. A trigger never delays a source already due sooner. A source
swept within the last two minutes is brought forward to two minutes after
that sweep rather than to now — a busy channel delivers faster than any
provider should be read, but what it reported should not then wait a whole
interval. And a source that is failing keeps its backoff: a provider refusing
it is not asked again on every delivery.

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
| `files` with a `connection_id` | It reads the organization's own storage, not a connected app |
| GitHub, Notion or Slack with no `connection_id` | There is nothing to proxy calls through |
| A connection for a different toolkit, one that is not `active`, or one that is not the caller's own | It could only ever fail once a run finally reached it |

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
| `github` | `{"repository": "<owner>/<name>"}` | Issues and pull requests, oldest updated first | GitHub's own page number |
| `notion` | `{"database": "<id>"}`, or nothing for every page the connection can see | Pages, or one database's rows across every data source it holds since the 2025-09-03 split | Notion's own `start_cursor`/`next_cursor` |
| `slack` | `{"channel": "<id>"}` | One channel's message history | Slack's own `cursor`, `oldest` a second before the watermark |
| `files` | `{"team": "<id>"}` or `{"purpose": "<purpose>"}`, or nothing | The organization's own stored files | Paged here: files answers its whole listing at once |

`github`, `notion` and `slack` take a `connection_id`, naming a connection a
member made through `apps`; every call is proxied through Composio's API on
the connected account behind it, never made directly. They mount only when
`COMPOSIO_API_KEY` is set.

`files` reads the organization's own storage as the member who registered it,
and takes no connection. A scheduled pass has no member at a browser, so it
redeems the standing agent grant they gave auth: `/internal/membertoken`
exchanges it for a short-lived token at auth's `/v1/internal/agent-tokens`,
holding each until shortly before it expires. It mounts once `FILES_URL` and
`KNOWLEDGE_SERVICE_KEY_ID` are set and auth lists knowledge in
`AUTH_SERVICE_CALLERS` with the `agent` purpose. Without either setting it
says which is missing at start and stays off; auth not listing knowledge shows
on first use, as a warning and a `forbidden` pass. A member who never granted
fails their own source with `grant_required` rather than the pass falling back
to anything wider, and it is tried again, with backoff, so it resumes once
they grant.

A files source reads the organization's files, or one team's when its target
names one, and publishes under that same scope. Billing attachments are never
read, whoever the source reads as.

Only text is indexed. A media type holding none is refused rather than stored
as though bytes were words.

HTML is read by taking its text out: the title, then the prose, with a line
wherever a block element begins or ends so a list does not run together into
one word nobody will search for. Script, style and comments are not prose and
do not survive; neither do attributes. What is stored is `text/plain`, because
that is what the body then is, and the hash is over those words rather than
the markup — so a provider that re-serializes the same page has not changed
it.

## API

Members authenticate with a bearer token or the `oryxa_access` cookie; a
cookie-authenticated change must come from an origin in `ALLOWED_ORIGINS`. A
source never shows its lease, version or attempt count.

| Method | Path | Who | Purpose |
| --- | --- | --- | --- |
| `GET` | `/healthz` | Anyone | Liveness |
| `GET` | `/v1/sources/providers` | Anyone | What this build can read |
| `GET` | `/v1/organizations/{organizationID}/sources` | Members | The caller's sources; `scope=organization` gives an org admin all of them; `connection_id` narrows to one connection |
| `POST` | `/v1/organizations/{organizationID}/sources` | Members | Register one: `{connection_id, provider, target, interval_seconds}` → `201`. `connection_id` names an `apps` connection for `github`, `notion` or `slack`; `files` takes none |
| `GET` | `/v1/organizations/{organizationID}/sources/{sourceID}` | Its creator or an org admin | One source, including its `connection_id` |
| `DELETE` | `/v1/organizations/{organizationID}/sources/{sourceID}` | Its creator or an org admin | Stop indexing it and retract what it held → `204` |
| `POST` | `/v1/organizations/{organizationID}/sources/{sourceID}/sync` | Its creator or an org admin | Read it now rather than at its next interval |
| `GET` | `/v1/organizations/{organizationID}/sources/{sourceID}/runs` | Its creator or an org admin | How recent passes went |
| `GET` | `/v1/organizations/{organizationID}/documents/{documentID}` | Its source's creator or an org admin | A document's body, for what an event was too small to carry |
| `GET` | `/v1/internal/organizations/{organizationID}/documents/{documentID}/body` | A named service | The same body, for a retrieval pipeline that has no member to be |

The internal route is how the largest documents get indexed at all. An event
carries a body up to 128 KiB and flags the rest as omitted, which left a
retrieval pipeline two bad choices: index nothing, or drop the version it
held because that version no longer matches. Both mean the handbook, the
specification and the long thread are the documents an organization cannot
search.

It is deliberately narrow: one document's body, named by an id nothing
guesses, to a caller that proved which service it is with a signed token
whose purpose is `documents`. It answers no listing, no search and nothing
about who may read the document — a service asking has no member to be, and a
route that answered more would be a way around the rules that decide what a
member sees. Without `KNOWLEDGE_SERVICE_CALLERS` there is no internal API at
all, and it answers 404 like any route that is not there.

| Code | Status | Means |
| --- | --- | --- |
| `invalid_request` | 400 | The provider, target or interval is not one knowledge takes |
| `unauthorized` | 401 | No member |
| `forbidden` | 403 | Not the source's creator or an org admin, or an agent choosing what to index |
| `not_found` | 404 | No such source or document — also what one the caller may not see answers |
| `needs_reconnect` | 409 | The connection behind the source must be reconnected before it can be read again |
| `scope_required` | 403 | The connection is missing a scope the source needs |
| `conflict` | 409 | The source is not active, or the named connection is not `active` |
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
| `knowledge.source.paused.v1` | A source's connection expired, or a run discovered it needs reconnecting before the event saying so arrived |

Every document carries its scope, team and member, because that is who may
read it: a consumer retrieving across an organization must honour it. A body
over `MaxBodyBytes` (128 KiB) is left out and flagged, and the document id
names what to fetch. No event ever carries a credential.

Knowledge consumes:

| Event | What it does |
| --- | --- |
| `auth.organization.deleted.v1` | Retires every source and retracts what they held |
| `auth.member.removed.v1` | Retires the sources that member registered, since extraction reads as them |
| `apps.connection.expired.v1` | Pauses every source reading through that connection; reconnecting is what resumes them |
| `apps.connection.removed.v1` | Retires every source reading through that connection and retracts what they held, since it is not coming back |
| `teams.team.deleted.v1` | Retires the deleted team's sources and retracts what they held. Rag removes the team's corpora on the same event; left running, the team's sources would publish into a new one |

Unlike the lifecycle handlers elsewhere in this repo, these are not naturally
idempotent — pausing a source and retracting documents both publish events a
retrieval pipeline acts on — so the envelope's `event_id` is checked before
anything is done and recorded once it succeeds.

Each retraction says why: `"connection_removed"` for a connection
disconnected on purpose, as against `"connection_lost"` for one whose
credential could not be renewed, and `"team_deleted"` for a deleted team, so a
consumer can tell a member why their documents went.

Rag is the one subscriber today: it indexes what `knowledge.document.published.v1`
publishes into a corpus per owner and retracts what `knowledge.document.removed.v1`
says is gone, entirely on its own side of the event — this package knows
nothing about corpora, embeddings or chunks. See
[rag/README.md, Knowledge documents](../rag/README.md#knowledge-documents).

## Database

Knowledge uses `/internal/postgreskit`. Other services never read these tables.

| Table | Holds |
| --- | --- |
| `knowledge_sources` | What to index, its placement and owner, the connection and Composio account a provider source reads through, its schedule, when it was last swept in full, and the lease one replica claims it under |
| `knowledge_documents` | What came back, with the content hash that is its version and when a pass last saw it |
| `knowledge_cursors` | Where the last pass stopped, and the newest change a completed pass saw |
| `knowledge_sync_runs` | What each pass did, so a member can see why nothing new arrived |
| `knowledge_handled_events` | Envelope ids already acted on, since the bus delivers at least once |
| `knowledge_outbox` | Events written in the same transaction as the change they describe |

## Security

- Knowledge stores no credential. A `github`, `notion` or `slack` source
  reads through Composio, which holds the connected account's credential
  itself: knowledge carries only its id, `composio_account_id`, and every
  call is proxied. `files` is read as the member who registered it, under the
  grant they gave, because files has no service identity to accept.
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
	Apps:       appsclient.New(config.AppsURL), // nil unless COMPOSIO_API_KEY is set
	Grants:     grants,
	Sink:       events.NewPublisher(eventBridge, config.EventBusName),
	Extractors: extractors,                     // knowledge.Builtin(composioClient), or nil
	Logger:     logger,
}, knowledge.Config{AllowedOrigins: config.AllowedOrigins, FullSweepInterval: config.FullSweepInterval})
if err != nil { return err }

if err := service.Migrate(ctx); err != nil { return err }
mux.Handle("/", service.Handler())
for _, worker := range service.Workers() {
	group.Go(func() error { worker(ctx); return nil })
}
```

Building the pool, the AWS clients and the Composio client stays the host's
job: a host running several services shares those rather than having each
build its own. `knowledge.Builtin` wires `github`, `notion` and `slack` onto
one shared HTTP client so pacing and rate-limit backoff are per toolkit across
all of them; `knowledge.Subscriptions()` says which events the queue it polls
must carry.

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
pause or retire a source when its connection, member or organization goes
away. Without `COMPOSIO_API_KEY` it starts with `github`, `notion` and `slack`
unmounted; without `FILES_URL` and `KNOWLEDGE_SERVICE_KEY_ID`, without
`files`.

## Deploy

Knowledge deploys as part of the single `oryxa` image, behind the same public
edge as every service it runs alongside. Its runtime identity needs
`events:PutEvents` on the event bus, and `sqs:ReceiveMessage` and
`sqs:DeleteMessage` on `knowledge-events`.

Deploy the binary before running the events provisioner again. The rule is
generated from what this build consumes, and a build that does not yet
handle an event type the rule already routes refuses it until it lands in
the dead-letter queue.

`apps.trigger.received.v1` carries the app's whole payload — a mail body, a
message — and knowledge reads only which connection it came from. It is
deleted from the queue once handled, but one that keeps failing sits in the
dead-letter queue for its retention; see TODO.md.

## Tests

Unit tests need nothing. The store tests need PostgreSQL; each migrates a
throwaway schema and drops it afterwards, so they are safe against a shared
development database.

```sh
go test ./knowledge/...
KNOWLEDGE_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./knowledge/...
```

## Later

