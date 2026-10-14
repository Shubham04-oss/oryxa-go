# Oryxa next — design contract

This is the source of truth for the new TanStack Start frontend in `web-next/`.
It records the decisions made while building the current UI. It is deliberately
prescriptive: new pages should look and behave like one product, not a new
interpretation of the Figma export. Direct user decisions supersede this file;
when one changes, update this file with the implementation.

The older repository-level `docs/design/` plan concerns the previous app. Do
not use it to override this contract. The implementation is primarily in
`src/workspace.css`, `src/components/workspace-*.css`, and
`src/components/auth/auth.css`; those files show how the rules are realized,
not permission to copy every incidental value into a new component.

## 1. Visual foundation

| Role | Token | Dark | Light | Use |
| --- | --- | --- | --- | --- |
| Base surface | `--base` | `#111111` | `#f4f4f4` | Page surround, naked sidebars, chat background, auth inset |
| Main surface | `--canvas` | `#1e1e1e` | `#ffffff` | Central canvas, settings content, auth page |
| Cards and side panels | `--base` | `#111111` | `#f4f4f4` | Compact cards and agent/navigation panels |
| Quiet border | `--border-quiet` | `#353535` | `#e6e6e6` | Interior controls/cards only |
| Primary text | `--text` | `#f2f2f2` | `#111111` | Titles and essential labels |
| Muted text | `--text-muted` | `#999999` | `#737373` | Secondary information |

- Two themes, one set of roles. Dark is the workspace as it has always been;
  light takes oryxa-platform's light roles. The profile menu's Theme item moves
  from System to Light to Dark (System follows the device), and next-themes
  keeps the choice in the browser. Every colour is a token in `src/theme.css`,
  the only place a colour is written: use a role token, or a `tone-<hex>`
  token that already exists, never a literal. A `tone-<hex>` is the colour
  that is `<hex>` in dark, carried to light by the same mapping (surfaces to
  whites and light greys, borders to soft greys, text to dark greys, tints
  keeping their hue).
- Integration logos sit on a white tile in both themes (`--logo-tile`, with a
  quiet edge in light), so every brand keeps its own colour and dark marks
  such as GitHub's or Notion's stay visible; a white mark is inverted on it.
- oryxa's wordmark (gtm/brand/BRAND.md: lowercase oryxa in Inter Bold, tight,
  with the gold-deep `#d98e62` square) sits on a tab of the shell rising from
  the bottom edge of the central canvas, rounded on top and square below as
  the canvas is, at the scale of the sidebar's tabs. The canvas keeps its
  height clear. Gold-deep is the brand's one accent; use it nowhere else.
- Keep the product nearly monochrome and low-noise. Reserve color for
  app logos, planet/user/agent avatars, validation, and genuinely meaningful
  status. Do not introduce a new accent color for decoration.
- Use Plus Jakarta Sans (variable) for UI type; the wordmark alone is Inter. Prefer the existing compact type scale:
  workspace title about 18px, subsection title 13–16px, body 12–14px, and
  metadata 10–12px. Hierarchy comes from weight and space, not giant slogans.
- Use a restrained spacing rhythm based on 8, 12, 16, 24, and 32px. Rounded
  cards and pill controls are welcome; avoid ornamental gradients, broad
  shadows, glass effects, or divider lines between shell regions. Borders may
  group content *inside* a surface. Shadows are for overlays, not page depth,
  with one exception: the selected item in a tiled group is a **raised tile**
  (`--raised` fill, a 1px `--border-quiet` hairline and a 1–3px
  `--shade-66` shadow), never a sunken fill. The same raised tile marks the
  selected pill in a segmented control.
- A chat composer (message to a person, room, or agent) is a pill-shaped
  field — full pill at one line, softening to a large radius once its
  content wraps past one line — with a circular icon send button inside,
  never a rectangular field with a text "Send" button. Share one set of CSS
  across composers instead of copying the pattern per screen.
- Use Lucide for general UI icons. Use actual brand marks for integrations.
  Icons should express an action or entity, never fill empty space.
- Every interactive state must be visible: hover, active/selected, keyboard
  focus, disabled, loading, empty, and error as applicable. Never imply a
  connection or completed action from example data.

## 2. Global shell and navigation

