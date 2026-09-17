# Provider readiness: the 12 without a surface

Connections connects 17 providers. Five of them — GitHub, Slack, Linear,
Microsoft and HubSpot — also have a provider here: a subscription target, a
verified delivery, events, and actions. The other 12 have connections but
nothing to trigger on and nothing to call.

This is the survey for giving them one. It exists because **they do not all
fit the contract the first providers were built against**. Everything below is
checked against each provider's own documentation or OpenAPI specification.
The first survey's sources are in `docs/research/`; how each provider proves a
delivery was checked again on 2026-09-17, against the pages listed at the end.

**HubSpot is built.** Its shape differs from the first four in a way worth
knowing: HubSpot sets **one target URL per app**, not one per subscription, so
it is an app delivery rather than a webhook one, and the app's client secret —
not a per-subscription secret — is what signs every delivery.

## The contract, and what it assumes

`Provider` supports two shapes, enforced by `TestBuiltinProvidersAreConsistent`:

- **`DeliveryWebhook`** — integrations registers something per subscription,
  supplying a `CallbackURL` and a secret, and removes it again.
- **`DeliveryApp`** — the provider sends everything to one URL set on its app.

Both need a `Receive` that proves a delivery is genuine. Every provider built
so far can do that: GitHub and Linear sign with an HMAC over the body using a
secret we supply, Slack signs with the app's signing secret, and Microsoft
Graph echoes back a `clientState` we supply, compared in constant time.

## Four do not deliver the usual way

The first survey said five of these providers could not have a verified
delivery at all. Checked again, that was too strong. Asana's gap was ours and
is closed. Of the other four, two are the provider's limit and two were our
own reading — and each has a route that can be verified, at a different cost.

| Provider | Whose limit | The verifiable route |
| --- | --- | --- |
| Notion | Ours | Deliveries are signed; only setting one up is manual. An app delivery whose secret arrives by handshake |
| Atlassian (Jira) | Mostly ours | A token signed with the app's own client secret, in a format Atlassian does not document; or a secured admin webhook |
| Google Drive | Google's, for `watch` | Polling its change log; the Workspace Events API, generally available since 2026-05-18; or `watch` taken as a nudge |
| Confluence | Atlassian's | No webhook API for OAuth 2.0 apps. Polling, or a Forge app |

### Notion — signed, but set up by hand

Every delivery carries `X-Notion-Signature`, an HMAC-SHA256 of the raw body
keyed with the subscription's `verification_token`. That is as strong as
GitHub's. What Notion lacks is an API to create subscriptions: someone creates
one in the connection's settings, and its URL cannot change once verified.

That is the app delivery shape Slack and HubSpot already have — one URL set on
the app, one secret signing everything — so the first survey's reason for
blocking Notion does not hold. The difference is where the secret comes from.
Slack's and HubSpot's are given to integrations by an admin. Notion's arrives
once, in an unsigned `POST` to the URL, and must be copied back into Notion's
verification modal. So integrations has to reserve an app's route before it has
a secret, keep the token that arrives, show it to the admin, and refuse to
replace it silently: anyone who learns the route could post a token of their
own, although Notion's verification step would then refuse it rather than
accept it.

Notion publishes no replay window, so deliveries must be deduplicated on the
event `id`. Events carry identifiers, never content.

### Atlassian (Jira) — signed with a key we hold, in a format nobody documents

A dynamic webhook — the kind an OAuth 2.0 app registers through
`/rest/api/3/webhook` — has no body HMAC, but its `Authorization` header
carries a token that Atlassian says "is signed with the app's client secret".
Connections already holds that secret for the organization's app, so the key
to verify it is ours. What Atlassian does not document is the token's
algorithm, its claims, its lifetime, or whether anything in it binds the body.
One delivery captured from a Jira Cloud site answers all four; building on a
guess instead would mean trusting a token nobody has checked.

Two documented alternatives exist:

- **Secured admin webhooks.** A Jira admin creates the webhook on Jira's
  webhooks page with a `secret`, and every delivery then carries an HMAC in
  `X-Hub-Signature`. That is verifiable today, but set up by hand, as an app
  delivery is. Its REST form, `/rest/webhooks/1.0/webhook`, is documented for
  other integrations and closed to Connect apps; whether an OAuth 2.0 token may
  call it is not stated.
