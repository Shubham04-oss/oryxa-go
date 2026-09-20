# Foundations

Tokens first. Nothing else in this plan can be built until these exist, and
once they do, most of the "it feels cheap" problem is already gone.

## Colour

The base is right and stays: a warm grey canvas with warm near-black ink. What
is missing is everything on top of it. Today the theme defines three accent
slots — `--workspace-warm`, `--workspace-rose`, `--workspace-sage` — and sets
all three to the same grey, while the markup still asks for three different
colours. The product is not monochrome by design. It is monochrome by erasure.

Five hues, each with an ink and a soft tint. That is the whole palette.

### Light

| Token | Hex | What it is for |
| --- | --- | --- |
| `--canvas` | `#F7F7F6` | the page |
| `--surface` | `#FFFFFF` | cards, rows, sheets |
| `--surface-subtle` | `#FBFBFA` | nested panels, table headers |
| `--border` | `#E2E2DE` | hairlines |
| `--border-strong` | `#D2D2CC` | edges that need to be seen |
| `--ink` | `#171716` | text |
| `--ink-muted` | `#6B6B67` | secondary text |
| `--ink-faint` | `#8A8A84` | captions, timestamps |
| **Ember** `--brand` | `#C1502E` | the one primary action on a page |
| `--brand-hover` | `#A64322` | its hover |
| `--brand-soft` | `#FBEEE8` | its tint |
| `--brand-edge` | `#EFD2C5` | its border on tinted surfaces |
| **Iris** `--agent` | `#5B53C9` | agents: monograms, the agent badge, an agent's line |
| `--agent-soft` | `#EEEDFB` | agent surfaces |
| `--agent-edge` | `#D9D6F4` | agent borders |
| **Moss** `--ok` | `#3F7A48` | active, connected, present, healthy |
| `--ok-soft` | `#EAF3EB` | |
| **Amber** `--attention` | `#9A6B10` | waiting on somebody, paused, expiring |
| `--attention-soft` | `#FBF2DF` | |
| **Clay** `--danger` | `#B53C35` | destructive and failed |
| `--danger-soft` | `#FFF0EE` | |

Ember is warm on purpose. Against a warm grey canvas a blue accent fights the
base and lands the app in the middle of every other B2B product; terracotta
belongs there and is recognisably ours.

Iris earns its own hue because roughly half of what happens in this product is
done by an agent, and a person must be able to tell at a glance whether a
colleague or a colleague's agent just said something. It is the only decorative-
looking use of colour in the system, and it is not decorative: it is identity.

### Dark

Real dark mode, not an inversion. Surfaces lift by lightness; shadows do
nothing in the dark.

| Token | Hex |
| --- | --- |
| `--canvas` | `#141412` |
| `--surface` | `#1C1C1A` |
| `--surface-subtle` | `#191917` |
| `--border` | `#2C2C29` |
| `--border-strong` | `#3A3A36` |
| `--ink` | `#F3F3F0` |
| `--ink-muted` | `#A6A6A0` |
| `--ink-faint` | `#84847E` |
| `--brand` | `#E2744B` |
| `--brand-soft` | `#2A1C15` |
| `--agent` | `#9A93F0` |
| `--agent-soft` | `#1E1C33` |
| `--ok` | `#68B471` |
| `--attention` | `#D6A441` |
| `--danger` | `#E4736A` |

Shipped behind `prefers-color-scheme` with an explicit override attribute, so a
person can pin it either way. Today there is no dark mode at all, so this is
new work rather than a repaint — it belongs at the end of phase 5, but the
tokens are defined now so nothing has to be redone later.

### Rules

1. One accent per card. If a card already carries an agent tint, its button is
   quiet, not Ember.
2. Exactly one Ember element per screen: the primary action. A second Ember
   button means the page has two jobs and needs splitting.
3. Status colour never appears without a word beside it. A green dot alone is
   not an accessible status.
4. Contrast targets: 4.5:1 for text, 3:1 for control edges and icons. Every
   pair in this table is verified against both surfaces before it ships; the
   soft tints are backgrounds only and never carry text of their own hue at
   small sizes.
5. Charts use the categorical five in this order — Iris, Ember, Moss, Amber,
   Slate `#4C6272` — never the five greys they use today.

## Type

Everything on screen today sits between 10px and 13px, in grey. That is the
main reason the app reads as texture rather than content.

One family (Geist Variable, already loaded), one scale:

| Role | Size / line / weight | Where |
| --- | --- | --- |
| Display | 28 / 1.2 / 600 | the home greeting, nowhere else |
| Title | 20 / 1.3 / 600 | page title |
| Section | 15 / 1.4 / 600 | section headings |
| Body | 14 / 1.55 / 400 | everything a person reads |
| Secondary | 13 / 1.5 / 400 | supporting lines |
| Caption | 12 / 1.4 / 500 | timestamps, counts, meta |
| Badge | 11 / 1 / 600, `0.04em` | uppercase labels, 12 characters maximum |

11px is allowed only in a badge. Nothing below 12px carries a sentence.

Figures in any table, counter, or price use `font-variant-numeric:
tabular-nums`. Body copy sets `text-wrap: pretty`; titles set `text-wrap:
balance`.

## Space

A 4px base, and a scale that does not improvise: **4, 8, 12, 16, 20, 24, 32,
40, 56**.

- Card padding 20 (16 below 640px)
- Gap between sections 24
- Page gutter 32 (16 below 640px)
- Content max width 1120px; any column of prose max 72 characters
- Row minimum height 44px, so every row is a touch target
- Dense table rows 40px

Radii keep what they have: 16 for cards, 10 for controls, 999 for badges and
pills.

## Elevation

Borders, not shadows. One hairline shadow token exists for menus, popovers, and
dragged things; cards never use it. In dark mode elevation is lightness only.

## Motion

| What | Duration | Curve |
| --- | --- | --- |
| Hover, tint, focus ring | 120ms | ease-out |
| Disclosure, panel, sheet | 180ms | ease-out |
| Route change | 240ms | ease-out |

Nothing animates position by more than 8px. Everything respects
`prefers-reduced-motion: reduce` by dropping to an opacity change. A skeleton
that would show for under 400ms is not shown at all — a quiet, correctly sized
placeholder beats a flash of bones.
