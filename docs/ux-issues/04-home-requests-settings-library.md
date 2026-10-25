# Home, Requests, Settings, Library, and the shell around them

Walked on production (app.oryxa.in, owner account) on 2026-10-25, desktop and 375 px mobile. Read-only: nothing sent, no setting changed. IDs H1, H2, ...

### H1 Slack is switched off in production by a hidden local file

- **Severity:** blocker
- **Kind:** broken
- **Seen:** Settings → Slack says "Slack isn't available here. This server doesn't run Oryxa's Slack app." Every `/v1/organizations/<org>/apps/slack/{status,link,install}` call answers 404. These are the console 404s on every page.

**What happens.** Slack has been off in production since at least 15 Oct, even though Slack was turned on there on 30 Sept. Asks from Slack stop arriving: the newest "· Slack" item in Requests is from 30 Sept. Owners can't install or link Slack, and the Slack consent loop is off (`AGENTS_PRIMARY_SLACK_CONSENT=off`).

**Why.**
- `deploy/production/slack.auto.tfvars.json` (tracked) says `"slack_app_ready": true`.
- On the release laptop, an untracked `deploy/production/zz-restore.auto.tfvars.json` (13 Oct) says `{ "slack_app_ready": false }`. Tofu loads `*.auto.tfvars.json` in name order, so `zz-` wins.
- It is hidden from `git status` by `.git/info/exclude:19` (`deploy/production/zz-*.auto.tfvars.json`), so nobody sees it.
- `tofu console` gives `var.slack_app_ready` = `false`.
- Every task definition checked, 120 (15 Oct) through 191 (today), has `APPS_SLACK_SURFACE=false`. That drives `compute.tf:131-133`.
- It was probably left behind by a restore around 13 Oct (a guess).

**Fix.**
1. The owner decides whether Slack should be on. If yes, delete `zz-restore.auto.tfvars.json` and its exclude line, then plan and apply with approval. The plan should show only the three Slack env values plus `APPS_SERVICE_CALLERS` changing.
2. Prevention: CI or the release script fails when any untracked `*.auto.tfvars*` exists in `deploy/production`. A release should be built only from tracked files.

### H2 The address bar never changes: refresh and Back lose your place

- **Severity:** high
- **Kind:** broken
- **Seen:** prod. After visiting Requests, Chats, Q1, Library and Settings, `location.href` was still `https://app.oryxa.in/` and `history.length` was 2.

**What happens.**
- Refreshing on any page drops you on My workspace.
- Back leaves the app instead of going to the previous page.
- You can't copy a link to a teamspace, a chat or a request and send it to a teammate.
- Two tabs can't show two different places.

**Why.**
- `web-next/src/routes/` has a single app route, `/`.
- The route already accepts `?view=` and `?dm=` (`routes/index.tsx:37-46`), and the billing redirect uses `view` (`routes/settings.billing.tsx:16`). The shell never writes them.
- Navigation is React state: `useState<PrimaryMode>` and per-area sections in `components/workspace-shell.tsx:154` and around it. Selecting a teamspace uses `teamspaces.select(...)`, not the URL.

**Fix.** Put the page in search params and push them on every navigation: `view` (overview, requests, chats, teamspace, knowledge, settings section), `teamspace`, `chat` and `request`. Read them on load. TanStack Router's `navigate({search})` is already in use in `routes/index.tsx:152-196`.

### H3 Home's "Needs you" card shows an internal limitation even when nothing needs you

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod, every visit to My workspace.

**What happens.** The card reads "Tool confirmations are limited to your five newest chats and the open chat. Older chats may still need you." It shows even when nothing is waiting. The wording changes between loads: one load said "...five newest chats and the open chat", a later one "...five newest chats".

**Why.** The client only scans the five newest chats' details for pending confirmations, because Go exposes them only per thread. See C6 in [02-agent-chat.md](02-agent-chat.md). The test pins the text: `home-overview.test.tsx:1212`.

**Fix.** Add a server count of pending confirmations across all chats (one endpoint, or a field on the chat list). Hide the card when the count is 0, and drop the disclaimer.

### H4 Home suggests "Catch me up on Linkedin" while app updates are switched off

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod home and Settings → Your agent.

**What happens.**
- Home shows the suggestion chip "Catch me up on Linkedin" next to "While you were away: Nothing new from your apps."
- Settings → Your agent says "Keeps updates from your apps: Off. New app updates are not kept."

So the empty card is the result of a switch, not of a quiet day, and nothing says so.

**Why.** The chip comes from the first connected tool (`components/home-overview.tsx:904-906`) without checking whether updates are kept. The empty card doesn't look at the switch either.

**Fix.**
- When updates are off, the card says "App updates are off" and links to the setting.
- Only suggest "Catch me up on X" when updates are on and X has something new.

### H5 People with the same first name are indistinguishable in Requests

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod Requests. Items from "Shubham · Oryxa" ask "what has he recently published about the deployment…"

**What happens.** The org has "Shubham Tanwar" (the owner) and "Shubham" (`s.tanwar.connect@gmail.com`, a member). Requests shows the short display name only, so the owner can't tell whether they asked or someone else did. Mixed in are "apollo mail" asks, and that is a member's real display name.

**Fix.**
- Show the full display name, plus an email hint or avatar, whenever two members share a name.
- Mark the viewer's own asks as "You".

### H6 Settings shows raw provider ids and slug names for connected tools

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod Settings → Tool access, and Settings → Your agent.

