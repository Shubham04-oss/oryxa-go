# UX and visual design

How the app looks and reads, beyond whether flows work. Measured on production on 2026-10-25 at 1279 px and 1024 px, in dark and in a light preview (the `dark` class toggled in the page only; no setting was changed). Numbers come from computed styles and from `web-next/src/**/*.css`. IDs U1, U2, ...

## Structure and concepts

### U1 Too many nouns for one product, and several mean the same thing

- **Severity:** high
- **Kind:** confusing
- **Seen:** prod navigation, settings and page copy.

**What happens.** A new member meets all of these on day one: Teamspaces, Parleys, Room, Plan, Requests, Asks, Inbox/Sent/Activity, Chats, Conversations, DMs, Messages, Side conversations, At a glance, Agent overview, Specialists, Agent teams / Multi-agent teams, Workflows, Library, Knowledge, Tools, Tool access, Apps, Connected apps, and Sources.

The pairs that mean the same thing:
- **Chat and conversation:** "New chat" and "New conversation", "All conversations" and "Chats".
- **Request and ask:** the page is "Requests" while the stats say "asks today".
- **Library and Knowledge:** the rail says Library, the sidebar says Knowledge.
- **Tools, Tool access, Apps and Connected apps.**
- **Agent teams and Multi-agent teams.**

A rough count of these words in `web-next/src/components/*.tsx` (it includes identifiers) has "conversation" 686 times and "chat" 721; "request" 1046 and "ask" 213.

**Fix.**
- Pick one word per concept and write a glossary in `docs/design/`: Chat, Request, Library, Apps, Team.
- Rename in the UI strings only, so the API names stay.
- Hide what members can't use yet (Specialists, Agent teams and Workflows are empty for everyone) behind one "Build" entry.

### U2 Two to four panes on every screen, and two message boxes

- **Severity:** high
- **Kind:** confusing
- **Seen:** prod.
  - Home has the main page plus the agent panel.
  - Chats has a list, the chat and the agent overview, plus At a glance when opened: four columns (F15).
  - In Q1 at 1024 px there are two message boxes on one screen: "Message the room, or @mention someone" and "Give Arlo a task, or ask anything". The room's own conversation gets about 180 px, less than its empty-state side column.

**What happens.** The eye has no single place to land, and the member has to decide which box they mean every time.

**Fix.**
- One primary pane per page.
- The Arlo panel is closed by default on pages that have their own composer (teamspace room, DMs). It opens on demand and remembers being closed.
- In a teamspace, the conversation takes the main width and the side cards collapse into a drawer.

### U3 Empty states everywhere, each with its own explanation

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod agent overview (three empty sections with a paragraph each), Q1 (Checklist, Team summary, Purpose and Contributed agents all empty), the Live tab and the Files tab.

**What happens.** A new workspace is mostly grey paragraphs explaining features that aren't set up, and there's no sense of what to do first.

**Fix.**
- Show one "Get started" checklist on Home: connect an app, ask Arlo something, invite a teammate, make a teamspace.
- Hide empty side cards; show a card only when it has content, or as one line ("No purpose yet · Add").

### U4 Home's main area is mostly empty space and blank thumbnails

- **Severity:** medium
- **Kind:** polish
- **Seen:** prod at 1279 px. The bottom half of Home is empty. The four "Pick up where you left off" cards each spend about 60% of their height on a grey box with a generic chat icon, and their titles are cut off ("UX test: what can you …").

**Fix.** Make the cards rows: title on two lines, the last answer's first line, and the time. Use the freed space for the "Get started" checklist (U3) or for real items that need you.

## Type, colour and spacing

### U5 Most text is 10–12 px

- **Severity:** high
- **Kind:** readability
- **Seen:** computed styles on Home and the CSS tally below.

| Element | Size and weight |
|---|---|
| Body copy in cards | 11 px |
| Section titles ("Needs you", "Pick up where you left off") | 12 px semibold, the same grey as body |
| Page title "My workspace" | 13 px |

Across `web-next/src/**/*.css`:

| Size | Uses |
|---|---|
| 12 px | 210 |
| 11 px | 201 |
| 10 px | 62 |
| 12.5 px | 43 |
| 11.5 px | 24 |
| 9–9.5 px | 8 |
| 8 px | 2 |

That's over 500 declarations at 12 px or below. Only the hero ("Good afternoon", 34 px) and chat answers (about 15 px) are comfortable.

**Fix.**
- Body 14 px, secondary 13 px, and nothing under 12 px except timestamps and badges.
- Section titles get a different weight and colour from the body, not just 1 px.

### U6 No type or radius scale: ~25 font sizes and 13 radii across 36 CSS files

