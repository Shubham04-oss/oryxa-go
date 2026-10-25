# Plan: one type, radius and colour scale for web-next

Fixes U5, U6 and U7 in [06-ux-design.md](06-ux-design.md). This is a plan only; nothing is built yet. web-next is the owner's lane, so the plan names the owner's decisions first.

## Where things stand (measured 2026-10-25)

**Type.** About 25 font sizes appear in 36 component CSS files (11,814 lines).

| Size | Declarations |
|---|---|
| 12 px | 210 |
| 11 px | 201 |
| 13 px | 81 |
| 10 px | 62 |
| 12.5 px | 43 |
| 11.5 px | 24 |
| 14 px | 21 |
| 16 px | 15 |
| 8 to 30 px | the rest |

There's no size token. `src/styles.css` defines only `--font-sans` and `--font-heading`.

**Radius.** There are 13 values: 4, 5, 6, 8, 9, 10, 11, 12, 14, 16 and 18 px, plus 999 px and 50%. shadcn's `--radius-*` scale exists in `styles.css:56-62`, but the component CSS doesn't use it.

**Colour.**
- `src/theme.css` follows DESIGN.md §1: "every colour is a token". In practice, every literal colour was renamed `--tone-<hex>`.
- 175 tone tokens are in use: **116 are greys** and 59 tinted. 17 are defined but unused.
- The role tokens (`--base`, `--canvas`, `--raised`, `--text`, `--text-soft`, `--text-muted`) carry most text, but surfaces and borders are spread over dozens of greys that differ by 1–2 steps (#1b1b1b, #1c1c1c, #1f1f1f, #202020…).

**The written contract.** `web-next/DESIGN.md` §1 already asks for a compact scale ("workspace title about 18px, subsection title 13–16px, body 12–14px, and metadata 10–12px"), an 8/12/16/24/32 spacing rhythm, and one shared composer style. The code doesn't follow it, because nothing enforces it.

## Decisions for the owner

1. **Base size.** DESIGN.md allows body 12–14 px, but in practice it's 11–12. Proposal: body **14**, secondary **13**, metadata **12**, and **11** only for badges and counters. Nothing under 11. The other option is to keep the compact feel at body 13, metadata 12, floor 11.
2. **How far to collapse the greys.** Proposal: about 8 surface/border steps plus the existing text roles. Light-theme values come from the same mapping.
3. **Enforcement.** Add `stylelint` (it isn't installed) with a rule that rejects literal `font-size`, `border-radius` and colour values in `src/components/**/*.css`. The alternative is a 20-line grep check in CI, with no new dependency.

## The scale

Define these once in `src/theme.css`, next to the colour roles.

**Type tokens:**
```css
--fs-meta: 12px;   /* timestamps, counters, badges; 11px allowed only via --fs-badge */
--fs-badge: 11px;
--fs-small: 13px;  /* secondary lines, list subtitles */
--fs-body: 14px;   /* body, inputs, list titles */
--fs-title: 16px;  /* card and section titles */
--fs-page: 20px;   /* page heading */
--fs-hero: 30px;   /* home greeting */
```

**Radius tokens:**
```css
--r-sm: 6px;   /* chips, small icon buttons, inputs inside rows */
--r-md: 10px;  /* buttons, inputs, menu items */
--r-lg: 16px;  /* cards, panels, dialogs */
--r-pill: 999px;
```

**Mapping from today's values:**

| Today | Becomes |
|---|---|
| 8, 9, 9.5, 10, 10.5 px | `--fs-badge` (review each: most are labels that should be `--fs-meta`) |
| 11, 11.5 | `--fs-meta` |
| 12, 12.5, 12.8 | `--fs-small` |
| 13, 13.5, 14 | `--fs-body` |
| 15, 16, 17, 18 | `--fs-title` |
| 20, 22, 23 | `--fs-page` |
| 30, 34 | `--fs-hero` |
| radius 4–6 | `--r-sm` |
| radius 8–12 | `--r-md` |
| radius 14–18 | `--r-lg` |

Line heights move with sizes. Where a component sets `line-height` in px, change it to a unitless value (1.4 for body, 1.25 for titles).

**Greys.** Collapse the 116 in-use grey tones onto about 8 steps per theme:
- base, canvas and raised already exist;
- add `--surface-1` and `--surface-2` for card-on-card;
- keep `--border-quiet` and `--border-strong`;
- add `--hover` and `--selected`, which already exist.

Each grey tone maps to the nearest step. Tinted tones (red/green status fills) stay for now.

## Order of work

Each step is one PR. Each PR is screenshotted before and after at 375, 1024 and 1279 px, in dark and light.

1. **Tokens only.** Add the type and radius tokens and the grey steps to `theme.css`, plus the CI check in report-only mode. Nothing visible changes.
2. **Shell:**
   - `workspace-shell` and `workspace-sidebar` CSS;
   - the rail;
   - the header;
   - the brand tab.
3. **Home:** `home-overview.css` (643 lines). The same PR does U4's card rows, since the cards are being touched anyway.
4. **Chat:** `agent-session.css`, `agent-run.css`, `agent-chat-history.css`, `chats-page.css`. The shared composer CSS that DESIGN.md asks for goes here too: one composer class used by every composer (see [07-duplication.md](07-duplication.md)).
5. **Teamspaces and DMs:** `teamspaces.css` (691), `direct-messages.css` (550), `side-conversations.css`.
6. **Settings, library, tools:** `workspace-settings.css`, `library.css`, `workspace-tools.css` (969).
7. **The rest:** `agents-mode.css` (1,439), `call.css` (917) and `live.css`. Then switch the CI check from report to fail, and delete unused tone tokens.

Steps 2–7 are mostly mechanical find-and-replace with a per-file review. Two or three Sonnet workers in parallel can do them on disjoint files, once step 1 has pinned the tokens. Visual review stays with a human.

## Risks

- **Layout shift.** Bigger text wraps more, and fixed-height rows and the 300 px overview column will overflow. Each PR is checked at 1024 px, where things are tightest.
- **Conflicts with the owner's ongoing web-next edits.** Each PR touches one area's CSS only. Do it file by file, after the owner's current branch lands.
- **Tests.** `vitest` tests don't assert styles, so they won't catch regressions. The screenshots are the check.
- **DESIGN.md must change in the same PR as step 1:** the new sizes, the tokens, and the "no literal sizes or radii" rule. Its §6 requires that.