**What happens.**
- Connected tools are listed as "Linkedin · linkedin_haslet-thrip", "Zoho · zoho_gene-picus", "Github · github_strong-rathe", "Googledrive · googledrive_mere-kissy" and "Gmail · gmail_sleeve-gyrous".
- The agent card says "reads Linkedin, Zoho, Github, Googledrive and Gmail".
- In "Add a tool", Gmail is offered with "Connect" although it is connected.
- "Comp… ai agents" (Composio itself) is offered as a tool.
- Tiles truncate names to "Googl…".

**Why.** The UI shows the Composio connection id and the app slug capitalised, not the app's display name and the connected account (email or handle). Guessed from the shown strings; the component wasn't traced.

**Fix.**
- Show "Google Drive", "GitHub", "LinkedIn" from the catalog's display name, with the account email or handle as the second line.
- Hide connected apps and Composio from "Add a tool".
- Give tiles two lines.

### H7 "Your agent" redaction choices offer a default that duplicates an option

- **Severity:** low
- **Kind:** confusing
- **Seen:** prod Settings → Your agent → "What it hides when it answers".

**What happens.** Each group offers three radio options: "Secrets only", "All personal details" and "Default (secrets only)". For "Answers other people read" the third is "Default (all personal details)". Choosing "Secrets only" and choosing "Default (secrets only)" look identical to the user.

**Fix.** Show two options, and mark which one is the default with a "(default)" tag. If "follow the default when it changes" matters, say so in one line.

### H8 Notifications and email forwarding are "coming soon" pages inside Settings

- **Severity:** low
- **Kind:** confusing
- **Seen:** prod Settings → Notifications ("Choosing where you hear about asks, parleys and mentions is coming soon"), Settings → Email ("Forwarding mail to oryxa so it lands in Requests is coming soon").

**Fix.** Hide unfinished sections, or group them under one "Coming soon" note. Keep only the parley email switch, which works.

### H9 The Library is a raw dump of everything indexed

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod Library: "1,173 files from 13 sources".

**What happens.**
- Almost every row is a GitHub PR title ("jev: Cloudflare's Clef as a drop…", "deploy: stop the perpetual tas…").
- Rows are tagged "Knowledge from connected apps · yesterday" and owned by "Shubham Tan…".
- Names are truncated, and there's no source column, preview or grouping.
- The collections are "All files 1,173", "Project Falcon launches 1" and "Knowledge from co… 1,172".

A user can't find a document or tell why something is there.

**Fix.**
- Group or filter by source (GitHub, Gmail, Drive).
- Show the full title on hover, with the source icon in the row.
- Add a per-source "what gets indexed" switch, so PR noise can be turned off.
- Name the auto collection "From your apps".

### H10 Navigation labels change between the rail, the expanded sidebar and page headings

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod desktop rail and mobile sidebar.

**What happens.**
- The same place goes by three names: the rail says "Library", the expanded sidebar has a "LIBRARY" group holding "Knowledge" and "Tools", and the page heading is "Library".
- Chats is "Chats with your agent" in the rail and "Chats" on the page.
- "Agent teams" in the sidebar is "Multi-agent teams" on the agent overview.
- Home's "Pick up where you left off" row has "New chat", "All conversations" and "Library". "All conversations" appears twice on home.

**Why.** The labels live in several places: `components/workspace-sidebar.tsx`, `workspace-shell.tsx:84-94` (`headings`) and `home-overview.tsx`.

**Fix.** One label per page from one map (the `headings` record), used by the rail, the sidebar and the headings.

### H11 The desktop rail is icons only, and its tooltips are the only labels

- **Severity:** low
- **Kind:** confusing
- **Seen:** prod desktop.

**What happens.** Ten icons with no text. Teamspaces show as a planet avatar ("Q1") and the agent as a face avatar. A new user has to hover each one.

**Fix.** Open the sidebar by default on wide screens, or show short labels under the icons.

### H12 Mobile: the home section header breaks one word per line, and controls overlap

- **Severity:** medium
- **Kind:** broken (layout)
- **Seen:** prod at 375 px.

**What happens.**
- "Pick up where you left off" wraps to six lines ("Pick / up / where / you / left / off") because the three links beside it take the row.
- The two header arrows (→ for the sidebar, ← for the agent panel) look alike and don't say what they open.
- In the mobile sidebar, the "Shubham" DM row's avatar overlaps its name.
- The floating "oryxa." badge at the bottom covers content.

**Fix.**
- Stack the section title above its links below `sm`.
- Use distinct icons with labels for the two panels.
- Fix the avatar row's gap.
- Hide the brand badge on mobile, or reserve space for it.

### H13 Every page polls the requests inbox twice

- **Severity:** low
- **Kind:** slow
- **Seen:** prod network log. `GET …/agent-requests/inbox?limit=50` and `?limit=100` fire together, again and again, on every page.

**Why.** Two components query the same inbox with different limits, so they don't share a cache key (a guess; the two callers weren't traced).

**Fix.** Use one query key with limit 100 that both read, and refetch on a realtime event rather than a timer.

### H14 Many icon buttons have no accessible names

- **Severity:** low
- **Kind:** polish (accessibility)
- **Seen:** prod Chats page accessibility tree. Fifteen `button` elements with no name, plus the chat-row buttons in the chat list.

**Fix.** Add an `aria-label` to every icon-only button. See C15 in [02-agent-chat.md](02-agent-chat.md) for the chat-list side.

## Open questions

- Should Slack be on in production now (H1)? It's the owner's call, and it needs an apply.
- Is "Shubham" (gmail) a test seat that should be renamed, or a real teammate?
