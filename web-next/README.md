# Oryxa next frontend

This is the clean TanStack Start app for the new Oryxa interface. The current
`../web` app remains available while the new design and workflows are built.

UI changes must follow [DESIGN.md](DESIGN.md). [AGENTS.md](AGENTS.md) makes
that contract part of the working instructions for this frontend.

## Run locally

```sh
pnpm install
pnpm dev
```

The app runs at `http://localhost:3001`. Requests to `/v1/*` are proxied to
`ORYXA_URL` (default: `http://localhost:8080`). If you open the app at
`http://127.0.0.1:3001`, start Vite with `pnpm dev --host 0.0.0.0`.

The new auth flow uses the backend's password and Google endpoints. Local
`AUTH_RETURN_URLS` and service `*_ALLOWED_ORIGINS` must include both
`http://localhost:3001` and `http://127.0.0.1:3001`; the repository's local
`.env` and `.env.example` now do. Email verification, reset and invitation
links target `http://localhost:3001` in `.env.example`: localhost is the host
of the Google callback, and a Google sign-in started on `127.0.0.1` loses its
state cookie there. Restart the backend after changing these variables. The auth service must be running to actually sign
in; it currently proxies through port 8080.

Only in development, an unavailable auth service leaves the existing workspace
prototype accessible with a Preview badge. A signed-out response still sends
you to sign-in, and production never falls back to the prototype.

## Included

- TanStack Start and file-based TanStack Router
- TanStack Query with request-scoped SSR integration
- TanStack Form and Zod 4 for forms and validation
- Tailwind CSS 4 and shadcn/ui configured with Base UI primitives
- Core dashboard components, Lucide icons, Sonner, and dark theme support
- Markdown rendering and autosizing textareas for future chat screens
- ESLint, Prettier, TypeScript, and Vitest tooling

Add more shadcn components with `pnpm exec shadcn add <component>`.

## Existing code

The previous app's `../web/src/lib` is untouched. Migrate individual clients
and domain models after their API contracts are reviewed; directly copying that
folder would bring its existing coupling and issues into this app.

## Checks

```sh
pnpm typecheck
pnpm lint
pnpm build
```

Deployment configuration and final brand assets will be set when the new app is
ready to replace the current frontend.
