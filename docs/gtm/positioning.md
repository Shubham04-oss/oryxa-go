# Positioning: what we say, in what order, in what words

*2026-10-15. The words here follow [direction.md](../design/direction.md)
and [philosophy.md](../design/philosophy.md): plain words, no internal
vocabulary, the work before the explanation. Everything marked **live** was
read from production's switches and STATUS.md; **verify** means the outsider
dry-run has to confirm it before it goes in a post.*

## The story, in order

Tell it in this order, every time, on every platform. Shorter versions stop
earlier; they never reorder.

1. **The problem.** Work is a team sport and AI is single-player. Each person
   has a private chat with a model, and nothing crosses to a teammate. The
   answer you need is in someone's head, or in a document you can't open, so
   you ping, you wait, you chase. Search tools stop at "access denied".
2. **The team.** Oryxa is a team of people and their agents. Every person
   gets an agent that is theirs: their identity, their apps, their
   permissions. The team gets rooms where people and agents work together:
   teamspaces with the team's own agent, DMs, side conversations where you
   ask a teammate's agent, and parleys where the agent is in the call. Slack
   stays; the ask starts there.
3. **The first moment: the ask.** Ask a question, in Slack or in a room.
   Oryxa asks the person who knows, in a direct message, with three choices:
   share the answer, allow for an hour, or deny. If they approve, their agent
   answers in your thread, from what they approved and nothing more. Two
   people, two agents, one click.
4. **Your own agent.** It does your work: reads your apps, drafts, files,
   follows up, and brings you only what needs you. Anything that changes
   something asks you first, unless you gave it standing permission, which
   you can see and revoke.
5. **Why it can be trusted.** A screen on the way in stops chatter, injection
   and implausible asks before anyone is pinged. A guard on the way out strips
   personal data and credentials and holds an answer the source doesn't back.
   Every action is recorded. The code is public.

## One-liners, by length

| Length | Use |
|---|---|
| **Five words** | "People and their agents, one team." The proof line under it: "Ask the person who knows." |
| **Headline** (three to pick from) | "A team of people and their agents." · "Your team, and everyone's agent." · "People and agents, one team." The owner picks one; the first is this plan's default |
| **Bio** (under 160 chars) | "A team of people and their agents. Everyone gets an agent that is theirs; agents ask each other for their owners, with one click of consent. Slack stays." |
| **One sentence** | "Oryxa gives every person on a team an agent that is theirs, gives the team rooms where people and agents work together, and lets agents ask each other on their owners' behalf, with one click of consent." |
| **The paragraph** | Steps 1–3 above, one sentence each |
| **The category** | "Human-agent teams". "AI coordination agent for teams", which the site already ranks for, stays in the title tag only |

Never say "Switchboard" in public until the owner settles the name
(north-star.md, open decisions). Never say "Krude" as the product: it is the
default name of a person's agent, which they rename.

**Not a replacement.** Slack, Meet and Teams stay. Oryxa's teamspaces, DMs
and parleys exist because a team that has agents in it needs rooms built
for that, not because the team should leave its chat. The ask starts in
Slack because that is where teams are; the room is where the team and its
agents work. Say this early in every conversation, because "a new
workspace" is the first thing a buyer fears.

## Words

Public words, from the product's own screens and the design direction.

| Say | Not |
|---|---|
| a team of people and their agents; your team and its agents | human-agent team (the category, never on screen), multiplayer, multi-agent system |
| your agent, Ada's agent | Krude (except as "the default name"), whisperer, primary agent, operator |
| an ask; the approve card; Share answer, Allow for 1 hour, Deny | a request flow, a consent interaction, an approval workflow |
| teamspace (today's UI), room (once the rehaul lands) | shared surface, multi-agent room |
| the team's agent | organization agent, org agent |
| a specialist | contributed specialist, bounded specialist, sub-agent |
| what your agent may do; background access | agent grant, standing consent, member grant |
| parley (a call your agent can join) | RealtimeKit meeting, voice DO |
| Library | knowledge base, corpus, RAG |
| apps you connect | toolkits, integrations, connectors, Composio |
| recorded, audited | the ledger, the interactions table |

Rules: no "🚀", no "revolutionary", no "10x", no "AI-powered" (everything is),
no numbers we haven't measured, no screenshots with example data (the design
docs forbid example data on screen; the same holds for posts).

## What we can show today

The sixty seconds is the ask, because it is the team moment we can show
fastest. The two-minute cut is the team itself: a teamspace, two people,
their agents, a side conversation, and the ask arriving. Parleys join the
videos when they run in production.

