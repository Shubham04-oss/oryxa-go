# Browser and live layer

Read-only code investigation, 2026-10-25, branch main. Nothing here was run against production: every "Why" is from reading the file and line cited. Guesses are marked **(guess)**. Paths are relative to the repo root.

## How it fits together

- **Who creates a browser.** Krude's `browser_*` tools. The backend is picked per organization by `LiveOnCloudflare` (`agents/primary/internal/serviceconfig/config.go:40`, wiring in `agents/primary/internal/assembly/live.go:37-58`).
- **Cloudflare orgs (the owner's).** The key is (thread room, member): `cfbrowser/browser.go:107-111` names the browser by member, and `edgeclient/target.go:60` makes the room the ADK session id. So one browser per member per chat. The room's `Children` table (`edge/src/room/children.ts`) tracks it, `BrowserDO` (`edge/src/browser/browser.ts`) holds the Browser Run session id in DO storage, and `PuppeteerDriver` (`edge/src/browser/driver.ts`) talks to Browser Run.
- **Other orgs (AWS AgentCore).** The key is app, user and ADK session (`agentcorebrowser/toolset.go:174-176`), so one browser per chat. The session lives in an in-memory map in the ECS task (`agentcorebrowser/browser.go:120-142`). Nothing on the edge or in web-next knows about it.
- **What the UI knows.** Only the room's `children` rows (`status` running or stopped, `lastUsedAt`), read through `listChildren` (`web-next/src/lib/live/children.ts:76`). There is no check against the real session.
- **Cross-chat sharing is ruled out by the keys.** Chat A and chat B never share a browser on either backend. Two runs inside one chat do share one (B11).

Most severe first.

---

### B1 The UI shows "running" from a bookkeeping row, never from the real session

- **Severity:** high
- **Kind:** confusing
- **Seen:** owner report ("browser remains active even if the session is old"); code reading

**What happens.** The Live tab count (`Live 1`), the green "Running · used 9 min ago" tile and the "Running" header all say a browser is live. The real Browser Run session may already be gone, or may have died with the failed call that ended the chat's work. The row stays "running" until the room's idle sweep, up to 10 minutes after the last call. Reopening an old chat inside that window still shows a live browser.

**Why.**
- `status` is written only by `Children.touch` and `setStatus`. `touch` marks running on any successful op (`edge/src/room/children.ts:164-166, 270-286`) and `setStatus` marks stopped from `reset`, `stopChild` and the sweep (`:167, 288-290, 320, 347-352`). Nothing reads the real session state.
- The sweep limit is `BROWSER_IDLE_MS = 10 min` (`edge/src/browser/browser.ts:38`, `children.ts:371-373`). Chat end, tab close, member leaving the page and agent errors never reach it.
- On the web side, `running = child.status === "running"` (`web-next/src/components/browser-pane.tsx:65`, `live-work.tsx:72`). The list is fetched once and re-fetched only when the room log carries a `sandbox.*` or `browser.*` event (`live-work.tsx:40-48`). With no open log socket (reconnecting, tab in the background) the tile stays stale.
- The tab count (`agent-session-work.tsx:85-90, 105`) counts the same rows and opens a room-log WebSocket per open chat just to do so (`live-work.tsx:37-39`).

**Fix.** The row cannot know. Either have `GET children` ask `BrowserDO` whether its session is alive (a cheap `driver.open` is not read-only, see B3, so add a real `status` op that only does `puppeteer.connect` or a sessions list), or show the row as "last used N min ago, closes after 10 min idle" instead of "Running". Also stop reporting a chat's browser as running once the chat has been quiet for the idle limit (client-side clock on `lastUsedAt`).

---

### B2 "Stopped" in the UI does not mean the remote browser is closed (idle sweep, reset and stop are no-ops after the DO is evicted)

- **Severity:** high
- **Kind:** broken
- **Seen:** code reading; this is the real-lifetime side of the owner's report

**What happens.** After 10 idle minutes the room marks the browser "stopped" and logs "Browser closed (idle)". The Browser Run session, with its cookies and signed-in pages, can keep running for roughly another keep-alive window. `browser_reset` ("permanently clears cookies") has the same hole: it forgets the session id and reports success while the old session stays alive until Browser Run's own timer.

**Why.**
- `BrowserDO.driver()` builds a fresh `PuppeteerDriver` on first use after every DO restart or eviction (`edge/src/browser/browser.ts:96-99`). Its `browser` field starts `null`.
- `stop()` (`:75-88`) and `reset` (`:200-206`) call `this.driver().close()`, which only closes `this.browser` if set (`edge/src/browser/driver.ts:123-128`). On a fresh driver it does nothing, then both paths delete the stored session id (`browser.ts:81, 203`). They never `puppeteer.connect` to the stored id first.
- A DO is evicted within about a minute or two of its last call, so by the 10-minute sweep the driver is almost always cold. The only thing that ends the session is the launch-time `keep_alive` of `BROWSER_IDLE_MS + 60_000` (`browser.ts:97`, `driver.ts:55`).
- The sweep also trusts the answer blindly: `stopChild` marks the row stopped whatever the child returned, even a 500 (`children.ts:312-320`).

**Fix.** In `stop()` and `reset`, when the driver has no live `browser` and a session id is stored, `puppeteer.connect(binding, sessionId)` then `close()` it (swallowing "already gone"). Only delete the id after that. Make `stopChild` keep the row running when the child reply is an error.

---

### B3 Opening the Live pane can launch a new, blank browser and keeps it billed

- **Severity:** high
- **Kind:** brittle (works sometimes)
- **Seen:** code reading

**What happens.** A member opens a chat whose tile still says "Running" but whose remote session has died. The pane asks for a live link. The edge starts a brand-new browser (about:blank), returns a link to it, and the member sees an empty page. A `browser.started` event is logged and the idle clock restarts, so a look at an old chat creates a new billed browser.

**Why.**
- The pane queries `POST .../live` as soon as `running` is true (`web-next/src/components/browser-pane.tsx:69-94`).
- The `live` op is `web: true` but not `readOnly` (`edge/src/room/children.ts:76`), so it counts as use and calls `touch` (`:164-166`), refreshing `last_used_at`.
- Inside `BrowserDO`, `live` runs `this.session(events)` (`edge/src/browser/browser.ts:207-212`). `session()` reconnects or, when the stored id is dead, launches a new session (`:114-128`, `driver.ts:42-59`).
- A web member (watching, not driving) therefore has a code path that starts a browser.

**Fix.** Give `live` a "reconnect only, never launch" mode: if `driver.open` would have to create a session, return a conflict ("this browser has stopped") and let the pane show its stopped message. Mark it `readOnly` so watching does not extend the idle clock.

---

### B4 AWS orgs: the browser outlives the chat, with no idle stop, no UI and no way to end it

- **Severity:** high
- **Kind:** brittle (works sometimes)
- **Seen:** code reading

**What happens.** For every org not on Cloudflare, a browser started in a chat runs for the whole configured session TTL (default 15 minutes, configurable up to 8 hours) from when it started, whether or not anyone uses it. The member cannot see or stop it: the Live tab is driven by the edge's room rows, which AWS sessions never create.

**Why.**
- Start passes a fixed `SessionTimeoutSeconds` (`agents/primary/internal/agentcorebrowser/browser.go:262, 482`). There is no idle timer and no `Stop` except `Reset` (member-confirmed tool call, `toolset.go:161-172`) and process shutdown (`browser.go:299-321`, `assembly/live.go:120-125`).
- Chat end, closing the tab, the member leaving and the agent erroring call nothing. Eviction of expired entries happens only on the next `acquireSession` from some chat (`browser.go:342-386`), and just drops the local entry.
- The TTL is `AGENTS_PRIMARY_BROWSER_SESSION_TIMEOUT_SECONDS`, default 15 min, max 8 h (`serviceconfig/config.go:318`). If someone raises it to make long tasks work, an abandoned signed-in browser stays alive for hours.
- The web app never shows these sessions (no AWS live view is wired anywhere under `web-next/src/lib/live/`).

**Fix.** Add an idle timer to the Manager (stop a session unused for N minutes, a sweeper goroutine or `time.AfterFunc` reset on each Invoke), keep TTL as the backstop, and surface "browser active" through the same children list the edge serves (or at least document that AWS orgs have no Live view).

---

### B5 AWS: a dropped automation socket is never repaired; every later call in that chat fails

- **Severity:** high
- **Kind:** brittle (works sometimes)
- **Seen:** code reading

**What happens.** If the WebSocket to AgentCore breaks while the session sits idle (NAT or load-balancer idle drop, a command timeout), later `browser_*` calls in that chat return an error until the session reaches its expiry cushion and is recreated. The agent says the browser is broken; resetting needs member confirmation.

**Why.**
- `Invoke` returns the error and keeps `entry.automation` (`agentcorebrowser/browser.go:237-240`). Recreation is triggered only by `entry.id == ""`, `automation == nil` or the TTL cushion (`:230`).
- The CDP client has no ping or read loop; it reads synchronously per command (`cdp.go:391-445`). A write deadline hit on gorilla's connection is terminal for that connection **(guess on gorilla behaviour)**.
- There is no distinction between a page error ("no element matches") and a transport error.

**Fix.** On a transport error (send or receive failure, not a CDP error response), close the automation client and set `automation = nil` so the next call reconnects to the same session id, and recreate if that fails.

---

### B6 Browser state is silently lost on every deploy (AWS) and on session expiry (both)

- **Severity:** medium
- **Kind:** brittle (works sometimes)
- **Seen:** code reading

**What happens.** A member signs in to a site during a handoff, the agent continues, and then the login disappears: a restart of the agents task, or 15 minutes after the browser started (AWS), gives a fresh empty browser. The model is told `session_recreated` but the member is not.

**Why.**
- AWS sessions live only in the process map (`browser.go:132`). The CD pipeline releases on every push to main (project memory: cd-live-2026-09-28), and a new task starts empty. A hard-killed task also leaves the old AWS session running until its TTL, because `Close` is best effort (`browser.go:297-299` says so).
- Even in one process, the session is recreated at TTL minus a cushion of at most 30 s (`:230, 404-410`), a fixed lifetime from start, not from last use. A browser in active use at minute 15 gets replaced, dropping cookies.
- On Cloudflare the stored id lets the DO reconnect (`driver.ts:44-50`), but a session past its keep-alive is replaced the same way (`browser.ts:123-126`, `sessionRecreated`).

**Fix.** Make the AWS timeout slide (re-create only when idle, or use a longer TTL with the idle reaper from B4), and tell the member in chat or in the pane when a login was lost.

---

### B7 The live-view link expires after 15 minutes and the pane never fetches a new one

- **Severity:** medium
- **Kind:** broken
- **Seen:** code reading

**What happens.** A member leaves the Live pane open on a running browser. After about 15 minutes the embedded view stops updating or shows an expiry page. Nothing prompts a refresh until they leave the pane and come back.

**Why.**
- The edge issues links valid for `LIVE_VIEW_MS = 15 min` (`edge/src/browser/browser.ts:39, 207-212`).
- The query has a `staleTime` computed from `expiresAt` minus one minute but no `refetchInterval`, and `refetchOnWindowFocus: false` (`web-next/src/components/browser-pane.tsx:82-93`). `staleTime` only decides whether a refetch is needed when something triggers one.
- The iframe is keyed by URL (`:225`), so it would swap cleanly if a new link did arrive.
- What an expired link shows is a **(guess)**: an empty or error page inside the frame.

**Fix.** Add `refetchInterval` derived from `expiresAt - margin` while the browser is running (only while the pane is mounted and visible), and let it use the reconnect-only behaviour from B3.

---

### B8 "Needs a hand" can stick on after the handoff timed out, and Hand back can fail forever

- **Severity:** medium
- **Kind:** brittle (works sometimes)
- **Seen:** code reading

**What happens.** The agent asks for help (a login, a captcha). The member does not respond, or responds after a delay. The banner "needs a hand" stays even after the handoff window is over, and pressing "Hand back" shows "Couldn't hand the browser back. Try again." on every attempt.

**Why.**
- No event is written when a handoff simply times out. `askedForHand` reads "latest handoff event wins" (`web-next/src/lib/live/children.ts:225-235`), and the banner needs only that plus `running` (`browser-pane.tsx:125-129`).
- The handoff-state query has `staleTime: Infinity` and refetches only on a log event (`:95-102`).
- On the edge, `handoff/state` does not open the session. After the DO restarts the driver has no page, `handoffState()` throws, and the `.catch` turns that into `{ active: true }` (`edge/src/browser/browser.ts:225-234`), so a dead or expired handoff reports active.
- `resume` calls `session()` (`:239`). If the session had died, that launches a fresh one and deletes the stored handoff (`:123-125`), then `handoffComplete` runs on a browser with no handoff and presumably throws **(guess about Cloudflare's CDP behaviour)**. The next try gets "no handoff is open" (`:237-238`).

**Fix.** Write a `browser.handoff.timeout` event when the stored handoff is older than its timeout (the DO already knows `startedAt`, `browser.ts:53-58, 220`), make `handoff/state` report inactive when the session cannot be reached, and have the pane treat a "no handoff is open" conflict on resume as done.

---

### B9 The Live tab says "Couldn't load" on any chat that has not used a browser, and always on AWS orgs

- **Severity:** medium
- **Kind:** confusing
- **Seen:** code reading

**What happens.** A member opens the Live tab on a new chat, or on any chat in an AWS org. They see a red-flag message, "Couldn't load this conversation's live work", instead of "Nothing live yet".

**Why.**
- The edge room for a thread is created by the first agent call, not by the web (`edge/src/room/room.ts:113-135, 236-237`). A web caller on a room that does not exist gets `not_found`.
- `useRoomChildren` uses `retry: false` (`web-next/src/components/live-work.tsx:35`), and the tab shows its error branch on `children.isError` (`agent-session-work.tsx:145-148`). The friendly empty message at `:150-155` only shows when the call succeeded with an empty list. (`RoomLive` has the same split, `live-work.tsx:343-350`, but its copy there is already friendly.)
- AWS-org chats never create an edge room, so they hit this every time.

**Fix.** Treat a 404 from `listChildren` as an empty list in `useRoomChildren`, and for AWS orgs say plainly that the browser is not viewable.

---

### B10 A browser that fails to start leaves a "Stopped" tile and no reason anyone sees

- **Severity:** medium
- **Kind:** confusing
- **Seen:** code reading

**What happens.** The agent calls `browser_navigate`, Browser Run refuses (concurrency limit, outage, bad request). The agent gets an error and may retry or give up. In the Live tab a "Browser: Stopped, used just now" tile appears, with no event in the run log saying why. The member never learns that the browser could not start.

**Why.**
- `session()` throws `unavailable` "the browser did not start" (`edge/src/browser/browser.ts:117-121`) and pushes no event, so `childError` returns events that do not include the failure.
- The room still calls `touch(kind, name, context, running=false)` for a failed reply, which inserts the row as `stopped` (`edge/src/room/children.ts:164-166, 270-286`).
- `navigate` also maps every navigation error to `invalid_request` (`browser.ts:152-154`), so "site blocked us" and "malformed URL" look the same.
- On the Go side the call is not retried for non-idempotent actions (`cfbrowser/browser.go:113-126, 147`) and the error text goes to the model, not the member.

**Fix.** Emit a `browser.failed` event with a short reason when `session()` or navigation fails, and do not insert a row for a browser that never ran.

---

### B11 No cap across chats; two runs in one chat drive one browser

- **Severity:** medium
- **Kind:** brittle (works sometimes)
- **Seen:** code reading

**What happens.** Every chat gets its own browser, and the only cap is per room (three running browsers in one room). A member with many chats open, or an agent triggered often, can hold many concurrent Browser Run sessions. Separately, two runs in the same chat (an agent and a delegate, a retry after a timeout) share one page and can navigate it out from under each other.

**Why.**
- `MAX_RUNNING_BROWSERS = 3` is counted inside one room (`edge/src/room/children.ts:36-37, 201-205`). A thread room has one member, so the cap rarely bites. There is no org, member or account limit anywhere on the edge or in Go.
- The browser name is just the member id (`cfbrowser/browser.go:107-111`); the run and agent are claims, not part of the key (`edgeclient/target.go:36-65`).
- Calls queue per DO (`browser.ts:90-94`) so they do not interleave mid-call, but nothing stops one run's navigate from landing between another run's snapshot and click.
- On AWS, calls in one session serialize on `entry.mu` (`agentcorebrowser/browser.go:226-227`), same effect.
- How many concurrent sessions the Cloudflare account allows is not in the repo **(open question)**.

**Fix.** Add a per-member (or per-org) running-browser cap on the edge, checked next to `checkLimit`, and stop the oldest idle one when it is reached. For same-chat sharing, at minimum note which run last drove it, or key the browser by run for delegates.

---

### B12 Costs: sessions are billed for roughly the keep-alive after last use, and every one is recorded

- **Severity:** medium
- **Kind:** polish
- **Seen:** code reading

**What happens.** A single page visit costs the launch plus the whole keep-alive window, and every session is recorded by Browser Run, including anything the member types while they have control (a password in a takeover).

**Why.**
- The session is launched with `keep_alive: BROWSER_IDLE_MS + 60_000` (11 min) and `recording: true` always (`edge/src/browser/driver.ts:55`). Given B2, nothing shortens it.
- The sweep is the only explicit close and it is a no-op when cold (B2).
- The handoff "full" live link lets the member type into the page (`web-next/src/components/browser-pane.tsx:67, 168-176`) and the recording keeps what the session shows **(guess: that credentials appear in the recording)**.
- Screenshots are also written to R2 under the run (`edge/src/browser/browser.ts:188-199`).
- 660000 ms may exceed Browser Run's documented keep-alive maximum **(guess; I recall about 10 minutes)**. If it is rejected, `launch` would throw and every start would fail with B10's silent error. Since the owner reports a working browser, this is more likely clamped silently.

**Fix.** Close explicitly (B2), lower `keep_alive` to what the sweep needs (the 10-minute idle, or less), and make recording opt-in or off while a member has control.

---

### B13 A live-view link is a bearer link, and the control link stays valid after hand back

- **Severity:** medium
- **Kind:** brittle (works sometimes)
- **Seen:** code reading

**What happens.** The pane puts the signed live-view URL in an iframe and in an "Open in a new tab" `href`. In "full" mode that link controls a browser that holds the member's logins. After "Hand back" the pane switches back to watch mode but the control link is still good until its expiry.

**Why.**
- `liveView(mode, expiresInMs)` returns Cloudflare's `devtoolsFrontendUrl` (`edge/src/browser/driver.ts:105-108`). Whether that URL needs any further auth is not visible here **(guess: it does not)**.
- The handoff link lives `max(timeout, 15 min)` (`edge/src/browser/browser.ts:221-223`), up to 10 minutes of handoff, and `resume` never revokes it (`:235-250`).
- The link is visible in the DOM and the new-tab anchor (`browser-pane.tsx:224-243`).

**Fix.** Short expiry for "full" links and refetch them (B7), and close the handoff view at `resume` if Cloudflare offers a way (check the `Cloudflare.*` CDP methods).

---

### B14 A failed stop can spin the room's alarm

- **Severity:** medium
- **Kind:** brittle (works sometimes)
- **Seen:** code reading (trigger rare, **guess**)

**What happens.** If the browser DO throws while being stopped (for example during a deploy), the room keeps the child "running" with an old `last_used_at`. The next sweep time is already in the past, so the alarm is set to fire immediately and loops, logging an error each time, until the stop works.

**Why.**
- `stopChild` returns early on a thrown error without changing status (`edge/src/room/children.ts:316-319`).
- `nextSweep` computes `last_used_at + idleLimit`, which is in the past, and `alarm()` sets the alarm to the minimum of the due times (`edge/src/room/room.ts:550-563`).
- There is no back-off or retry counter.

**Fix.** On a failed stop, bump `last_used_at` (or store a `retry_after`) so the next attempt is a minute away, and cap retries.

---

### B15 The home "live" pill is the parley pill, and it stays lit after a dropped connection

- **Severity:** medium
- **Kind:** confusing
- **Seen:** code reading

**What happens.** The pill on home has nothing to do with the agent's browser. It says "Parley in <teamspace>" with a duration. If a member closes their laptop or loses the connection without pressing Leave, the pill keeps saying the parley is live to everyone.

**Why.**
- `HomeLivePill` and `AgentPanelChips` read only calls: `readCall(...).participants.length` every 60 s (`web-next/src/components/home-live-pill.tsx:89-107`; `agent-panel-chips.tsx:78-125`).
- The edge keeps a participant's seat on a dropped connection until they leave or the call ends: "A dropped connection is not a leave" (`edge/src/call/index.ts:361-365`). Only `leave`, a member ending the call, or the meeting ending close it (`:125-131, 260-275`).
- It caps at eight teamspaces (`home-live-pill.tsx:19`), so a ninth live parley is invisible.
- Whether the web app sends `leave` on tab close is not checked **(open question)**.

**Fix.** Expire a seat after a window with no heartbeat or webhook reconnect, or show "N min since last seen". If owners expect the pill to mean "agent browser live", say so in a separate indicator.

---

### B16 Live badge and tiles disagree with each other and with the pane

- **Severity:** low
- **Kind:** confusing
- **Seen:** code reading

**What happens.** The tab badge counts rows marked running (`agent-session-work.tsx:90, 105`). A tile shows "Needs a hand" in place of "Running" (`live-work.tsx:89-92`). Inside the pane the header says "Running" (`:188`) while the body can say "This browser isn't running, or it's private to someone else" (`browser-pane.tsx:218-221`) for one failed live link. The last message guesses at two unrelated causes for any error, including a plain network failure, since `retry: false` (`:93`).

**Fix.** Distinguish 403, 404 and network failures in the pane's message, and let the header follow the live query rather than the row.

---

## Open questions (only a live test settles these)

1. After the owner's org uses a browser once, wait 12 minutes with no calls. Is the Browser Run session actually gone (dashboard or API session list), and was it gone before the room's "Browser closed (idle)" event? (B2, B12)
2. Does `launch({ keep_alive: 660000 })` work as written, or does Browser Run clamp or reject it? (B12)
3. Open Live on a "Running" tile whose session has expired. Does it show a blank new browser and a `browser.started` event? (B3)
4. What does the live-view iframe show after the link expires? Is the link usable without any other auth? (B7, B13)
5. Does Browser Run keep a session alive while a `puppeteer.connect` socket from an idle DO is still open, or only after disconnect? This decides how long a session really outlives the last call. (B2)
6. Does a handoff left unanswered stop reporting "active" on its own, and what does `Cloudflare.handoffComplete` do on a session with no handoff? (B8)
7. What is the account's concurrent Browser Run limit, and what error comes back at the limit? (B10, B11)
8. On an AWS org: leave a chat idle and check the AgentCore console for the session's life after the last call, and after an ECS deploy. (B4, B6)
9. Does web-next send `call/leave` on tab close or reload? (B15)