- **A Forge app**, described under Confluence.

The `X-Hub-Signature` scheme does not apply to dynamic webhooks.

### Google Drive — no signature on `watch`, and a signed alternative

Drive's `files.watch` and `changes.watch` notifications carry no signature, no
HMAC and no timestamp. The only check is the channel `token`, a string we
choose and Google echoes back in a header. The body is always empty. This is
Google's limit, and nothing built here changes it.

It is, however, the same assurance integrations already accepts from
Microsoft Graph, whose `clientState` is also a secret we choose and Graph
echoes back — and a Drive notification carries less that could be forged.
Three routes exist:

- **Polling `changes.list`.** Drive's change log is made to be polled: keep
  the page token, which never expires, and ask for what changed since. It goes
  through the API with the connection's own credential, so it is verified by
  construction. Each shared drive keeps a change log of its own, and an entry
  is a file's current state rather than a diff. Its cost is latency and a
  request per connection per interval — and since agents are to be woken in
  batches rather than per event, latency within the batch window costs nothing.
- **The Workspace Events API.** Drive event subscriptions — a file added,
  moved, edited or trashed; access proposals; comments and replies; approvals —
  became generally available on 2026-05-18. Events arrive through Pub/Sub,
  whose push delivery carries a JWT that Pub/Sub signs; the receiver checks the
  signature, and that its audience and service account match the push
  subscription. That is a real signature. The cost is Google Cloud setup:
  Google's guide creates the Pub/Sub topic in the app's Cloud project, and
  organizations here bring their own OAuth clients, so each would need a topic
  of its own. It fits Oryxa-managed apps better than organization-owned ones.
  Subscriptions expire and need renewing.
- **`watch` as a nudge.** The token is compared in constant time, as Graph's
  `clientState` is, and a notification is taken as a prompt, never as data.
  Because it carries nothing, it can prompt a `changes.list` call with the
  connection's own credential, and events can come only from what that
  authenticated call returns. A forged notification can then cause an extra
  fetch and nothing else. That needs `Receive` to make an authenticated call,
  which the `Provider` contract does not allow today, and fetches coalesced so
  a flood of nudges cannot spend the connection's quota.

