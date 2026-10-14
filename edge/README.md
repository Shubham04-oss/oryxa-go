# oryxa-edge

The live layer on Cloudflare: one Durable Object per room (`RoomDO`) that owns
the room's log and the sandboxes and browsers it runs. The plan is
[docs/design/live-layer](../docs/design/live-layer/README.md); the wire
contract is [01-contracts.md](../docs/design/live-layer/01-contracts.md), as
code in `src/contracts` and Go's `contracts/edge/v1`.

## Who calls it

- **Go agents** call `/v1/rooms/{kind}/{id}/…` over HTTPS with an edge token
  (`Authorization: Bearer …`), an ES256 JWT agents' serviceauth KMS key signs
  per call. `src/router.ts` verifies it; the room checks its scopes and
  refuses a reused `jti`.
- **web-next** calls the `WebEdge` entrypoint through a service binding,
  naming the member it has already authenticated in `x-oryxa-member`
  (`{"org","member"}`). The room checks they are a member.

## Layout

| Path | What |
|---|---|
| `src/contracts` | zod schemas twinned with `contracts/edge/v1` |
| `src/auth/token.ts` | edge token verification |
| `src/router.ts`, `src/web.ts` | the two ways in |
| `src/room` | `RoomDO`: identity, members, replay, log, live sockets, R2 flush, children |
| `src/sandbox`, `src/browser`, `src/voice` | the children |

## Configuration

| Name | What |
|---|---|
| `AGENTS_TOKEN_PUBLIC_KEYS` | JSON array of base64 DER SPKI public keys allowed to sign edge tokens (agents' `ServiceKeyID`, from `aws kms get-public-key`) |
| `RUNS` | the `oryxa-runs` R2 bucket (`oryxa-runs-staging` in `--env staging`) |

## Develop

```sh
corepack pnpm@12.4.2 install
corepack pnpm@12.4.2 test        # contracts (Node) + workers (workerd)
corepack pnpm@12.4.2 typecheck
corepack pnpm@12.4.2 exec wrangler dev
```

After a change to the token claims, re-sign the shared fixture:
`go test ./internal/serviceauth -run TestEdgeTokenFixture -update`.
