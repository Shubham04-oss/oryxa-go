# Go-to-market: design partners first

*Plan, 2026-10-15, written from the code and the docs at `e33f8b7b`
(main plus the connector-strategy PR). It changes no code. What is "live"
below was read from `deploy/production/*.auto.tfvars.json`,
[STATUS.md](../STATUS.md) (2026-09-30) and the board; where a page says
**verify**, the outsider dry-run on the board settles it.*

## What this folder is

| Page | What it answers |
|---|---|
| this page | The strategy on one page: the bet, who, the phases, the numbers, what the owner decides |
| [positioning.md](positioning.md) | What we say, in what order, in what words. What we can show today. The gap with the landing page |
| [design-partners.md](design-partners.md) | The program: who qualifies, the offer, the pipeline, the 20-minute onboarding path, call scripts, outreach templates, metrics |
| [calendar.md](calendar.md) | The weekly rhythm across platforms, and the 16-week plan with dates |
| [content-bank.md](content-bank.md) | Demo scripts, forty post ideas drawn from the code, the technical series, objections and answers, pitches |
| [platforms/](platforms/README.md) | One page per platform, each with its own routine: GitHub, X, LinkedIn, Hacker News, Reddit, Slack communities, the Slack Marketplace, Product Hunt, YouTube, email, the website, ecosystems, India |

## The product, in one breath

**Oryxa is a team of people and their agents.** Every person gets an agent
that is theirs: their identity, their apps, their permissions. The team gets
the places where people and agents work together: teamspaces with the team's
own agent, DMs, side conversations, and parleys where the agent is in the
call. Agents talk to each other for their owners, and nothing crosses from
one person to another without that person's say. That is
[north-star.md](../north-star.md) in plain words, and it is the owner's
decision of 2026-10-15: the story is the team, not an assistant and not a
Slack bot.

The first moment of that team, and the one we can show in sixty seconds, is
the ask. The Slack app's own description
([manifest.yaml](../../apps/internal/slackbot/manifest.yaml)) says it:

> Ask a question in Slack. Oryxa asks the person who knows, and the answer
> comes back only if they approve it.

Slack, Meet and Teams stay. Oryxa's rooms exist because a team with agents
in it needs a place of its own, not because the team should leave its chat.

## Where we stand, and what the plan must respect

