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
- **Buttons and action rows** (founder, 15 Oct 2026: button order, spacing
  and consistency were poor across Settings, Teamspaces and the rest of the
  app). One convention everywhere:
  - Every button is `ui/button` (`Button`), with its variants (`default`,
    `outline`, `secondary`, `ghost`, `destructive`, `link`) and sizes. Never
    write a new button recipe in CSS; restyle through tokens and variants.
  - One height per context: `size="default"` (32px) for page headers, forms,
    dialogs and confirms; `sm` for trailing row actions; `icon-sm` for inline
    row icons. The buttons in one row are always the same size.
  - **Dialog footers and inline confirm boxes:** the message above, the
    buttons right-aligned with 8px between them: Cancel (`outline`), then
    the confirm on the far right. The confirm is `default`, or `destructive`
    when it deletes, removes, revokes, disconnects or leaves.
  - **Form footers:** status text on the left, the buttons on the right.
    A destructive action never sits next to Save; it goes in a separate
    danger group at the end of the page or dialog.
  - **Settings rows:** one trailing `outline` `sm` action, right-aligned;
    switches and selects sit in the same trailing slot.
  - **Cards and detail headers:** the primary first, start-aligned, 8px gap.
    A destructive action is pushed to the far end (`ml-auto`) or moved into
    the ⋯ menu.
  - **Page header actions:** right-aligned, at most one `default` primary.
  - **Inline row icons:** `ghost` `icon-sm`; a destructive one tints only on
    hover. One icon-button size per context, never three.
  - **Spacing:** 8 between buttons, 8 from a label to its control, 12
    between rows, 24 between sections, and page padding of 24 (32 when the
    page is wide). Keep values on the 8/12/16/24/32 rhythm.
  - **Phones:** an action row stacks full-width, primary on top.
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
- **One sidebar** (founder, 14 Oct 2026; boards Sidebar and Rail, 15 Oct):
  every page is listed at once, so nothing is a mode switch away. It is the
  library's sidebar (`components/ui/sidebar.tsx`) in
  `workspace-sidebar.tsx`, its colour roles pointed at the theme's tokens in
  `workspace-sidebar.css`. Picking a page opens it; the page you are on is
  the raised tile. The top row is the workspace switcher (its initial on a
  white tile, the name, and a menu of the member's workspaces when they have
  more than one; it replaced the header's select) beside the collapse
  toggle; "Search or jump to ⌘K" sits under it once the palette answers it.
  The footer is the member (avatar, name, role), which opens the profile
  menu (theme, Settings, Sign out), and a Settings gear beside it. Every
  count is a real read, left out until it has loaded or when it fails. Its
  rows and Library tiles are the library's menu buttons; its toggles, search
  and gear are `ui/button` (outline `icon` for the toggles, as round edge
  controls; ghost `icon-sm` for the gear), never a CSS button recipe.
- The right agent panel is the member's own agent, where they are (AgentPanel
  board, founder's redesign, 15 Oct 2026). Its **header** is the agent's voxel
  avatar and name over a lock and "Only you · in {place}" (the teamspace, the
  knowledge base, the person, the file or the page), then History and New
  chat for the primary agent's chat (in the header, not over the log, whose
  row only names the conversation), and the collapse toggle. Under it, one
  **segmented control** (the library's tabs; the open view the raised tile):
  the agent by name (the default), **At a glance**, with how many requests
  wait on the member as a count, and DMs as an icon with its unread badge.
  DMs keep that quiet place until the sidebar's Messages section holds them;
  the empty Teams tab is gone. Over the agent's chat, a strip of **chips**
  says only what is real: "Needs you N" (opens Requests) while requests wait
  on the member; "Parley · Join" on a teamspace whose parley runs without
  them; "In the parley · Open" while they are in one. My workspace shows no
  parley chip until the edge can list the organisation's live rooms. The
  composer carries a quiet **"Looking at {item}"** chip: what the agent is
  shown with the message (the page's selection, an open file, or the page).
  The board's quick toggles (reads, suggests, web) are not shown until a
  capability API exists. **At a glance** keeps My workspace's widgets one tab
  away on every page (founder, 14 Oct 2026: the sidebar's quick widgets
  stay), restyled to the AgentPanelGlance board as compact library cards
  (`ui/card`, `home-glance.tsx`) in one column, without a composer:
  - **From your apps:** the connected apps' marks in a row, the latest brief
    (the first sentence Krude wrote in the wake-up thread, opening it) and
    Manage (Tools). Per-app counts wait for an app digest API.
  - **Teammates:** avatars with first names, those the member shares a
    teamspace with first, a tap opening the DM, the green dot where a DM is
    open; All opens People.
  - **Conversations** (New chat), **Knowledge** (name · documents, Library),
    **Files {agent} made** (each with its kind tile, opening its chat) and
    **Recurring work** (the member's own, All N opening Agent overview).
  - What needs the member is not a card here: it is the panel's chip and
    count, and Requests. Each card says in one quiet line what lands there
    when it is empty, and offers Try again when its read failed. When the member
  has more than one agent, the Agents tab's top row is a quick switch between
  them: the one in use is the oval pill with its name, like the pills
  elsewhere, and the rest stay avatar-only. With a single agent the row is not
  shown, and it never shows empty or example slots. The panel is
  location-aware: on a teamspace page the agent's view also offers that
  teamspace's teammates and their agents, below the member's own (§3, "Side
  conversations"); elsewhere it does not. Collapsed, the panel's rail shows
  one icon per view (the agent, At a glance with the needs count, DMs), and
  each opens the panel on that view. A tab without a
  live service shows a one-line empty state, never example people, rooms or
  messages.
- The sidebar's tiles, in order (each heading in quiet capitals, with its
  + where it has one):

  | Tile | Rows |
  | --- | --- |
  | (no heading) | My workspace; Requests, with how many wait on the member as a strong count; Chats with your agent (the Chats page) |
  | Live now (only while the member is in a parley) | Its own tile, edged in green: "Parley · {room}" with how long it has run (opens the parley), then mute, camera and leave (`ui/button` outline `icon-sm`, leave `destructive`). Agents' live sessions ("Krude is browsing …") are left out until there is an org-wide live list (#322). |
  | Teamspaces (the heading opens the list; + leads there to add one) | Each of the member's teamspaces by its planet, opening it, with a green dot while a parley runs there (each room's call read every 20 s, for the first eight only, until the org-wide live list exists); "No teamspaces yet." when there are none |
  | Messages (+ opens People to pick someone) | The three latest DMs, plus any older one with unread messages (six at most), each with its unread count; People, with how many |
  | Agents (+ leads to Specialists) | The primary agent by name and voxel avatar, "Your agent" (Agent overview); Specialists, Agent teams and Workflows, each with how many (Workflows opens Agent overview, where they live) |
  | Library | Two tiles, Knowledge and Tools. The board's Files tile waits for a Files page. |

  Collapsed, the rail (board Rail) is: the workspace's initial, which
  expands the sidebar (a round arrow on a phone); Search once it is
  answered; then tiles of icons: My workspace, Requests (its count a badge)
  and Chats; each teamspace's planet; the DMs with unread messages, each an
  avatar with its count; the agent's avatar and the Library; and at the
  foot Settings over the member's avatar (the profile menu). Every icon's
  name is its tooltip and accessible name. A tile with nothing in it is left
  out.

- **⌘K, Jump to** (board NavCommand; `workspace-command-palette.tsx`, the
  library's command dialog): ⌘K or Ctrl+K anywhere, or the sidebar's
  Search, opens it. "Go to" lists what matches every typed word, at most
  five of each, from the lists the workspace already reads: teamspaces
  (planet, "Teamspace · N people"), requests in the inbox (the question,
  "Request from Raya"), chats with the agent (title, "Chat with Krude ·
  4m") and files (downloaded). Once something is typed, "Do" offers: Start
  a parley in the first matching teamspace (⌘⇧H, not while already in one),
  Ask the agent about it (⌘↵, a new chat in the panel), and Have the agent
  open a page and show you (sent as a chat ask). A footer shows the keys.
  There is no server search: a capped list says so ("Searching your latest
  50 requests."), and without a workspace it says to sign in.
- The shared workspace header is compact (56px at normal desktop/mobile
  widths), with its title centered. It contains the **current context**:
  opening a DM shows the person's name/avatar, not “People”; opening an
  agent shows that work's title, and a teamspace its "Teamspaces / <name>"
  crumbs. Back navigation lives there.
  Do not repeat the page title, a hero slogan, or a generic description at
  the top of the main canvas. No preview badge in the header.
- Desktop panel toggles use left/right panel glyphs so back navigation remains
  visually distinct. On mobile, the two circular edge controls use arrows,
  as previously chosen for that layout. Keep accessible expand/collapse names.

## 3. Focused work and content pages

- **Home — My workspace** (founder's redesign, centred, 15 Oct 2026; board
  WorkspaceV3). Home is the member's desk: one centred column on an axis,
  symmetric and decluttered, under a faint band of the sea-of-bottles scene
  that fades out before the content (no text on it). Start with both rails
  collapsed (Krude's panel too) and preserve the member's explicit toggle
  choices on return. The column is at most 720px wide. Top to bottom:
  - **The live parley pill** (`home-live-pill.tsx`), only while a parley is
    live: the member's own parley ("You're in a parley in X", Open), else the
    first of their open teamspaces with a call running ("Parley in X · 18
    min", up to three faces, Join opening that teamspace). Each teamspace the
    member is on is read once a minute with `readCall`, at most eight, with
    no log socket, until an org-wide live list exists (#322). With nothing
    live there is no pill.
  - **The greeting** by the member's clock and first name ("Good afternoon,
    Ada"; no name when only an email is known), centred, with one line: how
    many things need them, then how many asks Krude decided for them today
    (inbox decisions with `decided_by.kind === "agent"`), and nothing when
    neither is true.
  - **The composer**, a large pill (60px, the page's only shadow) that asks
    Krude. It always starts a **new** chat in the right panel through the
    authenticated chat-choice queue, keeping Home as its screen context;
    drafts and recoverable unsent messages are kept.
  - **Suggestions** under it, as outline chips (the founder's starter chips
    replace the old "no starter prompts" rule): "What needs me today?", and
    "Catch me up on <app>" for the member's first active app connection.
    Each asks at once in a new chat. A suggestion is drawn only from what the
    member really has; never a made-up teammate, file or channel.
  - **Two equal tiles** side by side (one column on a phone): **Needs you**
    and **While you were away**, both filled `--base` tiles.
  - **Pick up where you left off** (`home-pickup.tsx`): four equal cards, the
    member's most recent work across the files Krude saved in the chats
    already read for confirmations, their titled chats and their Library
    collections, newest first, a running chat leading. A card is a
    thumbnail (the file's kind badge, a chat or Library mark, or the Thinking
    Orb while it works) over a name and a quiet line. New chat, All
    conversations and Library sit in its header. Fewer cards leave the row
    short; none says what lands there in one line.
  Inside a tile, rows stay plain (a 26px mark, a 13px line clamped to two
  lines, an 11px quiet line) with a hover tint. Every action is `ui/button`:
  row actions `outline` `sm`, header actions `ghost` `sm`, inline links
  `link` `sm`, suggestion chips `outline` `sm` (§1, Buttons and action rows).
  - **While you were away** (At a glance: **From your apps**): at most three
    briefs on the page (four at a glance), newest first. A brief
    is the first sentence of what Krude told the member in their wake-up
    thread (`origin: "wakeup"`, "Updates from your apps") after a batch of
    app events; its mark is the first app's logo on the white logo tile and
    its quiet line names the apps (as the batch's `1. github · …` lines do)
    and how long ago. A row and **All updates** open that thread. Krude's
    words are the source; Today never rewrites or invents a brief. It is
    never a bare title: with no app connected it says "Connect an app to see
    what happened while you were away." with Connect your tools; with apps
    and no briefs, "Nothing new from your apps."; and when the chats or the
    wake-up thread can't be read, "Updates from your apps can't be loaded
    right now." Every read's loading and error line on Home is its own
    sentence naming the area ("Requests can't be loaded right now."), never
    a resource name dropped into a template.
  - A Pick up card is one button named for what it opens ("Open Product
    docs"), its quiet line the description; the thumbnail is decoration.
  - **Needs you** (was Teammates), with a count beside the heading on the
    page: who needs the member, one row each. An Inbox
    decision or held answer that waits on them shows the asker's avatar,
    first name and task, a quiet line (Waiting for your OK, Your agent's
    answer is held for you, or Needs your own reply · Slack or Oryxa · ago)
    and one action, Decide, Review or Reply, which opens that exact request
    in Requests, where the request card's choices, race handling and Reply
    yourself live. At most four; the rest are counted. The same tile
    lists the member's own agent waiting on a tool confirmation (Open goes to
    that chat) and open access windows, with End access only where the server
    grants revocation. Confirmations are checked in the five newest chats plus
    the open chat, as history and the opening rule select them; Go keeps no
    aggregate pending flag, so state that coverage visibly and never show an
    unqualified all-clear while any read is unchecked. **Nothing needs you
    right now.** appears only when every read is complete and quiet.
  - Hidden until the API exists (#322): the Needs-you parley row and
    "Krude suggested an edit" rows, per-app counts and previews, "N editing"
    presence, and document thumbnails.
  - **The first run** (board OnbFirstRunV3, `home-first-run.tsx`): when
    setup finishes, the page opens with `?welcome=1` on the same axis.
    Krude's line ("Krude is setting up with you · only you see this page")
    sits over the greeting, whose line names how many briefs stood out, or
    invites a first ask. The pair becomes **What Krude found** (the briefs)
    and **Getting started**: five steps ticked from real reads (the agent
    grant, an active app connection, another member or a pending
    invitation, a teamspace, a first chat), each untaken one with a Start
    action opening its place. A step whose read fails or is not served is
    left out and the count says "n of m". Needs you appears under the pair
    only when something waits, and Pick up only once there is work to pick
    up. Hide (Done once all are ticked) drops `welcome` and returns the
    page to its everyday form. "What Krude read during setup" (channel and
    source counts) waits on an API (#322).
  - **At a glance** keeps the full widget stack: From your apps and Needs
    you, then **Conversations** (New chat, All, and up to three titled
    recent primary chats, the running one first with the Thinking Orb),
    **Knowledge** (up to three knowledge bases by last update, with document
    counts), **Files** (what the agent saved in the chats already read for
    confirmations, newest first, each opening its chat) and **Recurring
    work** (the member's own agent workflows and their schedules in words),
    then the Finish setup line. An empty part says what lands there in one
    quiet line.
  - **Finish setup** (at a glance): one quiet line at the bottom with a link per
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
- An extensive agentic task is a focused work view: no left sidebar, naked
  conversation on the base surface, compact title/back header, and a
  separate work panel. Tabs sit **above** the work panel. The panel's top
  area belongs to the selected view's heading and state, not the tab strip.
  - **A teamspace** (TeamspaceHome board, 15 Oct 2026) keeps the sidebar,
    which marks Teamspaces. The page's header is the crumbs "Teamspaces /
    <name>"; the first goes back to the list. On the canvas, under a faint
    band of the harbour scene: the hero (planet, the name as a 30px title,
    "<Team> team · N people and M agents", "Paused · " first when paused),
    and on its right the faces of the people with a seat, the Parley pill
    and, only for whoever manages the team, an Invite pill. Invite opens a
    pop-up sheet listing the workspace's people not yet on the team, each
    with Add (adding them to the team is what opens the teamspace to them;
    someone new to the workspace is invited from Settings). Under the hero,
    a segmented control of Room, Plan, Files (with its count) and Parleys,
    the selected tab the raised tile, then a quieter Activity tab set a
    little apart (the ledger has no place on the board; owner to confirm).
  - **Room:** the room is the main tile, with a side column beside it: a
    Live card only while a sandbox or browser is running in the room
    (RoomLive), a **Files N** card with the newest four team files (a kind
    tag from the extension, the name, "added …"; its heading opens the
    Files tab, and it shows only once there are files to list), the team
    summary, then the purpose, the contributed agents and the people. The
    board has a Plan card and a Last parley card too, left out until
    their APIs exist. Purpose, agents and People aren't on the board; they
    are kept on purpose, below the board's cards, as Overview's content
    with nowhere else to go (owner to confirm). The
    room stays mounted behind the other tabs, so its draft, scroll and
    socket carry over. **Files** is the team's files, full width. **Plan**
    and **Parleys** have no API yet, so each is one honest line on the
    empty scene ("A shared plan is coming", "Past parleys will be listed
    here"). No launch date in the hero until the teamspace carries one.
  - **The member's agent beside a teamspace** (TeamspaceHome, Teamspace3 and
    TeamspaceGlance boards, 15 Oct 2026): opening a teamspace on a desktop
    opens the agent panel (§2) beside it, so the sidebar folds to its
    rail; a phone keeps the room in front. The panel is a private session
    that reads the teamspace: "Only you · in {teamspace}", "Parley · Join"
    while its parley runs without the member, "Needs you N", "Looking at
    {teamspace}" on the composer, and the teamspace's teammates for side
    conversations under the agent's chat. At a glance carries the needs
    count. The quick toggles (reads room and files, suggests, web) wait for a
    per-teamspace capability API.
  - **The teamspaces list** (TeamspacesNew, 15 Oct 2026) opens under the
    same faint band on a compact row, with no in-page title (the shell's
    header already says Teamspaces, §2): a subtitle that says how many are
    active and, when one is, that a parley is live there ("4 active, and a
    parley is live in EU launch."), and New teamspace. Below it, a search
    (`/` focuses it, unless the
    member is typing) and segmented status filters that carry their counts
    (All 5, Active 4, Paused 1). The teamspaces are a grid of cards: planet,
    name, "<Team> team · N people" (or "· paused"), purpose, a live row
    with the "Parley · 18 min" chip while a call runs, and a footer with
    the faces of the people who have a seat (four, then +N). Clicking a
    card opens the teamspace; its More menu has Open, Manage details, and,
    for whoever manages it, Pause or Resume and Delete. Pause and Delete
    open the details on their usual confirm; Resume needs none. A paused
    card is dimmed, still opens from anywhere on it, and says its room is
    closed until a team admin resumes it. The parley chip reads each open teamspace's call once a minute
    (the member must be on the team), under the room's own call key.
    Unread counts, who's editing, custom facts, the last activity line and
    who paused it stay off the card until the list carries them.
  - For a member who can open one, the last card opens a new teamspace,
    drawn as the ship's lines on deep sea, with a New teamspace button.
    Both it and the header's button open the create form as a pop-up
    sheet (`ui/dialog`), the lines faint at its top.
- **People, Agents and Settings as tiles** (founder's redesign, 14 Oct
  2026). People is a grid of cards (the person, their presence, what you
  share, Message), or a plain list of the same people, one toggle away
  (founder, 15 Oct 2026: people may be a plain list). The page names the
  workspace and counts its people and the agents the member can reach, with
  Invite (Settings → People, for owners and admins), a search that also
  finds an agent's name ("/" moves to it), and a segmented teamspace filter
  over the teamspaces the member shares with someone. The member's own card
  leads, with their agent and Settings. Each card shows, under the person,
  their agent where a shared teamspace seats one ("Nia · Priya's agent"),
  then Message and **Ask Nia**: a small dialog files an agent request in the
  member's own session, with no teamspace, so the owner always approves it
  first; it says so, and that the answer arrives in Requests, with "Open in
  Requests" once sent (`ask-agent-dialog.tsx`). Someone who shares no
  teamspace with the member shows no agent and no Ask until the members API
  names everyone's agent (#322). On the Agents pages the primary agent,
  teams, specialists and workflows are filled tiles on the canvas, with no
  outlines. **Agent overview** (AgentsNew board, 15 Oct 2026) reads top to
  bottom: a hero (the agent's avatar and name, "Your agent · reads Slack,
  Notion and Drive · suggests, you decide" from the member's active app
  connections, its status, inline numbers Go can count — waiting for you,
  specialists, teams, each leading to its page — and Settings, which opens
  Tool access); then **Multi-agent teams**, **Specialists** and
  **Workflows**, each under a head with its count and an outline New team,
  New specialist or New workflow that opens its form in place. A team is a
  row with its chain (the coordinator, an arrow, its specialists), purpose,
  "led by Leo, 2 specialists" and status. A specialist is a card with its
  kind and status, what it does, and a ⋯ menu (Open, Pause or Resume,
  Edit); a paused one says "Paused · updated <date>", never who paused it.
  A workflow row keeps its schedule and last run with Run now (Start, for
  one that runs only when started) beside it. "Answered today", files made
  and usage per specialist are not shown until Go counts them (#322).
- **Settings, a pop-up** (founder, 15 Oct 2026: keep it a pop-up; what was
  wrong was button order, spacing and consistency between pages). Settings is
  the library's Dialog over the workspace: its head reads "Settings ·
  {organisation}" with a close button, and its body holds two tiles on the
  base, its own menu and the open page. The menu is three groups, You (Account, Your
  agent, Notifications), Workspace (Organisation, People, Teams, Billing) and
  Connections (Tool access, Sign-in apps for admins, Slack, Email), the open
  page the raised tile. A page is its title and one line under it, then
  groups: a quiet uppercase label over one card on the canvas, and in the
  card rows, a title and what it means on the left and the control on the
  right, with a hairline between rows. Pages are built only from the settings
  kit (`settings-kit.tsx`: group, card, row, switch, select, chips, status,
  value, button, actions, note, message), which wraps the shadcn base-nova
  library; inside Settings and its pop-ups the library's colour variables are
  mapped to theme roles (`workspace-settings.css`). Every page has the same
  padding (24) and 24 between groups. Buttons follow "Buttons and action
  rows" (§1): a row's one trailing action is `outline` `sm`; a form footer or
  confirm (`SettingsActions`) has its status on the left and Cancel then the
  confirm on the right, `destructive` when it removes; a destructive action
  never sits beside Save but in the page's last group, Danger zone, whose row
  title is red.
  - **Account:** the profile row (no Edit until a profile route exists),
    sign-in (password reset, sign out) and the theme as three swatches
    (Light, Dark, System) on the library's radio group.
  - **Your agent:** the primary agent with the apps it reads and Rename
    (an inline form, Cancel then Save); "Works while you're away" (the agent
    grant: on goes through its sign-in, off asks first) and "Keeps updates
    from your apps" as switches; what it hides when it answers (what the
    level hides for people who ask, as chips, then the two levels); and,
    while access is on, Danger zone → Revoke background access.
  - **Notifications:** where each notice goes and quiet hours are one honest
    line until Go serves them; parley emails by team are switches.
  - **Organisation:** the workspace row (its first letter as the mark,
    "Created … by …", Rename), the address (the slug, read-only) and its
    email domains (Edit), each edit an inline form; Danger zone holds Delete
    (owners, typing the name) or Leave. No data region, retention or export
    until Go has them, and no promise of a grace period.
  - **People:** Invite is one line, several addresses separated by commas
    and a role, then Send invite at the right (one invitation per address);
    Members is the library's table (person, role menu for owners, a quiet ✕
    that asks before removing), its label counting seats when the plan has
    a limit; On their way lists invitations with who sent them, when they
    expire, and Cancel.
  - **Teams:** the library's table, one row a team (its name and how many
    people, its lead, the faces of who is in it, its teamspaces as chips),
    from the directory in one read; a row opens the team's own page (name,
    members with an Admin/Member menu, Add a member, Danger zone → Delete
    team). New team is a small dialog (name, inside which team), Cancel then
    Create.
  - **Billing:** the plan is one card (Current plan, its name, when it
    renews, seats used against the limit as a bar, and Change plan, which
    opens the billing portal, or Choose a plan, which lists the plans to
    check out), the harbour scene fading in at its right. Payment is one row
    pointing to the portal. No price, card, contact or invoices until the
    billing routes carry them.
  - **Tool access:** the member's own connected tools (logo tile, "Only
    you" and what each reaches, such as "14 channels", and a ⋯ menu whose
    Disconnect asks first), Add a tool (search and catalog tiles with
    Connect, and All tools), and for owners and admins who in the
    organisation has connected what. No "Who can use" choice until Go has
    one.
  - **Slack:** Your Slack account, then the workspace row (Connected, and
    Disconnect on it, which asks first), then high-priority channels as
    switches, each turned on after a confirm. No who-installed, member
    counts or "How asks look" until Go serves them.
  - **Email:** forwarding asks by email is one honest line; Gmail's real
    connection; masking points to Your agent; what oryxa always sends.
  - **Sign-in apps** keeps its forms, now on the kit's surfaces: the switch
    is the library's, Save sits alone at the right, and removing an app is
    its own danger row with a destructive confirm.
- **Chats** (ChatsNew; `chats-page.tsx`): the member's chats with their
  agent as a page. It is the agent session (a focused view, the header
  "Chats · Only you see these") with a 280px list tile before the chat:
  Search chats (by title, in the page), Today and Earlier groups, each row
  its title and how long ago, "Needs approval" where that is known (the open
  chat, or details the chat already read; the list never reads every
  transcript to mark itself), the open chat the raised tile, and New chat.
  Picking a row moves the one primary chat to it through the same queue Home
  uses, so drafts and unsent messages behave as anywhere else; while a run
  works the other rows wait, and say so. The chat's own History disclosure is
  hidden here. Narrower than 1180px the work column gives way to the list
  until a file or live pane opens, and then the list gives way. The chat's
  bar says who sees it ("Only you · Krude") and has a ⋯ menu with Rename
  and Delete. Rename is a small dialog (the name field, 1 to 80 characters
  once trimmed, checked as you type; Cancel, then Save): the title changes
  in the bar and the list at once and goes back if Go refuses, and while a
  run answers it says "Krude is answering — try again in a moment". The
  Requests thread has no Rename. Delete is a confirm (Cancel, then the
  destructive Delete). A specialist's chat bar has the same ⋯ with Rename.
  A row whose chat kept files adds "3 files · " and the latest file's kind
  badge (`FileKindBadge`, sized to the meta line) from Go's count; with no
  count, or 0, the row says nothing about files.
- **Chats and DMs as tiles** (founder's redesign, 14 Oct 2026). The open
  chat in the chat list is the raised tile and the others lift on hover; DM
  rows are rounded the same way, with unread as a filled count. The member's
  bubble is a raised tile in a 720px reading column, and a file made in a
  chat is a filled block, not an outline. The log keeps its rules below: the
  member on the right, nobody named.
- **One frame for a file** (founder's redesign, 14–15 Oct 2026;
  `file-viewer-frame.tsx`). Every open file, an agent's or the Library's, is
  one bar over an inset tile holding the file. The bar has back, the crumbs
  of where the file sits (Library / collection), the file's kind, its name
  and a line of facts (owner · origin · size), the viewer's own tools, then
  Edit where it applies, Download, Open original (the file at its app) and a
  ⋯ More menu, each shown only when it does something. Its buttons are ghost
  `icon-sm`. The frame is only the file's column: where a chat shares the
  screen, the chat is on the left and the file on the right (AgentSession),
  so opening a Library file folds the agent panel away. Show in parley is
  not offered until a call can be handed a file. A viewer puts its own
  controls in the bar (`ViewerTools`). A PDF (`pdf-viewer.tsx`) is its pages
  one under another on white paper (`--paper`), with a rail of page
  thumbnails (the page in view is the raised tile; hidden on a phone). Its
  tools are the page counter "3 / 12" with previous and next, zoom − / % / +
  in steps with Fit (to the width, the default), and Find, which marks every
  match on the page (`--viewer-highlight`, the current one stronger) and
  steps through them with Enter. A page is drawn only near the view and
  gives its pixels back when far from it. A spreadsheet or CSV
  (`sheet-viewer.tsx`) has a formula bar (the selected cell's reference, then
  its formula from exceljs or its value), lettered columns and numbered rows
  that stick as it scrolls, a cell selected by click or the arrow keys, and
  its sheets as tabs under the grid (`ui/tabs`, the chosen one the raised
  tile) with "N sheets" in the bar. It is read-only. A presentation
  (`slides-viewer.tsx`) is the slide on a stage, its speaker notes under it
  when it has any, and a filmstrip of every slide, with previous and next
  and "n / N" in the bar; the arrow keys move between slides. An image
  (`media-viewer.tsx`) fits the frame by default and zooms in steps from
  there (− / % / + and Fit). Video and audio use the browser's own player,
  with speed and, for video, full screen in the bar; a file the browser
  can't play says so in one line and leaves it to Download, never a broken
  player. A code file (`code-viewer.tsx`; JS/TS, Python, Go, Rust, Java,
  Ruby, JSON, YAML, SQL, shell, HTML/XML/SVG and CSS) has numbered lines,
  light highlighting from a pinned highlight.js core loaded only when code
  opens (its colours are the `--code-*` tokens), and Wrap and Copy in the
  bar. HTML and SVG show as their source and never run. A cited passage
  (a citation in an answer, or a passage search matched) opens in its file
  when the Library can read the file's text: an uploaded file, or a connected
  app's document. The file opens on the passage, its text in a reading column
  with the passage marked (`--viewer-highlight`) and scrolled to, and a
  Cited passage / Formatted (or Whole file) switch in the bar
  (`passage-view.tsx`). Offsets are rag's bytes, cut as bytes before
  decoding. A link's citation stays a link to the page, and a PDF or Office
  citation never happens, since rag indexes text only. A Library file opens
  over the whole Library page: an uploaded file from files, a connected
  app's document as the text knowledge keeps. A note or a link says
  plainly that its text isn't kept for reading, since rag gives none back. A file's kind is a small coloured tile with
  its extension (`file-kind.tsx`, `--file-*` tokens, the same in both
  themes), wherever a file is listed or open.
- **A first-time empty page** (founder's redesign, 14 Oct 2026), such as an
  empty Library or no teamspaces yet, is one tile of the brand's ship lines
  on deep sea (`.workspace-empty-scene`). "Nothing matches", errors and
  warnings stay plain; a destructive confirm never gets a scene.
- **Pop-ups** (founder's redesign, 15 Oct 2026; `popup.tsx`, `popup.css`).
  Every pop-up is the library's dialog (`ui/dialog`, Base UI's focus trap,
  Escape and focus return), themed: a raised 420px tile (480px wide
  variant), 18px corners, a close X at the top right, then an icon on a
  raised tile, a green kicker, a 17px title, the text, optional lines on a
  quiet tile (with a Decision tag), and a foot that follows the dialog
  footer rule: a quiet note on the left, then `ui/button` buttons at the
  default size, right-aligned 8px apart, Cancel (`outline`) before the
  confirm. The close X is a ghost `icon-sm` button (on the deep-sea shade
  over a scene). Spacing inside is 16 between blocks and 24 padding. On a
  phone the actions stack full width, the confirm on top.
  - **The brand rule:** a pop-up that opens something (a welcome, an invite,
    a parley's notes, a plan) opens with a slight band of its own brand
    scene, 112px, dimmed and fading into the surface; the icon and title
    ride up over its foot. Sea of bottles for what a parley left, the
    harbour for people coming aboard, the crow's nest for the welcome (the
    one place the wordmark may sit on the painting), the deck for plans.
  - **Welcome** (`welcome-dialog.tsx`): the crow's nest, taller, with the
    wordmark on it; "Welcome aboard, Ada"; three first steps (connect your
    apps, invite your team, ask your agent) ticked from real reads (app
    connections, the members list, the member's conversations); "N of 3
    done", Skip for now, and the next step as the primary. It is for
    someone new (joined the workspace in the last 14 days), shows once per
    member and workspace (remembered in this browser), and only while a
    step is left.
  - **Toasts** (the library's sonner Toaster, themed in `ui/sonner.tsx`;
    `workspace-toasts.tsx`): raised tiles in the theme's roles. Plain for a
    confirmation ("Link copied", when a link is copied); with the person's
    avatar on a thin strip of sea and an action when someone does something
    the member can join ("Raya started a parley", Join, while the member
    has that room's page open).
  - **Hover card** (`teamspace-hover-card.tsx`, the library's hover card):
    a teamspace from the sidebar, as a small pop-up with a 72px band of the
    harbour: its name and description, "5 people · 3 agents" (its members;
    its own agent and the specialists brought to it) with faces, and
    "Parley live" from the room's own call view, read when the card opens.
  - **Plain:** warnings and destructive confirms (`PopupConfirm`, the
    library's alert dialog) never get a scene: an icon, what will happen,
    Cancel (`outline`) and the action (`destructive` when it deletes,
    removes, revokes, disconnects or leaves).
- **Parleys** (live calls; founder's redesign, 14 Oct 2026). A parley is a
  room's call (live layer B6, `call-provider.tsx`, `call-surfaces.tsx`,
  `call.css`).
  - **Starting and joining:** "Parley" in a teamspace's header (an outline
    button) starts one. While one runs it stays, with the live dot, and
    joins it (or opens it once you're in). A strip under the header shows
    the faces with their names, who's in, how long it has run and any
    recording, with the mic and camera set before you join, "Listen in"
    (muted, camera off) and Join. Who is talking and what is shared (with a
    thumbnail) join the strip once the edge's call view carries them.
  - **Once you're in:** the strip says so, with Open and Leave. On any other
    page a mini player follows you (bottom left) with mute, back to the
    parley, and leave. Over a shared screen it keeps the speaker's cam
    inset at the bottom right (the sharer's when nobody speaks).
  - **The stage** (`call-stage.tsx`; HuddleShare): the parley in the
    workspace's main column, with both sidebars still beside it. "Open" on
    the strip, the header pill and the mini player's back open it. The page
    under it stays mounted and inert (so the room's conversation can move
    into the Room pane and back); going elsewhere in the workspace, Escape
    or the header's mini-player button leave it for the mini player. The
    header has the crumbs (the teamspace, "Parley · 18:42"), the faces and
    a Full page pill. A shared screen is the focused tile under a label
    bar ("Raya's screen", Fill), with a column of named small cams beside
    it (a row on a phone); Fill gives the screen the whole stage. With no
    share, a grid of cams. While the room's agents run a browser
    (HuddleBrowse), each running one is a tile beside the share: its live
    page (the browser pane, compact, with take over and hand back when the
    agent asks), Working or Needs a hand, and its last steps from the
    room's log, the newest marked "now"; the people move to a filmstrip on
    top. Opening the Room beside a share or a browser makes it **Mix**
    (HuddleMix): the crumb names what's open ("Parley, room, Raya's screen
    and Browser"); on the left a People tile (small named cams, then mute,
    camera, share, full page and Leave) over the room's conversation (moved
    in, with a close X); on the right the screen and browsers stacked, with
    the library's resizable grips between the columns and the tiles.
    Closing the room goes back. Otherwise the dock is the full page's. Exit full page
    comes back to the stage; where no stage is hosted, Open goes straight
    to the full page and out of it is the mini player.
  - **The full page:** "Parley" with the teamspace's description under it
    (or the recording line), a Gallery / Speaker / Shared layout (Shared,
    while someone shares: the screen fills the stage and the people are a
    row of small tiles), live captions, and the transcript as this page
    heard it.
  - **The dock** (`CallDock`, one component for every surface, built from
    the library's toggles and buttons): on the left the live time and "5
    people, Krude taking notes"; in the middle mic, camera, screen, record,
    ask the agent in, devices and Leave; on the right captions, the Room
    (with a count of what others said there since its pane was last open,
    from the room page's messages; no count while no page reports them),
    the transcript, "End for everyone", and Full page / Exit full page.
    The dock is its own button context (#332): round `ui/toggle` icon
    controls of one size, Leave a `destructive` button at the end of the
    controls, "End for everyone" `destructive` too.
  - **Raise hand and React** sit after share in the dock and in Mix's
    People tile, while the call can carry them (RealtimeKit broadcast
    messages; a refused broadcast hides both for that call). A raised hand
    is a "Raised hand" flag on the tile (the hand alone on a small tile)
    and the pressed button; a reaction (👍 👏 😂 🎉 ❤️, from the library's
    menu) rises from its sender's tile for four seconds.
    Devices opens a small panel over the stage to choose the microphone,
    camera and speaker, and follows devices plugged in or out. Beside the
    call is the Transcript or the **Room**: the room's own conversation,
    moved in from the room's page while the pane is open (there's only ever
    one, so the room is never open twice). From any other page the Room
    pane says to open the room's page.
  - **Tiles:** camera tiles stay dark in both themes, as video does
    (`--call-*` tokens). A person keeps their tile colour everywhere. Every
    tile names its person with their mic, small ones too; only the faces in
    the join strip go without. Flags sit top right: Sharing, Camera off (big
    tiles only). The agent is a tile with no camera, flagged Taking notes
    (Notes when small) or Speaking.
  - **Recording:** while it is on, everyone in the call sees who turned it
    on, and when.
  - **Parley ended:** when the call the member is in ends (someone ended it,
    or they ended it for everyone), the pop-up with the sea of bottles: the
    kicker "Parley ended · 35 min · 5 people", what was kept (the recording
    and transcript, or the transcript) and Done. Just leaving shows nothing.
    The agent's notes, the save-recording switch, "Edit first" and "Post
    notes to the room" join it once there's an API for the notes.
  - **In a DM:** the same strip, or a "Parley" button when none runs, sits
    above the conversation in its reading column. The call is the DM's own
    room on the edge. A DM whose ids the edge can't name offers no parley.
  - **In a side conversation:** the same, under its bar in the panel; the
    strip wraps there and leaves out the faces. The call is the side
    conversation's own room (the teamspace, then the two members).
- **A specialist's access** (I3, I9). Its page has an Access tab with two
  cards:
  - **Model:** Lite or Main, as two tiles, with the chosen one raised.
  - **Apps:** "Every app you connect" (the default, which reads everything
    and changes nothing) or "Only the apps you choose". Every connected app
    gets a row with a checkbox for whether the specialist may use it, and a
    "May change things" switch, which is off unless it may use the app.
    Saving replaces the whole grant. An app the grant names that is no
    longer connected stays listed, marked so, until it is taken off.
- **What the organization agent made of a team's posts** (I7).
  - **Team summary:** a teamspace's room has this card in its side column
    (summary, decisions, risks, open questions) and the team's proposed
    follow-ups.
    Anyone who may not read it (403) sees no card at all.
  - **Agent overview** lists the follow-ups from the member's teamspaces,
    grouped by teamspace.
- **Live work** (live layer A8): the sandboxes and browsers agents run in a
  room, as far as a member may see them (the edge hides another member's
  private ones).
  - **Where:** beside the primary agent's chat, the session's Live tab
    shows the conversation's own as tiles, and one opens in the work panel
    like a file. A teamspace shows a Live card beside its room, only while a
    sandbox or browser is running there (stopped ones don't count), with
    its tiles, the open one, and the run log.
  - **A sandbox** has Files (folders and a reader) and Preview (a port it
    serves, in a frame without its own origin). A member gets a Terminal
    (xterm, loaded when opened) only in their own private sandbox (owner's
    decision, 15 Oct 2026): a shell runs anything, so a shared or a run's
    sandbox is to watch. The terminal stays dark in both themes
    (`--terminal-*`).
  - **A browser** shows the agent's browser live, only while it runs (a
    stopped one isn't woken by looking). When the agent asks for a hand,
    its tile says "Needs a hand" and the pane says why; the page reads
    that from the room's log, since every request to a browser keeps it
    awake and billed. "Take over"
    gives the member the controls; "Hand back" returns them with an
    optional note, and "I couldn't do it" says so.
  - **The run log** shows who did what (you, your agent, a teammate),
    newest first, with how long it took.
- **Settings → Slack is for every member** (with #199's linking): it starts
  with "Your Slack account", where a member links theirs through Sign in with
  Slack (so what they ask and decide in Slack counts as them), sees since
  when, and unlinks it after a confirm. The organisation's connection and
  high-priority channels below stay for owners and admins; anyone else is
  told who manages them. Where the server runs no Slack app, the section
  isn't shown.
- **Library** (was Knowledge bases; founder's redesign, 14–15 Oct 2026). One
  two-pane page (`knowledge-bases.tsx`, `library-rail.tsx`,
  `library-file-table.tsx`, `library.css`). The header reads "Library" over
  "N files from M sources" (the corpora's `document_count`s and the member's
  knowledge sources). The left rail lists All files, then each collection (a
  rag corpus) with a coloured dot (`--collection-1…6`, picked from its id;
  rag stores no colour) and its count; the open one is the raised tile, and a
  quiet "+" at Collections opens New collection in a dialog. Under them,
  Sources lists each connected app the member's agents read (their knowledge
  sources, one entry per provider, its mark on the logo tile), with a quiet
  "Paused" or "Reconnect" when none of its sources runs; there is no count,
  since no route counts a source's files. Choosing one shows the files the
  connected-apps collection holds from that app (`metadata.provider`), as
  far as they are loaded, with Manage in Tools. The right pane
  has a pill search that filters the loaded titles and the one primary, Add
  files (in All files it first asks which collection, among those the member
  may add to). A file table (kind badge, name with its tags and sub-line,
  owner from the members list, updated) lists a collection's files, or in
  All files the first page of every collection newest first, saying so when
  a collection holds more. A collection's header names it with its facts,
  then Add content (primary), Manage in Tools, and a ⋯ menu with Delete
  collection; a row's ⋯ has Remove. Both confirm in an alert dialog. Ask
  stays a composer under the header. Pinned, out-of-date notes, Cited and
  Out-of-date tags and a Grid view are not shown: there is no data for them.
  On a phone the rail is a sideways row of pills and the table drops kind
  and owner. The code keeps the corpus names; only what a member reads says
  Library or collection.
- **The agent session: the chat is always on the left, the work always on
  the right.** Expanding the primary agent's chat, or opening a file from it,
  opens Agent overview as one two-column session (`AgentSession`): the
  conversation column (30%, 300px at least) and the work panel beside it,
  which holds the overview, a file viewer or a live pane. The conversation is
  the panel's own mounted chat, moved into the column by a host element
  (`useAgentChatHost`; the host is portalled into, never remounted) and
  moved back when the session closes, before the column leaves the page, so
  its draft, scroll, socket and keyboard focus carry over both ways. The
  column is named for the agent ("Conversation with Leo"). A plain click on
  a file chip opens it beside the chat; a Cmd, Ctrl or Shift click still
  opens it in a new tab, and Download saves it without opening anything. Nothing is laid over the page. On a phone the
  two stack, the chat above; with the keyboard up the chat takes the screen
  below the header. The Viewer boards' right-docked agent panel is
  superseded by this. The right rail offers only At a glance and DMs in this state.
  With nothing open, the work panel is a segmented control (the chosen tab
  the raised tile) over four tabs (`agent-session-work.tsx`): **Overview**
  (the agent's own page: the agent, stats, teams and specialists, follow-ups
  and workflows, nothing of one conversation), **Files** (this
  conversation's files, latest version first, "Version 2 of 3"), **Live**
  (its sandboxes and browsers) and **Requests** (the requests its agent
  filed in it, as request cards; nothing from other conversations). Each
  tab counts what it holds, and an empty one says what lands there in one
  line. Opening a file or a live child swaps in its viewer, and Back returns
  to the same tab. The tabs follow the conversation the chat is on: a new
  chat has none of these, and neither does a chat that has not opened yet.
  Selecting a file in the Files tab or a file chip in
  the chat (in the panel or in the session), or a newly saved file while the
  chat is in view (the expanded panel's Agents tab, or this view), turns the
  work panel into a clean, compact viewer while the chat stays on the left,
  even if the agent panel was never opened. A chip never opens a new browser
  tab there; a small Download control beside it is the second way. A file
  saved while the chat is out of view stays a chip in the chat. Markdown uses the same
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
- **The agent conversation is a working session, not a messenger thread**
  (founder, 15 Oct 2026; `agent-run.tsx`, `agent-run.css`). People chats keep
  bubbles; the primary agent's chat does not. Each thing the member asks is a
  **task line**: left-aligned, no bubble, 14px semibold, with "You · 09:41"
  above it (a batch from their apps keeps its "From your apps" notice there).
  Under it, the work that answered it is one **run card**, a filled tile (the
  base on the panel's canvas tile, the canvas in the session's column):
  - **Header:** a status (Working or Waiting for you with a Thinking Orb;
    Done, Interrupted or Failed with an icon) as an outline badge, how long
    it took, and tokens used where the run said; Stop (ghost, sm) while it
    runs. Duration comes from the run's own start and end markers, which only
    runs streamed into the chat's transcript carry; otherwise from the items'
    times; with neither, nothing is shown. Go's history keeps no run markers
    or usage (#322), so older runs show no tokens.
  - **Steps:** one ghost toggle ("3 steps · 1 failed", with app marks) that
    opens a list, one line per call: app mark, what it did, the key argument
    (query, file, URL or channel, from the call's own arguments), and its
    state; a failed step shows its error. Open while the run works or waits,
    folded once it is over, until the member chooses. "Opened" lists the web
    addresses calls were given. Go sends no result text or sources, so a
    step says only Done or Failed, and there is no Sources view yet (#322).
  - **Approvals** sit inside the card as the confirmation cards below.
  - **Outputs:** a file is a card row (its kind badge, name, "Version 2 ·
    Made in this chat", Open as outline sm, Download as ghost icon-sm; links,
    so a click that asks for a new tab gets one); an image also shows its
    preview. Requests keep their request card.
  - **The answer** is the Markdown at the end of the card; a failed run's
    words close it.
  The composer is a task bar ("Give Leo a task, or ask anything") with a quiet
  "Looking at …" chip above the field: what the page's screen context names
  (its selection, or the page), which goes with the message.
  **Specialist and team chats are working sessions too.** A specialist's
  card takes what its work result says: "Needs your input" or "Blocked" in
  place of Done, the files it saved as output rows by name (Go serves no
  specialist files yet, so they say they can't be opened here), and the
  hand-offs it suggests as rows ("Suggests Planner: …"; suggestions only,
  nothing ran); the line under its answer keeps only the tools it used. A
  multi-agent team's posts are tasks, and the agents' answers after one are
  its run card, each named for who wrote it; while the team works the card
  says so and the composer says it can take a minute and a half.
- No screen uses the older log layout any more (`AgentTimeline`'s `log`
  variant, kept for now), which read an agent conversation as a quiet log
  on the base surface: the
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
  cards whose actions follow the action-row convention: `ui/button`, both
  `sm`, right-aligned 8px apart, Decline (outline; destructive once chosen)
  then Approve (default) on the far right (several waiting are answered
  together with Send answers);
  files as a thumbnail or a file chip (opening in the session's viewer, with
  Download beside it); a failed run as one muted error line,
  and an answer cut short marked "Interrupted". A specialist's answer is its
  work result's summary, with one muted line under it for the rest (needs
  your input or blocked, the tools it used, the files it saved, the hand-offs
  it suggests); while it works, one muted line with a Thinking Orb says so. In
  the agent panel one muted row above it names the conversation, and the
  panel's header holds New chat and History (§2); Agent overview and a
  specialist's view keep New chat on that row. A compact History list shows real
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
  teams” directory. Recent conversations belong in the sidebar's Messages
  tile (above). A DM uses the same dynamic header
  principle and keeps the conversation/composer as the main content.
- **Direct messages.** A DM is one conversation (`direct-message.tsx`) in two
  layouts, each its own view of the same live object (REALTIME.md): the main
  canvas, opened from People or the sidebar's Messages, under the header
  that names the person; and the agent panel's DMs tab, under a compact row
  with a back arrow to the list.
  - **Messages** (founder's redesign, 15 Oct 2026; `messages-page.tsx`). In
    the canvas, the open DM sits between the member's DM list and a card
    about the person, the three columns answering the canvas's width (a
    container query) rather than the window's: a narrow canvas drops the
    card first, then the list, leaving the DM alone as before. The list, a
    filled tile, is "Messages · N unread" over "Find a person" and the DMs
    (side conversations stay in the DMs tab, under their teamspace), the open
    one the raised tile; a search also lists anyone else who matches, to
    start a conversation, and New message moves to it. The person card, a
    filled tile in the canvas and never in the agent panel's place, says
    their name, role and "Online" only while the page knows it, Parley (the
    DM's own room; while the card shows, the DM keeps only a running
    parley's strip), their agent where a shared teamspace seats one, and the
    teamspaces you share, each agent with Ask as on People. Local time, a profile and shared files wait for
    the API (#322), so nothing stands in for them. The header still names
    the person.
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
  - Unread is a count on a DMs row and on a DM in the sidebar's Messages,
    and a badge on the DMs tab and its rail icon ("9+" past
    nine); a phone, which has no rail, puts a dot on the panel's edge
    control. A DM counts as read while it is on screen, at its end, in a
    visible tab.
  - States: loading; empty ("Say hello to Priya"); "You can't message this
    person" once they have left; session ended, with Sign in; and "Can't
    reach this conversation" with Try again, while the composer keeps taking
    messages that send once it is back.
  - People lists everyone in the organization, the member's own card first,
    with role and shared teamspaces as context, search, and a Message
    action. The sidebar's Messages shows the latest DMs; the agent panel's
    DMs tab lists them all, where "New message" picks anyone in the
    organization. The canvas DM is in the
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
  - **To the RequestsNew / RequestsClear boards** (15 Oct 2026). The header
    reads "Requests" over "N waiting for you" or "All clear", and the agent
    panel collapses as Requests opens, so the open request takes the width
    (the member may still open it). The list tile has a search (a `ui/button`
    that opens a field, filtering what the lists hold, Escape to close), the
    inbox's first heading "Waiting for you N" even at none, then "Earlier".
    A row's time is the clock today and how long ago before; its tag says
    what it waits on ("Expires in 1 h", "Wants your reply", "Answer held for
    you") or who answered ("Answered by Krude", "Answered by you"). The card's
    head carries the expiry as a quiet pill. The all-clear says "Nothing is
    waiting for you. Krude will flag the next one." and today's numbers from
    the inbox (asks today, by Krude, by you, average wait). Not shown until
    Go serves them: the draft "If you share, Krude will say" with sources and
    masked count, the Slack thread and channel name, a new/unread dot, and
    the "Has an email address" warning.
  - **The request card** says who asked, the question, where it came from (in
    Slack, or in Oryxa), where it stands and, for the person it is theirs to
    answer, what to do, in one set of words wherever the request came from,
    the same as the buttons of Slack's notice. A request from Slack is answered
    with one of four choices in a 2×2 grid, "Share answer" (the primary),
    "Allow for 1 hour", "Reply yourself" and "Deny" (destructive), each a
    `ui/button` with its key, and the card says in a line what each lets the
    agent do ("Allow for 1 hour": the agent may quote what it reads, and the
    asker's follow-ups in that thread are answered for an hour). On the
    Requests page S, A, R and D pick the open request's choices from
    anywhere but a text field. Any other request, one an agent filed in
    Oryxa, is answered with "Share answer", "Reply yourself" or "Deny" (S, R,
    D) and an optional note. Sharing the answer allows the tools the request asked for, which the
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
    request or review a held answer, and nowhere else. It is the grid's R choice
    (under a held answer, a quiet text button), named "Reply yourself to Raya's
    request"; it opens an inline field in the composer's style, a pill at one
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
  sidebar's Settings entry (the user menu, and the rail's Settings button
  where the rail has one). Keep the simple Organization tab in that single Settings surface;
  do not split account and organization into separate full-page settings apps.
  A generic Settings entry opens Account on the member's first visit, then
  their last section in this browser, remembered per member and workspace.
  Unavailable or no longer permitted remembered sections fall back to Account.
  Explicit Billing and Slack entry points still open the requested section.
- The Library is a Home destination. Creating a collection asks for a short
  name, with a concrete label example and a visible 2–60 character hint;
  facts and notes belong in its content. After creation, open that collection
  with its Add dialog already on Note, retaining the choices for a link or
  file. The member may cancel adding; ordinary visits to an existing
  collection keep the dialog closed. Cancelling creation while its request is
  out never opens a collection that arrives later. Preserve the existing
  permissions and honest errors.
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
- **Setting up a new workspace** (onboarding v3, founder's redesign of 15
  Oct 2026; boards OnbV3s1–s5 and Onb3Model) uses the sign-in layout through
  `OnboardingFrame` (`components/onboarding-frame.tsx`). Naming the
  workspace is step 1 (routes/index `NewWorkspace`); `?setup=` carries steps
  2–5 in `components/onboarding.tsx`: Krude (background access), Tools
  (connect apps), Crew (invite by email) and Teamspace (the first one).
  - **The scene** on the left holds a miniature of the member's workspace
    (`WorkspaceModel`): navigation, the page and Krude's panel, as three
    panes. One part fills in per step, from what is really set up: the
    workspace's name as it is typed, Krude's panel, connected apps,
    invited addresses (pending invitations and this session's), and the
    teamspace's name as it is typed. Parts not yet set up stay as quiet
    ghost lines (`ui/skeleton`, no pulse); never example people or apps.
    The scene's caption sits at its foot; on narrow screens the miniature
    is hidden.
  - **The form** on the right sits under "Step N of 5 · Name" and a
    five-part bar (an ordered list; the current part is `aria-current`).
    Each step opens with an eyebrow, a question (30px) and one line.
  - **Navigation** follows §1's buttons: Back (`ghost`) on the left, Skip
    for now (`ghost`) and Continue (`default`, with its ↵ hint) on the
    right. Enter anywhere outside a field, button or link continues; in a
    field it submits that field's form. Steps 1 and 2 have no Back (the
    workspace exists once named; step 1 offers Sign out there). Skip for
    now shows only while the step has nothing done; the last step's
    primary is "Open my workspace", which lands on My workspace's first
    run (§3).
  - Each step does real work through the existing APIs, and an app's or
    the grant's sign-in page returns to the step it left. Board elements
    with no API stay out: the workspace address and domain join, how far
    Krude acts on its own and what it calls the member (#322), per-person
    roles on invites, and teamspace starters.

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
