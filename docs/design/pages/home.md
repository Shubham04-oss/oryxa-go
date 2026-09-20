# Home (`/app`) · Phase 3

**Job:** start work, or pick up where you left off.

## Today

A centred hero asks *"What do you want to move forward?"* over a composer —
which is the right instinct. Then the page spends its remaining height on
`WORK MODES — One product, clear boundaries`, three cards labelled `SINGLE
USER` / `MULTI-AGENT` / `MULTI-USER`, a "Start here" list of three generic
prompts, and finally **Recent**, which holds the single most useful thing on
the screen and gets one line, below the fold.

The three "Start here" rows carry `data-tone="warm" | "rose" | "sage"` — they
were designed to be three colours and render as three identical greys.

## Becomes

The composer is the page. Everything under it is the person's own work.

```
        Good morning, Shubham

    ┌────────────────────────────────────────────┐
    │  Ask your agent, or start something…    ↑  │
    └────────────────────────────────────────────┘
      Plan something   Review a file   Use an app      ← first week only

  Needs you  2
  ● Cora asked to use your Gmail draft tool          Review
  ● Launch Council · 3 new lines since you looked    Open

  Pick up where you left off
  [M] In one sentence: what specialists and teams…   3 hr
  [M] Northwind pricing review                       yesterday
  [L] Enclosure cost check                           Tuesday
                                                     All chats →
```

- **Greeting, not a slogan.** Their name and the time of day. The abstract
  question goes.
- **Needs you** is the only new idea on this page and the most valuable: the
  things that will not move without this person — an agent waiting on approval,
  a room that has spoken since they last read it, a failed run. Empty most
  days, and when empty it is not rendered at all.
- **Pick up where you left off** is Recent, promoted, with the agent's monogram
  and the last line rather than "Updated 3 hr".
- The three starters survive as quiet chips under the composer, shown only
  while the person has fewer than five conversations, then never again.

## Deleted

- The `WORK MODES` section and all three mode cards
- "One product, clear boundaries" / "Private by default. Shared only when you
  choose a shared surface."
- The "Start here" section as a permanent fixture
- `--workspace-warm|rose|sage` and the `data-tone` attributes, replaced by
  nothing — the chips do not need colour

## Words

| Now | Becomes |
| --- | --- |
| What do you want to move forward? | Good morning, Shubham |
| Ask, plan, research, or build… | Ask your agent, or start something… |
| Recent | Pick up where you left off |
| Updated 3 hr | 3 hr |

## States

- **First run:** greeting, composer, three chips, and one line — "Your agent is
  Maya. Anything you say here is private to you." Nothing else.
- **Loading:** the composer is live immediately; the lists below fade in when
  they arrive. The composer never waits for data it does not need.
- **Empty:** no **Needs you** section at all; **Pick up** shows one line about
  where a conversation comes from.

## Needs from the backend

**Needs you** is a small aggregate that does not exist yet: pending agent
requests for this member, rooms with unseen lines, runs that failed in the last
day. One endpoint, one call, and it is also what the sidebar counts.
