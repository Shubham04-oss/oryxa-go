# Icons

## What is wrong now

Every row, every heading, and every nav item leads with a glyph, and the
glyphs were chosen one at a time. A brain-circuit for the organization agent. A
robot for agents. A network node for Teamspaces. Sparkles and a magic wand for
anything that felt clever. A pull-request arrow in a product with no pull
requests. Twelve `Bot`s, seven `Network`s, six `Sparkles`.

Two costs. The obvious one: it looks generic, because these are the same
default glyphs every AI product ships with, in the same places. The quieter
one: when everything leads with an icon, the icon stops meaning anything, and a
person scanning a list reads eleven identical grey squares instead of eleven
different things.

## Where an icon is allowed

1. **Navigation** — five items, five icons, learned once and then recognised
   forever.
2. **A verb that repeats in a dense row** — delete, edit, copy, open. When the
   same action appears on forty rows, a word forty times is worse.
3. **A status mark** — beside a word, never instead of it.
4. **An app's own brand mark** — GitHub is GitHub's logo, not a plug.

That is the entire list.

## Where it is banned

- Beside a section heading. "Shared work ledger" does not need a stack of
  papers next to it.
- As a mascot in an empty state. Say what is missing and what to do; a large
  grey outline of a folder adds nothing.
- On every row of a list, as an ornament.
- To mean "AI": no `Sparkles`, no `WandSparkles`, no `BrainCircuit`, no `Bot`.
  An agent is represented by who it is, below.

## An avatar wherever a glyph stood for somebody

The icon set itself is fine. What has to go is the glyph standing in for a
*who*: every place that shows a robot, a network node or a plug is a place
that should show the face of whoever it means.

- **People** — a 36px rounded square (radius 8), initials in 14/600, on a
  muted ground picked by a stable hash of the user id: sand, dusk, clay, sage,
  slate. Desaturated on purpose, so a roster does not turn into confetti. A
  real photo replaces it the moment there is one.
- **Agents** — the same square in Iris, initial in white, the tint picked by a
  stable hash of the agent id from five steps around the hue. An agent never
  reads as a colleague at a glance, and the tag beside its name says the rest.
- **Shared specialists** — the agent avatar with a hairline ring, which is
  what distinguishes "the team's pricing specialist" from "Ada's own agent"
  without a word of explanation.
- **Apps** — their own logo at 20px inside a neutral square. GitHub is
  GitHub's mark, never a plug.
- **A room** — the facepile of who is in it, three avatars overlapped at -8px
  with a 2px surface ring. A room is the people in it; that is also the only
  thing about it you would recognise from across a list.

Sizes: 20 inline, 28 in lists and rosters, 36 in a message row, 44 on a detail
header. Radius 8 at every size except 20, which takes 6.

Beside the name, an agent carries a quiet `AGENT` tag — 11/600, Iris tint,
the same pattern Slack uses to mark an app. When it is answering in somebody's
absence it also carries `standing in for Ada`, which is the one thing a reader
must never have to work out for themselves.

## The rest of the icon set

Lucide stays, at one size scale (16 and 20), 1.5px stroke, `currentColor`, and
a short allowlist:

`Plus` · `Trash2` · `Pencil` · `Search` · `Check` · `X` · `ChevronDown` ·
`ChevronRight` · `ArrowUp` (send) · `ArrowUpRight` (opens elsewhere) ·
`ExternalLink` · `Paperclip` · `MoreHorizontal` · `AlertTriangle` ·
`CircleAlert` · `Loader2`

Navigation adds exactly five: Home, Rooms, Agents, Library, Apps.

Anything outside this list needs a reason at review, and the reason cannot be
"it looked nice there". The list is meant to be short enough that adding to it
is a decision.

## Status marks

A status is a word plus an optional 6px dot in the status colour:

| State | Dot | Word |
| --- | --- | --- |
| Active, connected, present | `--ok` | Active · Here now · Connected |
| Waiting, paused, expiring | `--attention` | Waiting on Ada · Paused |
| Failed, revoked | `--danger` | Failed · Revoked |
| Off, neutral | none | Off · Not connected |

Never a dot on its own, and never colour as the only signal.
