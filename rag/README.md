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
                     ├── vectors ──▶ pgvector, S3 Vectors, or a Bedrock knowledge base
                     ├── chunks, documents, corpora ──▶ PostgreSQL
                     ├── GET files /v1/organizations/{org}/files/{file}
                     ├── GET teams /v1/organizations/{org}/teams/{team}
                     └── oryxa.rag ──▶ EventBridge
auth ── organization deleted, member removed ──▶ rag-events queue
teams ── team deleted ──▶ rag-events queue
files ── object deleted ──▶ rag-events queue
knowledge ── document published, document removed ──▶ rag-events queue
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
| `team` | Anyone who can **see** the team | Team member or org admin | Anyone who can see the team, or org admin | Creator, team `can_manage`, or org admin |
| `organization` | Org admin | Any org member | Org admin | Org admin |

A caller who is not in the organization gets `404`, as auth conceals which
organizations exist. A team the caller cannot see is `404`, as teams conceals
it. A corpus the caller may not search is `404` for the same reason.

Documents in an organization corpus also have an ACL. `access: members` is
the default and is visible to every member allowed into that corpus.
`access: admins` is visible only to organization owners and admins. An admin
can set this field when adding an inline or external document. A Files
document takes its access from Files and cannot be reclassified in RAG. It
goes only into a corpus whose readers can all read the file already: an
organization file with members access into any corpus, an admin-only one into
an organization corpus alone, a team file into that team's corpus or the
member corpus of whoever adds it, and a member's file into that member's own
corpus. Anywhere else it is `404`. Admin-only shared files must be
uploaded with `access: admins` in Files; a `billing.attachment` is always
admin-only. Knowledge carries that label into its publication event. The ACL applies
to document lists and reads, and to matches before reranking or generation.
Bedrock Knowledge Base retrieval also filters member queries on `access` in
the managed index; PostgreSQL checks the document ACL again before returning
a passage. Older organization file documents are marked admin-only by
migration until re-ingested from Files, since RAG did not previously store
their purpose. The same migration requeues existing Bedrock documents to add
`access` to the managed index; member searches may omit them while that work
completes.

## Where the vectors live

One interface, two places. A corpus names which, and nothing above the store
asks again. The chunk text stays in PostgreSQL either way, so there is one
answer to what a chunk says and a citation addresses a byte range of a document
still on disk.