| Moment | State | Show it as |
|---|---|---|
| `@Oryxa ask @raya what the staging DB host is` → Raya's DM with three buttons → "Approved by Raya at 14:02" → the answer in the thread | **Live** (`slack_app_ready: true`), **verify** with two outsider accounts | The sixty-second demo, every platform |
| A secret shared as a one-time view in Oryxa, with the thread saying only "Raya shared it with @asker in Oryxa" | **Live**, verify | The second clip: "secrets never go to Slack" |
| Your agent reading your connected apps (Drive, Gmail, GitHub, Notion, Slack) and reporting "Updates from your apps" | **Live** through Composio | Screen recording; mind the unverified-Google screen in the recording |
| Background access: Turn on, the status line "Your agents can act for you while you're away", Revoke | **Live** | A still, for trust posts |
| A specialist's Access tab: which apps, "May change things" off by default | **Live** (I3/I9 merged) | A still, for trust posts |
| A teamspace with two people, their agents, and the team's summary: decisions, risks, follow-ups | **Live** (I7) | The two-minute team cut, every platform |
| A side conversation: asking a teammate's agent from a teamspace, and the teammate seeing it did | **Live** (side conversations, `askTeammateAgent`) | The two-minute team cut |
| Set up a workspace from `oryxa.yaml` with the CLI, run by your coding agent | CLI client **on** in production; the public installer is **not** (`get.oryxa.in` does not resolve) | Developer story, once the installer is public. Until then, show it from a release binary, labelled "beta" |
| A parley: a call with your team where the agent listens and speaks | **Staging** (calls end to end; UI PRs open) | Video only, labelled "coming", once it runs in production |
| An agent running code in a sandbox and browsing, watched live in the room | **Staging** (Cloudflare baseline A) | Same |
| Remembered consent, two-of-three approvals, takeover after 15 minutes, Microsoft Teams | Not built | Roadmap, on the partner page only |

## How we are different

