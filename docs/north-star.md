# North star: Switchboard

What Oryxa must become. This is the outcome every plan should serve, written
2026-09-28 from the owner's product vision and extended the same day: the
operator also does work for its own owner. Technical choices below are
directional; the design documents and the code settle the details.

## The problem

Enterprise knowledge tools assume the truth lives in tidy, public, static
documents. In a real company it does not:

- **The Slack black hole.** Most of the context people act on lives in private
  channels, threads that scroll away, tickets and people's heads.
- **The access-denied dead end.** Search tools (Glean, Confluence and the like)
  answer a question about restricted content with a blank page or a
  permission wall, and the work stops there.
- **The cross-department privacy dilemma.** Teams cannot hand each other raw
  documents. Engineering cannot give Sales its pull requests and architecture
  debates without confusing them or leaking drafts.

## The idea

Replace the passive search box with an active operator: **Switchboard**. It
does two jobs.

1. **It brokers answers for others.** When someone asks about something they
   cannot read, it finds who owns the answer, asks that person in one click,
   and posts a safe, grounded answer back where the question was asked,
   without ever handing over the document itself.
2. **It does work for its owner.** Each person's operator acts for them: it
   gathers what they may see, asks other people's operators for what they may
   not, and carries out the task with their own apps, within the permissions
   they have given it.

## Job 1: brokering an answer

1. **Ask.** Someone tags Switchboard in a shared channel: an Oryxa room, Slack
   or Microsoft Teams.
2. **Fast intake.** The entry point checks the platform's signature, puts the
   request on the event bus and acknowledges at once, well inside Slack's
   three-second limit.
3. **Screen the request.** Before any person is disturbed, a small model checks
   that the request is real work, that it carries no prompt injection, and
   that it is plausible for the asker's role. An intern asking for the root
   database keys is turned away automatically.
4. **Decide with policy.** Cedar decides whether the asker may read the source
   directly: **allow**, **deny**, or **needs the owner's consent**, for
   instance across a department line on restricted material.
5. **Find the owner.** From the source's metadata (document authors, Git
   commits, Jira assignees, SharePoint owners) Switchboard finds the person
   directly responsible.
6. **Ask the owner privately.** The owner gets a direct message with a card and
   three choices: **Share answer**, **Allow for 1 hour**, or
   **Deny**.
7. **One click, once.** The first click wins. The card turns into a read-only
   confirmation ("Approved by Alice at 14:02") on every device, so nothing can
   be approved twice or replayed.
8. **Write the answer** from the approved source, within what was approved.
9. **Check it on the way out.** A guard removes personal data and credentials,
   checks the answer against the approved source and blocks it if it makes
   things up, and blocks topics the company never lets out (pending
   acquisitions, board discussions, unreleased financials), whoever approved.
10. **Answer in the thread.** The approved answer lands in the original thread,
    and the whole channel is unblocked at once.

## Job 2: doing work for its owner

The same operator works for the person it belongs to.

- **When its owner asks.** It answers from everything the owner may see (their
  apps, their knowledge, their teams), and when the answer sits with someone
  else it runs Job 1 on the owner's behalf, as the requester.
- **When its owner hands it a task.** It does the task with the owner's tools:
  drafting and sending mail, filing and updating tickets, scheduling,
  following up. It delegates parts to teammates' operators when they own the
  work.
- **When something arrives.** Events from the owner's apps (a new email, a
  mention, a ticket assigned) wake it. It sorts what matters, prepares what it
  can and brings the owner only what needs them.
- **When others ask it.** It answers requests addressed to its owner by the
  owner's standing rules: approve what the owner pre-approved, draft a reply
  for the owner to confirm, or bring the request to the owner.
- **Within the owner's permissions.** Anything that changes something in the
  world needs the owner's confirmation, unless a standing permission the owner
  granted covers it. Standing permissions are Cedar policies with a scope and
  an expiry, the owner can see and revoke them, and every action is audited.

## Safety: two airbags

- **On the way in**, the request screen protects people: it stops chatter,
  injection and implausible asks before anyone is pinged.
- **On the way out**, the answer guard protects the company: deterministic
  removal of personal data and credentials, a grounding check against the
  approved source, and company-defined banned topics. It never relies on
  prompt wording alone.
- **Between agents**, the same guard applies to everything one person's
  operator tells another's.

## Governance with Cedar

- **Rules by attribute.** Policies speak in departments, roles,
  confidentiality and relationships, for example: no direct reading of
  restricted material across departments without the owner's consent.
- **Timed access.** "Allow for 1 hour" becomes a lease: a permit whose
  window travels in the request's context, so it stops working the moment it
  expires, with no clean-up job.
- **Executive override.** An executive's question skips manual approval, but
  the answer goes to their private messages, with a note in the channel that
  it was sent privately.
