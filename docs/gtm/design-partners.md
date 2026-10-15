# Design partners: the first ten teams

*2026-10-15. The program that gets Oryxa from "switched on in production" to
ten workspaces that ask every week. It is also the road to the Slack
Marketplace, which needs ten active workspaces held through the review.*

## Goals

| By | Goal |
|---|---|
| Week 2 (Nov 1) | The outsider dry-run passes twice; the kit below exists; a list of 100 names |
| Week 5 (Nov 22) | 15 conversations held, 6 workspaces installed, 3 with a first approved ask |
| Week 8 (Dec 13) | **10 workspaces with an approved ask in the last 7 days**, 5 of them with a teamspace used that week; 3 deep partners on a fortnightly call; the Marketplace kit ready |
| Week 16 (Feb 7) | 25 weekly active workspaces; 3 paying at the founding rate; 5 quotable partners |

The funnel that gets there: 100 named → 40 conversations → 15 installs → 10
active. Those ratios are a guess; after the first twenty conversations,
replace them with the real ones.

## Who qualifies

**The company.**
- 15–150 people. Below ten, there is no boundary to cross; above 150,
  procurement arrives before the first ask.
- Slack is where work happens. The loop lives in Slack; no Slack, no partner.
- Google Workspace, or GitHub plus Notion. These are the apps a member
  connects on day one, and the ones the native connectors cover first.
- At least two functions that depend on each other and ask each other things:
  engineering and support, product and engineering, sales and engineering,
  operations and everyone. A `#ask-eng`, `#help-*` or `#questions` channel is
  the tell.
- Remote or hybrid. Co-located teams walk over.

**The champion.** One person who feels the chasing and can get the Slack app
installed: a head of operations, a chief of staff, an engineering manager, a
head of customer success, or the founder of a 20–50 person company. They are
a Slack admin, or one message away from one.

**Not now.**
- Regulated or enterprise: a security questionnaire before the first ask, a
  DPA, SOC 2. Say "not yet, here is what we have", and keep the name for
  phase 3.
- Microsoft Teams as the main channel. The surface isn't built.
- Companies that won't accept Google's "unverified app" screen for the beta.
  It is honest to warn them up front: the Drive and Gmail scopes are
  restricted and Google's review takes 6–10 weeks ([connector-strategy.md](../design/connector-strategy.md), section 5).
- Anyone who wants to "try the AI" alone. One person can't run an ask.

**Where the hundred names come from.**

| Source | How many | How |
|---|---|---|
| The founder's own network (India and abroad) | 30 | A list, written down, this week. Ex-colleagues, founder friends, people who have complained about Slack black holes |
| LinkedIn search | 30 | Titles above, companies 11–200 on LinkedIn, Slack visible in their job posts or stack pages. Two lists: India, then US/EU remote-first |
| Communities | 15 | People who answer questions in ops, chief-of-staff and engineering-management communities ([platforms/slack-communities.md](platforms/slack-communities.md)) |
| Inbound from building in public | 15 | Everyone who replies, DMs or signs up gets a conversation within 24 hours |
| Ecosystem introductions | 10 | Composio, Cloudflare and Google ADK people know teams building with agents ([platforms/ecosystems.md](platforms/ecosystems.md)) |

## The offer

What they get, said in this order on the partner page and on the call:

1. **Free through the beta**, and a founding rate locked for a year after
   the Marketplace lists us. (The owner names the rate.)
2. **The founder on a call every two weeks**, and a Slack Connect channel
   between their workspace and ours, answered the same day.
3. **A say in what gets built next.** Deep partners see the board and vote
   on the next lane.
4. **Named at launch**, if they want to be. Logo, quote, or nothing.
5. **Straight answers on security**, in writing, from the design documents.

What we ask:

1. Install the Oryxa Slack app in the real workspace, not a sandbox.
2. At least three members with background access turned on, from two teams,
   together in one teamspace in Oryxa.
3. One real ask a week for eight weeks, and the teamspace used for one
   piece of real work between the two teams.
