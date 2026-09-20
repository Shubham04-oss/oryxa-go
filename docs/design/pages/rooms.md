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

A list route and a room route, and the room route is a plain group chat: a
column of rooms, a column of messages, a box at the bottom. Nothing else
permanent on screen.

**`/app/rooms`** keeps the organization summary at the top — it is about every
room, so it does not belong inside one — over a list where each row is the
faces in that room, its last line, and what is unread.

**`/app/rooms/$id`**

```
┌──────────┬─────────────────┬────────────────────────────────────────┐
│ Home     │ Rooms        +  │ Launch Council            [SH][PR][AD] │
│ Rooms  3 ├─────────────────┤ Launch crew · 4 here    ✓ 3 decisions ⋯│
│ Agents   │ Launch Council  ├────────────────────────────────────────┤
│ Library  │ Maya: Agreed…   │           ──────  Today  ──────        │
│ Apps     │                 │                                        │
│          │ Firmware     2  │ [SH] Shubham  11:04                    │
│          │ Kepler: 0.9.3…  │      @Ledger what did the last two     │
│          │                 │      Northwind invoices come to?       │
│          │ Vendors     Tue │                                        │
│          │ Ada: freight…   │ [L]  Ledger  AGENT  shared · pricing   │
│          │                 │      $1,250.00 each, on the last two.  │
│          │                 │ ─────────────────────────────── NEW ── │
│ Settings │                 │ [PR] Priya  11:12                      │
│          │                 │      decision: we hold the 14th…       │
│          │                 │      ✓ Kept as a decision              │
│          │                 │ ┌────────────────────────────────────┐ │
│          │                 │ │ Message Launch Council         ↑   │ │
│          │                 │ └────────────────────────────────────┘ │
└──────────┴─────────────────┴────────────────────────────────────────┘
```

- **Messages, not cards.** Avatar, bold name, time, and what was said at 15px.
  Consecutive lines from the same person lose the avatar and keep a hover
  timestamp. No borders, no tinted boxes — the name is the boundary.
- **Agents sit in the same column as everybody else**, marked by a quiet
  `AGENT` tag and, when they answer for somebody who is away, `standing in for
  Ada`. This is the room's central honesty and it costs one tag.
- **A decision is marked where it happened** — "✓ Kept as a decision" under the
  message — rather than filed silently into a ledger nobody has open.
- **The ledger is a pill in the header** (`✓ 3 decisions`) that opens
  `/app/rooms/$id/decided`. No permanent right rail: who is here is the
  facepile, and everything else is a click.
- **The unread line is a rule across the column** with NEW at its end, drawn
  where the person left off — no button to press, no catch-up to remember.
- Managing the room — contributing a specialist, renaming, deleting — lives in
  the `⋯` menu.

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
- **Been away:** the NEW rule sits where you left off, and the brief is one
  click behind it — never a button you have to think to press.
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