- **Takeover.** When an owner is away or does not answer an urgent request
  within 15 minutes, the card goes to their manager. The manager's approval
  grants an expiring lease, and when the owner returns they get a report of
  who read what, who approved it, and when.

## Working as a team

- **Shared state.** An approved answer unblocks the whole thread, and consent
  is remembered for that room (30 days by default), so the same document does
  not ping its owner again.
- **N-of-M sign-off.** Sensitive actions can need, say, two of three approvers,
  with a live counter on the card: "1 of 2 approvals (Alice approved, Dave
  pending)".
- **Claiming.** A request to a team alias goes to the whole team; the first to
  act claims it, and everyone else's card updates to say so.

## Where it lives

- **Surfaces.** Oryxa's own app (rooms, direct messages, side conversations),
  Slack (Block Kit cards, in-place updates, direct messages) and Microsoft
  Teams (Adaptive Cards with Universal Actions, the Graph activity feed).
- **One identity per person**, keyed by normalized work email, joining their
  Slack, Teams, Google, Microsoft and GitHub identities.
- **Knowledge, two ways.** Where a company already has search (Glean, Danswer,
  Microsoft Graph), Switchboard queries it as the asker and adds consent,
  guards and actions on top, with no index of its own. Where it has none,
  Switchboard builds a curated one, filtering out stubs and near-duplicates
  and weighing documents by how informative and authoritative they are
  before anything is indexed.

## Getting a company on board

- **Their own apps, no audits.** The company registers Switchboard as an
  internal app in its own Google or Microsoft tenant, which skips public app
  verification and Google's yearly security assessment. Oryxa's sign-in system
  already takes each organization's own OAuth apps.
- **A one-shot installer.** A small Go command line tool grants the
  permissions with the engineer's own cloud credentials, catches the OAuth
  callback locally, and removes itself when done.
- **Configuration as a file.** The whole workspace (identity provider, search
  mode, which surfaces, governance switches such as cross-department consent,
  executive override and takeover) lives in one version-controlled file.
- **A sandbox on the landing page.** Two columns, the asker in a channel and the
  owner in their direct messages, with a live trace underneath: the request,
  the Cedar decision, the guard's result.

## Where Oryxa stands (2026-09-28)

| Piece | Status |
| --- | --- |
| Asking an owner, who approves on a card; the answer comes back | Mostly built: agent-to-agent requests with approve and reject, and an approval can already limit which tools and permissions the work gets. Missing: the three choices, and results landing in the thread that asked |
| Doing work for the owner | Built in part: each member's agent reads and writes their connected apps (writes after the member confirms), searches their knowledge, and wakes on app events. Missing: standing permissions as policies, and the holes below |
| Answer guard | Partly built: Bedrock Guardrails screens answers for personal data and the owner reviews held answers. Missing: the grounding check, banned topics, and guarding agent-to-agent messages |
| Request screen | Partly built: Jev screens app events. Nothing screens a request before it reaches an owner |
| Cedar | An engine exists, parked with knowledge bases (PR #109), with bypasses to fix first. Missing: attribute rules, the "needs consent" outcome, leases |
| Event bus and signed intake | Built: EventBridge, SQS and the outbox; apps' webhook ingress verifies Slack's signatures. Missing: first-click-wins on consent |
| Slack and Teams surfaces | Not built. Slack is only a tool today, through Composio |
| Organizations' own apps | Built (phase 1 of [integrations-plan.md](design/integrations-plan.md)), switched off |
| Finding the owner | Not built. Needs owner metadata from connectors and knowledge bases, which are on hold |
| Remembered consent, N-of-M, claiming, executive override, takeover and audit | Not built |
| Knowledge overlay on existing search | Not built. The native knowledge base exists; knowledge work is on hold |

## Build order

The focus from 2026-09-28 is agents and teams; connectors and knowledge bases
are on hold.

1. **Close the cross-member holes** the 2026-09-28 review found. They break the
   core promise: a stand-in agent in a room can list and decide its owner's
   requests, a sender decides whether a request needs approval, and agents
   woken by app events can approve requests.
2. **Consent as a first-class step**: the three choices, first click wins,
   Cedar's allow, deny or needs-consent with expiring access, the answer guard
   with a grounding check, and the answer delivered to the thread that asked.
3. **Reliability**: retries on model rate limits, rooms that do not drop turns,
   and honest status for blocked or cut-off work.
4. **Doing work for the owner, safely**: standing permissions as policies, with
   scope, expiry, revocation and audit.
5. **The team pieces**: remembered consent, N-of-M, claiming, takeover with a
   report to the owner.
6. **Slack**, then Teams, as surfaces, on the sign-in and webhook work already
   built.

## Open decisions

- Build the loop in Oryxa's own rooms first, then Slack, or Slack first.
- Whether "Switchboard" is the product's name, and what each person's operator
  is called (the "Krude" question).
- Defaults: how long timed access lasts, how long consent is remembered, and
  which actions an owner may pre-approve.
