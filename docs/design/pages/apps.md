# Apps (`/app/connections`) · Phase 4

**Job:** connect the tools your agents work through, and know what they may do.

## Today

The page is close to right already: a list of connections, an Active status, a
**Connect an app** button. Two things hold it back.

The description — "One place for member-owned connections, event
subscriptions, searchable sources, and agent tools" — is four internal nouns
where a person needs none.

And each row reads `GitHub · github_1`, led by a generic plug glyph, with a
gear and a chevron. `github_1` is a database key. The question a person has
about a connected app — *what can my agents actually do with this, and what
have they been doing* — is behind the chevron, if it is anywhere.

## Becomes

```
  Apps                                          [ + Connect an app ]

  ┌──────────────────────────────────────────────────────────┐
  │ (GitHub logo)  GitHub                         ● Active   │
  │                Read issues and pull requests · 12 uses   │
  │                this week                                 │
  ├──────────────────────────────────────────────────────────┤
  │ (Gmail logo)   Gmail                          ● Active   │
  │                Read and draft mail · never sends without │
  │                asking you                                │
  └──────────────────────────────────────────────────────────┘
```

- The app's **own logo**, its real name, and one line of what it lets agents
  do — in verbs, not scopes.
- The boundary is stated on the row where it matters: "never sends without
  asking you". This is the most reassuring sentence in the product and it is
  currently unwritten.
- Expanding a row shows what it has been used for recently and the switches for
  what it may do. Disconnect lives there, not in the row.

## Deleted

- "One place for member-owned connections, event subscriptions, searchable
  sources, and agent tools."
- The connection id in the title
- The plug glyph

## Words

| Now | Becomes |
| --- | --- |
| Connection | App |
| GitHub · github_1 | GitHub |
| Event subscriptions | What it tells your agents about |
| Agent tools | What your agents can do with it |

## States

- **Empty:** "No apps connected. Your agents can only reach what you connect
  here."
- **Expired or revoked:** Amber dot, "Needs reconnecting — GitHub signed you
  out on 3 March", **Reconnect**.
- **Connecting:** the row appears immediately in a pending state; the OAuth
  window is not the only feedback.
