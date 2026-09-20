# Chats (`/app/threads`, `/app/threads/$id`) · Phase 3

**Job:** talk to your own agent, and find a conversation you had before.

## Today

**The list** is a title, "Updated 3 hr", and a trash can. Nothing about what
the conversation was, who handled it, or how it ended. The page explains itself
— "Every conversation with your primary agent, in one place" — which tells a
person nothing the word Conversations did not.

**The thread** puts the person's message in a black bubble on the right and the
agent's answer in a white bordered bubble on the left, with a tool line above
it reading `✓ Agent system list agents · 4 calls · Done`. The answer never says
who wrote it. The only action in the header is a bare trash icon. The title is
truncated with an ellipsis and cannot be changed.

Three problems. A bordered box around every answer makes a paragraph look like
a form field, and long answers — which is most of them — read badly. The tool
line is louder than the answer it produced, and it is written in our words:
"Agent system list agents". And the one thing the header offers is the one
thing a person almost never wants: delete.

## Becomes

```
  Chats                                            [ + New chat ]
  ┌──────────────────────────────────────────────────────────┐
  │ [M] Northwind pricing review                       3 hr  │
  │     "…so the enclosure lands at 62.5% margin."           │
  ├──────────────────────────────────────────────────────────┤
  │ [L] Enclosure cost check                        Tuesday  │
  │     "The last two invoices came to $1,250.00 each."      │
  └──────────────────────────────────────────────────────────┘
```

```
  ← Northwind pricing review                            ⋯

                     In one sentence: what specialists and
                     teams do I have?

  [M] Maya
      You have one specialist named Ledger and one team called
      Finance desk, and your workflow ran its first
      initialization step last.
      ⌄ Looked at your agents and teams · 4 steps

  ┌────────────────────────────────────────────────────────┐
  │  Reply…                                            ↑   │
  └────────────────────────────────────────────────────────┘
```

- **The agent's answer is text on the page**, with its monogram and name, not a
  card. The person's own message keeps a bubble: one bubble in the conversation
  means the bubble means something.
- **Tool work collapses under the answer**, in plain words — "Looked at your
  agents and teams · 4 steps" — expandable for anyone who wants the calls. It
  is evidence, not headline.
- The header keeps the title (editable in place, no dialog) and a `⋯` menu that
  holds rename, export, and delete.
- Reading column capped at 72 characters. The composer stays pinned.
- A run that is working says so where the answer will be, with its steps
  streaming into the same collapsed line.

## Deleted

- The page description line
- The bordered bubble around agent answers
- The bare trash icon in the header
- `4 calls · Done` as a top-level string

## Words

| Now | Becomes |
| --- | --- |
| Conversations | Chats |
| Every conversation with your primary agent, in one place. | *(deleted)* |
| Agent system list agents · 4 calls · Done | Looked at your agents and teams · 4 steps |
| Ask, plan, research, or build… | Reply… *(in a thread)* |
| New conversation | New chat |

## States

- **Empty list:** "No chats yet. Ask Maya something and it will show up here."
  with the composer right there.
- **Streaming:** the answer area shows the agent's name and a quiet working
  line; the composer stays usable; stop is where send was.
- **Failed run:** the failure sits in the conversation as a line from the agent
  — "I could not finish that: …" — with a retry, not as a red banner at the top
  of the page.
- **Long answer:** no truncation, no "show more". It is a conversation.

## Open question

Threads are per-member and private. Should a chat be promotable into a room
("continue this with the team")? It is one button and the room already accepts
a first message — worth deciding before the room work lands.
