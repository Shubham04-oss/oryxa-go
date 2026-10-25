# 01 Teamspaces

Area: the teamspace page in `web-next/` (room, parley, plan, files, activity, invite), the edge call and voice code in `edge/src/call/` and `edge/src/voice/`, the web Worker's room object `web-next/src/teamspace-room-agent.ts`, and the Go side in `agents/primary/internal/rooms/`, `edgedispatch/` and `httpserver/edge_*.go`.

Read at `main` (dfe3eacb). Nothing here was run against production; every "Seen" marked *code reading* is from the source only. Line numbers are from that commit.

## How a parley works end to end

A **parley is a call**, not a chat feature. The pieces:

1. **Button.** `ParleyButton` (`web-next/src/components/call-surfaces.tsx:307`) calls `join()` in `call-provider.tsx`. `join` does `POST /live/<org>/rooms/teamspace/<id>/call/join` and gets a RealtimeKit token (`lib/call/client.ts:65`).
2. **Worker.** The web Worker (`web-next/src/server.ts:776`, `edge-proxy.ts:108`) asks Go who the member is on every `/live/*` request, then forwards to the edge through a service binding.
3. **Edge.** `RoomDO` hands `/call*` to `edge/src/call/index.ts`. `join` (`index.ts:212`) creates the RealtimeKit meeting, starts the per-room `VoiceDO`, and adds the member as a participant.
4. **Media.** The browser joins RealtimeKit (`lib/call/media.ts`). RealtimeKit transcribes each speaker **in that speaker's browser**. `call-provider.tsx` buffers the member's own segments and POSTs them every 1.5 s to `/call/transcript` (`call-provider.tsx:596-608`, `edge/src/call/transcript.ts:32`).
5. **The agent in the call.** `VoiceDO.hear` (`edge/src/voice/voice.ts:166`) answers only when the text addresses `VOICE_AGENT_NAME` ("Krude"). It uses Gemini from the edge (`voice/brain.ts`), and hands real work to Go with `POST /v1/edge/dispatch` (`voice/dispatch.ts:78`, `httpserver/edge_dispatch.go:47`). Go posts the answer back to `/voice/result`. The answer is spoken, but only when the asker is alone in the call. Otherwise it is private (`voice.ts:204-222`).
6. **End.** The last leaver, or "End", runs `endCall` (`index.ts:281`). It writes the live transcript to R2 (`transcript.ts:96`). **Nothing else happens.**
7. **Notes back to the UI.** Go has `POST /v1/edge/teamspaces/{id}/parley-notes` (`httpserver/edge_notes.go:16`) and a `ParleyNotesCard` in the room (`components/parley-notes-card.tsx`). **No edge code ever calls that route** (T1).

The teamspace **chat** is a different system. It runs on a Durable Object in the web Worker (`teamspace-room-agent.ts`) that polls Go. The parley's own state (`/call`, `/log`, the `/live` socket) is on the edge. They share a page and a room id, nothing else.

---

### T1 Parley notes are never produced; the UI promises them

- **Severity:** blocker
- **Kind:** broken
- **Seen:** code reading; consistent with the owner's "sometimes it does nothing". The owner should confirm that no parley has ever left a notes card.

**What happens.** A member starts a parley, talks, leaves. The call dock says the agent is "taking notes". When the call ends the member gets "Parley ended / What's kept from <room>" with only a Done button. No notes card ever appears in the room. The Parleys tab says "Past parleys will be listed here".

**Why.**
- Go's receiving end exists: `httpserver/edge_notes.go:16-67` and `rooms/notes.go:46`. A search of all of `edge/src` for "parley" or "notes" finds nothing. The edge never builds, summarizes or posts notes. Nor does any other service; `git log -S"parley-notes" -- edge` is empty.
- The edge token the voice agent signs only carries scope `call:speak` (`edge/src/voice/dispatch.ts:55`). The notes route requires `log:write` (`edge_notes.go:40`). So even a sender added to the edge would get 403 until the signer changes.
- The notes route is for teamspace rooms only (`edge_notes.go:44`). It also returns `ErrPaused` for a paused teamspace (`rooms/store.go:186-215`), and the edge has no retry or outbox for it.
- The UI says otherwise: "Taking notes" (`call-surfaces.tsx:239` and `:993`). The ended popup's own comment says the notes "join it with an API for the notes" (`call-surfaces.tsx:619-623`). That part was never built.
- The live transcript is saved to R2 (`transcript.ts:96-129`), but no route or screen reads it back.

