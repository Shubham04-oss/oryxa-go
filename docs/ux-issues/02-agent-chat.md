# 02 Agent chat (Arlo / Krude)

Area: the member's primary-agent chat in `web-next/` and the Go agents service (`agents/primary/internal/threads`). Read-only investigation, 2026-10-25, branch main. Paths are relative to the repo root. Anything marked **guess** was not confirmed in code and needs a live check (see Open questions).

How the chat is wired (needed to read the issues below):

- There is **one** `<AgentChat>` in the whole app, mounted by `WorkspaceAgentPanel` (`web-next/src/components/workspace-agent-panel.tsx:1048`). The side panel, the Chats page and the Agent overview all show that same mounted chat; the session page just moves it into its own column (`useAgentChatHost`, panel:223/664). That is why the same old chat shows on Q1, Library and Chats.
- Which thread it shows is a reducer (`primaryChatReducer`, `web-next/src/lib/threads/chat-choice.ts:533`) held in React state in the panel (panel:252). Nothing is in the URL. The only persistence is `sessionStorage` (chat-choice.ts:41-88), per tab.
- Home, chat cards, "New chat" and the Chats list all go through one module-level queue, `requestPrimaryChat` (chat-choice.ts:254), claimed by the panel (panel:387-400).

---

### C1 The chat reopens the newest chat that has an unanswered tool confirmation, so a stale one becomes "the chat opens from history"

- **Severity:** high
- **Kind:** broken
- **Seen:** owner report; prod walkthrough (the same "tell me whether you can use browser" chat on Q1, Library and Chats). Which chat that is on prod is a **guess**: it fits a chat stuck on a browser-tool confirmation.

**What happens.** Open the agent chat (rail button, panel, reload, new tab) with no explicit choice. Instead of a blank chat you land in an old conversation. It is always the same one until someone answers its confirmation, and in that chat the composer is disabled.

**Why.** The opening rule is: view remembered for under five minutes, else "the newest conversation waiting on their approval", else New chat (`chat-choice.ts:138-159`, doc at `chat-choice.ts:8-11`, wired at `agent-chat.tsx:333-357`). The approval step looks at the five newest chats (`approvalCandidates`, chat-choice.ts:99-103) and picks the first with `pending_confirmations.length > 0` (`agent-chat.tsx:157-167`). In Go a confirmation is pending forever until answered: `pendingConfirmations` (`agents/primary/internal/threads/items.go:356-373`) has no expiry, and a new message to that thread is refused with `ErrConfirmationPending` (`threads/run.go:277-281`). So an abandoned confirmation (a browser or sandbox tool the member never approved) permanently outranks "start fresh". Two things make it worse: (a) an automatic New is deliberately not remembered (`useRememberedView(..., rememberNew = chat.chosen || firstMessage)` at agent-chat.tsx:241-247, chat-choice.ts:732-745), so every remount after the five minutes re-runs the rule and reopens the stale chat; (b) an automatically chosen old thread *is* remembered and its five-minute grace is renewed by any pointerdown/keydown/wheel anywhere on the page (chat-choice.ts:191-210), so it stays sticky while you work elsewhere with the panel visible.

**Fix.** Do not open a chat on its own because it has a pending confirmation: open New chat, and show the pending chat as a "Needs you" item (the Requests inbox and the Home needs section already do this). If an auto-open is wanted, limit it to confirmations younger than some age (needs a timestamp on the pending call from Go) and never when the member explicitly pressed New. In `openingThread` (chat-choice.ts:139) drop the `waiting` branch. Also give a thread's stale confirmation an expiry or a "Dismiss" in Go (`pendingConfirmations`), so a chat is not bricked forever.

---

### C2 A finished answer stays marked "Working 17m38s" and keeps counting

- **Severity:** high
- **Kind:** broken
- **Seen:** coordinator's live finding on prod 2026-10-25: chat "which deployment is live and recent commit", last answer shows its full text, chip reads "Working 17m38s".

**What happens.** The answer text is complete, but the run card keeps the "Working" status, a running timer and a Stop button. Because the chat believes a run is active, the History and chat-list buttons are disabled too ("You can switch chats once it finishes", C9).

