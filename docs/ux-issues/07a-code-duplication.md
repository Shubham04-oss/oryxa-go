# 07a. Code duplication in web-next/src

Read-only review, 2026-10-25. Method: grep and reading of `web-next/src` (components, lib, hooks, routes), plus three small throwaway scripts (CSS rule comparison, orphan-file check, unused-export check). Nothing in the repo was changed.

Paths are relative to `web-next/src/`. "Guess" marks anything I inferred but did not confirm by reading the code path end to end.

Short version: the chat composer is already one component (good). The mess is in **words and numbers about time, people, agents, apps and request status**, each re-written 3 to 8 times with slightly different output, and in **how the same server data is fetched** (same endpoint, several cache keys, several limits, several sockets).

---

## Ranked items

### D1 "How long ago" / time formatting: 9 formatters, 5 vocabularies
- **Impact:** high
- **Where:**
  - `components/agents-format.ts:69` `timeAgo` ("just now", "5 min ago", "3 h ago", "2 d ago", then "Sep 3, 2026"); used by chats-page, workflow-execution, live-work, agent-chat-history, specialist-focus, agents-parts, team-synthesis, multi-agent-team-focus.
  - `components/teamspace-format.ts:93` `timeAgo` ("just now", "5 min ago", "3 hr ago", "yesterday", "Monday", "3 Sep 2026"); used by teamspace-focus, teamspaces-management, teamspace-hover-card.
  - `components/today-format.ts:108` `agoPhrase` ("Just now", "12 min ago", "3 h ago", "Yesterday", "Sep 3"); used by home-glance, home-overview, requests-view-format.
  - `components/knowledge-format.ts:19` `relativeTime` ("just now", "5 minutes ago", "yesterday", "on Sep 3"); used by library-file-table, specialist-access and `corpusTimeLabel`.
  - `components/direct-message-format.ts:74` `listTime` ("now", "5m", "3h", "Yesterday", "Mon", "3 Sep"); inbox, command palette.
  - `components/teamspace-format.ts:106` `messageTime` ("5:30 PM", "Sat 5:30 PM", "3 Sep 2026"); teamspace-room, parley-notes-card.
  - `components/request-card-format.ts:166` `whenPhrase` ("at 5:30 PM", "yesterday at ...", "on Mon, 3 Sep at ...").
  - `components/browser-pane.tsx:36` `elapsed` ("just now" / "N min ago"), `components/workflow-execution.tsx` uses `agents-format.timeAgo` while `components/agents-parts.tsx` and `workspace-tool-helpers.ts:352` `prettyTime` ("Never"/"Unknown") add two more.
- **How they differ:** the same moment reads "5 min ago", "5 minutes ago", "5m" or "5:30 PM" depending on the screen. "3 h ago" vs "3 hr ago". "Yesterday" vs "yesterday" vs "on Sep 3". Past 24 h: agents-format counts "2 d ago" up to a week, teamspace-format switches to the weekday name, today-format jumps to a date after 48 h. The cut-off for "just now" is 45 s in two, 60 s in one, "< 1 min" in two. An unparseable date gives "" in two, "at an unknown time" in two, the raw string in one.
- **Keep / merge:** one `lib/time.ts` (or `components/time-format.ts`, which already exists and is imported by only one file) with three functions: `ago(iso, now)` (pick the agents-format one but add "yesterday" and weekday, as teamspace-format does), `clock(iso)`, `listTime(iso, now)`. Re-export nothing; update the ~30 call sites. Keep `whenPhrase` as a sentence built from those. Existing tests (`today-format.test.ts`, `agents-format.test.ts`, etc.) move with the functions. One decision for the owner: "min" or "minutes", "h" or "hr". Pick the short one used by agents-format.

