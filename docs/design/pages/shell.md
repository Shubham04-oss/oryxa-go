# The shell — sidebar, header, search · Phase 2

**Job:** get a person to the five places they work, and stay out of the way.

## Today

A 200px sidebar with the organization switcher, a **New conversation** button,
then eight links under three shouted group labels — `SOLO WORKSPACE`,
`COLLABORATE`, `RESOURCES` — each with a lucide glyph. At the bottom,
Organization settings and the account row. The header carries a two-line
eyebrow (`SOLO WORKSPACE / Simulated Org`) and a Search field with ⌘K.

The group labels teach a taxonomy nobody asked for. The eyebrow repeats the
organization name that is already in the sidebar, under a word for a concept.
Eight destinations is more than the product has jobs.

## Becomes

```
┌──────────────┬──────────────────────────────────────────────┐
│ ● Oryxa      │  Search…                            ⌘K   [M] │
│ Simulated Org│                                              │
│              │  Page title                    [ action ]    │
│ + New chat  N│                                              │
│              │                                              │
│ ⌂ Home       │                                              │
│ ▤ Rooms    2 │                                              │
│ ◇ Agents     │                                              │
│ ▢ Library    │                                              │
│ ⚯ Apps       │                                              │
│              │                                              │
│ ⚙ Settings   │                                              │
└──────────────┴──────────────────────────────────────────────┘
```

- Five links, no group labels, no eyebrow.
- A count appears beside a link only when something is waiting for this person
  there — unread lines in a room, a request for their agent. Never a total.
- The account row moves into the header as an avatar menu; the sidebar's job is
  navigation, and its bottom edge is a poor place for the two things a person
  clicks least.
- Settings is one link to a settings shell with its own sub-navigation.
- 640–1024px collapses to a 56px icon rail with tooltips; below 640 it is a
  sheet behind the header.
- The active item is Ember text on `--brand-soft`, not a grey fill. It is the
  one piece of colour in the chrome.

## Deleted

- `SOLO WORKSPACE` / `COLLABORATE` / `RESOURCES` labels
- The header eyebrow
- The `/app/teams` link (see [people-and-teams.md](people-and-teams.md))
- `workspaceNavigation` / `organizationNavigation` compatibility aliases in
  `navigation.ts`, and the comment about "explicit work modes"

## Search

Already ⌘K and already fine. It gains what the new nav removes: people, rooms,
agents, files and apps in one list, grouped by kind, with the monogram beside
each result. Recent destinations when the field is empty.

## Needs from the backend

A per-person unread count for rooms — the room store already tracks presence
and sequence, so this is `last_seen_seq` on `room_presence`, which is also what
[rooms.md](rooms.md) needs for its catch-up line.
