# Rag service

Rag owns retrieval-augmented generation for an organization: the corpora it
searches, the documents in them, and the grounded answers they support.

Auth remains the identity and membership boundary; teams remains the team
graph; files remains object storage. Rag forwards the caller's credentials to
each of them rather than copying what they know, so a member who cannot read a
file cannot index it either, without rag having to know why.

```text
app ── rag HTTP ──▶ rag ── embed ──▶ Bedrock / Vertex AI
                     │
                     ├── vectors ──▶ pgvector  or  S3 Vectors
                     ├── chunks, documents, corpora ──▶ PostgreSQL
                     ├── GET files /v1/organizations/{org}/files/{file}
                     ├── GET teams /v1/organizations/{org}/teams/{team}
                     └── oryxa.rag ──▶ EventBridge
auth ── organization deleted, member removed ──▶ rag-events queue
teams ── team deleted ──▶ rag-events queue
files ── object deleted ──▶ rag-events queue
```

## What a corpus is

A corpus is one searchable collection. It pins its **embedding spec** —
provider, model, width and metric — and its **chunk policy** at creation, and
neither changes afterwards. Vectors from two models do not share a space, and
an index holding both answers plausibly and wrongly. Re-indexing under a
different model means a new corpus.

Placement mirrors files. **Scope** is where the corpus hangs; privilege still
comes from auth, and from teams for a team corpus.

| Scope | Who may create | Who may search | Who may add or remove documents | Who may delete the corpus |
| --- | --- | --- | --- | --- |
| `member` (default) | Any org member; hangs under the caller | Owner or org admin | Owner or org admin | Owner or org admin |
| `team` | Anyone who can **see** the team | Anyone who can see the team | Anyone who can see the team | Creator, team `can_manage`, or org admin |
| `organization` | Org admin | Any org member | Org admin | Org admin |

A caller who is not in the organization gets `404`, as auth conceals which
organizations exist. A team the caller cannot see is `404`, as teams conceals
it. A corpus the caller may not search is `404` for the same reason.

## Where the vectors live

One interface, two places. A corpus names which, and nothing above the store
asks again. The chunk text stays in PostgreSQL either way, so there is one
answer to what a chunk says and a citation addresses a byte range of a document
still on disk.

| Backend | Where | Suits |
| --- | --- | --- |
| `pgvector` (default) | The shared PostgreSQL, beside the chunk text | Anything searched interactively |
| `s3vectors` | Amazon S3 Vectors, one index per corpus | Large, cold or archival corpora |

pgvector needs a declared width before it can index a column, so there is one
table per width — 256, 384, 512, 768, 1024, 1536 and 3072 — and
`rag_vectors_other` for anything else, which is correct but scans. Measured
against pgvector 0.8: the `vector` type cannot be indexed past 2000
dimensions and `halfvec` past 4000, so 3072 (what `gemini-embedding-001`
emits) is stored as `halfvec`, which is the only way to index it at all.

S3 Vectors indexes on cosine or euclidean only, so an inner-product corpus on
it is refused by the domain and by a database constraint. Its index name is a
hash of the organization and corpus ids rather than the corpus id lower-cased,
because index names allow only lowercase letters, digits and hyphens and a
collision there would silently mix two organizations' documents.

## Embedding models

The catalog is a convenience, not a gate: a model it does not know can still be
used by stating its dimensions, so a model released after this code was written
needs configuration rather than a deploy.

| Provider | Model | Widths | Reads | Per call |
| --- | --- | --- | --- | --- |
| Bedrock | `amazon.titan-embed-text-v2:0` | 1024, 512, 256 | 8192 | 1 |
| Bedrock | `amazon.titan-embed-text-v1` | 1536 | 8000 | 1 |
| Bedrock | `cohere.embed-english-v3` | 1024 | 512 | 96 |
| Bedrock | `cohere.embed-multilingual-v3` | 1024 | 512 | 96 |
| Vertex | `text-embedding-005` | 768, 512, 256 | 2048 | 250 |
| Vertex | `text-multilingual-embedding-002` | 768, 512, 256 | 2048 | 250 |
| Vertex | `gemini-embedding-001` | 3072, 1536, 768 | 2048 | 1 |

Every call says whether it is embedding a document or a query. These models are
trained asymmetrically — Cohere takes `input_type`, Vertex takes `task_type` —
and embedding a question as though it were a passage is the quietest way to
make retrieval mediocre.

`GET /v1/capabilities` reports which providers and backends this deployment can
actually serve, so an operator learns it by reading rather than by trying one
and parsing the error.

