# Content bank: what to show, what to say

*2026-10-15. Everything here is drawn from the repository, so every post has
a file behind it. Agents draft from this page; the founder posts. A post
idea marked **planned** describes something not built; it is posted as a
plan, never as a feature.*

## Demo scripts

Record on a clean workspace named for a fictional company, with two real
accounts of our own, no example data, the browser at 1280×800, Slack in
light mode, and a cursor that moves slowly. No voice-over on the sixty-second
cut; captions. Voice-over on the long one.

### The ask (sixty seconds)

| Time | Screen | Caption |
|---|---|---|
| 0–8 s | Slack, `#support`. Sam types: `@Oryxa ask @raya which env var turns the rate limit off on staging` | "The answer is with Raya. Sam can't open her runbook." |
| 8–14 s | The ephemeral reply: "Asked Raya · Open in Oryxa" | "Oryxa asks her. Nobody else is pinged." |
| 14–28 s | Raya's Slack DM: the card with the question and three buttons. She clicks **Share answer** | "Raya decides. Share the answer, allow for an hour, or deny." |
| 28–34 s | The card turns into "Approved by Raya at 14:02", in Slack and on the Oryxa card side by side | "First click wins. It can't be approved twice." |
| 34–50 s | The thread in `#support`: the answer, attributed "From Raya's agent · approved by Raya" | "Her agent answers from what she approved, and nothing more." |
| 50–60 s | Still: the Oryxa Requests page showing the ask recorded | "Recorded. Nothing shared without the owner's say. oryxa.in" |

### Secrets and deny (ninety seconds)

Same two people. Sam asks for the staging database password. Raya clicks
Allow for 1 hour. The thread says only "Raya shared it with @sam in Oryxa";
the value appears once, in Oryxa's DM, and expires. Then a second ask Raya
denies: Sam sees "Raya declined", ephemerally, and nothing else. Captions:
"Secrets never go to Slack." "Deny is a button, and the asker sees only
that."

### The team (two minutes, voice-over)

1. A teamspace, "Launch", with two teams in it: Sam and Raya, and their
   agents, named by them. At the top, what the team's agent made of the
   week: decisions, risks, open questions, follow-ups.
2. Sam opens a side conversation and asks Raya's agent directly. It answers
   from what Raya allows, and Raya sees that it did.
3. The ask from Slack arriving on the Requests page, with the approve card
   in Oryxa too: one decision, two surfaces.
4. Settings → Account → Background agent access → Turn on. "Your agents can
   act for you while you're away." Then the Revoke button.
5. A specialist's Access tab: "Only the apps you choose", "May change
   things" off. "Updates from your apps" after a push: the agent reports,
   changes nothing. (Skip Google on camera until verification, or show the
   unverified screen honestly and say why.)
6. The last line, over the teamspace: "Slack stays. This is where the team
   and its agents work."

### Coming, when they run in production

- **A parley:** the team on a call, the agent as a tile with no camera,
  marked Listening, then Speaking when asked. Live captions on screen.
- **Set up with your coding agent:** a terminal, `oryxa init`, the agent
  editing `oryxa.yaml`, `oryxa plan`, the admin's two clicks, `oryxa apply`.
  Beta label until `get.oryxa.in` is public.

## Forty-five post ideas, with their source

One idea is one post. The hook is the first line; the rest is one screen,
one sentence of why, and the file for developers.