**Why.** Status is not derived from the answer. `turnStatus` returns "working" whenever `live` is true, before looking at the entries (`web-next/src/lib/threads/timeline.ts:779`). In `AgentTimeline`, `live = index === last && (running || !ended)` (`web-next/src/components/agent-timeline.tsx:998`). `running` is purely the client's streaming state: `chat.status === "submitted" | "streaming" || chat.isServerStreaming` (`agent-chat.tsx:964-968`). `ended = !running && (stopped || settled)`, and `settled` only becomes true after the socket has been `open` and not `running` for 2 s (`agent-chat.tsx:975-986`, `RUN_SETTLE_MS` at :88). So the card stays "Working" in either of two cases:

1. `running` never goes false: the stream's final frame never reached the browser (socket dropped or resumed mid-stream; `onFinish` at agent-chat.tsx:892 is the only place that clears things and it only fires on a finish). `chat.isServerStreaming` is a library flag; whether it can stick after a missed finish is a **guess**.
2. `connection` is not `"open"`: `connection` is set to "reconnecting" in `onClose` (agent-chat.tsx:866-869) and back to "open" only by `onOpen`. A socket that keeps failing (expired cookie, laptop sleep, Worker turned the socket away) keeps `settled` false, so every finished turn is "working" forever. Hence the timer: `RunCard` computes `now - start` while active (`agent-run.tsx:313-322`).

The server cannot correct it. Go records `run_completed`/`run_failed` brackets in the thread (`threads/brackets.go:1-40`), and the web parses them as `runs[].endedAt` (`timeline.ts:614-620, 740`), but `turnStatus` ignores them. The Go comment says a client "knows a run is still answering from its stream, never from history" (brackets.go:25-29), and the thread detail has no "active run" field (`threads/threads.go:135-142`). The relay does write a `run_failed` when the stream stops early (`web-next/src/chat-relay.ts:275-281`), but only if the Durable Object itself saw the stream end; if the DO or the browser socket died, nothing is written. Also the reconcile effect that would merge Go's history only runs when `!running && connection === "open"` (agent-chat.tsx:1004-1011), so the same stuck flags also block the recovery.

**Fix.** Make status depend on facts, not only on the socket. In `AgentTimeline`/`turnStatus`: if the last turn's last run has an `endedAt` (a stored `run_completed`) and no streaming draft entry, it is not live, whatever `running` says. Second, add a "Working" watchdog: if `running` is true but no chunk has arrived for N seconds (say 60), refetch the thread detail and trust Go's brackets. Third, expose `active_run: true|false` (from the run store, which already holds the lease: `RunStore` in threads.go:144) on the thread detail so the browser can ask. Reset `connection`/`settled` on `visibilitychange`.

---

### C3 Home chat cards and Home "send" take over the chat while an answer is running

- **Severity:** high
- **Kind:** brittle
- **Seen:** code reading. Not run live.

**What happens.** An answer is streaming in the panel. On Home you click a chat card, or send a new message from "Ask Krude anything". The chat switches at once; the running conversation is unmounted. The run carries on in Go, but the panel shows no sign of it, and Home loses "Working" (it reads the published state).

**Why.** The guard is inconsistent. The history list and the Chats list are disabled while `state === "running"` (`agent-chat.tsx:649`, `chats-page.tsx:104,197`). The panel's New chat button is disabled only for `first !== null` (`workspace-agent-panel.tsx:538`; `agent-chat.tsx:627`), not for a running answer. Requests from Home (`openHomeConversation`, `workspace-shell.tsx:522-532`) go straight into the reducer, and for an `open`/`blank` task `settle()` switches the thread immediately (`chat-choice.ts:507-518, 538-553`); only `send` tasks wait behind an earlier unsent first message. The old `ThreadChat` is keyed by thread id and unmounts (agent-chat.tsx:495-497), and its cleanup publishes "idle" for it (`agent-chat.tsx:1073-1076`), so Home's "Working" indicator and "needs you" for that thread are cleared although the run is still going.

**Fix.** One rule everywhere: while `state === "running"`, either block the switch and say why, or allow it and keep the running thread's state published (key the activity by thread, not a single latest value, `publishPrimaryChatActivity` at chat-choice.ts:347). Apply it in the reducer's `request`/`move` so every entry point gets it.

---

### C4 "New chat" vs "New conversation" are the same action under two names

- **Severity:** low
- **Kind:** confusing
- **Seen:** prod walkthrough; code reading.