## Chunking

Rag takes **extracted text**. Turning a PDF, a spreadsheet or a rendered page
into text is the upstream pipeline's job, and doing it in two places would mean
two answers about what a document says. Accepted content types are
`text/plain`, `text/markdown`, `text/html`, `text/csv` and `application/json`;
anything else is refused at ingest rather than indexed as a binary header.

HTML, CSV and JSON are normalized **before** chunking and the normalized text
is what is stored, so every chunk offset addresses text that still exists. CSV
rows become labelled `column: value` blocks, because a bare row shares no words
with any question anyone would ask.

The cut follows the document's shape: headings bound a section, paragraphs fill
a chunk, and text is only broken mid-sentence when one sentence is already over
budget. Two properties hold throughout:

- **Every chunk is an exact byte range of the stored document**, so a citation
  can be checked against the original rather than trusted.
- **Every chunk carries the trail of headings above it**, which is prefixed to
  the text that gets embedded. A chunk reading "It must be rotated every ninety
  days" is unfindable alone; under `Security > Credentials` it is found by a
  question about credential rotation.

Consecutive chunks in a section overlap, because the sentence that answers a
question is often the one that straddles a boundary. `max_tokens` is a ceiling
on the finished chunk including its overlap, since what it ultimately guards is
the model's input limit. Token counts are an estimate — the providers tokenize
differently — biased to run 10–20% high, so chunks come out slightly under
budget rather than silently truncated by a provider.

## Retrieval

Two retrievers run and their rankings are fused, because they fail differently.
Vectors miss an exact identifier, error code or surname; full-text search
misses a question worded nothing like the passage that answers it.

| Mode | What runs | Cost per query |
| --- | --- | --- |
| `hybrid` (default) | Vector search and PostgreSQL full-text, fused | One embedding call |
| `dense` | Vector search alone | One embedding call |
| `lexical` | PostgreSQL full-text alone | Nothing |

Fusion is by reciprocal rank, not by adding scores: a cosine similarity and a
`ts_rank_cd` are different numbers about different things. The fused score is
scaled so a chunk ranked first by every retriever scores exactly `1`, which is
what makes `min_score` a dial anyone can set. In `dense` mode the similarity is
kept instead, because with one retriever there is nothing to fuse and a
similarity is a better number than a position.

Each retriever's opinion travels on the match — `dense_score`, `lexical_score`,
`dense_rank`, `lexical_rank` — so a result can be explained. A chunk only the
lexical side found looks very different from one both agreed on.

`rerank` sends the fused candidates through a cross-encoder, which reads the
question and the passage together and is far better at telling a passage that
is *about* the subject from one that *answers the question*. It is opt-in
because it is the largest latency cost in the path. If the reranker fails the
query falls back to the fused order rather than failing. A candidate pool
larger than the cross-encoder accepts is reranked down to its ceiling and the
tail keeps its fused order below, rather than the whole rerank failing.

`diversity` trades relevance for spread by maximal marginal relevance, measured
on the words chunks share rather than on their vectors: what it suppresses is a
passage repeated across documents, which word overlap measures directly. `1` is
pure relevance and `0` is maximum spread; omitting it means `1`, and sending
`0` means `0`.

The full-text index uses the `english` configuration, which stems and drops
stopwords. A corpus that is mostly another language wants a different
configuration; that is a migration, because the column is stored.

## Ingestion

Adding a document returns `202` as soon as its text is safely stored, not once
an embedding provider has answered. The bytes are fetched from files **while
the caller's credentials are still to hand**, which is why the document row
keeps the text — and why the worker that indexes it later never needs to act as
anyone.

Most of the work is about not doing the expensive part:

- The same bytes offered again are recognised and cost nothing, so an upstream
  pipeline can re-deliver everything it has.
- A changed document re-embeds only the chunks whose text changed, matched by
  hash. A one-word edit is one embedding call, not one per chunk. A paragraph
  that only moved keeps its vector.
- `rag.document.indexed.v1` reports `embedded_chunks` against `reused_chunks`,
  which is the number worth watching on the bill.

A chunk counts as reusable only once its vector has actually been written, not
merely because its text hash matches. Otherwise an attempt that wrote the chunk
rows and then failed while embedding would leave rows that look reusable on the
retry, and the document would be marked searchable holding no vectors at all —
the worst failure available here, because it looks exactly like success.

A document is `pending`, then `ingesting`, then `ready` or `failed`. Failure is
kept with its reason rather than deleted, so it can be seen and retried. A
transient failure releases the document for another attempt and the attempt
count decides when to stop; a document that will fail identically every time —
one that chunks to nothing — is failed at once rather than left in the queue.

