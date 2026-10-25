# Duplication: many ways and many pieces of code for one job

Keeping things neat means one place in the UI and one component in the code for each job. Part 1 covers what a member sees, counted on production (Home with the agent panel, 2026-10-25, plus the walkthroughs in 04–06). Part 2 covers the code behind it, in [07a-code-duplication.md](07a-code-duplication.md). IDs N1, N2, ...

## Part 1: in the UI

### N1 Talking to your agent: four message boxes and eight ways in

- **Severity:** high
- **Where:**
  - **Message boxes:** Home's "Ask Krude anything", the Arlo side panel's "Give Arlo a task…", the Chats page composer, and ⌘K's "Ask Arlo about…".
  - **Entry points:** the rail's "Chats with your agent", the rail's "Arlo, your agent", "Expand agent panel", "Open chat with Arlo", Home's chat cards, Home's "New chat", and two "All conversations" links.
- **Why it hurts:** each box opens or continues a different chat by different rules (C1, C3, C16). Members don't know which chat they're writing into.
- **Keep:** one message box per screen. The Chats page is the home of chats; the Arlo panel is the same chat in a drawer. Home's box starts a new chat there and opens it. ⌘K's "Ask Arlo" does the same.

### N2 Starting a new chat has two names

- **Severity:** low
- **Where:** "New chat" (Home, Chats header, At a glance) and "New conversation" (the Chats page button, the accessible name of the composer's new-chat icon).
- **Keep:** "New chat" everywhere.

### N3 Seeing all chats: three links with two names, none clearly the Chats page

- **Severity:** medium
- **Where:** "Chats with your agent" (rail), "All conversations" ×2 (Home's "Needs you" card and Home's "Pick up" row), the Arlo panel's history icon, and At a glance → Conversations.
- **Keep:** one name, "Chats", and every link opens the Chats page.

### N4 "Your agent": three pages describe it

- **Severity:** medium
- **Where:**
  - **Agent overview** (rail "Arlo, your agent"): name, apps, counts, specialists, teams, workflows.
  - **The Chats page's right column:** the same overview again, with Files, Live and Requests tabs.
  - **Settings → Your agent:** name, apps, background access, redaction.
  - The agent card's "Settings" button and the People card's "Settings" lead to the third.
- **Keep:** one agent page with tabs (Overview, Files, Live, Settings). The Chats page links to it rather than embedding it.

### N5 What needs you: four places, counting differently

- **Severity:** high
- **Where:**
  - the Requests page ("Waiting for you 0");
  - the agent overview's "0 waiting for you" button;
  - its Requests tab;
  - Home's "Needs you" card, which counts tool confirmations in five chats only (H3, C6), not requests.
- **Why it hurts:** a member can have something waiting that one place shows and another doesn't. Prose confirmations (F4) appear in none of them.
- **Keep:** one "Needs you" count from one server query, covering requests, tool confirmations and handoffs. It's shown as a badge on Requests, and Home lists the same items.

### N6 Files: four views of overlapping sets

- **Severity:** medium
- **Where:** Library (all indexed files), the agent overview's Files tab (files Arlo made in this chat), At a glance → "Files Arlo made", and the teamspace Files tab.
- **Why it hurts:** a file Arlo made isn't in the Library, and a Library file can't be found in ⌘K (F1).
- **Keep:** the Library as the one file browser, with filters for "Made by Arlo", "In this chat", "In Q1" and "From apps". The tabs become filtered links into it.

### N7 Apps and connections: four places to see or manage them

- **Severity:** medium
- **Where:** Settings → Tool access (connect and disconnect), Library → Tools (sidebar), Library → Sources (GitHub, Gmail, Google Drive), and At a glance → "From your apps · Manage".
- **Keep:** one "Apps" page, with connect, disconnect, what is indexed and what Arlo may do. The other places link to it.

### N8 "Team" means three different things

- **Severity:** high
- **Where:**
  - **Settings → Teams:** groups of people (Engineering).
  - **Teamspaces:** rooms owned by a team (Q1).
  - **Agent teams / Multi-agent teams:** groups of specialist agents.
  - Also Q1's subtitle "Engineering team", and the Notifications page's "Parley emails by team".
- **Keep:** Teams for people. Rename agent teams to something that doesn't say "team" (for example "Crews") or fold them into Specialists. Teamspaces could simply be "Rooms".

### N9 People: two pages

- **Severity:** low
- **Where:** the People page (cards, Message, Ask their agent) and Settings → People (invite, roles, members table). Invite exists in both.
- **Keep:** the People page for everyone. Its admin view adds the role and remove actions, and Settings → People becomes a link.

### N10 Settings opens from four places, and is a modal

- **Severity:** low
- **Where:** the rail's gear, the user menu, the agent card's "Settings" and the People card's "Settings". Each opens the same modal at a different section, and the modal has no URL (H2).
- **Keep:** the gear and the user menu only. Make Settings a page with a URL per section.

### N11 Search: three boxes

- **Severity:** low
- **Where:** ⌘K "Search or jump to", the Chats page's "Search chats", and the Library's "Search 1,173 files". Each searches only its own list, and ⌘K doesn't search the other two (F1).
- **Keep:** the local boxes as filters, with ⌘K searching everything.

### N12 Requests are split by channel, and the agent split one conversation into seven rows

- **Severity:** medium
- **Where:** the Requests inbox has the tabs Inbox, Sent and Activity × All, Slack and Oryxa, and each message in a conversation is its own row (F18).
- **Keep:** group by conversation; make channel a filter chip, not a tab.

## How to use this

Part 1 is mostly decisions about names and which page owns what, with little code. A one-page glossary plus a "who owns what" table in `web-next/DESIGN.md` settles N2, N3, N8 and N10. N1 and N5 need behaviour changes and pair with C1 and C3 in [02-agent-chat.md](02-agent-chat.md).