**What happens.** The same thing is called "New chat" (panel header, chat list, thread bar) and "New conversation" (aria-label and tooltip of the Agent-overview bar button, the placeholder title of an unsaved chat, the Home composer's label "Start a new conversation"). Home's pickup also says New chat.

**Why.** `agent-chat.tsx:444` (title "New conversation"), `agent-chat.tsx:625-630` (aria-label/title "New conversation" but visible text "New chat"; the accessible name differs from the visible name), `workspace-agent-panel.tsx:536` ("New chat"), `chats-page.tsx:150` ("New chat"), `home-ask.tsx:30` ("Start a new conversation").

**Fix.** Pick "New chat" and use it as the visible text, accessible name and tooltip everywhere. A `chatLabels` constant in `agent-chat-menu.tsx` or a small copy file is enough.

---

### C5 Two "All conversations" links, neither goes to the Chats page

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod walkthrough; code reading.

**What happens.** Home shows two "All conversations" links (the Needs-you note and the pickup list). Clicking either opens the narrow panel with its History drawer, not the full Chats page that has search, day groups and files.

**Why.** Both pass `onOpenAgent`, which is `openHomeHistory` (`workspace-shell.tsx:536-540, 650`): `closePrimaryWork(); expandAgentPanel(); setTabRequest({tab: "agents", history: true})`. Links: `home-overview.tsx:849-851` and `HomePickup` (`home-overview.tsx:975-985`). The Chats page is a separate route (`openChats`, workspace-shell.tsx:508-513). The History drawer lists only titled threads plus waiting/selected ones (`agent-chat-history.tsx:58-65`); the Chats list does the same filter (`chats-page.tsx:64-71`), so an untitled chat that is not open or waiting is invisible in both.

**Fix.** Point both links at `openChats()`. Keep History for the panel only. Show untitled chats as "Untitled chat" instead of hiding them (a first message that failed leaves an empty untitled thread that otherwise just disappears).

---

### C6 "Tool confirmations are limited to your five newest chats" is permanent noise, and it costs five full-transcript reads every 30 s

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod walkthrough (Needs-you note); code reading.

**What happens.** Anyone with more than five chats sees "Tool confirmations are limited to your five newest chats and the open chat. Older chats may still need you." under Needs you, and "The check is not complete." flashes while the five reads refetch.

**Why.** Go gives a pending confirmation only inside the full thread detail; there is no list field for it. `Thread` list rows carry no pending flag; `Detail.Pending` is computed by replaying all events (`threads/threads.go:405-420`, `items.go:356`). The code comment: "ADK stores confirmations in events, not reliable session metadata. Never walk all histories" (`home-overview.tsx:372-374`). So the web reads the five newest threads in full (`approvalCandidates`, chat-choice.ts:99-103; `useQueries` home-overview.tsx:376-381) with `refetchInterval: 30_000` and `refetchOnWindowFocus: "always"` (`lib/threads/client.ts:689-708`). The note shows whenever any chat falls outside the five (`outsideConfirmationCoverage`, home-overview.tsx:515-517, rendered 843-858) or any read is fetching (`freshConfirmations` 518-520). What breaks: a confirmation in the sixth-newest chat is invisible on Home, in "Needs you" and in the opening rule, and the history list's "Needs approval" tag; the count in the Needs chip (`use-needs-you.ts`) counts Requests only, so it disagrees with the Home list.

**Fix.** Add `pending_confirmations: number` (or a bool) to the list rows in Go. It is cheap to maintain: set a state key when a confirmation is asked and clear it when answered, written next to where the run already writes its state delta. Then delete the five-chat reads and the note. Short term: hide the note unless a chat outside coverage exists *and* the check finished, and fetch the detail with a "light" query that skips `items`.

---

### C7 Stale context chip "Looking at Agent overview"

- **Severity:** low
- **Kind:** confusing
- **Seen:** prod walkthrough ("Looking at Agent overview", "Looking at Q1"); the cause for the Chats page is a **guess** because I did not read the label builder.

**What happens.** In the Chats or Agent overview page the chat is already on the page it names, so the chip reads "Looking at Agent overview", which tells nobody anything, and it is sent to the model as context. The panel on Q1 says "Looking at Q1", which is right while you are there and wrong after you navigate if the chip is not yet refreshed.

**Why.** `LookingAt` polls `getScreenContext` every 2 s while the document is visible (`agent-run.tsx:459-491`, interval at :476), so it lags a navigation by up to 2 s and can show the previous page. The same snapshot is taken again when a first message is sent from Home (`workspace-agent-panel.tsx:394`, stored on the task, `chat-choice.ts:411-413`), so a message queued before the panel mounted carries the page it was typed on (reasonable) but a message retried from "Not sent" carries the page it was *first* sent from (`chat-choice.ts:658-679`), which can be stale on purpose.

**Fix.** Subscribe to route changes rather than polling. Do not show the chip when the view is the agent's own session (`agents/...` views).

---

### C8 Reload or new tab: the chat position is lost, and "last view for five minutes" is per tab and heartbeat-driven

- **Severity:** medium
- **Kind:** brittle
- **Seen:** code reading.

**What happens.** Which chat is open is not in the URL. A reload within five minutes returns to the last chat; after that, or in another tab, the opening rule (C1) decides. A shared link to a chat is impossible.

**Why.** State is a React reducer (panel:252) plus `sessionStorage` (`chat-choice.ts:51, 81`) with `FRESH_CONVERSATION_MS = 5 * 60 * 1000` (chat-choice.ts:32). The remembered time is refreshed by activity at most every 15 s (`chat-choice.ts:195`) and only while the chat is visible (`visible` param, `agent-chat.tsx:241-247`); a collapsed panel that kept the chat alive saves nothing, so leaving the panel collapsed for five minutes forgets the thread even though the member was working in the app.

**Fix.** Put the open thread in the URL (search param `?chat=<id>`) and let that win over everything; fall back to the rule only when it is absent. Count any activity in the app, not just while the panel is visible.

---

### C9 "You can switch chats once it finishes": switching is blocked for the whole run and, with C2, forever

- **Severity:** medium
- **Kind:** brittle
- **Seen:** code reading.

**What happens.** While an answer runs, the list rows, the history rows and the thread menu are disabled. If the status is stuck (C2) they stay disabled until reload.

**Why.** `busy={primaryChatActivity?.state === "running"}` (`workspace-shell.tsx:795`), `disabled={first !== null || state === "running"}` (`agent-chat.tsx:601, 649`), the note at `chats-page.tsx:239-243`. The only way out is Stop, and Stop only works if the run card shows it, which needs `running` true (`agent-timeline.tsx:1011`).

**Fix.** Let the member leave a running chat (C3's keyed activity). Keep Stop.

---

### C10 Failed or cut-off messages: errors are shown but only some can be retried

- **Severity:** medium
- **Kind:** brittle
- **Seen:** code reading.

**What happens.** A first message that could not start a conversation is kept with Retry/Edit/Dismiss (good, `agent-chat.tsx:665-726`). But a message in an existing chat that fails ("Your agent couldn't answer. Try again.") only shows a line; there is no Retry button for it, and the member must retype.

**Why.** In `ThreadChat`, `send` and `onError` only `setRunError(...)` (`agent-chat.tsx:890-891, 928-939`); `AgentConversation` renders `onRetry` only when a caller passes it, and `ThreadChat` does not (`agent-chat.tsx:1097-1107`). The one automatic retry is the expired-session resend (`resendWithFreshSession`, 912-926), once. Also `runError` is cleared only on the next send (`setRunError("")` at 929, 942), so a stale error stays in view after the run later finishes on its own (the run continues in Go when the stream drops: `chat-relay.ts:246-247`).

**Fix.** Pass `onRetry` that calls `chat.regenerate({ body: lastSend.current.body })`, and clear `runError` when `onFinish` fires with success or when a run resumes.

---

### C11 Opening the chat can hold the composer on "Connecting…" for up to 5 s, then silently open a new chat

- **Severity:** low
- **Kind:** slow
- **Seen:** code reading.

**What happens.** On first open the chat waits for the thread list and up to five approval reads before showing anything (`openingThread`, chat-choice.ts:151-158), and the composer is disabled with "Connecting…" (`OpeningConversation`, agent-chat.tsx:733-781). After `OPENING_FALLBACK_MS = 5000` it decides New (agent-chat.tsx:362-369). If the five reads are slow the result can flip between "the stale approval chat" and "new chat" depending on load, which makes C1 look intermittent.

**Why.** `agent-chat.tsx:151, 333-369`. `waiting(...).catch(() => false)` (chat-choice.ts:156) treats a failed read as "not waiting", so a flaky network changes which chat opens.

**Fix.** Removing the approval step (C1 fix) removes this wait; the list alone is enough.

---

### C12 "Needs approval" markers in the list come from a cache of the last five reads

- **Severity:** low
- **Kind:** brittle
- **Seen:** code reading.

**What happens.** Chats outside the five newest never show "Needs approval", and a chat shows it for up to 30 s after being answered elsewhere.

**Why.** `ChatsList` builds `waiting` from `queryClient.getQueryState` for the approval keys only (`chats-page.tsx:114-129`); the keys exist only for the five candidates plus the open chat (agent-chat.tsx:248-285). Staleness: `staleTime/refetchInterval` 30 s (`lib/threads/client.ts:702-706`).

**Fix.** Same as C6: a pending flag on list rows.

---

### C13 The agent's name is hardcoded "Krude" in many places, while the real name (Arlo, Nia, ...) is in the agents list

- **Severity:** high
- **Kind:** confusing
- **Seen:** prod walkthrough (agent is "Arlo", Home says "Ask Krude anything"; mentions like "@Arlo-UJVX").

**What happens.** The member's agent has a generated friendly name (Go picks one of 50: `agents/primary/internal/managedagents/store.go:497-503`). The panel, header and composer use it, but Home, onboarding and several strings say "Krude".

**Why.** The real name is `primaryAgent(ownAgents.data).name`, already read in the shell (`workspace-shell.tsx:774-780`, panel `workspace-agent-panel.tsx:340-341`) and passed to the chat as `agentName`. Hardcoded "Krude" in `web-next/src` (non-test):

- `components/home-ask.tsx:2,31` (placeholder "Ask Krude anything"; `HomeAsk` takes no `agentName`)
- `components/home-overview.tsx:215` (comment), `:541` ("What Krude found"), `:930` and `:931` (hello lines), `:950` ("Krude is setting up with you"); `HomeProps.agentName` exists (home-overview.tsx:216) but is only used for the glance variant
- `components/home-first-run.tsx:42` ("Let Krude work while you're away"), `:66` ("Ask Krude something"), `:183` ("Krude takes it from here")
- `components/today-format.ts:209` (`helloLine(needs, handled, agent = "Krude")`: a default that wins whenever the caller omits the name)
- `components/onboarding.tsx:206` (`"Krude"` fallback), `:506`, `:613` ("Each person gets their own Krude")
- `components/onboarding-frame.tsx:69` (`sketch.agentName?.trim() || "Krude"`)
- `components/request-card-format.ts:121` (doc), `:395`, `:617` (comments only describe "Krude"; check the real strings use `agent_name`)
- `components/call-surfaces.tsx:987` (comment "Krude taking notes"), `components/parley-notes-card.tsx:39` ("posted by Krude", comment), `components/requests-view.tsx:148`, `components/agent-chat-menu.tsx:42`, `components/workspace-sidebar-live.tsx:6` (comments)
- `components/request-card-fixtures.ts:51-52` (fixture)
- `routes/index.tsx:250` (comment), `lib/agents/managed-agents.ts:1`, `lib/agents/README.md:82,84,87` (comments)
- CSS only: `workspace.css:609`, `home-overview.css:610-620`, `onboarding.css:114-115`

Fallbacks that say neither name: `FALLBACK_AGENT_NAME = "Primary agent"` (`agent-switcher.tsx:12`), "your agent" / "Your agent" (agent-chat.tsx:91-98, panel:484). On the Go side "Krude" is still the model-facing name in tool and sub-agent prompts (`agents/primary/internal/subagents/brief.go:162,182`, `cfbrowser`, `relevance`); those are prompts, not member-facing UI, but a model that has been told it is "Krude" can answer to the member as Krude. **Guess**: check the system prompt in `agents/primary/internal/agents` for how the member's name is injected.

**Fix.** Thread one `agentName` (from `primaryAgent(ownAgents.data)?.name`, fallback "your agent") into `HomeAsk`, `HomeFirstRun`, `HomeDesk` hello lines, `helloLine` (drop the `"Krude"` default), onboarding. Add a lint rule or a test that greps `src` for `\bKrude\b` outside comments.

---

### C14 "@Arlo-UJVX": handles and suffixed names from an old build still show

- **Severity:** medium
- **Kind:** broken
- **Seen:** prod walkthrough (mentions with suffix).

**What happens.** Mentions and seat chips show `@Arlo-UJVX` or "Arlo-UJVX" next to the clean "Arlo".

**Why.** Two layers. (1) An earlier build named primaries `Name-XXXX`. `RenameSuffixedPrimaries` fixes those at startup (`managedagents/store.go:541-590`, called from `agents/primary/service.go:165`), but it only matches the regexp `^(Ada|Arlo|...|Zara)-[A-Z0-9_-]{4}$` (store.go:541): note the list omits seven of the 50 friendly names (Alma, Bea, Dara, Eden, Ezra, Faye, Finn, ...), so an agent that had one of those is never renamed (**guess**: check against `friendlyNames`, store.go:497-503, line by line). (2) Teamspace/room seats copy the name into their own handle when the seat is created: `handle(contribution.Name, taken)` lowercases and keeps hyphens (`agents/primary/internal/rooms/service.go:488-516`), so a seat made while the agent was still "Arlo-UJVX" keeps handle `arlo-ujvx` after the rename, and the room's mention chips show it (`web-next/src/components/teamspace-room.tsx:106,145,377`; `multi-agent-team-room.tsx:88,129`). A duplicate name also gets a numeric suffix (`arlo-2`).

**Fix.** Make the rename also update `rooms` seats' handle and `agent_name`, or recompute handles from the current name when listing seats. Extend the regexp to the whole `friendlyNames` list (build it from the slice). Show `display_name` in chips and keep the handle only as the inserted text.

---

### C15 The chat-list and history buttons: the code gives them names; "15 buttons with no accessible name" is not explained by it

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod walkthrough (accessibility tree). Not reproduced in code.

**What happens / Why.** `ChatsList` rows are `<Button>` containing `<strong>{title}</strong>` and time (`chats-page.tsx:193-231`), so they have a name. History rows have the title in a `<span>` (`agent-chat-history.tsx:124-139`) and delete buttons have `aria-label` (`:163`). The owner's count of 15 therefore comes from somewhere else on those pages (**guess**: icon-only buttons in the Agent overview tabs, file rows, or the thread menu `agent-chat-menu.tsx`, or an `Input type="search"` with no visible label). Needs a live accessibility-tree pass with node ids.

**Fix.** Re-run with `read_page` and list the 15 refs, then give each an `aria-label`.

---

### C16 A first message from Home goes into a *new* thread, but only after the agents list loads; until then it sits in a module-level queue with no visible state

- **Severity:** low
- **Kind:** brittle
- **Seen:** code reading.

**What happens.** Sending from Home is correct in the normal case: it makes a new thread (`createThread`, agent-chat.tsx:392) and sends into it. If the agents query is slow or fails, the message waits and Home has already cleared the draft and expanded the panel.

**Why.** `HomeAsk.onSubmit` clears the draft immediately (`home-ask.tsx:42-45`). The panel claims the request only `if (!request || !scope || !chatAgent) return` (`workspace-agent-panel.tsx:388-390`); `chatAgent` is undefined while `agents.isPending` (:347). A failed agents query is no longer pending, so it proceeds. The queue is capped at 20 and shows "Not sent" when refused (`chat-choice.ts:227, 254-267`). The 30 s `START_TIMEOUT_MS` turns a stuck create into "Not sent. Try again." (agent-chat.tsx:135, 384-391). So the message is not lost, but there is up to 30 s of "Connecting…" with no cancel.

**Fix.** Show the message in the panel as pending as soon as it is requested (before the claim), and add a Cancel next to "Connecting…".

---

### C17 Scroll position and "follow" reset on thread switch, and a long thread jumps to the end when the member scrolls up during streaming

- **Severity:** low
- **Kind:** polish
- **Seen:** code reading; not tried live.

**Why.** `AgentConversation` follows the end until the member is within 48 px of it (`agent-conversation.tsx:5, 51-55`), re-enables following whenever `follow` changes (the last member message id: :46-49), and the log is remounted per thread (`key={threadId}`, agent-chat.tsx:497), so scroll is never restored when switching back. During a stream, the member scrolling up stops following (good), but a new member message sent from the Home panel re-pins to the end.

**Fix.** Keep a scroll offset per thread id in the panel. Low priority.

---

## Open questions (need a live test)

1. Which prod chat is the "tell me whether you can use browser" one, and does it have an unanswered confirmation? (Confirms or breaks C1; `GET /threads/{id}` `pending_confirmations`.)
2. For C2: in the stuck "Working 17m38s" chat, is the socket "open" or "reconnecting", and is `chat.status` "streaming"? Check the console and the `useAgent` socket readyState, and whether a thread refetch shows a `run_completed` bracket after the answer.
3. Does `chat.isServerStreaming` in `@cloudflare/ai-chat` stay true after a resumed stream whose finish frame was missed?
4. Which 15 buttons on the Chats page have no accessible name (C15)?
5. What does `screenContextLabel` return on the Chats/Overview pages, and is the label sent to the model?
6. Does the model call itself "Krude" to members (system prompt), even when the UI says Arlo?
7. Are any `Name-XXXX` agents or `name-xxxx` seat handles left in prod after the startup rename (C14)?