| | What |
|---|---|
| **Live in production** | Sign-up, workspaces, invitations. Your agent (chat, specialists, memory). Teamspaces, side conversations, DMs. Library (knowledge bases). Apps through Composio, and "Updates from your apps". Background agent access (the grant). **The Slack app is switched on** (`slack_app_ready: true`): the ask, the approve card with Share answer / Allow for 1 hour / Deny, one-time secret views, Slack account linking. The setup CLI's Cognito client is on. Cognito email goes through SES (the 50-a-day cap is lifted, per the board) |
| **On staging or in flight** | The live layer on Cloudflare: sandbox, browser, and parleys (calls an agent can join), end to end on staging, UI PRs #304–#306 and #321 open. The web-next rehaul. Native connectors for GitHub, Drive, Notion, Slack and Gmail ([connector-strategy.md](../design/connector-strategy.md), PR #278). Agent isolation |
| **Not built** | Remembered consent, N-of-M sign-off, takeover, Microsoft Teams as a surface, Cedar policies, the Slack Marketplace listing. `get.oryxa.in` does not resolve, so the installer is not public |
| **Never done** | The outsider dry-run of the whole loop: sign up → workspace → invite → connect → grant → Add to Slack → ask in a channel. It is a Todo on the board (macbookpro). Phase 0 starts with it |
| **Constraints** | Free beta (Dodo's key is absent on purpose). Google's unverified-app screen and 100-user cap until verification (6–10 weeks, plus CASA). The Slack Marketplace needs **10 active workspaces, held through the whole review** ([Slack changelog, 2026-09-01](https://docs.slack.dev/changelog/2026/09/01/slack-marketplace-install-requirement)). The public repo has no license, no topics, no releases and an engineering README |

## The strategy, in three sentences

1. **Win ten teams** that feel the "ask the person who knows" pain, through
   the founder's own network and LinkedIn, with a 20-minute path to a first
   approved answer, a teamspace with both teams in it, and a fortnightly
   call. Ten is Slack's number for the Marketplace and a fair number for
   finding out whether people and agents work as a team.
2. **Build in public from the repository that is already public**, so
   developers find us, the Slack and Google reviewers see a real company
   behind the app, and the founder has something true to post every day.
3. **Launch publicly only after** the loop works for strangers and the
   Marketplace submission is in: Show HN first, Product Hunt later.

## The five bets

| Bet | Why |
|---|---|
| **The story is the team; the proof is the ask.** Not "an assistant for your inbox" (today's landing page), not a Slack bot, and not a replacement for Slack or Meet | The team is what the product is and where the engineering is going. The ask is the first moment two people and their agents work as one: live, demoable in sixty seconds, and it spreads inside a company because it needs two people. Rooms and parleys are what a team walks into after. [positioning.md](positioning.md) |
| **Design partners before any launch** | A Show HN or a Product Hunt day with a loop nobody outside has run is a wasted day. Ten partners also unlock the Marketplace, which is itself a channel |
| **Founder-led, agent-assisted** | One person posts and takes calls. Agents draft from the repo (release notes, changelog, post drafts from merged PRs), keep the pipeline, and watch mentions. Every routine marks which is which |
| **Two audiences, one story** | Teams buy (LinkedIn, Slack communities, email, partner calls). Developers amplify (GitHub, X, Hacker News, the Go community). Same product, two doors |
| **India for calls, the world for presence** | IST calls, the founder's network, Dodo as merchant of record with GST handled. Presence is global from day one because every platform is |

## Who we want first

The full profile and the disqualifiers are in
[design-partners.md](design-partners.md#who-qualifies). In short: a company
of 15–150 people, Slack as the main channel, Google Workspace or GitHub and
Notion, at least two functions that depend on each other (engineering and
support, product and engineering, ops and everyone), and a champion who is a
Slack admin or one call away from one: a head of operations, chief of staff,
engineering manager, head of customer success, or the founder of a 20–50
person company.

Not now: regulated enterprises (security questionnaires before a first ask),
Microsoft Teams shops, companies under ten people (no boundary to cross).

## Phases

Dates start Monday 2026-10-19. [calendar.md](calendar.md) has the weeks.

| Phase | Weeks | Goal | Exit when |
|---|---|---|---|
| **0 · Ready** | 1–2 (Oct 19–Nov 1) | The loop works for a stranger; one story everywhere; the kit exists | The outsider dry-run passes twice (two accounts, two workspaces). Landing page says the ask. Sixty-second demo recorded. Partner page and kit done. Repo has a product README, a license decision, topics |
| **1 · Ten teams** | 3–8 (Nov 2–Dec 13) | 10 active workspaces, 3 deep partners | 10 workspaces with ≥1 approved ask in the last 7 days; 3 partners on a fortnightly call; the Marketplace kit assembled |
| **2 · Submit and show** | 9–12 (Dec 14–Jan 10) | Slack submission in; Show HN; Google verification filed | Submission accepted for review; Show HN posted the week of Jan 5; Drive/Gmail verification submitted (needs the native connector, section 5 of connector-strategy) |
| **3 · Open the door** | 13–16 (Jan 11–Feb 7) | Self-serve for anyone; first paid plan; Product Hunt | Pricing on (Dodo live), 3 paying workspaces, Product Hunt the first week of February |

## The numbers

The north star is **weekly active teams**: workspaces where, in the last
seven days, at least two people and their agents worked together, counted
as an approved ask, or a teamspace with messages from two members and an
agent's reply. It counts the team, not sign-ups. The Slack gate (ten
workspaces with an approved ask in the last seven days) is tracked beside
it, because the Marketplace counts only Slack.

| By | Reach (followers, all platforms) | Sign-ups | Workspaces with Slack installed | Weekly active teams | Paying |
|---|---|---|---|---|---|
| Week 8 | 1,500 | 150 | 15 | 10 | 0 (free beta) |
| Week 16 | 4,000 | 500 | 40 | 25 | 3 |

Per workspace, we also watch: time from sign-up to first ask, asks per week,
approval rate, median time to approve, held-answer rate, grants given, and
week-4 retention. [design-partners.md](design-partners.md#what-we-measure)
says where each number comes from.

## Who does what

| Who | Does |
|---|---|
| **The owner** | Every post under their name, every call, every DM. Decisions below. Google and Slack consoles, Dodo live mode, legal pages |
| **Agents** (any of the three, as a docs lane) | Release notes from merged PRs each Friday; the changelog page; first drafts of posts from [content-bank.md](content-bank.md); the pipeline sheet; a weekly digest of mentions; partner feedback into issues labelled `partner` |
| **macbookpro** | The outsider dry-run (already on the board); the product README and repo metadata; the Marketplace submission kit's technical answers |

Time budget for the owner: about 90 minutes a day on presence and outreach
plus partner calls, and Friday afternoon for recording. The routines in
[platforms/](platforms/README.md) add up to that and no more.

## Decisions for the owner

1. **Settled 2026-10-15: lead with the team, prove with the ask.** The
   landing page today sells follow-ups and manager updates with a "Book a
   demo" button. The headline becomes the team of people and their agents,
   the ask is the sixty seconds under it, and self-serve is the first
   button. Still open: the exact headline;
   [positioning.md](positioning.md#one-liners-by-length) offers three.
2. **The public repo's license.** It has none, so promoting it invites the
   question. Pick one (source-available such as BUSL or FSL, or plain
   "all rights reserved, source visible") before the first post that links it.
3. **Self-serve or demo-gated during the beta.** Recommended: self-serve
   sign-up stays open, "Book a demo" stays for teams over fifty, and the
   Slack install stays behind Oryxa's own settings page (never Slack's
   install button, per the runbook).
4. **The founding price to quote.** Partners will ask. A number to say out
   loud, even if billing stays off: this plan suggests "free until the
   Marketplace lists us, then a founding rate locked for a year" and leaves
   the rate to the owner.
5. **No Discord, no community server yet.** A Slack Connect channel per
   partner, and that is all, until there are twenty-five active workspaces.
6. **Handles.** One founder account and one company account on X and
   LinkedIn; the founder's is primary. YouTube and GitHub are the company's.

## What this plan leaves out

Pricing design beyond a founding rate, paid acquisition, SEO at scale, the
Microsoft Teams surface, and anything that depends on the native connectors
shipping on a date. Those get their own pages when they are real.
