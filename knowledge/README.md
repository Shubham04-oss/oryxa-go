# Knowledge service

The pipes between what an organization holds and the retrieval that reads it:
the sources a member asks to have indexed, the scheduled passes that extract
documents from them, the normalization that makes a document's hash mean
something, and the `oryxa.knowledge` events a retrieval pipeline holds. It
mounts into the single `oryxa` binary like every other service — see
[Mounting it](#mounting-it) and [Run locally](#run-locally).

**GitHub, Notion, Slack, Google Drive and Gmail read through Composio.**
Knowledge holds no credential for any of them: a member connects an app
through `apps`, which owns the connection and its Composio connected account,
and a source names that connection at registration. Every call is proxied
through Composio's own API, never made directly, so this process never sees a
token. These five are mounted only when `COMPOSIO_API_KEY` is set; without it
the binary mounts no extractor for them, and `apps` need not even be running.
The files source reads the organization's own storage as the member who
registered it, and mounts once it has a way to act as one — see
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
                                                          ├── POST composio /tools/execute/proxy ──▶ GitHub / Notion / Slack / Drive / Gmail
                                                          └── oryxa.knowledge ──▶ the bus ──▶ retrieval

auth ── organization deleted, member removed ──▶ knowledge-events queue
apps ── connection expired, connection removed ──▶ knowledge-events queue
teams ── team deleted ──▶ knowledge-events queue
```

## Shared, team and private files index themselves

Files has no connection to announce it. The first organization-scoped upload
starts a shared Files source; the first team-scoped upload starts one for that
team; the first member-scoped upload starts one for that member. Knowledge
hears `files.object.created.v1` and registers each source through the
uploader's standing grant. Later uploads
schedule the existing source to sync promptly.

A deletion is heard the same way: `files.object.deleted.v1` retracts the
file's document from every files source in the organization at once, and
deletes its original, instead of leaving it searchable until the next full
sweep notices it missing, up to a week later.

The shared source asks Files for organization scope alone. A private source
asks for member scope and checks every returned object's member ID against
its own recorded owner before reading its bytes. A team source asks for that
team alone and checks every returned object's team ID. These checks also
protect a source reading with an admin's broader Files view. Only an admin
can share a file with the whole organization, so the shared
source reads an admin's view: it carries billing attachments and other files
explicitly marked `access: admins`.
The source carries Files' access label into the document event; RAG keeps
restricted files out of member listings and search results. An admin's source
may also target only billing attachments.

A files source registered for a team (`{"team": "<id>"}`) is the team's: what
it reads is searchable by that team's members and organization admins.
A member can register `{"member": "self"}` to index their own private
files; a target cannot name another member.

A source explicitly removed by a member is not registered again by another
upload. A source retired because its registering member left can be replaced
by the next uploader.

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

Some providers do better than that. Drive's changes feed and Gmail's history
both say outright, as part of the very page that reports what changed, that a
particular document is now gone — `Page.Removed`, a list of external ids an
extractor is certain of. Sync retracts those at once rather than waiting for
the next full sweep: a file trashed in Drive or a message deleted in Gmail
stops being searchable within the source's ordinary interval, not up to a
week later. `github`, `notion`, `slack` and `files` have no such feed and
never set it; nothing changes for them.

A provider's own incremental cursor is not always a moment. Drive hands back
`changes.startPageToken`, an opaque string; Gmail hands back a `historyId`,
a number it treats as opaque too. `Cursor.Token` carries this the way
`Cursor.Watermark` carries a time-based provider's position — read back to
start the next incremental pass, moved only once a pass drains — so a source
with a token behind it is exactly as "already read through" as one with a
watermark for deciding whether the next pass is full or incremental.

A document is published when it is first seen, when its normalized content
changes, or when its title, URL, kind, MIME type, or source metadata changes.
A pass over a truly unchanged source publishes nothing. Normalizing before
hashing avoids republishing a provider's harmless serialization or line-ending
changes. When the judge is enabled, Knowledge also records its semantic
category as `knowledge_category` in document metadata; that category describes
the content and does not grant access to it.

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
| `googledrive` | `{"folder": "<id>"}`, or nothing for the whole of the member's Drive | Google Docs, Sheets and Slides as text; PDF, Word, Excel and PowerPoint files as their original bytes | Drive's own `pageToken`, plus a breadth-first queue of subfolders when scoped to a folder |
| `gmail` | `{"label": "<id>"}`, or nothing for `INBOX` | One label's messages | Gmail's own `pageToken` |
| `files` | `{"team": "<id>"}` or `{"purpose": "<purpose>"}`, or nothing | The organization's own stored files | Paged here: files answers its whole listing at once |

`github`, `notion`, `slack`, `googledrive` and `gmail` take a `connection_id`,
naming a connection a member made through `apps`; every call is proxied
through Composio's API on the connected account behind it, never made
directly. They mount only when `COMPOSIO_API_KEY` is set.

### Google Drive

`GET .../drive/v3/files` lists everything the account can see — Drive's own
default corpus already covers "the whole of my Drive" without this walking
anything — with a `q` of `trashed = false`, paged by `pageToken`, asking for
`fields=nextPageToken,files(id,name,mimeType,modifiedTime,webViewLink,size,md5Checksum,trashed,parents)`.
A folder target instead asks `q='<id>' in parents and trashed = false`
against that one folder, and Drive's query language has no operator for
"under this folder, however deep": a subfolder found in a page is queued
rather than descended into immediately, so a walk scoped to a folder visits
every folder under it breadth first, one cursor carrying which folder is
current, Drive's own page token within it, and everything still queued.

A Google Doc or Slide is read with `GET .../files/<id>/export?mimeType=text/plain`;
a Sheet the same way with `mimeType=text/csv`, since that is what its rows
actually are. Drive can reportedly export a Doc as `text/markdown` too, which
would read better, but nothing here has confirmed that against a live
account, so this asks for `text/plain`, which every version of the API has
always answered — see TODO.md. A PDF, a `.doc`, `.docx`, `.xls`, `.xlsx`,
`.ppt` or `.pptx` file is downloaded whole with `GET .../files/<id>?alt=media`
and kept as-is; Drive told its size in the same listing that named it, so
anything over 50 MiB is never downloaded at all, only counted as skipped.
Everything else Drive holds — an image, a video, a Form, a Site, a Drawing —
is skipped the same way. Composio never returns a file's bytes inline for
either call: it stores the file and answers a link, which the shared client
follows itself (`sources.AllowBinary`, `composio.go`) rather than refusing the
way every other call here does.

An incremental pass reads `GET .../drive/v3/changes`, from the token a full
pass leaves behind (`GET .../changes/startPageToken`, asked once a walk
drains). A changed file still in scope is read the same way a full pass would
read it; one Drive reports `removed` or `trashed` is retracted at once
through `Page.Removed`. Scoped to a folder, a change is kept only when the
file's own `parents` names that folder directly — matching a file nested in a
subfolder would mean this pass already knowing the whole current tree, which
only a full walk actually has, so that is left to the next full sweep
instead, documented rather than solved.

### Gmail

`GET .../gmail/v1/users/me/messages` lists a label's messages —
`labelIds=INBOX` by default — and, only on a source's very first pass,
`q=newer_than:90d`: recent mail is useful the moment a connection is made,
and reading years of history before anything is searchable would make a
heavy mailbox's first connection look hung. A periodic re-sweep of a source
already read once carries no such bound, because a message that has simply
aged past ninety days between one sweep and the next must not look deleted
just because a bounded re-sweep stopped seeing it. Each message is then read
with `GET .../messages/<id>?format=full`; the document is the message itself
— title the subject, text its `text/plain` part or, missing that, its
`text/html` part stripped through the same transform an HTML page from any
other source reads through — with its from, to, date and thread id as
metadata, and its Gmail web link as its URL. Attachments are left out
entirely: nothing here follows an attachment's id to fetch what it names.

An incremental pass reads `GET .../users/me/history`, from the `historyId`
the previous pass left (`GET .../users/me/profile`, asked once a walk
drains), for `messageAdded`, `messageDeleted` and `labelRemoved`. A message
added to the label is read in full; one deleted outright, or one that merely
left this label — archived out of `INBOX`, say — is retracted through
`Page.Removed` either way, since this source is exactly that label. Gmail
keeps history for about a week; a `startHistoryId` older than that answers
404, which reads as a fresh watermark rather than a failure, and the next
periodic full sweep is what actually catches anything missed in between.

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

A files source reads the organization's files, one team's files, or its
member's private files and publishes under that same scope. Billing
attachments are published with admin-only access.

Only text is indexed. A media type holding none is refused rather than stored
as though bytes were words — unless it is a PDF or an Office file and
`KNOWLEDGE_ORIGINALS_BUCKET` is configured, in which case its own bytes are
kept instead of refused; see [Originals](#originals).

HTML is read by taking its text out: the title, then the prose, with a line
wherever a block element begins or ends so a list does not run together into
one word nobody will search for. Script, style and comments are not prose and
do not survive; neither do attributes. What is stored is `text/plain`, because
that is what the body then is, and the hash is over those words rather than
the markup — so a provider that re-serializes the same page has not changed
it.

## Originals

A PDF or an Office file holds no text this package reads itself, and used to
be skipped outright — counted, never published. With `KNOWLEDGE_ORIGINALS_BUCKET`
set, one is kept instead: its bytes go to S3 at
`originals/<organization_id>/<source_id>/<document_id>/<sha256>`, content
addressed so a changed document is a new object and a reader never sees one
half written, and the document is published with `Original{Bucket, Key,
Size, SHA256}` (`contracts/events/knowledge/v1`) rather than a `Body` —
`MIME` is the file's real type, and `ContentHash` is `Original.SHA256`. A
retrieval backend that can parse such a file, such as a Bedrock knowledge
base, reads it from there; one that cannot still gets everything else this
service already publishes. Without the bucket configured, nothing about this
changes: such a file is skipped exactly as before.

Two sources produce these today. Drive's own PDF, Word, Excel and PowerPoint
files pass through as-is (see [Google Drive](#google-drive)). The
organization's own files source (`files`) does the same for a stored PDF or
Office file, but only once the bucket is configured — unlike Drive, it checks
first, the same way it already avoided fetching any file's bytes until its
declared type said they were worth reading. Either way, a file over 50 MiB
(`sources.MaxOriginalBytes`) is never kept: counted as skipped, the same as a
type nothing here reads at all.

Before writing a new version, the previous document with the same external id
is read back (`DocumentByExternalID`) — cheap, and it means a weekly full
sweep over a file that has not actually changed re-uploads nothing, and that
the object's key carries the document's own stable id rather than a
throwaway one that would not survive an update. The old object is deleted
once the new one is safely published, and whenever a document carrying an
Original is itself retracted — a full sweep no longer seeing it, `Page.Removed`
saying so at once, or the source, connection, team, member or organization it
belonged to going away — its object is deleted with it. A failed delete is
logged and not retried: the row is already correct, and a byte nothing will
read again is a smaller problem than failing a sync run or a removal over it.

`KNOWLEDGE_ORIGINALS_BUCKET` is knowledge's own bucket; nothing outside this
service reads or writes it directly. `AWS_ENDPOINT_URL_S3` (or the generic
`AWS_ENDPOINT_URL`) points it at a local emulator instead of AWS.

## API

The internal body-fetch contract used by RAG is
[`contracts/http/knowledge/v1/openapi.yaml`](../contracts/http/knowledge/v1/openapi.yaml).

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
| `knowledge_documents` | What came back, with its content hash, searchable provenance, and when a pass last saw it |
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
signer, err := registry.Signer(knowledge.Name) // the host's eventauth registry
if err != nil { return err }
sink, err := events.NewPublisher(eventBridge, config.EventBusName, signer)
if err != nil { return err }
service, err := knowledge.New(knowledge.Dependencies{
	Store:      store,
	Directory:  authclient.New(config.AuthURL),
	Apps:       appsclient.New(config.AppsURL), // nil unless COMPOSIO_API_KEY is set
	Grants:     grants,
	Sink:       sink,
	Verifier:   registry.Verifier(), // proves who published each event the consumer handles
	Extractors: extractors,          // knowledge.Builtin(composioClient), or nil
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
build its own. `knowledge.Builtin` wires `github`, `notion`, `slack`,
`googledrive` and `gmail` onto one shared HTTP client so pacing and
rate-limit backoff are per toolkit across all of them; `knowledge.Subscriptions()`
says which events the queue it polls must carry.

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
away. Without `COMPOSIO_API_KEY` it starts with `github`, `notion`, `slack`,
`googledrive` and `gmail` unmounted; without `FILES_URL` and
`KNOWLEDGE_SERVICE_KEY_ID`, without `files`; without
`KNOWLEDGE_ORIGINALS_BUCKET`, a PDF or an Office file is skipped rather than
kept — see [Originals](#originals).

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
