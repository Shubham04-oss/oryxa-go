# Signed-out browser loop QA

Local fixture QA on 2026-09-30 uses the actual Home, WorkspaceShell, SignIn
route, query context, AuthShell and styles. An external client-only Vite harness
and local HTTP fixture supply disposable session data; neither is included in
the product. The transient screenshot exercises the production error branch.

## Browser observations

- An initial protected 401 followed by refresh 401 shows **Your session has
  ended.** and **Sign in again**.
- The link keeps `/?org=org_auth_fixture` as its return destination. Signing in
  with fixture credentials returns to Today. Expiring that already-open
  workspace removes its private UI and shows the terminal state again.
- After the terminal state, fixture request counters remain unchanged for at
  least 90 seconds (three former 30-second polling periods). The expiry burst
  contains one Inbox read, one sent-requests read and one refresh; the later
  observation contains exactly the same counts.
- A refresh 503 follows bounded backoff and reaches **We couldn't reach your
  workspace**, with **Try again**, rather than ending the session. Restoring
  the fixture and choosing Try again opens Today.

| View | Viewport | Evidence |
| --- | --- | --- |
| Terminal state, desktop | 1099 × 807 | [desktop.png](desktop.png) |
| Terminal state, phone | 423 × 927 | [phone.png](phone.png) |
| Recoverable transient error | 1099 × 807 | [transient-desktop.png](transient-desktop.png) |

## Automated coverage

The combined 18-file targeted set passes 362 tests. Each independent browser
test starts authenticated; the shared fixture resets only at `beforeEach`, so
terminal state persists across all calls inside a test. The Inbox refusal
matrix uses separate cases, and expiry fixtures explicitly return refresh 401.

The focused API/auth/apps/threads/request and actual-route tests cover browser
refresh single flight, isolated SSR refresh and query caches, failed and
successful sign-in, stale refresh/header/body/clone results, aborted private
HTTP and streams, public auth availability, bounded transient retries and the
return URL. The route integration uses the real Agents SDK hooks, replacing
only native WebSocket transport. It proves live polling before expiry, socket
closure on terminal unmount, no reconnect after native close events, no HTTP
after 90 seconds plus focus/online/visibility events, cleared private cache,
and preserved scoped conversation choice.

The browser fixture does not exercise production sign-in, Safari, live OAuth
or Durable Object streaming. Stream cancellation and SDK cleanup are covered
by automated tests. Existing mount-local drafts and Not sent rows are not
recovered across a terminal unmount followed by a new sign-in; this change
retains scoped chat choices and the existing request queue without adding new
draft persistence.
