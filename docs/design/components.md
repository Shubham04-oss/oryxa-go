# Components

The contract every screen is built from. If a page needs something that is not
here, that is a design decision and goes in this file first.

## The page

```
Title (20/600)                                   [ primary action ]
One line of what this page is for, only if it is not obvious.

  section
  section
```

- One `h1`, one primary action, top right.
- The description line is optional and usually unnecessary. "Conversations —
  every conversation with your primary agent, in one place" tells a person
  nothing they did not know from the word Conversations. Delete it.
- No eyebrow above the title.
- Sections carry a 15/600 heading and nothing else: no icon, no count badge
  unless the count is the point, no horizontal rule.

## Card

A white surface, 1px `--border`, radius 16, padding 20.

- Cards hold **things**, not explanations.
- A card never nests another card. One level of surface inside a card:
  `--surface-subtle`, radius 10, no border.
- A card with a single number in it is not a card; it is a sentence. Write the
  sentence.

## Row

The workhorse. Lists of rooms, agents, files, members, apps.

```
[monogram]  Name                      meta · meta        [ actions ]
            secondary line
```

- 44px minimum, 12px vertical padding, hairline separators rather than gaps —
  a list is one object, not eight floating ones.
- The whole row is the click target when it navigates. Actions inside it stop
  propagation.
- Hover is a 4% ink tint, not a border change, not a shadow.
- Actions appear on hover and on focus, and are always present for touch.

## Buttons

| Kind | Look | Use |
| --- | --- | --- |
| Primary | Ember fill, white text | the one action a page is for |
| Default | white, `--border-strong`, ink text | everything else |
| Quiet | no fill, no border, ink-muted | inside rows and toolbars |
| Danger | white with `--danger` text; fill only inside a confirm dialog | destructive |

Height 36 (32 for quiet, 44 for the primary action on mobile). Label is a verb
in the imperative: **Connect an app**, **Add a specialist**, **Leave room**.
Never "Submit", never "OK".

Disabled buttons must say why in a line beneath, or not be disabled.

## Badges

11/600 uppercase, `0.04em`, tinted background, no border, radius 999. Three
only: **agent** (Iris), **status** (colour per the table in
[icons.md](icons.md)), **kind** (neutral). A badge is at most 12 characters.

## Empty states

Three lines, left-aligned, inside the surface that would have held the content.
No illustration, no icon.

```
Nothing here yet.
One sentence about what will appear and who puts it there.
[ the action that creates the first one ]
```

Today's empty states describe the system's state of mind — "No agent requests
yet", "Nothing has been made shared yet". Write them from the person's side:
"Nobody has asked your agents for anything. When a teammate's agent needs
something from yours, it lands here."

## Loading

- Never a spinner for data that usually arrives in under 400ms.
- Lists render their container and a quiet placeholder of the right height.
- A pending action disables its own button and says what is happening on it
  ("Sending…"), rather than covering the page.
- A long operation that blocks — a room answering, a run starting — shows the
  wait where the result will appear, not where the button was.

## Errors

Four kinds, four voices:

| Kind | Voice | Example |
| --- | --- | --- |
| We could not load this | Plain, retryable | "We could not load your knowledge bases. **Try again**" |
| You cannot do this | Say who can | "Only an owner or admin can create a team." |
| You did something we cannot accept | Say the limit | "A message here is at most 4,000 characters." |
| It broke | Admit it, keep the work | "Something went wrong on our side. Your draft is still here." |

The list page that currently greets a person with *"That no longer exists, or
you do not have access to it"* is a 404 on a collection. "That" names nothing
the person asked for. Collection failures use the first voice, with the
collection's own name in the sentence.

## Dialogs

For decisions, never for content. A dialog has a title that is a question or a
noun phrase, one paragraph of consequence, and two buttons where the
destructive one is not the default. Anything a person needs to consult the
page to answer belongs on the page, not in a dialog.

## Density and breakpoints

| Width | Shell | Content |
| --- | --- | --- |
| < 640 | sidebar becomes a sheet | one column, 16px gutter |
| 640–1024 | icon-only rail | one column, 24px gutter |
| 1024–1440 | full sidebar | one column, max 1120 |
| > 1440 | full sidebar | one column, max 1120, centred |

No screen gets a second column just because there is room. The room transcript
and the ledger beside it is the single exception, above 1280.
