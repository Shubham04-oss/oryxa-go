# web/ → web-next/ parity and build guide

Mapped 2026-09-23 from `main`: `web/` with #82's Durable Object thread chat, and `web-next/` as merged in #83 with its review fixes. Every endpoint below was checked against the services' `*/internal/httpserver/server.go`. Update the status columns as surfaces get built.

## Summary

web-next today is a **single-page workspace** (`web-next/src/routes/index.tsx` plus the auth/invitation routes) built around one `WorkspaceShell` component, not the ~30-route app `web/` is. Of the old app's ~25 member-facing capability areas, **about 16 are wired to real APIs** in web-next: all of auth (sign-in/up, forgot/reset, verify, Google), invitation accept, an onboarding-replacement (create-workspace only), and — inside the Settings overlay — People & invitations, Organization profile, Teams, Tool access (read-only), Billing, and Account (password reset, sign-out, agent grant, app-update preferences, team-call emails), plus the Home → Tools canvas (browse catalog, connect, the whole connection lifecycle, events/triggers, indexing sources and their kinds, sync runs), Home → Knowledge bases (corpora, documents, add and remove, ask with citations), Team → Teamspaces (list, create, pause/resume, delete, and the room with its overview, activity and files), Agents mode (the agent overview, specialists and multi-agent teams with their room; workflows read-only, requests as a count), the primary-agent chat in the right panel's Agents tab, through the `ThreadChatAgent` Durable Object (no history list or delete control yet), each specialist's chat through its `SpecialistChatAgent` (the specialist's focused view and the Agents tab's quick switch), direct messages between any two members of the organization (Team → People, Team's recent conversations and the right panel's DMs tab, on the `DirectMessageAgent` and `InboxAgent` Durable Objects), and teamspace side conversations (two teammates and both their agents, in the right panel from a teamspace page's Agents tab or the DMs tab, on the `SideConversationAgent`). **One area still shows nothing real**: Home's overview (an empty canvas, with three saved-view labels that open nothing; its design is the user's call). **Several areas are absent or partial**: editing and running agent workflows (read-only on the overview), cross-agent interaction requests (a count only: inbox/sent/decide/respond are missing), Files, and the ⌘K command palette. The most important gaps to close, in rough priority order: (1) the primary-agent chat has no history list or delete control yet; (2) Files has no web-next surface at all; (3) workflow editing, agent requests and ⌘K search are absent; (4) Home's overview needs its design. Tools' dead ends (no disconnect or reconnect, a pending connection silently hiding its app's Connect button, Composio's return ignored) are closed. (Tools' raw fetch and first-organization bug (Tools now reads only through `lib/apps` and `lib/knowledge`), the reset-password statuses, the onboarding slug and the StrictMode verify hang were fixed in #83; §6 keeps them as pitfalls.)

---

## 1. `web/` — route by route

Routes are TanStack Start flat file routes under `web/src/routes/`; URL = filename with `.` → `/` and `$param` → `:param`. Every route runs behind `src/routes/__root.tsx` (global `TooltipProvider`, title "The Weirdly ODD") and `src/router.tsx` (`defaultPreload: "intent"` — links prefetch on hover/focus). All API calls go through `web/src/lib/api/client.ts`'s `apiFetch`/`apiRequest` (cookie auth, silent refresh-and-retry-once on 401, `VITE_API_URL`-relative).

### Shell, root and the Durable Object thread socket

- **`src/server.ts`** (Cloudflare Worker entry): routes `^/agents/thread-chat-agent/([^/]+)(?:/get-messages)?/?$` to the DO path; everything else falls through to the TanStack Start SSR handler. For a matched path: validates `Origin`, requires a `Cookie`, and does a server-side `GET /v1/organizations/{orgId}/threads/{threadId}` as an auth pre-check (mapping Go's `401/403/404/503` through, else `502`). **DELETE** (thread deletion): POSTs `{pathname}/purge` to the named `ThreadChatAgent` Durable Object (header `X-Oryxa-Internal-Purge: 1`) to wipe its SQLite history first, then forwards the real `DELETE /v1/organizations/{id}/threads/{threadId}` to Go. **GET/WebSocket**: injects `X-Oryxa-Cookie`/`X-Oryxa-Organization`/`X-Oryxa-Thread` headers and hands off to the DO via `routeAgentRequest` (Cloudflare `agents` package); a `get-messages` sub-path is HTTP-only (no WS upgrade) and serves the DO's own stored history.
- **`src/thread-chat-agent.ts`** (`ThreadChatAgent`, extends `AIChatAgent`, SQLite-backed DO): `onRequest` handles only the internal `/purge` (resets state + deletes rows). `onChatRecovery` returns `{continue:false}` — an interrupted Go run is **not** replayed after a DO restart (tools may have side effects). `sanitizeMessageForPersistence` strips superseded streaming deltas before persisting. `onConnect` reads the three injected headers and closes with code 1008 if any is missing. `onChatMessage` builds a `RunInput` from the message/confirmations, **POSTs from the Worker itself** to `env.ORYXA_API_ORIGIN` at `POST /v1/organizations/{id}/threads/{threadId}/runs`, parses the SSE body with `createEventStreamReader()`, and re-emits each item as a `data-oryxa` UI-message part (skipping the member's own echoed message). A Go `401` becomes `code: sessionExpiredCode` — the signal the route watches to silently retry once after refreshing the session; other codes it maps to copy: `run_in_progress`, `confirmation_pending`, `agents_unavailable`.
- **`src/routes/__root.tsx` / `router.tsx`**: global shell; SSR + TanStack Query integration; no route-level role gating here — every gate lives per-page.

### Auth

| Route (file) | What the member does | API | Notable |
|---|---|---|---|
| `/auth/sign-in` (`auth.sign-in.tsx`) | Email+password sign-in, or Google (when not `passwordOnlyDemo`); `redirect` search param round-trips a return-to path | `POST /v1/auth/password/login`; Google is a full-page nav to `GET /v1/auth/login?return_to=` | 401 → "Email or password is incorrect."; success clears the query cache before navigating |
| `/auth/sign-up` (`auth.sign-up.tsx`) | Name/email/password sign-up | `POST /v1/auth/password/signup` | Success shows "check your email"; local Mailhog link in `passwordOnlyDemo` |
| `/auth/forgot-password` (`auth.forgot-password.tsx`) | Request a reset link | `POST /v1/auth/password/forgot` | Always shows the same success copy regardless of whether the email exists (enumeration-safe) |
| `/auth/reset-password` (`auth.reset-password.tsx`) | Set a new password from an emailed token | `POST /v1/auth/password/reset` | Maps `410→"expired"`, `404→"invalid"`, else generic error — **see Pitfalls, §6**; success warns other sessions are signed out |
| `/auth/verify-email` (`auth.verify-email.tsx`) | Redeem an email-verification token; can resend | `POST /v1/auth/password/verify`; resend `POST /v1/auth/password/resend-verification` | `failedVerification`: any 5xx/non-`ApiError` → "unavailable" (retryable), else `410→expired`, else `invalid`. A `useRef` guard stops the same token being redeemed twice, but together with its `active` cleanup flag it drops the only result under StrictMode — **web-next copied it and hung; see §6 for the fixed pattern** |

Shared: `components/auth/auth-shell.tsx` (page chrome), `form-field.tsx` (labeled input + password-reveal toggle), `google-button.tsx` (full-page redirect, not a fetch).

### Onboarding

- **`/onboarding`** (`onboarding.tsx`) — `beforeLoad` requires a session only (`ssr:false`, `ensureSession`); reachable any time (e.g. sidebar "Create organization"), not just for new members. A 4-step client-state wizard: intro → name the organization (`organizationSchema`) → add members (repeatable email+role rows, local list, "Skip for now") → complete. On finish: `POST /v1/organizations` (`{name, slug:""}` — server derives the slug), then `POST /v1/organizations/{id}/invitations` per queued row (marks each `sent` so a retry doesn't resend), then `selectOrganization` (localStorage only) + `reloadSession`. Idempotent-ish: won't recreate the org if one already succeeded; per-invite `sent` guard prevents double-sends on retry. No role gating — any signed-in member may create an org here.

### Invitations

- **`/invitations`** (`invitations.index.tsx`) — the signed-in member's own inbox of received invitations (`GET /v1/invitations`), each with "Join" (`POST /v1/invitations/{id}/accept`). `beforeLoad` redirects here from `/app` when the member has zero orgs and zero pending invitations it redirects onward to `/onboarding`. `402` → member-limit copy; `{404,409,410}` → "no longer open" (and invalidates the list so the row disappears).
- **`/invitations/accept`** (`invitations.accept.tsx`) — the direct emailed-link flow (`?token=`), independent of the inbox above. `POST /v1/invitations/accept`. Distinct error screens: `402` full, `403` wrong signed-in account, `{404,409}` invalid, `410` expired.

### Top-level redirects

- **`/`** (`index.tsx`) unconditionally redirects to `/app`.
- **`/settings/billing`** (`settings.billing.tsx`) unconditionally redirects to `/app/settings/organization/billing` — kept only because Dodo's `BILLING_URL` and billing emails are configured to point at this exact path.

### App shell & navigation (`app.tsx` layout + `components/app/*`)

`app.tsx` guards every `/app/*` page: redirects signed-out visitors to sign-in, and members with zero organizations to `/invitations`/`/onboarding`. Renders `AppShell` → `WorkspaceSearchProvider` + shadcn `SidebarProvider` + `AppSidebar`. `AppSidebar` swaps between `WorkspaceSidebar` (wordmark, `OrganizationSwitcher`, Solo/Collaborate/Resources nav groups from `navigation.ts`, `ConversationsMenu`, `AccountMenu`) and `OrganizationSettingsSidebar` under `/app/settings/organization/*`. Nav items are **not role-filtered** — every item shows to every member; individual pages gate their own controls. `workspace-search.tsx` is the ⌘K command palette (scopes: `"conversations"` or `"everything"` across threads + every nav item) — **web-next has no equivalent**. `confirm-dialog.tsx` is the one reusable destructive-action pattern used everywhere (optional type-to-confirm gate). `thread-composer.tsx` is the shared autosize composer used by Home, the thread page, `AgentTeamRoom`, `TeamspaceRoom`, and `SpecialistCreationDialog`.

- **`/app`** (`app.index.tsx` → `WorkspaceHome`) — a launchpad composer (submit creates a thread via `POST /v1/organizations/{id}/threads`, stashes the first message in an in-memory `Map` via `stashPendingMessage`, then navigates to the new thread, which sends it on mount); 3 static starter-prompt buttons (client-only prefill); "Work modes" tiles (Solo/Agents/Teamwork) with live counts from `GET .../agents`, `.../agent-teams`, `.../agent-workflows`, `.../teamspaces`; "Recent" = top 3 threads from `GET .../threads`.

### Conversations / threads

- **`/app/threads`** (`app.threads.tsx`) — layout only; bare `/app/threads` redirects to `/app` (no index page; threads live in the sidebar/search).
- **`/app/threads/:threadId`** (`app.threads.$threadId.tsx`, 625 lines, the most complex route) — the primary-agent conversation. Opens the `ThreadChatAgent` DO via `useAgent({agent:"ThreadChatAgent", name: threadAgentName(orgId,threadId)})` + `useAgentChat`. History load: `getSession()` (refresh cookie) → DO's own `GET {socketUrl}/get-messages` (SQLite) **and** `GET /v1/organizations/{id}/threads/{threadId}` (Go's authoritative log) → reconciled (`reconcileChatMessages`). A `run_failed` item with `code:"session_expired"` triggers one silent retry (`resendWithFreshSession`: refresh session → `reopenSocket` → `regenerate`) rather than an error. Sending: `chat.sendMessage(...)` over the socket (→ DO → Go `POST .../runs`); confirmation answers sent the same way with `metadata:{oryxaConfirmation:true}`. Delete: `ConfirmDialog` → `DELETE /agents/thread-chat-agent/{org}~{thread}` (purges DO, then Go delete) → back to `/app`. Streaming is WebSocket-via-DO, not raw browser SSE; artifacts render as image thumbnail or download link via `GET .../threads/{id}/artifacts/{name}?version=` (plain `<a>/<img>`, cookie-authed, no refresh protection); a distinct "From your apps" digest message (author `"apps"`) collapses after 4 lines. No per-thread role gating — org membership is the boundary. Uses `components/threads/message-markdown.tsx` (react-markdown + remark-gfm, `target=_blank rel=noopener`, never `dangerouslySetInnerHTML`) and `tool-activity.tsx` (groups consecutive tool-call entries, animated tool-name label, state icon running/waiting_approval/failed/declined/done).

### Agents

- **`/app/agents`** (`app.agents.tsx` → `src/features/agents/agent-system-page.tsx`) — `?tab=overview|specialists|teams|workflows|requests`, `?box=inbox|sent`. **Overview**: primary-agent identity card + edit; 4 metric tiles; static "operating model" diagram; first 4 inbox requests. **Specialists**: searchable directory + inspector (Active/Paused switch, "Talk" opens `SpecialistConversationDialog`, edit, name-typed delete). Creation is a guided chat (`SpecialistCreationDialog`) that talks to Krude via the *same* primary-agent HTTP+SSE run APIs (`createThread`/`startRun`/`getThread` — **not** the WebSocket DO) with a fixed instruction prefix, detecting success by watching the timeline for a completed `agent_system_create_agent` tool call — **`POST /v1/organizations/{id}/agents` is never called directly by the frontend**. **Teams**: directory + detail with `AgentTeamRoom` (group chat: `GET/POST .../agent-teams/{id}/messages`, optimistic merge by `seq`, `@handle` mentions, composer disabled unless team is active). **Workflows**: CRUD + "Run now" (`POST .../executions`) + lazy "Runs" history (`GET .../executions?limit=20`). **Requests**: Inbox/Sent tabs (`GET .../agent-requests/{box}?limit=50`), decide (`POST .../decision`) / respond (`POST .../response`). No owner/admin gating observed anywhere on this route — scoped to the caller's own membership server-side, not by UI role check. Components: `components/agents/agent-dialogs.tsx` (`AgentDialog`/`AgentTeamDialog`/`AgentWorkflowDialog`), `agent-team-room.tsx` (`AgentTeamRoom`), `specialist-conversation-dialog.tsx`, `specialist-creation-dialog.tsx`, `specialist-identity.tsx` (`SpecialistAvatar`, seeded by agent id so it survives renames), plus the shared `WorkspacePage`/`ConfirmDialog`/`EmptyState`/`StatusBadge`/`TabNav`.

### Teamspaces & rooms

- **`/app/teamspaces`** (`app.teamspaces.tsx` → `src/features/teamspaces/teamspaces-page.tsx`, `?space=`) — list + detail (`TeamspaceDetail`): status badge, Settings (if `can_manage`), "Share work" (gated by `canPublishSharedWork` = member + active space + active org-agent + ≥1 author), the **room** (`TeamspaceRoom`, only if `is_member`), whisperers grid, contributed specialists (+ delegation-mode picker), shared work ledger, delete (if `can_manage`). Below: an "Organization Pulse" card from the org-wide synthesis agent (`GET .../organization-agent`, `.../context`, `.../follow-ups`). Room uses `lib/rooms/client.ts`: `GET .../teamspaces/{id}/messages?limit=200` **polled every 5s** (reading the room is explicitly how it learns the member is present — presence lapses after ~90s of silence), `POST .../messages`, `GET .../catchup?after=`. Sending is a blocking mutation, not optimistic (a room reply can come from any agent). Components: `components/teamspaces/teamspace-dialogs.tsx` (`CreateTeamspaceDialog`, `ContributeAgentDialog`, `PublishEntryDialog`, `OrganizationAgentDialog`, `EditTeamspaceDialog`), `teamspace-room.tsx` (`TeamspaceRoom`).

### Teams

- **`/app/teams`** (`app.teams.tsx`, `?team=`) — indented team tree (built client-side from a flat list + `parent_id`) + detail panel (members, role select, "email me when this team is called" switch, add/remove/leave). `canCreate` (top-level) = org admin or manages any team; per-team `can_manage` gates rename/nested-create/delete/add/role-edit/remove; a member can always leave regardless of `can_manage`. 409 on role-change/remove → "A team always needs an admin." Components: `components/teams/team-dialogs.tsx` (`CreateTeamDialog`, `RenameTeamDialog`, `AddTeamMemberDialog` — debounced search against `GET .../members/search`).

### Knowledge

- **`/app/knowledge`** (`app.knowledge.tsx` layout → `app.knowledge.index.tsx`) — grid of corpora (`GET .../corpora`) with scope label, doc/chunk counts, "from connected apps" hint; empty state links to Connections.
- **`/app/knowledge/:corpusId`** (`app.knowledge.$corpusId.tsx`) — corpus detail + document list, **manual** "Load more" cursor pagination (`GET .../corpora/{id}/documents?cursor=&limit=`, `useInfiniteQuery`), status badge `Pending|Ingesting…|Ready|Failed`. No client-side role gating (visibility is scoped server-side).

### Files

- **`/app/files`** (`app.files.tsx`, `?team=&view=`) — tab (Everything/Mine/Teams/Organization) + team select; list with content-type icon, expiry badge (warning ≤3 days), download, delete. `GET .../files?scope=&team_id=`; upload is multipart `POST .../files`; delete `DELETE .../files/{id}`; download is a plain `<a>` to `fileContentUrl` (`GET .../files/{id}/content`) — **the `download-url` presigned endpoint is not used**; `getFile` is called once before download purely to force a session-refresh opportunity the anchor navigation itself can't retry. `isOrgAdmin` controls whether "Organization" is offered as an upload scope; `canDeleteFile` = org admin, or (member scope) owner, or (team scope) uploader/team manager. Max 8 MB, retention `{7,30,90}` days. Components: `components/files/upload-file-dialog.tsx` (`UploadFileDialog` — scope/team/retention selects, client-side size pre-check).

### Apps & connections

- **`/app/connections`** (`app.connections.tsx`, `?app_connection=&status=`) — grid of the **member's own** connections; expandable detail with "Wake my agents when…" (`ConnectionTriggers`) and, if the toolkit supports source indexing, "Searchable knowledge" (`ConnectionSources`). Composio's OAuth-style return trip lands back here via the search params, parsed once (`useRef` guard), shown as a toast-like alert, then cleared. `GET .../apps/connections[?scope=&toolkit=]`, `GET/DELETE .../apps/connections/{id}`, `POST .../apps/connections` (returns `redirect_url` → `window.location.assign`, used for both connect and reconnect), `GET .../apps/connections/{id}/targets`. No role gating — connections are explicitly member-owned; the read-only org-wide rollup lives at `/app/settings/organization/tools`. Components: `components/apps/connect-app-dialog.tsx` (debounced catalog search, `limit=40`), `connection-sources.tsx` (`ConnectionSources`/`KindsDialog`/`RunsDialog`/`IndexSourceDialog`), `connection-state.tsx`, `connection-triggers.tsx` (`ConnectionTriggers`/`AddTriggerDialog`, dynamic config form per trigger's `config_schema`), `toolkit-icon.tsx`.

### Settings

`/app/settings` (`app.settings.tsx`) is a pure-outlet layout with no content of its own — everything below hangs off it.

- **`/app/settings/profile`** (`app.settings.profile.tsx`) — read-only name/email; **Organizations** (every org, role, "Leave" with 409→"you're its only owner" copy); **Team call emails** (per-team switch, only for teams the member is on); **Updates from your apps** (single switch, `PUT .../preferences`); **Background agent access** (`GET/POST/DELETE /v1/agent-grant*`, 404 treated as "not available on this server", not an error); **Sign-in** (self-service reset-password send, sign out). Entirely self-service — no role gating.
- **`/app/settings/organization`** (layout, no route-level role check — every sub-page decides its own gating). All sub-pages share `components/app/organization-settings.tsx` (`SettingsSection`/`SettingsRow`/`SettingsPageBody`/`StatusBadge`/`SettingsListSkeleton`/`SettingsListEmpty`):
  - **index** (`.../organization`) — name/handle, editable only by `isOwner`; "Delete organization" (typed-name confirm) shown only to owners.
  - **`.../members`** — Directory (all members, footer shows plan's member-limit via a billing-summary read) + Pending invitations (owners/admins only), via `components/app/invite-member-dialog.tsx` (`InviteMemberDialog`, role select disabled/relabelled for admins: "Admins invite members; owners invite admins"). Invite requires `managesMembers` (owner/admin); role `<select>` (vs static badge) shown to **owners only**; `canRemoveMember`/`canRevokeInvitation` = owner, or admin acting on a plain member; a 409 on invite maps to "already a member or has a pending invitation."
  - **`.../teams`** — read-only flattened index of every visible team, linking to `/app/teams` for management; "New team" (top-level) only for org admins.
  - **`.../tools`** — admins/owners see a read-only rollup of **organization-scope** connections grouped by toolkit (`GET .../apps/connections?scope=organization`); non-admins see a placeholder pointing at `/app/connections`. No mutations on this page either way.
  - **`.../email`** — **fully static**, no queries or mutations, just explanatory copy + links to Billing/profile settings.
  - **`.../billing`** — plan/status/subscription summary (`GET /v1/billing/organizations/{id}`), plan list with checkout (`POST .../checkout`) gated by `summary.can_manage && !hasOpenSubscription`, "Manage billing" opens the Dodo portal (`POST .../portal`). `503`→"not set up yet", `404`→"no billing account yet."

---

## 2. `web/src/lib/*` — the API client layer web-next will port

`api/client.ts` is the one place fetch/cookie/refresh logic lives — every other module calls `apiRequest`/`apiFetch`. Auth: cookies only (`credentials:"include"`), never a bearer header. **Refresh**: a 401 (when `refreshSession` isn't `false`) triggers one shared `POST /v1/auth/refresh`, deduped across concurrent callers via a module-level `pendingRefresh` promise (Auth rotates the refresh token on every use, so concurrent expiries must share one refresh, not race each other with it), then retries the original request once. `refreshSession:false` (`publicAuth`) is used on every unauthenticated auth call so a 401 there is treated as an answer, not an expired session. Org scope is **always a URL path segment** (`/v1/organizations/{id}/...`), built via an `apiPath` tagged template that encodes only the interpolated values — never a header or query param, and no exceptions found across any of the 11 domain modules read. Error bodies are `{error:"msg"}` (auth/teams/files/billing) or `{error:{code,message}}` (apps/connections); `ApiError.code` is populated only for the latter. Three near-duplicate humanizers exist — `apiErrorMessage` (generic, status-table + sentence-casing), `authErrorMessage` (auth-specific, no casing, raw message on unmapped statuses), `appsErrorMessage` (branches on `ApiError.code`, table: `choose_connection`/`conflict`/`needs_reconnect`/`not_connectable`/`not_connected`/`rate_limited`/`unavailable`) — worth converging into one configurable helper rather than porting all three.

Request/response columns give the key fields only — see the source file for full types.

**`auth/client.ts`**

| Function | Endpoint | Request → response |
|---|---|---|
| `signIn` | `POST /v1/auth/password/login` | `{email,password}` → void |
| `signUp` | `POST /v1/auth/password/signup` | `{display_name,email,password}` → void |
| `signOut` | `POST /v1/auth/logout` | — → void |
| `verifyEmail` | `POST /v1/auth/password/verify` | `{token}` → void |
| `resendVerification` | `POST /v1/auth/password/resend-verification` | `{email}` → void |
| `requestPasswordReset` | `POST /v1/auth/password/forgot` | `{email}` → void |
| `resetPassword` | `POST /v1/auth/password/reset` | `{token,password}` → void. **Zero status branching in the client itself** — the `410/404` mapping lives in the route component (bug, see §6) |
| `getSession` | `GET /v1/auth/me` | — → `Session{user,organizations[]}` |
| `createOrganization` | `POST /v1/organizations` | `{name,slug:""}` → `Organization` |
| `getOrganization` | `GET /v1/organizations/{id}` | — → `{organization,membership}` |
| `updateOrganization` | `PATCH /v1/organizations/{id}` | `{name,slug}` → `Organization` |
| `deleteOrganization` | `DELETE /v1/organizations/{id}` | — → void |
| `listMembers` | `GET .../members` | — → `Membership[]` |
| `changeMemberRole` | `PATCH .../members/{uid}` | `{role}` → `Membership` |
| `removeMember` | `DELETE .../members/{uid}` | — → void (leaves org when `uid` is self) |
| `listInvitations` | `GET .../invitations` | — → `Invitation[]` |
| `inviteMember` | `POST .../invitations` | `{email,role}` → `Invitation` |
| `revokeInvitation` | `DELETE .../invitations/{iid}` | — → void |
| `acceptInvitation` | `POST /v1/invitations/accept` | `{token}` → `Membership` |
| `listReceivedInvitations` | `GET /v1/invitations` | — → `ReceivedInvitation[]` |
| `acceptReceivedInvitation` | `POST /v1/invitations/{iid}/accept` | — → `Membership` |

No `getAuthConfig`/Google-provider-detection call exists in `web/` at all — it reads the client-side `VITE_AUTH_PASSWORD_ONLY` flag instead (`lib/auth/demo.ts`), a real behavioral difference from web-next's `getAuthConfig()`. `GET /v1/organizations` (plural, no id) has **no caller** anywhere — `getSession` already embeds `organizations[]`. `managesMembers(role)` = `role==="owner"||role==="admin"`, used throughout for the owner/admin gate.

**`auth/session.ts`, `organization.ts`, `schemas.ts`, `agent-grant.ts`, `demo.ts`** — no new endpoints beyond `client.ts`, but real logic: `session.ts` owns org *selection* client-side (not API) — `localStorage["weirdly-odd:organization"]` + a `useSyncExternalStore` cache, `resolveOrganization` falls back to `organizations[0]` if the stored id is gone; `ensureSession`/`ensureReceivedInvitations` are the `beforeLoad` guards that redirect to sign-in/onboarding; `safeRedirect`/`searchToken`/`isInvitationRedirect` are the open-redirect/empty-token guards (web-next has equivalents in `lib/auth/schemas.ts`). `organization.ts`: `slugify`, `sortMembers` (owner→admin→member then name), `canRemoveMember`/`canRevokeInvitation` policy helpers. `agent-grant.ts`: `getAgentGrant`→`GET /v1/agent-grant`→`{granted,created_at?}`; `authorizeAgentGrant(returnTo)`→`POST .../authorize` `{return_to}`→`{authorization_url}`; `revokeAgentGrant`→`DELETE`→void.

**`billing/client.ts`**

| Function | Endpoint | Request → response |
|---|---|---|
| `getPlans` | `GET /v1/billing/plans` | — → `{plans[],unpaid:{member_limit}}`, cached 5 min |
| `getBillingSummary` | `GET .../organizations/{id}` | — → `BillingSummary{can_manage,entitlements,subscription}` |
| `startCheckout` | `POST .../checkout` | `{plan,quantity}` → `checkout_url` (string) |
| `openBillingPortal` | `POST .../portal` | — → `portal_url` (string) |

`hasOpenSubscription` = status ∈ `{active,past_due,on_hold,paused}` — web-next's `openStatuses` set already matches exactly.

**`files/client.ts`**

| Function | Endpoint | Request → response |
|---|---|---|
| `listFiles` | `GET .../files?scope=&team_id=` | `FileFilter` → `StoredFile[]`, unbounded |
| `getFile` | `GET .../files/{id}` | — → `StoredFile`; called before download purely to force a refresh opportunity |
| `uploadFile` | `POST .../files` | multipart `FormData{file,filename,content_type,purpose:"upload",scope,retention_days,team_id?}` → `StoredFile` |
| `deleteFile` | `DELETE .../files/{id}` | — → void |
| `fileContentUrl` | *(not fetched)* `GET .../files/{id}/content` | returns a plain URL string for `<a href>`/`<img>` |

`fileContentUrl` (and `threads/client.ts`'s `artifactContentUrl`) bypass `apiFetch` entirely — direct browser navigation on the ambient cookie, **no refresh-on-401**. The `download-url` presigned endpoint is unused. Max upload 8 MB; retention choices `{7,30,90}` days; `canDeleteFile` = org admin, or (member scope) owner, or (team scope) uploader/team manager.

**`rag/client.ts`**

| Function | Endpoint | Request → response |
|---|---|---|
| `listCorpora` | `GET .../corpora` | — → `Corpus[]` |
| `getCorpus` | `GET .../corpora/{id}` | — → `Corpus` |
| `listDocuments` | `GET .../corpora/{id}/documents?cursor=&limit=` | → `{documents,next_cursor?}` |

The **only** module using true cursor pagination wired to `infiniteQueryOptions` (`getNextPageParam: p=>p.next_cursor`, page size 50) — the pattern to copy for any other paginated list ported into web-next. `externalSourceLink` only links `http:`/`https:` targets (XSS guard); `isFromConnectedApps` checks `metadata.origin==="knowledge"`.

**Ported to web-next as `src/lib/rag/client.ts`**, with the rest of the handler: `getCapabilities` (`GET /v1/capabilities`; `answering` decides answer or search), `createCorpus` (`{name,slug,scope,team_id?,metadata?}`), `deleteCorpus`, `addDocument` (`{source:{kind,ref?},title?,content_type?,text?}` → 202 `pending`), `addFileDocument` (a files upload in the corpus's own scope, then a `file` document; the upload is deleted if rag refuses it), `deleteDocument`, `searchCorpus`/`answerCorpus` (`{query,top_k?}`), policy helpers mirroring `mayWrite`/`mayDelete`/`mayCreate`, and `ragKeys` with query options. `documentsInfiniteQueryOptions` polls only while a loaded document is `pending`/`ingesting`: every 3 s, every 15 s once it has taken two minutes, every 30 s while only a retry is awaited. Checked against Go: the corpora list is wrapped (`{corpora}`) and leaves out team corpora unless asked per team; a corpus has **no description field** (the decoder refuses unknown fields), so web-next keeps it in `metadata.description`; slugs are unique across the whole organization, so the client sends its own and retries a 409 with a suffix; `updated_at` never moves after creation; rag indexes text only (plain, Markdown, HTML, CSV, JSON), with no multipart upload, no URL fetching and no PDF/Office extraction; `answer` is a 400 when no answering model is configured.

**`rooms/client.ts`**

| Function | Endpoint | Request → response |
|---|---|---|
| `readRoom` | `GET .../teamspaces/{id}/messages?limit=200` | — → `{messages,seats}`, polled every 5s (`refetchIntervalInBackground:false`) |
| `sayInRoom` | `POST .../messages` | `{body}` → `RoomMessage[]` |
| `catchUpOnRoom` | `GET .../catchup?after={seq}` | — → `{brief,messages}` |

The 5s poll is load-bearing for *presence*, not just refresh — reading the room is how it learns a member is there (presence lapses after ~90s of silence per a source comment).

**`teams/client.ts`**

| Function | Endpoint | Request → response |
|---|---|---|
| `listTeams` | `GET .../teams` | — → `TeamSummary[]` |
| `getTeam` | `GET .../teams/{id}` | — → `TeamDetail{team,membership,can_manage}` |
| `createTeam` | `POST .../teams` | `{name,parent_id?}` → `Team` |
| `renameTeam` | `PATCH .../teams/{id}` | `{name}` → `Team` |
| `deleteTeam` | `DELETE .../teams/{id}` | — → void (cascades to nested teams) |
| `listTeamMembers` | `GET .../teams/{id}/members` | — → `TeamMembership[]` |
| `addTeamMember` | `POST .../teams/{id}/members` | `{user_id,role}` → `TeamMembership` |
| `changeTeamMemberRole` | `PATCH .../teams/{id}/members/{uid}` | `{role}` → `TeamMembership` |
| `removeTeamMember` | `DELETE .../teams/{id}/members/{uid}` | — → void (leaves when self) |
| `setTeamEmailNotices` | `PATCH .../teams/{id}/notification` | `{notify_email}` → `TeamMembership` |
| `searchMembers` | `GET .../members/search?limit=8&q=` | — → `MemberSearchHit[]`, debounced, `enabled` gated on non-empty query |

`teamTree`/`flattenTeams`/`teamTrails` build the tree **client-side** from the flat list + dotted `path` strings; `closestParent` keeps a team at top-level in the rendered tree if its real parent isn't visible to the caller. The Go `teams` service also exposes `tools`/`callTool`/`directory`/`teams/search`/`teams/nearest`/`descendants`/`embedding` — **none have a client function here**; see §3.

**`teamspaces/client.ts` + `policy.ts`**

| Function | Endpoint | Request → response |
|---|---|---|
| `getOrganizationAgent` / `updateOrganizationAgent` | `GET`/`PATCH .../organization-agent` | update body is name/description/instructions |
| `getOrganizationContext` | `GET .../organization-agent/context` | → `OrganizationSynthesis` |
| `listOrganizationFollowUps` | `GET .../organization-agent/follow-ups?limit=` | → `OrganizationFollowUp[]`, limit-only |
| `listTeamspaces`/`getTeamspace` | `GET .../teamspaces[/{id}]` | → `Teamspace[]`/`Teamspace` |
| `createTeamspace` | `POST .../teamspaces` | `{name,description,team_id}` → `Teamspace` |
| `updateTeamspace`/`deleteTeamspace` | `PATCH`/`DELETE .../teamspaces/{id}` | |
| `addTeamspaceContribution` | `POST .../teamspaces/{id}/agents` | `{agent_id,delegation_mode,role}` → whole `Teamspace` |
| `updateTeamspaceContribution`/`deleteTeamspaceContribution` | `PATCH`/`DELETE .../agents/{aid}` | delete **also returns the updated `Teamspace`**, not void — inconsistent with every other delete in the codebase |
| `listTeamspaceEntries` | `GET .../entries?limit=100` | → `TeamspaceEntry[]`, limit-only |
| `publishTeamspaceEntry` | `POST .../entries` | `{author_agent_id,kind,summary,title,artifacts?,tools_used?}` → `TeamspaceEntry` |

`organizationSynthesisQueryOptions` is a verbatim duplicate of `organizationContextQueryOptions` — dead alias, don't port both. `safeArtifactHref`/`canPublishSharedWork` (member + active space + active org-agent + ≥1 author) are pure policy helpers worth keeping as-is.

**`knowledge/client.ts`** (largest non-agents/apps module, because it also owns per-provider UI copy)

| Function | Endpoint | Request → response |
|---|---|---|
| `listProviders` | `GET /v1/sources/providers` | → `string[]`, `staleTime:Infinity` |
| `listKinds` | `GET /v1/sources/kinds` | → `SourceKind[]`, `staleTime:Infinity` |
| `listSources` | `GET .../sources?connection_id=&provider=&scope=` | → `Source[]` |
| `registerSource` | `POST .../sources` | `{connection_id,provider,target,interval_seconds}` → `Source` |
| `removeSource` | `DELETE .../sources/{id}` | — → void |
| `setSourceKinds` | `PUT .../sources/{id}/kinds` | `{include_kinds}` (`null`=reset to default) → `Source` |
| `listRuns` | `GET .../sources/{id}/runs?limit=` | → `SyncRun[]`, limit-only, no cursor |

Per-provider (github/notion/slack/googledrive/gmail) label/placeholder/help/validation copy and interval presets (`min 300s`/`max 604800s`/`default 3600s`) live in this same file — a lot of UI logic colocated with the fetch layer, worth deciding deliberately whether to keep colocated when porting.

**`agents/client.ts`** (643 lines)

| Function | Endpoint | Request → response |
|---|---|---|
| `listAgents` | `GET .../agents` | → `ManagedAgent[]` |
| `startSpecialistRun` | `POST .../agents/{id}/runs` | `{thread_id?,message?,confirmations?}` → **SSE stream** via `apiFetch` (not `apiRequest`); reads response header `X-Oryxa-Thread` for a server-assigned thread id |
| `listSpecialistThreads`/`getSpecialistThread` | `GET .../agents/{id}/threads[/{tid}]` | |
| `updateAgent`/`deleteAgent` | `PATCH`/`DELETE .../agents/{id}` | |
| `listAgentTeams`/`createAgentTeam`/`updateAgentTeam`/`deleteAgentTeam` | full CRUD on `.../agent-teams[/{id}]` | |
| `listAgentTeamMessages`/`getAgentTeamThreads` | `GET .../agent-teams/{id}/messages`\|`/agent-threads` | polled every 5s |
| `postAgentTeamMessage` | `POST .../agent-teams/{id}/messages` | `{body}` → `AgentTeamMessage[]` |
| `listAgentWorkflows`/`createAgentWorkflow`/`updateAgentWorkflow`/`deleteAgentWorkflow` | full CRUD on `.../agent-workflows[/{id}]` | update body can include `clear_schedule` |
| `startAgentWorkflow` | `POST .../agent-workflows/{id}/executions` | → `WorkflowExecution` ("Run now") |
| `listWorkflowExecutions` | `GET .../executions?limit=20` | `refetchInterval` is a **function**: 2s while any execution is `queued`/`running`, else `false` — adaptive polling, good pattern to reuse |
| `listAgentRequests(box)` | `GET .../agent-requests/{box}?limit=50` | `box:"inbox"\|"sent"` as a path segment; polled 10s |
| `decideAgentRequest` | `POST .../agent-requests/{id}/decision` | `{decision,message,allowed_tools?,granted_permissions?}` |
| `respondToAgentRequest` | `POST .../agent-requests/{id}/response` | `{status,content,summary,artifacts?,tools_used?}` |

**No `createAgent` export exists** — specialists are created through a primary-agent tool call in conversation (the UI watches the timeline for a completed `agent_system_create_agent` tool call), not a direct POST to `.../agents`.

**`apps/client.ts`** (887 lines, largest module in the app)

| Function | Endpoint | Request → response |
|---|---|---|
| `listCatalog` | `GET /v1/apps/catalog?search=&category=&cursor=&limit=` | → `{toolkits,nextCursor?}` (server's `next_cursor` renamed camelCase) |
| `getCatalogToolkit` | `GET .../catalog/{toolkit}` | cached 5 min |
| `listCatalogTriggerTypes` | `GET .../catalog/{toolkit}/triggers?cursor=&limit=` | `limit:50` hardcoded, effectively page-1-only in practice |
| `connect` | `POST .../apps/connections` | `{toolkit,alias?,return_to}` → `{connection,redirect_url}` |
| `listConnections` | `GET .../apps/connections?scope=&toolkit=` | unbounded |
| `getConnection`/`disconnectConnection` | `GET`/`DELETE .../apps/connections/{id}` | |
| `listTriggers`/`createTrigger`/`removeTrigger` | `GET .../apps/triggers?connection_id=` / `POST` `{connection_id,trigger,config}` / `DELETE .../triggers/{id}` | |
| `listConnectionTargets` | `GET .../apps/connections/{id}/targets` | empty list is a fact ("nothing indexable"), not an error |

`appsErrorMessage` is the one place `ApiError.code` is exercised: `choose_connection`/`conflict`/`needs_reconnect`/`not_connectable`/`not_connected`/`rate_limited`/`unavailable` → friendly copy, else falls back to the generic humanizer. Cursor pagination here is **not** wired to `infiniteQueryOptions` (caller manages `cursor` state manually) — inconsistent with `rag/client.ts`. ~60% of the file is a JSON-Schema→form-field mapper (`triggerConfigCapability`/`fieldShape`/`declaredTypes`/`enumOptions`) for dynamic trigger config forms; web-next's `workspace-tool-helpers.ts` already has an independent, smaller reimplementation of the same idea (`triggerCapability`/`buildTriggerConfig`), so this one likely doesn't need porting wholesale. `runTool`/`proxy`/`connect/callback` endpoints have no client function (agent-runtime-internal or redirect-only).

**`threads/*`**

| Function | Endpoint | Request → response |
|---|---|---|
| `listThreads` | `GET .../threads` | → `Thread[]`, server-ordered most-recent-first |
| `createThread` | `POST .../threads` | — → `Thread` |
| `getThread` | `GET .../threads/{id}` | → `ThreadDetail{items,pending_confirmations}` |
| `deleteThread` | `DELETE /agents/thread-chat-agent/{org}~{threadId}` | **not a `/v1/...` Go route** — Cloudflare Agents SDK DO-routing convention, still cookie-authed via `apiRequest` |
| `getKrudePreferences`/`setAppUpdates` | `GET`/`PUT .../preferences` | `{app_updates}` |
| `startRun` | `POST .../threads/{id}/runs` | `{message}` or `{confirmations}` → **SSE stream** via `apiFetch`; doc-only error codes `run_in_progress`/`confirmation_pending` (not branched on in code) |
| `artifactContentUrl` | *(not fetched)* `GET .../threads/{id}/artifacts/{name}?version=` | plain URL string, cookie-authed, no refresh protection |

`event-stream.ts`'s `createEventStreamReader()` is a hand-rolled incremental SSE parser (needed because `EventSource` can't do POST), shared by `startRun` and `startSpecialistRun` — handles chunk-split lines/UTF-8/keep-alive comments/no-trailing-blank-line. `chat-messages.ts` converts between the Vercel AI SDK `UIMessage` shape and the Go `Item` event shape (`itemsToChatMessages`/`chatMessagesToItems`/`reconcileChatMessages` merges DO-stored history with a fresh Go fetch); `sessionExpiredCode` is DO-side only (Go never sets `.code`). `timeline.ts` is pure `buildTimeline`/`unseenLiveItems` — the message/tool/confirmation/artifact grouping state machine every conversation UI renders from (tool_call+tool_result+confirmation merge into one entry by `call_id`). `pending-message.ts` is an in-memory-only (not persisted) hand-off `Map`, explicitly built to survive a remount/StrictMode double-invoke, **not** a hard page reload — flag this assumption if web-next's create-thread flow ever does a full navigation between steps. `agent-socket.ts` (35 lines) is *only* a generic reopen-with-timeout helper (`reopenSocket`), not socket construction — that lives in `server.ts`/the `useAgent` hook. `recent.ts` doesn't exist as a file — `recentThreads`/`SIDEBAR_THREAD_LIMIT`(=3)/`threadTitle` live in `client.ts`; only `recent.test.ts` exists.

**`format.ts`, `utils.ts`** — pure date/relative-time formatting (`formatDate`/`daysUntil`/`expiryText`/`relativeTime`) and a `cn` re-export. No network calls.

**Coupling flags for whoever ports these**: three near-duplicate error-humanizers exist (`apiErrorMessage` in `api/client.ts`, `authErrorMessage` in `auth/client.ts`, `appsErrorMessage` in `apps/client.ts`) with subtly different edge-case behavior — converge into one configurable helper rather than porting all three. Pagination strategy is inconsistent across every module (true cursor+infinite-query only in `rag`; cursor-but-manual in `apps`; limit-only-no-more in `knowledge`/`teamspaces`/`agents` list-execution endpoints; fully unbounded everywhere else) — pick one convention for web-next rather than copying the inconsistency.

---

## 3. Real API surface — coverage matrix

Grepped from `server.handle(`/`handleFunc(` in each service's `internal/httpserver/server.go`. Mounted services (`cmd/oryxa/services.go`): `auth, teams, billing, files, knowledge, rag, apps, batching, email, agents` (`agents/primary`). **No `connections/` or `integrations/` service exists in this snapshot** — both were archived; ignore any older doc describing a `connections` microservice. `batching`/`email` expose no `/v1` routes of their own. `healthz` endpoints and one `auth` internal endpoint (`POST /v1/internal/agent-tokens`, service-to-service only) are omitted below as not member-facing.

Legend: **W** = used by `web/`, **N** = used by `web-next/`, **—** = used by neither (as far as either app's client code shows a caller), **redirect** = a browser-navigation target rather than a `fetch()` call, **unclear** = plausible but no confirmed caller found.

### auth
| Endpoint | web | web-next |
|---|---|---|
| `GET /v1/auth/config` | — | N |
| `GET /v1/auth/login` | redirect | redirect |
| `GET /v1/auth/callback` | redirect | redirect |
| `POST /v1/auth/refresh` | W (silent) | N (silent) |
| `POST /v1/auth/logout` | W | N |
| `POST /v1/auth/password/{signup,verify,resend-verification,login,forgot,reset}` | W | N |
| `GET /v1/auth/me` | W | N |
| `GET /v1/organizations` | — | — |
| `POST /v1/organizations` | W | N |
| `GET /v1/organizations/{id}` | W | N |
| `PATCH /v1/organizations/{id}` | W | N |
| `DELETE /v1/organizations/{id}` | W | N |
| `GET /v1/organizations/{id}/members` | W | N |
| `POST/GET /v1/organizations/{id}/invitations` | W | N |
| `DELETE /v1/organizations/{id}/invitations/{iid}` | W | N |
| `POST /v1/invitations/accept` | W | N |
| `GET /v1/invitations` | W | N |
| `POST /v1/invitations/{iid}/accept` | W | N |
| `PATCH/DELETE /v1/organizations/{id}/members/{uid}` | W | N |
| `GET/POST/DELETE /v1/agent-grant`, `GET .../callback` | W | N |

### teams
| Endpoint | web | web-next |
|---|---|---|
| `GET /v1/organizations/{id}/tools`, `POST .../tools/{name}` | — | — |
| `GET .../teams` | W | N |
| `GET .../directory` | — | — |
| `POST .../teams` | W | N |
| `GET .../members/search` | W | — (web-next reuses the full member list instead) |
| `GET .../teams/search` | — | — |
| `POST .../teams/nearest` | — | — |
| `GET .../teams/{id}` | W | N |
| `GET .../teams/{id}/descendants` | — | — |
| `PUT .../teams/{id}/embedding` | — | — |
| `PATCH .../teams/{id}` | W | N |
| `DELETE .../teams/{id}` | W | N |
| `PATCH .../teams/{id}/notification` | W | N |
| `GET .../teams/{id}/members/descendants` | — | — |
| `GET .../teams/{id}/members` | W | N |
| `POST .../teams/{id}/members` | W | N |
| `PATCH/DELETE .../teams/{id}/members/{uid}` | W | N |

Seven of `teams`' 20 endpoints (`tools`, `directory`, `teams/search`, `teams/nearest`, `descendants`, `embedding`, `members/descendants`) have no caller in **either** frontend — they read as agent-routing/MCP-adjacent surface, not human-UI surface. Confirm before building against them.

### apps
| Endpoint | web | web-next |
|---|---|---|
| `GET /v1/apps/catalog[/{toolkit}[/tools\|/triggers]]` | W | N |
| `GET /v1/apps/connect/callback` | redirect | redirect |
| `POST /v1/apps/webhooks/composio` | — (Composio→Go) | — |
| `GET/POST /v1/organizations/{id}/apps/connections` | W | N |
| `GET/DELETE .../apps/connections/{cid}` | W | N (Tools: the return trip's reason; disconnect, cancel, and removing a replaced row) |
| `POST .../apps/tools/{tool}/run` | — | — |
| `POST .../apps/read` | — | — |
| `GET .../apps/connections/{cid}/targets` | W | N |
| `GET/POST/DELETE .../apps/triggers[/{tid}]` | W | N |
| `GET /v1/apps/integrations`, `GET /v1/apps/integrations/{toolkit}/manifest` | — | N (Oryxa's own sign-in, 2026-09-28: which toolkits connect through it, and the form a way in is rendered from) |
| `POST .../apps/connections` with `broker: "oryxa"`, `kind`, `fields` | — | N (Tools → Connect for a listed toolkit; a key goes through the credential dialog) |
| `GET /v1/apps/oauth/callback` | — | redirect (the return trip `parseConnectReturn` already reads) |
| `PUT .../apps/connections/{cid}/credentials` | — | N (Replace key) |
| `GET/PUT/DELETE .../apps/oauth-clients/{client}` | — | N (Settings → Sign-in apps, admins) |
| `POST .../apps/webhooks/{toolkit}/verification-token` | — | N (Notion's token, revealed once) |

`?scope=organization` on the connections list switches between "my connections" (default) and "every member's" — both apps use each mode correctly for its own purpose (member view vs. admin rollup); this is not a bug, see §6.

### knowledge
| Endpoint | web | web-next |
|---|---|---|
| `GET /v1/sources/providers` | W | N |
| `GET /v1/sources/kinds` | W | N |
| `GET/POST .../sources` | W | N |
| `GET .../sources/{id}` | unclear | — |
| `DELETE .../sources/{id}` | W | N |
| `POST .../sources/{id}/sync` | — | N |
| `PUT .../sources/{id}/kinds` | W | N |
| `GET .../sources/{id}/runs` | W | N |
| `GET .../documents/{id}` | — | — |

### rag
| Endpoint | web | web-next |
|---|---|---|
| `GET /v1/capabilities` | — | N |
| `GET/POST .../corpora`, `GET .../corpora/{id}` | W (GET only) | N |
| `DELETE .../corpora/{id}` | — | N |
| `GET/POST .../corpora/{id}/documents` | W (GET only) | N |
| `GET .../corpora/{id}/documents/{did}` | — | — (the list carries everything the page shows) |
| `DELETE .../corpora/{id}/documents/{did}` | — | N |
| `POST .../corpora/{id}/search`, `.../answer` | — (agent-side, not browser-fetched) | N (a knowledge base's ask field: answer when `capabilities.answering`, else search) |

web-next's Knowledge Bases page (`components/knowledge-bases.tsx`, `knowledge-base-detail.tsx`) calls these through `lib/rag/client.ts`.

### files
All 6 endpoints: **W** for `list/create/get/delete/content`; `download-url` — **—** (unused, web uses the plain content URL instead). **web-next has no Files surface**; its only callers are the Knowledge Bases page, which uploads a document's file (`POST .../files`) and deletes it again if rag refuses the document (`DELETE .../files/{id}`), and a teamspace's Files tab, which downloads the team's files — both through `lib/files` (`src/lib/files/client.ts`).

### billing
`GET .../plans`, `GET .../organizations/{id}`, `POST .../checkout`, `POST .../portal` — **W and N** (both apps fully wired). `POST .../webhooks/dodo` — neither (Dodo→Go).

### agents/primary (56 routes — the largest service)
All thread, agent (specialist), agent-team, agent-workflow, agent-request/interaction, organization-agent, and teamspace/room endpoints are **W, not N** — this entire service (the bulk of the old app's "Agents" and "Teamspaces" experience) has zero web-next callers today. The two exceptions: `GET/PUT /v1/organizations/{id}/preferences` (app-update toggle) is **W and N** (wired in web-next's `WorkspaceAccountSettings`). `POST /v1/organizations/{id}/agents` (create specialist) is **not called directly by either frontend** — `web/` creates specialists through a primary-agent tool call in conversation, not a REST POST (see §2). A few single-item GETs (`getAgent`, `getAgentTeam`, `getAgentWorkflow`, `getAgentWorkflowExecution`, `getInteraction`) have no confirmed caller in `web/` either — their list endpoints appear to supply enough data for the UI as built; treat as **unclear** rather than assuming they're needed.

---

## 4. web-next today — surface by surface

Per `web-next/DESIGN.md` §2–3: three primary modes (Home/Team/Agents) drive a left nav; Settings is one role-aware modal from the user menu (not separate settings apps); "use real service state when available... never fake success." Per `web-next/AGENTS.md`: read DESIGN.md before any UI change, run `typecheck`/`test`/`build` and report failures honestly.

| Area | File | Status |
|---|---|---|
| `/`, `/auth/*`, `/invitations/accept` | `routes/index.tsx`, `routes/auth.*.tsx`, `components/auth/auth-pages.tsx`, `invitation-accept-page.tsx` | **Wired** — session (`/v1/auth/me`), sign-in/up, forgot/reset/verify, Google, invitation accept, create-workspace (`NewWorkspace` in `index.tsx`, replaces `/onboarding` but has no "invite first members" step) |
| Home → Overview | `components/workspace-shell.tsx` (`savedViews` const) | **Example data** — hard-coded saved views. The right panel's mock activity feed, "5 online" and quick-note form were removed when it became the agent panel |
| Home → Knowledge bases | `components/knowledge-bases.tsx`, `knowledge-base-detail.tsx`, `knowledge-format.ts`, `knowledge-bases.css`; `lib/rag/client.ts` | **Wired** — the member's corpora (own and the organization's) with real descriptions, counts and times; create (member scope, or the whole workspace for owners/admins); a base's documents with honest statuses (pending with retry attempts, indexing, ready, failed with its reason), load more, and where each came from (note, link, file, connected app); add a note, a link with its text, or a text file; remove a document and delete a base behind a confirm step; an ask field that answers with citations, or searches when the deployment has no answering model. The connected-apps corpus is read-only here and points to Tools. No example data; without a workspace or the rag service it shows a "Layout preview" notice and nothing else. Since #85 (merged 2026-09-24), live and mocked-`fetch` tested: an "Admins only" choice when adding content (org owners/admins, organization-scoped bases only) sending `access: "admins"` — a file is also re-uploaded to Files with that access — with a quiet lock badge on a restricted document and 403/400 shown in plain words; a citation's provenance (`metadata.provider`/`document_kind`/`category`) as small muted chips under its title |
| Home → Tools | `components/workspace-tools.tsx`, `workspace-tool-lifecycle.tsx`, `workspace-tool-connection.tsx`, `workspace-tools-data.ts`, `workspace-tool-helpers.ts`; `lib/apps`, `lib/knowledge` | **Wired** — through `lib/apps` and `lib/knowledge` on TanStack Query, in the organization chosen in the header: catalog browse/search (infinite query), connect, events (triggers), indexing (register, run now, kinds, remove), sync runs. The connected row shows every connection that matters with its honest state (active, connecting, failed, expired); its detail offers Disconnect (confirm step), Cancel for pending, Reconnect and Remove for failed, Reconnect and Disconnect for expired. Once a reconnect's new connection is active, a failed old row is removed automatically; an expired one is never removed for the member (it still holds indexed documents, and may be another account): it stays, marked "Replaced by your new connection", with only Remove behind the confirm step (`replacedConnections`). Composio's return (`?app_connection=&status=`) shows a notice (a failure's reason read with `getConnection`), opens that connection and is cleared from the address. A 409 on connect opens the existing connection. **Oryxa's own sign-in** (work package 1W, 2026-09-28; ships switched off): `connect()` in `lib/apps` sends a toolkit `GET /v1/apps/integrations` lists through `broker: "oryxa"` and every other one through Composio with the request it always sent. One OAuth way in with nothing to type redirects to the app; a key, or a choice of ways in (GitHub's token beside its OAuth), opens the credential dialog (`workspace-tool-credential-dialog.tsx`), rendered from the manifest: standard and declared fields, the manifest's rules checked before sending, secrets masked, never filled in, never kept in state and wiped once the request settles, and an `invalid_credentials` answer shown under the field it names. A key connects at once and comes back through the same `?app_connection=&status=` return trip. Tools' Connect button is in `workspace-tools.tsx`, which renders no dialog, so `lib/apps/connect-prompt.tsx` mounts one in a root of its own; closing it says so under the card. An Oryxa connection's detail shows how it signs in and "Replace key" (`PUT …/credentials`) instead of Events, Indexing and Runs, which apps refuses for it until its integration ships. Falls back to `previewToolkits`/`previewConnections` with a "Layout preview" notice only when the Apps service is absent (the probe `GET /v1/apps/catalog?limit=1` fails without apps' own error code, or 404s); a coded failure such as Composio `unavailable` keeps the page live with a retry. |
| Team → Teamspaces | `components/teamspaces-management.tsx`, `teamspace-focus.tsx`, `teamspace-room.tsx`, `hooks/use-teamspaces.ts`, `hooks/use-teamspace-room.ts`, `teamspace-room-agent.ts` | **Wired** — the member's teamspaces (`GET …/teamspaces`) with status, member counts (people with a seat: `whisperers`) and when each was last updated; All/Active/Paused filters and search on real data. Create (`POST …/teamspaces` `{team_id,name,description}`) is offered only for teams the member manages (`GET …/teams` `can_manage`) that have no teamspace yet; pause/resume (`PATCH {status}`) and delete (`DELETE`) only when `can_manage`, pause and delete behind inline confirms; Go's refusals (400/403/404/409 `teamspace_conflict`/503) shown as copy, never as success. Detail: people (Lorelei), agents (voxel: each member's own agent, contributed specialists with role and delegation mode), creator (from the organization's members when not on the team), status, access, latest shared work. "Open teamspace" is the focused view: real title and status in the header; the room through `useTeamspaceRoom`, over the teamspace's `TeamspaceRoomAgent` Durable Object (REALTIME.md): one live object per teamspace shared by its team, which reads Go as each member with the room open in a visible tab (at least every 30 s, so Go keeps them present and their agent quiet; never for a hidden tab), relays a post as its author and reads every second or two while Go runs the room's agents, so the member's message and each reply reach every open page as Go records them; no polling from the page (people and agents told apart, whose agent spoke, presence at once from who has the room open, @mention from the seats; the shared pill composer; posts shown as sending until the room records them; paused/403/404/503 and session-ended states, a refused socket explained by asking the Worker); work tabs Overview (description, status, contributed agents, people), Tasks (Go has none: one line), Files (the team's files, `GET …/files?scope=team&team_id=`, downloads through `downloadFile`), Activity (the ledger, `GET …/entries?limit=100`, artifact links only through `safeArtifactHref`) |
| Team → People | `components/people-directory.tsx`, `direct-message.tsx`, `direct-message-inbox.tsx`, `direct-message-people.ts`, `direct-message-format.ts`, `direct-message-presence.ts`, `direct-messages.css`; `workspace-shell.tsx` | **Wired** — People lists everyone in the organization but the member (`GET …/members`, the key Settings → People reads), with their role and the teamspaces they share as context, search, and a Message action; no examples, and without a workspace it says to sign in. A DM opens in the main canvas under a header that names the person (avatar with an online dot, back to People), and in the right panel's DMs tab, each view its own socket on the same `DirectMessageAgent` (`useDirectMessages`): live messages and older pages on the way up (keeping the reader's place), day separators, groups by author and minute, pending and failed-with-Retry messages, "Seen", typing, presence, unread and read state, and the not-allowed, session-ended and can't-reach (Try again) states. The canvas DM is in the address (`?dm=`, replaced, not pushed). DMs have no Go endpoint by design (§3); REALTIME.md has the objects. DESIGN.md §3, "Direct messages", has the patterns. **Not in v1:** editing, deleting, reactions, attachments; presence in lists only for people whose DM is open on the page (the inbox carries none) |
| Agents (overview / multi-agent teams / specialists) | `components/agents-mode.tsx` (what the shell renders; `hooks/use-agents-mode.ts` keeps which item is open), `specialists-section.tsx`, `specialist-focus.tsx`, `multi-agent-teams-section.tsx`, `multi-agent-team-focus.tsx`, `multi-agent-team-room.tsx`, `hooks/use-agent-team-room.ts` | **Wired** through `lib/agents`, no example data (the dev preview, with no workspace, says there is nothing to show). **Agent overview:** the primary agent's name and status (Resume when paused), counts of specialists, teams, workflows and requests waiting on the member, teams and specialists as entry cards, workflows with their recent executions (read-only). **Specialists:** list, create with a plain form (`createAgent`, `409 name_conflict` on the name field), edit, pause/resume, delete with a confirm step (blocked while on a team or contributed to a teamspace; the server's `agent_in_use` shown), which clears the specialist's chat object before Go deletes it. The focused view's conversation column is the specialist's chat (`specialist-chat.tsx`, below); a team's Members tab offers Chat for each active specialist, which opens the agent panel on the Agents tab with it selected (the shell's `onChat`). **Multi-agent teams:** list, create/edit with a roster and roles, pause/resume, delete (blocked while a workflow runs it); refused in advance when the primary agent is paused or no specialist is active. The focused view's conversation is the team room: read every 5 s, @mentions from the seats, a post shown as sending until the team answers (~90 s at most), behind `useAgentTeamRoom` so its Durable Object can replace the transport |
| Settings → Account | `components/workspace-account-settings.tsx` | **Wired** — password-reset self-service, sign out, agent grant authorize/revoke, app-update preference, per-team call-email toggles |
| Settings → Organisation | `components/workspace-organization-settings.tsx` | **Wired** — profile update, delete/leave with typed confirm |
| Settings → People & invitations | `components/people-management.tsx` | **Wired** — members, invitations send/revoke, role change, remove, received-invitations join |
| Settings → Teams | `components/workspace-teams-settings.tsx` | **Wired** — full team CRUD + members, matches `web/`'s `/app/teams` + `/app/settings/organization/teams` combined into one role-aware surface |
| Settings → Tool access | `components/workspace-tool-access-settings.tsx` | **Wired** (read-only) — `?scope=organization` rollup, owner/admin gated; uses `lib/settings/client.ts` |
| Settings → Sign-in apps | `components/workspace-signin-apps-settings.tsx`; `lib/apps` | **Wired** (2026-09-28, work package 1W) — an organization's own OAuth app for each client registration Oryxa's own sign-in uses (GitHub, Google, Notion, Slack), read from the integrations' manifests and `GET …/apps/oauth-clients/{client}`. Offered to owners and admins only, and only once a toolkit with an OAuth way in is switched on; a member never sees the entry, and the page itself reads nothing for one. Each card: the redirect URI and any webhook URL with Copy; the client ID; a write-only secret shown only by its last four characters; client-level fields; the organization's webhook signing secret where the app needs one (Slack), whose URL appears once it is saved; Notion's verification token revealed once behind a confirm step (a 409 says it already was); the "don't use Oryxa's app" switch where Oryxa has an app to refuse; Remove behind a confirm step. `invalid_credentials` lands under the field apps names; secrets are wiped from their inputs after every save |
| Settings → Email | `components/workspace-email-settings.tsx` | **Static/no API** — matches `web/`'s equally-static `/app/settings/organization/email` exactly; not a gap |
| Settings → Billing | `components/workspace-billing-settings.tsx` | **Wired** — plans, summary, checkout, portal |
| Conversations/threads | `components/agent-chat.tsx`, `specialist-chat.tsx`, `agent-timeline.tsx`, `agent-conversation.tsx`, `agent-switcher.tsx`, `workspace-agent-panel.tsx`; `src/server.ts`, `src/thread-chat-agent.ts`, `src/specialist-chat-agent.ts` | **Wired** — the right agent panel's Agents tab chats with the member's primary agent (its real name and id from `lib/agents`) through the `ThreadChatAgent` Durable Object: it opens a new chat unless one is fresh (the thread the member picked in this browser session, else their latest chat while it was active within the last hour or still waits on their confirmation; a tab left alone for an hour comes back to a new one), starts a thread with the first message, and a quiet "New conversation" button starts another (created on its first message; the previous one stays). The panel keeps each agent's conversation and unsent message across a switch, and `requestPrimaryChat` (`lib/threads/chat-choice.ts`) lets the rest of the app open a thread, or a new chat with a first message: each request waits in a queue until the panel takes it, in order (twenty messages at most; the next is refused), and a first message that could not be sent stays in the log as "Not sent." with Retry, Edit and Dismiss. Timeline with Markdown answers, streaming and interrupted drafts, grouped tool rows, confirmations (single and multiple), artifacts through `fetchArtifactLink`, failed runs, the silent `session_expired` resend, and Stop. With more than one agent, the tab's quick switch (the one in use a named pill, the rest avatar-only) moves between the primary agent and the member's active specialists; a specialist's chat is `SpecialistChat` on its `SpecialistChatAgent`, the same component as its focused view's conversation column (below). **Not yet:** a conversation history list, deleting a conversation (`deleteThreadChat` exists, no control). The DMs tab is the member's inbox (`useInbox`): each conversation's last message, time and unread count, "New message" to pick anyone in the organization, a conversation in its compact layout, and the unread total as a badge on the tab and its rail icon; a side conversation lists with its teamspace and opens in the tab (`side-conversation.tsx`, below). On a teamspace page the Agents tab also lists that teamspace's teammates and their agents, each opening a side conversation. The Teams tab is an empty state |
| Files | `src/lib/files/client.ts` (no route or component of its own; used by Knowledge bases' uploads and a teamspace's Files tab) | **Missing entirely** — no screen to browse or manage files generally |
| Agent requests (interactions) | `components/requests-view.tsx` (Agents → Requests: Inbox, Sent, Activity), `request-card.tsx` (+ `-format`, `-review`, `-secret`), `requests-activity.tsx`, `requests-view-open.ts`, `requests-view-queries.ts`, `lib/agents/agent-requests.ts`; `components/agents-mode.tsx` (the Requests waiting tile leads to Requests); `src/side-conversation-agent.ts` | **Wired** in Agents mode as **Requests**, the fourth item of its navigation. **Inbox** holds the requests addressed to the member, those waiting on them first; **Sent** the ones they asked; **Activity** what happened to their Slack asks (names, states and times, never a question or an answer). A filter (All, Slack, Oryxa) narrows the two lists with `?origin=`; every list and card is re-read on focus and every 10 s, so a request decided in Slack settles here. A card is the request as its member reads it: who asked, the question, where from, where it stands; for a Slack ask (it asks for `consent:summary` and `consent:access`) the owner's three choices (`POST …/consent`, one click, a 409 `already_decided` shows the winner); for any other request Approve, Approve without tools and Decline with a note (`…/decision`); a held answer with why, the answer, the masked preview and Send, Send masked and Don't send (`…/review`); access they gave, which they can end (`DELETE …/agent-leases/{id}`); and one-time secrets (`PUT/GET/DELETE …/secret`). `openRequest(id)` (requests-view-open.ts) opens Requests on a card from anywhere, `/?org=…&view=requests&request=id` does from a link, and the shell gives every DM view `openRequest` through `OpenRequestProvider` (src/lib/dms/request-navigation.ts), so a request's line in a DM has its Open. A teamspace side conversation still shows its own requests as live cards, approved or declined there |
| Search / command palette | *(none)* | **Missing entirely** — no ⌘K equivalent |

---

## 5. Parity table and build plan

One row per member capability. "Build" names the DESIGN.md location, the client to port/write, and the states to handle.

| Capability | Old route/file | Endpoints | web-next status/file | To build |
|---|---|---|---|---|
| Sign in/up, Google, forgot/reset/verify | `auth.*.tsx` | `POST /v1/auth/password/*`, `GET /v1/auth/login` | Wired, `auth-pages.tsx` | Done; #83 fixed the reset-password statuses and the StrictMode verify hang |
| Invitation accept / inbox | `invitations.*.tsx` | `POST /v1/invitations/accept`, `GET/POST /v1/invitations*` | Accept wired; inbox (`/invitations` index) folded into `NewWorkspace` only for the zero-org case | Add a "received invitations" list to People settings or Home for members who already have an org — currently only reachable pre-org |
| Onboarding | `onboarding.tsx` | `POST /v1/organizations`, `POST .../invitations` | Partial — `NewWorkspace` creates the org only (it sends its own slug and retries a taken one, since #83) | Add the "invite your first members" step, or explicitly decide Settings→People after creation is the replacement and drop it from scope |
| Primary agent conversations | `app.threads.tsx`, `app.threads.$threadId.tsx`, `thread-chat-agent.ts` | `GET/POST/DELETE .../threads*`, `POST .../runs` (SSE/DO), `GET .../artifacts/{name}/editor?mode=view\|edit`, signed editor source and callback | **Wired**, `agent-chat.tsx` in the right panel's Agents tab, expanding to the left of the existing Agent overview work panel while staying mounted on the ported `ThreadChatAgent` DO. The right rail then offers DMs and Teams only. `primary-agent-files.tsx` lists current conversation files in Agent overview and opens a compact file viewer in that work panel while the chat stays visible; a newly saved file opens the viewer as its artifact arrives. Files open in a simple in-app preview for PDF, DOCX (in a sandboxed frame), PPTX, XLSX, CSV, Markdown, text and images, up to 25 MB, above which the viewer says so and leaves it to the download; the file-header Edit button, for DOCX, XLSX and PPTX only, opens the full ONLYOFFICE editor. Other formats show an honest download fallback. Edits save as new artifact versions; unconfigured deployments retain native previews where available. Primary and specialist runs receive a bounded Oryxa screen snapshot, read with `get_current_screen` | Left: a history list of past conversations (the user decides its shape), a delete control (`deleteThreadChat`), and `web/`'s "From your apps" author handling is ported but unexercised against real app updates |
| Specialists (CRUD + chat) | `app.agents.tsx?tab=specialists` | `agents/*`; the chat through `SpecialistChatAgent` (Worker + Durable Object, `org~agent`): `POST …/agents/{id}/runs` with the `thread_id` it keeps, `GET …/agents/{id}/threads/{thread}` | **Wired**, `specialists-section.tsx`, `specialist-focus.tsx`, `specialist-chat.tsx`, `workspace-agent-panel.tsx`. The chat is one component on the specialist's object, shown in its focused view's conversation column and in the agent panel's Agents tab (the quick switch lists the primary agent and the member's active specialists; a team's Members tab Chat opens it there). Each view keeps its own socket on the same object, which sends every socket the same conversation; each reads the stored transcript itself and hands it to `useAgentChat` as `messages` (`getInitialMessages: null`), since the SDK caches that read once per object for the whole page. It shows loading, empty and error states; "{name} is working…" while a draft is the empty progress marker; the readable answer (the result's summary) with one quiet line for its status, tools, files and suggested hand-offs; tool rows; files by name (Go has no file route for a specialist's conversation); confirmations; the silent `session_expired` resend; Stop; "New conversation" (`clearSpecialistChat`; the object sends every socket the empty chat and forgets the conversation, which Go keeps); Go's copy merged when the transcript lacks it (`readableSpecialistItems` first). A paused specialist stays readable with the composer off and why; `agent_disabled` from a run re-reads the member's agents. A specialist that is gone says so: the list no longer has it, the transcript read answers 404, or a socket that closes is explained by asking Go `GET …/agents/{id}` (the Worker's own check), which also stops its reconnecting. Delete clears the chat object first, then deletes in Go; it is refused in advance while on a team or contributed to a teamspace, and a refusal Go gives only after the clear says the chat started over | Creation is the plain form; the old app's guided creation (Krude drafting the instructions in a conversation) is not ported. A specialist's past conversations (`listSpecialistThreads`) have no list, and its saved files cannot be opened until Go serves a specialist conversation's artifacts |
| Agent teams (CRUD + room) | `app.agents.tsx?tab=teams` | `agent-teams/*` | **Wired**, `multi-agent-teams-section.tsx`, `multi-agent-team-focus.tsx`, `multi-agent-team-room.tsx` | The room's Durable Object replaces what `useAgentTeamRoom` does inside. Not yet: opening a specialist's team thread (`…/agent-threads`) when it asks for a confirmation in the room |
| Agent workflows | `app.agents.tsx?tab=workflows` | `agent-workflows/*` | **Read-only** on Agent overview: each workflow with its team, schedule and recent executions (re-read every 2 s while one runs) | Create/edit/delete, Run now and run history have no surface, and the nav gets no fourth item without a user decision. Proposed: a Workflows tab in a team's focused view, since every workflow runs one team, with the overview's list linking there. Until then a team that a workflow runs cannot be deleted here (`team_in_use`, explained in the delete step) |
| Agent requests | `app.agents.tsx?tab=requests` | `agent-requests/*` | **Wired**, Agents → Requests (`requests-view.tsx`, `request-card.tsx`, `requests-activity.tsx`) | Inbox/Sent as in the old app, plus Activity and an origin filter; decide is Approve/Decline (the old app's manual Respond to an approved request is not built: an approved request is answered by its owner's agent, which the waker wakes). Pitfalls: `origin` is only `agent` or `slack` (a blank filter is a 400, so none is sent for All); a Slack request's `requester` has no name, so names come from the members list (`usePeopleNames`, which latches once it has answered so a failing list cannot flip every card between loading and shown); the request JSON has no Slack channel, so a card says "Slack" and not which channel; `held` is on the target member's reads only, and only while the status is `review`; `secret_viewed_at` is only ever a time |
| Slack asks: consent, leases and one-time secrets | *(no old-app equivalent)* | `POST …/agent-requests/{id}/consent {choice}`, `GET …/agent-requests/activity?limit=`, `DELETE …/agent-leases/{id}`, `PUT/GET/DELETE …/agent-requests/{id}/secret` (agents/primary `consent.go`) | **Wired**, `request-card.tsx`, `request-card-secret.tsx`, `requests-activity.tsx` | The secret's value is an uncontrolled field read at submit (owner) and state of the opener alone (asker): never a query, a mutation's variables or data, or a log; opening consumes it (a second GET is 410 `secret_gone`), so it asks first. A lease's id is not on the request: the card learns it from the consent response, from Activity's `lease_given` entries (read only while the inbox holds an access approval under an hour old), or from a follow-up's `decided_by.reason`. Held kinds the card says in words: personal details, `OFF_TOPIC` and its categories, `COPIED_FROM_SOURCE`, `LINK`, `NO_SOURCES`, `UNGROUNDED`, `IRRELEVANT`, `DENIED_TOPIC`, `UNCHECKED`; a summary-rule hold (`COPIED_FROM_SOURCE`, `LINK`) has no masked version |
| Slack settings | *(no old-app equivalent)* | `GET/DELETE …/apps/slack/install` (with `?return_to=`), `GET/PUT …/apps/slack/channels` (apps `slack.go`) | **Wired**, Settings → Slack (`slack-settings.tsx`, `lib/apps/slack.ts`), for owners and admins | Add to Slack goes to Slack's consent page and returns to `/?org=…&view=slack&slack=connected` or `slack=error&slack_error=…`, which opens Settings on Slack and says how it went; every read of the install makes a new single-use link, so it is never re-read on focus and a link about to lapse is re-read before the page leaves; the link must be an https `slack.com` page. Disconnect is behind a confirm step. Each private channel the app is in has a high-priority switch that asks first (a bypass: no owner click); a marked channel Oryxa can no longer see can only be unmarked. A 404 or 503 says Slack isn't available here. Not built: a Requests badge in the navigation |
| Teamspaces (CRUD + room + entries) | `app.teamspaces.tsx` | `teamspaces/*`, `rooms/*` | **Wired** — list, create, pause/resume, delete, detail, the room (live, through the `TeamspaceRoomAgent` Durable Object: `src/teamspace-room-agent.ts`, `hooks/use-teamspace-room.ts`, REALTIME.md), ledger (read) and team files; `teamspaces-management.tsx`, `teamspace-focus.tsx` (§4) | Still to build: contributing and withdrawing specialists and the owner's delegation mode (`POST/PATCH/DELETE …/agents[/{id}]`, `contributionCandidates`), "Share work" (`publishTeamspaceEntry`, gated by `canPublishSharedWork`), the catch-up brief (`roomCatchUpQueryOptions` is ready), rename/description (`PATCH {name,description}`), the Organization Pulse (`organization-agent`, `/context`, `/follow-ups`). The room's seats follow a contribution change within one read (up to 12 s). Pitfalls, checked live: a post answers only after its agents' turns, and one real-model turn took 96 s though Go's relay budget is 20 s (it is checked only between turns); a paused room answers 400 `invalid_request` "this room is paused"; someone off the team gets 404; a manager who is not on the team can read the room but gets 403 posting, so the UI keeps the room to members (`canUseRoom`) and the Worker refuses the room's socket without `is_member`; a member without an active primary agent has no seat and gets 403 posting; names are unique across the organization, so a 409 can come from a teamspace the member cannot see; Go tells nobody when a teamspace is deleted, so the room object clears its copy once nobody connected may open it, or ten minutes after its last socket closes |
| Teams (org hierarchy) | `app.teams.tsx` + `.../organization/teams` | `teams/*` | **Wired**, `workspace-teams-settings.tsx` | Already ported and consolidated into Settings per DESIGN.md — no action beyond verifying parity with `web/`'s per-team notification switch (present) |
| Knowledge bases/corpora | `app.knowledge*.tsx` | `rag/*`, `files` upload/delete | **Wired**, `knowledge-bases.tsx`, `knowledge-base-detail.tsx`, `lib/rag/client.ts` | Beyond `web/`: create, add/remove content, delete, ask; admins-only document access and a citation's provenance chips (PR #85, merged 2026-09-24 — see the Home → Knowledge bases row). Still open: team corpora (the list only returns them per team, and the page offers no team scope yet); a team manager who didn't create a team corpus isn't offered its delete; backend gaps (no corpus description field or rename, `updated_at` frozen at creation, no PDF/Office or URL ingestion, uploaded files expire with files' 30-day default) |
| Files | `app.files.tsx` | `files/*` | **Missing** | No DESIGN.md-named destination yet — needs a placement decision (a Home destination, by the pattern of Knowledge bases/Tools) before building. web-next already has a narrow `src/lib/files/client.ts` (used by Knowledge bases and a teamspace's Files tab, not for general browsing); extend it to cover the rest of `web/`'s `files/client.ts`, and route `fileContentUrl`/download through a refresh-safe path or explicitly accept the no-refresh tradeoff |
| Apps & connections (browse/connect/lifecycle/events/indexing/runs) | `app.connections.tsx` + dialogs | `apps/*`, part of `knowledge/*` | **Wired**, `workspace-tools*.tsx`, `workspace-tool-*.tsx` | Done: disconnect (confirm), reconnect (a failed old row removed once the new one is active; an expired one kept as replaced, removable with confirm), cancel pending, remove failed, Composio's return notice, source kinds and removal. Left: a live pass through Composio's hosted page (an active connection, a real return, a reconnect of an expired one) |
| Org-wide tool rollup | `.../organization/tools` | `apps/*?scope=organization` | **Wired** | None |
| Oryxa's own sign-in: connect, keys, organizations' OAuth apps | *(no old-app equivalent)* | `apps/*` integrations, manifests, `broker: "oryxa"` connect, `…/credentials`, `…/oauth-clients/{client}`, `…/webhooks/{toolkit}/verification-token` | **Wired**, `lib/apps`, `workspace-tool-credential-dialog.tsx`, `workspace-tool-connection.tsx`, `workspace-signin-apps-settings.tsx` | Left: a live pass once a toolkit is switched on (G4); rendering the dialog from Tools' own page instead of `connect-prompt.tsx`'s root of its own, when `workspace-tools.tsx` can change; Events and Indexing for Oryxa connections with phase 2 |
| People/members/invitations | `.../organization/members` | `auth` members/invitations | **Wired**, `people-management.tsx` | None |
| Organization profile | `.../organization` index | `auth` org PATCH/DELETE | **Wired** | None |
| Account/profile settings | `app.settings.profile.tsx` | `auth`, `agent-grant`, `preferences`, `teams/notification` | **Wired**, `workspace-account-settings.tsx` | None |
| Email settings (informational) | `.../organization/email` | none | **Wired** (static, matches old app) | None |
| Billing | `.../organization/billing`, `settings.billing.tsx` | `billing/*` | **Wired** | None |
| Search (⌘K) | `workspace-search.tsx` | threads + nav items | **Missing** | Low complexity, high daily-use value; needs the underlying list data (threads especially) to exist first |
| People-as-DM | *(no old-app equivalent)* | none in Go, by design; the Worker's `/agents/direct-message-agent/{org~userA~userB}`, `/agents/inbox-agent/{org~user}` and Go's signed `POST /internal/realtime/purge` (REALTIME.md); `GET …/members` for whom to message | **Wired**: the DM and inbox Durable Objects behind the Worker's access checks (403 `not_participant`, 404 `peer_not_found`) and the purge, and the views on `src/lib/dms`: People, a DM in the main canvas (from People or the three latest in Team's navigation) and in the right panel's DMs tab, the tab's list and unread badges | Side conversations open from the DMs list in the tab (the side conversations row). An "online in the app" signal (a list shows presence only for DMs open on the page) needs the inbox or the registry to carry it |
| Teamspace side conversations | *(no old-app equivalent)* | the Worker's `/agents/side-conversation-agent/{org~teamspace~userA~userB}`; Go's `POST …/threads`, `…/threads/{id}/runs`, `…/agent-requests` (create; list with `?teamspace_id=&with_user=`; read; `/decision`; `/review`), all as each person, from the `feat/interactions-owner-approval` branch (REALTIME.md) | **Wired**. The core: `SideConversationAgent` (people's messages with a DM's semantics; asking one's own agent, streamed to its owner alone and shared with the other person only once Go's answer guard has checked it as the owner (what it lets through is all that is stored, told to an inbox or used as context; an answer it cannot check is not shared), confirmations to their owner alone; asking the teammate's agent through an agent request, decided by its owner as themself from their own socket (approved with the tools it asked for, `apps` and `knowledge`, or without), followed every 3 s while open, an answer the answer guard held shown to its owner alone (read as them, kept in memory) and reviewed by them (send, send masked, withhold), its result as that agent's message "on behalf of … approved by …", quiet lines for declined, failed and expired; both inboxes; the teamspace-leave purge (#95/#96): leaving the team behind a teamspace, distinct from leaving the organization, ends just that teamspace's side conversations, in `inbox-agent.ts` and `side-conversation-agent.ts`), its access check (the caller one of the two and on the team, the other on the team too; 403 `not_participant`/`forbidden`, 404 `peer_not_found`), and `useSideConversation` (`src/lib/side`, with `reconnect()` for Try again). The screens (`components/side-conversation.tsx`, `side-conversation-teammates.tsx`, `side-conversation-format.ts`, `side-conversations.css`; DESIGN.md §3): on a teamspace page the right panel's Agents tab lists the teammates with a seat there, their agents and delegation ("asks first", "automatic") and their unread; a teammate, or one of their agents, opens the conversation in the tab; the DMs tab opens one from its list with no teamspace open. The conversation is a DM's compact layout on the shared log (`conversation-log.tsx`): people's messages as a DM's ("Asked Leo" on a question to an agent), agents' messages with whose they are, Markdown, a working line and the "on behalf of" line, request cards (Approve, Approve without tools and Decline with a note, and what approving lets the agent read, for the owner alone; the owner's review of a held answer, why in plain words, with its masked preview; "waiting for Priya to review the answer" and "reviewed by" for the one who asked, "Waiting for Priya to approve" for the requester, "runs without asking" for automatic delegation, Retry when not filed), quiet status lines, the owner's own confirmations, and a composer whose small pill picks whom a message goes to (the teammate, "Ask my agent", or one of the teammate's agents). States: loading, empty, not allowed, session ended, paused (read-only, with why), unreachable with Try again. Tested in `side-conversations.test.tsx` over stand-in sockets | Left: nothing in the room links to a side conversation (a seat in the room's participants could open one); Agents mode's requests screen does not list the requests filed here; an agent's files and tool calls stay in its owner's thread, with no link to it from the conversation. Pitfalls, checked live: Go wakes the asked agent only through its events queue, so a dev stack without `AGENTS_EVENTS_QUEUE_URL` approves a request but never answers it; a member without an active primary agent is not among the teamspace's seats, so a side conversation with them is refused (`peer_not_found`), as Go refuses them a seat (the Agents tab lists only teammates with a seat); a refused socket upgrade in `vite dev` is dropped without a status, so the page asks the Worker why |

### Suggested build order

1. **Stop the DESIGN.md violations first** (cheap, high-trust-impact): label or gate Teamspaces' "create" actions as examples, or wire their real (already-simple) `POST` endpoints. (Knowledge Bases is wired.)
2. ~~**Close the Tools dead ends**~~ — done: disconnect/reconnect, cancel a pending connection, show a failed return.
3. **Primary-agent conversations** — done in the right panel's Agents tab, through the `ThreadChatAgent` Durable Object (Worker routing, purge after Go's delete, session-expiry resend). Left: the history list and a delete control.
4. **Teamspaces + rooms**, reusing the primary-thread timeline/confirmation UI once it exists.
5. **Agents/Specialists/Teams/Workflows/Requests** — same order as `web/`'s own tabs, since later tabs depend on earlier ones existing (workflows need teams; teams need specialists).
6. **Files** — a comparatively simple CRUD+list surface with no streaming dependency; can be built any time. (Knowledge bases, the other one listed here, is wired.)
7. **Search** once threads exist to search.

---

## 6. Cross-cutting guidance

- **Always use `web-next/src/lib/api/client.ts`'s `apiFetch`/`apiRequest`, never raw `fetch`.** Tools' old `readApi`/`writeApi` called `fetch()` directly until #83, so an expired access cookie read as "signed out"; Tools now reads only through `lib/apps` and `lib/knowledge`, and that wrapper is gone.
- **Organization scoping**: the header switcher sets `?org=` and reloads (`window.location.assign`); `WorkspaceShell` resolves `selectedOrganization` from `session.organizations.find(o => o.id === org)`. Every component that needs the current org should **receive it as a prop from the shell** (as `WorkspaceToolAccessSettings`/`WorkspaceTeamsSettings`/etc. correctly do), not re-derive it. `WorkspaceTools` broke this until #83 — it read `GET /v1/auth/me` itself and used `organizations[0]`, so a member of several organizations connected apps in the wrong one; it now takes `organizationId` from the shell, and its connect round trip keeps `?org=`.
- **Query keys**: existing convention is an array starting with the domain (`["auth","session"]`, `["organizations", orgId, "members"]`, `["settings","teams",orgId,...]`, `["billing","summary",orgId]`). Follow it; invalidate the narrowest matching key on mutation success (e.g. team-member mutations invalidate `["settings","teams",orgId,selectedId,"members"]`, not the whole `"teams"` branch).
- **Error/empty states**: every wired list handles `isPending`/`isError`/empty distinctly with inline copy (see `people-management.tsx`, `workspace-teams-settings.tsx`); a 404 on a feature-flagged/optional service (agent grant, preferences, billing, teams) is treated as "not available in this environment," not a generic error. Reuse `authErrorMessage(error, fallback)` from `lib/auth/client.ts` rather than inventing new copy per component.
- **Preview mode is dev-only and per-section, not global.** `WorkspaceShell`'s `preview` prop only turns on via the dev-only auth-service-unreachable fallback in `routes/index.tsx` (the old `?preview=people` examples went with the real DMs: People never shows example people). There is **no header preview badge** — DESIGN.md explicitly forbids one (`web-next/DESIGN.md` §2: "No preview badge in the header"); the actual pattern is small inline "Layout preview · ..." notices per section (Tools, Tool access, Teams, Billing settings). `web-next/README.md`'s "Preview badge" wording is stale relative to DESIGN.md and the code — follow DESIGN.md.
- **Role gates should mirror the Go handlers' checks**, not reinvent them: role/membership enforcement happens server-side in each service's `internal/application` layer via `authclient.Credentials`, not spelled out route-by-route in `server.go`. The frontend's job is to hide/disable controls a role can't use and handle the resulting 403/409 gracefully — it is not the source of truth.
- **Testing**: Vitest + `@testing-library/react`, `// @vitest-environment jsdom` pragma, a `render()` helper wrapping in a fresh `QueryClient({queries:{retry:false}})`. Mock `fetch` via `vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(body), {status, headers:{"Content-Type":"application/json"}})))`, clean up with `afterEach(() => { cleanup(); vi.unstubAllGlobals() })`. Assert on the exact `fetch` call shape (`toHaveBeenCalledWith(url, expect.objectContaining({method, credentials:"include", body: JSON.stringify(...)}))`) and, for auth flows, assert refresh is or isn't attempted by checking call count.
- **Known pitfalls**:
  - *Reset-password wrong statuses (copied bug).* `web/src/routes/auth.reset-password.tsx` checks `status===410→"expired"`, `status===404→"invalid"`, else a generic error, and web-next's `ResetPasswordPage` copied it until #83 (now `410→expired`, `401/404/409→invalid`). The real backend chain (`auth/internal/application/service.go` `actionToken`, `auth/internal/postgres/actions.go` `ClaimActionToken`) never returns 404 for this call: an unknown/bogus token is `401` (`ErrNotFound` is explicitly rewritten to `ErrUnauthorized`), and an already-used/racing token is `409` (`ErrConflict`) — only "genuinely expired" (`410`) is correctly wired. Fix by checking `410→expired`, `401→invalid`, `409→invalid` (or treat any non-410 4xx as invalid, matching `VerifyEmailPage`'s already-correct broader catch in both apps).
  - *Tools once used raw fetch and ignored the selected organization* — see above; fixed in #83. New surfaces must take the organization from the shell.
  - *The onboarding slug.* Both apps send `slug:""` on `POST /v1/organizations` deliberately (the server derives it from the name) — that part is fine. The real gotcha: slugs are globally unique (`auth_organizations_slug_unique` on `lower(slug)`), so two organizations with the same/similar name collide and the second create fails with `409`. `web/`'s `onboarding.tsx` still shows the raw server string ("resource state conflicts with the request"). Since #83 web-next sends its own slug (the server's slugify, cut to 63 characters, `workspace` when the name has fewer than two a–z/0–9 characters), retries a taken one with a random suffix, and checks the name in UTF-8 bytes as auth does. A server-side fix (suffix a derived slug on conflict) would cover both apps.
  - *React StrictMode double effects on single-use tokens.* A ref guard alone is not enough. `web/src/routes/auth.verify-email.tsx` guards with `redeemedAttempt = useRef(-1)` plus an `active` flag cleared in cleanup; under StrictMode (TanStack Start's client entry turns it on in development) the first run's POST consumes the token, its cleanup discards the answer, and the second run returns early, so the page stays on "Verifying…". web-next copied that and hung until #83. The fixed `VerifyEmailPage` keeps the attempt's in-flight promise in a ref, and every effect run applies its outcome — one request per attempt, and the result always lands. `ResetPasswordPage` doesn't need it (submit-triggered); `InvitationAcceptPage` sidesteps it with a manual button. **Any new auto-firing single-use-token effect must copy the fixed pattern.** (`web/`'s page likely has the same flaw in development; not re-checked.)
  - *DO-addressed and direct-URL endpoints aren't `/v1/...` calls.* `web/`'s `deleteThread` hits `/agents/thread-chat-agent/{org}~{threadId}` (Cloudflare Agents SDK routing, needs `server.ts`-equivalent handling), and `fileContentUrl`/`artifactContentUrl` are plain `<a href>`/`<img src>` URLs relying on the ambient cookie with no refresh protection — don't assume either behaves like a normal `apiRequest` call when porting.

---

### Notes on uncertainty
A few items above are marked **unclear** rather than guessed: single-item GET endpoints across `agents/primary` (agent/team/workflow/interaction) and `rag` (corpus/document), and `knowledge`'s single-source GET — no caller was found in either app's client code, but a caller could exist in a code path neither research pass traversed (e.g. inside a dialog not opened during review). Confirm with a repo-wide grep for the specific function name before deleting or assuming any of these are truly dead.

### Known gaps from the #83 review, still open
- Role and workspace `<select>`s commit in `onChange`; arrow keys on a closed select change a member's role at once on Windows (`people-management.tsx`, the header switcher, team roles). Commit on an explicit choice instead. Fixed in #104: these are now `DropdownMenu` radio menus that commit only on an explicit choice.
- The invitation page has no "use another account" for someone signed in with the wrong email (`web/`'s had one). Fixed in #104: it now offers "Sign out and use another account".
- Teams settings keeps a team's `can_manage` after its admin demotes or removes themselves; refresh the team detail and list too. Fixed in #104: a role change or removal now refreshes the team detail too, and re-syncs both queries on a 403/404/409.
- `BILLING_URL` still sends Dodo's return to `web/`'s `/settings/billing`; web-next has no such route. Fixed in #103: `BILLING_URL` now points at web-next's own `/settings/billing`, which opens Settings on Billing.
- The example create flows use `crypto.randomUUID()`, which is missing over plain http on a LAN address; use `crypto.getRandomValues` (as `createOrganization` does) when these become real.