### D2 Day separators, clock and full-date helpers: 3 copies of the same code
- **Impact:** medium
- **Where:**
  - `components/time-format.ts:1-66` (`dayLabel`, `clockTime`, `fullTime`; only `requests-view-format.ts:4` imports it).
  - `components/direct-message-format.ts:1-72` (identical `clockFormat`, `dayThisYearFormat`, `dateFormat`, `localDay`, `dayBefore`, `dayLabel`, `clockTime`) plus its own `fullTime`-equivalent not exported; used by DM, side conversation, request cards, activity, sidebar, slack-settings.
  - `components/teamspace-format.ts:119` `fullTime`, `:167` `dayLabel(date, {short})`, and `:74-90,147-165` another 10 `Intl.DateTimeFormat` instances.
  - Also inline: `components/agent-run.tsx:90-95`, `components/call-surfaces.tsx:282`, `components/auth/cli-device-page.tsx:27`, `components/slack-settings.tsx:278`, `components/workspace-billing-settings.tsx:34`, `components/workspace-organization-settings.tsx:23` (the last two are `en-GB` fixed, everything else is the viewer's locale).
- **How they differ:** `dayLabel` exists with three different signatures (string vs Date). A user sees "Mon, 3 Sep" in DMs, "3 September" in teamspaces, "Sep 3" on Home, and "03 Sep 2026" (en-GB) in Settings → Billing and Organization.
- **Keep / merge:** `components/time-format.ts` becomes the home of all of it (it is the cleanest copy). Delete the duplicated block from `direct-message-format.ts`, `teamspace-format.ts`. Make the two `en-GB` settings dates use the viewer's locale like the rest. Merge with D1 as one change.

### D3 Same inbox endpoint polled under 4 cache keys, with 2 limits (50 and 100)
- **Impact:** high (extra traffic every 10 s, and counts that can disagree)
- **Where:**
  - `components/requests-view-queries.ts:44` `requestListQueryOptions` (key `.../inbox/filtered/all/50`): used by `workspace-sidebar.tsx:202`, `workspace-command-palette.tsx:127`, `requests-view.tsx:208,367,384`.
  - `lib/agents/queries.ts:266` `agentRequestsQueryOptions` plain (key `.../inbox/50`): `components/agents-mode.tsx:250`.
  - Same function wrapped by `homeRead` with limit 100 (key `.../inbox/100/home/<viewer>`): `components/home-overview.tsx:75,264` (`requestLimit = 100`) and `hooks/use-needs-you.ts:11,28` (`NEEDS_YOU_LIMIT = 100`), used by `workspace-shell.tsx:633` and `workspace-agent-panel.tsx:327`.
  - Every one of them has `refetchInterval: AGENT_REQUESTS_POLL_MS` (10 s, `lib/agents/queries.ts:34`).
- **How they differ:** with a busy inbox the sidebar badge counts waiting requests among the newest 50 (and says "50+" at the cap, `workspace-sidebar.tsx:219`), while the agent panel and the Requests header count among the newest 100. Same user, same moment, two numbers (guess: only visible once the inbox has more than 50 rows). Three separate cache entries means three network polls of the same list.
- **Keep / merge:** one `useInbox(org, viewer)` hook in `lib/agents/` with one limit and one key (the `homeRead` wrapper adds `viewerId` to the key, which is good and should be the key everywhere). The count (D4) is derived from it. Sidebar, palette, Requests, Home, panel, Agents tab all call it. The "origin" filter in Requests can filter the same list in memory or keep a second key only for filtered views.

### D4 "Waits on you" count: 5 implementations, 2 rules
- **Impact:** high
- **Where:** `components/request-card-format.ts:72` `waitsOnOwner` (used by `workspace-sidebar.tsx:211`, `requests-view.tsx:214,371,396`, `hooks/use-needs-you.ts:37`); `components/home-overview.tsx:83` `waitsOnMember` (used at `:383`).
- **How they differ:** `waitsOnOwner` = owner side AND (pending and not expired, OR review). `waitsOnMember` = target is viewer AND not expired AND (review OR (pending AND `approval !== "agent"`)). So a pending request that the owner's agent was set to handle counts in the sidebar, panel and Requests but not on Home ("N things need you"). Both also repeat the "expired" check differently (`isExpired` vs inline `Date.parse`). Guess: whether `approval === "agent"` rows can still be `pending` for the owner depends on server rules I did not check.
- **Keep / merge:** keep `waitsOnOwner`. Delete `waitsOnMember`; Home uses `useNeedsYou` (D3). Add one test pinning the rule.

### D5 Request status words: 5 vocabularies for one state machine
- **Impact:** high
- **Where:**
  - `components/request-card-format.ts:509` `statusOf` (the card): "Waiting for you to approve.", "Waiting for your choice.", "Waiting for {owner} to decide.", "{agent} is working on it…".
  - `components/side-conversation-format.ts:146` `cardState` (side conversation card): "Waiting for {owner} to approve.", "Waiting for {agent} to start.", "Approved by {owner}. ...", "Withheld by Oryxa.", "Not shared.".
  - `lib/threads/requests.ts:30-65` `recordedStateWords` + `requestCardState` (agent chat pointer card): "Waiting for approval", "Approved · working", "Waiting for the answer to be reviewed", "Not shared".
  - `components/requests-view-format.ts:16-45` `rowTag` (inbox row): "Waiting for your OK", "Wants your reply", "In review", "Withheld", "Declined".
  - `components/requests-activity-format.ts:250` `activitySentence`, and the card mini-line in `home-overview.tsx:706` ("Waiting for your OK").
- **How they differ:** one request that the owner has not decided is "Waiting for you to approve." on its card, "Waiting for your OK" in its row, "Waiting for approval" in the asker's chat, and "Waiting for {owner} to approve." in a side conversation. A held-for-review answer is "In review" / "Waiting for the answer to be reviewed" / "Waiting for {owner} to review the answer." A withheld one is "Withheld" / "Not shared" / "Withheld by Oryxa.". Side conversation does not know the "needs a typed reply" variant that `statusOf` has (guess: it may never get one).
- **Keep / merge:** `statusOf` in `request-card-format.ts` is the fullest and takes a `Reader`. Make it the one source of copy, and give it a short form for rows (`rowTag` becomes a view of it). `cardState` and `requestCardState` should call it (they need the request, which the side card and the chat entry can build from the same `AgentRequest`). Owner decision: one word for each status (suggest "Waiting for {owner}", "Declined", "Not shared"). Largest merge in this list; do after D3/D4.

### D6 Two agent-name fallbacks and hard-coded "Krude"
- **Impact:** high (a renamed agent still shows as Krude)
- **Where:**
  - Hard-coded: `components/home-ask.tsx:31` ("Ask Krude anything"), `home-overview.tsx:541,930,931,950`, `home-first-run.tsx:42,66,183`, `today-format.ts:209` (default param), `onboarding.tsx:206,506,613`, `onboarding-frame.tsx:69`.
  - Name-based: `agent-chat.tsx:95` ("Give {name} a task, or ask anything"), `workspace-agent-panel.tsx:484`, `home-glance.tsx:262`.
  - Fallbacks for an unnamed agent: "Primary agent" (`agent-switcher.tsx:12` `FALLBACK_AGENT_NAME`, `workspace-shell.tsx:769`, `agents-mode.tsx:327`), "Your agent" (`workspace-sidebar.tsx:449,463,701`, `workspace-agent-settings.tsx:153,158`, `teamspace-room.tsx:339`, `people-directory.tsx:454`, `workspace-settings.tsx:53`, `onboarding.tsx:46`), "your agent" (`request-card-format.ts:124`, `requests-view.tsx:650`, `home-glance.tsx:262`, `side-conversation.tsx:809`), "Agent" (`agents-format.ts:39`), "the agent" (`call-surfaces.tsx:1116`).
- **How they differ:** the member can rename their primary agent; Home still says "Ask Krude anything" and "What Krude found", the panel says "Give {NewName} a task". Home's ask box and the chat composer give two different prompts for the same agent.
- **Keep / merge:** one `useAgentName()` (or `agentNameOf(primary)`) returning `{ name, possessive, fallback }` with a single fallback ("Your agent", sentence-case, lowercased by a helper where used mid-sentence). Replace the literals. Check `onboarding*.tsx` separately: before the agent exists, "Krude" may be an intended default (guess).

### D7 App display name: `appName` copied 4 times
- **Impact:** low (identical today, so no visible difference; drift risk)
- **Where:** `components/home-glance.tsx:49`, `components/home-overview.tsx:78`, `components/agents-mode.tsx:170`, `components/onboarding-frame.tsx:46`. All: `toolkitIcon(toolkit)?.title || toolkitName(toolkit)`. `toolkitName` is `lib/apps/client.ts:825` (used in 28 places). `components/knowledge-format.ts:143` `humanizeToken` and `lib/threads/timeline.ts:1015` `humanizeToolName` do the same "snake_case to Words" job a third and fourth way.
- **Keep / merge:** move one `appName` next to `toolkitIcon` in `workspace-tools-data.ts`, delete the four copies. Fold `humanizeToken`/`humanizeToolName` into one `humanize` in `lib/`.

### D8 Brand marks: two icon maps
- **Impact:** medium
- **Where:** `components/app-logo.tsx:7-12` (4 icons: GitHub, Gmail, Drive, Notion; used by library-rail, library-source-files, knowledge-base-detail) vs `components/workspace-tools-data.ts:70` `toolkitIcon` + `components/workspace-tool-logo.tsx` `ToolkitLogo` (larger set plus remote logo plus initial; used in 9 files).
- **How they differ:** the Library shows an app with a plain initial where Tools shows its logo (for any app outside the four, and Slack in both, per the comment at `app-logo.tsx:5`). Different tile styling too.
- **Keep / merge:** `ToolkitLogo` + `toolkitIcon` survive. `AppLogo` becomes a thin caller (or is deleted; its three callers pass `provider` and `name`). 

### D9 Person identity and initials: 9 copies of `initials`, 3 of `personIdentity`, 3 of `agentIdentity`
- **Impact:** medium (avatar letters differ between screens for the same name)
- **Where:**
  - `initialsOf`: `components/people-format.ts:6`, `teamspace-format.ts:8` (identical, "?" fallback), `agents-format.ts:18` ("A" fallback), `people-management.tsx:55`, `workspace-teams-settings.tsx:63` (no fallback), `workspace-agent-panel.tsx:353`, `workspace-shell.tsx:247`, `agent-switcher.tsx:20` (inline `.split.slice.map.join`), `workspace-sidebar.tsx:766` (`name.slice(0,2).toUpperCase()`, so "Primary agent" becomes "PR" not "PA"), `workspace-agent-settings.tsx:175,224` (first letter only).
  - `personIdentity`: `people-format.ts:17`, `teamspace-format.ts:19`, plus object literals in `workspace-teams-settings.tsx:71` (a local `PersonAvatar`), `workspace-shell.tsx:244`.
  - `agentIdentity`: `agents-format.ts:33` (takes an object), `teamspace-format.ts:24` (takes id, name), `workspace-sidebar.tsx:761` (third form), `agent-switcher.tsx`, `workspace-agent-panel.tsx:351`.
  - Avatars: `direct-message.tsx:50` `PersonAvatar` (online dot), `workspace-teams-settings.tsx:71` `PersonAvatar` (different props), `agents-parts.tsx:71` `AgentFace`, `:89` `AgentAvatarStack`, `workspace-avatar.tsx:179,197` `IdentityAvatar`, `TeamspaceAvatar`, `ui/avatar.tsx` (only used by workspace-avatar and `teamspaces-management.tsx:23`).
- **How they differ:** same agent, initials "PA" in one place, "PR" in another, "P" in a third. Only matters where the DiceBear art fails to load (guess; the fallback letters are what the user sees then) and for the accessible name.
- **Keep / merge:** `components/workspace-avatar.tsx` owns `initialsOf`, `personIdentity(id, name)`, `agentIdentity(id, name)` and one `PersonAvatar` with an optional `online`. Delete the 8 other copies. Two small edits per call site.

### D10 Person name helpers: `firstName` twice, name lookup in 5 places
- **Impact:** medium
- **Where:** `firstName`: `components/people-format.ts:22` and `direct-message-format.ts:233` (identical). Name resolution: `request-card-format.ts:90` `personName`/`shortName`, `usePeopleNames` (`requests-view-queries.ts:119`), `useDmPeople` (`direct-message-people.ts:56`), `teamspace-format.ts:29` `memberName`, `lib/side/roster.ts`, `workspace-tool-helpers`/`people-management.tsx` `person.display_name || person.email` (`workspace-teams-settings.tsx:66`, `people-management.tsx`).
- **How they differ:** fallbacks are "someone", the email, "Unknown", or empty depending on the screen; "you" is only substituted in `personName`.
- **Keep / merge:** one `lib/people.ts` exporting `firstName`, `displayName(member)` (name, else email) and one `usePeople(org)` backed by `membersQueryOptions` (see D11). `personName(reader, id)` stays as the sentence-level wrapper.

### D11 Organisation members: fetched in 9 places, 4 cache-key shapes
- **Impact:** medium
- **Where:** `lib/auth/queries.ts:7-19` (`organizationKeys.members`, `membersQueryOptions`, staleTime 60 s) is used by `direct-message-people.ts:62`, `welcome-dialog.tsx:71`, `knowledge-bases.tsx:466`. Ignoring it and writing the key by hand: `workspace-billing-settings.tsx:133`, `teamspaces-management.tsx:1002`, `teamspace-invite.tsx:47`, `people-management.tsx:99,296`, `workspace-teams-settings.tsx:147` (all `["organizations", id, "members"]`, **no** staleTime); `workspace-organization-settings.tsx:74` uses `["settings","members",id]` (own cache, never invalidated with the others); `requests-view-queries.ts:133` and `home-first-run.tsx:89` use the shared key plus `viewerId` (separate entries again).
- **How they differ:** after inviting or removing a person, some lists update and others show stale rows until their own refetch. Each mount of a no-staleTime copy refetches.
- **Keep / merge:** every caller uses `membersQueryOptions(org)` (add an optional viewer argument if the viewer-scoped cache is wanted). Delete the hand-written keys. Same cleanup for `["auth","session"]`, written literally in 9 files (`routes/index.tsx:63`, `workspace-tools.tsx:187`, `people-management.tsx:410`, `workspace-organization-settings.tsx:87,99`, `workspace-answer-check-settings.tsx:76`, `specialist-chat.tsx:284`, `auth/invitation-accept-page.tsx:18`, `auth/cli-device-page.tsx:40`): add `sessionKey` to `lib/auth/queries.ts`.

### D12 Markdown: two renderers with different look and different features
- **Impact:** high (agent answers look different by screen)
- **Where:** `components/agent-timeline.tsx:295` `AgentMarkdown` (code blocks with Copy button, class `workspace-agent-markdown`, CSS `workspace.css:719-754`, used by agent chat, side conversation, request cards and review, document preview) vs `components/room-markdown.tsx:32` `RoomMarkdown` (no code block chrome, class `workspace-room-markdown`, CSS `teamspaces.css:155+`, only used by `teamspace-room.tsx:315`).
- **How they differ:** an agent's answer in a teamspace room has different spacing (13 px / 8 px gaps vs 10 px), plain `<pre>` with no language label or Copy, different heading, table and blockquote styles. `RoomMarkdown` is lazy-loaded to save weight (`teamspace-room.tsx:325`), but `AgentMarkdown` (which imports the same `react-markdown` and `remark-gfm`) is in the main bundle, so the lazy load saves nothing (guess: depends on how Vite chunks it).
- **Keep / merge:** `AgentMarkdown` survives; move it to `components/markdown.tsx` with an optional `compact` prop if the room needs tighter spacing. Delete `room-markdown.tsx` and its CSS block (about 40 rules in `teamspaces.css`) and its test `room-markdown.test.tsx` (port the sanitizing assertions).

### D13 Message list / bubble renderers: 4 families
- **Impact:** high (chat looks and behaves differently per surface), but a large merge
- **Where:**
  - Agent log/session: `components/agent-timeline.tsx:351` `MessageBlock` + `:836` `AgentTimeline` (used by agent-chat, specialist-chat, multi-agent-team-room).
  - DMs and side conversation humans: `components/direct-message.tsx:497-600` `DaySeparator`, `MessageGroup` (class `workspace-dm-*`), reused by `side-conversation.tsx` for human lines.
  - Side conversation agent lines: `components/side-conversation.tsx:590+` `AgentMessage` (class `workspace-side-agent*`, own avatar, role line, markdown).
  - Teamspace room: `components/teamspace-room.tsx:288` `RoomLine` (class `workspace-message*`, own avatar, name, time, no day separators, plain text for humans).
  - Plus `multi-agent-team-room.tsx` (a fourth room UI that uses `AgentTimeline`) and `parley-notes-card.tsx`.
- **How they differ:** DMs group by minute and show one time per group with day separators ("Today"/"Yesterday"); the room shows a time on every line ("Sat 5:30 PM") and no day separators; agent chat uses bubbles for the member and a plain column for the agent; side conversation agent lines show "name · role · time". Delivery state ("sending", "Seen", retry) exists only in DM groups. A teamspace room and a team room are both group chats with agents but share nothing.
- **Keep / merge:** this is the biggest structural item. Cheapest first step: one `Message` row component (avatar, name, tag, time, body) and one `DaySeparator` used by room, side agent line and DM group, with `AgentMarkdown` for agent bodies (D12). Longer term: `MessageGroup` (DM) as the human-message primitive, `AgentTimeline` for agent work, and fold `multi-agent-team-room` into the teamspace room (guess: team rooms may be the archived "teams" feature from 2026-09-30; check with the board before spending time on it).

### D14 Search / filter field: 11 hand-built versions
- **Impact:** medium
- **Where:** `chats-page.tsx:141`, `workspace-tools.tsx:686`, `requests-view.tsx:534`, `people-directory.tsx:192`, `messages-page.tsx:168`, `teamspaces-management.tsx:422`, `direct-message-inbox.tsx:421`, `teamspace-invite.tsx:111`, `workspace-tool-access-settings.tsx:275`, `knowledge-bases.tsx:607`, `pdf-viewer.tsx:513`. Some use `ui/input-group` (`messages-page`, `people-directory`), others a raw `<input>` in a `<label>` with their own CSS.
- **How they differ:** Escape clears the text in `messages-page`/`people-directory` but leaves the screen in `direct-message-inbox.tsx:426`; only `people-directory` shows the `/` shortcut hint; icon size 15, 16 or 17 px; clear (X) button only in `requests-view`.
- **Keep / merge:** one `SearchField` on `ui/input-group` (`value`, `onChange`, `placeholder`, `onEscape?`, `shortcut?`). 

### D15 Message composer: one component, 3 outliers, plus 3 skeleton copies
- **Impact:** medium (the good news: the main composer is shared)
- **Where (shared):** `components/agent-composer.tsx:26` `AgentComposer` is used at `home-ask.tsx:29`, `agent-chat.tsx:544,769,1130`, `specialist-chat.tsx:399,927`, `side-conversation.tsx:467`, `direct-message.tsx:378`, `teamspace-room.tsx:219`, `multi-agent-team-room.tsx:175`, `workspace-agent-panel.tsx:1093`. Enter sends, Shift+Enter newline, IME-safe (`:117`), grows to 6 lines, stop button while streaming.
- **Where (outliers):**
  - `components/request-card-reply.tsx:125-175` `ReplyField`: its own copy of the auto-grow code (`fit`, `ResizeObserver`, `multiline` state, lines 85-115 against `agent-composer.tsx:68-100`), **Ctrl/Cmd+Enter** to send, max 8 lines, no IME guard.
  - `components/ask-agent-dialog.tsx:124-140`: plain `Textarea`, 4 rows, Ctrl/Cmd+Enter, no IME guard.
  - Other textareas are form fields, not composers (`knowledge-base-detail.tsx:370`, `workflow-form.tsx`, `teamspaces-management.tsx:920`, `specialists-section.tsx:195`, `multi-agent-teams-section.tsx:286`, `knowledge-bases.tsx:228`, `workspace-tool-connection.tsx:864`).
  - There are no attachment controls in any composer (grep for "attach" and `type="file"`: only the Library upload and tools settings); so nothing to unify there.
- **How they differ:** in a chat, Enter sends; in "Reply yourself" and "Ask agent", Enter adds a line and the hint must say Ctrl+Enter. Placeholders for the same agent: home "Ask Krude anything" (`home-ask.tsx:31`) vs chat "Give Krude a task, or ask anything" (`agent-chat.tsx:95`). Disabled/connecting state: `agent-chat.tsx:769` ("Connecting…", send disabled), `specialist-chat.tsx:399` (blocked reason or "Connecting…"), `workspace-agent-panel.tsx:1093` ("Chat isn't connected yet.", whole field disabled): three near-identical skeleton composers with three different messages. Draft is cleared on submit in `home-ask`, `agent-chat`, `specialist-chat` but after the send resolves in `direct-message.tsx:263` and `teamspace-room.tsx:100` (guess: a failed send in the first group loses the text).
- **Keep / merge:** extract the auto-grow into a `useAutoGrow(ref, {minLines, maxLines})` hook inside `agent-composer.tsx` and use it in `ReplyField` (about 40 lines gone). Give `AgentComposer` a `sendOn="enter" | "mod-enter"` prop and use it in the reply field and ask dialog (add the IME guard there). Replace the three disabled copies with one `<AgentComposer disabled status=... />` whose copy comes from one helper. Make the home prompt use `composerPlaceholder` from `agent-chat.tsx`.

### D16 Destructive confirm: 6 patterns
- **Impact:** medium (the user meets a different "are you sure" on each delete)
- **Where:**
  - Modal `Dialog`: `agent-chat-menu.tsx:184` (delete chat).
  - `ConfirmDialog` (own component, exported from the wrong file): `knowledge-base-detail.tsx:731`, used `:1187,1215`.
  - `ConfirmStep` inline panel: `agents-parts.tsx:333`, used by specialist-focus, multi-agent-team-focus, workflow-overview.
  - Inline Delete/Cancel buttons in the row: `agent-chat-history.tsx:140-175`.
  - `SettingsActions` + `SettingsButton` row: `people-management.tsx:190-215`.
  - Own `workspace-teamspace-confirm` group: `teamspaces-management.tsx:1096+`.
  - `Popup` has a `PopupConfirm` (`popup.tsx:187`) used by nobody (see Dead code).
- **How they differ:** modal vs inline, button order, wording ("Delete" vs "Delete collection" vs "Remove"), "Deleting…" pending copy, whether the error shows.
- **Keep / merge:** `ConfirmDialog` (move to `components/confirm-dialog.tsx`, built on `ui/alert-dialog`) as the one pattern for destructive actions; keep the inline `ConfirmStep` only if the design contract (DESIGN.md) wants inline panels in Settings (guess: it may; check §1 before deleting). Remove the other four.

### D17 Dialog / popover / menu: hand-rolled outliers beside shadcn
- **Impact:** low to medium
- **Where:** `ui/dialog` directly in 9 files (`workspace-tool-credential-dialog.tsx`, `knowledge-base-detail.tsx`, `agent-chat-menu.tsx`, `teamspaces-management.tsx`, `teamspace-invite.tsx`, `workspace-settings.tsx`, `workspace-teams-settings.tsx`, `knowledge-bases.tsx`, `ask-agent-dialog.tsx`); the themed `Popup` (`popup.tsx`, "one themed dialog" per its header comment) in only two (`welcome-dialog.tsx`, `call-surfaces.tsx`). Hand-rolled: `side-conversation.tsx:1250-1345` `TargetPicker` (own outside-click `pointerdown`, `role="menu"`, arrow keys, focus handling: a dropdown menu rebuilt by hand); `call-surfaces.tsx:830` devices popover (`role="dialog"`, no focus trap); `call-surfaces.tsx:1281` `call-full` modal (`role="dialog" aria-modal`, own Escape handler at `:1244`). Menus on `ui/dropdown-menu`: `agent-chat-menu`, `workspace-sidebar`, `teamspaces-management`.
- **How they differ:** the hand-rolled ones have no focus trap or return-focus (guess; I did not run them), and a different look from the shadcn ones. Dialogs look different depending on whether they use `Popup` or raw `Dialog`.
- **Keep / merge:** `TargetPicker` becomes `ui/dropdown-menu` radio items (`DropdownMenuRadioGroup`, already used by the sidebar theme menu). Decide once whether `Popup` is the standard frame for all dialogs; if not, delete the scene variants nobody uses.

### D18 Buttons: shadcn `Button` vs 9 hand-styled button classes
- **Impact:** medium
- **Where:** `ui/button` in 56 files; `SettingsButton` (`settings-kit.tsx:232`, a wrapper of `Button`, 12 files); and raw `<button className=...>` (215 `<button` elements in components): `workspace-agents-button` (36 uses; `agents-mode.css:76`, own `primary`/`danger`/`quiet-danger` variants), `workspace-agent-pill` (20; `workspace.css:783`), `workspace-agent-text-button` (16; `workspace.css:713`), `workspace-tool-text-button` (14), `auth-submit` (7), `workspace-tool-action` (6), `workspace-agent-new` (5), `call-btn` (5), `workspace-tools-pill` (4), `live-link` (4), `live-button` (3), `workspace-teamspace-secondary-button` (`workspace.css:351`).
- **How they differ:** radius (10 px, 999 px, shadcn default), height (28, 36), hover colours, disabled opacity (`.45` here, shadcn's default there). The same "Try again" is a link-variant `Button` on Home (`home-glance.tsx`, `home-overview.tsx:115`), `workspace-agent-text-button` in agent chat (`agent-timeline.tsx:810`) and a bare `<button>` in conversation notices (`conversation-log.tsx:376`).
- **Keep / merge:** `ui/button` is the survivor. Map: `workspace-agents-button` to `variant` default/destructive/outline, `*-text-button` to `variant="link"`, `*-pill` to a `size="pill"` (or `Badge`/`Toggle` where it is pressed state: `workspace-agent-pill[aria-pressed]` is a toggle). Keep `call-btn` and `live-button` if call/live UI is intentionally different (guess). `SettingsButton` can stay as a thin wrapper.

### D19 Badges, pills, chips, tags: 12 classes beside `ui/badge`
- **Impact:** medium
- **Where:** `ui/badge` in 16 files; hand-styled: `people-pill` (`people-directory.tsx`, 7), `workspace-agent-pill`, `workspace-tools-pill`, `workspace-presence-pill` (`teamspace-room.tsx:125`, `multi-agent-team-room.tsx:112`, `specialist-chat.tsx:187`: three copies of the same "who is here" pill), `workspace-agent-chip`, `library-tag`, `settings-chip`, `workspace-room-tag`, `requests-row-tag`, `popup-tag`, `home-chip`, `workspace-knowledge-chip`, `workspace-unread-badge`, `workspace-tools-connected-badge`, `StatusPill` (`agents-parts.tsx:38`, Active/Paused; the same words also come from `teamspace-format.ts:37` `statusLabel` and `agents-format.ts:45` `statusLabel`). 23 of the 36 CSS files declare `border-radius: 999px` rules.
- **How they differ:** padding, font size (10 to 12 px), colours for the same state.
- **Keep / merge:** one `Badge` with `tone` (neutral, live, warning, danger) for all of these; `StatusPill` becomes `Badge`. One `statusLabel`. The three `workspace-presence-pill` instances become one `PresencePill` component.

### D20 Notices, empty and loading states: 10 variants
- **Impact:** medium
- **Where:**
  - "Couldn't load X. Try again" blocks: `home-glance.tsx:463` `Notice`, `home-overview.tsx:106` `ReadNotice` (near-identical; both skip the retry for a missing service, but with different tests: `home-glance.tsx:455` `unavailable` adds 404, `home-overview.tsx:115` does the same inline), `agents-parts.tsx:47` `AgentsNotice`, `workflow-execution.tsx:46` `WorkflowQueryNotice`, `conversation-log.tsx:369` `ConversationNotice`, `agent-timeline.tsx:803-815`, `direct-message-inbox.tsx:436`, `settings-kit.tsx:201-260` `SettingsStatus/SettingsNote/SettingsMessage`, `auth/auth-shell.tsx:100` `AuthNotice`.
  - Empty: `direct-message.tsx:591` `EmptyConversation`, `side-conversation.tsx:1153` `EmptySide`, `workspace-agent-empty` (many), `workspace-dm-empty`, `workspace-knowledge-loading`.
  - Loading: `ui/skeleton` used in 2 files (`knowledge-bases.tsx`, `onboarding-frame.tsx` `Ghost`), plain "Loading…" text in about 47 places.
  - "service not available" test written three ways: `lib/agents/policy.ts:146` `isUnavailableHere` (503 plus code), `home-glance.tsx:455` (that or 404), `workspace-agent-settings.tsx:37` (404 or 503, no code check).
- **How they differ:** wording ("Couldn't load…" / "can't be loaded right now" / "Loading request…"), tone and retry style.
- **Keep / merge:** one `<QueryState query={...} loading=... empty=... error=... />` and one `isUnavailable(error)` helper in `lib/api` that replaces all three. Start with Home (the two copies are nearly identical) and the Agents tab.

### D21 Error-to-message mappers: 13 functions, 5 different 429 texts
- **Impact:** medium
- **Where:** `agents-format.ts:110` `agentsErrorMessage`, `lib/auth/client.ts:40` `authErrorMessage`, `lib/apps/client.ts:985` `appsErrorMessage`, `components/slack-settings.tsx:110` `slackError`, `lib/rag/client.ts:827` `ragErrorMessage`, `teamspaces-management.tsx:157` `refusal`, `ask-agent-dialog.tsx:36` `refusal`, `workflow-overview.tsx:34` `workflowRefusal`, `knowledge-bases.tsx:83` `createError`, `call-provider.tsx:316` `joinError`, `auth/cli-device-page.tsx:13`, `teamspace-launch.tsx:72` `writeError`, `hooks/use-teamspace-room.ts:657` `sendErrorMessage`, `lib/threads/rename.ts:71`. 102 `instanceof ApiError` checks overall.
- **How they differ:** 429 reads "Too many requests. Try again in a minute." (agents, rag), "Too many attempts. Try again in a minute." (auth), "Too many changes at once. Try again in a moment." (teamspaces); 401 reads "Your session has ended. Sign in again." vs "...Sign in again to continue."; a 5xx gives each caller's own fallback.
- **Keep / merge:** one `errorMessage(error, { fallback, byStatus?, byCode? })` in `lib/api/` that owns 401, 403, 429, 5xx and "network down"; the 13 call sites pass only their special cases (they already pass copy tables).

### D22 `sentence()` x6, `plural/countLabel` x3, code-point counters x4, byte formatters x2
- **Impact:** low (identical logic; maintenance and slight differences)
- **Where:**
  - Capitalise and end with a full stop: `workspace-tool-helpers.ts:348` (no capital), `teamspaces-management.tsx:116`, `agents-format.ts:98` (strips a sentinel), `lib/rag/client.ts:817`, `lib/apps/client.ts:1665` `sentenceOf`, `hooks/use-teamspace-room.ts:650`.
  - Counting: `agents-format.ts:83` `plural`, `knowledge-format.ts:73` `countLabel`, `teamspaces-management.tsx:119` `people`, `direct-message-format.ts:228` `countLabel` (a different job: "50+").
  - Code points: `agents-format.ts:9` `runeCount`, `lib/dms/protocol.ts:348` `codePoints`, plus `[...x].length` inline in 8 places (`request-card.tsx:99`, `request-card-review.tsx:21`, `request-card-secret.tsx:104`, `workspace-tools.tsx:309`, `lib/threads/client.ts:239,417`, `lib/apps/client.ts:1594`, `lib/agents/screen-context.ts:24`).
  - Bytes: `components/sandbox-pane.tsx:88` `formatSize` ("1.5 KB", one decimal) vs `lib/files/client.ts:105` `formatBytes` ("1.5 KB" below 10, whole number above, up to GB).
- **How they differ:** one decimal vs rounding; "ends with a full stop" regex differs (`…` accepted in two); one strips a sentinel.
- **Keep / merge:** `lib/text.ts` with `sentence`, `plural`, `codePoints`; `formatBytes` from `lib/files/client.ts` replaces `formatSize`. Pure mechanical edit.

### D23 Realtime: `useRoomLog` opens one WebSocket per hook call, and `useRoomCall` is called in 7 places
- **Impact:** medium (extra sockets and polls; guess on how many are mounted together)
- **Where:** `lib/live/use-room-log.ts:111` (`new WebSocket(liveSocketUrl(..."/live"))` inside the hook, state local to each instance). `useRoomCall` (`lib/call/use-room-call.ts:36` calls `useRoomLog`): `call-surfaces.tsx:324,387,589`, `agent-panel-chips.tsx:84`, `call-stage.tsx:311`, `workspace-sidebar-live.tsx:82`. `live-work.tsx:37,169` adds two more `useRoomLog` instances for the same room. The call state query (`callKey`) is shared by react-query, the socket is not.
- **How they differ:** with the sidebar, the agent panel chips and the call dock all mounted, one room is followed by several sockets and several `CALL_REFRESH_MS` polls (guess: some of them are `enabled: false` until a call exists; I did not trace every `enabled`).
- **Keep / merge:** a `RoomLogProvider` (or a module-level ref-counted store keyed by `org:kind:id`) so many components share one socket; `useRoomCall` reads from it. Same pattern as the existing `PresenceContext` (`direct-message-presence.ts`).

### D24 Polling intervals and limits written as numbers
- **Impact:** low
- **Where:** `home-overview.tsx:279-316` (30_000, 60_000 repeated 6 times), `lib/threads/client.ts:703` (30_000), `hooks/use-teamspaces.ts`, `home-live-pill.tsx:97`, `teamspace-focus.tsx:421`, vs named constants in `lib/agents/queries.ts` and `lib/call/use-room-call.ts`. Limits: `AGENT_REQUESTS_LIMIT` 50, `requestLimit` 100, `NEEDS_YOU_LIMIT` 100, `ACTIVITY_LIMIT` 100, `workspace-tools.tsx:312` 100.
- **Keep / merge:** `lib/polling.ts` with `POLL_FAST`, `POLL_NORMAL`, `POLL_SLOW`; handle together with D3.

### D25 Per-component CSS re-declaring shared styles
- **Impact:** low to medium (visual drift, size)
- **Where:** 37 CSS files under `components/` (36 plus `auth/auth.css`), 12,247 lines, plus `workspace.css` (1,020 lines, which already holds shared button/pill/markdown rules). A rule-body comparison across files (5+ declarations, same body, different selector) found only 11 pure clones, mostly: the muted one-line ellipsis caption (`agent-run.css:204`, `people-directory.css:241`, `home-overview.css:312`, `file-viewer-frame.css:32`, `teamspaces.css:649`), the flex column "fill" box (`slides-viewer.css`, `media-viewer.css`, `sheet-viewer.css`, `file-viewer-frame.css:2`, `agents-mode.css:331`), the reset column list (`chats-page.css:32`, `side-conversations.css:37`, `requests-view.css:78`, `workspace.css:525,529`). The real duplication is the near-clones (same idea, 1 to 2 properties apart), which a body-equality check does not catch: pills (23 files with `border-radius: 999px`, see D19), buttons (D18), the two markdown stylesheets (D12), and the three popup/overlay shells (`popup.css`, `call.css`, `workspace.css`).
- **Keep / merge:** fix D12, D18 and D19 first; these remove most of the duplicate CSS. Then add 3 utility classes (`.truncate-caption`, `.fill-column`, `.plain-list`) to `workspace.css`. Not worth a CSS-wide rewrite.

### D26 People/agent chips, task bar, "looking at" context: 2 sources
- **Impact:** low
- **Where:** `agent-composer.tsx:106-122` has two sources of "Looking at X" (the `AgentComposerLookingAt` context from the panel, and `context` from `agent-chat.tsx` `LookingAt`), with a comment saying the panel wins; `agent-panel-chips.tsx` and `workspace-agent-panel.tsx:725-785` build the panel's own copy of the same reading.
- **Keep / merge:** one source (the panel's context), drop `context` from `agent-chat.tsx:544,1130` once nothing renders `LookingAt` outside the panel (guess: `agent-chat` can also render outside the panel, e.g. Agents tab; check `getScreenContext` callers).

---

## Dead code

How each was checked: a script listed every non-test file under `src/` with no importer outside tests (`from "…/<name>"` and `import("…/<name>")`), and every `export` with no reference in any other non-test file; then I grepped the name by hand.

- `components/ui/scroll-area.tsx`: not imported by any file (not even by other `ui/` files). Safe to delete.
- `components/agents-parts.tsx:421` `WorkflowRow` (about 70 lines), with `scheduleLabel`, `executionWords`, `executionLabel`, `RECENT_EXECUTIONS` (`:393-419`) that only it uses: exported, referenced nowhere else, no test. A second, live copy of `executionWords` is in `workflow-execution.tsx:19`.
- `components/popup.tsx:130,145,187` `PopupList`, `PopupLine`, `PopupConfirm`: referenced only by `popup.test.tsx`. `Popup` itself has two callers (`welcome-dialog.tsx`, `call-surfaces.tsx`). Also check which of the four `PopupScene` values are still used (guess: `sea`, `harbour`, `deck` may only be used by the removed teamspaces pop-ups).
- `components/direct-message-format.ts:125` `dmTimeline`: referenced only by its own test; `conversationTimeline` (`:144`) replaced it.
- `lib/agents/agent-requests.ts:834` `SECRET_MIN_TTL_SECONDS`, `lib/agents/managed-agents.ts:41` `MAX_GRANT_ENTRIES`, `lib/dms/request-notes.ts:63` `REQUEST_ORIGIN_AGENT`, `lib/knowledge/client.ts:81,82,86` (`minSourceIntervalSeconds`, `maxSourceIntervalSeconds`, `maxRunsLimit`), `lib/rag/client.ts:222,228` (`maxCorpusNameLength`, `maxMetadataValueBytes`): exported constants with one reference (their own definition) and no tests. Keep the ones that mirror a server limit as comments if wanted; otherwise delete.
- `components/time-format.ts` is not dead (one importer), but is the survivor for D1/D2.
- Not dead, but test-only: `components/agents-fake-backend.ts`, `components/request-card-fixtures.ts` (imported only by `*.test.tsx`). They live in `components/`, so they ship in the module graph only if something imports them (nothing does); safe, but better under `src/test/`.
- Old and new side by side (alive, not dead): `agents-mode.tsx` with its specialists/teams/workflows screens versus the teamspace screens (see D13); `home-glance.tsx` versus `home-overview.tsx` (both render Home widgets: `home-overview.tsx:75-88` and `home-glance.tsx:49` share copied helpers). My notes (not the repo) say teams, workflows, teamspaces, DMs and specialists were meant to be archived from the UI on 2026-09-30, while web-next still carries all of them; whether those screens are meant to be reachable is a question for the owner, not something the code answers.

## What I could not settle
- Which duplicate sockets and polls are actually live at once (D23, D3): needs a browser run with the network panel, not reading.
- Whether the Home vs sidebar "waiting" counts really differ on real data (D4): depends on whether a `pending` request with `approval: "agent"` can reach the owner's inbox; the Go service decides.
- Whether the inline `ConfirmStep` and the hand-rolled `TargetPicker` are intended by `DESIGN.md` (D16, D17); I did not read it in full.
