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

## An agent is a monogram, not a robot

The single change that takes the generic feeling out of the app.

Every agent already has a name — Maya-XFZP, Cora-RGMI, Ledger. So:

- **Agents:** a 28px squircle, the first letter of the name in 13/600, white on
  a tint derived from the name. The tints are five steps around Iris (`#5B53C9`
  at 100%, 88%, 76%, 64%, 52% lightness of the same hue), picked by a stable
  hash of the agent id, so an agent looks the same everywhere forever and two
  agents in a room rarely collide.
- **People:** the same squircle in neutral — `--surface-strong` with `--ink`
  initials. People are not tinted; only agents are. That single rule means a
  room transcript can be read at a glance: warm-neutral lines are people,
  Iris-marked lines are agents.
- **Shared specialists** get the agent monogram with a hairline `--agent-edge`
  ring, which is what distinguishes "the team's pricing specialist" from
  "Ada's own agent" without a word of explanation.
- **Apps** use their real logo at 20px, monochrome only when the brand has no
  usable mark.

Sizes: 20px inline in a message line, 28px in rosters and lists, 40px on a
detail page header.

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