## Answering

`POST .../answer` retrieves and then writes a grounded answer with citations.

Passages go to the model as numbered, delimited sources with instructions to
use only them, to cite every claim, and to say plainly when the sources do not
contain the answer. Temperature is zero: the same sources should give the same
answer, and sampling makes a wrong one harder to reproduce.

Two rules keep this honest. **With nothing retrieved, no model is called** — a
model given no sources and asked a question answers from memory, which is the
failure the whole design exists to prevent; the response comes back
`grounded: false`. And **citations are checked, not trusted**: a source number
the model invented is dropped rather than rendered as a link to a passage the
answer never saw. Only the sources the answer actually used are returned, since
listing every retrieved passage would make it look better supported than it is.

The answer comes back with the matches it was built from, so a reader can check
it against the passages rather than take it on trust.

There is no default answering model. The id that works depends on the region
and on whether a cross-region inference profile is in use, so a guess would fail
at the first question rather than at startup. Without `RAG_ANSWER_MODEL` rag
retrieves and says why it cannot answer.

## API

Cookie-authenticated mutations must come from an origin in
`RAG_ALLOWED_ORIGINS`. A bearer token is not sent by a browser on its own, so
that path needs no origin check.

| Method | Path | Who | Purpose |
| --- | --- | --- | --- |
| `GET` | `/healthz` | Anyone | Liveness |
| `GET` | `/v1/capabilities` | Anyone | Which providers, backends and models this deployment serves |
| `GET` | `/v1/organizations/{org}/corpora` | Members | List. Query `scope`, `team_id`. Default is the caller's own plus the organization's |
| `POST` | `/v1/organizations/{org}/corpora` | Per placement | Create. `{name, slug, scope, team_id, embedding_provider, embedding_model, embedding_dimensions, embedding_metric, backend, chunking, metadata}` |
| `GET` | `/v1/organizations/{org}/corpora/{corpus}` | Per placement | One corpus, with its document and chunk counts. `{corpus}` may be an id or the slug |
| `DELETE` | `/v1/organizations/{org}/corpora/{corpus}` | Per placement | Delete the corpus, its documents and its vectors |
| `GET` | `.../corpora/{corpus}/documents` | Per placement | List. Query `status`, `limit` |
| `POST` | `.../corpora/{corpus}/documents` | Per placement | Add. `{source:{kind,ref}, title, content_type, text, metadata}`. Answers `202`: it is queued, not yet searchable |
| `GET` | `.../corpora/{corpus}/documents/{document}` | Per placement | One document and its ingest status |
| `DELETE` | `.../corpora/{corpus}/documents/{document}` | Per placement | Remove it and its vectors |
| `POST` | `.../corpora/{corpus}/search` | Per placement | `{query, top_k, mode, min_score, diversity, rerank, over_fetch, filter}` |
| `POST` | `.../corpora/{corpus}/answer` | Per placement | The same body; returns the answer, its citations and the matches |

A document source is `inline` (text in the request — what an upstream
extract-and-transform step delivers), `file` (a files object id, fetched as the
caller) or `external` (text with the URI it came from, kept so a citation can
point back).

Filters narrow on document ids and on metadata, with `eq`, `ne`, `in` and
`nin`. That set is deliberately small: every operator maps onto both a
PostgreSQL `jsonb` predicate and an S3 Vectors metadata filter, so a filtered
query means the same thing whichever backend the corpus was written into.
Metadata is bounded to 16 keys and 2 KiB total, which is the tighter of the two
backends' budgets, so a corpus indexed on one is portable to the other. Keys
the pipeline writes itself — `chunk_id`, `document_id`, `corpus_id`,
`organization_id`, `heading_path`, `title`, `source_kind`, `source_ref` — are
reserved, so a caller cannot forge provenance and make a citation point
elsewhere.

## Events

Rag publishes source `oryxa.rag` to `EVENT_BUS_NAME`. Contracts live in
`contracts/events/rag/v1`. Events carry identifiers, counts and outcomes, never
chunk text, embeddings or credentials.

| Detail type | When |
| --- | --- |
| `rag.corpus.created.v1` | A corpus is created, with the spec it pinned |
| `rag.corpus.deleted.v1` | A corpus is removed |
| `rag.document.received.v1` | A document is accepted and queued |
| `rag.document.indexed.v1` | A document is searchable, with what it cost |
| `rag.document.failed.v1` | Indexing gave up, with the reason |
| `rag.document.removed.v1` | A document is removed |