- **Severity:** medium
- **Kind:** consistency
- **Seen:** `web-next/src/components/*.css` (36 files).
  - **Font sizes** include 8, 9, 9.5, 10, 10.5, 11, 11.5, 12, 12.5, 12.8, 13, 13.5, 14, 15, 16, 17, 18, 20, 22, 23 and 30 px.
  - **Border radii** include 4, 5, 6, 8, 9, 10, 11, 12, 14, 16, 18 and 999 px.
  - **Text colours:** Home alone uses three near-identical greys: rgb(153,153,153), rgb(168,168,168) and rgb(170,170,170).

**What happens.** Cards next to each other have slightly different corners and text sizes, so the app feels assembled rather than designed. Every new screen adds new values.

**Fix.**
- Define the scale once in `src/theme.css` as CSS variables:
  - type: 12/13/14/16/20/28;
  - radius: 6/10/16/999;
  - text colour: primary, secondary, muted.
- Replace literal values with the variables, file by file, starting with the shell, Home and chat.
- Add a stylelint rule that rejects literal `font-size` and `border-radius` in component CSS.

### U7 Light theme: secondary text fails contrast

- **Severity:** medium
- **Kind:** accessibility
- **Seen:** light preview on Home. Card text and timestamps are rgb(115,115,115) on rgb(244,244,244), which is **4.31:1** at 11 px. WCAG AA needs 4.5:1. Dark passes (6.6:1) but is still small (U5).

**Fix.** Darken light secondary text to about rgb(95,95,95), which is above 5:1 on #f4f4f4. This becomes one token change once U6 is done.

### U8 The background photo sits behind content, so readability varies

- **Severity:** low
- **Kind:** polish
- **Seen:** prod Home (ship in fog), Q1 (harbour). The chips "What needs me today?" and "Catch me up on Linkedin" sit directly on the photo, and the teamspace subtitle ("Engineering team · 3 people and 3 agents") is grey on a busy image.

**Fix.** Keep the photo in a header band only, with a scrim under any text on it. Content below sits on the plain surface.

## Feedback and state

### U9 Loading and connection states are plain text in random places

- **Severity:** medium
- **Kind:** feedback
- **Seen:** prod.
  - "Connecting…" appears under the composer after the first send.
  - "Opening the room…" fills the room for several seconds, and its composer placeholder reads "Opening the room…".
  - "Loading Slack…" appears in Settings.
  - "Couldn't load this conversation's live work." shows in the Live tab.

There are no skeletons, and nothing distinguishes waiting from failing.

**Fix.**
- Skeleton rows for lists and rooms.
- One inline status component (spinner and text) for connecting, and one error component with a Retry button.
- Never show an error for an empty state (F12).

### U10 Status labels contradict what's on screen

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod.
  - Q1's presence chip says "Just you here" next to six avatars.
  - The context chip says "Looking at Agent overview" on the Chats page at 1024 px, where the overview column is hidden.
  - A finished answer reads "Working 17m" (C2).
  - The agent card says "Active" with no hint of what inactive would mean.

**Fix.**
- Presence: "Only you are online · 6 members".
- The context chip follows what is visible.
- Each status badge has a tooltip saying what it means.

### U11 Nothing confirms that an action happened

- **Severity:** low
- **Kind:** feedback
- **Seen:** prod.
  - After cancelling the specialist form, the panel just collapses.
  - After sending a message, the only sign is the message itself.
  - Rename or Delete a chat: not tried, so the result isn't known.

**Fix.** Show a short toast for saves, creates and deletes. `workspace-toasts.tsx` already exists, so use it consistently.

## Navigation feel

### U12 The rail, the expanded sidebar and the Chats page each navigate differently

- **Severity:** medium
- **Kind:** confusing
- **Seen:** prod.
  - **Rail:** ten icons with no text (H11).
  - **Expanded sidebar:** grouped (Teamspaces, Messages, Agents, Library) and holds pages the rail doesn't show: People, Specialists, Agent teams, Workflows, Tools.
  - **Chats page:** replaces the rail with a single back arrow, which goes to Agent overview (F16).
  - **Settings:** a modal over everything with its own left nav.

**Fix.**
- One sidebar model: collapsed shows the same items as expanded, with tooltips.
- The Chats page keeps the sidebar.
- Settings is a page with a URL (H2), not a modal.

### U13 Header controls are bare icons with no labels

- **Severity:** low
- **Kind:** confusing
- **Seen:** prod.
  - The Arlo panel header has history, new chat and collapse icons.
  - The right edge has avatar, grid and chat-bubble icons.
  - Mobile has two arrow buttons that look alike (H12).
  - The meaning of the grid icon ("Open At a glance") is unguessable.

**Fix.** Text labels on the three or four most used controls ("New chat", "History"). Keep icons only where the symbol is universal (search, settings, close).

## Fix order (UX only)

1. **U6 and U5 together:** add tokens, then raise the base size. One pass over the shell, Home and chat changes the feel of everything.
2. **U2:** one primary pane, and the Arlo panel closed on pages with their own composer.
3. **U1:** the glossary and string renames.
4. **U3 and U4:** the "Get started" checklist replaces empty states and blank cards.
5. **U9 and U10:** one loading component, one error component, and honest status labels.
