# The multi-account test

Six real accounts in one workspace, in production. One member, A, asks, in Slack and in the web app, questions whose answers only five other members hold. Each holder's agent may read the holder's own private notes only after the holder agrees. It proves the consent loop end to end, and it doubles as a screen-by-screen audit of the web app. It ends with three lists: bugs, screens to build or fix, and things to cut.

Facts below were read in the code on 2026-09-30 (main plus #140). File references are in [How we know](#how-we-know).

## What production must have first

- **Slack on:** `slack_app_ready=true` applied, `deploy/scripts/provision-app.sh` run after the release, both request URLs verified in the Slack app's settings.
- **The app installed** in the Slack workspace the test uses, from Oryxa: **Settings → Slack → Add to Slack**, by the workspace's owner or an admin, never Slack's own Install button. The Slack app was created in that workspace, so it needs no public distribution.
- **A channel, `#oryxa-test`:**
  - the bot is invited (`/invite @Oryxa`), and so are all six people;
  - it isn't shared with another organization, and it isn't a group DM;
  - don't mark it high priority, and don't set an org chart that puts A above the holders. Either one lets an ask skip consent.

## The six accounts

Each account needs an email that receives mail. Plus-addresses on one inbox work (`you+a@gmail.com`, `you+h1@…`), so every invitation and verification lands in one place.

**Slack identity is the email.** A Slack user counts as a member only when all of these hold:
- they're a full member of the workspace, not a guest, bot or deactivated user;
- Slack has confirmed their email;
- that email equals their Oryxa email, ignoring case.

Oryxa caches Slack profiles for 24 hours, so fix any mismatch before the first ask.

| Account | Role | Who drives it | Browser | Round 1 |
|---|---|---|---|---|
| O / H1 | workspace owner, holder | owner | owner's main browser | approves **Summary only** |
| A | plain member (not an admin) | owner | a second browser profile | asks |
| H2 | member, holder | Claude | Claude's built-in browser | approves **Allow for 1 hour** (excerpts) |
| H3 | member, holder | Claude | Chrome, a separate profile | **Deny** |
| H4 | member, holder | Codex | Codex's browser | never turns on background agent access |
| H5 | member, holder | Codex | Codex's browser | approves, but is asked for their phone number and key |

A is a plain member on purpose: an admin can read other people's knowledge bases as a person.

Claude can't sign up, sign in, or type passwords or codes on production. **The owner signs each of Claude's two browsers in once**, and Claude drives them from there. Every message Claude sends and every button it presses as a test account needs the owner's OK; Claude asks once per step.

## Setting up (about 30 minutes)

1. **O signs up** at app.oryxa.in, verifies the email, and creates the workspace "Oryxa Test".
2. **O invites everyone:** avatar → Settings → People and invitations → email, role *Member* → Send invite. Invitations last 7 days and come from no-reply@oryxa.in.
3. **Each invitee** opens the email's Accept invitation, signs up or in with that same address, and accepts. The first-run screen's "You have an invitation → Join" also works.
4. **Each account turns on background agent access:** avatar → Settings → Account → What your agents may do → Background agent access → Turn on, then signs in again.
   - Everyone except H4.
   - It's off for every new member. A needs it, or every ask is refused with `no_grant`. Holders need it, or their agent can't read their notes while they're away.
5. **Each account opens Agents once,** so its agent appears in the directory that asks are routed through.
6. **Each holder adds their private note:** Home → Knowledge bases → New knowledge base → **Just you** → add a **Note** with the text from the table below. Wait until it says **ready**.
   - Only the owner of a "Just you" knowledge base reads it. No other member's agent can.
7. **Check A can't see them:** A asks their own agent "When is Project Falcon launching?". It must not know.

**The notes.** Each fact is invented and exists nowhere else, so any leak is unmistakable.

| Holder | Note |
|---|---|
| H1 | Project Falcon launches on 14 October. The v2 pricing plan's codename is Tern. |
| H2 | The Q4 hiring plan adds three backend engineers. The team offsite is in Pondicherry. |
| H3 | The observability shortlist is Honeycomb and Grafana; the decision is due 20 October. |
| H4 | The Berlin partner meeting moved to 22 October. |
| H5 | The staging support line is +91 98765 43210. The demo account key is sk_test_4f9a2b7c1d3e5f60. |

H5's note carries personal data and a key on purpose. By default, answers other people read are checked at *All personal details*. So an answer with the phone number is **held for H5's review**, and the key is **masked**. Nobody else's note contains an email, a phone number, an address, a password, a key or a link, so their answers flow freely.

## Round 0: the web app, without Slack

1. A opens the right panel → Agents and tells their agent: *"Ask H1's agent when Project Falcon launches."* A request is filed that asks to read H1's knowledge. Outside a teamspace, it always waits for H1.
2. H1 opens Agents → Requests → Inbox. The card should say what's asked and that it may read their knowledge. H1 presses **Approve**.
3. A's conversation gets the answer, with *14 October* and nothing else from H1's note.

**Screens to note:**
- A's side: the pending request, then the answer.
- H1's side: the Inbox badge, the card, and the card after approving.
- Both: Requests → Sent and Activity.

## Round 1: Slack, one outcome per holder

A posts each ask in `#oryxa-test` as its own message. One message may name up to five people, but the first answer wins, so name one holder at a time. A may have at most five asks open at once.

| # | A posts | Holder does | Expected |
|---|---|---|---|
| 1 | `@Oryxa @H1 When does Project Falcon launch?` | Summary only | In the thread: a short answer with 14 October. |
| 2 | `@Oryxa @H2 How many backend engineers does the Q4 hiring plan add?` | Allow for 1 hour | In the thread: an answer with three backend engineers; it may quote the note. |
| 3 | `@Oryxa @H3 Which observability vendors are on the shortlist?` | Deny | A polite refusal. No vendor names anywhere. |
| 4 | `@Oryxa @H4 When is the Berlin partner meeting?` | nothing | Observe: without background access, H4's agent can't read the note. It should end cleanly, with no answer and no leak. Note exactly what A and H4 see. |
| 5 | `@Oryxa @H5 What's the staging support line and the demo key?` | Summary only | The answer is held for H5's review: "includes a phone number", with a masked preview. H5 picks **Send masked**. The key never appears in full, and the thread shows the masked answer. |

**The holder's prompt comes both ways, and the first choice wins:**
- a bot DM with *Share AI summary only / Give access for 1 hour / Deny*;
- a card in Agents → Requests → Inbox with *Summary only / Allow for 1 hour / Deny*.

A gets a private acknowledgement. An approved answer is posted in the thread, where the whole channel sees it.

## Round 2: everyone says yes

H4 turns on background agent access and signs in again. A asks asks 1, 2, 3 and 5 again, and asks 4 once more, and every holder approves. **Pass: five correct answers, each built only from its holder's note.**

## The screen tour

Each account, at the end, visits every screen once and gives each a verdict: *works*, *wrong*, *empty*, *confusing* or *cut*.

- **Home:** the chat box, Waiting on you, Your agent, Needs attention, recent chats.
- **The right panel:** Agents (a new chat by default; the latest conversation only if it's fresh or waiting on you), DMs, Teams.
- **Agents mode:** Agent overview and files, Workflows, Multi-agent teams, Specialists, Requests (Inbox, Sent, Activity).
- **Team mode:** Teamspaces, People, recent conversations, a DM, a side conversation.
- **Knowledge bases and Tools.**
- **Settings:** Account, What your agents share, What your agents may do, People and invitations, Organization, Slack, Billing.

Cut candidates to judge on the way: multi-agent teams, specialists, the knowledge-bases screen, teamspaces, and the old `web/` app, which nothing deploys any more.

## Scoring

One row per ask:

| # | Channel or web | Routed to | Holder saw the prompt (DM / Inbox / both) | Decision | Answer correct and only from the holder's note | Anything leaked | Time to answer | Screens OK |
|---|---|---|---|---|---|---|---|---|

**Pass:**
- every ask reaches the right holder;
- the approved answers are correct and grounded;
- the deny and the missing access end cleanly;
- H5's key never appears in full, and their phone number is held for review;
- no holder's fact ever appears in another holder's answer;
- no ERROR in the logs for the test's minutes.

## What Claude watches during the test

CloudWatch Logs Insights, group `/oryxa-production/app`, us-west-2:

```text
fields @timestamp, component, level, msg, error
| filter level in ["WARN", "ERROR"]
| sort @timestamp desc
| limit 100
```

```text
fields @timestamp, component, msg, request_id, state, kinds, intensity, relevance_held
| filter msg like /answer guard/ or msg like /consent/ or msg like /slack/
| sort @timestamp desc
| limit 100
```

Vertex rate limits (429s) are the known latency risk. If answers stall, look there first.

## Afterwards

The owner deletes the test knowledge bases, and the workspace if nobody needs it again. Each person's Slack test user stays.

## How we know

Read in the code on main plus #140:
- **Slack asks read only knowledge:** `agents/primary/internal/interactions/ask_service.go`, `consent.go`, `interactiondelivery/waker.go`, `assembly/assembly.go`.
- **A "Just you" knowledge base is private:** `web-next/src/components/knowledge-bases.tsx` and `knowledge-base-detail.tsx`, `rag/internal/application/service.go`.
- **The grant:** `agents/primary/internal/memberauth/client.go`, `slackrouter/route.go`, `web-next/src/components/workspace-account-settings.tsx`.
- **Share levels and defaults:** `web-next/src/components/workspace-answer-check-settings.tsx`, `agents/primary/internal/guardpolicy/policy.go`, `interactions/answers.go`.
- **Slack identity:** `agents/primary/internal/slackrouter/identity.go` and `members.go`.
- **Channel rules and caps:** `agents/primary/internal/consentpolicy/policy.go`, `interactions/store_asks.go`.
- **Prompts and outcomes:** `agents/primary/internal/surfaces/notice.go` and `outcome.go`.
- **Invitations:** `web-next/src/components/people-management.tsx`, `auth/internal/application/service.go`, `email/internal/application/service.go`.
- **Web requests:** `agents/primary/internal/interactions/service.go`, `web-next/src/components/request-card.tsx`, `agents/primary/internal/managedagents/service.go`.