Rag consumes `auth.organization.created.v1`, `auth.organization.updated.v1`,
`auth.organization.deleted.v1`, `auth.member.removed.v1`,
`teams.team.deleted.v1` and `files.object.deleted.v1`.

Each of those is something rag indexed going away, and an index outliving what
it was built from is the failure that matters most here: it goes on answering
questions from text nobody is entitled to see. A deleted organization takes
every corpus with it; a member leaving takes their **member-scoped** corpora,
while team and organization corpora they created stay, exactly as their files
do; a deleted team takes its corpora; a deleted file takes the documents
indexed from it. Vectors are dropped before the rows that point at them, so a
half-finished wipe never leaves an orphan nobody can find.

## Provision

Copy the root `.env.example` to the ignored `.env`. Its emulator block points
the AWS services rag uses at the emulators in the root `docker-compose.yml`;
delete that block to provision AWS through the default credential chain. Then run:

```sh
(set -a && . ./.env && go run ./rag/infra/vectors/cmd/provision)
```

The command is repeatable. It creates or updates:

- the `RAG_VECTOR_BUCKET_NAME` S3 Vectors bucket, when one is named. The
  per-corpus indexes inside it are not created here: an index depends on the
  corpus's pinned width and metric, so the ingest path creates it on first
  write;
- the `rag-events` queue with its dead-letter queue;
- a `rag-lifecycle` rule on `EVENT_BUS_NAME` whose target is that queue and
  which the queue policy admits alone.

Put the printed `queue_url` in `RAG_EVENTS_QUEUE_URL`. The pgvector tables need
nothing here: they are service migrations the process applies at startup.

LocalStack forgets buses, rules, queues and buckets when it restarts, so re-run
the provisioner after one.

## Run locally

```sh
docker compose up -d
(set -a && . ./.env && go run ./rag/infra/vectors/cmd/provision)
(set -a && . ./.env && ORYXA_SERVICES=rag go run ./cmd/oryxa)
```

Schemas install automatically at start unless `ORYXA_AUTO_MIGRATE=false`; to
install them ahead of time instead, run `go run ./cmd/oryxa migrate`. Rag
deploys as part of the one `oryxa` image, not a container of its own.

Logs are structured JSON from `/internal/observability`. HTTP access logs skip
`/healthz`.

The store and the pgvector backend have tests against a real PostgreSQL with
pgvector, which the unit run skips:

```sh
RAG_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' \
  go test ./rag/internal/postgres ./rag/internal/vectorstore
```

Those need pgvector **0.8 or newer**: below 0.7 there is no `halfvec`, so the
3072-wide tables cannot be created, and below 0.8 there is no iterative index
scan, which is what keeps a filtered search from coming back short. The root
`docker-compose.yml` runs `pgvector/pgvector:0.8.6-pg17`.

## Deploy

Rag deploys as part of the one `oryxa` image, behind the same public edge as
the other services running alongside it. Its HTTP API and its ingest worker
share that one process; the worker leases its work from PostgreSQL, so several
replicas are safe.

Its runtime identity needs:

- `bedrock:InvokeModel` on the embedding models, and `bedrock:Converse` on the
  answering model, when either is on Bedrock;
- `bedrock:Rerank` on the reranking model, when one is configured;
- `bedrock:ApplyGuardrail` on the guardrail, when one is configured;
- `s3vectors:PutVectors`, `s3vectors:QueryVectors`, `s3vectors:DeleteVectors`,
  `s3vectors:GetIndex`, `s3vectors:CreateIndex` and `s3vectors:DeleteIndex` on
  the vector bucket, when S3 Vectors is in use;
- `events:PutEvents` on the event bus;
- `sqs:ReceiveMessage` and `sqs:DeleteMessage` on `rag-events`.

For Vertex AI it needs application default credentials for a service account
with `aiplatform.endpoints.predict` in `GOOGLE_CLOUD_PROJECT`, the same project
the agents service uses.

## What has and has not been run

Everything here builds, vets and passes its tests. That is not the same as
having run, and the difference matters more in this service than in most,
because the expensive paths are the ones that talk to a provider.

**Verified against a real dependency.** The migrations, the store and the
pgvector backend run against PostgreSQL 16 with pgvector 0.8: corpus and
document lifecycles, incremental re-embedding, hybrid search, tenant scoping,
the `halfvec` path at 3072 dimensions, the unindexed fallback width, and the
cascade from a deleted document to its vectors. The two pgvector index
ceilings in the schema — 2000 for `vector`, 4000 for `halfvec` — were measured
there rather than assumed.

