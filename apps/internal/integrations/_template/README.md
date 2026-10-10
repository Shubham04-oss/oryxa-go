# An integration

One toolkit's own integration: how it signs a member in, how its webhooks are
verified, and the tools, readers and events it offers. Copy this directory,
rename the package to the toolkit's slug, and fill in what the provider's
documentation says.

The directory is named with a leading underscore so the go tool ignores it.
Nothing is compiled from it, and a copy is the first thing a worker does with
it. The template's own test is run from `apps/internal/integrations`, so a
broken template is caught before it is copied six times.

## What is in here

| File | What it is |
|---|---|
| `toolkit.go` | the manifest — its Auth and Webhook are frozen, everything else is yours — and the tools |
| `runtime.go` | the reader, the polled and pushed events, and the one helper every call goes through |
| `toolkit_test.go` | the whole conformance check, which is one line of the harness |
| `testdata/` | the cassettes, built from the provider's own documented answers |

## The rules

**Auth and Webhook are frozen.** Connections have already been made against
them, so a change to either is a change every connection made so far has to
live with. A copy keeps whatever the toolkit's own package already declares
there. Need a credential kind or a webhook scheme that is not in
`apps/internal/integration`? Stop and say so in the PR: the main session
implements it once, in `signin` or `hooks`, with conformance tests.

**The credential is never yours.** Build the SDK client on `Session.HTTP` and
the core places the credential in the innermost transport, below anything that
can see it, and only for the credential's own host. Never set `Authorization`
or `Cookie`, and never import `golang.org/x/oauth2`. Use `Session.Token` only
if the SDK has no client hook, which is slack-go's case.

**A read tool cannot write.** A read tool's session refuses every method but
GET, HEAD and the POSTs in its own `read_posts`, before anything is sent. A
write tool takes the caller's idempotency key, which arrives in the context.

**Four refusals, four answers.** 401 is `integration.ErrNeedsReconnect`, 429 is
`*integration.RateLimited`, 404 is `integration.ErrNotFound`, and anything else
is `*integration.AppError` carrying the app's own words. Those four are the
only shapes the core acts on, and an error that is none of them reaches the
caller as a failure it cannot do anything about.

**Never log a Session, an argument or a result.** A cassette must not hold a
credential either: `cmd/check` scans `testdata/` for the shapes one takes, and
a recording made from a live account is exactly what it is looking for.

**A page is under 6 MiB.** A reader uses the provider's own paging, and takes
fewer items rather than returning a page too large. An incremental pass reads
by watermark or token; there is no `Held`, and knowledge's content hash keeps a
document that has not changed from being published twice.

**The first poll answers no events and a cursor.** A member who subscribes to
an event starts hearing about it from then on, rather than being handed
everything the app has ever recorded. Inventing a cursor is how a poll would
skip events, so a poll that has nothing to report leaves the cursor alone.

**Push: Subscribe registers, Parse only reads.** `Subscribe` is handed
`hook.URL` and `hook.Secret` and returns the app's own id for the subscription.
`Parse` sees only deliveries the core has already verified: the signature is
checked before it is called, and its `Authorization` and `Cookie` headers are
gone.

## Names and scopes

Every tool and event name begins with the toolkit's slug in upper case, as
`GITHUB_LIST_ISSUES` does. Agents' skill files read tools by exactly those
names, so a name that changes is a member's breakage rather than a cleanup.

Every scope the sign-in asks for is used by something, and nothing asks for a
scope the sign-in does not grant. A scope nobody uses is one a member is asked
to grant for nothing.

## Before the PR

```
go test ./apps/internal/integrations/<toolkit>/...
go run ./apps/internal/integration/cmd/check <toolkit>
```

The first runs the harness. The second is gate G1: the manifest and its
prefixes, the scopes, the imports, a secret scan of `testdata/`, and the diff
against the branch you branched from.

Then a reviewer reads the read and write marks, the destructive ones, the
paging and resumption, the error mapping and the payload schemas. That is gate
G2, and it is the one this template cannot check for you.

In the PR, say what each tool is (read or write, and the scope it needs), what
each reader indexes, what each event is, and paste the check's output.
