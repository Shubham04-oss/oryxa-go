# Passage-only knowledge grounding

This opt-in follow-up to [the lane 10 investigation](https://github.com/Shubham04-oss/oryxa-go/pull/170)
adds `AGENTS_PRIMARY_CONSENT_KNOWLEDGE_PASSAGES_ONLY`, a standard boolean setting
that defaults to **false**. It is wired through agent service configuration to
the consent guard. No deployment configuration enables it, and no threshold,
prompt, provider guardrail or consent rule changes.

The original 0.23 grounding / 0.87 relevance incident remains unexplained:
the exact live source payload, full answer and guardrail version were not
retained in the finding. This change is a source-quality experiment, not a
claim that it fixes that score. No live guard calls or production data reads
were used to prepare it.

## Source selection

With the flag off, grounding uses every recorded nonblank `path: value` part,
as before. With it on, only explicit string `text` fields in the structured
`knowledge_search.passages` list become knowledge grounding text. The trusted
after-tool callback captures them separately, within the same character and
part bounds as full capture. Malformed lists, nested text lookalikes, flattened
field names, titles, IDs, retrieval scores and counts do not become passages.

For example, default grounding sees:

```text
passages.collection_id: private_falcon
passages.document_title: Falcon
passages.score: 0.91
passages.source: note:falcon
passages.text: Project Falcon launches on 14 October.
searched: 1
```

Passage-only grounding sees:

```text
Project Falcon launches on 14 October.
```

Full capture stays intact for the Summary only copy guard, including metadata
that the answer might quote. Captured passage slices are cloned with the rest
of the run record. No raw content is added to logs or persisted by this change.
The answering model cannot supply the captured passages through response fields.

Knowledge collection/status results are excluded from grounding in this mode.
Unknown future knowledge tools provide no grounding source until an appropriate
adapter is defined. **Non-knowledge tools keep their existing source semantics
and result order.** An empty knowledge search plus a separate substantive,
verified app result may still ground normally. Dropping that app source would
incorrectly hold an answer supported by what the run actually read.

Zero returned passages alone never creates a bypass. With no other usable
source, a substantive answer holds as `NO_SOURCES`, without asking Bedrock to
score metadata. The existing exceptions for owner-written answers and failure
reports are unchanged; an agent's substantive completed answer gains neither.

## Deterministic before/after examples

The assessments below are **mocked**. They demonstrate source wiring and guard
decisions, not measured Bedrock accuracy or a predicted score improvement.
Both Summary only and Allow for 1 hour use the selected grounding sources.

| Case | Flag off | Flag on |
|---|---|---|
| Correct one-line-note answer, mock grounding 0.95 / relevance 0.87 | Full source above; passes | Passage text above; passes under the same mock assessment |
| Correct date plus unsupported AI-launch claim, mock grounding 0.23 / relevance 0.87 | Holds `UNGROUNDED` | Holds `UNGROUNDED`; selecting passages does not override a low score |
| Summary copies 25 consecutive words from full capture | Holds `COPIED_FROM_SOURCE` | Same copy hold, even when those words were in a title rather than the selected passage |
| Empty search returns only `searched: 1`, with a substantive answer | Metadata is sent for grounding; a low mock assessment holds `UNGROUNDED` | Metadata is excluded; holds `NO_SOURCES` regardless of what a scorer might have returned |
| Empty search plus substantive app result, high mock assessment | Both full results participate; passes | App source remains; passes under the same mock assessment |

Go's grounding/relevance minima remain 0.75 / 0.5. The provider's threshold floor
and detected/blocked flags still apply. The experiment does not admit a 0.23
score by lowering a threshold or by skipping the check.

## Tests and remaining evidence

- `run_results_passages_test.go`: real knowledge toolbox and ADK callback,
  populated/empty search, malformed shapes, bounded extraction, unchanged full
  capture and separately cloned slices.
- `slack_passage_sources_test.go`: environment -> normal service config ->
  consent guard -> actual tagged SDK request against a fake client, showing
  unset/false retain full sources and true selects passage text.
- `answers_consent_passages_test.go`: both consent levels forward the trusted
  passage slice through the ledger.
- `consentguard/passages_test.go`: representative mocked decisions, copy guard,
  no-source holds, metadata exclusion and retained non-knowledge sources.

A later, separately approved evaluation should compare exact synthetic inputs
and negative examples under a fixed guardrail version. It must measure whether
passage-only sources reduce false holds without admitting unsupported additions.
These unit tests cannot establish that, and this PR does not enable the flag or
add a live scoring runner.
