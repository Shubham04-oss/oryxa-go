# The Weirdly ODD web

The Cloudflare-hosted product interface for The Weirdly ODD. It uses TanStack
Start, React, TypeScript, TanStack Query, Zod, Tailwind CSS, and the Cloudflare
Vite plugin.

## Local development

```sh
cp .env.example .env
pnpm install
pnpm dev
```

The app calls every service under `/v1` on its own origin, so session cookies
stay first-party and no service needs CORS. `pnpm dev` proxies those paths to
the local services:

| Path | Service |
| --- | --- |
| `/v1/organizations/{id}/teams`, `…/tools`, `…/members/search` | teams |
| `/v1/organizations/{id}/files` | files |
| `/v1/organizations/{id}/apps`, `…/connections`, `/v1/oauth` | connections |
| `/v1/billing` | billing |
| Everything else under `/v1` | auth |

The `*_SERVICE_URL` variables in `.env` say where each service runs. A
deployment's edge must route `/v1` the same way; set `VITE_API_URL` only to
send API calls to another origin. Auth accepts browser requests only from its
`AUTH_RETURN_URLS` origins, and the other services check cookie-authenticated
changes against their `*_ALLOWED_ORIGINS`, so keep the app on
`http://localhost:3000`.

## Routes

- `/auth/sign-in` and `/auth/sign-up`.
- `/auth/verify-email`, `/auth/forgot-password` and `/auth/reset-password`:
  auth's `EMAIL_VERIFICATION_URL` and `PASSWORD_RESET_URL` links open these.
- `/invitations/accept`: auth's `INVITATION_ACCEPT_URL`. A signed-out visitor
  signs in first and comes back to it.
- `/onboarding`: create an organization and invite its first members.
- `/app`: the workspace. It sends a signed-out visitor to sign in and a member
  without an organization to onboarding, and remembers the organization last
  chosen in that browser.

API calls refresh an expired access token once and retry before giving up.

## Checks

```sh
pnpm check
pnpm typecheck
pnpm test
pnpm build
```

## Cloudflare

`wrangler.jsonc` configures the application as `the-weirdly-odd-web`. Preview
the production build locally with `pnpm preview`; deploy with `pnpm deploy`
after Cloudflare authentication and environment configuration are in place.
