# Knowledge: what is left, and what to watch

Written alongside the first build of this service. It records what is
deliberately unfinished, what is finished but incomplete, and the few places
worth a second pair of eyes. `README.md` describes what the service does; this
describes what it does not do yet and why.

Ordered by what stops you first.

---

## 1. Nothing runs until these exist

None of this is a bug. The service was built as a library on purpose, because
the services are being consolidated into a single binary and the wiring is
exactly what that changes. `knowledge/service.go` is the shape that wiring
mounts.

| Missing | Where it goes | Notes |
| --- | --- | --- |
| Entrypoint | `knowledge/cmd/knowledge/` | Or a mount in the shared binary's composition root. `service.go` already exposes `Name`/`Migrate`/`Handler`/`Workers`/`Consumer`. |
| Queue provisioner | `knowledge/infra/events/` | Queue, dead-letter queue, and an EventBridge rule built from `knowledge.Subscriptions()`. Copy `integrations/infra/events/` and rename. |
| Environment template | `knowledge/.env.example` | Only needed once there is a process to configure. |
| Compose overlay | `compose.knowledge.yaml` | Optional — `integrations` has none and runs with `go run`. |

**Two prerequisites live outside this repo and outside this service.** Both
fail loudly rather than quietly, but both fail everything.

- **Connections must allow knowledge to call it.** An operator adds
  `{"service": "knowledge", "key_id": "alias/oryxa-knowledge-identity", "purposes": ["action"]}`
  to `CONNECTIONS_SERVICE_CALLERS` on the **connections** deployment, and the
  KMS signing key must exist first. It is read once at startup, so connections
  needs a restart. Without it every pass fails at the first token request.
- **`application.Grants` has no implementation.** It mints a member-scoped
  token for an internal source, because `files` has no service identity —
  every route there wants a member's own credentials. Until one is supplied,
  a files source fails with a clear message and every provider source works
  fine. `agents/primary/internal/memberauth` is the working precedent for
  minting one under auth's agent grant.

---

## 2. Deletions stop being noticed after the first pass

**This is the most significant functional gap and it is not obvious from
reading any single file.**

`application/sync.go` decides a pass is a backfill when the source has no
watermark, and only a backfill retracts what it did not see. Once a source
completes one full pass it has a watermark forever, so every later pass is
incremental, and `RetractUnseen` is never called again. A document deleted at
the provider on day two stays in the index indefinitely.

That reasoning is correct per pass — an incremental pass genuinely has not
looked at everything, so it has no standing to say anything is missing — but
the consequence is that nothing ever looks at everything again.

It bites differently per provider:

- **GitHub** asks with `since`, so it truly cannot see a deleted issue.
- **Notion** reads the same breadth either way (its search takes no `since`),
  so it *sees* everything and still will not retract, because the pass is
  labelled incremental.
- **Slack** messages are rarely deleted, so the exposure is smallest.
- **Files** has the same shape, and files also expires objects silently.

**Suggested fix**, roughly in order of effort: add a `full_sweep_at` column to
`knowledge_sources`, force `full = true` when it is due (say weekly), and
clear the cursor so the pass starts from the beginning. The retraction
machinery already works and is tested; only the decision of when to do a full
pass needs changing. `TestSyncRetractsWhatAFullPassDidNotFind` and
`TestSyncNeverRetractsOnAnIncrementalPass` pin the current behaviour, so
whoever changes this will see exactly what they are changing.

---

## 3. Finished but incomplete

| Gap | Where | What it costs |
| --- | --- | --- |
| **HTML is not extractable** | `internal/transform` | Only plain text, Markdown, CSV and JSON are read. HTML means promoting `golang.org/x/net` from an indirect dependency to a direct one — a deliberate choice left open rather than made unilaterally. Microsoft Graph mail and Notion's richer blocks wait on it. |
| **Notion gives properties, not page content** | `internal/sources/notion.go` | A page's body is its title and properties. Block children are a recursive call per page; for a workspace of any size that is a different cost profile, and worth designing rather than bolting on. |
| **Notion incremental reads full breadth** | `internal/sources/notion.go` | Notion's search takes no `since`. Unchanged hashes mean it publishes nothing, but it costs a full read to find that out every interval. |
| **Only four of seventeen providers** | `internal/sources/` | Box, Zendesk, Intercom and Figma already have generated clients in `internal/providerapi`, so each is a file in the shape of `github.go`. The others need hand-rolling like `slack.go`. |
| **Team files need team ids you have to already know** | `internal/filesclient` | Files never returns team-scoped objects unless a team is named, and offers no way to enumerate teams. Indexing everything an organization holds means asking `teams` first. A source naming one team works fine today. |
| **Slack reads channel history, not threads** | `internal/sources/slack.go` | Replies inside a thread are not walked. `thread_ts` is carried in metadata, so a consumer can at least group them. |
| **One run's ceiling is ~20k documents** | `internal/application/sync.go` | `pagesPerRun` (200) × `pageSize` (100). A source with more gives the worker back and resumes next tick, which is the intent — but a first backfill of a very large repository takes several intervals to complete. |