| Backend | Where | Default |
| --- | --- | --- |
| `s3vectors` | Amazon S3 Vectors, one index per corpus | Wherever `RAG_VECTOR_BUCKET_NAME` names a bucket: serverless, priced by use, about 100 ms a query when warm and under a second cold |
| `pgvector` | The shared PostgreSQL, beside the chunk text | Where no bucket is named — local development and tests, since S3 Vectors has no local emulation — and the fastest to query |
| `bedrock-kb` | One Amazon Bedrock knowledge base per deployment, managed or vector, shared by every corpus on it | Never implicitly — only when `RAG_DEFAULT_BACKEND=bedrock-kb` is set explicitly. See [Knowledge base corpora](#knowledge-base-corpora) |

`RAG_DEFAULT_BACKEND` overrides either default, and a corpus keeps the backend
it was created on, so changing the default moves only corpora made after it.
The production deployment sets `RAG_DEFAULT_BACKEND=bedrock-kb` and selects the
`MANAGED` knowledge base. It retrieves passages with Bedrock and sends them to
Gemini on Vertex AI for grounded answers. Direct S3 Vectors and Bedrock
Converse remain configurable alternatives; their live smoke tests do not mean
production sends requests through them. Local `.env` uses pgvector. Existing
corpora retain their recorded backend until explicitly reindexed. Terraform
still provisions the older VECTOR knowledge base as a rollback option; that
resource is separate from the active MANAGED knowledge base.

Naming a knowledge base does not make it the default on its own:
`RAG_DEFAULT_BACKEND` selects the backend for new corpora. Parsing, chunking
and lexical search all work differently on `bedrock-kb`, and a deployment
should choose that trade deliberately rather than inherit it from which
environment variables happen to be set.

pgvector needs a declared width before it can index a column, so there is one
table per width — 256, 384, 512, 768, 1024, 1536 and 3072 — and
`rag_vectors_other` for anything else, which is correct but scans. Measured
against pgvector 0.8: the `vector` type cannot be indexed past 2000
dimensions and `halfvec` past 4000, so 3072 (what `gemini-embedding-001`
emits) is stored as `halfvec`, which is the only way to index it at all.

S3 Vectors indexes on cosine or euclidean only, so an inner-product corpus on
it is refused by the domain and by a database constraint. One query returns at
most 100 results, so the candidates rag over-fetches for fusion and reranking
are capped there, and a bucket holds at most 10,000 indexes: with one per
corpus — and the knowledge bridge makes one per member whose apps it indexes —
a deployment nearing that needs a second bucket or shared indexes. Its index name is a
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

Vertex's text models also refuse a call carrying more than 20,000 tokens across
its texts, whatever their count, so a batch closes at whichever limit it
reaches first, packed to 90% of the token limit because a count here is an
estimate. No estimate holds for every kind of text, so a call Vertex still
refuses as too large is embedded in halves, and a text the provider says it
truncated is logged: part of it is missing from its vector.

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

Two cross-encoders are wired, one at a time: a Bedrock reranking model by ARN
(`RAG_RERANK_MODEL_ARN`), or Google's Ranking API (`RAG_RERANK_VERTEX_MODEL`,
for example `semantic-ranker-default@latest`), which is serverless — billed per
call, nothing provisioned — and needs `discoveryengine.googleapis.com` on the
project. Measured over this repository's own documentation
(`rag/internal/eval`), the Ranking API put the answer's section first for 75%
of questions rather than 67%, and in the top three for 93% rather than 87%,
for about half a second a query.

`diversity` trades relevance for spread by maximal marginal relevance, measured
on the words chunks share rather than on their vectors: what it suppresses is a
passage repeated across documents, which word overlap measures directly. `1` is
pure relevance and `0` is maximum spread; omitting it means `1`, and sending
`0` means `0`.

The lexical side matches a chunk holding **any** of the question's words and
ranks it by how many it holds and how close together (`ts_rank_cd`).
Requiring every word, as a search box does, found nothing for most questions
worded the way a person asks them: measured over this repository's own
documentation, it put the answer's section in the top eight for 8% of
questions, and matching any word for 85%. In the fusion a lexical rank counts
half a dense one, since the dense retriever is right at the top more often.

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
- A large document is embedded 256 chunks at a time, and each slice is kept as
  its vectors land, so an attempt that fails near the end costs only the slice
  that failed.

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

The machine-readable search and answer contract is
[`contracts/http/rag/v1/openapi.yaml`](../contracts/http/rag/v1/openapi.yaml).

Cookie-authenticated mutations must come from an origin in
`RAG_ALLOWED_ORIGINS`. A bearer token is not sent by a browser on its own, so
that path needs no origin check.