4. Thirty minutes every two weeks, and honesty when something is bad.
5. Permission to count: anonymous numbers (asks, approvals, time to
   approve), never the content of an ask.

The "deep" partner adds a weekly call for the first month and a seat in the
reviewer workspace for Slack's review ([platforms/slack-marketplace.md](platforms/slack-marketplace.md)).

## The pipeline

Stages, and what moves a name forward. Keep it in a **private** GitHub
Project (the public board is for code) or a sheet; one row per company, one
owner (the founder), and the agents keep it current from the call notes.

| Stage | Moves on when |
|---|---|
| Named | The company and the champion are written down with why they fit |
| Contacted | First message sent (template below). Three touches over ten days, then park |
| Call | A 20-minute discovery call happened |
| Installed | Sign-up, workspace, Slack app installed, two teammates invited |
| First ask | One approved ask with an answer in a thread. **Most partners stall here**; the install session (below) exists to prevent it |
| Active | An approved ask in each of the last two weeks |
| Reference | Agreed to a quote, a logo, or a call with a prospect |
| Parked | With a reason and a date to look again |

Columns to keep: company, size, apps (Slack, Google, GitHub, Notion), champion,
stage, last touch, next step, deep (yes/no), feedback issues (labels).

## The onboarding path (twenty minutes)

This is the "golden path". It is the outsider dry-run, run with the partner
on a screen share, and it must be run by the owner alone first, twice, with
two fresh accounts (board item). Every step names its known trap.

| Step | What | Trap |
|---|---|---|
| 1 | Sign up at app.oryxa.in with a work email; verify (link expires in 24 h) | Email lands in spam: check SES is sending from the domain, not Cognito's default |
| 2 | Create the workspace. The setup steps are Krude, Tools, Crew, Teamspace (`onboarding.tsx`) | Name the agent here; "Krude" is only the default |
| 3 | Settings → Account → **Background agent access** → Turn on | **Both the asker and the owner need this**, or the screen refuses the ask with `no_grant`. Say so before the first ask |
| 4 | Connect Google Drive and Gmail (and GitHub, Notion) from Apps | Google shows "unverified app"; the member clicks through "Advanced". Warn them, and that the cap is 100 users |
| 5 | Invite two teammates from two teams; they repeat steps 1, 3 and 4 | The unpaid member limit is 3 in the example catalog; **verify** production's limit before inviting a fourth |
| 6 | Settings → Slack → **Add to Slack**. Never Slack's own install button | The installer must be a Slack admin, or installation needs approval in Slack |
| 7 | Each member links their Slack account (Sign in with Slack) | Without the link, the router can't match the Slack user to the member |
| 8 | In a channel: `@Oryxa ask @raya <a real question>` | Not a group DM, not a Slack Connect channel: both are refused. The screen refuses chatter and implausible asks; use a real question |
| 9 | Raya's DM: Share answer / Allow for 1 hour / Deny. She clicks | 2–4 seconds for the other surface to update; don't double-click |
| 10 | The answer lands in the thread, attributed to Raya's agent, approved by Raya | If the answer is held ("not grounded"), that is the guard working; show the owner's review in Oryxa |

Then, in the same session: one ask that needs a secret (the one-time view),
one "Deny" so they see what the asker gets, and the teamspace both teams
are in: the ask on the Requests page, the team's summary, and a side
conversation where Sam asks Raya's agent directly. Twenty minutes, three
asks, one room, and they have seen the team. The Slack thread was the door;
the teamspace is where they will work.

A partner who finishes step 10 on the call almost never stalls. One who is
left to "try it later" usually does.

## The calls

**Discovery (20 minutes).** Not a demo; a listen.
1. "Walk me through the last time you waited on someone for an answer. Who,
   what, how long, what did you do meanwhile?"
2. "Where do those questions get asked today?" (Channel names. Note them.)
3. "What can't people see, and who decides?" (The boundary. If there is none,
   they are not a partner.)
