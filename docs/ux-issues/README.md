# Usability and brittleness issues

What makes Oryxa hard to use or unreliable: things people see in web-next on app.oryxa.in, and the backend behaviour behind them. One file per area. Each issue has the same shape so a fixer can pick it up cold.

Started 2026-10-25 from the owner's walkthrough notes and a read-only walk of production.

| File | Area |
|---|---|
| [01-teamspaces.md](01-teamspaces.md) | Teamspace room, parley, plan, files, activity |
| [02-agent-chat.md](02-agent-chat.md) | Opening the agent chat, history, new chat, side panels |
| [03-browser-and-live.md](03-browser-and-live.md) | Browser / sandbox sessions, Live tab, session lifecycle |
| [04-home-requests-settings-library.md](04-home-requests-settings-library.md) | Home, Requests inbox, Settings, Library |

## Issue template

```md
### <ID> <one-line title>

- **Severity:** blocker | high | medium | low
- **Kind:** broken | brittle (works sometimes) | confusing | slow | polish
- **Seen:** where and how (prod walkthrough, owner report, code reading)

**What happens.** Steps and what the user sees.

**Why.** Root cause with file:line references. Say when it is a guess.

**Fix.** The smallest change that solves it, and where.
```

Severity: **blocker** means a core flow fails; **high** means it fails often or misleads; **medium** means it confuses or is slow; **low** is polish.

## Ranked summary (2026-10-25)

64 issues so far: T1–T17, C1–C17, B1–B16, H1–H14. These are the ones that decide whether the product feels usable. Fix them first, in roughly this order.

| # | Issue | Why it matters |
|---|---|---|
| 1 | [H1](04-home-requests-settings-library.md) Slack is off in prod because of a hidden local tfvars file | Asks from Slack stopped arriving on 15 Oct. Verified: task defs 120–191 have `APPS_SLACK_SURFACE=false`. |
| 2 | [T1](01-teamspaces.md) Parley notes are never produced | No edge code posts to `/v1/edge/teamspaces/{id}/parley-notes`, and the edge token lacks `log:write`. The UI promises notes anyway. Verified by grep. |
| 3 | [T2](01-teamspaces.md) The agent in a parley is silent | The edge has no `GEMINI_API_KEY` (verified with `wrangler secret list`), and the voice DO only logs a warning. Ties to #515 (voice on Vertex). |
| 4 | [C1](02-agent-chat.md) The chat opens an old chat from history | It reopens the newest chat with an unanswered tool confirmation, and those never expire. Verified in `chat-choice.ts:138-159`. |
| 5 | [C2](02-agent-chat.md) A finished answer stays "Working 17m" | Seen live. It also blocks switching chats (C9). |
| 6 | [B2](03-browser-and-live.md) / [B1](03-browser-and-live.md) / [B4](03-browser-and-live.md) The browser stays alive, or shows live, after the chat is old | The UI state comes from a bookkeeping row; close does nothing after the DO restarts; AWS browsers have no idle stop. |
| 7 | [H2](04-home-requests-settings-library.md) The URL never changes | Refresh, Back and links all lose your place. |
| 8 | [C3](02-agent-chat.md) Home cards and sends take over a running chat | Answers get lost from view mid-run. |
| 9 | [T5](01-teamspaces.md) / [T6](01-teamspaces.md) Teamspace opens slowly, and fetches `/log` three times | Seen live: "Opening the room…" for several seconds. |
| 10 | [C13](02-agent-chat.md) / [C14](02-agent-chat.md) / [T8](01-teamspaces.md) The agent is called Krude, Arlo and "@Arlo-UJVX" | Users can't tell who they are talking to. |
| 11 | [B3](03-browser-and-live.md) Opening the Live pane launches and bills a blank browser | Costs money and confuses. |
| 12 | [H3](04-home-requests-settings-library.md) / [C6](02-agent-chat.md) The "five newest chats" note, and five full reads every 30 s | Noise, plus load. |
| 13 | [H6](04-home-requests-settings-library.md) / [H9](04-home-requests-settings-library.md) Raw tool ids in Settings, and a raw Library dump | First impressions for a new user. |
| 14 | [H12](04-home-requests-settings-library.md) Mobile layout breaks on home | 375 px is unusable in places. |

Some of these come from reading the code only, and each file marks its guesses. Each file's open questions list what only a live test can settle.