Short, honest, by alternative. For objections in full, see
[content-bank.md](content-bank.md#objections-and-answers).

| They | We |
|---|---|
| **Enterprise search** (Glean, Confluence search, Microsoft Copilot): index everything, then stop at the permission wall | We don't index what you can't see. We ask the person who can, and they decide. Where a company already has search, Oryxa sits on top and adds consent and actions |
| **A single-player assistant** (ChatGPT, Claude, Gemini for Workspace): one person, one model, one chat | Oryxa's unit is the team. Each person's agent is theirs, and agents work with each other for their owners, with consent on record |
| **Slack AI, and the note-takers in Meet and Teams**: an AI beside the room you're already in | They sit beside the team. Oryxa's agents are on the team: in the room, in the call, asking and answering for their owners. And Slack stays |
| **Automation agents** (Zapier, Lindy, Relay): triggers and actions for one person | We have those too (your agent wakes on app events), but nobody else brokers consent between two people's agents |
| **Agent platforms** (Dust and the like): org-wide agents on a shared corpus | We give each person their own, with their own identity and permissions, and the team a room they share, never a service account acting as everyone |

## Two audiences, one story

| Audience | Where | What they hear first | What they do |
|---|---|---|---|
| **The team buyer**: head of ops, chief of staff, engineering manager, head of CS, founder of a 20–50 person company | LinkedIn, Slack communities, email, calls | The problem and the team (steps 1–2), the ask as the proof (step 3), then trust (step 5) | Installs the Slack app, invites two teammates into a teamspace, becomes a design partner |
| **The developer**: Go and agent-infrastructure people, teams that set tools up with their coding agents | GitHub, X, Hacker News, Reddit, the Go community | How it is built (agent identity and isolation, the consent loop, the guard, the outbox, one binary), then the team and the ask | Stars, reads, writes about it, brings their own team |

## Proof points, from the code

Say these as facts, with the file when a developer asks.

- **First click wins.** A consent decision takes a row lock, so an approve
  card can't be approved twice or replayed, and every copy of the card turns
  into "Approved by Raya at 14:02" ([slack-consent-loop.md](../design/slack-consent-loop.md)).
- **"Allow for 1 hour" is a lease**, with its window in the request itself,
  so it stops working when it expires with no clean-up job.
- **A run nobody is watching never decides a request.** Unattended wake-ups
  can't approve asks, which closes the prompt-injection path (board items,
  PR #194).
- **Secrets never go to Slack.** A secret is a one-time, expiring view in
  Oryxa's DMs; the thread only says who shared it with whom.
- **The guard is not a prompt.** Personal data and credentials are removed
  deterministically (Bedrock Guardrails), an answer that isn't grounded in
  the approved source is held for the owner, and "Share answer" allows no
  run of twenty or more words copied from the source.
- **Your agent is you, not a service account.** It runs with your own
  identity; when you are away it gets a short-lived token under a grant you
  gave and can revoke ([auth/README.md](../../auth/README.md), agent grants).
- **Writes ask first and carry an idempotency key**, so a retry is never a
  second change ([apps/README.md](../../apps/README.md)).
- **Deploys don't kill agent runs**: work in flight drains, what is left is
  recorded as interrupted and started again by the next process (PR #196).
- **One static binary, twelve services**, each owning its data and its
  queue; the outbox pattern to EventBridge ([README.md](../../README.md)).
- **An isolation test per known leak** between personal, specialist, team
  and organization agents (PR #256).
- **Inbound Slack text is purged after seven days.** Sources, context and
  secrets never go to Slack at all.
- **The code is public**: [github.com/Shubham04-oss/oryxa-go](https://github.com/Shubham04-oss/oryxa-go),
  with the design documents and the status page in it.

## The gap with the landing page

[oryxa.in](https://oryxa.in) today: "Your workday, with the chasing taken
out. Oryxa follows up with teammates, drafts manager updates, and clears the
busywork around your real work." Buttons: **Get Oryxa**, **See what it
handles**, **Book a demo**. Logos: Notion, Linear, Google Drive, GitHub,
Jira, Asana. Pages: privacy, terms, security, contact. No changelog, docs,
blog or pricing page (all 404).

That is the fourth step of the story sold as the whole play, with a demo gate in
front of a product that has self-serve sign-up. It also shows Linear, Jira
and Asana, which reach members through Composio but don't appear in the
onboarding's app list (Slack, Google Drive, GitHub, Notion, Gmail). Proposed:

| Block | Now | Proposed |
|---|---|---|
| Eyebrow | YOUR ORYXA AGENT FOR THE WORK AROUND YOUR WORK | PEOPLE AND THEIR AGENTS, ONE TEAM |
| Headline | Your workday, with the chasing taken out. | A team of people and their agents. |
| Sub | Oryxa follows up with teammates, drafts manager updates, and clears the busywork around your real work. | Everyone gets an agent that is theirs. Your team gets rooms where people and agents work together, in Oryxa and alongside Slack. When the answer is with someone else, their agent is asked, and they approve with one click. |
| First button | Get Oryxa (→ **verify** where it goes) | Join the beta (→ sign-up) |
| Second button | Book a demo | Watch the sixty seconds (→ the demo) |
| Logo strip | Notion, Linear, Drive, GitHub, Jira, Asana | Slack, Google Drive, Gmail, GitHub, Notion: the apps a member connects on day one. "and 500 more through your connected apps" |
| New section two | | "Where the team works": a teamspace, a DM, a side conversation, a parley, each a real still, and the line "Slack stays" |
| The scrolling day | Inbox / Team / Manager / Focus | Keep, as section three, retitled "And your own agent, for the rest" |
| New section | | "How it stays yours": the four proof points a buyer cares about (asks first, approve card, guard, revoke) |
| New pages | | `/changelog` (from GitHub releases), `/docs` (the `docs/beta` pages exist as MDX), `/partners` (the design-partner page), `/security` filled in |
| Footer | hello@oryxa.in | Keep. Add the GitHub link |

The landing page lives in the private `oryxa-website` repository; this is
copy, not code.

## The public repo's first screen

The repository README opens with "Oryxa is an AWS-backed Go monorepo". For a
developer arriving from a post, add above it, in five lines: what Oryxa is
(the bio line), the sixty-second demo link, "Try it: app.oryxa.in", "How it is
built: docs/north-star.md, docs/STATUS.md", and the license. Set the repo
description, topics (`go`, `agents`, `slack-app`, `adk-go`, `cloudflare-workers`,
`eventbridge`, `ai-agents`, `team-collaboration`), and turn merged PRs into
releases ([platforms/github.md](platforms/github.md)).

## The Slack listing's words

Already written, in the manifest: name, short description and the long
description quoted above. The listing adds three screenshots (the ask in a
channel, the owner's DM with the buttons, the answer in the thread), a
support page and the privacy page. [platforms/slack-marketplace.md](platforms/slack-marketplace.md).
