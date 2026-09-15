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

`VITE_AUTH_API_URL` points browser auth requests at the public auth service.
It defaults to `http://localhost:8080` during local development and to the
current origin in production.

The initial routes are:

- `/auth/sign-in`
- `/auth/sign-up`

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
