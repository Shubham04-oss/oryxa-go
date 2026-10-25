# Core flows, driven end to end

Driven on production (app.oryxa.in, owner account) on 2026-10-25 at about 17:40 IST. I used a new chat with my own agent and sent four messages:
- "UX test: what can you do…";
- the newest Gmail subject;
- create a Drive doc;
- cancel that.

Nothing was created in any connected app. I opened setup forms but didn't submit them. DMs and parleys were looked at only. IDs F1, F2, ...

## Search and jump-to (⌘K)

### F1 Jump-to finds only the teamspace, request texts and recent chats: no pages, no people, no files

- **Severity:** high
- **Kind:** confusing
- **Seen:** prod ⌘K.

**What happens.**

| Typed | Found |
|---|---|
| "settings" | nothing |
| "apollo" (a member's name) | nothing |
| "Clef" (in a Library file title, "jev: Cloudflare's Clef as a drop…") | nothing |
| "deploy" | three request texts and one chat |

None of the 1,173 Library files that start with "deploy:" showed up. The palette's own hint says "Go to a teamspace, a request, a chat or a file", but files never appear.

**Why.** Guess: the palette searches only the lists the shell has already loaded, which are teamspaces, the inbox and the newest chats. It doesn't search the pages map, members, or the library.

**Fix.**
- Add static entries for every page and settings section (the `headings` map in `workspace-shell.tsx:84`).
- Add members, with "Message" and "Ask their agent" actions.
- Search library titles through the existing documents API, with a debounce.

### F2 Every query offers "Have Arlo open a page and show you · Browser preview"

- **Severity:** low
- **Kind:** confusing
- **Seen:** prod ⌘K: shown for "settings", "Clef" and "apollo".

**What happens.** Pressing ⌘↵ on any query starts an agent run. Pressing Enter on "settings", where you meant to go to Settings, asks Arlo about "settings".

**Fix.** Offer the browser action only when the query looks like a URL or a site name. Make ⌘↵ the only way to ask Arlo. Enter must never start a run when no "Go to" result is selected.

## Chatting with your agent

### F3 There's no way to attach a file to a message

- **Severity:** high
- **Kind:** missing
- **Seen:** prod composer. There's no attach or upload button, and dropping a file does nothing visible (the drop itself wasn't tested).

**What happens.** The member can't say "summarise this PDF". The only way to give the agent a file is to upload it to the Library and hope retrieval finds it.

**Fix.** Add an attach button to the composer. Upload through the existing files service into the chat's scope, and pass the file id with the message.

### F4 Write actions are confirmed in plain text, so nothing tracks them

- **Severity:** high
- **Kind:** brittle
- **Seen:** prod. "Create a Google Drive document titled 'UX test doc'…" came back as "I am about to create… Please confirm this action so I can proceed." There were no Approve or Deny buttons, the status was "Done", and Home's "Needs you" stayed empty.

**What happens.** Whether something gets confirmed depends on the model's wording, not on the tool-confirmation mechanism that Home and the chat list track. A member who never comes back to the chat never sees the ask. Typing "yes" is the only way to approve, and a yes in the wrong chat goes nowhere.

**Why.** Guess: the model asks in prose before calling the write tool, so the run ends "Done" and no confirmation item exists. The instruction or prompt telling it to "confirm before writing" overlaps with the real confirmation gate. Look at the Krude/Arlo system prompt and at whether the Drive create tool is marked as needing confirmation.

**Fix.** Make the confirmation gate the only path for write tools: tell the model to call the tool and let the gate ask. If prose asks stay, turn "Please confirm…" into a pending item with buttons.

### F5 Every turn costs 100k–340k tokens, even "cancel that"

- **Severity:** high
- **Kind:** slow / cost
- **Seen:** prod run footers in one new chat.

| Message | Time | Tokens |
|---|---|---|
| "what can you do" | 1 s | 2.6k |
| newest Gmail subject | 16 s | 151k |
| create Drive doc | 11 s | 342k |
| "No, cancel that" | 2 s | 115k |

**Why.** Guess: once a tool search has run, the loaded tool catalogue (Composio schemas for five apps) and the tool results stay in context, and they're re-sent on every turn. The first, tool-less answer was 2.6k.

**Fix.**
- Drop tool schemas from context after the turn that used them, or load them per turn only when Jev picks them.
- Trim large tool results to a summary once they've been answered.
- Track tokens per turn in metrics, so a regression is visible.

### F6 The run shows plumbing steps: "List tools", "Load tools", "Check Gmail connection", "Find Gmail tools" twice

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod. Of the 6 steps in the Gmail question, only one, "Gmail · Read data", was the actual work.

**Fix.** Collapse discovery steps (listing, loading or finding tools, connection checks) into one "Getting Gmail ready" line, or hide them. Show the real actions by name: "Read your newest email".

### F7 The footer shows token counts to members ("Done · 16s · 151k tokens")

- **Severity:** low
- **Kind:** confusing
- **Seen:** prod, every answer. Formatted in `web-next/src/components/agent-run.tsx:79-84`.

**Fix.** Keep the time and hide tokens behind a details toggle, or show them only to owners or admins.

### F8 Math is shown as raw LaTeX

- **Severity:** low
- **Kind:** polish
- **Seen:** prod. In the presentation chat (15 Oct), the answer shows "atmospheric $\text{CO}_2$ concentrations".

**Why.** The markdown renderer has no math plugin: there's no `katex` or `remark-math` in web-next.

**Fix.** Tell the model to write plain text such as "CO₂", which is the cheapest fix, or add `remark-math` and `rehype-katex`.

