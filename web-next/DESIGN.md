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
| Raised surface | `#242424` | Cards and grouped content |
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
  shown, and it never shows empty or example slots. Collapsed, the panel's rail shows one icon per tab, and each opens
  the panel on that tab. A tab without a live service shows a one-line empty
  state, never example people, rooms or messages.
- Sidebar content follows the primary mode:

  | Mode | Left navigation |
  | --- | --- |
  | Home | Overview, Knowledge bases, Tools, then saved views |
  | Team | Teamspaces, People, then up to three visible recent conversations |
  | Agents | Agent overview, Multi-agent teams, Specialists |

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

- A teamspace or extensive agentic task is a focused work view: no left
  sidebar, naked conversation on the base surface, compact title/back header,
  and a separate work panel. Tabs such as Overview, Tasks, Files, and Activity
  sit **above** the work panel. The panel's top area belongs to the selected
  view's heading and state, not the tab strip.
- Header-row controls — work tabs, panel tabs, panel toggles — sit on the
  header's own centre row (y=28 in its 56px band), vertically level with the
  back button and title, even when (like the work tabs) they are positioned
  over a region below the header rather than inside the `<header>` itself.
- An agent conversation reads as a quiet log on the base surface: the
  member's messages in raised bubbles on the right; the agent's answers as
  full-width Markdown, labelled only when a sub-agent wrote them; each run of
  tool calls folded into one muted status row; confirmations as small cards
  with Decline and Approve pills (several waiting are answered together);
  files as a thumbnail or a file chip; a failed run as one muted error line,
  and an answer cut short marked "Interrupted". A specialist's answer is its
  work result's summary, with one muted line under it for the rest (needs
  your input or blocked, the tools it used, the files it saved, the hand-offs
  it suggests); while it works, one muted pulsing line says so. In the agent
  panel one muted row above it names the conversation, with a quiet icon
  button that starts a new one; a specialist's focused view carries the same
  in its presence row. No history list until one is designed.
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