**Fix.** Add a notes step to `endCall` in `edge/src/call/index.ts`. Run it with `waitUntil` after `saveLiveTranscript`: summarize `store.segments()` into `{call_id, duration_ms, decisions, actions}`, then POST it to Go with a token that has `log:write` and the room claim. Keep it in a small pending table in the room DO so a 5xx or a paused room is retried. Until that exists, change "Taking notes" to "Listening", so the UI does not promise it.

---

### T2 The agent in the call is mute in production: the voice model key is missing

- **Severity:** blocker
- **Kind:** broken
- **Seen:** PR #514's own body (the production deploy log); code reading.

**What happens.** A member says "Krude, what's on our plate?". Nothing is said back. No error, no "I didn't catch that". Join and talk work, so the call looks healthy.

**Why.**
- PR #514 says: "Still missing on the edge: `GEMINI_API_KEY` for the voice model." Production vars are in `edge/wrangler.jsonc`. The key is a secret, so it is not in the repo.
- `Gemini.fromEnv` returns `undefined` without the key (`edge/src/voice/brain.ts:50-53`). Then `VoiceDO.respond` does a `console.warn` and **returns without speaking** (`voice.ts:241-244`). Nothing reaches the call.
- Until #514's key rotation, the edge's `EDGE_SIGNING_KEY` was lost too. A dispatch with a bad key makes the agent say "I can't reach my tools right now" (`voice.ts:282`). That one is audible. The missing model key is not.
- The same silence happens when the Workers AI binding is missing: `speak` logs "speech not configured" and says nothing (`voice.ts:346-355`).

**Fix.** Set `GEMINI_API_KEY` on the production edge. Make a missing model or speech provider loud. Put one `voice.state` frame with `present:false` or an `error` field into the call, and show it in the UI, instead of a `console.warn`. Add a health line to the edge's `/call` view so the dock can say "Krude can't speak right now".

---

### T3 The agent only wakes on the word "Krude", which nothing in the UI says, and which is not the agent the room shows

- **Severity:** high
- **Kind:** brittle (works sometimes)
- **Seen:** code reading; fits "hit or miss".

