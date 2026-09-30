# Reply yourself, and asks that need the owner's own reply: visual QA

Captured on 2026-10-01 from the implemented request cards (Requests, the
desk's Needs you and the conversation's request cards), running under
`vite dev` with the real Worker, against a disposable local fixture API with
made-up data: one workspace, in which Ada is signed in and Raya and Sam ask.
Go's `POST …/reply`, `answer_by` and `result.written_by_owner` are not on
`main` yet, so the fixture answers them as the pinned contract says (a 200
with `{"request"}`, a 409 `already_decided` with a `winner`, a 500). The
fixture and the capture tool (headless system WebKit) lived outside the
repository and are not part of the product. Animations were turned off and
scroll indicators hidden for the captures. Desktop: 1440 × 900. Phone:
375 × 812.

| State | Desktop | Phone |
| --- | --- | --- |
| An ask that needs the owner's own reply, from Slack: the field open, then Send reply, Let my agent answer and Deny (the next card is today's three choices with Reply yourself under them) | [Slack ask](owner-first-slack-desktop.jpg) | [Slack ask](owner-first-slack-phone.jpg) |
| The same for a request an agent filed in Oryxa: no line on what letting the agent answer does, and no note | [In Oryxa](owner-first-in-app-desktop.jpg) | |
| Reply yourself under a Slack ask's three choices, opened and written | [Field open](reply-open-desktop.jpg) | [Field open](reply-open-phone.jpg) |
| Reply yourself for an answer held for review: it says it takes the held answer's place | [Held answer](held-review-desktop.jpg) | [Held answer](held-review-phone.jpg) |
| A reply over 4,000 characters: the count, the marked field and why it was not sent | [Too long](too-long-desktop.jpg) | |
| A send that failed: what was written is kept, with the reason | [Send failed](send-failed-desktop.jpg) | |
| A reply that lost the race: who won, and the words kept to copy | [Lost](lost-race-desktop.jpg) | |
| Settled, for the owner: "You replied." and their words as written | [You replied](settled-owner-desktop.jpg) | [You replied](settled-owner-phone.jpg) |
| Sent, for the asker: "Waiting for Sam to reply." and "Raya replied." beside an answer the agent wrote | [Sent](asker-sent-desktop.jpg) | [Sent](asker-sent-phone.jpg) |
| The asker's conversation: the same three states on its quiet request cards | [Conversation](asker-chat-desktop.jpg) | [Conversation](asker-chat-phone.jpg) |
| Requests → Activity | [Activity](activity-desktop.jpg) | |
| The desk's Needs you, with both kinds of card | [Needs you](desk-needs-you-desktop.jpg) | |

## Browser observations

- In the browser pane, against the same fixture: pressing Reply yourself opened
  the field with focus in it; typing two lines and pressing Command and Enter
  sent the reply (`POST …/irq_slack/reply`, 200); the card settled to "You
  replied." with "Sent in Oryxa at 3:24." and the two lines kept, took focus,
  and the sidebar's Requests count went from 6 to 5.
- Cancel dropped what was written and gave focus back to the Reply yourself
  button; the owner-first cards were untouched and took no focus on load.
- The shell's focus ring for a textarea drew a square inside the pill, so the
  field's own border is its focus mark, as the chat composer's is.
- On a phone the field takes the width and the buttons stack: Send reply above
  Cancel, and Send reply, Let my agent answer and Deny in that order; the field
  is 16 px, so iOS does not zoom into it.

## Not shown

- Go's side of `…/reply`, `answer_by` and the Slack modal: they are separate
  work. The cards read the contract's fields and nothing else.
- Letting the agent answer, and Deny, on an owner-first card: they are the
  consent and decision calls the cards already made, covered by the tests.
- The chat's composer says "Reconnecting…" in the conversation captures: the
  fixture does not serve the chat socket. Only the request cards matter there.
