# Provider readiness: the 13 without a surface

Connections connects 17 providers. Four of them — GitHub, Slack, Linear,
Microsoft — also have a provider here: a subscription target, a delivery that
is verified, events, and actions. The other 13 have connections but nothing to
trigger on and nothing to call. This note is the survey for giving them one,
and the reason it exists is that **they do not all fit the contract the first
four were built against**.

Everything below is checked against the provider's own documentation. Where a
claim is not yet confirmed it says so.

## What the contract asks of a provider

`Provider` in `internal/providers/providers.go` supports exactly two shapes,
and `TestBuiltinProvidersAreConsistent` enforces the choice:

- **`DeliveryWebhook`** — integrations registers something per subscription,
  supplying a `CallbackURL` and its own generated `Secret`, and removes it
  again. Needs `Register` and `Unregister`; must not set `AccountKey`.
- **`DeliveryApp`** — the provider sends everything to one URL configured on
  the provider's app. Needs `AccountKey` to route a delivery to the
  connections it belongs to; must not set `Register`.

Both need a `Target` that validates what a subscription watches, and a
`Receive` that verifies a delivery and reads it.

The part that decides how much work a provider is, is **who chooses the
signing secret and how a delivery is proved genuine**. Two of the 13 fail the
current contract outright.

## The two that do not fit

### Asana — the provider chooses the secret, during the registration call

Asana's webhook is established, then Asana itself `POST`s to the target URL
with an `X-Hook-Secret` header. The receiver must echo that value back in an
`X-Hook-Secret` response header and answer `200`/`204`; only then does the
create call return `201`. That secret — not one we generated — is what signs
every later delivery, as `X-Hook-Signature`, HMAC-SHA256 over the body.

The mismatch is precise. `subscriptions.go` generates the secret and stores it
*after* `Register` returns, and `Register` has no way to answer a handshake
because `Reception.Reply` belongs to `Receive`, which never sees the handshake.
Supporting Asana needs one of:

- a `Registration.Secret` the application persists after `Register` returns,
  replacing the one it generated, or
- a provider-declared option saying "the provider issues the secret".

The first is smaller and generalises to every provider that issues its own
secret. It is a framework change, not a provider change, and it should be made
deliberately rather than smuggled into a provider file.

Asana also sends a heartbeat with an **empty** `events` array every eight
hours, and deletes a webhook it cannot reach for 24 hours, so `Receive` must
treat an empty event list as a success rather than a malformed delivery.

### Google Drive — a delivery cannot be verified at all

Drive push notifications are `changes.watch` / `files.watch`: a channel with an
`id`, a `type` of `web_hook`, an `address`, and a `token`. Notifications arrive
as a bare `POST` carrying `X-Goog-Channel-ID`, `X-Goog-Resource-ID`,
`X-Goog-Resource-State`, `X-Goog-Channel-Token` and `X-Goog-Message-Number`.

There is no HMAC. Google's model is that the channel token is unguessable and
the address is secret. That means `Receive` has nothing to verify: it can
compare the echoed channel token, which proves only that the sender knew a
value that travelled in a URL. Against the rest of this service — where every
other provider's deliveries are proved with a signature — accepting Drive
notifications unverified is a decision to make explicitly, not a gap to paper
over. Additionally the notification body is empty: it says only *that*
something changed, so an action has to follow up with `changes.list` from a
stored page token.

## What this means for the order of work

The 13 are not 13 equal pieces of work. They fall into three groups:

| Group | Providers | What they need |
| --- | --- | --- |
| Fits the contract as it stands | HubSpot, Atlassian, ClickUp, Airtable, Box, Dropbox, Salesforce | A provider file, actions, a skill and tests, in the existing shape |
| Needs the secret-issuing extension | Asana | The framework change above, then an ordinary provider |
| Needs a decision before any code | Google Drive, Notion, Figma, Intercom | Unverifiable deliveries, or no subscription API at all |

The third group is why this note exists. Google is the single most requested
provider and it is the one whose deliveries cannot be authenticated; shipping
it first would mean either weakening `ErrUnverified` for one provider or
documenting a hole. That should be the user's call, with the trade-off stated,
not a quiet exception.

Confirmation still outstanding for Atlassian, HubSpot, Notion, ClickUp,
Airtable, Box, Dropbox and Salesforce signature details. Nothing above should
be treated as settled for those until their documentation is read the same way
Asana's was.
