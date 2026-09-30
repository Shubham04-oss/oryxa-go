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
(the desk, the primary agent's chat, Requests, Knowledge bases, Tools,
Settings and sign-in). Multi-agent teams, Workflows, Specialists and the whole
Team mode (Teamspaces and rooms, side conversations, direct messages and the
People directory) are gone from the UI, along with the right panel's DMs and
Teams tabs. Their screens and their rules were removed from this contract with
them; everything is recoverable from the git tag
`archive/pre-simplify-2026-09-30`, and §7 says what stays in the Worker. Do not
rebuild any of it without an explicit decision that supersedes this note.

**One central screen, later on 2026-09-30.** The Home and Agents modes, the
Agent overview and the right agent panel were then replaced by one sidebar
and one central screen (§2, §3). The Agent overview's Resume and the
conversation's files moved to the desk and the conversation; its Requests
tile became the sidebar's count.

## 1. Visual foundation

| Role | Color | Use |
| --- | --- | --- |
| Base surface | `#111111` | Page surround, the naked sidebar, the header band, auth inset |
| Main surface | `#1e1e1e` | Central canvas, settings content, auth page |
| Cards | `#111111` | Compact cards on the canvas (the desk's, the conversation's files) |
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

**One sidebar and one central screen (2026-09-30).** The owner replaced the
Home and Agents modes, their tabs and the right agent panel with one layout:
a sidebar, the only navigation, and one central screen that shows exactly one
thing. There is no right panel and no mode: do not bring back a second panel,
mode pills or navigation that changes with the screen.

- The base `#111111` surface carries the sidebar without a card. The
  `#1e1e1e` canvas is the center: it sits beside the sidebar with an 8px
  gutter at the right and bottom and rounded corners, under the header. No
  separator or divider lines between the two.
- The sidebar, top to bottom:
  1. the workspace: its name, or a switcher when the member belongs to more
     than one (choosing one opens it), beside the collapse toggle;
  2. **New chat**, a quiet bordered button;
  3. **Requests**, with the Inbox count (the requests waiting on the member,
     the same number as the Inbox tab), **Knowledge** and **Tools**, each
     marked while its screen is in the center;
  4. **Recent**, the primary agent's conversations (below);
  5. at the foot, the profile button, whose menu holds Settings and Sign out.
- On a desktop the sidebar is about 232–272px wide and collapses to a 56px
  icon rail: the toggle, New chat, the three destinations (the count as a
  small badge on Requests) and the profile. The rail has no Recent list and
  no workspace name; every icon keeps its accessible name and a title. The
  member's choice is kept in this browser (`localStorage`), a per-viewer
  convenience only.
- **Recent** lists the member's primary-agent conversations newest first,
  each a quiet two-line row: its title (or "Untitled chat") and when it was
  last active, "Needs approval · …" when it waits on the member's tool
  approval, and "Working" with a small Thinking Orb while the open chat's run
  streams in it. The conversation the center shows is marked current; while
  another screen is in the center, none is. Delete appears on hover or focus
  (always on touch screens), asks inline, clears the live chat object first,
  then deletes the canonical Go thread; a failure stays visible and
  retryable, and deleting the open conversation brings back the desk. Hide
  empty abandoned starts unless selected or waiting on approval. While a
  first message is settling into its conversation the rows wait (they are
  disabled), as New chat does. Approval markers come from the bounded check
  of the five newest chats, read only while the list is shown, and from what
  this member's earlier reads already hold; the list never fetches every past
  transcript. Loading, empty and failed reads say so, with Try again.
- The shared header is compact (56px), its title centered over the center.
  It names what the center shows: **Today** for the desk, the conversation's
  title (with Files and New chat at the right), **Requests**, **Knowledge
  bases** (an open base's name and "Knowledge base", with a back button to
  all of them), **Tools**. Do not repeat the title, a slogan or a description
  in the canvas. No preview badge in the header.
- The center shows exactly one of: the desk, a conversation, Requests,
  Knowledge or Tools (§3). Settings opens over everything from the profile
  menu.
