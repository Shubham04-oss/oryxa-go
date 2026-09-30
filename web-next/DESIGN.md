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

**Archived on 2026-09-30.** The owner kept only the agent ask-and-consent loop
(Home's desk, the primary agent's chat, Requests, Knowledge bases, Tools,
Settings and sign-in). Multi-agent teams, Workflows, Specialists and the whole
Team mode (Teamspaces and rooms, side conversations, direct messages and the
People directory) are gone from the UI, along with the right panel's DMs and
Teams tabs. Their screens and their rules were removed from this contract with
them; everything is recoverable from the git tag
`archive/pre-simplify-2026-09-30`, and §7 says what stays in the Worker. Do not
rebuild any of it without an explicit decision that supersedes this note.

## 1. Visual foundation

| Role | Color | Use |
| --- | --- | --- |
| Base surface | `#111111` | Page surround, naked sidebars, chat background, auth inset |
| Main surface | `#1e1e1e` | Central canvas, settings content, auth page |
| Cards and side panels | `#111111` | Compact cards and agent/navigation panels |
| Quiet border | around `#353535` | Interior controls/cards only |
| Primary text | around `#f2f2f2` | Titles and essential labels |
| Muted text | around `#999999` | Secondary information |

- Keep the product dark, nearly monochrome, and low-noise. Reserve color for
  app logos, planet/user/agent avatars, validation, and genuinely meaningful
  status. Do not introduce a new accent color for decoration.
- Use Geist Variable for UI type. Prefer the existing compact type scale:
  workspace title about 18px, subsection title 13–16px, body 12–14px, and
  metadata 10–12px. Hierarchy comes from weight and space, not giant slogans.
- Use a restrained spacing rhythm based on 8, 12, 16, 24, and 32px. Rounded
  cards and pill controls are welcome; avoid ornamental gradients, broad
  shadows, glass effects, or divider lines between shell regions. Borders may
  group content *inside* a surface. Shadows are for overlays, not page depth.
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

- The base `#111111` surface carries the two sidebars without separate cards.
  The `#1e1e1e` canvas sits between them. No shell separator/divider lines.
- On desktop, the left sidebar is 20% when expanded and a 48px rail when
  collapsed. The right agent panel has a 48px collapsed rail and a wider
  expanded panel. The center takes all remaining width.
- Expanding the right panel collapses the left. Collapsing the right expands
  the center; it must **not** automatically expand the left. A manually
  collapsed left sidebar stays collapsed until the user opens it.
- Home and Agents are the two primary icon-first pill controls. The active
  one is slightly enlarged and more prominent, not a giant tab.
- The right agent panel holds the member's primary agent: its chat and the
  history of its conversations, and nothing else. It has no tabs. Expanded, its
  top row is the agent's oval pill (the agent's voxel avatar and its name, the
  name only once it has loaded) beside the collapse toggle. Collapsed, its rail
  is one button, the agent's avatar, which opens the panel; focus moves to the
  toggle. Without a workspace it shows the chat disabled with a one-line
  reason, never example messages. In the focused Agent overview (§3) the panel
  is that view's conversation column and has no toggle and no rail.
- Sidebar content follows the primary mode:

  | Mode | Left navigation |
  | --- | --- |
  | Home | Today, Knowledge bases, Tools |
  | Agents | Agent overview, Requests |

- The shared workspace header is compact (56px at normal desktop/mobile
  widths), with its title centered. It contains the **current context**:
  opening a knowledge base shows its name. Back navigation lives there.
  Do not repeat the page title, a hero slogan, or a generic description at
  the top of the main canvas. No preview badge in the header.
- Desktop panel toggles use left/right panel glyphs so back navigation remains
  visually distinct. On mobile, the two circular edge controls use arrows,
  as previously chosen for that layout. Keep accessible expand/collapse names.

## 3. Focused work and content pages

