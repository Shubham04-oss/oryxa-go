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
stay first-party and no service needs CORS. Every service runs in one `oryxa`
process, so `pnpm dev` proxies all of `/v1` to the address `ORYXA_URL` names
(`http://localhost:8080` by default); start it from the repository root with
`go run ./cmd/oryxa`. A deployment's edge must send `/v1` to it the same way;
set `VITE_API_URL` only to send API calls to another origin. Auth accepts
browser requests only from its `AUTH_RETURN_URLS` origins, and the other
services check cookie-authenticated changes against their `*_ALLOWED_ORIGINS`,
so keep the app on `http://localhost:3000`.

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
- `/app/agents`: a tabbed workspace for the member's primary agent,
  specialists, agent teams, durable workflows, and audited cross-user requests.
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
