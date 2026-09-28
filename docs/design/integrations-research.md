# Integrations and ingestion: research, 2026-09-28

**Decided later on 2026-09-28:**

- Nango, decoupled Activepieces pieces and AgentCore Identity are ruled out.
- Composio stays the tool provider.
- Oryxa builds its own sign-ins and integrations on official SDKs; see
  [integrations-plan.md](integrations-plan.md).

What follows is the research as it stood before that decision. It records what was
learned on 2026-09-28 about getting members' app data (Gmail, Google Drive,
Slack, Notion, GitHub) into Oryxa's knowledge bases, and about who should
provide agents' tools, so the discussion can continue on any machine.

## The problem

Retrieval is solved and tunable on AWS: a Bedrock managed knowledge base, with
hybrid and full-text search, reranking and two-stage retrieval all available.
Ingestion is the weak layer. knowledge's Go sources already do the syncing —
sources, cursors, scheduled passes, extraction — but every read goes through
Composio's proxy, with Composio holding the tokens. Composio stays the tool
provider; the question is ingestion, and whether tools should move too.

Why Composio is a poor sync layer, from its own documentation:

- Its rate limits are shared per organization between tool calls and triggers,
  so ingestion traffic can throttle live agent actions.
- Polling triggers run at most every 15 minutes since April 2026 — for
  Composio-managed auth only. Custom auth configs (a customer's own OAuth app)
  can poll every minute.
- Since its May 2026 security incident, Composio redacts every connection's
  tokens in its API, custom auth configs included. A connection can only be
  used through Composio's proxy; its token can never be read out.
- It can import a token Oryxa already holds, as a `BEARER_TOKEN` connection,
  but only the access token, and it never refreshes it: the holder must push
  fresh ones.

Agents' wake-ups already batch and poll, so instant triggers are not needed.

## Nango

- **The free self-hosted edition is auth and proxy only**: OAuth flows and the
  Connect UI, automatic refresh, `GET /connection` (a live token, so Oryxa can
  call a provider directly), the proxy, `POST /integrations` (registering each
  organization's own OAuth app through the API) and `POST /connection`
  (importing tokens). It has **no syncs, no actions, no MCP server — so no AI
  tools — and no triggers or webhooks**; whether its own "connection created"
  and "refresh failed" webhooks work there is unclear, so assume polling.
- Paid: Nango Cloud (Growth about $500/month for 100 connections, or
  pay-as-you-go from $50/month plus about $0.29 a connection and compute; the
  region is not guaranteed) or Enterprise self-hosting in Oryxa's own AWS
  account (quote only), which adds syncs, actions, triggers and webhooks.
- Its templates exist for all five providers, but some sync metadata only —
  Drive's `documents` carries no content.
- Self-hosting is one service, PostgreSQL 16 and Redis 7.2 (both can be
  shared or external). `NANGO_ENCRYPTION_KEY` can never change once set. The
  license is ELv2: free for internal use, not for reselling Nango itself.
- Known issues: a GitHub App refresh bug (#6136) that breaks connections about
  8 hours after the first refresh — use a GitHub OAuth app, or mint GitHub App
  tokens directly; and Nango is steering users toward its paid self-hosting
  (PR #7453), so the free edition would be pinned to a version.

## Customers' own OAuth apps

The owner's direction is that customers bring their own OAuth apps ("BYOC").
Two findings make that more than a way around verification:

- **Slack** cut `conversations.history` and `replies` to one request a minute
  and 15 messages for apps distributed commercially outside its Marketplace
  (phased through 3 March 2026); a customer's own internal app is exempt and
  keeps about 1,000 messages a request.
- **Google** exempts an "Internal" OAuth app, owned by the customer's own
  Workspace, from brand verification and the CASA assessment, whatever its
  scopes. A customer on personal Gmail cannot make one, and an external app in
  testing is capped at 100 users with refresh tokens expiring every 7 days.

Sharing one grant between two holders is safe only where refresh tokens do not
rotate: Google, Notion and GitHub OAuth apps (by default). Slack with rotation
turned on, and GitHub Apps, rotate — only one holder may refresh.

## Providers' own change detection

- **Gmail**: `history.list` from a stored `historyId`; push through
  `users.watch` and Pub/Sub, renewed within 7 days. `history.list` answers 404
  when too much time has passed, forcing a full pass.
- **Google Drive**: `changes.list` from a `startPageToken`; push channels
  through `changes.watch`, renewed before they expire; Google Docs exported
  with `files.export` as text, at most 10 MB.
- **Slack**: `conversations.history` and `replies` with cursors, or the Events
  API.
- **Notion**: search sorted by `last_edited_time`; webhooks were still in beta
  as of April 2026.
- **GitHub**: webhooks, or polling with ETags, whose "not modified" answers do
  not count against the rate limit. A GitHub App scales its limit with
  installations; an OAuth app does not.

## Options

1. **Own ingestion.** Self-hosted Nango (free) for auth; knowledge's Go syncs
   call providers directly with Nango's tokens and poll their change APIs;
   Composio keeps tools and triggers. Cheapest and least locked in; costs a
   service to run and the providers' quirks to handle.
2. **Nango as the one consent, pushing tokens into Composio.** Nango holds and
   refreshes each grant; a job pushes fresh access tokens into Composio's
   bearer-token connections, so Composio still serves tools and triggers.
   Only Nango refreshes, so rotation stops mattering, and Composio never holds
   a refresh token. Unproven: whether Composio's triggers fire on an imported
   connection — a short test with one Gmail connection settles it. The
   fallback is two separate connections per member, at the cost of a second
   consent.
3. **Two tiers.** A "your keys" tier — self-hosted Nango, Oryxa's own tools and
   polling, behind an Oryxa gateway that authenticates as the member, decides
   with Cedar what may be called, audits every call and limits rates — beside a
   one-click tier on Composio for everyone else. Strong for security-conscious
   buyers ("it's yours, not ours"), and the parked Nango design already has
   this shape (a provider interface in `apps` with Composio as the fallback).
   Costs: Oryxa builds the tools for the first tier (a curated 30–60, ideally
   generated from providers' OpenAPI specifications), setup guides for
   customers' OAuth apps, and two stacks kept behind one tool interface.
4. **Hand ingestion to a vendor.** Paragon Managed Sync is the closest fit —
   full content, incremental sync with deletions, customers' own OAuth apps
   and the only turnkey permissions sync found — at quote-only pricing and
   more lock-in. Ragie Connect, Nango Cloud's syncs and Airbyte Embedded are
   weaker fits; Merge (no Gmail or Slack), Unified.to (pass-through only),
   Pipedream (building blocks, like Composio), Graphlit and similar ("be your
   RAG") and AWS's own connectors (one company's data, not many customers')
   were ruled out.

## Open questions

- Whether Composio's triggers fire on an imported bearer-token connection
  (decides option 2).
- Paragon's coverage of all five providers with full content, how it
  delivers, whether it supplies OAuth apps, and its price (decides option 4).
- For option 3: which tools the first tier needs, and whether generating them
  from OpenAPI specifications holds up.
- Where the cleaning pass — a cheap model turning each document into clean
  Markdown with a title, summary and tags, keeping the original for citations —
  sits in whichever option is chosen.