- **Home — Today.** Home is the member's desk for the consent loop. Start
  with both rails collapsed and preserve the member's explicit toggle choices
  on return. Use a centered column at most 760px wide on the `#1e1e1e` canvas,
  compact `#111111` cards, the section 1 palette and Geist type. On a phone,
  cards use the available width and controls wrap without horizontal scrolling;
  navigation overlays close after a selection. No greeting or starter prompts.
  - The first card holds the composer and up to three recent primary-agent
    conversations, newest first. The composer always starts a **new** chat in
    the right panel through the existing authenticated chat-choice queue,
    keeping Home as its screen context. Each recent opens its exact thread;
    All opens primary conversation history in the right panel. Keep drafts
    and recoverable unsent messages. Do not infer running work from timestamps;
    use the selected chat's scoped live activity and the existing Thinking Orb.
    Untitled selected work stays reachable as Current conversation; a run
    without a thread ID is a noninteractive Starting conversation row.
  - Next, **Needs you** holds actual Inbox decisions, held answers awaiting
    the member's review, and their primary-agent tool confirmations. Reuse the
    request cards' authorized actions and race handling: Slack asks offer
    Share answer, Allow for 1 hour and Deny inline, and any other request
    Share answer and Deny; held answers offer the existing review choices. A
    tool-confirmation row opens its exact chat so the member can review the
    actual confirmation cards. Count conversations once, even when they have
    several pending tool confirmations. Requests awaiting automatic agent
    approval do not wait on the member. Primary-chat
    confirmations are checked in the five newest chats plus the open chat,
    using the same newest-five selection as history and the opening rule. Go
    derives confirmations from events and maintains no aggregate pending flag,
    so this bounded read does not cover older unopened conversations. State
    the coverage visibly, and never show an unqualified all-clear when any
    conversations or reads remain unchecked.
    Full chat detail checks poll every 30 seconds and refresh on mount and
    window focus; lightweight lists may poll every 10 seconds. The open chat's
    live waiting state updates Home immediately through its scoped activity.
  - **Open access** follows Needs you. List live access windows from the
    server's active, member-scoped lease list, with expiry and whose agent may
    read whose information. End access is offered only when the server grants
    this member revocation authority; it uses the existing lease revocation
    path. Do not reconstruct live windows from a bounded activity feed or
    promise they ended before the server confirms it.
  - **Your requests & shared answers** follows Open access. Show what the
    member asked others, its current status and the answer actually released
    to them, plus their agent's sharing activity. Read Inbox, Sent and Activity;
    Activity is metadata, never an authorization to reveal a held answer or
    private tool output. Open the exact request for its full authorized detail.
  - **Finish setup** comes last and contains only incomplete steps: Slack
    not connected, no knowledge content, and background access off. Use real
    Slack status, knowledge and account grant reads. Offer the action the
    member may perform; a member who cannot manage Slack gets an honest
    administrator hint. Once setup is complete, this card disappears.
  - Hide every empty section. The quiet state is the composer card, the line
    “Nothing needs you right now.”, then **Set up your workspace** if setup
    has unfinished steps. Real recent conversations remain inside the composer
    card. Loading, failed, refused, paginated or incomplete decision reads
    must not imply all clear; state the actual checked coverage and offer retry
    where appropriate. Full bounded responses are lower bounds, not totals.
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
- **Agents → Agent overview** shows the primary agent (its name and status,
  with Resume while it is paused), one "Requests waiting" tile that leads to
  Requests, and the files of the current conversation (below). Nothing else
  belongs to Agents mode: it offers no specialists, teams or workflows.
- The Agent overview is the one focused work view: no left sidebar, naked
  conversation on the base surface, compact title/back header, and a separate
  work panel.