---

## 4. Worth a second look

Nothing here is known-wrong. They are the judgement calls I would want
someone else to confirm.

- **A failing credential pauses every source on that connection, not just the
  one that failed.** `application/sync.go` calls `PauseSources` by
  `connection_id`. That is intentional — the credential is shared, so the
  others would fail identically on their next tick — but it means one bad
  source pauses its siblings. If that is wrong, pause by source id and let
  the others discover it themselves.
- **Slack's `oldest` is treated as exclusive.** That matches Slack's default
  (`inclusive=false`), so the message the watermark names is not re-read. If
  that default ever changes, the boundary message is silently skipped rather
  than duplicated, which is the worse direction. A cheap hedge is to subtract
  a second from the watermark and let the idempotent upsert absorb the
  overlap.
- **A Notion database with several data sources only has its first indexed.**
  `notionDataSourceID` takes the first. Rare, but silent.
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
- **`RemoveSource` calls auth twice.** `application/sources.go` calls
  `s.Source` (which resolves the caller) and then `s.caller` again. One
  round trip too many on a rare path; trivial to fold together.
- **No token is shared between sources on the same connection.** The cache in
  `connectionsclient` is keyed by connection, so this already works across
  sources — worth confirming under load rather than assuming.

---

## 6. Test coverage gaps

The layers that carry the real logic are covered: the store against a real
Postgres, transform, the HTTP client's retry and rate-limit behaviour, the
sync semantics, the event dedupe, the error mapping.

What is not:

- **`knowledge` (the seam) and `internal/authclient` have no tests.** The seam
  is mostly assembly; authclient is a thin HTTP client that should get the
  same treatment as `filesclient` and `connectionsclient`, which do have them.
- **No end-to-end test.** Other services have an `internal/e2e` that runs the
  HTTP API, the application, the store and the adapters together against a
  fake provider. This service should have one; it is the test most likely to
  catch a wiring mistake once the entrypoint exists.
- **No live provider tests.** `integrations/internal/e2e/live_test.go` is the
  pattern: skipped unless credentials are in the environment, creating and
  cleaning up real resources. Nothing here has run against a real GitHub,
  Notion or Slack.
- **`events.Consumer.Poll` is untested.** `Handle` is well covered; the SQS
  loop around it is not.
- **The store tests ran against a locally started Postgres 16, not the
  project's `pgvector/pgvector:0.8.6-pg17` image**, because this sandbox has
  no Docker daemon. The schema uses no extensions, so this should not matter —
  but it has not been proven on the real image.

---

## 7. For whoever is building the retrieval side

The contract is `contracts/events/knowledge/v1`. Three things in it are easy
to miss and expensive to get wrong:

- **Scope travels on every document and is not decoration.** `Scope`, `TeamID`
  and `MemberID` say who may read that document. A retrieval pipeline
  answering across an organization has to honour them, or a member-scoped
  connection's documents leak to the whole org.
- **`ContentHash` is the version.** A document is published on first sight and
  whenever its hash changes, so upsert on `(SourceID, ExternalID)`. Delivery
  is at least once — dedupe on the envelope's `event_id`, or treat an equal
  hash as a no-op.
- **`BodyOmitted` means fetch it.** Bodies over 128 KiB are not carried;
  `GET /v1/organizations/{org}/documents/{DocumentID}` returns the full text.

`knowledge.document.removed.v1` must actually remove things — see §2 for why
it fires less often than you would want today.

---

## 8. Decisions that are not mine to make

1. **Promote `golang.org/x/net` to a direct dependency for HTML?** Unblocks
   Microsoft Graph and richer Notion content. `go.mod` is untouched by this
   whole branch, deliberately.
2. **Adopt the `Name`/`Migrate`/`Handler`/`Workers` seam across the other
   seven services?** It was proven here because this one is greenfield. The
   alternative is designing it during the consolidation itself, with seven
   services in flight.
3. **How often should a source do a full sweep?** See §2. Weekly is a guess.
4. **Where do document bodies live long-term?** Postgres today; see §4.
5. **Should background workers drain on shutdown everywhere?** This service
   hands workers back so a host can wait for them. Every existing service
   starts them fire-and-forget and lets the process exit mid-flight. That is a
   real gap in those services, not a pattern this one should have copied.
