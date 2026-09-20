# Rooms (`/app/teamspaces` → `/app/rooms`, `/app/rooms/$id`) · Phase 3

**Job:** talk with your team and their agents, and see what the talking settled.

This is the best thing in the product and the fourth section of its own page.

## Today

One route stacks seven things vertically:

1. An organization-context card — the org agent's rolling summary, `Shared
   context only · Revision 25 · 1 hr`, three counters (`2 Decisions · 1 Risks ·
   20 Proposals`), and a proposal
2. `SHARED ROOMS` — the list of rooms, a strip three rows tall
3. The room header, with **Share work**
4. "Whisperer presence, deliberate sharing" — an explainer
5. **Available whisperers** — three cards, each "Your primary · private-s…"
6. **Contributed specialists** — with `Ask owner first` / `Allow automatic`
7. **Conversation** — the room itself
8. **Shared work ledger** — 38 entries
9. A delete zone

At 1024px the conversation begins below the second scroll. Every section is the
same white card with the same grey caption, so the roster of agents weighs
exactly as much as the room.

## Becomes

A list route and a room route. The room route is the room.

**`/app/rooms`**

```
  Rooms                                              [ + New room ]

  What the organization knows                        Updated 1 hr
  Firmware 0.9.3 builds clean on the new board. Northwind was chosen
  for enclosures on shorter lead times. The launch date of the 14th holds.
  2 decisions · 1 risk                                   See all →

  ┌──────────────────────────────────────────────────────────┐
  │ [▤] Launch Council            Launch crew · 4 here   3 ● │
  │     "@member friday works, I'll send the numbers…"       │
  └──────────────────────────────────────────────────────────┘
```

**`/app/rooms/$id`** — above 1280px, transcript and rail; below, one column
with the rail's contents collapsed under the composer.

```
  ← Launch Council                                    ⋯

  ┌────────────────────────────────────┐  ┌──────────────────┐
  │ [S] Shubham            11:04       │  │ In this room     │
  │ @ledger what did the last two…     │  │ [S] You     here │
  │                                    │  │ [P] Priya   here │
  │ [L] Ledger  agent                  │  │ [A] Ada     away │
  │ The last two Northwind invoices    │  │ [M] Maya  · yours│
  │ came to $1,250.00 each.            │  │ [L] Ledger·shared│
  │                                    │  │  pricing sheet   │
  │ [M] Maya  agent · for Ada  11:09   │  ├──────────────────┤
  │ Agreed on Friday for the second    │  │ Decided here     │
  │ mould answer.                      │  │ Northwind at     │
  │                                    │  │ $4.50 · 1 hr     │
  └────────────────────────────────────┘  │ Hold the 14th    │
  ┌────────────────────────────────────┐  │ Owed: Shubham →  │
  │ Say something…                 ↑   │  │ vendor by Friday │
  └────────────────────────────────────┘  │        See all → │
                                          └──────────────────┘
```

- **The transcript is the page.** It opens scrolled to the end, follows while
  you are at the end, and stops the moment you scroll back.
- **People are neutral, agents are Iris.** A person's line carries their
  initials; an agent's carries its monogram, the word `agent`, and — when it
  answered in somebody's absence — `for Ada`. This is already built and is the
  single most important honesty in the room.
- **The rail is who and what**, not configuration. Presence is a word, not a
  card. "Shared" and "yours" replace "contributed specialist" and "whisperer".
- **Decided here** is the ledger, reduced to what a person scanning wants: the
  decisions, the owed items, and a link. The full ledger is a route of its own
  (`/app/rooms/$id/decided`), which is also where the noise problem stops being
  visible on the main screen.
- **The organization summary moves up a level**, to the rooms list. It is about
  every room, so it does not belong inside one.
- Managing the room — contributing a specialist, renaming, deleting — moves
  into the `⋯` menu and a settings sheet. Three of the current nine sections
  exist only to configure, and configuration is not what a person comes here to
  do.

## Deleted

- "Where teammates' primary agents collaborate quietly, and only intentional
  work becomes shared context."
- "Whisperer presence, deliberate sharing" and its paragraph
- The **Available whisperers** card grid (becomes rail rows)
- `2 Decisions · 1 Risks · 20 Proposals` counters
- The **Refresh** button — the room polls
- The delete zone at the bottom of the page (moves to the `⋯` menu)

## Words

| Now | Becomes |
| --- | --- |
| Teamspace | Room |
| Shared rooms | Rooms |
| Available whisperers | In this room |
| Your primary · private-side agent | yours |
| Contributed specialists | Shared specialists |
| Explicit capabilities with owner-controlled delegation. | *(deleted)* |
| Ask owner first / Allow automatic | Ask me first / Anyone here can use it |
| Shared work ledger | Decided here |
| Immutable, attributed publications. Newest first. | *(deleted)* |
| Organization context · Synthesizing | What the organization knows · Updating |
| Share work | Publish something |

## States

- **New room:** the transcript area holds one line — "Nobody has said anything
  yet. Say something and whoever's work it is will answer." — and the rail
  already shows who is here, which is the reassuring part.
- **Room answering:** the wait appears where the answer will be, with the seat
  that is thinking. Today the composer disables and says "The room is
  answering…", which is honest but puts the wait in the wrong place.
- **Been away:** a divider line in the transcript — "12 lines since you were
  here · Catch me up" — instead of a button that a person has to think to press.
- **Paused room:** the composer is replaced by one line saying who paused it.

## Needs from the backend

- `last_seen_seq` per person on `room_presence`, for the unread divider and the
  sidebar count.
- The rooms list needs each room's last line and present count — one extra
  projection on the list endpoint.

## Open questions

1. An organization owner or admin who is not on the team can read a room's
   whole conversation today. Decide deliberately, and if it stays, the rail
   should say it: "Owners can read this room."
2. The ledger's older entries include chatter published before the signal
   filter landed ("Lunch", "Thanks", "+1 to the later standup"). Hide entries
   below a quality bar, or leave history as it was?