- The address follows the screen, so a reload stays on it:
  `?view=requests` (with `&request=` while a request a link or a note named
  is open), `?view=knowledge`, `?view=tools`. The member's agent (the desk or
  a conversation) has no view: a reload there follows the opening rules
  (§3). Inbound links keep working: `/?org=…&view=requests&request=…` from
  Slack and Go's acknowledgements, `?view=tools` with Composio's return,
  `?view=slack` from Slack's install and `?view=billing` from
  `/settings/billing` open their screen or Settings page once.
  `?chat=<thread id>` opens that conversation once and leaves the address,
  so a later reload follows the opening rules.

## 3. The center

- **The desk (Today)** is what the center shows with no conversation open:
  as the app opens (unless the opening rules reopen one), and after New chat.
  It is the primary chat's blank state. Use a centered column at most 760px
  wide on the `#1e1e1e` canvas, compact `#111111` cards, the section 1
  palette and Geist type. On a phone, cards use the available width and
  controls wrap without horizontal scrolling. No greeting or starter prompts.
  - The first card names the member's primary agent (its voxel avatar and
    name; "Your agent" until they load) above the composer "Ask your agent…".
    While the agent is paused it says so, with Resume. The composer is the
    primary chat's own, with one draft shared with the conversations'
    composer, as one composer kept it before: what the member typed follows
    them between the desk and their conversations, and a first message sent
    from the desk takes it. Sending makes a conversation of its own and
    **switches the center to that conversation**; it never opens a side panel.
    The first messages that were not sent stay under the composer, with Retry,
    Edit and Dismiss (below), and so does why the chat left a conversation
    ("That conversation no longer exists."). The desk lists no conversations
    and has no New chat or All of its own: the sidebar has them.
  - Next, **Needs you** holds actual Inbox decisions, held answers awaiting
    the member's review, and their primary-agent tool confirmations. Reuse the
    request cards' authorized actions and race handling: Slack asks offer
    Share answer, Allow for 1 hour and Deny inline, and any other request
    Share answer and Deny; held answers offer the existing review choices. A
    tool-confirmation row opens its exact conversation in the center so the
    member can review the actual confirmation cards. Count conversations
    once, even when they have several pending tool confirmations. Requests
    awaiting automatic agent approval do not wait on the member. Primary-chat
    confirmations are checked in the five newest chats plus the open chat,
    using the same newest-five selection as Recent and the opening rule. Go
    derives confirmations from events and maintains no aggregate pending flag,
    so this bounded read does not cover older unopened conversations. State
    the coverage visibly, and never show an unqualified all-clear when any
    conversations or reads remain unchecked; its "All conversations" brings
    the sidebar's Recent list into view (it opens the phone's overlay).
    Full chat detail checks poll every 30 seconds and refresh on mount and
    window focus; lightweight lists may poll every 10 seconds.
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
    has unfinished steps. Loading, failed, refused, paginated or incomplete
    decision reads must not imply all clear; state the actual checked coverage
    and offer retry where appropriate. Full bounded responses are lower
    bounds, not totals.
  - All data comes from authenticated APIs; there is no production prototype
    route or fixture data. Scope private caches and retained chat activity to
    the current workspace and member. Hide cached private work and actions
    after an authorization refusal. A member change remounts the shell and a
    workspace/member change resets the chat; refreshing the same member's
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
- **A conversation** is the primary agent's chat in a centered column about
  760px wide: the timeline, its confirmations and request cards, and the
  composer at the bottom. The log scrolls the whole canvas width, with its
  content centered. The header holds the conversation's title (Go's first
  line of its first message; "New conversation" until it has one), **Files**
  and **New chat**, which brings back the desk. While the primary agent is
  paused, a quiet notice above the log says so, with Resume.
  - **Files** (with a count once there are any) shows the conversation's
    files inline, above the log, newest first, each by its latest version: a
    new chat has none, and neither does a chat that has not opened yet.
    Choosing one opens the viewer **in place of the conversation**, which
    stays mounted behind it; Back returns to it, and leaving the conversation
    (New chat, another conversation) closes it. A file a run saves joins Files
    at once, and stays a chip in the log; the chat never opens a viewer by
    itself. The viewer is clean and compact: Markdown uses the same rendering
    as chat, and PDF, DOCX, PPTX, XLSX, CSV, text and images use small in-app
    rendering libraries, with only page or sheet navigation when needed. A
    Word document is drawn in a sandboxed frame of its own, so nothing it
    carries (styles, links, scripts) can reach the app around it. A file over
    25 MB is not read: the viewer says it is too large to preview and leaves
    it to the download. The viewer has no annotation or office-editor chrome.
    A compact Edit button, offered for DOCX, XLSX and PPTX only (the formats
    the editor saves back as they were), opens the full ONLYOFFICE editor, and
    View returns to the simple viewer. Edits save as new conversation-file
    versions. A missing editor preserves native PDF, image and text previews
    where available.
  - The agent receives only the current Oryxa view (`home/overview` for the
    desk, `agents/chat` in a conversation, `agents/file` with the viewer open,
    `agents/requests`, `home/knowledge`, `home/tools`), selection and
    explicitly marked visible item handles when a message is sent; it never
    implies a live view of other windows or grants access to those items.
  - An agent conversation reads as a quiet log: the member's messages in
    raised bubbles on the right; the agent's answers as full-width Markdown,
    labelled only when a sub-agent wrote them; each run of tool calls folded
    into one muted status row with a Thinking Orb for the activity state and
    small app marks when a call identifies an app; the row never expands into
    a list. A group stays Failed only for an unrecovered final tool failure. A
    later successful retry can recover an earlier error only when it starts
    after that failed result, in the same group, with the same tool, known
    author and deeply equal explicit arguments. Missing arguments cannot prove
    a retry; parallel duplicates and unrelated successes do not clear
    failures. Keep recovered errors in a muted “Recovered tool error” note (or
    count), with their bounded reasons available on hover. Genuine failures
    keep the existing red status; waiting, running and interrupted states keep
    their precedence. A request filed by the agent is a small, quiet request
    card showing its status and its published answer as it arrives; only the
    result authorized for the requester appears. Confirmations are small cards
    with Decline and Approve pills (several waiting are answered together);
    files as a thumbnail or a file chip; a failed run as one muted error line,
    and an answer cut short marked "Interrupted".
  - **The opening rules** hold as the app opens or reloads: the conversation
    the member left **less than five minutes ago** in this tab reopens,
    including a Recent pick, and an **explicit** New chat in that window stays
    new (the desk); otherwise the chat checks the five newest chat
    conversations and opens the newest one waiting on the member's tool
    approval, including an untitled one; otherwise the desk. Record member
    presence and actual leave time per workspace, member and agent, never
    server update time, and only while the member's agent is in the center:
    activity saves at most once every 15 seconds; pagehide, hiding, unmount
    and leaving for another screen save immediately. A hidden tab never renews
    that grace. An automatically opened blank chat (the desk) does not
    suppress a newly waiting approval. While the chat chooses, the center
    shows nothing for the moment the member's agents take to load, then the
    conversation loading if it is about to reopen the one the member just
    left (they can type at once, and keep what they typed), otherwise the
    desk; a choice that takes five seconds gives way to the desk. A
    conversation a link or the member opens is never switched away from by
    the rules.
  - The chat stays mounted while Requests, Knowledge or Tools is in the center,
    hidden: a run goes on streaming and a first message on its way still
    reaches its conversation. It connects to an open conversation only once
    the member's agent has been in the center (or a first message needs it),
    so a link straight to another screen opens no socket.
  - A first message, from the desk or from a request elsewhere in the app
    (`requestPrimaryChat`, as a link does), is made a conversation of its own
    and sent into it, in order, whatever the member does meanwhile: the chat
    ends on the latest, no message is dropped, and a request never touches the
    composer's text. One that can't be sent stays in view (at the end of the
    open conversation's log, or on the desk) as their bubble with "Not sent."
    and the reason under it, and Retry (into a conversation of its own), Edit
    (into the composer, only while that is empty) and Dismiss: nothing else
    clears it, and it is never put in the composer by itself. A request waits
    until the chat takes it (once the member's agents have loaded), in the
    order it was made; past twenty messages waiting, the next is refused the
    same way. Go keeps previous conversations until deleted. What the chat
    holds belongs to the member: another member taking over the page inherits
    none of it.
- **Requests** (the sidebar's first destination) is where a member sees what
  waits on them and what they asked, whether an agent filed it or someone
  asked in Slack, in a centered column. It has three tabs: Inbox, with the
  requests waiting on the member first and the count in its tab; Sent; and
  Activity, what happened to their Slack asks (asks, decisions and where they
  were made, access given and ended, secrets opened), which names people and
  states and never a question or an answer. A filter (All, Slack, Oryxa)
  narrows the first two. The lists are re-read as the page is focused and
  every few seconds, so a request decided in Slack settles here without a
  reload. A request a link or a note names opens first, above the lists
  (`/?org=…&view=requests&request=…`); leaving Requests puts it away.
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
- **Knowledge** (the sidebar's) shows the knowledge bases. Creating one asks
  for a short name, with a concrete label example and a visible 2–60 character
  hint; facts and notes belong in its content. After creation, open that base
  with Add content already on Note, retaining the choices for a link or file.
  The member may cancel adding; ordinary visits to an existing base keep the
  form closed. Cancelling creation while its request is out never opens a base
  that arrives later. Preserve the existing permissions and honest errors.
- **Tools** (the sidebar's). Connected tools form one horizontal, logo-led row
  with expandable details. The browse catalog is categorized and uses two
  sleek cards per row at normal and small widths. Collapsed cards show logo
  and name only; description and details appear on expansion. Do not add a
  “Connect” button to every collapsed card.
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

- At 800px and below the center takes the whole width and the sidebar is an
  overlay over it, behind a scrim. The header's one small circular menu
  control, at its top left, opens it; focus moves into it (its close button)
  and back to the menu control as it closes. It closes after any selection
  (New chat, a destination, a conversation in Recent, Settings), on Escape,
  from its close button, or on a tap outside it. No persistent rail, extra
  control bar or second overlay.
- Keep the header's title centered between the menu control and the
  conversation's actions, which become icon-only buttons (Files keeps its
  count as a small badge). Preserve adequate clearance for the title and
  touch targets at narrow widths.
- While a composer (the desk's or a conversation's) is in use and the virtual
  keyboard reduces the visual viewport, the page fits the visible height, so
  the header and the composer stay on screen.
- Check a desktop viewport near 1440×900 and a phone viewport near 375×812,
  with the sidebar expanded and collapsed (desktop) and open and closed
  (phone), and the composer in use where relevant. These are regression
  targets, not fixed layout sizes.

## 6. Rules for adding or changing a screen

1. Start from the shared shell: a new screen is a destination of the sidebar
   or part of the member's agent (the desk or a conversation). Do not add a
   mode, a second navigation, tabs across the shell, or a side panel.
2. Decide what belongs in the header, the sidebar, and the center. The header
   names what the center shows; the sidebar navigates; the center does the
   work, one thing at a time.
3. Reuse the palette, type, pills, avatars, button states, and card density
   above. Add a new token or visual treatment only for a real semantic need.
4. Preserve keyboard access, focus visibility, responsive behavior, and honest
   loading/empty/error states. Role restrictions must affect controls as well
   as copy.
5. Visually verify desktop and mobile. For shell changes, exercise the
   collapsed rail, the phone overlay, the desk and a conversation. For forms,
   exercise invalid, submitting, success, and service-offline states.

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
- **Replaced, not archived** (the central screen, later the same day): the
  Home and Agents modes, the Agent overview (`agents-mode.tsx`) and the right
  agent panel (`workspace-agent-panel.tsx`) with its rail. Their behavior
  lives on in the sidebar, the desk and the conversation (§2, §3); the old
  files are in the history of `main` before that change.
- **Not touched:** the Go services, and the realtime Worker. `src/server.ts`
  still exports every Durable Object class (the specialist chat, teamspace
  rooms, side conversations, direct messages, the inbox and the registry) and
  serves their routes, so no Durable Object migration is needed; the UI just no
  longer connects to any but the primary agent's thread chat. REALTIME.md says
  what each object holds. A request card that names a teamspace still renders,
  without a link into an archived screen.
