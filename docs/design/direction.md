# Direction

## What is actually wrong

The app is not ugly. It is clean, consistent, and carefully built. It is also a
documentation site for its own architecture, and that is what makes it tiring.

Four things, in the order they hurt.

**It explains itself instead of getting out of the way.** The second section of
the home screen is titled `WORK MODES — One product, clear boundaries`, with
three cards labelled `SINGLE USER`, `MULTI-AGENT`, `MULTI-USER`. Agent system
carries a panel called "Operating model" diagramming Maya → Specialists →
Requests. Teamspaces opens on "Whisperer presence, deliberate sharing". These
are design notes rendered as UI. Meanwhile "Recent" — the reason a person came
back — is one line, below the fold.

**The vocabulary is ours.** Whisperers. Contributed specialists. Bounded
specialists. Shared surface. Organization context. Synthesis. Proposals.
Delegation mode: allow automatic / ask owner first. The roster explains one
term with three more: "Available whisperers — one private-side primary agent
per active teammate." Nobody arrives knowing these words, and no screen teaches
them.

The strange part is that the app already knows how to write. Account settings
says "Where sign-in links and notices go" and "Your agents can no longer act
for you while you're away." That voice exists. It just never reached the
surfaces people meet first.

**Everything weighs the same.** One white card, one hairline border, one 11px
grey caption — for a launch decision, for a counter that reads `0`, and for
"No agent requests yet." Nothing is louder than anything else, so the eye gives
up and the page reads as texture. No screen has a single primary action: home
offers a composer *and* three cards; a Teamspace offers "Share work" and
"+ Contribute" at identical volume.

**The colour was taken out and the app still expects it.** The theme defines
`--workspace-warm`, `--workspace-rose` and `--workspace-sage` — three slots for
three different things — and sets all three to `#555551`. The three "Start
here" rows on home still carry `data-tone="warm" | "rose" | "sage"`, still ask
for three colours, and still render identically grey. `--status-active` is
grey. `--agent-accent` is near-black. The five chart colours are five greys.
The structure for meaning is there; it has been painted out.

There is no dark mode at all: 5,159 lines of CSS, one `color-scheme: light`, no
`prefers-color-scheme` block anywhere.

## Six rules

1. **The work is the interface.** Every screen opens on the thing a person came
   to do. Explanation earns its place only where somebody is stuck, only once,
   and never above the work.
2. **Say it the way you would say it out loud.** If you would not say
   "contributed specialist" to a colleague, it does not go on screen.
3. **One page, one job, one primary action.** Everything else is quiet.
4. **Colour means something.** Action, identity, status, danger. Never
   decoration, never two accents in one card. How much colour — see the three
   candidates in [foundations.md](foundations.md); the room is drawn in the
   quietest of them.
5. **An icon must do work,** and never stands in for a person. Navigation, a
   repeated verb in a dense row, a status mark — everything that means a who
   is an avatar.
6. **Delete before you design.** A screen that survives this plan is a screen
   that earned it.

## Navigation

Eight items in three labelled groups, plus five tabs inside one of them, plus
six settings routes — for a product that does two things: you talk to your
agent, and your team talks with theirs.

There are literally two pages for teams today: `/app/teams` ("People & teams")
and `/app/settings/organization/teams` ("Teams").

**Now**

```
SOLO WORKSPACE      COLLABORATE          RESOURCES
  Overview            Teamspaces           Knowledge bases
  Conversations       People & teams       Files
  Agent system                             Apps & tools
```

**Becomes**

```
  Home        your agent, your chats, what needs you
  Rooms       where your team and its agents talk
  Agents      yours and the team's specialists, and what they are running
  Library     knowledge and files — everything your agents can read
  Apps        what they can reach

  Settings    organization (people, teams, billing, email, tool access) · account
```

Five items, no group labels, no mode taxonomy. What moved:

- **Teamspace → Room.** It is a room. People say room.
- **People & teams is deleted as a page.** Who is on a team is either settings
  (managing it) or the room's own roster (working with it). The duplicate goes.
- **Agent system's five tabs collapse** into one page; workflows and requests
  appear as sections only when they exist.
- **Knowledge bases and Files merge into Library.** To a person they are the
  same thing: stuff my agents can read.
- **The header eyebrow goes.** `SOLO WORKSPACE / Simulated Org` above every
  page title is a label for a concept, not a place.

## The words

Applied everywhere, including error copy, dialog titles, and empty states.

| Now | Becomes |
| --- | --- |
| Teamspace | Room |
| Whisperer | *(name the person)* Ada's agent · your agent |
| Available whisperers | Who is in this room |
| Contributed specialist | Shared specialist |
| Bounded specialist | Specialist |
| Shared surface | the room · the ledger |
| Shared work ledger | What this room decided |
| Organization context | What the organization knows |
| Synthesis / Synthesizing | Summary / Writing the summary |
| Proposals | Suggested next steps |
| Delegation mode: allow automatic | Anyone here can use it |
| Delegation mode: ask owner first | Ask me first |
| Agent system | Agents |
| Primary agent | Your agent |
| Entry kind: update | Note |
| Entry kind: artifact | File |
| Entry kind: handoff | Handover |
| Run / durable workflow | *(keep)* — these are honest words |

Two words stay because they are load-bearing and true: **decision** and
**result**.

## Deleted outright

- The `WORK MODES` section and its three cards (home)
- "One product, clear boundaries" and "Private by default. Shared only when you
  choose a shared surface."
- The "Operating model" diagram (agents)
- Counter tiles that cannot be acted on: `1 Specialists · 1 Teams · 1 Workflows
  · 0 Open requests`, and `2 Decisions · 1 Risks · 20 Proposals`
- "Whisperer presence, deliberate sharing" and every other explainer strip
- The `/app/teams` page
- Manual **Refresh** buttons on surfaces that already poll
- The `SOLO WORKSPACE / COLLABORATE / RESOURCES` group labels and the page
  eyebrow

## How we will know it worked

- A person who has never seen the app can say what the home screen is for
  without being told.
- No screen shows a number that cannot be acted on.
- Every page fits its job above the fold at 1280×800, room transcript included.
- No word on screen appears in this document's left-hand column.
