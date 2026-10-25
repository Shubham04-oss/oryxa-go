# Usability and brittleness issues

What makes Oryxa hard to use or unreliable: things people see in web-next on app.oryxa.in, and the backend behaviour behind them. One file per area. Each issue has the same shape so a fixer can pick it up cold.

Started 2026-10-25 from the owner's walkthrough notes and a read-only walk of production.

| File | Area |
|---|---|
| [01-teamspaces.md](01-teamspaces.md) | Teamspace room, parley, plan, files, activity |
| [02-agent-chat.md](02-agent-chat.md) | Opening the agent chat, history, new chat, side panels |
| [03-browser-and-live.md](03-browser-and-live.md) | Browser / sandbox sessions, Live tab, session lifecycle |
| [04-home-requests-settings-library.md](04-home-requests-settings-library.md) | Home, Requests inbox, Settings, Library |
| [05-core-flows.md](05-core-flows.md) | Driven end to end: search, chat, tools, files, setup, requests |
| [06-ux-design.md](06-ux-design.md) | UX and visual design: concepts, panes, type, colour, feedback, navigation |
| [07-duplication.md](07-duplication.md) | Many ways to do one job in the UI (N1–N12) |
| [07a-code-duplication.md](07a-code-duplication.md) | Many pieces of code doing one job (D1–D26), plus dead code |
| [plan-design-tokens.md](plan-design-tokens.md) | Plan: one type, radius and colour scale (fixes U5–U7); owner decisions first |

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

134 issues so far: T1–T17, C1–C17, B1–B16, H1–H14, F1–F19, U1–U13, N1–N12, D1–D26. Ranked by how much they hurt everyday use of the main app.

**Fix first: trust and "does it work"**

| # | Issue | Why it matters |
|---|---|---|
| 1 | [C1](02-agent-chat.md) The chat opens an old chat from history | It reopens the newest chat with an unanswered tool confirmation, and those never expire (`chat-choice.ts:138-159`). |
| 2 | [C2](02-agent-chat.md) + [F10](05-core-flows.md) Answers stuck on "Working", and the chat list locked while one runs | Seen live: "Working 17m" on a finished answer. You can't switch chats meanwhile. |
| 3 | [F17](05-core-flows.md) Arlo answers teammates from stale indexed docs | It said deploys were disabled on a day with two releases, and the teammate argued with it. |
| 4 | [F4](05-core-flows.md) Write actions are confirmed in plain text | Nothing tracks or surfaces them, and the "yes" depends on the model's wording. |
| 5 | [T1](01-teamspaces.md)–[T4](01-teamspaces.md) Parley: no notes, a silent agent, wakes only on "Krude", hears only some people | Why parley is hit or miss. |
| 6 | [B1](03-browser-and-live.md) / [B2](03-browser-and-live.md) / [B4](03-browser-and-live.md) The browser shows as live, or stays alive, after the chat is old | The UI reads a bookkeeping row, and close does nothing after a restart. |
| 7 | [F5](05-core-flows.md) 100k–340k tokens per turn, even for "cancel that" | Slow and costly. |

**Fix next: findability and layout**

| # | Issue | Why it matters |
|---|---|---|
| 8 | [H2](04-home-requests-settings-library.md) + [F16](05-core-flows.md) The URL never changes; Back doesn't mean back | Refresh, links and tabs all lose your place. |
| 9 | [F1](05-core-flows.md) ⌘K finds no pages, people or files | The main way to navigate doesn't navigate. |
| 10 | [F15](05-core-flows.md) Four columns on the Chats page crush the overview | Text wraps one word per line, and tabs disappear. |
| 11 | [F3](05-core-flows.md) No file attach in chat | A basic expectation for an assistant. |
| 12 | [C3](02-agent-chat.md) Home cards and sends take over a running chat | Answers get lost from view mid-run. |
| 13 | [C13](02-agent-chat.md) / [C14](02-agent-chat.md) / [T8](01-teamspaces.md) Krude vs Arlo vs "@Arlo-UJVX" | Users can't tell who they are talking to. |
| 14 | [T5](01-teamspaces.md) / [T6](01-teamspaces.md) Teamspace opens slowly, and fetches `/log` three times | "Opening the room…" for seconds. |
| 15 | [F6](05-core-flows.md) / [F7](05-core-flows.md) Plumbing steps and token counts shown in answers | Noise in every answer. |
| 16 | [F13](05-core-flows.md) / [F14](05-core-flows.md) Specialist → team → workflow is a hidden chain with a bare form | Nobody gets past the first step. |
| 17 | [F18](05-core-flows.md) One teammate conversation becomes seven request rows | The inbox can't be read as conversations. |
| 18 | [H3](04-home-requests-settings-library.md) / [C6](02-agent-chat.md) The "five newest chats" note, and its polling | Noise, plus load. |
| 19 | [H6](04-home-requests-settings-library.md) / [H9](04-home-requests-settings-library.md) Raw tool ids in Settings, and a raw Library dump | First impressions. |
| 20 | [H12](04-home-requests-settings-library.md) Mobile home layout breaks | 375 px is unusable in places. |

**UX and design, decides how the app feels:** [U1](06-ux-design.md) too many nouns for one product; [U2](06-ux-design.md) two to four panes and two message boxes per screen; [U5](06-ux-design.md) / [U6](06-ux-design.md) text mostly 10–12 px, with no type or radius scale (about 25 sizes and 13 radii across 36 CSS files); [U3](06-ux-design.md) empty states everywhere instead of a first-run path. The fix order is at the end of that file.

**Neatness, one way and one component per job:** [N1](07-duplication.md) four message boxes and eight ways into the same agent; [N5](07-duplication.md) + [D4](07a-code-duplication.md) "what needs you" is counted in five places by two different rules; [N8](07-duplication.md) "team" means three things; [D1](07a-code-duplication.md) nine time formatters and [D5](07a-code-duplication.md) five wordings for one request status.

Not about the main app's usability, recorded so it isn't lost: [H1](04-home-requests-settings-library.md), Slack off in production because of a hidden local tfvars file.

Some of these come from reading the code only, and each file marks its guesses. Each file's open questions list what only a live test can settle.
