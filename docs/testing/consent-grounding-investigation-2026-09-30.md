# Slack consent grounding: investigation, 2026-09-30

This investigation changes tests and documentation only. It does not change
source selection, prompts, thresholds, consent rules or infrastructure. No live
Bedrock call or production data read was made.

## What the incident establishes

[Finding 4](findings-2026-09-30.md) records an Allow for 1 hour answer at
03:34 UTC, described as “launches 14 October, related to the newest AI launch”.
It was held with `UNGROUNDED`, grounding 0.23 and relevance 0.87, and the owner
reviewed it in Oryxa. The earlier Summary only ask completed successfully.

The finding does not contain the exact live note, retrieved passages, full
Summary/Content/artifact fields, or the runtime guardrail version and filter
thresholds. The original cause is therefore **unproven**. The
[multi-account test fixture](multi-account-test.md) contains a launch date and a
pricing codename, but does not mention an AI launch; that fixture is not evidence
that the live note had exactly the same text.

## Confirmed source and consent path

1. [consentInput](../../agents/primary/internal/interactions/consent.go) grants
   the same requested tools for Summary only and Allow for 1 hour. Access also
   grants `consent:access` and creates a bounded lease for follow-ups. The
   [delivery message](../../agents/primary/internal/interactiondelivery/waker.go)
   asks a summary to use its own words, copy fewer than 20 consecutive words
   and include no links; access permits short excerpts.
2. Delivery agents register
   [recordToolResult](../../agents/primary/internal/agents/run_results.go) as an
   ADK after-tool callback. Successful results are recorded in memory by trusted
   invocation ID, then flattened into `path: value` parts. Tool arguments,
   `interactions_*` results and `set_model_response` are excluded. The respond
   tool supplies the invocation's recorded results in context; the model cannot
   declare its own sources in the response.
3. The capture retains the latest 16 results, at most 40,000 characters per
   result and 100,000 together, for 30 minutes since the last recorded result.
   The record is not persisted or logged. A restart, expiry, eviction or a
   response concurrent with its first read can leave it missing. These are
   limits, not established causes of this incident.
4. [consentRead](../../agents/primary/internal/interactions/answers.go) forwards
   the request's Task, recorded results and the answer after the general answer
   guard. The consent guard strips credentials, then checks the nonblank
   **Summary, Content and artifact references together**. `tools_used` is not
   part of that text. Both consent levels use the same sources, grounding and
   relevance minima. Summary only adds copy/link checks.
5. [BedrockGrounder](../../agents/primary/internal/consentguard/bedrock.go) sends
   `ApplyGuardrail` with `source: OUTPUT`, `outputScope: FULL` and separate
   `grounding_source`, `query` and `guard_content` text blocks. This matches
   the [AWS qualifier contract](https://docs.aws.amazon.com/bedrock/latest/userguide/guardrails-contextual-grounding-check.html)
   and [FULL output contract](https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ApplyGuardrail.html).
   Sources are combined, keeping the newest within 100,000 characters; query is
   clipped to 1,000. Answers are checked in pieces of at most 4,000 characters,
   retaining the lowest grounding and highest relevance score.

### Captured text includes metadata

The real knowledge toolbox returns passage text **and** collection ID, document
title, retrieval score, source reference, optional classification fields, and
search counts. The current generic capture flattens all nonblank values. The
populated fixture reaches the respond tool as:

```text
passages.collection_id: private_falcon
passages.document_title: Falcon
passages.score: 0.91
passages.source: note:falcon
passages.text: Project Falcon launches on 14 October. The v2 pricing plan's codename is Tern.
searched: 1
```

An actual successful search with no passages still produces `searched: 1`.
The consent guard treats that as nonblank source text and calls Bedrock.
Collection listings and other successful status results can likewise contribute
metadata. Thus `UNGROUNDED` with a score establishes that nonblank text was
checked, **not** that the supporting note was included. A wholly missing record
instead holds as `NO_SOURCES` without calling Bedrock.

### Why relevance does not settle grounding

AWS describes grounding as support from the supplied reference, including new
information as ungrounded, and relevance as answering the query. A correct date
can coexist with an unsupported extra claim in another answer field. That is a
plausible explanation to test, alongside missing passages and metadata-heavy
sources; none is proved by the two recorded scores. The docs provide no rule
that a one-line source must receive a low score.

### Lowering Go alone would not admit 0.23

The [Go defaults](../../agents/primary/internal/consentguard/guard.go) are 0.75
grounding and 0.5 relevance. The checked-in
[Terraform configuration](../../deploy/production/answer_guard.tf) sets provider
floors of 0.5 and 0.3 respectively; this is not verification of the incident's
live version. [Score.below](../../agents/primary/internal/consentguard/grounder.go)
holds below the higher of the Go minimum and provider threshold, and also holds
when Bedrock marks the filter detected or blocked. At those settings 0.23 is
below both grounding thresholds; 0.87 clears both relevance thresholds.

## Deterministic evidence added

- `TestKnowledgeSearchProvenanceReachesRespondThroughTheDeliveryCallback`
  runs the real knowledge toolbox against fake RAG through real ADK callbacks,
  proving that the one-line passage and metadata reach respond, and that an
  empty search still records its count.
- `TestSlackConsentChoicesForwardTheSameSourcesAndAnswerToBedrock` runs the
  ledger with the real consent guard and Bedrock adapter against a fake SDK
  client. Both consent choices produce the same exact tagged source, query and
  complete answer text. Fixed 0.23/0.87 scores hold as `UNGROUNDED`; a high-score
  control passes. Empty-search metadata is sent; no recorded results skip the
  SDK call and hold as `NO_SOURCES`. Separate controls isolate the provider
  threshold floor and detected/blocked behavior after lowering the Go minimum.

These tests prove capture, request construction and score handling. They do not
measure Bedrock's semantic scoring, explain the original 0.23, or certify a new
threshold.

## Proposed experiment, requiring Claude's approval

Prefer testing source quality before lowering a global threshold:

1. For `knowledge_search`, compare today's flattened payload with a semantic
   source containing the returned passage text. Keep provenance metadata
   separate; IDs, retrieval scores and search counts should not establish
   factual support by themselves. Preserve the complete trusted capture for the
   summary copy check. Define other tool-specific source adapters separately.
2. Compare a concise source-supported answer with the same answer plus an
   unsupported AI-launch claim. Check all shared answer fields; omitting Content
   or artifacts from the guard while still publishing them would weaken the
   boundary.
3. Use a small synthetic evaluation set: correct date, faithful paraphrase,
   wrong date, unsupported addition, empty search, unrelated passages, and a
   longer source. Compare both consent choices. Record exact synthetic inputs,
   guardrail version, thresholds, detected/action flags and scores. Do not put
   private source or answer text in production logs.
4. If passage-only sources improve supported answers without admitting
   unsupported ones, propose the narrowly scoped adapter change. If false
   holds remain, calibrate the approach or thresholds against both positive and
   negative examples. Passing 0.23 at the current provider floor would require
   changing the provider configuration as well as Go, not just a Go constant.

There is no scoring runner or enabled experiment in this PR. Any source,
instruction, threshold or infrastructure change remains a separate decision.
