# One central screen: visual QA

Captured on 2026-09-30 from the implemented `WorkspaceShell` (sidebar and
central screen), running under `vite dev` with the real Worker and its
`ThreadChatAgent` Durable Object, against a disposable local fixture API with
made-up data for two workspaces. The fixture and the capture tool (headless
system WebKit) lived outside the repository and are not part of the product.
Animations were turned off and scroll indicators hidden for the captures (the
capturing Mac draws classic scrollbars; a phone overlays them). Desktop:
1440 × 900. Phone: 375 × 812.

| View | Desktop | Phone |
| --- | --- | --- |
| The desk, busy | [Needs you](desk-busy-desktop.jpg), [lower: access, answers, setup](desk-busy-lower-desktop.jpg) | [Needs you](desk-busy-phone.jpg) |
| The desk, empty | [Quiet, setup unfinished](desk-empty-desktop.jpg) | [Quiet](desk-empty-phone.jpg) |
| A conversation | [With its Files open](conversation-desktop.jpg), [agent paused](conversation-paused-desktop.jpg) | [Conversation](conversation-phone.jpg) |
| Requests | [Inbox](requests-desktop.jpg) | [Inbox](requests-phone.jpg) |
| Knowledge | [Knowledge bases](knowledge-desktop.jpg) | [Knowledge bases](knowledge-phone.jpg) |
| Sidebar | [Collapsed to the rail](sidebar-collapsed-desktop.jpg) | [Open over a conversation](sidebar-open-phone.jpg) |

## Browser observations

- As the app opened with no view to resume, the opening rules opened the
  conversation waiting on the member's approval (the pricing FAQ, marked
  "Needs approval" in Recent); New chat brought back the desk, whose Needs you
  lists that conversation's tool confirmation once, beside the Slack ask and
  the held answer.
- `?chat=th_launch` opened that conversation in the center and left the
  address as `?org=org_busy`. Its socket to `ThreadChatAgent` opened through
  the Worker's access check (the composer shows no "Connecting…"); the
  transcript came from Go's copy of the thread.
- Files (1) opened the conversation's files inline; choosing the brief opened
  the viewer in place of the conversation, with Back to the conversation.
- The sidebar's Requests count (2) equals the Inbox tab's; `?view=requests`
  and `?view=knowledge` opened those screens.
- The collapsed rail keeps New chat, the three destinations (with the count as
  a badge) and the profile; the choice survives a reload.
- On a phone the menu control opens the sidebar over the conversation behind
  a scrim; a selection closes it.

Chat streaming itself (a live run) is not shown: the fixture answers no runs.
The tests cover streaming, the request queue, drafts, "Not sent." rows, the
opening rules and the hidden-but-mounted chat.
