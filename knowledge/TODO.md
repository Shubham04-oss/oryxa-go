# Knowledge: what is left, and what to watch

Written alongside the first build of this service. It records what is
deliberately unfinished, what is finished but incomplete, and the few places
worth a second pair of eyes. `README.md` describes what the service does; this
describes what it does not do yet and why.

Ordered by what stops you first.

---

## 1. What still blocks every pass

The wiring is done. `knowledge/service.go` exposes `Definition`, `Migrate`
and `Mount` — the shape `cmd/oryxa` mounts every service with, proven first
by `teams` and now adopted here. `knowledge/infra/events` provisions its
queue. What is left is not a wiring gap; it is work nobody has done yet.

The root `.env.example` documents knowledge's older variables but not yet
`APPS_URL`, `COMPOSIO_API_KEY` or `COMPOSIO_BASE_URL` — outside `knowledge/`,
so worth a follow-up rather than something this pass could reach.

**GitHub, Notion and Slack read again, through Composio.** The connections
service they used to read through is gone for good; these three now proxy
every call through Composio's API on the connected account named by an
`apps` connection, and `application.Apps` (`internal/appsclient`) reads that
connection at registration. They mount only once `COMPOSIO_API_KEY` (and
`APPS_URL`) are configured; unset, the binary mounts no extractor for them,
same as `files` today. `internal/providerapi`'s generated GitHub and Notion
clients are not restored — both are hand-rolled on the shared paced client
the way Slack already was, since Composio holds the credential and a
generated client has nowhere to place one.

**`files` is indexed again (fixed).** `application.Grants` now has an
implementation: `/internal/membertoken` redeems a member's standing agent
grant at auth's `/v1/internal/agent-tokens`, the same route
`agents/primary/internal/memberauth` has always used, and holds each token
until shortly before it expires so a pass over many sources asks auth once.

`Mount` registers the files source once `FILES_URL` and
`KNOWLEDGE_SERVICE_KEY_ID` are both set; without either it says which is
missing and leaves the source off, as before. The deployment also has to
name knowledge in auth's `AUTH_SERVICE_CALLERS` with the `agent` purpose —
auth refuses a service it does not know, and that reads as
`membertoken.ErrNotConfigured` rather than as a source that quietly never
runs.

Two things to know about it. A member who never granted answers
`ErrGrantRequired`, which no retry fixes and which stops that source rather
than falling back to anything wider. And `/internal/membertoken` duplicates
`agents/primary/internal/memberauth`, which cannot be imported from outside
`agents/primary`; the two speak the same route and should become one.

---

## 2. Deletions are noticed again after a periodic full sweep (fixed)

`application/sync.go` used to decide a pass was a backfill only when the
source had no watermark, and only a backfill retracted what it did not see.
Once a source completed one full pass it had a watermark forever, so every
later pass was incremental, and `RetractUnseen` was never called again — a
document deleted at the provider on day two stayed in the index indefinitely.

A pass is now also full — starting from the beginning and, once drained,
retracting what it did not see — when a source's sweep has come due: no
`knowledge_sources.full_sweep_at` yet, or one older than
`KNOWLEDGE_FULL_SWEEP_INTERVAL` (a Go duration, weekly by default; see §8.3).
`full_sweep_at` itself is recorded once such a pass drains. The watermark rule
did not change: it still moves only when a source is drained, never mid-walk,
and a walk already under way keeps what it saved rather than restarting from
the beginning mid-pass. `TestSyncRetractsWhatAFullPassDidNotFind` and
`TestSyncNeverRetractsOnAnIncrementalPass` still pin a fresh source's first
pass and an ordinary incremental one; `TestSyncRetractsAfterASweptSourceComesDueAgain`
pins the periodic sweep itself.

---

## 3. Finished but incomplete

| Gap | Where | What it costs |
| --- | --- | --- |
| **HTML is not extractable** | `internal/transform` | Only plain text, Markdown, CSV and JSON are read. HTML means promoting `golang.org/x/net` from an indirect dependency to a direct one — a deliberate choice left open rather than made unilaterally. |
| **Team files need team ids you have to already know** | `internal/filesclient` | Files never returns team-scoped objects unless a team is named, and offers no way to enumerate teams. Indexing everything an organization holds means asking `teams` first. A source naming one team works fine today. |
| **One run's ceiling is ~20k documents** | `internal/application/sync.go` | `pagesPerRun` (200) × `pageSize` (100). A source with more gives the worker back and resumes next tick, which is the intent — but a first backfill of a very large repository takes several intervals to complete. A full pass spanning several runs retracts against when the whole walk began, not against whichever run happens to finish it (`knowledge_cursors.walk_started_at`), so an earlier run's own reads are never mistaken for what the provider no longer has. |

---

## 4. Worth a second look

Nothing here is known-wrong. They are the judgement calls I would want
someone else to confirm.

