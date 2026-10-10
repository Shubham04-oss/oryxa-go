# Integration providers: Pipedream and Composio's alternatives, 2026-10-10

*Research, not a decision. It extends [integrations-research.md](integrations-research.md),
which looked at ingestion; this looks at who should serve agents' tools and
triggers, the part [integrations-plan.md](integrations-plan.md) left with
Composio.*

## The question

Composio serves three things for `/apps`: the connect flow, agents' tools
(through apps' MCP server) and triggers. All of it goes through
`internal/composio`, a REST client of about twenty functions used only by
`apps` and knowledge's sources, so a provider swap is contained. Phase 2 of the
integrations plan (#141 and the per-toolkit lanes) replaces Composio for six
apps with Oryxa's own integrations. Should Pipedream, or another provider,
replace Composio for the rest?

## Recommendation

**Don't move to Pipedream. Keep Composio for tools and the long tail of apps,
and move the high-volume triggers onto Oryxa's own integrations (phase 2).**

1. **Triggers are the cost.** Every hosted provider bills per trigger event.
   At the volumes below, tool calls on Composio cost about $900 a month and
   trigger events about $35,000. Switching provider keeps that cost.
2. **Phase 2 removes it.** GitHub, Slack, Gmail, Drive, Calendar and Notion
   carry nearly all trigger volume. On official SDKs, with Oryxa's own
   tokens, an event costs nothing.
3. **Composio stays cheap for tools** at $0.0003 a call, and covers the apps
   Oryxa will never build itself.
4. **Pipedream fails on its own terms:** the most expensive option at list
   price, hosted only in us-east-1, and owned by Workday since early 2026.

**A possible deadline.** Composio's new pricing applies to sign-ups from
2026-08-15. Earlier customers keep their old pricing until **2026-12-31**.

**Check before acting:**

- **Real volumes:** the figures below are assumptions. Read monthly trigger
  events and tool calls from Composio's dashboard, or count
  `apps.trigger.received.v1` in production. If triggers are a few hundred
  thousand a month, Composio's new pricing is fine and nothing is urgent.
- **Oryxa's Composio sign-up date and plan,** which decide whether the
  2026-12-31 date applies.

## Comparison

Assumed volumes: 100 organizations × 20 members = 2,000 members, 5 apps each;
50 tool calls and 200 trigger events per member per day, so about 3M tool
calls and 12M trigger events a month. List prices, read on 2026-10-10.

| Provider | Est. cost/mo | Tokens exportable | Triggers | India / us-west-2 hosting | Go SDK | Main problem |
|---|---|---|---|---|---|---|
| Composio (today) | ~$36k, 95% triggers | No | Yes; ~15-min polling on Composio's own auth | No | No | New pricing; no token export |
| Pipedream Connect | ~$180k | Only with Oryxa's own OAuth apps | Yes; ~60 s polling | No (us-east-1 only) | No | Cost; Workday; no 2026 changelog |
| Nango | ~$5k | Yes | Forwards providers' webhooks, HMAC-signed | Enterprise, in Oryxa's own AWS | No | Tools are code Oryxa writes |
| Unified.to | ~$7.5k | Tokens can live in Oryxa's AWS vault | Yes, native and polled | US/EU/AU, not India | Yes | Normalized, shallower tools |
| Arcade | ~$30k | Yes | None | Enterprise only | No | No triggers; $0.01 a call |
| Paragon ActionKit | Quote | Unclear | Workflow triggers | Enterprise self-host | No | Not enough public detail |
| Merge Agent Handler | Quote | Not stated | Not stated | EU option | No | Not enough public detail |

Out of the market or out of scope: Klavis now sells AI training data;
Scalekit Agent Auth and Auth0 Token Vault provide credentials but no tools
or triggers.

## Pipedream Connect in detail

- **What it is:** managed OAuth for 3,000+ apps, with Oryxa's own OAuth
  clients allowed; 10,000+ community-written tools through a per-member
  remote MCP server or a REST call; an authenticated proxy (30 s timeout);
  per-member triggers delivered to a webhook with an `x-pd-signature` header.
- **Read and write:** since 2025-10-01 every tool carries MCP annotations
  (`readOnlyHint`, `destructiveHint`), which fits apps' rule that reads run
  unconfirmed. Contributors write them, so they need spot checks.
- **Tokens:** readable only for accounts connected through Oryxa's own OAuth
  clients.
- **Pricing:** Startup $99/month with 100 external users and 10,000 credits;
  then $2 per user and $0.012 per credit. One credit per 30 s of compute for
  each action, tool call, proxy call or trigger event.
- **Compliance:** SOC 2 Type 2, GDPR addendum, HIPAA on Business. Payloads are
  not stored. Hosted only in AWS us-east-1.
- **Company:** Workday announced the acquisition on 2025-11-19 and it closed
  by 2026-02-24. Workday is merging Pipedream, Sana and Flowise into one agent
  platform. Self-serve plans are still sold; the public changelog's last
  entry is 2025-10-01.
- **Gaps:** no Go SDK (TypeScript, Python, Java), no idempotency keys for
  writes, and no published limits for actions, the proxy or triggers.

## Composio today

- **Pricing since 2026-08-15:** Pro $29/month with 100k tool calls and 50k
  trigger events; then $0.0003 a tool call and $0.003 a trigger event, more on
  Composio's own OAuth apps.
- **Custody:** tokens are redacted and the docs say reading account state "is
  not a supported token-export workflow". Enterprise offers self-hosting or
  Oryxa's own encryption keys.
- **SDKs:** the June 2026 Python and TypeScript releases removed the old
  custom-tool APIs. Oryxa calls REST from Go, so this does not affect it.

## Not verified

- Prices for Paragon and Merge; Merge's custody and triggers.
- How Composio's and Pipedream's webhook signatures are computed.
- How good any provider's read/write annotations are.
- Whether Pipedream charges credits for polls that find nothing.
- Business and Enterprise pricing and SLAs for Pipedream and Composio.
- Any provider offering managed hosting in ap-south-1; only self-hosted or
  in-your-own-cloud options give India hosting.

## Sources

Pipedream: [Connect](https://pipedream.com/docs/connect),
[pricing](https://pipedream.com/pricing),
[OAuth clients](https://pipedream.com/docs/connect/managed-auth/oauth-clients),
[MCP](https://pipedream.com/docs/connect/mcp/developers/),
[triggers](https://pipedream.com/docs/connect/components/triggers),
[security](https://pipedream.com/docs/privacy-and-security),
[changelog](https://pipedream.com/changelog),
[Workday's announcement](https://www.nasdaq.com/press-release/workday-signs-definitive-agreement-acquire-pipedream-2025-11-19),
[Workday Q4 FY26](https://newsroom.workday.com/2026-02-24-Workday-Announces-Fiscal-2026-Fourth-Quarter-and-Full-Year-Financial-Results).
Composio: [pricing](https://composio.dev/pricing),
[token custody](https://docs.composio.dev/docs/security/token-custody.md),
[changelog](https://docs.composio.dev/docs/changelog/2026/06/16.md).
Others: [Nango pricing](https://nango.dev/pricing),
[Nango self-hosting](https://nango.dev/docs/guides/platform/self-hosting),
[Unified.to pricing](https://unified.to/pricing),
[Unified.to secrets](https://unified.to/blog/credential_management_in_unified_bring_your_own_secrets_manager),
[Arcade pricing](https://www.arcade.dev/pricing),
[Paragon pricing](https://www.useparagon.com/pricing),
[Merge Agent Handler](https://www.merge.dev/agent-handler).
