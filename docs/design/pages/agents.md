# Agents (`/app/agents`) · Phase 4

**Job:** see who works for you, give one of them a job, and check what they did.

## Today

Titled "Agent system", described as "Create specialists, compose teams, run
durable workflows, and supervise agent-to-agent requests" — four pieces of
jargon in one sentence. Five tabs: Overview, Specialists, Teams, Workflows,
Requests. The Overview tab holds a card for the primary agent, four counters
(`1 Specialists · 1 Teams · 1 Workflows · 0 Open requests`), an **Operating
model** diagram explaining Maya → Specialists → Requests, and "Recent requests:
No agent requests yet."

A person with one specialist and no requests is shown five tabs, four counters
and an architecture diagram. The page is built for the shape of the system, not
for the size of the person's world.

## Becomes

One page that grows with what exists. No tabs until there is enough to need
them.

```
  Agents                                        [ + New specialist ]

  [M] Maya                                          Edit identity
      Your agent. It plans, delegates and reports back to you.

  Specialists
  ┌──────────────────────────────────────────────────────────┐
  │ [L] Ledger        keeps the pricing sheet and invoices   │
  │     Shared with Launch Council · used 4 times this week  │
  ├──────────────────────────────────────────────────────────┤
  │ [F] Finance desk  team · Ledger + 1                      │
  └──────────────────────────────────────────────────────────┘

  Running                                    ← only when something runs
  ◐ Weekly pricing check    step 2 of 4 · started 10 min ago

  Waiting on you                             ← only when something waits
  ● Cora wants to use your Gmail draft tool       Allow   Decline
```

- **Your agent first**, as itself, with a monogram at 40px — not a card
  captioned `YOUR PRIMARY AGENT`.
- **Specialists and teams in one list.** A team is a specialist made of
  specialists; splitting them across two tabs makes a person choose a filing
  system before they can look at anything.
- **Running** and **Waiting on you** exist only when they have contents. An
  empty section is not rendered.
- **Workflows** stay, under their own heading, described by what they do and
  when they last ran — not by "durable".
- Tabs come back only above a threshold (say ten specialists), and then they
  are a filter on one list rather than five pages.

## Deleted

- The tab bar (until it is earned)
- Every counter tile
- The **Operating model** diagram
- "Create specialists, compose teams, run durable workflows, and supervise
  agent-to-agent requests."
- "Your primary Oryxa agent."

## Words

| Now | Becomes |
| --- | --- |
| Agent system | Agents |
| Your primary agent | Your agent |
| Bounded specialist | Specialist |
| Durable workflow | Workflow |
| Agent-to-agent request | One agent asking another |
| Open requests | Waiting on you |
| Execution / run | *(keep)* |

## States

- **First run:** just Maya, and one line — "Maya works for you alone. Add a
  specialist when there is a job you want done the same way every time." —
  above a single button.
- **A run in flight:** the row shows the step it is on, the elapsed time, and a
  way to stop it. No progress bars for work whose length is unknown.
- **A failed run:** stays in the list for a day with what failed and a retry,
  rather than disappearing into a log.

## Open question

Identity editing currently lives behind "Edit identity" on the agent card. An
agent's name, instructions, and what it may reach is the most consequential
form in the product. It deserves a route (`/app/agents/$id`), not a dialog.
