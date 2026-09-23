# web/ → web-next/ parity and build guide

Mapped 2026-09-23 from `main`: `web/` with #82's Durable Object thread chat, and `web-next/` as merged in #83 with its review fixes. Every endpoint below was checked against the services' `*/internal/httpserver/server.go`. Update the status columns as surfaces get built.

## Summary

web-next today is a **single-page workspace** (`web-next/src/routes/index.tsx` plus the auth/invitation routes) built around one `WorkspaceShell` component, not the ~30-route app `web/` is. Of the old app's ~25 member-facing capability areas, **about 12 are wired to real APIs** in web-next: all of auth (sign-in/up, forgot/reset, verify, Google), invitation accept, an onboarding-replacement (create-workspace only), and — inside the Settings overlay — People & invitations, Organization profile, Teams, Tool access (read-only), Billing, and Account (password reset, sign-out, agent grant, app-update preferences, team-call emails), plus the Home → Tools canvas (browse catalog, connect, events/triggers, indexing sources, sync runs; no disconnect yet). **About 7 areas render committed example data** regardless of sign-in state or real backend content: Home's overview dashboard, Knowledge Bases, Team → Teamspaces, Team → People (a DM concept with no backing API in either app), and all three Agents-mode tabs (overview, multi-agent teams, specialists) including the focused-work chat panel. **5 areas are entirely absent**, with no route, component, or client call at all: primary-agent conversations/threads (the old app's core feature, including the new Durable-Object-backed `ThreadChatAgent` socket), agent workflows (not even represented in the Agents-mode nav), cross-agent interaction requests (inbox/sent/decide/respond), Files, and the ⌘K command palette. The five most important gaps to close, in rough priority order: (1) primary-agent chat/threads has no web-next surface at all — it is the product's core loop; (2) Knowledge Bases, Teamspaces, and Agents-mode show fabricated data that looks real, which DESIGN.md's own "never fake success" rule forbids; (3) Tools, the one wired app surface, has dead ends: no disconnect or reconnect, a connection left pending hides its app's Connect button for 24 hours, and Composio's `?app_connection=&status=` return is ignored; (4) Files has no web-next surface at all; (5) agent workflows, agent requests and ⌘K search are absent. (Tools' raw fetch and first-organization bug, the reset-password statuses, the onboarding slug and the StrictMode verify hang were fixed in #83; §6 keeps them as pitfalls.)

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
| `GET/DELETE .../apps/connections/{cid}` | W | — (no single-connection read or disconnect in web-next) |
| `POST .../apps/tools/{tool}/run` | — | — |
| `POST .../apps/read` | — | — |
| `GET .../apps/connections/{cid}/targets` | W | N |
| `GET/POST/DELETE .../apps/triggers[/{tid}]` | W | N |

`?scope=organization` on the connections list switches between "my connections" (default) and "every member's" — both apps use each mode correctly for its own purpose (member view vs. admin rollup); this is not a bug, see §6.

### knowledge
| Endpoint | web | web-next |
|---|---|---|
| `GET /v1/sources/providers` | W | N |
| `GET /v1/sources/kinds` | W | — |
| `GET/POST .../sources` | W | N (register only) |
| `GET .../sources/{id}` | unclear | — |
| `DELETE .../sources/{id}` | W | — (no delete-source control in web-next) |
| `POST .../sources/{id}/sync` | — | N |
| `PUT .../sources/{id}/kinds` | W | — |
| `GET .../sources/{id}/runs` | W | N |
| `GET .../documents/{id}` | — | — |

### rag
| Endpoint | web | web-next |
|---|---|---|
| `GET /v1/capabilities` | — | — |
| `GET/POST .../corpora`, `GET .../corpora/{id}` | W (GET only) | — |
| `DELETE .../corpora/{id}` | — | — |
| `GET/POST .../corpora/{id}/documents` | W (GET only) | — |
| `GET/DELETE .../corpora/{id}/documents/{did}` | — | — |
| `POST .../corpora/{id}/search`, `.../answer` | — (agent-side, not browser-fetched) | — |

web-next's Knowledge Bases surface calls **none** of these — it is 100% local example state (see §4).

### files
All 6 endpoints: **W** for `list/create/get/delete/content`; `download-url` — **—** (unused, web uses the plain content URL instead). **web-next has no Files surface at all — every endpoint is N: —.**

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
| Home → Overview | `components/workspace-shell.tsx` (`savedViews`, `activity` consts, lines ~116–149) | **Example data** — hard-coded saved views/activity feed/"5 online"; the quick-note form posts nothing anywhere |
| Home → Knowledge bases | `components/knowledge-bases.tsx` (`initialKnowledgeBases`) | **Example data** — "New knowledge base" creates a `crypto.randomUUID()` entry in local state only; no `rag`/`knowledge` call exists anywhere in this file |
| Home → Tools | `components/workspace-tools.tsx`, `workspace-tool-connection.tsx`, `workspace-tools-api.ts`, `workspace-tools-data.ts` | **Wired** — catalog browse/search/pagination, connect, events (triggers), indexing (sources, sync now), sync runs, all against real `apps`/`knowledge` endpoints, in the organization chosen in the header and through the shared API client (both since #83). No disconnect, reconnect or cancel yet. Falls back to `previewToolkits`/`previewConnections` with a "Layout preview" notice when `GET /v1/apps/catalog?limit=1` fails. |
| Team → Teamspaces | `components/teamspaces-management.tsx` (`initialTeamspaces`) | **Example data** — create/pause/resume are local-state-only mutations with no API call |
| Team → People | `components/people-directory.tsx` | **Example data / new concept** — a DM directory derived from teamspace membership example data; messaging only works when `preview` is true (local-only). No member-to-member messaging endpoint exists anywhere in the Go surface (§3) — this is a **new** concept, not a ported one |
| Agents (overview / multi-agent teams / specialists) | `components/workspace-shell.tsx` (`workEntries`→`workspace-focus.tsx` `workItems`) | **Example data** — entirely hard-coded `WorkItem`s (`product-sync`, `launch-crew`, `research-specialist`); the focused-work chat panel (`workspace-focus.tsx`) is local-state-only, no API of any kind |
| Settings → Account | `components/workspace-account-settings.tsx` | **Wired** — password-reset self-service, sign out, agent grant authorize/revoke, app-update preference, per-team call-email toggles |
| Settings → Organisation | `components/workspace-organization-settings.tsx` | **Wired** — profile update, delete/leave with typed confirm |
| Settings → People & invitations | `components/people-management.tsx` | **Wired** — members, invitations send/revoke, role change, remove, received-invitations join |
| Settings → Teams | `components/workspace-teams-settings.tsx` | **Wired** — full team CRUD + members, matches `web/`'s `/app/teams` + `/app/settings/organization/teams` combined into one role-aware surface |
| Settings → Tool access | `components/workspace-tool-access-settings.tsx` | **Wired** (read-only) — `?scope=organization` rollup, owner/admin gated; uses `lib/settings/client.ts` |
| Settings → Email | `components/workspace-email-settings.tsx` | **Static/no API** — matches `web/`'s equally-static `/app/settings/organization/email` exactly; not a gap |
| Settings → Billing | `components/workspace-billing-settings.tsx` | **Wired** — plans, summary, checkout, portal |
| Conversations/threads | *(none)* | **Missing entirely** — no route, component, or client call anywhere for `agents/primary`'s thread endpoints or a `ThreadChatAgent`-equivalent DO |
| Files | *(none)* | **Missing entirely** |
| Agent requests (interactions) | *(none)* | **Missing entirely** |
| Search / command palette | *(none)* | **Missing entirely** — no ⌘K equivalent |

---

## 5. Parity table and build plan

One row per member capability. "Build" names the DESIGN.md location, the client to port/write, and the states to handle.

| Capability | Old route/file | Endpoints | web-next status/file | To build |
|---|---|---|---|---|
| Sign in/up, Google, forgot/reset/verify | `auth.*.tsx` | `POST /v1/auth/password/*`, `GET /v1/auth/login` | Wired, `auth-pages.tsx` | Done; #83 fixed the reset-password statuses and the StrictMode verify hang |
| Invitation accept / inbox | `invitations.*.tsx` | `POST /v1/invitations/accept`, `GET/POST /v1/invitations*` | Accept wired; inbox (`/invitations` index) folded into `NewWorkspace` only for the zero-org case | Add a "received invitations" list to People settings or Home for members who already have an org — currently only reachable pre-org |
| Onboarding | `onboarding.tsx` | `POST /v1/organizations`, `POST .../invitations` | Partial — `NewWorkspace` creates the org only (it sends its own slug and retries a taken one, since #83) | Add the "invite your first members" step, or explicitly decide Settings→People after creation is the replacement and drop it from scope |
| Primary agent conversations | `app.threads.tsx`, `app.threads.$threadId.tsx`, `thread-chat-agent.ts` | `GET/POST/DELETE .../threads*`, `POST .../runs` (SSE/DO) | **Missing** | Biggest build. Home mode + a focused-work conversation view (DESIGN.md §3: no left sidebar, tabs above the work panel). Needs either a ported `ThreadChatAgent` DO + `server.ts` routing, or a simpler SSE-only client first (`web/lib/threads/client.ts` + `event-stream.ts` port) with the DO as a fast-follow. States: loading skeleton, empty ("start a thread"), streaming, confirmation (single/multi), error, session-expired silent retry |
| Specialists (CRUD + chat) | `app.agents.tsx?tab=specialists` | `agents/*` | Example, `workspace-focus.tsx` | Agents mode → Specialists section (DESIGN.md nav table). Port `lib/agents/client.ts`'s specialist functions; creation flow depends on primary-thread chat existing first |
| Agent teams (CRUD + room) | `app.agents.tsx?tab=teams` | `agent-teams/*` | Example | Agents mode → Multi-agent teams. Port team CRUD + `AgentTeamRoom`'s polling/optimistic-merge pattern |
| Agent workflows | `app.agents.tsx?tab=workflows` | `agent-workflows/*` | Example (not represented) | Needs its own section — currently no workflows concept in web-next's nav at all; reuse the adaptive-polling pattern from `workflowExecutionsQueryOptions` |
| Agent requests | `app.agents.tsx?tab=requests` | `agent-requests/*` | **Missing** | Needs a home in Agents mode; inbox/sent + decide/respond |
| Teamspaces (CRUD + room + entries) | `app.teamspaces.tsx` | `teamspaces/*`, `rooms/*` | Example, `teamspaces-management.tsx` | Team mode → Teamspaces. Port `lib/teamspaces/client.ts` + `lib/rooms/client.ts` (keep the 5s presence-poll); replace fabricated create/pause/resume with real mutations first — cheapest fix to stop violating "never fake success" |
| Teams (org hierarchy) | `app.teams.tsx` + `.../organization/teams` | `teams/*` | **Wired**, `workspace-teams-settings.tsx` | Already ported and consolidated into Settings per DESIGN.md — no action beyond verifying parity with `web/`'s per-team notification switch (present) |
| Knowledge bases/corpora | `app.knowledge*.tsx` | `rag/*`, part of `knowledge/*` | Example, `knowledge-bases.tsx` | Home → Knowledge bases (DESIGN.md §3). Port `lib/rag/client.ts` including its infinite-query document pagination |
| Files | `app.files.tsx` | `files/*` | **Missing** | No DESIGN.md-named destination yet — needs a placement decision (a Home destination, by the pattern of Knowledge bases/Tools) before building. Port `lib/files/client.ts`, but route `fileContentUrl`/download through a refresh-safe path or explicitly accept the no-refresh tradeoff |
| Apps & connections (browse/connect/events/indexing/runs) | `app.connections.tsx` + dialogs | `apps/*`, part of `knowledge/*` | **Wired**, `workspace-tools*.tsx` | Add disconnect/reconnect and cancel for pending or failed connections, and show Composio's return (`?app_connection=&status=`) the way `web/`'s `/app/connections` does |
| Org-wide tool rollup | `.../organization/tools` | `apps/*?scope=organization` | **Wired** | None |
| People/members/invitations | `.../organization/members` | `auth` members/invitations | **Wired**, `people-management.tsx` | None |
| Organization profile | `.../organization` index | `auth` org PATCH/DELETE | **Wired** | None |
| Account/profile settings | `app.settings.profile.tsx` | `auth`, `agent-grant`, `preferences`, `teams/notification` | **Wired**, `workspace-account-settings.tsx` | None |
| Email settings (informational) | `.../organization/email` | none | **Wired** (static, matches old app) | None |
| Billing | `.../organization/billing`, `settings.billing.tsx` | `billing/*` | **Wired** | None |
| Search (⌘K) | `workspace-search.tsx` | threads + nav items | **Missing** | Low complexity, high daily-use value; needs the underlying list data (threads especially) to exist first |
| People-as-DM | *(no old-app equivalent)* | none exist | Example/new concept | Decide: keep as a labeled example/future feature, or drop until a real messaging endpoint exists — DESIGN.md forbids presenting it as real |

### Suggested build order

1. **Stop the DESIGN.md violations first** (cheap, high-trust-impact): label or gate Knowledge Bases' and Teamspaces' "create" actions as examples, or wire their real (already-simple) `POST` endpoints — both are small services with few fields.
2. **Close the Tools dead ends** — disconnect/reconnect, cancel a pending connection, show a failed return — so the one wired app surface never strands a member.
3. **Primary-agent conversations** — the core loop and the prerequisite for Specialists' creation flow, Agent Teams' room, and much of Home's "Recent"/composer. Start with the HTTP+SSE path (`lib/threads/client.ts` + `event-stream.ts` + `timeline.ts` ports) since it has no Durable Object/Worker-routing dependency; add the `ThreadChatAgent` DO once the plain path works, since it is the riskier, infra-dependent piece (Worker routing, DO purge-on-delete, session-expiry retry).
4. **Teamspaces + rooms**, reusing the primary-thread timeline/confirmation UI once it exists.
5. **Agents/Specialists/Teams/Workflows/Requests** — same order as `web/`'s own tabs, since later tabs depend on earlier ones existing (workflows need teams; teams need specialists).
6. **Knowledge bases (real corpora) and Files** — both are comparatively simple CRUD+list surfaces with no streaming dependency; can be built any time, but are lower-risk to save for last since nothing else depends on them.
7. **Search** once threads exist to search.

---

## 6. Cross-cutting guidance

- **Always use `web-next/src/lib/api/client.ts`'s `apiFetch`/`apiRequest`, never raw `fetch`.** Tools' `readApi`/`writeApi` (`workspace-tools-api.ts`) called `fetch()` directly until #83, so an expired access cookie read as "signed out"; they wrap `apiFetch` now.
- **Organization scoping**: the header switcher sets `?org=` and reloads (`window.location.assign`); `WorkspaceShell` resolves `selectedOrganization` from `session.organizations.find(o => o.id === org)`. Every component that needs the current org should **receive it as a prop from the shell** (as `WorkspaceToolAccessSettings`/`WorkspaceTeamsSettings`/etc. correctly do), not re-derive it. `WorkspaceTools` broke this until #83 — it read `GET /v1/auth/me` itself and used `organizations[0]`, so a member of several organizations connected apps in the wrong one; it now takes `organizationId` from the shell, and its connect round trip keeps `?org=`.
- **Query keys**: existing convention is an array starting with the domain (`["auth","session"]`, `["organizations", orgId, "members"]`, `["settings","teams",orgId,...]`, `["billing","summary",orgId]`). Follow it; invalidate the narrowest matching key on mutation success (e.g. team-member mutations invalidate `["settings","teams",orgId,selectedId,"members"]`, not the whole `"teams"` branch).
- **Error/empty states**: every wired list handles `isPending`/`isError`/empty distinctly with inline copy (see `people-management.tsx`, `workspace-teams-settings.tsx`); a 404 on a feature-flagged/optional service (agent grant, preferences, billing, teams) is treated as "not available in this environment," not a generic error. Reuse `authErrorMessage(error, fallback)` from `lib/auth/client.ts` rather than inventing new copy per component.
- **Preview mode is dev-only and per-section, not global.** `WorkspaceShell`'s `preview` prop only turns on via `?preview=people` (gated by `import.meta.env.DEV`) or the dev-only auth-service-unreachable fallback in `routes/index.tsx`. There is **no header preview badge** — DESIGN.md explicitly forbids one (`web-next/DESIGN.md` §2: "No preview badge in the header"); the actual pattern is small inline "Layout preview · ..." notices per section (Tools, Tool access, Teams, Billing settings). `web-next/README.md`'s "Preview badge" wording is stale relative to DESIGN.md and the code — follow DESIGN.md.
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
- Role and workspace `<select>`s commit in `onChange`; arrow keys on a closed select change a member's role at once on Windows (`people-management.tsx`, the header switcher, team roles). Commit on an explicit choice instead.
- The invitation page has no "use another account" for someone signed in with the wrong email (`web/`'s had one).
- Teams settings keeps a team's `can_manage` after its admin demotes or removes themselves; refresh the team detail and list too.
- `BILLING_URL` still sends Dodo's return to `web/`'s `/settings/billing`; web-next has no such route.
- The example create flows use `crypto.randomUUID()`, which is missing over plain http on a LAN address; use `crypto.getRandomValues` (as `createOrganization` does) when these become real.