**What happens.** Sometimes the agent answers and sometimes it does not, depending on how the speech recogniser spelled the name and where in the sentence it came. The teamspace shows agents called "Arlo" or "Maya" (the members' own agents). The call listens for a different name.

**Why.**
- `addressedTo` (`edge/src/voice/address.ts:27-35`) needs the exact word from `VOICE_AGENT_NAME` within the first 4 or last 3 words. Otherwise it needs a question within 10 s after the agent spoke (`FOLLOW_UP_MS`, `address.ts:9`, `voice.ts:175`). "Hey crude", "Kroode", a long preamble, or a statement are all ignored, silently. The file's own header says it "errs on the side of staying quiet".
- The name is one global env var (`wrangler.jsonc`, `VOICE_AGENT_NAME: "Krude"`). The room's agents are named per member by Go (`managedagents/store.go:497-520`). A member who sees "Arlo" has no reason to say "Krude".
- The follow-up state is in the DO's memory: `lastAnswered` and `lastSpokeAt` (`voice.ts:66-68`). They are lost when the DO is evicted or redeployed.
- Nothing in the join strip or dock tells the member how to address the agent. Dock text is "taking notes" (`call-surfaces.tsx:993`).

**Fix.** Show the wake word in the dock ("Say 'Krude, …'"). Add a "type to the agent" or push-to-ask control as a fallback. Accept the room agent's actual name as an alias, and a few fuzzy variants. A clear failure cue helps too: a short chime or caption "Krude heard: …" when addressed, so a miss is visible.

---

### T4 Speech reaches the agent only if the member's own browser forwards it, and errors are swallowed

- **Severity:** high
- **Kind:** brittle (works sometimes)
- **Seen:** code reading.

**What happens.** The agent, and the notes if they are ever built, only know what each member's browser managed to upload. If that member's upload fails, or their transcription is off, their words are missing with no sign to anyone.

**Why.**
- Each client forwards **only its own speaker**. `call-provider.tsx:589` drops every segment that is not the member's. The edge refuses anything else (`edge/src/call/transcript.ts:44-47`).
- `buffer.take()` removes the segments **before** the POST. On any failure the `.catch(() => undefined)` at `call-provider.tsx:607` throws them away. There is no retry and no counter.
- On Leave, `buffer.clear()` (`call-provider.tsx:612`) drops whatever has not been flushed (up to 1.5 s plus anything stuck).
- Partial segments are never forwarded to be stored. Only final ones are (`transcript.ts:54`, and the `partial` flag at `call-provider.tsx:594`). Barge-in uses partials, so it only works while the upload works.
- Transcription depends on a RealtimeKit preset with `transcription_enabled`. That preset is created by hand (`edge/src/call/README.md`, "The member preset"). Production "shares staging's RealtimeKit app" (`wrangler.jsonc` comment). Whether the preset exists in that app is not something the repo can show (open question).
- "Listen in" joins with the mic off (`call-surfaces.tsx:567`). That member contributes nothing to the transcript, which is correct, but nothing tells the others.
- Every flush is a `/live/*` request, so each one costs an uncached Go lookup in the Worker (`server.ts:776-785`). It is a lot of extra requests during a call.

**Fix.** Keep the segments until the POST succeeds (re-queue on failure, bounded). Flush on Leave with `sendBeacon`/`keepalive`. Count failed flushes and show "Your words aren't reaching the room" on the dock. Add a RealtimeKit-side fallback: the edge already receives RealtimeKit's post-call transcript by webhook (`transcript.ts:134`). Use it as the notes source when the live one is short.

---

### T5 The room fires three `/log?after=0` reads, three sockets and repeated `/call` reads on every open

- **Severity:** high
- **Kind:** slow
- **Seen:** prod walkthrough (three parallel `log?after=0`, later three `log?after=9`; `/call` repeated, some aborted); root cause from code reading.

**What happens.** Opening a teamspace fetches the room's whole log three times at once, opens three sockets to the same room, and later each of the three reads the log again. `/call` is refetched and cancelled in between.

**Why.**
- `useRoomCall` creates its **own** `useRoomLog` every time it is called (`lib/call/use-room-call.ts:36-38`). On a teamspace page it is called by `ParleyButton` (`call-surfaces.tsx:324`) and `ParleyStrip` (`:387`). `agent-panel-chips.tsx:84`, `workspace-sidebar-live.tsx:82`, `call-surfaces.tsx:589` and `call-stage.tsx:311` add more once a call is joined. Each instance has its own state, its own `after=0` read and its own WebSocket (`lib/live/use-room-log.ts:57`, `:111`). That is the "three".
- After a socket opens, each instance runs `catchUp()` again (`use-room-log.ts:122`). That is the later `after=9`, three times.
- Each instance replays the whole log, and `useRoomCall` then looks for *any* old `call.*` event (`use-room-call.ts:39-41`). A room with any call history has one, so the effect invalidates `/call` straight after load (`use-room-call.ts:43-49`). Three instances invalidate three times. TanStack cancels the in-flight read each time, which gives the aborted `/call`s.
- The log is read from `after=0` every time, and `transcript.segment` events are one event each (`edge/src/call/transcript.ts:57-63`). After a few parleys the log is large. `CATCH_UP_PAGES = 20` pages of 500 (`use-room-log.ts:14`, `:69`) is a silent cap; past it the loop just stops without error.
- `LiveWork` adds two more `useRoomLog`s when a sandbox or browser runs (`live-work.tsx:37`, `:169`).

**Fix.** One log subscription per room per page. Put `useRoomLog` behind a small shared store keyed by `org/kind/id` (a `Map` plus ref count), or lift it into `CallProvider` and let the buttons read from context. Do not replay history to find the last `call.*` event: ask the edge for `/log?type=call.` or keep the cursor from the first `/call` answer. Do not store `transcript.segment` in the shared log at all unless the Parleys tab needs it. Raise or remove the silent page cap, with a visible "history truncated".

---

### T6 "Connecting…" / "Opening the room…" for several seconds on every open

- **Severity:** medium
- **Kind:** slow
- **Seen:** prod walkthrough (strings at `teamspace-room.tsx:176`, `:400` and `:455`); causes from code reading.

**What happens.** The room area shows "Opening the room…" and the composer placeholder "Opening the room…" until the first read from Go completes.

**Why.** The states are serial, each waiting for the last:
1. Page loads the teamspace, then `useTeamspaceRoom` opens the socket (`hooks/use-teamspace-room.ts:259`). The Worker first asks Go whether the member is on the team (`server.ts:644`, `checkAccess`).
2. The room object's `onConnect` calls `admit`: `GET /v1/auth/me`, then `GET …/teamspaces/{id}` (`teamspace-room-agent.ts:770-798`, `:1488`, `:1536`). Two Go round trips.
3. The object **reads nothing until the page sends `room-visibility`** (comment at `teamspace-room-agent.ts:793-796`). `onOpen` sends it (`use-teamspace-room.ts:264-267`). That triggers `readAs`, which can ask Go again (`vouch`, `:1501`), then `GET …/messages` (`:971`).
4. Only then `sendSnapshot` runs and `connection` becomes `live` (`use-teamspace-room.ts:588-599`).
5. In parallel the parley layer does its own chain: `POST …/edge/rooms/…/admit` (`lib/live/client.ts:60-74`), then Worker→Go membership lookup for **every** `/live/*` request (`server.ts:776-785`), then `/call`, and only after `/call` succeeds the log opens (`use-room-call.ts:36-38`: `enabled: active && call.isSuccess`), then the log read, then the socket, then another catch-up. That is a five-step waterfall, multiplied by T5.
6. No skeleton shows what is being waited on; `messages.length === 0` and not live just says "Opening the room…" (`teamspace-room.tsx:171-177`).

**Fix.** Cache the Go membership answer in the Worker for `/live/*` for a few seconds (the member id does not change per request). Remove the `call.isSuccess` gate or open the log in parallel with `/call`. Have the room object read on connect for a visible tab instead of waiting for `room-visibility`. Render the last known messages from a cache while the socket opens.

---

### T7 The agent's answer in a call is private unless the asker is alone, and it is lost if they have no voice socket

- **Severity:** high
- **Kind:** confusing
- **Seen:** code reading.

**What happens.** In a call with two or more people, the agent says "<Name>, I've sent you that answer privately." The answer appears as a small "Only you:" block in the call surface. The others never hear or see it. If the asker has just reconnected, or joined "Listen in", it is gone.

**Why.**
- `voice.ts:204-222`: the answer is spoken only when `present.every(member === asker)`; otherwise it goes to the asker's sockets with `sendTo` (`:220`). The comment says this stays until the "room-audience guard (Layer C)" exists.
- `sendTo` sends to whichever sockets that member has now. No socket, or a dropped one, and the frame is lost silently (`voice.ts:383-393`). The private answer is also logged as a private room event, but the UI shows it from the socket frame only (`call-provider.tsx:635`, `call-surfaces.tsx:777`).
- The voice socket has no heartbeat, and its `close` handler reopens forever every 2 s without checking why (`call-provider.tsx:951-953`). After the DO closes a socket on purpose ("left the call", `voice.ts:141`), or the member is removed, the page loops on refused upgrades. Nothing replays missed frames when it reconnects.
- If the dispatch fails after retries the member hears "I can't reach my tools right now" (`voice.ts:282`). If the run later finishes with `failed`, they hear a generic apology (`:231`).

**Fix.** Show the private answer in the room panel as well as the call (it is already a private event in the log: render it from `call.result` events with `private:true` so a reconnect does not lose it). Add a ping and a backoff to `followVoice`. Say in the UI, at join, that answers in a group call come privately.

---

### T8 System lines name agents by internal handle ("@Arlo-UJVX", "@krude"); handles are not stable

- **Severity:** high
- **Kind:** confusing
- **Seen:** prod walkthrough ("@Arlo-UJVX", "@krude"); causes from code reading. Whether the production rename ran is unverified.

**What happens.** Lines like "@arlo-ujvx couldn't answer right now." appear in the room, with a random suffix, in a name no one chose.

**Why.**
- The relay's own status lines use the seat **handle**, not the display name: `turns.go:75`, `:77`, `:79`, `:81`, `:89`, `:95`. Handles are derived by `handle(name)` (`rooms/service.go:497-519`): lowercased, with `-N` for duplicates. The agent name `Arlo-UJVX` is an old generated name (`managedagents/store.go:537-541`). `handle()` strips case and punctuation but keeps the suffix.
- A rename migration exists (`RenameSuffixedPrimaries`, `agents/primary/service.go:165`, shipped in release 67f200a3). But it only touches names in `member_agents` and the tables listed in `nameCopies` (`managedagents/store.go:339-356`). It cannot rewrite what was already posted: `room_messages.author_handle` keeps the old handle, and the status lines are plain text containing it. So history keeps "@arlo-ujvx" after the rename (guess: this is what was seen; a live check of the current agent names would confirm).
- Handles are recomputed on every read from the current names, with `-2`, `-3` for duplicates in **seat order** (`service.go:466-490`). A new teammate named like an existing one can take a handle an old message already used. `RoomLine` finds the speaker by `seats.find(handle === message.author_handle)` (`teamspace-room.tsx:247`). A rename, or a new duplicate, breaks that lookup. The line then falls back to showing `@${message.author_handle}` (`teamspace-room.tsx:252`).
- The call uses a third name ("Krude", `wrangler.jsonc`), and the notes card says "posted by Krude" (`parley-notes-card.tsx:39`). One agent, up to three names.
- Messages are stored with only the handle plus ids (`rooms/rooms.go`, `Message.AuthorAgentID`). The UI could look up by `author_agent_id` and show the current name, but `RoomLine` only does so for the avatar (`teamspace-room.tsx:255`).

**Fix.** In the relay lines, use `seat.DisplayName`, and render mentions in the UI as the seat's current name by looking up `author_agent_id` / seat id, not the handle. Keep handles for parsing `@` only. Make handles stable by storing each seat's handle once (a column on the room's seating) instead of recomputing from order. Rewrite old status lines or hide them. Use one name for the agent in the call, the notes card and the room.

---

### T9 The room is never marked read when the newest message is a parley-notes card

- **Severity:** medium
- **Kind:** broken
- **Seen:** code reading.

**What happens.** After notes are posted (once T1 is fixed), the teamspace's unread count and the call button's "Room" badge can stay stuck on the notes card.

**Why.**
- The "seen" observer looks for the DOM node of the newest message: `list.querySelector('[data-seq="…"]')`, and returns if there is none (`teamspace-room.tsx:80-81`).
- Normal messages render `data-seq` (`teamspace-room.tsx:288-294`). `ParleyNotesCard` renders an `<article>` with no `data-seq` (`parley-notes-card.tsx:66-70`). When the last message is a notes card, `last` is null, `saw()` never runs, and `useMarkTeamspaceRead` gets `undefined`.
- Same effect on the other path: `useReportRoomActivity` is fed `message.seq` for every message including notes (`teamspace-room.tsx:50-59`), so the badge counts something the observer cannot clear.

**Fix.** Pass `data-seq={message.seq}` into the notes card's `<article>` (one prop). Add a test: last message is a notes card, room still marks read.

---

### T10 The chat is not live: other people's messages arrive on a poll, up to 12 s late, and not at all while no tab is visible

- **Severity:** medium
- **Kind:** slow
- **Seen:** code reading.

**What happens.** A teammate writes. You see it up to about 12 s later. An agent's answer shows up in a burst when Go finishes the whole turn. A message you post is not visible in the room until Go's decision call returns.

**Why.**
- The room object reads Go on a timer: `slowMs: 12_000`, `fastMs: 2_500` for 2 minutes after a post, `landingMs: 1_000` while a post is out (`teamspace-room-agent.ts:92-134`, `cadence` at `:1659-1666`). No push from Go to the object.
- With no visible tab it does not read at all (`planTick`, `:1685`). The first tab to come back triggers the read (`see`, `:942-962`).
- A post is not stored until the decision call returns: `service.go:156` (`Decide`, bounded to 5 s, `decide.go:197`) runs before `relay.Post` appends (`relay.go:180`). Then the relay runs the agents inline for up to 80 s (`relay.go:47`) and the poster's HTTP call waits for all of it (`teamspace-room-agent.ts:1166-1170`, timeout 5 min `:122`). The "Sending…" line is the only feedback.
- The pending post map is in memory (`teamspace-room-agent.ts:711-712`). A DO restart mid-post loses it; the page resolves it as "The room did not answer" (`use-teamspace-room.ts:631-633`) even if Go recorded the message (the page handles that with `hasLanded`, `:556`, which is good).

**Fix.** Short term: lower `slowMs` when a message is pending in Go or add a cheap `last_seq` check. Medium term: have Go publish room events to the object, which the other realtime objects already do (see `REALTIME.md`). Append the human message first and run the decision/agents after it.

---

### T11 A message nobody answers shows no reason

- **Severity:** medium
- **Kind:** confusing
- **Seen:** code reading.

**What happens.** You write "can someone look at the invoice?" with no @mention. The composer says "Sent. The room's agents are answering…", then nothing. Or you @mention a teammate who is online: nobody answers, by design, and nothing says so.

**Why.**
- Who answers: an explicit `@handle`, else a model verdict (`relay.go:386-415`). An `@` for a present person returns no responders (`relay.go:432-435`). A verdict that says "nobody" or an erroring decision (`service.go:158-162`) also means no one. All of these just end `Post` with only the author's message stored.
- The composer text is based on whether a post is outstanding, not on whether any agent was asked (`teamspace-room.tsx:459-467`).
- If the decision fails it is logged only (`service.go:160`).

**Fix.** Have `Post` return a small `{asked: [handles]}` with the messages. Show "Nobody was asked. @mention an agent to bring it in." when it is empty. The composer placeholder already hints at this ("or @mention someone's agent", `teamspace-room.tsx:175`); show it at the moment it matters.

---

### T12 The call log subscription cannot tell a dead socket from a quiet one

- **Severity:** medium
- **Kind:** brittle (works sometimes)
- **Seen:** code reading. Laptop-sleep behaviour needs a live test.

**What happens.** After the laptop sleeps or the network changes, a call can look unchanged (no new joiners, no end of call) until the 20 s poll.

**Why.**
- `use-room-log.ts:119` sends `"ping"` every 25 s and never checks for `"pong"` (`parseLiveFrame` just ignores it, `lib/live/client.ts:133-135`). A half-open TCP connection never fires `close`, so the hook stays `live`.
- `socket?.send("ping")` on a socket that is no longer open throws inside the interval; there is no try/catch.
- Nothing listens to `online` or `visibilitychange`. Recovery relies on the browser eventually closing the socket.
- Backup: `/call` polls every 20 s (`use-room-call.ts:11`, `:29`). The log-driven parts (sandbox, browser) have only a 30 s list poll (`teamspace-focus.tsx:81`).
- The chat socket is better: it reconnects through `useAgent`, and `onClose` runs the access check (`use-teamspace-room.ts:275-292`). It also resends visibility on open. It has no heartbeat check either (the heartbeat there is Go's read, `heartbeatMs`).

**Fix.** Count a missed pong (two intervals) as a dead socket, close it and reconnect. Wrap `send` in try/catch. Re-run `catchUp` on `visibilitychange` and `online`.

---

### T13 The Parley button is enabled for people who cannot join, and goes dead for a transient error

- **Severity:** medium
- **Kind:** brittle (works sometimes)
- **Seen:** code reading.

**What happens.** A team manager who is not on the team, or anyone while the teamspace is paused, sees an active Parley button. Clicking it fails with "You can't join a parley in this room yet." In the other direction, one failed `/call` read greys the button with "Parleys aren't available in this teamspace yet." for up to 20 s.

**Why.**
- `ParleyButton` is rendered when `hasCalls && viewerId` only (`teamspace-focus.tsx:217`). It ignores `space.is_member`, `space.status` and `canPost`. The edge answers 403 on join (`call-provider.tsx:318`).
- The button is disabled when `unavailable` is true, and `unavailable` is `call.isError` (`use-room-call.ts:53`, `call-surfaces.tsx:333-341`): *any* error, not only "no such room". Retries stop at 403/404/401 (`use-room-call.ts:32-34`).
- `/call` goes through `liveRequest`, which first does `admit` and **swallows its failure** (`lib/live/client.ts:64-71`). A failed or slow admit means the next request is refused 403 by the edge, and the user sees "not available".
- Paused teamspaces: the room goes read-only, but a call still starts, and its notes would be refused by Go (T1).

**Fix.** Hide or disable Parley for non-members and paused teamspaces, with the reason in the tooltip. Make `unavailable` only 403/404/503. Retry `admit` once before giving up.

---

### T14 Plan and Parleys tabs are placeholders; the side column mixes empty states

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod walkthrough (header "3 people and 3 agents"; Team summary, Purpose and Contributed agents all empty); tab placeholders from code reading.

**What happens.** Five tabs, two of them "coming": Plan ("A shared plan is coming") and Parleys ("Past parleys will be listed here"). The side column shows Team summary "Nothing summarized yet", Purpose "No description yet.", Contributed agents "No one has contributed a specialist yet." The header says "3 people and 3 agents".

**Why.**
- `teamspace-focus.tsx:309-318` and `:331-340` render `ComingPanel`. The checklist that does exist lives in the side column (`teamspace-launch.tsx`), not under Plan.
- The header counts `whisperers` and the agents of those same whisperers (`teamspace-focus.tsx:91-96`). Every person has one primary agent, so "N people and N agents" is always the same number unless specialists are contributed. It carries no information.
- Team summary is empty until the organization agent writes it; nothing writes it from a parley (`rooms/notes.go` does not publish or compact; compare `service.go:184-190` for normal messages). Purpose is the teamspace description, empty by default (`teamspace-focus.tsx:495-497`). All three render as full cards.
- Past parleys exist only as a notes card in the chat stream (and the R2 transcript).

**Fix.** Hide empty cards for managers who can fill them in (show "Add a description" inline) and for everyone else show nothing. Drop the "N agents" count or make it "N people, their agents, and M specialists". Either remove the Plan and Parleys tabs until they work or fill Parleys from the notes messages (`GET …/messages` kind=`parley_notes` is already served; `rooms/store.go:672`).

---

### T15 Checklist, Activity, Files and Team summary do not update when someone else changes them

- **Severity:** medium
- **Kind:** brittle (works sometimes)
- **Seen:** code reading.

**What happens.** A teammate ticks a task, publishes shared work or uploads a file, and your page shows the old state until you reload, change tab, or do something yourself.

**Why.**
- Data is plain `useQuery` with `staleTime: 30_000` (`integrations/tanstack-query/context.ts:9`). Tasks, files, entries and synthesis have no `refetchInterval` or event hook (`teamspace-launch.tsx`, `teamspace-focus.tsx:146-153`, `team-synthesis.tsx:71`).
- The Activity ledger is invalidated only after **your own** post succeeds (`use-teamspace-room.ts:549-553`). A decision made by someone else's message never refreshes it.
- Files: `retry: false` (`teamspace-focus.tsx:152`), and the tab shows "Couldn't load the team's files" with no retry button (`:645-652`). The Activity tab has a "Try again" button; Files does not.
- Task changes invalidate three keys (`queries.ts:186-203`) but nothing tells other clients.

**Fix.** Invalidate these keys from the room's message events (a `kind`/`room_id` hint on a message, or a ledger event), or add a modest `refetchInterval` while the tab is visible. Add "Try again" to Files.

---

### T16 Call controls and "Leave" swallow errors, and the last person to leave gets no "Parley ended" summary

- **Severity:** low
- **Kind:** polish
- **Seen:** code reading.

**What happens.** "Record" or "Agent in/out" can fail with nothing shown. "Leave" ends the call for you without the "What's kept" popup (that popup appears only on End or when someone else ends it).

**Why.**
- `record` and `agent` `await` the call with no catch (`call-provider.tsx:737-751`). The caller is `act(...)` in the dock (`call-surfaces.tsx:1101`, `:1119`). Whether `act` shows the error needs a live check. `end` and `leave` swallow all errors (`call-provider.tsx:714`, `:371`, `:500`).
- `remember()` (which sets the "ended" summary, `call-provider.tsx:656`) runs in `end` (`:713`) and `endedElsewhere` (`:673`) but not in `teardownAndLeave` (`:382`). The last leaver's call ends on the edge by "everyone left" (`edge/src/call/index.ts:273-275`) and the page never learns.
- The "Parley ended" popup says "The transcript, kept with <room>" (`call-surfaces.tsx:660-664`). Nothing lets the member open it (see T1).

**Fix.** Show an inline error for record/agent failures. Call `remember()` on Leave when the view says the call is gone.

---

### T17 Invite adds people to a team, but they cannot post until they have an active agent, and nobody is told

- **Severity:** low
- **Kind:** confusing
- **Seen:** code reading. Guess about how often this occurs.

**What happens.** A manager adds someone with Invite. The person opens the teamspace and sees a room they can read but cannot post in: "Only teammates with an active agent can post in this room."

**Why.**
- Invite just adds a team member (`teamspace-invite.tsx:56-72`, copy at `:104-108`). It does not mention agents.
- Seats come from `space.whisperers`, i.e. people **with an active agent** (`rooms/service.go:468-480`, `teamspace-focus.tsx:533-560`). A member without one has no seat. `canPost` needs `me` (`use-teamspace-room.ts:504-511`).
- The new teammate's seat also appears only on the room object's next read (T10), up to 12 s after Invite succeeds (invite only refreshes the page's own queries, `teamspace-invite.tsx:61-71`).

**Fix.** Say it in the Invite dialog and show "no active agent yet" next to such members in the People list. Nudge the room object (a no-op read) after Invite.

---

## Open questions (only a live test can settle)

1. **Has any parley ever produced a notes card in production?** T1 says no by code. A look at `agents_primary.room_messages` for `kind = 'parley_notes'` in prod would confirm.
2. **Is `GEMINI_API_KEY` still unset on the production edge?** PR #514 says so; check `wrangler secret list` before testing T2.
3. **Does the RealtimeKit member preset exist in the shared app, with `transcription_enabled`?** Without it `meeting.ai.on("transcript")` never fires and the agent hears nothing (T4).
4. **What does "@krude" come from?** T8 gives the likely source (relay lines using a handle). Check which seat or organization agent that handle belongs to in the walkthrough org, and whether the rename migration ran in prod (`SELECT name FROM agents_primary.member_agents WHERE name ~ '-[A-Z0-9_-]{4}$'`).
5. **Laptop sleep.** Close the lid for 5 minutes with a parley open, then reopen. Does the call dock recover by itself, how long until the call view and voice socket are right again (T7, T12), and does the chat socket reconnect without a reload?
6. **Does the dock's `act(...)` wrapper show an error** when `record` or `agent` fails (T16)?
7. **How big is a log after a few parleys?** Count `transcript.segment` events in a room that has had three or four calls and see how long `/log?after=0` takes (T5).
8. **Two members in a call:** ask the agent a real question from each and from a "Listen in" member; confirm the private-answer behaviour (T7).