- The base `#111111` surface carries the two sidebars. Their groups (the
  navigation, the conversation list, the profile, the collapsed rails) are
  **tiles**: filled `--canvas` blocks with a 14px radius, the same surface as
  the central canvas, so each group reads at a glance. The expanded agent
  panel's body is one such tile. No shell separator/divider lines; tiles are
  separated by the base showing between them. (Founder, 14 Oct 2026: "use
  the same surfaces … instead of just borders … use tiling".)
- On desktop, the left sidebar is 20% when expanded and a 48px rail when
  collapsed. The right agent panel has a 48px collapsed rail and a wider
  expanded panel. The center takes all remaining width.
- Expanding the right panel collapses the left. Collapsing the right expands
  the center; it must **not** automatically expand the left. A manually
  collapsed left sidebar stays collapsed until the user opens it.
- **One sidebar** (founder, 14 Oct 2026): every page is listed at once, in
  three tiles headed Home, Team and Agents, so nothing is a mode switch away.
  Picking a page opens it and its area; the page you are on is the raised
  tile. The sidebar's top row names the workspace. Collapsed, the rail shows
  the same three tiles as icons. (The Home/Team/Agents mode pills are gone.)
- The right agent panel holds the member's agent and team conversations. Its
  tabs, Agents (the default), DMs, Teams and **At a glance**, use the same
  icon-first pills. At a glance keeps Today's widgets (from your apps,
  teammates, conversations, knowledge, files, recurring work) one tab away on
  every page, as compact tiles in one column, without Today's composer
  (founder, 14 Oct 2026: the sidebar's quick widgets stay). The
  Agents pill carries the primary agent's voxel avatar. When the member
  has more than one agent, the Agents tab's top row is a quick switch between
  them: the one in use is the oval pill with its name, like the pills
  elsewhere, and the rest stay avatar-only. With a single agent the row is not
  shown, and it never shows empty or example slots. The panel is
  location-aware: on a teamspace page the Agents tab also offers that
  teamspace's teammates and their agents, below the member's own (§3, "Side
  conversations"); elsewhere it does not. Collapsed, the panel's rail shows
  one icon per tab, and each opens the panel on that tab. A tab without a
  live service shows a one-line empty state, never example people, rooms or
  messages.
- The sidebar's tiles, in order (Team's recent conversations follow them):

  | Tile | Pages |
  | --- | --- |
  | Home | My workspace, Library, Tools |
  | Team | Teamspaces, People, then up to three visible recent conversations |
  | Agents | Agent overview, Requests, Multi-agent teams, Specialists |

- The shared workspace header is compact (56px at normal desktop/mobile
  widths), with its title centered. It contains the **current context**:
  opening a DM shows the person's name/avatar, not “People”; opening a
  teamspace or agent shows that work's title. Back navigation lives there.
  Do not repeat the page title, a hero slogan, or a generic description at
  the top of the main canvas. No preview badge in the header.
- Desktop panel toggles use left/right panel glyphs so back navigation remains
  visually distinct. On mobile, the two circular edge controls use arrows,
  as previously chosen for that layout. Keep accessible expand/collapse names.

## 3. Focused work and content pages

- **Home — My workspace** (was Today; founder's redesign, 14 Oct 2026).
  Home is the member's desk: what their apps said, who needs them, a place to
  ask, and their own stack. Start with both rails collapsed and preserve the
  member's explicit toggle choices on return. One calm centered column at
  most 760px wide, under a faint band of the sea-of-bottles scene that fades
  out before the content (no text on it). In order: a greeting by the
  member's clock and first name ("Good afternoon, Ada"; no name when only an
  email is known) with one line saying how many things need them, only when
  something does; Teammates (what needs them) as one tile; the composer and
  their tools; then **Pick up where you left off**, a row of cards that
  scrolls sideways (snapping, the next card faded at the edge): From your
  apps, Conversations, Knowledge, Files and Recurring work. Inside a card or
  tile, rows stay plain (a 26px mark, a 13px line clamped to two lines, an
  11px quiet line) with a hover tint. No starter prompts.
  - **From your apps** (top left): at most four briefs, newest first. A brief
    is the first sentence of what Krude told the member in their wake-up
    thread (`origin: "wakeup"`, "Updates from your apps") after a batch of
    app events; its mark is the first app's logo on the white logo tile and
    its quiet line names the apps (as the batch's `1. github · …` lines do)
    and how long ago. A row and **All updates** open that thread. Krude's
    words are the source; Today never rewrites or invents a brief.
  - **Teammates** (top right): who needs the member, one row each. An Inbox
    decision or held answer that waits on them shows the asker's avatar,
    first name and task, a quiet line (Waiting for your OK, Your agent's
    answer is held for you, or Needs your own reply · Slack or Oryxa · ago)
    and one action, Decide, Review or Reply, which opens that exact request
    in Requests, where the request card's choices, race handling and Reply
    yourself live. At most four; the rest are counted. The same column lists
    the member's own agent waiting on a tool confirmation (Open goes to that
    chat) and open access windows, with End access only where the server
    grants revocation. Confirmations are checked in the five newest chats plus
    the open chat, as history and the opening rule select them; Go keeps no
    aggregate pending flag, so state that coverage visibly and never show an
    unqualified all-clear while any read is unchecked. **Nothing needs you
    right now.** appears only when every read is complete and quiet.
  - **The composer** sits centered beneath them (at most 600px). It always
    starts a **new** chat in the right panel through the authenticated
    chat-choice queue, keeping Home as its screen context; drafts and
    recoverable unsent messages are kept. Under it, the member's active app
    connections as quiet pills (logo tile and name) and **+ Connect**, all
    opening Tools.
  - **The stack** follows, under a hairline, two by two: **Conversations**
    (New chat, All, and up to three titled recent primary chats, the running
    one first with the Thinking Orb), **Knowledge** (up to three knowledge
    bases by last update, with document counts), **Files** (what the agent
    saved in the chats already read for confirmations, newest first, each
    opening its chat) and **Recurring work** (the member's own agent
    workflows and their schedules in words). An empty part says what lands
    there in one quiet line.
  - **Finish setup:** one centered quiet line at the bottom with a link per
    unfinished step (Connect Slack for members who may manage it, Add
    knowledge, Turn on background access), from real Slack, knowledge and
    grant reads. A read that fails or is not served leaves its step out
    without a notice. Once setup is complete, the line disappears.
  - Sent requests and shared answers are not on Today: Requests holds Inbox,
    Sent and Activity. Plans and to-do lists have no Oryxa backend yet; Today
    shows none until one exists.
  - All data comes from authenticated APIs; there is no production prototype
    route or fixture data. Scope private caches and retained chat activity to
    the current workspace and member. Hide cached private work and actions
    after an authorization refusal. A member change remounts the shell and a
    workspace/member change remounts the panel; refreshing the same member's
    session preserves their choices and drafts.
  - If session refresh returns 401, immediately replace the authenticated
    workspace with **Your session has ended.** and **Sign in again**, carrying
    the current page as the return destination. Close private sockets, cancel
    private queries and clear their cached data; later polls, focus and
    reconnect reads make no protected HTTP requests in this document. Public
    sign-in remains available. Only successful sign-in resumes private work;
    opening Google sign-in or a failed password attempt does not. A fresh
    OAuth return document verifies its session before opening the workspace.
    Network failures, timeouts and 5xx responses keep the session recoverable
    with bounded backoff. Do not erase scoped chat choices or queued requests;
    drafts held only in the mounted shell are not recovered after a new sign-in.
- A teamspace or extensive agentic task is a focused work view: no left
  sidebar, naked conversation on the base surface, compact title/back header,
  and a separate work panel. Tabs such as Overview, Tasks, Files, and Activity
  sit **above** the work panel. The panel's top area belongs to the selected
  view's heading and state, not the tab strip.
  - **A teamspace** (founder's redesign, 14 Oct 2026) opens under a faint
    band of the harbour scene, its name a title (28px) beside its planet,
    with its status, team and how many people. Its room is the main tile
    (on the base), and its work the side tile, the tabs a segmented control
    whose selected tab is the raised tile.
  - **The teamspaces list** is a title with the counts (active, paused, in
    all) under the same faint band, segmented status filters and a search,
    and the teamspaces as a grid of cards: planet, status, name, purpose,
    team and when it was last updated, how many people. For a member who
    can open one, the last card opens a new teamspace, drawn as the ship's
    lines on deep sea.
- **People, Agents and Settings as tiles** (founder's redesign, 14 Oct
  2026). People is a grid of cards (the person, their presence, what you
  share, Message). On the Agents pages the primary agent, the stats, teams,
  specialists and workflows are filled tiles on the canvas, with no
  outlines. Settings' menu is three groups, You (Account), Workspace
  (Organisation, People & invitations, Teams, Billing) and Connections (Tool
  access, Sign-in apps, Slack, Email), on one tile, the open page the raised
  tile.
- **Chats and DMs as tiles** (founder's redesign, 14 Oct 2026). The open
  chat in the chat list is the raised tile and the others lift on hover; DM
  rows are rounded the same way, with unread as a filled count. The member's
  bubble is a raised tile in a 720px reading column, and a file made in a
  chat is a filled block, not an outline. The log keeps its rules below: the
  member on the right, nobody named.
- **One frame for a file** (founder's redesign, 14 Oct 2026). An open file
  is a bar (back, the file's kind, its name, Edit, open or download) over an
  inset tile holding the file; page and sheet controls are pills, and the
  chosen sheet is the raised tile. A file's kind is a small coloured tile with
  its extension (`file-kind.tsx`, `--file-*` tokens, the same in both
  themes), wherever a file is listed or open.
- **A first-time empty page** (founder's redesign, 14 Oct 2026), such as an
  empty Library or no teamspaces yet, is one tile of the brand's ship lines
  on deep sea (`.workspace-empty-scene`). "Nothing matches", errors and
  warnings stay plain; a destructive confirm never gets a scene.
- **Parleys** (live calls; founder's redesign, 14 Oct 2026). A parley is a
  room's call (live layer B6, `call-provider.tsx`, `call-surfaces.tsx`,
  `call.css`).
  - **Starting and joining:** "Parley" in a teamspace's header starts one.
    While one runs, a strip under the header shows who's in, how long it
    has run and any recording, with the mic and camera set before you join,
    "Listen in" (muted, camera off) and Join.
  - **Once you're in:** the strip says so, with Open and Leave. On any other
    page a mini player follows you (bottom left) with mute, back to the
    parley, and leave.
  - **The full page:** a gallery or speaker layout, live captions, the
    transcript as this page heard it, and a dock (mic, camera, screen,
    record, ask the agent in, leave), plus "End for everyone".
  - **Tiles:** camera tiles stay dark in both themes, as video does
    (`--call-*` tokens). A person keeps their tile colour everywhere. The
    agent is a tile with no camera, marked Listening or Speaking.
  - **Recording:** while it is on, everyone in the call sees who turned it
    on, and when.
- **Library** (was Knowledge bases; founder's redesign, 14 Oct 2026). Each
  collection is a filled tile on the canvas, with no outline, and lifts to
  the raised tile on hover; search, the source list, an open document and an
  answer are tiles too. The code keeps the corpus names; only what a member
  reads says Library.
- Expanding the primary agent's chat uses Agent overview as focused work:
  the same mounted conversation sits on the left, and the overview occupies
  the work panel. The right rail offers only DMs and Teams in this state.
  Files from the current conversation appear in the overview, following the
  conversation the chat is on: a new chat has none, and neither does a chat
  that has not opened yet. Selecting a file, or a newly saved file while the
  chat is in view (the expanded panel's Agents tab, or this view), turns the
  work panel into a clean, compact viewer while the mounted chat remains on
  the left, even if the agent panel was never opened. A file saved while the
  chat is out of view stays a chip in the chat. Markdown uses the same
  rendering as chat, and PDF, DOCX,
  PPTX, XLSX, CSV, text and images use small in-app rendering libraries, with
  only page or sheet navigation when needed. A Word document is drawn in a
  sandboxed frame of its own, so nothing it carries (styles, links, scripts)
  can reach the app around it. A file over 25 MB is not read: the viewer says
  it is too large to preview and leaves it to the download. The viewer has no
  annotation or office-editor chrome. A compact Edit button, offered for DOCX,
  XLSX and PPTX only (the formats the editor saves back as they were), opens
  the full ONLYOFFICE editor, and View returns to the simple viewer.
  Edits save as new conversation-file versions. A missing editor preserves
  native PDF, image and text previews where available. Back returns to the
  overview within that panel. On a phone, the chat and
  work panel stack, and the chat takes the available height while composing.
  The agent receives only the current Oryxa view, selection and explicitly
  marked visible item handles when a message is sent; it never implies a live
  view of other windows or grants access to those items.
- Header-row controls — work tabs, panel tabs, panel toggles — sit on the
  header's own centre row (y=28 in its 56px band), vertically level with the
  back button and title, even when (like the work tabs) they are positioned
  over a region below the header rather than inside the `<header>` itself.
- An agent conversation reads as a quiet log on the base surface: the
  member's messages in raised bubbles on the right; the agent's answers as
  full-width Markdown, labelled only when a sub-agent wrote them; each run of
  tool calls folded into one muted status row with a Thinking Orb for the
  activity state and small app marks when a call identifies an app; the row
  never expands into a list. A group stays Failed only for an unrecovered final
  tool failure. A later successful retry can recover an earlier error only
  when it starts after that failed result, in the same group, with the same
  tool, known author and deeply equal explicit arguments. Missing arguments
  cannot prove a retry; parallel duplicates and unrelated successes do not
  clear failures. Keep recovered errors in a muted “Recovered tool error”
  note (or count), with their bounded reasons available on hover. Genuine
  failures keep the existing red status; waiting, running and interrupted
  states keep their precedence. A request filed by the agent is a small, quiet
  request card showing its status and its published answer as it arrives;
  only the result authorized for the requester appears. An ask that needs its
  owner's own reply says "Waiting for Ada to reply", and an answer the owner
  wrote says "Ada replied" in place of "Answered" and shows their words as
  they wrote them (plain text, never Markdown). Confirmations are small
  cards with Decline and Approve pills (several waiting are answered together);
  files as a thumbnail or a file chip; a failed run as one muted error line,
  and an answer cut short marked "Interrupted". A specialist's answer is its
  work result's summary, with one muted line under it for the rest (needs
  your input or blocked, the tools it used, the files it saved, the hand-offs
  it suggests); while it works, one muted line with a Thinking Orb says so. In
  the agent panel one muted row above it names the conversation, with a
  clear, quiet New chat control; a specialist's focused view carries the
  same in its presence row. Below it, a compact History disclosure lists real
  conversations newest first, each with title, relative time, open and delete.
  A waiting conversation says "Needs approval". Hide empty abandoned starts
  unless currently selected or waiting on approval; an accessible untitled
  conversation uses "Untitled chat". Deletion asks inline, clears the live chat
  object first, then deletes its canonical Go thread. A failure stays visible
  and retryable.
  While the panel stays mounted, keep whatever is open, including through
  collapse/re-expand and tab absence. On remount or reload, the conversation
  the member left **less than five minutes ago** in this tab reopens, including
  a history pick; an **explicit** New chat in that window stays new. Otherwise,
  check the five newest chat conversations and open the newest one waiting on
  the member's tool approval, including an untitled one; otherwise open New chat.
  History still lists every conversation, and retains known approval markers
  from cached details; it never fetches all past transcripts to mark the list. Record member presence and
  actual leave time per workspace, member and agent, never server update time.
  Activity saves at most once every 15 seconds; pagehide, hiding and unmount
  save immediately. A hidden panel/tab never renews that grace. An automatically opened blank
  chat does not suppress a newly waiting approval. The member can type at once;
  the primary chat gives choosing five seconds before it opens New. Keep drafts
  and unsent messages, and never automatically switch a mounted conversation.
  A specialist uses the same history and opening rule in both its panel and
  focused view. Its shared live object refuses a conversation change while it
  is streaming, so another view cannot interrupt the run. A first message, from
  the composer or from a request elsewhere in the app (Home's box), is made a conversation of its own
  and sent into it, in order, whatever the member does meanwhile: the chat ends
  on the latest, no message is dropped, and a request never touches the
  composer's text. One that can't be sent stays at the end of the log, in
  whichever conversation is open, as their bubble with "Not sent." and the
  reason under it, and Retry (into a conversation of its own), Edit (into the
  composer, only while that is empty) and Dismiss: nothing else clears it, and
  it is never put in the composer by itself. A request waits until the panel
  takes it (once the member's agents have loaded), in the order it was made;
  past twenty messages waiting, the next is refused the same way. A
  specialist's live log changes only through its opening rule or an explicit
  history/New chat/delete action. Go keeps previous conversations until deleted.
  Switching agents keeps each one's conversation and unsent message. What the panel holds belongs to the member:
  another member taking over the page inherits none of it.
- People is a DM area, not teammate administration or a combined “People &
  teams” directory. Recent/started conversations belong in Team's left
  sidebar, with at most three visible there. A DM uses the same dynamic header
  principle and keeps the conversation/composer as the main content.
- **Direct messages.** A DM is one conversation (`direct-message.tsx`) in two
  layouts, each its own view of the same live object (REALTIME.md): the main
  canvas, opened from People or Team's recent conversations, under the header
  that names the person; and the agent panel's DMs tab, under a compact row
  with a back arrow to the list.
  - It reads as a quiet log (a centered column in the canvas): the member's
    messages in raised bubbles on the right, the other person's as plain text
    on the left beside their avatar. Each person's messages within one minute
    form a group with one time, and a separator starts each day ("Today",
    "Yesterday", or the date). The log names nobody: the header does.
  - A message waiting for the object is faded; a failed one says "Not sent."
    with Retry. A quiet "Seen" sits under the member's last message the other
    person has read, until they reply. Their typing is one muted pulsing line
    at the end.
  - Online means they have this DM open in a visible tab: a green dot on
    their avatar in the header, and on any list row whose DM a view on the
    page has open. Nobody is ever shown offline.
  - The log follows the end only while the reader is there; otherwise new
    messages raise a "N new messages" pill and are announced. The page before
    loads at the top without moving what the reader is looking at, and the
    start of the conversation says so.
  - Unread is a count on a DMs row, a dot on a recent conversation in Team's
    navigation, and a badge on the DMs tab and its rail icon ("9+" past
    nine); a phone, which has no rail, puts a dot on the panel's edge
    control. A DM counts as read while it is on screen, at its end, in a
    visible tab.
  - States: loading; empty ("Say hello to Priya"); "You can't message this
    person" once they have left; session ended, with Sign in; and "Can't
    reach this conversation" with Try again, while the composer keeps taking
    messages that send once it is back.
  - People lists everyone in the organization but the member, with role and
    shared teamspaces as context, search, and a Message action. Team's
    navigation shows the three latest DMs, and "All" opens the DMs tab, where
    "New message" picks anyone in the organization. The canvas DM is in the
    page's address (`?dm=`), so a refresh returns to it.
  - Opening a DM moves focus to its composer, or to the log on a phone so the
    keyboard stays down; going back returns focus to the person or row it
    came from. Editing, deleting, reactions and attachments do not exist yet,
    so nothing offers them.
- **Side conversations.** Two teammates and both their agents, about one
  teamspace's work, away from its room (REALTIME.md). On a teamspace page the
  Agents tab adds a quiet "In this teamspace" section under the member's own
  agents: each teammate with a seat there, their unread count, and their
  agents as small chips with how each takes a request ("asks first" or
  "automatic"). A teammate opens the conversation in the tab, with a back
  arrow to the list; a chip opens it asking that agent. The DMs tab lists
  side conversations with their teamspace as context and opens them the same
  way, with no teamspace open.
  - It is a DM's compact layout (`side-conversation.tsx`): the bar names the
    teammate and the teamspace, with both people's agents as small voxel
    avatars. People's messages read as a DM's; a question to an agent carries
    a quiet "Asked Leo". An agent's message is its avatar and "Leo · your
    agent" or "Nia · Priya's agent", full-width Markdown, a Thinking Orb working
    line until it has words, and for a request's result one quiet line, "on
    behalf of Priya · approved by Priya" (or "ran without asking"). The
    member's own agent answers live on the member's screen alone: the other
    person sees its working line until the answer has been checked, then
    the words. What the answer guard hid from the other person in it is one
    quiet line under the answer, on the member's view only ("Shared with a
    password hidden"), and an answer it could not check reads "This answer
    couldn't be checked, so it wasn't shared." for both.
  - A request is a small card: who asked which agent what, and one status
    line with an icon (sending; not filed, with Retry; waiting for Priya to
    approve; approved and working; waiting for Priya to review the answer;
    done; declined; failed; expired; not shared). Only the agent's owner
    sees Decline and Approve, with an optional note, named for who and what
    ("Approve Nia's request from Member"). When the request asks to read
    their connected apps or the team's knowledge, their card says so in one
    line ("Nia may read your connected apps and team knowledge.") and adds
    Approve without tools between them. Approve with tools is the filled
    primary choice and allows what was asked for; the card says plainly
    that Approve without tools means the agent can't look anything up.
    A request that ends without a result also leaves a quiet centered line
    in the log, which says why when Oryxa closed it or withheld its answer
    ("Withheld by Oryxa." on the card), and never repeats an agent's
    decision, whose words are fixed ("Pricing declined the request.").
  - An answer the answer guard holds for its owner's review shows them, on
    the card, why in plain words ("Nia's answer includes an email address
    and a phone number. Review it before it's shared.", or "couldn't be
    checked automatically"), the answer, a preview of it masked ("Send
    masked shares:"), and Don't send, Send and Send masked (the primary).
    An answer that could not be checked has no masked version, so neither
    the preview nor Send masked; one that was its agent's failure says so
    above it ("Nia couldn't finish the request, and says:"). Send and Send
    masked wait until the answer has loaded; Don't send never does. A
    refused review says why on the card. The one who asked sees only that
    it waits; a result shared after review says "reviewed by Priya" in its
    quiet line, and one not shared leaves "Priya chose not to share Nia's
    answer."
  - The composer is the shared pill with a smaller pill inside it, before the
    field, naming whom the message goes to: the teammate (the default), "Ask
    my agent", or one of the teammate's agents, picked from a small menu. The
    line under the field says who approves, or that the member's own agent
    is still answering (a second question waits for it). Asking an agent is
    for that one message; the next goes to the teammate again.
  - The member's own agent's confirmations show to them alone, as in the
    specialist chat; the teammate sees only that it waits. Answers and
    approvals that land in place are said aloud.
  - States: loading; empty ("Talk with Priya about Launch room"); "You can't
    open this conversation" once either person is off the team; session
    ended, with Sign in; a paused teamspace, readable with the composer off
    and why; and "Can't reach this conversation" with Try again.
- **Requests** (Agents → Requests) is where a member sees what waits on them
  and what they asked, whether an agent filed it or someone asked in Slack. It
  has three tabs: Inbox, with the requests waiting on the member first and the
  count in its tab; Sent; and Activity, what happened to their Slack asks (asks,
  decisions and where they were made, access given and ended, secrets opened),
  which names people and states and never a question or an answer. A filter
  (All, Slack, Oryxa) narrows the first two. The lists are re-read as the page
  is focused and every few seconds, so a request decided in Slack settles here
  without a reload. **One request at a time** (founder's redesign, 14 Oct
  2026): the lists are compact rows on a tile (who asked or was asked, where,
  what, when, and a tag for what it waits on), and the request open is its
  card beside them, its row the raised tile. The request a row, a link or a
  DM's line names opens (`/?org=…&view=requests&request=…`); without one, the
  first on show that waits opens, and stays open once answered so the member
  sees what their choice did. When nothing waits and none is open, the side
  says "Every ask answered." under a small scene. Below 1100px the card
  replaces the list while one is opened, with "Back to all requests".
  - **The request card** says who asked, the question, where it came from (in
    Slack, or in Oryxa), where it stands and, for the person it is theirs to
    answer, what to do, in one set of words wherever the request came from,
    the same as the buttons of Slack's notice. A request from Slack is answered
    with one of three choices, "Share answer", "Allow for 1 hour" or "Deny",
    and the card says in a line what each lets the agent do ("Allow for 1
    hour": the agent may quote what it reads, and the asker's follow-ups in
    that thread are answered for an hour). Any other request, one an agent
    filed in Oryxa, is answered with "Share answer" or "Deny" and an optional
    note. Sharing the answer allows the tools the request asked for, which the
    card says above the choices ("Krude may read team knowledge."), and it
    offers no approval without them. Each button is named to a screen reader by
    its words and whose request it is ("Share answer to Raya's request").
    Whichever surface decided first, the card settles, and says who won
    ("Approved in Slack by Raya at 14:02.") when the member's own choice lost.
    An answer the guards held shows its owner why in plain words, the answer,
    and Don't send, Send and Send masked; the one who asked sees only that it
    waits. Access the owner gave says until when the asker's follow-ups are
    answered without asking, with an End access action while it lasts and "You
    ended Ada's access." after.
  - **Reply yourself** lets the owner answer in their own words when an ask
    needs their own judgment (an approval, a plan) or when the agent can't find
    the answer. It is on the owner's card wherever they may decide a pending
    request or review a held answer, and nowhere else. Under the choices it is
    a quiet text button ("Reply yourself", named "Reply yourself to Raya's
    request"); it opens an inline field in the composer's style, a pill at one
    line that softens as its text wraps and grows to eight lines (its border is
    the focus mark, as the composer's is), taking focus, with "Send reply" (the
    primary action) and "Cancel". It says what it does
    ("Raya gets your words as you write them. Your agent doesn't run."; for a
    held answer, that they take its place), counts characters only in the last
    500 of its 4,000, and sends with Command or Control and Enter. The agent
    never runs: the request settles to "You replied.", where and when it was
    sent and the words as written (plain text with its line breaks, never
    Markdown); the asker's card says "Ada replied." in place of "Answered." and
    shows the same words, and Requests → Activity says "You replied to Raya's
    request in Oryxa." An empty or too long reply is refused before anything is
    asked. A reply that failed keeps what was written, focus and the field; one
    that lost the race shows who won, as a choice does, and words that can no
    longer be sent (it lost, ran out, or was decided elsewhere) stay readable
    under "Your reply wasn't sent." so they can be copied.
  - **An ask that needs the owner's own judgment** (`answer_by: "owner"`, which
    Jev judges when it is made; absent means the agent answers) leads with the
    reply: nothing runs the agent until the owner chooses to let it. The
    owner's card says "Waiting for your reply." and holds the field open from
    the start (it takes no focus by itself, for a desk may hold several), then
    "Send reply" as the primary action, "Let my agent answer" (for a Slack ask
    the Share answer choice, which the card says in a line; for any other
    request an approval with the tools it asked for) and "Deny" last, with no
    "Allow for 1 hour" and no separate note: the reply is where the owner says
    why. The asker's card and status say "Waiting for Ada to reply." On a phone
    the field takes the width and the buttons stack, in that order.
  - **A secret** is never shown in Slack or in a log. When an ask is for a
    credential, its owner pastes the value into the card, which keeps it in one
    uncontrolled field, sends it once and clears it, and picks how long it waits
    (up to an hour); they can take it back. The member who asked sees "Open
    the secret", which says it is shown once before it is pressed; the value is
    shown once, then gone for good.
- **A request in a DM.** A request between the two people of a DM is one quiet
  centered line in its log, where it happened in time: who asked whose agent,
  and where ("You asked Priya's agent in Slack", "Priya asked your agent"), with
  one word for where it stands (waiting, approved, declined, answered, held,
  failed, not shared, expired). The line's state moves in place, its place in
  the log does not, and a move while the DM is open is announced. It holds no
  question or answer: its "Open" opens the request's card in Requests, and the
  card is read from Go as the member, so the line is never more than a pointer.
  A request made in a teamspace has no line here: the side conversation in that
  teamspace shows it.
- Teamspace management is role-aware. Put account, teammate, organization,
  access, email, and billing administration inside Settings, opened from the
  user menu. Keep the simple Organization tab in that single Settings surface;
  do not split account and organization into separate full-page settings apps.
  A generic Settings entry opens Account on the member's first visit, then
  their last section in this browser, remembered per member and workspace.
  Unavailable or no longer permitted remembered sections fall back to Account.
  Explicit Billing and Slack entry points still open the requested section.
- Knowledge Bases is a Home destination. Creating one asks for a short name,
  with a concrete label example and a visible 2–60 character hint; facts and
  notes belong in its content. After creation, open that base with Add content
  already on Note, retaining the choices for a link or file. The member may
  cancel adding; ordinary visits to an existing base keep the form closed.
  Cancelling creation while its request is out never opens a base that arrives
  later. Preserve the existing permissions and honest errors.
- Tools is also under Home. Connected
  tools form one horizontal, logo-led row with expandable details. The browse
  catalog is categorized and uses two sleek cards per row at normal and small
  widths. Collapsed cards show logo and name only; description and details
  appear on expansion. Do not add a “Connect” button to every collapsed card.
- Use real service state when available. In an offline preview, clearly label
  examples as examples. Do not present a fake successful connection, run,
  subscription, invite, or payment.

## 4. Avatars and imagery

- Use the shadcn Avatar primitive for teamspace and identity avatars. Keep
  image boxes square so circular avatars never become oval in narrow rails.
- Human user identities use DiceBear Lorelei with stable per-user variation;
  agents use DiceBear voxel art. Teamspaces use distinct planet treatments,
  not the same Saturn repeated for every team.
- Use real integration logos where recognizable. Keep them legible against the
  dark UI, often on a small light tile. Do not substitute a generic UI icon
  for a known brand.
- Auth is the one expressive screen (founder's redesign, 14 Oct 2026): a
  painted brand scene (from the site and gtm/brand, in `public/scenes/`) as
  one rounded tile on the left, and the wordmark and form on the base to its
  right. Each page has its scene: sign-in the ships, sign-up the crow's nest,
  check-your-inbox the sea of bottles, password reset the deck, a failed or
  used link the locked sea gate, an invitation and a new workspace the
  harbour. Words on a scene are white (`--on-scene`) over a deep-sea shade at
  its foot; no text sits on the painting anywhere else. On a phone the scene
  is a band across the top that fades into the page. The Google option uses
  the full-color Google mark.
- Setting up a new workspace continues in the same layout (`?setup=`, in
  `components/onboarding.tsx`), with a five-part stepper: Workspace, then
  Krude (background access), Tools (connect apps), Crew (invite by email)
  and Teamspace (the first one). Each step does real work through the
  existing APIs and can be skipped; an app's or the grant's sign-in page
  returns to the step it left.

## 5. Small screens and keyboard

- At 800px and below, both sidebars become overlays rather than consuming
  workspace width. When closed, the content uses almost all of the viewport.
  Show only one small circular control at each top edge for opening them: no
  teardrops, persistent rail, extra mobile control bar, or sidebar glyphs.
- Keep the page/context title centered between those controls. Preserve
  adequate clearance for the title and touch targets at narrow widths.
- The agent panel and left navigation may open over content, but the
  default view should prioritize the center. Do not let both open panels
  compete for the viewport.
- In a focused mobile chat, when the message field is active and the virtual
  keyboard reduces the visual viewport, hide the work/onboarding panel and
  give the visible height to the conversation and composer. Restore the work
  panel after composition. Do not let a checklist push the composer off-screen.
  The agent panel's composer follows the same rule: the panel fits the visible
  height, so its tabs and composer stay on screen.
- Check a representative desktop viewport near 1099×807 and a phone viewport
  near 423×927, including panel-open, panel-closed, and keyboard/composer
  states when relevant. These are regression targets, not fixed layout sizes.

## 6. Rules for adding or changing a screen

1. Start from the shared shell/header and the correct Home/Team/Agents mode;
   do not create a new navigation system for one page.
2. Decide what belongs in the header, sidebar, and main canvas. The header
   names the current context; the sidebar navigates; the canvas does the work.
3. Reuse the palette, type, pills, avatars, button states, and card density
   above. Add a new token or visual treatment only for a real semantic need.
4. Preserve keyboard access, focus visibility, responsive behavior, and honest
   loading/empty/error states. Role restrictions must affect controls as well
   as copy.
5. Visually verify desktop and mobile. For shell changes, exercise both
   sidebar directions and a focused view. For forms, exercise invalid,
   submitting, success, and service-offline states.

If a new requirement conflicts with this contract, resolve it explicitly with
the user decision, implementation, and an update here in the same change.
