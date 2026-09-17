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
| `/v1/organizations/{id}/apps`, `…/connections`, `/v1/oauth`, `/v1/providers` | connections |
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
  signs in, or creates an account, and comes back to it.
- `/invitations`: the invitations sent to the signed-in member's email, to
  join without the emailed link.
- `/onboarding`: create an organization and invite its first members.
- `/app`: the workspace. It sends a signed-out visitor to sign in, and a member
  without an organization to their invitations, or to onboarding when there
  are none. It remembers the organization last chosen in that browser.
- `/app/teams`: the team tree, each team's people, and your email switch for it.
- `/app/files`: organization and personal files, with uploads and downloads.
- `/app/connections`: connect a tool, reconnect it, or replace its key.
- `/app/settings/organization`: name and handle, and deleting the organization.
  Beside it live `…/members` (directory, roles, invitations), `…/teams` (every
  team and who manages it), `…/tools` (which tools members may connect, from
  connections' provider catalogue),
  `…/email` (what the product sends, and who decides), and `…/billing` (plan,
  member allowance, and checkout).
- `/app/settings/profile`: your profile, your organizations, team call emails,
  and sign-in.
- `/settings/billing`: where billing's `BILLING_URL` sends people after
  checkout. It reopens the organization's billing settings.

Each page offers only what the member's role allows: owners change the
organization, its plan, and roles; admins manage members and every team;
members manage their own connections, files, and email preferences.

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