| Method | Path | Who | Purpose |
| --- | --- | --- | --- |
| `GET` | `/healthz` | Anyone | Liveness |
| `GET` | `/v1/capabilities` | Anyone | Which providers, backends and models this deployment serves |
| `GET` | `/v1/organizations/{org}/corpora` | Members | List, each with its document and chunk counts. Query `scope`, `team_id`. Default is the caller's own plus the organization's |
| `POST` | `/v1/organizations/{org}/corpora` | Per placement | Create. `{name, slug, scope, team_id, embedding_provider, embedding_model, embedding_dimensions, embedding_metric, backend, chunking, metadata}` |
| `GET` | `/v1/organizations/{org}/corpora/{corpus}` | Per placement | One corpus, with its document and chunk counts. `{corpus}` may be an id or the slug |
| `DELETE` | `/v1/organizations/{org}/corpora/{corpus}` | Per placement | Delete the corpus, its documents and its vectors |
| `GET` | `.../corpora/{corpus}/documents` | Per placement | List. Query `status`, `limit`, `cursor`. Answers `{documents, next_cursor}`; `next_cursor` is absent on the last page |
| `POST` | `.../corpora/{corpus}/documents` | Per placement | Add. `{source:{kind,ref}, title, content_type, text, metadata, access}`. `access` is `members` for a new document when left out, and a document added again without it keeps the access it has; an org admin may set `admins` in an organization corpus, and a `file` document takes its access from Files. Answers `202`: it is queued, not yet searchable |
| `GET` | `.../corpora/{corpus}/documents/{document}` | Per placement | One document and its ingest status |
| `DELETE` | `.../corpora/{corpus}/documents/{document}` | Per placement | Remove it and its vectors |
| `POST` | `.../corpora/{corpus}/search` | Per placement | `{query, top_k, mode, min_score, diversity, rerank, over_fetch, filter}` |
| `POST` | `.../corpora/{corpus}/answer` | Per placement | The same body; returns the answer, its citations and the matches |

The search and answer request field is `query` (a string), with optional
`filter: {document_ids: [...], conditions: [{key, operator, values}]}`.
Search returns `{matches, usage, reranked}`. Each match has `chunk`,
`document` and `score`; Knowledge documents carry `provider` and
`knowledge_source_id` in `document.metadata`, plus `document_kind` when the
source names one and `category` when judged. Answer returns `{answer, matches}`;
`answer` contains
`text`, `citations`, `model`, `usage` and `grounded`. Each citation carries
the same document metadata so a caller can identify its origin without a
second lookup. Scope and admin access are derived from the caller's token;
neither can be supplied in the request body. An agent's token, which auth
answers with `agent: true`, reads only what every member may: an agent acting
for an org admin may be answering someone else, such as a teammate's
interaction request or a room it was mentioned in, so its searches, answers,
document lists, document reads and counts leave out `access: admins`
documents. The admin's own session still sees them.

A request that adds a document may be up to 24 MiB; every other request body,
a corpus's settings or a query and its filters, at most 1 MiB. Past its limit
a request is answered `413`. A document's body is read only once the caller
is known to be allowed to add to that corpus, so a caller who is not gets their
refusal without the document being read.

A document source is `inline` (text in the request — what an upstream
extract-and-transform step delivers), `file` (a files object id, fetched as the
caller) or `external` (text with the URI it came from, kept so a citation can
point back).

Listing a corpus's documents pages by cursor. The first request sends none;
each answer's `next_cursor` — present whenever more documents follow, absent
on the last page — is passed back as the `cursor` query parameter to continue.
A request with no cursor gets the same first page it always has: newest first,
by `(created_at, id)`, which is also what the cursor addresses, so a document
added mid-walk cannot make a later page repeat or skip one already seen.
`limit` defaults to, and is capped at, 200.