4. "What have you tried?" (Search tools, a wiki, an FAQ bot. Why it failed.)
5. Show the sixty-second demo. Ask: "Who on your team would be Raya?"
6. Close: "Can we do the install together, Thursday, thirty minutes, with you
   and that person?" Book it before hanging up.

**Install (30 minutes).** The onboarding path above, on a screen share, to
the first approved answer. End with: "What's the first real question you'll
ask next week, and what's the first piece of work you'll bring into the
teamspace?"

**Fortnightly check-in (30 minutes).**
1. Numbers first, from the dashboard the agents prepare: asks, approvals,
   time to approve, anything held.
2. "Which ask was useless? Which was the best? Did anyone open the
   teamspace without being asked to?"
3. "What did you expect that didn't happen?"
4. One thing we shipped since last time, shown, not described.
5. One question about what's next (the board), and a vote.
6. Every point becomes a GitHub issue labelled `partner` and the partner's
   slug, that day. The partner gets the links.

**Month-two conversation.** "Is this worth money to you? What number would
you defend to your CFO?" Write the answer down verbatim; it sets the
founding rate.

## Outreach templates

Short, specific, no attachments, one ask. Replace the brackets. The founder
sends them; an agent may draft the research line.

**Warm introduction, to a mutual contact**
> Subject: Intro to [Name] at [Company]?
>
> [Mutual], I'm looking for ten teams to try Oryxa before we launch it. It's
> a team of people and their agents: everyone gets one, the team gets a room
> with them in it, and when the answer is with someone else it asks that
> person in Slack, who approves with one click. [Name] came to mind
> because [one specific reason: their #ask-eng channel, a post, your last
> conversation]. Would you introduce us, or may I mention you?

**Cold, LinkedIn, first touch** (under 300 characters; a connection note)
> [Name], you run [function] at [Company] on Slack, so you'll know the
> "who can tell me X" wait. I'm building the fix: ask in Slack, the person
> who knows approves with one click, the answer lands in your thread.
> Looking for ten teams to try it this month. Sixty seconds: [link].

**Cold, LinkedIn, second touch** (four days later)
> Short version: [a one-line finding from their public stack or a post of
> theirs]. If the chasing is real at [Company], I'd like thirty minutes with
> you and one engineer to run a real ask. If not, tell me and I'll stop.

**Cold, email, first touch**
> Subject: the "who knows about X" wait at [Company]
>
> [Name],
>
> When someone at [Company] needs an answer only [team] has, what happens?
> From the outside it looks like [the tell: a #help channel, a hiring post
> mentioning cross-team work].
>
> Oryxa gives everyone on the team an agent that is theirs, and gives the
> team a room where people and agents work together. Slack stays: ask there,
> Oryxa asks the person who knows, they approve with one click, and their
> answer comes back to your thread. Nothing is shared without their say.
>
> It's switched on, it's free through the beta, and I'm picking ten teams to
> shape it. Here is the sixty-second version: [link]. Would you give me
> twenty minutes this week?
>
> [Founder], Oryxa
> [one line: the public repo]

**Cold, email, second touch** (five days later; reply to the first)
> One thing I didn't say: the owner's agent answers from what they approved
> and nothing more, and a guard holds anything the source doesn't back. The
> design is public: [link to slack-consent-loop.md]. Twenty minutes?

**Cold, email, third touch** (ten days; the last)
> I'll stop here. If the chasing ever becomes the problem, the install takes
> twenty minutes: [sign-up link]. Thanks for reading.

**X / Bluesky DM** (to someone who posted about the problem)
> Saw your post about [the thing]. I'm building exactly that loop: ask in
> Slack, the person who knows approves with one click, answer lands in the
> thread. Would you try it with one teammate? [link]