- **Document bodies live in Postgres, up to 4 MiB each.** `transform.MaxBytes`
  caps one document; nothing caps a source. For a large corpus this table
  grows fast, and the body is only there to serve
  `GET /v1/organizations/{org}/documents/{id}` for events too big to carry
  inline. Object storage may be the better home once volume is real.

---

## 5. Efficiency, once volume is real

- **`Files.Fetch` re-lists the whole organization on every page.** Files
  answers its entire listing at once and takes no cursor, so paging happens on
  our side — but each page asks again. For an organization with thousands of
  files that is quadratic. Fix by carrying the listing across pages within one
  run, which needs the extractor to hold per-run state the seam does not offer
  yet.

---

## 6. Test coverage gaps

The layers that carry the real logic are covered: the store against a real
Postgres, transform, the sync semantics, the event dedupe, the error
mapping.

What is not:

- **No end-to-end test.** Other services have an `internal/e2e` that runs the
  HTTP API, the application, the store and the adapters together against a
  fake provider. This service should have one; now that the entrypoint
  exists, it is the test most likely to catch a wiring mistake.

---

## 7. For whoever is building the retrieval side

The contract is `contracts/events/knowledge/v1`. Three things in it are easy
to miss and expensive to get wrong:

- **Scope travels on every document and is not decoration.** `Scope`, `TeamID`
  and `MemberID` say who may read that document. A retrieval pipeline
  answering across an organization has to honour them, or a member-scoped
  source's documents leak to the whole org.
- **`ContentHash` is the version.** A document is published on first sight and
  whenever its hash changes, so upsert on `(SourceID, ExternalID)`. Delivery
  is at least once — dedupe on the envelope's `event_id`, or treat an equal
  hash as a no-op.
- **`BodyOmitted` means fetch it.** Bodies over 128 KiB are not carried;
  `GET /v1/organizations/{org}/documents/{DocumentID}` returns the full text.

`knowledge.document.removed.v1` fires whenever a full pass — a source's
first, or a periodic sweep at most `KNOWLEDGE_FULL_SWEEP_INTERVAL` apart —
finds something the provider no longer has; see §2.

---

## 9. HTML is read (fixed)

`transform` read plain text, Markdown, CSV and JSON, and refused everything
else — so a GitHub issue, a Notion page or a mail body delivered as HTML was
skipped in silence. `golang.org/x/net` is a direct dependency now and
`transform/html.go` takes the text out: the title first, then the prose, with
a line break wherever a block element begins or ends. Script, style,
`noscript`, `template`, `svg` and comments hold no prose and are dropped;
attributes are never indexed.

Two things worth keeping in mind if this is changed. The stored media type is
`text/plain`, not `text/html`, because the body is no longer markup and must
not claim to be. And the hash is over the extracted text — that is what keeps
a provider re-serializing a page from looking like a change, which is the
whole reason a pass over thousands of unchanged documents publishes nothing.

## 10. Trigger payloads in knowledge's queue

Knowledge consumes `apps.trigger.received.v1` only to learn which connection
changed, but the event carries the app's whole payload, so mail bodies and
messages pass through `knowledge-events` and, for an event that keeps failing,
sit in its dead-letter queue for its retention. Apps wipes the same payloads
from its own outbox once published. An EventBridge input transformer on a
separate rule for this detail type could hand knowledge the connection alone;
it needs the provisioner to create two rules rather than one.

## 8. Decisions that are not mine to make

1. **Promote `golang.org/x/net` to a direct dependency for HTML?** Decided:
   yes. HTML is read now — see §9.
6. **Whose view should the organization's files source read?** Open, and
   deliberately parked. It reads as whoever stored the first shared file —
   always an admin, since only an admin can share a file with the whole
   organization. The one thing an admin's view adds to organization-scoped
   files is billing attachments, and the source skips those itself, so what
   it publishes is what every member could already see. What stays open is
   availability: that member can leave, and their grant goes with them, and
   nothing says the first uploader should be the one the whole
   organization's index depends on. The alternatives — an organization-level
   grant, the owner, a service identity files would have to accept — each
   cost something different. Revisit before this is in front of real
   organizations.
2. **Adopt the `Name`/`Migrate`/`Handler`/`Workers` seam across the other
   seven services?** Decided: yes. It was proven here because this one was
   greenfield; `teams` adopted it first (`teams/service.go`), and this
   service now follows with `Definition`, `Migrate` and `Mount` in
   `knowledge/service.go` and `knowledge/mount.go` — the shape `internal/host`
   expects from every service it mounts.
3. **How often should a source do a full sweep?** Decided: it is a setting,
   `KNOWLEDGE_FULL_SWEEP_INTERVAL` in `knowledge/internal/serviceconfig`,
   defaulting to weekly (168h) rather than a cadence fixed in code — see §2.
4. **Where do document bodies live long-term?** Postgres today; see §4.
5. **Should background workers drain on shutdown everywhere?** Decided: yes,
   and it turned out not to be each service's call — `internal/host`'s
   `serve` stops every mounted service's workers together and waits for them
   on the way down, so the gap this bullet pointed at is closed process-wide,
   not only for the service that happened to hand its workers back.