Filters narrow on document ids and on metadata, with `eq`, `ne`, `in` and
`nin`. PostgreSQL and S3 Vectors can filter on stored document metadata. A
`bedrock-kb` corpus can filter on `source_kind`, `provider`,
`knowledge_source_id`, `document_kind`, and `category`, which RAG writes into
the managed index. Other metadata conditions are refused rather than silently
widening the search. Organization, corpus, and document ACL filters are always
applied separately from caller supplied conditions.
For example, `{"query":"September invoices","filter":{"conditions":[{"key":"provider","operator":"eq","values":["gmail"]},{"key":"category","operator":"eq","values":["financial_record"]}]}}`
searches Gmail financial records in the corpora the caller may access.
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
`teams.team.deleted.v1`, `files.object.deleted.v1`, and knowledge's
`knowledge.document.published.v1` and `knowledge.document.removed.v1` (see
[Knowledge documents](#knowledge-documents)).

Every one but knowledge's is something rag indexed going away, and an index
outliving what it was built from is the failure that matters most here: it
goes on answering questions from text nobody is entitled to see. A deleted
organization takes every corpus with it; a member leaving takes their
**member-scoped** corpora, while team and organization corpora they created
stay, exactly as their files do; a deleted team takes its corpora; a deleted
file takes the documents indexed from it. Vectors are dropped before the rows
that point at them, so a half-finished wipe never leaves an orphan nobody can
find.

## Knowledge documents

Knowledge extracts documents from an organization's connected apps and
publishes `knowledge.document.published.v1` and
`knowledge.document.removed.v1` (`contracts/events/knowledge/v1`) for every
one, whatever it came from. Rag consumes both, on the same `rag-events` queue
every other lifecycle event arrives on, and indexes what they carry through
the same path any other document goes through —
`Store.UpsertDocument`, then the ordinary ingest worker — so nothing about
chunking, embedding or removal needs to know a document came from knowledge
rather than a member's own upload.

This is where "where extraction lives" was settled: knowledge extracts, rag
indexes the text it publishes, and rag builds no extractor of its own. That
was an open decision; it is now a default, not a redesign, and can still be
revisited — see docs/REMAINING-WORK.md.

**Which corpus.** One corpus per owner, mirroring how a knowledge document's
scope, team and member work everywhere else: a member-scoped document lands
in that member's own corpus, a team-scoped one in that team's, an
organization-scoped one in the organization's — never mixed. Rag creates the
corpus the first time it is needed, at rag's own default embedding model,
backend and chunking — the same defaults a member's own corpus gets when they
name none — named "Knowledge from connected apps" and marked
`origin: knowledge` in its metadata so it reads as what it is. The slug is
deterministic: `knowledge-member-<owner-hash>` or
`knowledge-team-<owner-hash>`, with a hash of the complete owner ID;
organization scope uses `knowledge-organization`. This keeps IDs that differ
only by case, punctuation or a long suffix in separate corpora. Existing
corpora with the earlier readable slug are reused when their stored scope,
owner and `origin: knowledge` match. A matching slug with another owner or
origin is never adopted. Two replicas racing to create the same corpus use
rag's unique-slug conflict, and the loser checks the winner's placement.

A team- or organization-scoped document names no member — knowledge ties a
member id to a member-scoped document alone — so the corpus this bridge
creates for one records `created_by` as `svc_knowledge` rather than a real
member id. Rag's schema allows that (`created_by` only has to be non-empty
text); `svc_knowledge` is shaped so it can never equal a real one, so it
cannot pick up the "creator may delete their own corpus" right that column
otherwise carries. A team manager or an org admin can still remove it.

**Which document.** Source kind `external`. `ref` is the event's URL when it
is a valid, absolute URI of at most 2048 characters, so a citation can point
back at it; otherwise `knowledge:<document_id>`. Title comes from the event,
falling back to its external id; content type comes from the event's MIME
when rag accepts it, otherwise `text/plain`. Metadata carries
`knowledge_document_id`, `knowledge_source_id`, `provider`, and the extractor's
`document_kind`; when Knowledge's judge supplies a semantic category, RAG
also stores it as `category`. For example, a Gmail message has
`provider: gmail` and `document_kind: message`; its content may be classified
as `financial_record`.

**Idempotency and removal.** `rag_knowledge_documents` maps a knowledge
document id to the organization, corpus and rag document it became, with the
content hash and extraction time of the version it reflects. An unchanged
hash, title, content type, metadata, access label, owner and reference needs no
work. Changed content or metadata requeues the document for indexing. If the
owner or reference changes, the old document and its
vectors are removed before a new document is indexed at the new placement.
An event older than what is already mapped is ignored. Removal timestamps
are kept in `rag_knowledge_removals`, including when no document was indexed
yet, so an older publish cannot resurrect a removed document and an older
removal cannot delete a newer version. Publish and removal handlers for the
same Knowledge document are serialized with a PostgreSQL advisory lock across
rag replicas.
`knowledge.document.removed.v1` deletes the mapped rag document through rag's
normal delete path and the mapping row with it. A corpus left with no
documents after that is left alone — an empty corpus is harmless and will be
reused. A corpus or a document going away some other way — an organization, a
team or a member wiped — takes its mapping row with it by cascade, so nothing
here has to notice separately.

**Omitted bodies.** A published event whose body was too large to carry
(`body_omitted: true`, over knowledge's 128 KiB event budget) is read back
through Knowledge's internal, service-authenticated body route. Rag checks
the returned content hash against the event before indexing it. If the body
cannot be read or no longer matches and a prior version is indexed, rag
removes that stale version. A temporary read failure leaves the event for
retry.

**Scope safety.** A document missing the id its scope needs — no member id at
member scope, no team id at team scope — is refused and logged rather than
guessed at: the one mistake this bridge must never make is a member-scoped
document landing somewhere the whole organization, or the wrong team, can
read it.

## Knowledge base corpora

A corpus on the `bedrock-kb` backend hands a document to a fully managed,
serverless Amazon Bedrock knowledge base instead of chunking and embedding it
here. Bedrock parses it — including PDF and Office files rag cannot read
itself — cuts it into chunks of its own, embeds them, and answers a query by
searching them. Nothing about chunking, embedding models or vector storage in
the rest of this document applies to a corpus on this backend.

**One knowledge base per deployment.** OpenTofu creates it, with one data
source that takes documents directly, and hands rag only their ids:
`RAG_KNOWLEDGE_BASE_ID` and `RAG_KNOWLEDGE_BASE_DATA_SOURCE_ID`. Rag never
creates, configures or deletes either. It is one of Bedrock's two kinds, and
`RAG_KNOWLEDGE_BASE_TYPE` says which:

| Kind | What Bedrock does | Searched by |
| --- | --- | --- |
| `MANAGED` | Keeps the index itself. Its own parser (PDF, Office, images), embedding model and reranker, all in the retrieval price | Keyword and meaning together, on every query |
| `VECTOR` (when the type is unset) | Embeds with Titan v2 at 1024 dimensions into an S3 Vectors index the deployment made; data source of type `CUSTOM` | Meaning only: Bedrock mixes in keywords only on Aurora, OpenSearch or MongoDB |

Both take, report on and delete documents through the same calls, so
nothing about ingestion below differs between them; only the search request
does. Every corpus on this backend in a
deployment shares that one knowledge base; what tells their documents apart
inside it is metadata (`organization_id`, `corpus_id`, `access`), not separate
infrastructure, the same way tenancy is metadata rather than infrastructure
in the rest of Bedrock's multi-tenant surface.

Because the knowledge base is created once, outside rag, with one embedding
model, a corpus here cannot pin an embedding spec of its own the way a
pgvector or S3 Vectors corpus can: every `bedrock-kb` corpus is recorded as
Titan v2 at 1024 dimensions, cosine, and creating one that names anything
else is refused — by the service, naming what a `bedrock-kb` corpus must use
instead, and by a database constraint if it ever got that far (migration
`007`). On a `MANAGED` knowledge base that record is only a placeholder:
Bedrock embeds with a model of its own choosing, which no corpus can name.

**Configuration.**

| Variable | Purpose |
| --- | --- |
| `RAG_KNOWLEDGE_BASE_ID` | The knowledge base to use. Required, with the next one, for the backend to be available at all |
| `RAG_KNOWLEDGE_BASE_DATA_SOURCE_ID` | Its one `CUSTOM` data source |
| `RAG_KNOWLEDGE_BASE_TYPE` | `MANAGED` or `VECTOR` (the default): which kind the knowledge base is, and so how it is searched |
| `RAG_KNOWLEDGE_BASE_BUCKET` | Where rag stages document text too large to send inline. Without it, such a document is refused by name rather than failing at ingest |
| `RAG_DEFAULT_BACKEND=bedrock-kb` | Makes it the default for a corpus created without naming one. Configuring the two ids above does not do this on its own — see [Where the vectors live](#where-the-vectors-live) |

**Ingestion.** A document in a `bedrock-kb` corpus is recorded in PostgreSQL
exactly like any other — the same row, the same `pending → ingesting →
ready|failed` lifecycle, the same API — but the ingest worker hands it to the
knowledge base whole rather than cutting it into `rag_chunks` rows:

- Text that fits a conservative inline limit is sent straight in the ingest
  call. Larger text is staged first, to `RAG_KNOWLEDGE_BASE_BUCKET` at
  `rag-staging/<organization_id>/<corpus_id>/<document_id>/<content_hash>.txt`,
  and the knowledge base is pointed at that object instead; the staged copy is
  deleted once the document is indexed or removed.
- A document knowledge published with no extracted text of its own — a PDF, an
  Office file, carried as `Original` (see [Knowledge
  documents](#knowledge-documents)) — is pointed straight at that object in
  knowledge's own bucket. Rag never reads those bytes; only the knowledge base
  does. The same document published to a corpus that is not `bedrock-kb` has
  nothing here that could index it and is skipped, logged, with a previously
  indexed version removed if there was one — a knowledge document needs actual
  text for every other backend.
- Ingestion is asynchronous: submitting a document returns almost immediately,
  and the worker polls the knowledge base's own status on later passes,
  reusing the same lease a claim already holds rather than a timer of its own,
  until it reports the document indexed or failed with a reason. A live test
  indexed three documents, one of them a PDF, in about nine seconds; nothing
  here assumes that number. `PARTIALLY_INDEXED` counts as finished — what did
  index is searchable, and the shortfall is logged. A submission still
  unfinished an hour after it was made (`kb_submitted_at`, migration `008`) is
  given up on and submitted again, which counts as an attempt, so a document
  the knowledge base never finishes ends up `failed` rather than polled
  forever.
- A knowledge base document has no chunks of rag's own to count, so its
  `chunk_count` stays `0` — an honest answer, not a guess, once it is `ready`.
- Removing a document, or the whole corpus, deletes it from the knowledge base
  by the same id it was ingested under — rag's own document id doubles as the
  custom document id Bedrock knows it by. A corpus has no per-corpus index
  there to drop the way S3 Vectors does, so dropping one means naming every
  document it owned.

**Retrieval.** A search against a `bedrock-kb` corpus calls Bedrock's
`Retrieve` directly, filtered to that one corpus
(`organization_id` equals the caller's, `corpus_id` equals the corpus —
tenant isolation that must never regress, and is asserted in
`rag/internal/retrieval/bedrock_kb_test.go` against the exact filter Bedrock
receives). There is no lexical half to fuse it with — a `bedrock-kb` corpus
has no full-text index — so `mode` is not honoured the way it is elsewhere;
Bedrock's own search is what runs regardless of what was asked: keyword and
meaning together on a `MANAGED` knowledge base, meaning only on a `VECTOR`
one. `rerank` asks Bedrock to rerank inside the same call rather than sending
the results through the cross-encoder `Reranker` used for the other two
backends: with its own reranker on a `MANAGED` knowledge base, and with
`RAG_RERANK_MODEL_ARN` on a `VECTOR` one, which without it does not rerank.
A query that does not ask to rerank is not reranked on either. `top_k`, `min_score` and
`diversity` are applied afterward exactly as they are for pgvector and S3
Vectors, so a caller sees one contract regardless of which backend answered.
A query's own `filter.document_ids` and supported `filter.conditions` narrow
further inside the tenant and access filters. RAG submits provider, Knowledge
source ID, document kind and category as selected metadata attributes; it
does not copy provider specific details such as email addresses into the
shared index. A migration requeues existing Knowledge documents once to add
the new filterable attributes. Older documents acquire semantic categories
when Knowledge judges them again on a subsequent source sweep.

Each returned passage becomes a `Match` whose `Chunk` is synthesized rather
than read from `rag_chunks`, since there is no row: its id is derived from the
document id and a hash of the passage text, stable so the same passage cites
the same way twice, and its byte range is left at zero, since nothing here
knows where in the document Bedrock found it. Its document's title, source,
and selected metadata come from PostgreSQL, which holds a row for every
document whichever backend indexed it. The metadata travels with the match
and any citation built from it. A passage whose document PostgreSQL no longer
holds is left out, with a warning naming it. Rag removes a document's row once the
knowledge base accepts the delete, and the knowledge base finishes that delete
in its own time, so without this a removed document went on answering
questions, untitled, for as long as the delete took — or for good, if it
failed. A query naming both `bedrock-kb` and other corpora — the
knowledge toolbox's own multi-collection search, for one — runs each corpus
through whichever path it needs and merges the results the way it always has;
nothing about mixing them needed to change here, because a `bedrock-kb`
match is an ordinary `Match` by the time it leaves this package.

**What it does not do.**

- **No PostgreSQL lexical search.** `mode: lexical` and the lexical half of
  `hybrid` have nothing to run against; only Bedrock's own search answers,
  which on a `VECTOR` knowledge base means an exact name or number can be
  missed.
- **Citations with no byte range.** `start_byte` and `end_byte` are always
  zero — Bedrock does not report where in the source document a passage came
  from, so nothing here can check one against the original the way a pgvector
  or S3 Vectors citation can.
- **Asynchronous indexing.** A document is not searchable the instant it is
  added; how long it takes to become so depends on the knowledge base, not on
  a setting here.
- **No re-embed on a metadata hiccup without a resubmit.** A document Bedrock
  reports as only partially indexing its metadata is resubmitted rather than
  merely polled again — see `rag/internal/ingest/ingest.go` — since tenant
  isolation depends on that metadata actually landing and polling the same
  submission again would only ask the same question twice.

### Moving to another knowledge base

A document's `kb_synced_hash` says what content was handed to the knowledge
base rag was configured with at the time, not which one it was. Pointing rag
at a different knowledge base (in production, the `rag_knowledge_base`
variable) therefore leaves every `bedrock-kb` document looking indexed in one
that has never seen it, and searches there come back empty until each is
handed over again. After the release that moves rag has replaced every task
that still used the old one, put them all back in the queue:

```sh
deploy/scripts/sql.sh "UPDATE rag_documents AS document
  SET status = 'pending', kb_synced_hash = '', attempts = 0, failure_reason = '',
      locked_until = NULL, updated_at = now()
  FROM rag_corpora AS corpus
  WHERE corpus.id = document.corpus_id AND corpus.backend = 'bedrock-kb'"
```

The ingest worker then submits each one to the knowledge base it now uses,
at its usual pace, and searches fill back in as they finish. Documents that
failed before are tried again, which is worth it when the move is to a
knowledge base with a better parser. The old knowledge base still holds
every document as it was, so going back is the same two steps the other way.

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
- `bedrock:IngestKnowledgeBaseDocuments`, `bedrock:GetKnowledgeBaseDocuments`
  and `bedrock:DeleteKnowledgeBaseDocuments` on the knowledge base and its
  data source, and `bedrock:Retrieve` on the knowledge base, when
  `RAG_KNOWLEDGE_BASE_ID` is set; `s3:PutObject` and `s3:DeleteObject` on
  `RAG_KNOWLEDGE_BASE_BUCKET`, when one is named, for staging;
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
cascade from a deleted document to its vectors. Vertex's Ranking API reranked
live on 2026-09-18, measured by `rag/internal/eval`. The two pgvector index
ceilings in the schema — 2000 for `vector`, 4000 for `halfvec` — were measured
there rather than assumed. Vertex embedding ran on 2026-09-18:
`text-embedding-005` at 768 dimensions, through Application Default
Credentials at the `global` location, embedded two documents (six chunks) and
the queries against them, and searches as a member, and as Krude, came back
with the right passages first.

**Not run against the real thing.** Each of these is covered by a fake or a
stub server, which proves the request shape this code builds and the response
shape it reads, and proves nothing about whether the provider agrees:

| Path | What stands in for it | What is unverified |
| --- | --- | --- |
| Bedrock embedding (Titan, Cohere) | A fake `InvokeModel` and a small live Titan v2 run | Cohere and throttling behaviour |
| Vertex embedding | An `httptest` server, and one small live run | Per-model batch limits, which six chunks never approached; the other two Vertex models |
| Bedrock answering (`Converse`) | A fake `Converse` and a small live Nova Micro run | Broader grounding quality and guardrail behaviour |
| Vertex answering (`generateContent`) | An `httptest` server and one synthetic live run | Grounding and citation quality beyond the smoke test |
| Bedrock reranking | Nothing beyond construction | The whole call. This is the least exercised code in the service |
| S3 Vectors | A fake client and a small live index | Batch limits and larger corpora |
| The provisioner | Unit tests on its pattern and policy | It has never created anything, on AWS or on LocalStack |

S3 Vectors has no local emulation available: the LocalStack container in the
root `docker-compose.yml` runs `events,sqs,dynamodb,s3`, and S3 Vectors is not
among them. Exercising that backend means a real AWS account.

Vertex generation answered a synthetic invoice through `gemini-3.5-flash-lite`
on 2026-09-23, using the real passages returned from the managed knowledge
base; the answer contained the amount and a valid source citation. A second
live run used Titan v2 to embed two synthetic documents into a temporary S3
Vectors index, then Nova Micro through Converse to answer from the retrieved
passage with a citation. The index was deleted after the run.

The `bedrock-kb` backend has run against real knowledge bases in
`us-west-2`. Production has indexed members' Drive and Gmail documents
through it since 2026-09-19. On 2026-09-21 `rag/internal/eval` put its
corpus through this code into both kinds, a `VECTOR` one over S3 Vectors and
a `MANAGED` one: submitting, polling each document's status until it was
indexed, searching with the tenant filter, reranking inside Retrieve (Amazon
Rerank 1.0 on the first, Bedrock's own reranker on the second), searching the
same corpus as another organization and finding nothing, and deleting the
documents again. The eval did not exercise the `METADATA_PARTIALLY_INDEXED`
status, whose handling is a judgment call from its documentation (see
`rag/internal/ingest/ingest.go`).

On 2026-09-23 the opt-in `TestLiveManagedProvenance` ingested two synthetic
documents into the `MANAGED` knowledge base and removed them afterward. It
verified provider and category filters, organization isolation, admin-only
document access, a cited Gemini answer, and a metadata-only category change:
the new category found the document and the old category no longer did.
The opt-in `TestLiveS3VectorsConverse` repeated the provider/category,
organization, admin-only and metadata-update checks against S3 Vectors with
real Titan embeddings. Its Converse answer cited the retrieved invoice.

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
- **Dropping a `bedrock-kb` corpus does not clean up staged text.** Deleting
  one document does; deleting the whole corpus only removes it from the
  knowledge base, since doing more would mean reading every document's
  content hash first for what is, at worst, a handful of stray text objects
  in rag's own staging bucket rather than anything reachable by a search.

## Open decisions

These need a person, not a commit. Where extraction lives was one of these —
see [Knowledge documents](#knowledge-documents) for where it settled as a
default rather than a redesign.

- **Whether reranking should default on.** It is the largest quality gain per
  query and the largest latency cost. It is opt-in today because that trade
  belongs to whoever is watching the latency.
- **Whether the answering model should have a default.** There is none, so a
  deployment that forgets it retrieves but cannot answer. The alternative — a
  guessed model id — fails at the first question instead of at startup, which
  seemed the worse of the two.

## Not yet

- Team corpora in Krude's searches: its `knowledge` toolbox searches what
  `GET …/corpora` lists by default, the member's own and the organization's,
  and reaches a team's corpus only when Krude names it
  ([agents/primary/README.md](../agents/primary/README.md#knowledge-toolbox)).
- A web screen: corpora, documents and their ingest status have an API and no
  interface.
- Extraction of anything rag does not already take as text — knowledge is the
  one extractor feeding it today; see [Knowledge documents](#knowledge-documents).
