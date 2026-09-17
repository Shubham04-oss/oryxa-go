# Provider readiness: the 13 without a surface

Connections connects 17 providers. Four of them — GitHub, Slack, Linear,
Microsoft — also have a provider here: a subscription target, a verified
delivery, events, and actions. The other 13 have connections but nothing to
trigger on and nothing to call.

This is the survey for giving them one. It exists because **they do not all
fit the contract the first four were built against**, and the difference is not
a matter of effort — five of the 13 cannot have a verified delivery at all, for
four different reasons. Everything below is checked against each provider's own
documentation or OpenAPI specification; sources are in `docs/research/`.

**HubSpot is built.** It is the one provider of the 13 that needed neither a
framework decision nor a reverse-engineering task. Its shape differs from the
first four in a way worth knowing: HubSpot sets **one target URL per app**, not
one per subscription, so it is an app delivery rather than a webhook one, and
the app's client secret — not a per-subscription secret — is what signs every
delivery. Connections already stores that secret, so nothing had to change.

## The contract, and what it assumes

`Provider` supports two shapes, enforced by `TestBuiltinProvidersAreConsistent`:

- **`DeliveryWebhook`** — integrations registers something per subscription,
  supplying a `CallbackURL` and a secret, and removes it again.
- **`DeliveryApp`** — the provider sends everything to one URL set on its app.

Both need a `Receive` that proves a delivery is genuine. Every provider built
so far can do that: GitHub and Linear sign with an HMAC over the body using a
secret we supply, Slack signs with the app's signing secret, Microsoft Graph
uses a `clientState` it echoes back.

## Five cannot prove a delivery is genuine

This is the finding that matters. Each is blocked for a different reason, and
none of them is "more work" — they need a decision.

### Google Drive — no signature exists

Drive push notifications carry no HMAC, no shared secret, no timestamp and no
published sender addresses. Google's security model is that the channel token
is unguessable and the address is secret; the token exists so you can check
"that each incoming message is for a channel that your application created".
A receiver can compare `X-Goog-Channel-Token` in constant time and correlate
`X-Goog-Channel-ID`/`X-Goog-Resource-ID`, and that is all. No replay protection
is possible. The notification body is always empty.

The verifiable alternative is the Workspace Events API with a Pub/Sub topic,
whose push delivery supports OIDC JWT authentication — a different transport,
not a different header.

### Atlassian (Jira) — a bearer token whose verification is undocumented

Dynamic webhooks have **no shared secret and no body HMAC**; `WebhookDetails`
has no `secret` field. Deliveries carry `Authorization: Bearer <token>`, and
Atlassian says only that it "is signed with the app's client secret" and that
verifying it "with a library" is recommended. It does not document the
algorithm, the claims, the issuer, the audience or the lifetime.

The `X-Hub-Signature` scheme in Atlassian's docs belongs to *admin webhooks*, a
different mechanism, and does not apply. So Jira cannot be built until someone
captures a real delivery and reverse-engineers the token — a task that needs a
live Jira site, not documentation.

### Notion — webhooks exist but cannot be created through the API

Verification itself is strong (`X-Notion-Signature`, HMAC-SHA256 over the raw
body). The problem is registration: there are **zero webhook paths** in
Notion's OpenAPI spec. Subscriptions are created in a dashboard and the URL is
immutable once verified. Our service could only ever be the receiver, with a
human configuring each one — which is not what a subscription API here means.

There is also no documented replay window, so deliveries would need deduping on
the event ID as the only defence.

### Confluence — no webhook API at all

Verified by absence: no webhooks API group, zero webhook paths in the official
specifications. Webhooks are descriptor-only, so eventing would need Forge or
Connect. Confluence *actions* over the same token work fine.

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
| **Atlassian** | webhook, created via API | bearer JWT, **undocumented** | Blocked |
| **Google Drive** | `files.watch` / `changes.watch` | **none exists** | Blocked |
| **Notion** | dashboard only | strong, but no registration API | Blocked |
| **Confluence** | **none exists** | — | Blocked |
| Airtable, Box, Dropbox, Figma, Intercom, Salesforce, Zendesk | unconfirmed | unconfirmed | Not yet surveyed |

## Two things worth knowing before building HubSpot or Atlassian

- **HubSpot's current generation is dated `2026-09`**, not `v3` — `/crm/objects/2026-09/contacts`. Its webhook subscriptions live at `/app-webhooks/2026-09/{appId}/subscriptions`, and the request field is `eventType`, not `subscriptionType`. A **private app cannot manage webhooks through the API at all** (documented, verbatim: "not currently supported"), so runtime subscription creation requires a legacy public app. The deals path is inconsistent inside HubSpot's own docs — the spec says `/crm/objects/2026-09/0-3`, the guide says `/deals`.
- **Atlassian's `GET/POST /rest/api/3/search` is fully removed**, not deprecated. The replacement `/search/jql` is cursor-based, so any `startAt`/`total` pagination must become `nextPageToken`. Dynamic webhooks also expire after 30 days and need a scheduled refresh, and OAuth2 allows only 5 webhooks per app per user per tenant.

## Recommendation

Build the three that are ready — **HubSpot, ClickUp, Asana** — and give the
five blocked providers **actions only**. Actions need no delivery verification
at all, so Google, Notion, Atlassian and Confluence can have a useful surface
immediately, with triggers deferred until the verification question is settled
per provider. That is a decision for the user, not something to work around
quietly: accepting an unverifiable delivery, or reverse-engineering an
undocumented token, are both choices with security consequences.

Airtable, Box, Dropbox, Figma, Intercom, Salesforce and Zendesk have not been
surveyed yet. Nothing above should be read as settled for those.