### F9 "Open" on a generated file downloads it; there's no preview

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod. "Open" on `global_warming_presentation.pptx` is a link to `/v1/…/artifacts/…pptx?version=1`, and the server sends `content-disposition: attachment`, 707 KB.

**Fix.** Label it "Download". For a preview, render the slide images the agent already generated, or convert to PDF on the server.

### F10 The whole chat column and chat list dim and lock while an answer runs

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod. During every run the chat list is greyed out and can't be clicked. The composer shows "Connecting…" under it on the first send.

**What happens.** You can't read another chat while one is answering. Combined with C2 (an answer stuck on "Working"), you can stay locked out. See C9 in [02-agent-chat.md](02-agent-chat.md).

**Fix.** Let people switch chats freely. The run continues on the server, and its chat shows a running dot in the list.

### F11 Chat list: no actions on rows, and test chats pile up forever

- **Severity:** low
- **Kind:** confusing
- **Seen:** prod. The list holds 27 chats, including "hey baby what is up", "hi there" ×5 and "Requests". Rename and Delete exist only in the open chat's "…" menu.

**Fix.**
- Hover actions on rows: rename and delete.
- Multi-select delete.
- Name chats from the first answer, not the first message, so "hi there" chats get useful titles.

### F12 The Live tab says "Couldn't load this conversation's live work." on chats that never used a browser

- **Severity:** medium
- **Kind:** broken
- **Seen:** prod. Both the new test chat and the presentation chat show it. Root cause: B9 in [03-browser-and-live.md](03-browser-and-live.md).

**Fix.** A 404 means "Nothing live in this chat"; show an error only for real failures.

## Specialists, teams, workflows

### F13 Setup is a hidden chain: workflow needs a team, a team needs a specialist

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod agent overview. "New team" and "New workflow" look like buttons but do nothing beyond showing a line of text: "A team needs at least one active specialist. Create one in Specialists first." and "Create a multi-agent team before creating a workflow."

**Fix.** Show one guided setup ("Make your first specialist → team → workflow"), with the later steps visibly locked until earlier ones exist. Or let "New team" create its first specialist inline.

### F14 The New specialist form has no tools, no examples, and no field labels

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod. The form is inline in the narrow overview column, with three text boxes: name ("e.g. Pipeline reviewer"), description and instructions. The labels exist only as placeholders in the accessibility tree.

**What happens.** The form doesn't ask which apps or knowledge the specialist may use, which model or effort, or who can use it. A member has to guess what good instructions look like.

**Fix.**
- Offer two or three templates.
- Add a tools and knowledge picker that defaults to the member's connected apps.
- Make the labels real labels.
- Open it in a dialog or a full page, not a 300 px column.

## Layout across pages

### F15 Opening "At a glance" on the Chats page makes four columns and crushes the overview

- **Severity:** high
- **Kind:** broken (layout)
- **Seen:** prod at 1279 px. The Chats page already has three columns: list, chat and agent overview. "Open At a glance" adds a fourth. The overview then wraps one word per line ("Your / agent / reads / LinkedIn, / Zoho…"), and its tabs are cut off: "Requests" disappears.

**Fix.** The right panel replaces the overview column on the Chats page. Or set a minimum width per column and auto-collapse the overview when the panel opens.

### F16 Back from Chats goes to "Agent overview", not where you came from

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod. Home → Chats → ← lands on Agent overview. On the Chats page, the rail is replaced by a single back arrow.

**Why.** The shell's per-area "last page" memory (`workspace-shell.tsx` around line 70: "each still belongs to an area, which remembers its last page") puts Chats under the agents area. There's no real history (H2).

**Fix.** Comes with H2: real URLs and browser history make "back" mean back.

## Requests

### F17 Arlo answered a teammate from stale indexed docs, and the teammate had to argue with it

- **Severity:** high
- **Kind:** brittle (trust)
- **Seen:** prod Requests. At 16:53–17:00 the member "Shubham" asked Arlo seven times about the owner's deployments. The final shared answer said "deployments are currently disabled and on hold (gh workflow disable Deploy was set on 2026-10-24)", citing issue #504 and PR #494. That was already false: production released twice today. In between, the teammate wrote "look in his gh man ig that is stale info" and "nope this is wrong look in his gh action".

**What happens.** The agent prefers retrieved Library text, which is GitHub issue and PR bodies, over a live GitHub call. It doesn't say how old its source is. The owner "approved" the share (the request says "Approved in Oryxa by you at 17:01"), probably without seeing that it was stale.

**Fix.**
- When a question is about current state ("now", "live", "running", "recent"), the agent checks the live tool before answering from knowledge.
- Every answer from knowledge shows its source and date ("from issue #504, 24 Oct").
- The approval card shows those sources, so the owner can spot stale ones.

### F18 One conversation with a teammate becomes seven separate request rows

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod Requests. The seven asks above are seven rows, each "Answered by Arlo", and each detail shows only its own question and answer. Nowhere can you read the exchange as a conversation.

**Fix.** Group asks from the same person within a short window, or from the same DM thread, into one row with a thread view. Show the count ("7 messages").

## People

### F19 Sidebar counts disagree with the page

- **Severity:** low
- **Kind:** polish
- **Seen:** prod. The sidebar shows "People 2" while the page says "3 people, 3 agents you can reach". The sidebar's Messages row shows the "Shubham" avatar overlapping the name on desktop too, not only on mobile (H12).

**Fix.** Count everyone and say "2 others" if that's what's meant. Fix the avatar row's spacing.