**Community post** (where self-promotion is allowed, once)
> I'm looking for ten teams on Slack to try something before it launches.
> The problem: the answer you need is with someone whose docs you can't
> open, so you ping and wait. Oryxa is a team of people and their agents:
> everyone gets one, the team gets a room with its agents in it, and Slack
> stays. The first thing you'll feel: you ask in Slack, it asks the person
> who knows, they approve with one click, and their answer lands in your
> thread. Free through the beta, fortnightly call
> with me, your say on what's next. Teams of 15–150 on Google Workspace or
> GitHub+Notion. Reply or DM if that's you.

**After the discovery call** (same day)
> Thanks for today. What I heard: [two lines, their words]. Next: install
> on [day] with you and [name], thirty minutes, to the first approved
> answer. Before then, two things: both of you turn on background access
> (Settings → Account), and [name] confirms they're a Slack admin. Here's
> what we'll do: [link to the partner page].

**The nudge** (a partner who installed but hasn't asked in ten days)
> No ask from [Company] since [date]. Usually that means the first real
> question didn't come up, or it felt odd to ask the bot. Which is it? If
> it's the second, try this one: `@Oryxa ask @[name] [a question they
> mentioned on the call]`.

**The fortnightly ship log to partners** (every second Friday, from the
release notes the agents draft; [platforms/email.md](platforms/email.md))
> Three things shipped, one thing you asked for that is in, one question for
> you, one number (asks across all partners this fortnight).

## The partner page

`oryxa.in/partners`, one screen. The headline, the offer and the ask above,
the sixty-second demo and the two-minute team cut, the onboarding path as
five lines, "what's next" (parleys in production, remembered consent,
two-of-three approvals, Microsoft Teams, Google verification), and one
button: "Talk to the founder" (a calendar link).
No form longer than name, work email, company, Slack admin yes/no.

## What we measure

| Number | Where it comes from |
|---|---|
| Workspaces with Slack installed | `apps_slack_installs` (count, read-only) |
| **Weekly active teams** (two people and their agents worked together in the last 7 days) | an approved ask, or a teamspace with messages from two members and an agent's reply, by organization, by week: the interactions ledger plus the teamspaces' room messages |
| Slack-active workspaces (≥1 approved ask in 7 days; the Marketplace gate) | the interactions ledger: decisions with `consent`, by organization, by week |
| Time from sign-up to first ask | auth's organization created-at to the first ask row |
| Asks per workspace per week; approval, deny and lease rates | `interaction_asks`, decisions by button |
| Median time to approve | request created-at to decided-at |
| Held-answer rate | answers held by the guard over answers delivered |
| Grants per workspace | auth agent grants, active |
| Week-4 retention | workspaces active in week 4 over workspaces that had a first ask |

An agent builds the read-only query once (the way
[connector-strategy.md](../design/connector-strategy.md) section 1 asks for
counts: by organization, never content) and runs it every Friday into the
ship log.

## Feedback into the build

- Every observation on a call becomes an issue the same day, labelled
  `partner` and `partner:<slug>`, with the partner's words quoted and the
  surface named.
- Friday: the agents post a digest to the board's weekly discussion: what
  partners said, grouped by surface, with counts.
- A fix a partner asked for is shown to that partner, on the next call, before
  it is posted anywhere.
- A pattern three partners share becomes a lane in
  [LANES.md](../LANES.md), with the issues linked.

## Risks

| Risk | What we do |
|---|---|
| The dry-run fails for strangers | Phase 0 doesn't end until it passes twice. No outreach before |
| Partners install but don't ask | The install session goes to the first approved answer, always. The nudge at ten days. A real question from the discovery call, written down, is the first ask |
| Google's unverified screen scares a champion | Say it before they see it. Offer GitHub and Notion first; Drive and Gmail when they are ready |
| Ten workspaces by week 8 is missed | The Slack submission waits; the Show HN doesn't. Developers bring teams too |
| A partner's security team asks for SOC 2 | Answer in writing from the design docs; park them for phase 3 with a date |
| The founder's time | The routines are capped ([calendar.md](calendar.md)). Calls are IST mornings and evenings, Tuesday to Thursday, nothing else |
