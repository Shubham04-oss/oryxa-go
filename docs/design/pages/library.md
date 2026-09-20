# Library (`/app/knowledge` + `/app/files` → `/app/library`) · Phase 4

**Job:** put things where your agents can find them, and see what they can read.

## Today

Two nav items for one idea.

**Knowledge bases** — "Curated knowledge bases your primary agent and bounded
specialists can search." Against a stack with the rag service down it greets a
person with a red `That no longer exists, or you do not have access to it.` —
which is a 404 on a *collection*: "that" names nothing the person asked for.

**Files** — a separate page for the other half of the same idea.

A person does not hold "knowledge bases" and "files" as different categories.
They hold *stuff my agents can read*.

## Becomes

One page, two tabs, one mental model.

```
  Library                                          [ + Add ]

  ( Knowledge )  ( Files )

  ┌──────────────────────────────────────────────────────────┐
  │ [▢] Product docs        GitHub · 412 documents           │
  │     Everyone in Launch crew can search this              │
  ├──────────────────────────────────────────────────────────┤
  │ [▢] Pricing sheets      12 files · you only              │
  └──────────────────────────────────────────────────────────┘
```

- Each row says **who can read it**, in words. That is the question people
  actually have about anything they upload, and today it is nowhere.
- Sources keep their app's own logo — GitHub, Drive, Gmail — rather than a
  database glyph.
- **+ Add** opens one sheet: connect a source, or upload files. The distinction
  between a corpus and a file is ours; the sheet can make it without naming it.
- A knowledge base's detail route keeps what it has (documents, sync state) and
  gains a plain sentence about when it last synced and what failed.

## Deleted

- One of the two nav items
- "Curated knowledge bases your primary agent and bounded specialists can
  search."
- The database and file-stack glyphs on every row

## Words

| Now | Becomes |
| --- | --- |
| Knowledge base | Knowledge *(a tab, not a noun people must learn)* |
| Corpus | *(never on screen)* |
| That no longer exists, or you do not have access to it. | We could not load your library. **Try again** |
| No searchable knowledge yet | Nothing here your agents can read yet |

## States

- **Empty:** "Nothing here your agents can read yet. Connect GitHub, Drive or
  Slack, or upload a file." with the one button.
- **Syncing:** a quiet line on the row — "Syncing · 120 of 412" — never a
  blocking spinner.
- **Failed source:** Amber dot, the reason in words, and **Reconnect**.
