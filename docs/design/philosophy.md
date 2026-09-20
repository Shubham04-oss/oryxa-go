# Philosophy

Two references, and one rule that follows from both.

## What Linear actually does

Not the dark theme — the discipline underneath it.

- **Nothing is decorative.** Every element is content, or a control you are
  about to use. There is no third category.
- **Density with air.** Small type, tight rows, and then generous margins
  around the whole thing. Compression happens inside a group; space happens
  between groups.
- **Icons label, they never illustrate.** One grid, one stroke, one optical
  weight. You stop noticing them, which is the point.
- **Speed is a material.** Instant transitions, optimistic writes, keyboard
  before mouse. Nothing announces that it is loading unless it genuinely is.
- **The product never explains itself on screen.** No tours, no "here's how
  this works" panels, no taxonomy lessons.

## What Apple actually does

- **The expected thing happens.** Conventions are honoured exactly, and the
  reward is that nobody has to learn anything.
- **Defaults are opinions.** You can change them; you never have to.
- **One obvious action.** Depth lives behind a tap, never in front of it.
- **Plain words.** No internal vocabulary reaches the surface, ever.
- **It does not require understanding to use.** You can be wrong about how it
  works and still get what you wanted.

## The rule

**Pass as little as possible on to the user.**

Every choice we display is a choice we failed to make. Every word they have to
learn is a word we failed to translate. Every button labelled "refresh" is a
job we failed to do. When something must be decided, the system decides and
says what it decided, in a place where changing it is easy and obvious.

The test for anything on screen: *what does this ask the person to carry?* If
the answer is "a concept of ours", it goes.

## What this app stops asking

This is where the rule earns its keep, because this product currently asks a
great deal.

| Today the person must… | Instead |
| --- | --- |
| Pick a kind when publishing — update, result, artifact, handoff, decision | The room already judges this on every message. Nobody picks a kind; the label is output, not input |
| Decide to publish at all ("Share work") | Saying the thing is the publishing. A decision is kept because it was a decision |
| Choose a delegation mode per specialist — "ask owner first" or "allow automatic" | Default to asking. The switch lives in that specialist's own settings, not in the flow of adding it |
| Press "Catch me up" | The unread rule is already drawn where they stopped, and the brief sits at the top of the room |
| Live with `Maya-XFZP` | The system names the agent. Renaming is one click. Uniqueness in a room is the room's problem, not the person's |
| Choose which of their agents authors a publication | It is their agent. There is no choice to present |
| Press Refresh | The room polls |
| Pick a team when creating a room | Rooms belong to teams. Create it from the team and there is nothing to choose |
| Read "whisperer", "contributed specialist", "shared surface", "synthesis" | Plain words, everywhere, including errors and empty states |
| Work out what a switch is currently doing | Every switch states its present effect in a line beneath it |

Ten things removed from ten different screens, and not one of them is a
feature cut. The capability stays; the decision moves to us.

## Icons

Sharp, on a grid, drawn rather than picked.

- **16px viewBox**, 1.5px stroke, round caps and joins, `currentColor`.
- Geometry snapped to half-pixels so nothing blurs at 1×.
- At most three elements per icon. If it needs four, it is a picture, not an
  icon.
- Around twenty of them, drawn as one set so the weights match. Five for
  navigation, the rest verbs.
- Never a metaphor for a who: people and agents are avatars.
  See [icons.md](icons.md).

## What we do not do

- Modal dialogs for content. They are for decisions only.
- Empty states that teach vocabulary.
- Counters that cannot be acted on.
- Tooltips that explain a word we chose badly — we choose a better word.
- Confirmation for anything reversible. Undo instead.
- Loading states for work that finishes in under 400ms.
- More than one primary action on a screen.
