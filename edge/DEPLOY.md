# Deploying oryxa-edge (Baseline A)

What staging and production need before `AGENTS_PRIMARY_LIVE_CLOUDFLARE_ORGS`
names anyone. The edge's own config is in `wrangler.jsonc`; the steps below
are the owner's and macbookpro's (F4, A9): Cloudflare and AWS changes,
secrets, CI and routes. Nothing here has been applied yet.

## 1. Cloudflare account

- **Workers Paid**, with **Containers** and **Browser Run** enabled. The
  sandbox is a Durable Object–managed container (`containers` in
  `wrangler.jsonc`); the browser is the `BROWSER` binding.
- **R2 buckets** `oryxa-runs` and `oryxa-runs-staging`. Lifecycle rules
  (until Layer C's retention decision):
  - `runs/*/*/*/_room/media/sandbox-*/`: sandbox workspace backups. They're
    replaced on every idle stop; expire after 30 days.
  - Everything else under `runs/` and `artifacts/`: keep.

## 2. The edge Worker

| Setting | Staging (`--env staging`) | Production |
|---|---|---|
| Route | `edge-staging.oryxa.in/*` | `edge.oryxa.in/*` |
| `EDGE_PUBLIC_URL` (var) | `https://edge-staging.oryxa.in` | `https://edge.oryxa.in` |
| `AGENTS_TOKEN_PUBLIC_KEYS` (var) | `["<base64 DER SPKI>"]` of staging agents' `AGENTS_PRIMARY_SERVICE_KEY_ID` | the same for production's key |
| `EDGE_URL_SIGNING_KEY` (secret) | 32+ random bytes, `wrangler secret put EDGE_URL_SIGNING_KEY --env staging` | its own value |

The public key for `AGENTS_TOKEN_PUBLIC_KEYS`:

```sh
aws kms get-public-key --key-id "$AGENTS_PRIMARY_SERVICE_KEY_ID" --query PublicKey --output text
```

That's already base64 DER SPKI. During a key rotation, list both keys.

**Deploy:** `pnpm exec wrangler deploy [--env staging]` from `edge/`. It
builds `containers/sandbox/Dockerfile` for linux/amd64 and pushes it to
Cloudflare's registry, so it has to run on an amd64 runner with Docker (the
a1 runners are arm64; CI's dry run skips the image with
`--containers-rollout=none`). The first deploy takes a few minutes while the
image builds.

**Previews' origin (open):** a preview serves pages the sandbox made, so it
must not share cookies with Oryxa. On `edge.oryxa.in` it would see any
cookie set for `.oryxa.in`. Before members open previews, check that
nothing sets `Domain=.oryxa.in` cookies, or move previews to their own
registrable domain (Sandbox SDK's "serve previews on their own hostnames").

**WAF:** the edge's own zone needs no `/v1/internal` rule (it has no such
routes). Preview links (`/p/…`) and terminals (`/t/…`) are reached by
members' browsers; everything else only by agents.

## 3. agents (ECS)

| Variable | Value |
|---|---|
| `AGENTS_PRIMARY_EDGE_URL` | `https://edge.oryxa.in` (staging: `https://edge-staging.oryxa.in`) |
| `AGENTS_PRIMARY_LIVE_CLOUDFLARE_ORGS` | staging's org first, then ours, then set `AGENTS_PRIMARY_LIVE_BACKEND=cloudflare` (A11) |
| `AGENTS_PRIMARY_SERVICE_KEY_ID` | already set; it now also signs edge tokens |
| `AGENTS_PRIMARY_ARTIFACT_ENDPOINT` | `https://<account>.r2.cloudflarestorage.com` (optional; moves artifacts to R2) |
| `AGENTS_PRIMARY_ARTIFACT_ACCESS_KEY_ID`, `…_SECRET_ACCESS_KEY` (secrets) | an R2 API token with Object Read & Write on `oryxa-runs` only |

agents' task role needs `kms:Sign` on the service key; it already has it
for its auth tokens.

## 4. Checking it

From any machine with the staging key's signing rights:

```sh
EDGE_URL=https://edge-staging.oryxa.in \
EDGE_SIGNING_KEY_PEM="…a key staging's AGENTS_TOKEN_PUBLIC_KEYS trusts…" \
EDGE_EXPECT_SANDBOX=1 EDGE_EXPECT_BROWSER=1 \
go test ./agents/primary/internal/edgelive -count=1 -v
```

For this check, add a dedicated test key's public half to staging's
`AGENTS_TOKEN_PUBLIC_KEYS` beside agents' KMS key, rather than handing out
KMS signing. Never add one to production.

Then, in our org with `AGENTS_PRIMARY_LIVE_CLOUDFLARE_ORGS` set: ask Krude
to run Python, write and read a file, take a screenshot, and browse to a
page. The room's log at `runs/{org}/thread/{session}/…` in R2 should hold
each step.