What must never happen is forwarding a `watch` notification as though it were
signed — the mistake behind Nango's advisory
[GHSA-42f5-jg2r-v8j7](https://github.com/NangoHQ/nango/security/advisories/GHSA-42f5-jg2r-v8j7).

### Confluence — no webhook API for OAuth 2.0 apps

Confluence Cloud has no REST API for webhooks. Atlassian's staff say there are
"no official, documented, and supported ways to manage Confluence webhooks with
OAuth 2.0" (2024-08-19), and point to Forge and Connect instead. Webhooks exist
only as a Connect descriptor module, and Connect is being retired. This is
Atlassian's limit. Confluence *actions* over the same token work fine.

Two routes can be verified:

- **A Forge app.** Forge triggers can send Jira and Confluence product events
  to a remote endpoint, carrying a Forge Invocation Token checked against
  Atlassian's published JWKS, with issuer `forge/invocation-token` and the
  app's id as its audience. It is fully documented, and it covers Jira too. The
  cost is a different model: Oryxa would build and own one Forge app, which a
  site admin installs from an installation link. No Marketplace listing is
  needed, though an app with a license, or one submitted to the Marketplace,
  cannot be shared that way.
- **Polling** Confluence's search by `lastmodified`, which is precise to the
  minute, so polls overlap and deduplicate on content id and version. It is
  verifiable by construction and
  paid for in latency and requests. The contract has no polled delivery today.

### Asana — the provider issues the secret during the handshake

Asana `POST`s to the target with `X-Hook-Secret` and requires it echoed back
before the create returns `201`. That secret, not one we generated, signs every
later delivery. This was the framework gap — **now closed**: `Registration`
carries a `Secret` and `ActivateSubscription` takes an optional replacement.
Asana is otherwise an ordinary provider, and must treat its empty heartbeat
payload as a success.

**ClickUp is the same shape** — its create endpoint takes no secret and the
response carries `webhook.secret` — so the same change unblocks it too.

## What is ready

| Provider | Delivery | Verification | Ready? |
| --- | --- | --- | --- |
| **HubSpot** | app delivery, one target URL | v3 HMAC-SHA256, base64, 5-min window — fully specified | **Built** |
| **ClickUp** | webhook, created via API | HMAC-SHA256 hex in `X-Signature`, secret from the create response | Yes, with the new extension |
| **Asana** | webhook, created via API | HMAC-SHA256 hex in `X-Hook-Signature`, secret from the handshake | Yes, with the new extension |
| **Notion** | app delivery, set up in Notion's settings | HMAC-SHA256 in `X-Notion-Signature`, secret from the handshake | Yes, once an app delivery can take its secret from a handshake |
| **Atlassian (Jira)** | webhook, created via API | bearer token signed with the app's client secret, **format undocumented** | After one real delivery is captured; secured admin webhooks now |
| **Google Drive** | polling `changes.list`; later Workspace Events through Pub/Sub | polling needs none. `watch`: an echoed token only. Pub/Sub: a signed JWT | Yes, by polling, once integrations has a polled delivery |
| **Confluence** | polling search; **no webhook API** for OAuth 2.0 apps | polling needs none. A Forge app: a Forge Invocation Token | Yes, by polling, once integrations has a polled delivery |
| Airtable, Box, Dropbox, Figma, Intercom, Salesforce, Zendesk | unconfirmed | unconfirmed | Not yet surveyed |

## Two things worth knowing about HubSpot and Atlassian

- **HubSpot's current generation is dated `2026-09`**, not `v3` — `/crm/objects/2026-09/contacts`. Its webhook subscriptions live at `/app-webhooks/2026-09/{appId}/subscriptions`, and the request field is `eventType`, not `subscriptionType`. A **private app cannot manage webhooks through the API at all** (documented, verbatim: "not currently supported"), so runtime subscription creation requires a legacy public app. The deals path is inconsistent inside HubSpot's own docs — the spec says `/crm/objects/2026-09/0-3`, the guide says `/deals`.
- **Atlassian's `GET/POST /rest/api/3/search` is fully removed**, not deprecated. The replacement `/search/jql` is cursor-based, so any `startAt`/`total` pagination must become `nextPageToken`. Dynamic webhooks also expire after 30 days and need a scheduled refresh, and OAuth2 allows only 5 webhooks per app per user per tenant.

## Recommendation

Build **ClickUp, Asana and Notion**: all three sign with a documented HMAC, and
Notion needs only an app delivery that takes its secret from a handshake.
Capture one real Jira delivery from a test site before building Jira's
triggers; if the token turns out not to be verifiable, secured admin webhooks
are the documented fallback.

For **Google Drive and Confluence**, poll. Agents are to be woken in batches
rather than per event, so a trigger that arrives within the batch window is as
good as a pushed one, and polling through the connection's own credential is
verified by construction. That needs a polled delivery kind in integrations,
and nothing from the providers or from Oryxa-managed apps. The signed push
routes — the Workspace Events API, which wants a Google Cloud project Oryxa
owns, and a Forge app, which would also give Jira a fully documented route —
are worth taking up once volume makes polling expensive.

Airtable, Box, Dropbox, Figma, Intercom, Salesforce and Zendesk have not been
surveyed yet. Nothing above should be read as settled for those.

## Sources for the 2026-09-17 check

- Notion webhooks: <https://developers.notion.com/reference/webhooks>
- Jira Cloud webhooks: <https://developer.atlassian.com/cloud/jira/platform/webhooks/>
- Google Drive push notifications: <https://developers.google.com/workspace/drive/api/guides/push>
- Google Drive release notes, for the Workspace Events API's general availability: <https://developers.google.com/workspace/drive/release-notes>
- Creating a Workspace Events subscription: <https://developers.google.com/workspace/events/guides/create-subscription>
- Authenticating Pub/Sub push deliveries: <https://docs.cloud.google.com/pubsub/docs/authenticate-push-subscriptions>
- Forge Remote: <https://developer.atlassian.com/platform/forge/remote/essentials/>
- Distributing Forge apps: <https://developer.atlassian.com/platform/forge/distribute-your-apps/>
- Confluence webhooks with OAuth 2.0, answered by Atlassian: <https://community.developer.atlassian.com/t/confluence-webhook-api-scope/82746>
