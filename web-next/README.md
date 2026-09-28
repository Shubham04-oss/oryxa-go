# Oryxa next frontend

This is the TanStack Start app that serves Oryxa's frontend at
`app.oryxa.in`. The old `../web` app stays deployed only for rollback; see
"Releasing" in `deploy/README.md`.

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
prototype accessible, with inline "Layout preview · ..." notices per section
rather than a header badge, which DESIGN.md forbids. A signed-out response
still sends you to sign-in, and production never falls back to the prototype.

## Chats: the Worker and its Durable Objects

`pnpm dev` runs the app in workerd through the Cloudflare Vite plugin. The
Worker is `src/server.ts` (`main` in `wrangler.jsonc`); it serves the app
through TanStack Start and, beside it, one SQLite-backed Durable Object for
every conversation (REALTIME.md), each an entry in the Worker's table of
realtime objects. The member's primary-agent chat is one object per thread,
`ThreadChatAgent` (`src/thread-chat-agent.ts`), named
`{organization}~{thread}`, as `../web` has had since #82.

- `/agents/thread-chat-agent/{organization}~{thread}` is the page's WebSocket
  (the Agents SDK's `useAgent` and `useAgentChat`), and `.../get-messages` the
  stored transcript. Before routing either, the Worker asks Go whether the
  member may read the thread (`GET /v1/organizations/{org}/threads/{thread}`
  with their cookie), refuses a cross-site socket, and hands the object the
  cookie and ids itself.
- For each message the object POSTs Go's `.../threads/{thread}/runs` as the
  member (their cookie and origin), relays the run's items to the page as
  `data-oryxa` parts and keeps the transcript. A page that reconnects resumes
  a run still streaming; a run is never replayed after the object restarts,
  since its tools may have side effects. Go still owns runs, thread lists and
  artifacts. The right panel's Agents tab is the page's side
  (`src/components/agent-chat.tsx`); `src/lib/threads/README.md` describes
  the contract it follows.
- `DELETE` on the same path, from the app's own origin only, deletes the
  thread in Go and only then clears the object's transcript; a refusal from
  Go leaves both.

The member's chat with one of their specialists is `SpecialistChatAgent`
(`src/specialist-chat-agent.ts`), one object per specialist, named
`{organization}~{agent}`: Go creates a specialist conversation only inside
its first run, so the object keeps the conversation Go names and continues
it. It is served the same way at `/agents/specialist-chat-agent/...`, behind
`GET /v1/organizations/{org}/agents/{agent}` (Go answers only the
specialist's owner). Its `DELETE` starts the chat over and deletes nothing
in Go, which has no route for that. Both objects share the run relay in
`src/chat-relay.ts`.

Direct messages live only in Durable Objects: `DirectMessageAgent`
(`src/direct-message-agent.ts`), one per pair of members,
`{organization}~{userA}~{userB}` with the ids sorted, and `InboxAgent`
(`src/inbox-agent.ts`), each member's conversation list,
`{organization}~{user}`. The Worker serves them at
`/agents/direct-message-agent/...` and `/agents/inbox-agent/...` once Go has
said who the member is (`GET /v1/organizations/{org}`) and, for a DM, that
they are one of its two people and the other is in the organization
(`GET .../members`). Pages use `useDirectMessages` and `useInbox` from
`src/lib/dms`. When a member leaves or an organization is deleted, Go calls
`POST /internal/realtime/purge`, signed with `REALTIME_PURGE_SECRET`, which
empties their conversations. REALTIME.md holds the contract.

The Worker reaches Go at `ORYXA_API_ORIGIN`, which `wrangler.jsonc` sets to
`http://localhost:8080`. To point local development elsewhere, copy
`.dev.vars.example` to `.dev.vars` (gitignored) and restart `pnpm dev`; the
example targets the stand-in backend on `http://127.0.0.1:18096`. `ORYXA_URL`
still decides where the browser's `/v1` calls go, through Vite's proxy, so
keep the two on the same backend. `.dev.vars` also holds
`REALTIME_PURGE_SECRET` (at least 32 characters; `openssl rand -hex 32`),
without which the Worker refuses every purge; deployed, it is a Worker
secret (`wrangler secret put REALTIME_PURGE_SECRET`) holding the value Go
signs with.

web-next serves `app.oryxa.in` as the Worker `oryxa-production-web-next`,
`env.production` in `wrangler.jsonc`; `oryxa-web-next` is the development
Worker. Every release from main deploys it (`.github/workflows/deploy.yml`,
see `deploy/README.md`), built with `CLOUDFLARE_ENV=production pnpm build`.
`pnpm build` followed by `pnpm exec wrangler deploy --dry-run` checks the
bundle without deploying it.

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

[PARITY.md](PARITY.md) maps the old app route by route: what each screen does,
the Go endpoints behind it, where this app stands on it (wired, example data or
missing), a suggested build order and the pitfalls the #83 review found.

## Checks

```sh
pnpm typecheck
pnpm lint
pnpm test
pnpm build
```

Vitest runs in Node without the Cloudflare plugins (`vite.config.ts` leaves
them out under Vitest); the Worker's tests stand in for the Agents SDK.

Deployment configuration exists; see "Chats: the Worker and its Durable
Objects" above and `deploy/README.md`. Final brand assets are still open.