**Not run against the real thing.** Each of these is covered by a fake or a
stub server, which proves the request shape this code builds and the response
shape it reads, and proves nothing about whether the provider agrees:

| Path | What stands in for it | What is unverified |
| --- | --- | --- |
| Bedrock embedding (Titan, Cohere) | A fake `InvokeModel` | Request bodies, response shapes, throttling behaviour, real model ids |
| Vertex embedding | An `httptest` server | The `:predict` contract, ADC auth, per-model batch limits |
| Bedrock answering (`Converse`) | A fake `Converse` | Whether the grounding prompt holds, real model ids, guardrail behaviour |
| Vertex answering (`generateContent`) | An `httptest` server | The same, plus ADC auth |
| Bedrock reranking | Nothing beyond construction | The whole call. This is the least exercised code in the service |
| S3 Vectors | A fake client | Index creation, metadata filters, batch limits, the real 3–63 character index name rules |
| The provisioner | Unit tests on its pattern and policy | It has never created anything, on AWS or on LocalStack |

S3 Vectors has no local emulation available: the LocalStack container in the
root `docker-compose.yml` runs `events,sqs,dynamodb,s3`, and S3 Vectors is not
among them. Exercising that backend means a real AWS account.

The first live run should be a small corpus end to end — create, add one
document, wait for `rag.document.indexed.v1`, search, answer — against each
provider in turn. Expect the model ids and the Bedrock reranker to be where it
goes wrong first.

## Packages with no tests

| Package | Why it matters |
| --- | --- |
| `internal/application` | **The significant gap.** It holds every access rule — who may create, search, write to and delete a corpus at each scope — and those rules are currently only exercised indirectly, through the HTTP layer's fakes. They deserve tests of their own before anyone relies on them |
| `internal/events` | Consumer, dispatcher and publisher, adapted from the files service where the same code is tested |
| `internal/filesclient` | The fetch-as-the-caller path, including the size guard |
| `internal/authclient`, `internal/teamsclient` | Near-verbatim copies of the files service's, which are tested there |

## Known limits

Things that work as written, but are narrower than they will need to be.

- **No re-index path.** A corpus pins its embedding spec for good reason, but
  there is nothing that moves a corpus to a new model: no dual-write, no
  backfill, no alias to switch. Today that means creating a second corpus and
  re-adding every document, which an upstream pipeline can do but nothing here
  helps with.
- **The text search configuration is fixed at `english`.** It is a stored
  generated column, so changing it is a migration, not a setting. A corpus that
  is mostly in another language gets worse lexical recall and nothing says so.
- **Document listing is capped at 200 with no cursor.** Enough to look at a
  corpus, not enough to walk one.
- **Answers do not stream.** A long answer holds the request open for as much
  as the 180-second write timeout allows.
- **Guardrails are wired on the Bedrock answering path only.** A Vertex answer
  goes through Vertex's own safety settings and nothing of Oryxa's.
- **No per-organization quota or rate limit on embedding spend.** One large
  ingest can consume a shared provider quota; the concurrency and retry limits
  are per-process, not per-tenant.
- **`rag_vectors_other` is an exact scan.** A corpus whose width has no table of
  its own is correct and will be slow in proportion to its size, and only a log
  line at ingest says so.
- **Token counts are a heuristic**, not a tokenizer. They are biased to
  over-count so chunks land under the model's limit, which means chunks are
  slightly smaller than the budget asks for.
- **No k6 journey.** `/test` covers teams, files and email; rag is not in it.

## Open decisions

These need a person, not a commit.

- **Where extraction lives.** Rag takes extracted text and refuses anything
  else, on the reasoning that doing it in two places would mean two answers
  about what a document says. If extraction should instead live here, that
  changes the service's shape and the accepted content types, and is worth
  settling before more is built on the current boundary.
- **Whether reranking should default on.** It is the largest quality gain per
  query and the largest latency cost. It is opt-in today because that trade
  belongs to whoever is watching the latency.
- **Whether the answering model should have a default.** There is none, so a
  deployment that forgets it retrieves but cannot answer. The alternative — a
  guessed model id — fails at the first question instead of at startup, which
  seemed the worse of the two.

## Not yet

- The `agents/primary` toolbox: nothing lets Krude search a corpus or cite from
  one, so an agent cannot yet answer from an organization's own documents. This
  is the obvious next piece.
- A web screen: corpora, documents and their ingest status have an API and no
  interface.
- Extraction, as above.
