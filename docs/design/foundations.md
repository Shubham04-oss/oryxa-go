# Foundations

Black, eleven greys, white, and dots. That is the whole system.

## Surfaces

Pure black underneath, the way Vercel does it, so that lightness is free to
mean something. Nothing in the product is ever coloured.

| Token | Value | What it is |
| --- | --- | --- |
| `--app` | `#000000` | the page, everywhere |
| `--card` | `#0A0A0A` | a card — and only a card |
| `--raised` | `#161616` | composer, the row you are on, a quiet button |
| `--line` | `#1F1F1F` | a separator's dot |
| `--edge` | `#2E2E2E` | an edge that must be seen |
| `--ghost` | `#454545` | disabled text |
| `--faint` | `#6E6E6E` | timestamps, the least of it |
| `--meta` | `#8F8F8F` | secondary lines |
| `--second` | `#A1A1A1` | supporting text |
| `--ready` | `#D4D4D4` | a button at rest |
| `--text` | `#EDEDED` | what you read |
| `--now` | `#FFFFFF` | the one thing to do next |

### Grey is ready, white is now

This replaces every accent colour the product might have had.

- A button rests at `--ready`: clean light grey, obviously pressable, nothing
  has happened yet.
- It becomes `--now` the moment it is the thing to do — you typed something,
  or it is the single action the screen exists for.
- Everything that is not the point is `--raised` with `--meta` text.
- The same rule runs through every control: a switch is grey off and white on,
  a row is dark until it is the one you are in, an unread mark is a small white
  dot, an agent's avatar is near-white where a person's is dark grey.

**The check:** count the white on a screen. More than one white element means
the screen has more than one job.

### What it cannot say

Danger. Delete and save look identical, so a destructive action is carried by
its words and by a dialog rather than by a red fill. That is stricter than a
red button and slower on purpose. If it proves too quiet in real use, one red
is the smallest exception worth making — and it would appear nowhere except
failure and deletion.

## Separators

Dotted, always. A 1px dot every 5px in `--edge`, horizontally and vertically:

```css
/* a rule */
height: 1px;
background-image: linear-gradient(to right, #2E2E2E 1px, transparent 1px);
background-size: 5px 1px;
```

There should be almost none of them. The room has exactly one — the edge
between the conversation and your agent. Space separates things; a line is
what you reach for when space has already failed.

## Type

Geist, already loaded. The message is the biggest thing on its row, because a
chat is read at speed and everything else is scaffolding.

| Role | Size / line / weight | Where |
| --- | --- | --- |
| Title | 22 / 1.25 / 600, `-0.014em` | a room's name, a page |
| Name | 14 / 1.3 / 600 | who said it |
| Message | 15 / 1.6 / 400 | what they said |
| Body | 14 / 1.6 / 400 | cards, settings, descriptions |
| UI | 13 / 1.4 / 400–500 | rows, buttons, the rail |
| Meta | 12 / 1.4 | time, counts |
| Label | 10 / 600, `0.08em`, caps | the one word on a card |

Nothing below 12px carries a sentence. Figures use `tabular-nums`.

## Shape and space

- Radii: **14** a card, **8** a control, **6** an avatar or a small chip.
- Card padding 16–18; at least 20 of air around it.
- Reading column 640–720px, centred, whatever the window does.
- Rows 44px minimum.
- Scale: 4, 8, 12, 14, 18, 20, 24, 32, 40.

No shadows. A card is one step of light and a single inset hairline along its
top edge — `inset 0 1px 0 rgb(255 255 255 / 4%)` — which is the only highlight
in the system.

## Dots

The one material that moves. Four states, each a different shape, generated
geometry rather than an image: a sphere is thought, a turning globe is search,
a scatter is a tool working, a ring is a person being waited on.

Dots always mean something is happening. Never a texture, never a background,
never a decorative grid — if nothing is running, there are no dots on screen.
Always beside words, because an orb alone is a mystery. 20px inline, 46px when
it is the only thing there.

See [dots.md](dots.md).

## Motion

The orbs, and nothing else.

| What | Duration | Curve |
| --- | --- | --- |
| Hover, tint, focus | 120ms | ease-out |
| Disclosure, sheet | 180ms | ease-out |
| Route change | 240ms | ease-out |

Nothing travels more than 8px. `prefers-reduced-motion: reduce` stops the orbs
where they are — the shape and the sentence still say everything — and drops
every transition to an opacity change.
