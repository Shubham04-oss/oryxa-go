# The Slack Marketplace

## Why here

The listing is a channel: teams search Slack's directory for "questions",
"knowledge", "ask", and a listed app is one they trust to install. It is
also the only channel Slack allows for commercial distribution
([slack-consent-loop.md](../../design/slack-consent-loop.md), §7), and it
is why the design-partner program has the number ten in it.

## The gate

| | |
|---|---|
| **Active workspaces** | At least **10**, held for the whole review. Slack's changelog of 2026-09-01 says the threshold went back to 10 as of July 2026, after a lower temporary one ([docs.slack.dev](https://docs.slack.dev/changelog/2026/09/01/slack-marketplace-install-requirement)). Other pages still say 5; trust the changelog, and check it the week we submit |
| **"Active"** | Used in the last 28 days, per third-party accounts; **verify** against Slack's own page before submitting |
| **Review** | Preliminary feedback within 10 business days; the functional review up to 10 weeks for a new app |
| **Needs** | A support page, a privacy policy, scope justifications, a reviewer workspace, screenshots, a demo, data-handling answers |

## The thing

**The submission in week 9, and a listing that keeps installing after.**

## The kit

Assembled in week 7 ([calendar.md](../calendar.md)), from what exists.

| Item | Source | State |
|---|---|---|
| Name, short description, long description | [manifest.yaml](../../../apps/internal/slackbot/manifest.yaml) `display_information` | Written |
| Scope justifications | The bot scopes in the manifest, each with one sentence of why (`app_mentions:read`: to hear the ask; `im:write`: the owner's notice; `users:read.email`: to match the owner; and so on). No channel-history scope, and say so | To write |
| Privacy policy | `oryxa.in/legal/privacy`; must describe the Slack bot and Google data (STATUS.md's launch blocker) | To check |
| Support page | `oryxa.in/support`: the docs, the email, response time ("same business day") | To build ([website.md](website.md)) |
| Data handling | Inbound text purged after 7 days; sources, context and secrets never sent to Slack; link unfurling off; the one-time secret view | Written in §7 of the design |
| Reviewer workspace | A Slack workspace of ours with two test accounts, both with background access, one connected app, the install done, three asks already run | To build (macbookpro) |
| Screenshots (3) | The ask in a channel; the owner's DM with the three buttons; the answer in the thread | To record, from the demo workspace |
| Demo video | The sixty-second cut | Week 2 |
| Pricing | "Free during beta" | Decide the wording |
| Install landing | See the decision below | To decide |

**A decision before submitting.** Today an admin installs from Oryxa →
Settings → Slack → Add to Slack, never from Slack's side (the runbook). A
listing's "Add to Slack" starts in Slack, from someone who may have no Oryxa
workspace yet. The listing's install URL must land on a page that says what
to do: sign in or create a workspace, then Add to Slack from Settings; or
the install flow learns to create the workspace. Decide in week 7; the
first is a page, the second is a lane.

## The routine

| When | What | Who |
|---|---|---|
| Weekly, Friday | The count: workspaces with the app installed, and those with an approved ask in the last 28 days, from `apps_slack_installs` and the interactions ledger, read-only. In the ship log | Agents |
| Week 7 | The kit assembled; the install-landing decision | Agents, macbookpro, founder |
| Week 8 | Checkpoint: ten active. If not, the submission waits; outreach doesn't | Founder |
| Week 9 | Submit. Answer review feedback within 24 hours | Founder; technical answers from macbookpro |
| During review | Keep ten active: the nudge to any partner that goes quiet ([design-partners.md](../design-partners.md#outreach-templates)) | Founder |
| After listing | Installs from the listing counted separately (the install URL carries `source=marketplace`); screenshots refreshed after any UI change; the listing's description checked against [positioning.md](../positioning.md) quarterly | Agents, founder |

## Rules

- Nothing in the listing that the dry-run hasn't passed.
- The manifest stays the only place scopes change; the listing's
  justifications are written from it, not the other way round.
- No test-account credentials in the submission notes beyond what Slack's
  form asks; rotate them after the review.
- The count query reads counts by organization, never content.

## The first ten moves

1. Read Slack's current Marketplace requirements page, this week; note the
   date.
2. The support page.
3. The privacy page checked for the bot and Google data.
4. The scope justifications, one sentence each.
5. The reviewer workspace, with three asks already run.
6. The three screenshots and the demo.
7. The install-landing decision.
8. The weekly count in the ship log, from week 3.
9. The checkpoint, week 8.
10. The submission, week 9.

## What we count

Active workspaces (the gate, weekly). Review status and the days to each
reply. After listing: installs from the listing per month, and how many run
a first ask.

## Founder or agent

Agents count and assemble. macbookpro builds the reviewer workspace and
answers the technical questions. The founder submits and talks to Slack.
