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
- Home, Team, and Agents are the three primary icon-first pill controls. The
  active one is slightly enlarged and more prominent, not a giant tab.
- The right agent panel holds the member's agent and team conversations. Its
  tabs, Agents (the default), DMs and Teams, use the same icon-first pills;
  the Agents pill carries the primary agent's voxel avatar. When the member
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
- Sidebar content follows the primary mode:

  | Mode | Left navigation |
  | --- | --- |
  | Home | Today, Knowledge bases, Tools |
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

- **Home — Today.** Start Home with both sidebars collapsed. Ordinary
  navigation never auto-expands one: keep the state the member leaves with
  its toggles when they return to Home; the normal mutual-collapse rules
  still apply. Home's explicit chat shortcuts open the right panel. On a
  phone, both navigation overlays start closed and close after a selection.
  Today is a centered column about 360px wide on the `#1e1e1e` canvas, with
  its composer and cards on `#111111`, tight rows and 11–12px type. No greeting,
  starter prompts, saved-view examples or Teamspaces card.
  - The composer always starts a **new** primary-agent conversation. It never
    resumes the latest one. Sending follows the existing authenticated
    `ThreadChatAgent` path, with Home remaining the current screen context.
    The shell opens the right panel, then requests the new conversation
    through the shared chat-choice API; it does not navigate to focused work.
  - After the composer, show Waiting on you for owner approvals, held answers
    to review and the selected chat's tool confirmations, then Your agent,
    then Needs attention for failed or expired tools and failed knowledge
    documents. A conversation with one or more confirmations counts as one
    waiting item. Put that conversation first, labelled “Needs confirmation”;
    its row opens that exact chat. All confirmations opens the same chat's
    confirmation cards, and All requests separately opens Requests when Inbox
    items exist. For confirmations alone, show All with the accessible name
    All confirmations. Neither shortcut implies a chat confirmation is in
    Inbox.
  - Your agent has one list of at most three conversations. Put the selected
    primary chat first when its scoped live activity is running, with the
    existing Thinking Orb in its working state. Fill the remaining rows with
    recent chats, without duplicating its exact ID or a waiting conversation
    already shown above. Skip missing, empty or whitespace-only recent titles
    before limiting the rows. Selected running or waiting work with no title
    stays reachable as “Current conversation.” Idle or absent activity leaves up
    to three real recents, including when nothing else needs the member. This
    signal describes the selected chat, not every background job; never infer
    work from update time.
    A waiting chat suppresses the quiet line and adds one to Home's combined
    waiting count, while the Requests Inbox count remains its own. Before a
    running chat has an ID, show a noninteractive “Starting conversation” row.
    Each concrete conversation
    opens its exact ID in the right panel while staying on Home. New chat
    starts a blank conversation, and All opens the agent's work.
  - Scope the primary panel's retained conversation and draft to the current
    workspace and member. A scope change remounts that panel; Home must not
    treat the previous scope's activity as current.
    A member change remounts the entire shell, clearing open dialogs, navigation
    and drafts. Refreshing the same member's session preserves their choices.
  - At most three rows per section; All opens the relevant section.
    Hide sections that have nothing. With no decisions, tool confirmations or
    failures, keep one quiet line alongside any real recent conversations.
    Once all reads have settled successfully with complete coverage, that line
    is “Nothing needs you right now.” without repeating complete knowledge-base
    coverage. Bounded requests or partial knowledge coverage keep the qualified
    single line about the work checked; denied or loading reads never imply all
    clear.
  - Counts must reflect the response's limits: a full bounded requests response
    is a lower bound (`N+`), not a total; a known live confirmation also gets
    `N+` while Inbox reads are loading or refused. Check only a fixed number of
    knowledge bases, query failed documents directly, and state coverage naturally
    (for example, “Checked 3 of 8 knowledge bases”). Loading, a refused read,
    and partial coverage must never imply that all work is clear.
- A teamspace or extensive agentic task is a focused work view: no left
  sidebar, naked conversation on the base surface, compact title/back header,
  and a separate work panel. Tabs such as Overview, Tasks, Files, and Activity
  sit **above** the work panel. The panel's top area belongs to the selected
  view's heading and state, not the tab strip.
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
  never expands into a list. A request filed by the agent is a small, quiet
  request card showing its status and its published answer as it arrives;
  only the result authorized for the requester appears. Confirmations are small
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
  open the newest accessible conversation waiting on the member's tool approval,
  including an untitled one; otherwise open New chat. Record member presence and
  actual leave time per workspace, member and agent, never server update time.
  A hidden panel/tab never renews that grace. An automatically opened blank
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
    Approve without tools between them; Approve allows what was asked for.
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
  without a reload. A request a link or a DM's line names opens first, above
  the lists (`/?org=…&view=requests&request=…`).
  - **The request card** says who asked, the question, where it came from (in
    Slack, or in Oryxa), where it stands and, for the person it is theirs to
    answer, what to do. A request from Slack is answered with one of three
    choices, "Summary only", "Allow for 1 hour" or "Deny", and the card says in
    a line what each lets the agent do. Any other request is approved or
    declined, as in a side conversation. Whichever surface decided first, the
    card settles, and says who won ("Approved in Slack by Raya at 14:02.") when
    the member's own choice lost. An answer the guards held shows its owner why
    in plain words, the answer, and Don't send, Send and Send masked; the one
    who asked sees only that it waits. Access the owner gave says until when
    the asker's follow-ups are answered without asking, with an End access
    action while it lasts and "You ended Ada's access." after.
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
- Knowledge Bases is a Home destination. Tools is also under Home. Connected
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