- Expanding the primary agent's chat uses Agent overview as focused work:
  the same mounted conversation sits on the left, and the overview occupies
  the work panel. The panel offers nothing else in this state: no toggle and
  no rail (§2). Files from the current conversation appear in the overview, following the
  conversation the chat is on: a new chat has none, and neither does a chat
  that has not opened yet. Selecting a file, or a newly saved file while the
  chat is in view (the expanded panel, or this view), turns the
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
- Header-row controls, the panel's agent row and toggle, sit on the header's
  own centre row (y=28 in its 56px band), vertically level with the back
  button and title.
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
  only the result authorized for the requester appears. Confirmations are small
  cards with Decline and Approve pills (several waiting are answered together);
  files as a thumbnail or a file chip; a failed run as one muted error line,
  and an answer cut short marked "Interrupted". In the agent panel one muted
  row above it names the conversation, with a clear, quiet New chat control.
  Below it, a compact History disclosure lists real
  conversations newest first, each with title, relative time, open and delete.
  A waiting conversation says "Needs approval". Hide empty abandoned starts
  unless currently selected or waiting on approval; an accessible untitled
  conversation uses "Untitled chat". Deletion asks inline, clears the live chat
  object first, then deletes its canonical Go thread. A failure stays visible
  and retryable.
  While the panel stays mounted, keep whatever is open, including through
  collapse/re-expand. On remount or reload, the conversation
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
  A first message, from
  the composer or from a request elsewhere in the app (Home's box), is made a conversation of its own
  and sent into it, in order, whatever the member does meanwhile: the chat ends
  on the latest, no message is dropped, and a request never touches the
  composer's text. One that can't be sent stays at the end of the log, in
  whichever conversation is open, as their bubble with "Not sent." and the
  reason under it, and Retry (into a conversation of its own), Edit (into the
  composer, only while that is empty) and Dismiss: nothing else clears it, and
  it is never put in the composer by itself. A request waits until the panel
  takes it (once the member's agents have loaded), in the order it was made;
  past twenty messages waiting, the next is refused the same way. Go keeps
  previous conversations until deleted. What the panel holds belongs to the
  member: another member taking over the page inherits none of it.
- **Requests** (Agents → Requests) is where a member sees what waits on them
  and what they asked, whether an agent filed it or someone asked in Slack. It
  has three tabs: Inbox, with the requests waiting on the member first and the
  count in its tab; Sent; and Activity, what happened to their Slack asks (asks,
  decisions and where they were made, access given and ended, secrets opened),
  which names people and states and never a question or an answer. A filter
  (All, Slack, Oryxa) narrows the first two. The lists are re-read as the page
  is focused and every few seconds, so a request decided in Slack settles here
  without a reload. A request a link or a note names opens first, above
  the lists (`/?org=…&view=requests&request=…`).
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
  - **A secret** is never shown in Slack or in a log. When an ask is for a
    credential, its owner pastes the value into the card, which keeps it in one
    uncontrolled field, sends it once and clears it, and picks how long it waits
    (up to an hour); they can take it back. The member who asked sees "Open
    the secret", which says it is shown once before it is pressed; the value is
    shown once, then gone for good.
- Administration is role-aware. Put account, teammate, organization,
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

- Use the shadcn Avatar primitive for identity avatars. Keep image boxes
  square so circular avatars never become oval in narrow rails.
- Human user identities use DiceBear Lorelei with stable per-user variation;
  agents use DiceBear voxel art.
- Use real integration logos where recognizable. Keep them legible against the
  dark UI, often on a small light tile. Do not substitute a generic UI icon
  for a known brand.
- Auth is the one expressive screen: full `#1e1e1e` page, compact `#111111`
  form inset toward the right, and a small detailed live pixel cyclone behind
  it. It has no decorative card shadow. The Google option uses the full-color
  Google mark. Keep the motion subtle and honor reduced-motion preferences.

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
  height, so its agent row and composer stay on screen.
- Check a representative desktop viewport near 1099×807 and a phone viewport
  near 423×927, including panel-open, panel-closed, and keyboard/composer
  states when relevant. These are regression targets, not fixed layout sizes.

## 6. Rules for adding or changing a screen

1. Start from the shared shell/header and the correct Home/Agents mode;
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

## 7. Archived features (2026-09-30)

Removed from the UI, and from this contract, on 2026-09-30, when the owner
kept only the agent ask-and-consent loop. Everything is recoverable from the
git tag `archive/pre-simplify-2026-09-30`, which holds the screens, their
clients and their tests, and the design rules that stood in §2 and §3 (the
Team mode, the right panel's DMs and Teams tabs and agent switcher, focused
teamspace, team and specialist views, direct messages, side conversations and
a request's line in a DM).

- **Multi-agent teams, Workflows and Specialists** (Agents mode).
- **Team mode**: Teamspaces and rooms, side conversations, direct messages and
  recent conversations, the People directory, and opening a DM from the
  address (`?dm=`). People and invitations are in Settings.
- **Not touched:** the Go services, and the realtime Worker. `src/server.ts`
  still exports every Durable Object class (the specialist chat, teamspace
  rooms, side conversations, direct messages, the inbox and the registry) and
  serves their routes, so no Durable Object migration is needed; the UI just no
  longer connects to any but the primary agent's thread chat. REALTIME.md says
  what each object holds. A request card that names a teamspace still renders,
  without a link into an archived screen.