**Trust (the buyer's feed)**

1. First click wins. The same approve card is in Slack and in the web app; only one click counts, and every copy turns into "Approved by Raya at 14:02". ([slack-consent-loop.md](../design/slack-consent-loop.md))
2. "Allow for 1 hour" is not a timer we clean up. The permit's window travels inside the request; when it expires there is nothing to revoke. ([north-star.md](../north-star.md))
3. A run nobody is watching cannot decide a request. If your agent wakes because an email arrived, it can report, not approve. That is the prompt-injection path, closed. (PR #194)
4. Secrets never go to Slack. The thread says "Raya shared it with @sam in Oryxa". The value is a one-time view that expires.
5. The guard is not a prompt. Personal data and credentials are removed deterministically; an answer the approved source doesn't back is held for its owner.
6. The button decides what the agent may say. Share answer: a summary, no twenty-word run copied from the source. Allow for 1 hour: excerpts, and follow-ups in that thread.
7. Your agent is you. It runs with your identity. Away, it gets a short-lived token under a grant you gave, and the grant has a Revoke button. ([auth/README.md](../../auth/README.md))
8. Writes ask first and carry an idempotency key. A retry is never a second change. ([apps/README.md](../../apps/README.md))
9. Inbound Slack text is purged after seven days. Sources, context and secrets never leave Oryxa.
10. Before anyone is pinged, four questions: is this work, is it an injection, is it plausible for this asker, is it a credential request. It fails closed.
11. An intern asking for the root database keys is turned away before anyone is disturbed.
12. If we matched Slack users by email, a workspace admin could pose as a member. So members link their account themselves. (board: Slack identity)

**How it is built (the developer's feed)**

13. One static binary, twelve services. Each owns its API, its data and its queue; the binary refuses to start if two services would answer the same route. ([README.md](../../README.md))
14. Every event goes through an outbox in the same transaction as the change, then to EventBridge. Nothing is announced that wasn't committed.
15. Slack gives you three seconds. Before we say 200: verify the signature, dedupe on the event id, write the inbound record and a content-free nudge in one transaction. Nothing else, not even a model call.
16. Deploys don't kill agent runs. Work in flight drains, what is left is recorded as interrupted, and the next process starts it again. (`ORYXA_DRAIN_TIMEOUT`, PR #196)
17. Migrations run once, under one advisory lock, recorded by checksum. Replicas starting together take turns.
18. A test per known leak between personal, specialist, team and organization agents. (PR #256, [live-layer/06](../design/live-layer/06-agent-isolation.md))
19. A toolbox carries its own skill. Load the sandbox tools and the manual for using them joins the prompt, so the agent reads it instead of guessing. ([agents/primary/README.md](../../agents/primary/README.md))
20. Native for five apps, a broker for the long tail. At 5,000 members the big five through a broker would be about $90,000 a year; native is about $200 of infrastructure and Google's yearly review. ([connector-strategy.md](../design/connector-strategy.md) §6)
21. Google's restricted scopes: verification plus a CASA Tier 2 assessment, 6–10 weeks, repeated yearly. What we do meanwhile. (§5)
22. Nobody exports OAuth tokens, so a connection can't be carried over. The migration is one click that keeps your knowledge sources and triggers. (§4)
23. Pricing is a JSON file. A plan the catalog lags behind keeps its paid status, so a customer is never cut off by our mistake. ([billing/README.md](../../billing/README.md))
24. A room is a Durable Object. A chat with your agent, a DM, a teamspace, a side conversation: all rooms. Media never goes through the room. ([live-layer/README.md](../design/live-layer/README.md))
25. "A kernel stuck in a cell is killed, not waited on." One commit, one sentence. (`aab54dc3`)
26. The web app shows no example data. Every block has its own loading, empty and error state. ([LANES.md](../LANES.md))
27. Slack's Marketplace wants ten active workspaces, held through a review of up to ten weeks. That is why we are asking ten teams first.
28. The CLI holds one short-lived session in memory, refuses a secret passed as a flag, and deletes itself when done. ([onboarding.md](../design/onboarding.md))

**The way we work (both feeds)**

29. Three coding agents, one GitHub account. Every post starts with `[agent:name]`, because nobody is notified of anything. ([.claude/CLAUDE.md](../../.claude/CLAUDE.md))
30. "Today we'll do X" is planning. Builders start on an explicit go. ([STATUS.md](../STATUS.md))
31. The owner's steps are the owner's: cloud logins, secrets typed into hidden prompts, never pasted in a chat.
32. Our status page is in the repo: what's live, what's open, in merge order, with the commands.
33. The words we deleted: Whisperer became "Ada's agent"; contributed specialist became "shared specialist"; synthesis became "summary". The whole table. ([direction.md](../design/direction.md))
34. Pass as little as possible on to the user. Every choice we display is a choice we failed to make. ([philosophy.md](../design/philosophy.md))
35. A lane: one worker, one branch, files no other open lane touches. Hot files have one rule each.
36. Everything merged to main deploys. Infrastructure goes in before the merge, never after. That stopped a deploy once; here is what we changed.

**The product (the buyer's feed)**

37. Onboarding is four steps: name your agent, connect tools, invite the crew, open a teamspace. "An agent in the crow's nest. It keeps watch, so you don't have to." (`onboarding.tsx`)
38. Set up Oryxa by telling your coding agent to. It reads one page, writes `oryxa.yaml`, runs plan, shows you, runs apply. Two clicks for the admin. **Beta** until the installer is public.
39. A specialist's access: "Every app you connect" or "Only the apps you choose", and "May change things" is off unless you say. ([web-next/DESIGN.md](../../web-next/DESIGN.md))
40. A teamspace opens on what the team's agent made of the week: summary, decisions, risks, open questions, follow-ups.
41. Updates from your apps: five events or two minutes, whichever comes first, at most twenty, and the agent reports and changes nothing. ([batching/README.md](../../batching/README.md))
42. **Planned:** no answer in fifteen minutes and the card goes to the manager; when the owner is back they get a report of who read what. Posted as "what we're building and why".
43. A side conversation: ask a teammate's agent without interrupting the teammate. It answers from what they allow, and they see that it did.
44. The team's own agent reads the room and writes the week: decisions, risks, open questions. Anyone who may not read it sees no card at all. (I7)
45. Slack stays. Why a team that has agents in it needs rooms of its own anyway: a teamspace with the team's agent, a side conversation, a parley. ([positioning.md](positioning.md#the-story-in-order))

## The technical series

Long posts, 1,200–2,000 words, with code, on the website's `/blog` and
cross-posted to dev.to. Each is submitted to Hacker News as a plain link the
day it is published (not as Show HN), and to one subreddit.

| # | Title | Outline | Where |
|---|---|---|---|
| 1 | First click wins: consent you can't replay | The approve card on two surfaces; the row lock; `decided_by.surface`; leases with the window in the request; what a replay sees | HN, r/programming |
| 2 | Twelve services, one binary | Why one process; the route-collision refusal; one shared pool, migrations under an advisory lock; events through an outbox; what moving a service out costs (nothing) | r/golang, Golang Weekly |
| 3 | Three seconds: what a Slack bot does before it says 200 | Signature, dedupe, one transaction, a content-free nudge; what runs after; what a lost nudge means | HN, dev.to |
| 4 | The guard is not a prompt | Deterministic removal of personal data and credentials; the grounding check; the summary rule; why prompt wording alone is not a control | HN, LinkedIn (short version) |
| 5 | An agent that is you | A member's identity on every run; grants; short-lived tokens when they're away; why a service account acting as everyone is the wrong design; revoke and audit | HN, LinkedIn |
| 6 | Running a company's engineering with three coding agents and a board | The lanes, the hot files, `[agent:name]`, plan before build, what went wrong on 2026-09-29, what the owner never delegates | HN, X thread, LinkedIn |
| 7 | Native or broker: five integrations at 50, 500 and 5,000 members | The cost table; Google's review; the one-click migration; what we'd do differently | founders' feeds, Indie Hackers |

## Objections and answers

Short, true, and the same on every surface. Where the honest answer is "not
yet", say so and give the date or the condition.

| They ask | We say |
|---|---|
| Is our data used to train models? | No. Models run on Vertex AI (Gemini) and Bedrock under their enterprise data terms, which don't train on customer content. Oryxa keeps the record of asks and decisions; inbound Slack text is purged after seven days; sources and secrets never go to Slack. The design is public |
| Why does Google say "unverified app"? | Drive and Gmail scopes are restricted; Google's review takes 6–10 weeks and we are in it. Until then the screen appears and the cap is 100 users. GitHub and Notion have no such step; start there if you prefer |
| Can it send email or change things as me? | Only after you confirm, each time, unless you gave standing permission for that action, which you can see and revoke. Every write carries an idempotency key, and every action is recorded |
| What stops someone asking for what they shouldn't? | A screen before anyone is pinged (is this work, is it plausible for this person, is it a credential request). Then the owner decides, with Deny as a button. Then a guard on the answer |
| Can it make things up? | An answer the approved source doesn't back is held for the owner instead of posted. "Share answer" is a summary of the approved source, not a generation about it |
| Why not Slack AI, or Glean? | They summarise what you can already open. Oryxa's job is the boundary: the answer you can't open, with the owner's consent on record |
| Do we have to move off Slack? | No. Slack stays, and the ask starts there. Oryxa's rooms are for the work your team does with its agents: a teamspace with the team's agent, side conversations, parleys. Use them where they're better, not instead |
| What does it cost? | Free through the beta. Partners get a founding rate locked for a year after the Marketplace lists us. (The owner names it) |
| You're one person | Yes, and the code, the status page and the board are public, so you can see what is being built and how. Deploys are automatic on every merge, and three coding agents work the lanes. Here is how we work: [post #6] |
| Where is our data? | AWS us-west-2 (the services, Postgres, S3), Cloudflare for the web app and the live layer, SES in ap-south-1 for email. EU-only is an open decision for the live layer |
| SOC 2? A DPA? | Not yet. The security design is written down and we'll answer any questionnaire from it. SOC 2 is a phase-3 question, with a date once there are paying teams |
| Microsoft Teams? | Not yet. Slack first, then Teams, on the same loop |
| Will it spam my teammates? | At most five open asks per person, a screen that refuses chatter, and the owner's DM is the only ping. Remembered consent (don't ask twice for the same thing in the same room) is planned |

## Pitches

Sent in week 15, each with the angle it wants, never a press release.

| Where | Angle |
|---|---|
| Golang Weekly, r/golang | Post #2: twelve services, one binary, and the outbox |
| The Changelog (news and podcast) | Post #6: a company run by three coding agents and a board, with the real failure |
| Latent Space, Software Engineering Daily | Agent isolation and consent between agents: a system where two people's agents negotiate with a human click in the middle |
| TLDR AI, Ben's Bites, The Neuron | The sixty-second demo and one sentence |
| Indie Hackers | Post #7, the cost table, and the design-partner numbers |
| Inc42, YourStory (India) | The company: Oryxa AI Private Limited, Dodo as merchant of record, built from India for Slack teams everywhere |
| Cloudflare's and Google ADK's developer channels | See [platforms/ecosystems.md](platforms/ecosystems.md) |

## Assets

| Asset | For |
|---|---|
| OG image: the approve card with its three buttons, on the brand's deep sea | Every link |
| Four screenshots: a teamspace with two people and their agents; the ask in a channel; the owner's DM with buttons; the answer in the thread | Slack listing (the last three), Product Hunt, the website |
| The sixty-second clip (captions), the ninety-second clip, the two-minute team cut | X, LinkedIn, YouTube, the website |
| Stills: background access with Revoke; a specialist's Access tab; the Requests page | Trust posts |
| A one-page PDF for partners: the offer, the ask, the path, the roadmap | After the discovery call |
| The founder's photo and a 60-word bio | Pitches, podcasts |
