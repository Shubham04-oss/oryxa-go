# Conversations data layer

Primary-agent threads (`lib/threads`), and the specialist conversation's
wire types the Worker's specialist object shares (`lib/specialists`). No UI:
typed calls, stream parsing and pure timeline logic. The Go contract is
`agents/primary/internal/httpserver/server.go` and
`agents/primary/internal/threads`; every exported call names its handler.

The page chats with the primary agent only. The specialist chat's screens and
page client (`lib/specialists/client.ts`) were archived on 2026-09-30 (git tag
`archive/pre-simplify-2026-09-30`); `SpecialistChatAgent` stays in the Worker, unused by the page.

| Module | What it offers |
| --- | --- |
| `client.ts` | Go's types (`Thread`, `ThreadDetail`, `Item`, `RunInput`, `ConfirmationAnswer`, `ThreadErrorCode`); `listThreads`, `createThread`, `getThread`, `deleteThread`; the Durable Object's HTTP side (`deleteThreadChat`, `getThreadChatMessages`); `startRun` (direct SSE); `artifactContentUrl`, `fetchArtifactLink`; `threadKeys` and `thread*QueryOptions` |
| `event-stream.ts` | `createEventStreamReader` (incremental SSE parser for a POST's body) and `readItemStream`, the one read loop for the browser and the Durable Object |
| `timeline.ts` | `buildTimeline`, `unseenLiveItems`, `pendingConfirmationIds`, and small naming helpers |
| `chat-messages.ts` | The Durable Object's transcript: `ThreadChatMessage`, `threadAgentName`, `itemsToChatMessages`, `chatMessagesToItems`, `reconcileChatMessages`, `withoutSupersededDeltas`, `sessionExpiredCode` |
| `agent-socket.ts` | `reopenSocket`, to carry a refreshed session to the Durable Object |
| `../specialists` | what the Worker's `SpecialistChatAgent` shares: `parseSpecialistResult` for a specialist's JSON answer, `specialistAgentName`, `SpecialistChatState`, `readableSpecialistItems`, `isProgressMarker` (`chat-messages.ts`, `result.ts`). The page-side calls were archived |

Everything here uses relative URLs and the browser's cookies through
`apiFetch`: run the queries in the browser, not during SSR.

## Transport

The primary chat runs through the thread's Durable Object (`ThreadChatAgent`,
one per thread, named `threadAgentName(org, thread)`), as `web/` does since
#82. The page holds a WebSocket to it (the Agents SDK's `useAgent` and
`useAgentChat`); the object POSTs Go's runs route itself with the member's
cookie, reads the answer with `readItemStream`, and stores the transcript.
Against direct SSE it adds what Go's README calls the durability boundary:
the conversation survives a reload, and a page that reconnects resumes a run
still streaming into the object. With direct SSE the run belongs to the
page's request, and Go cancels it when that request goes away. The object
does not replay a run after it restarts: tools have side effects.

`startRun` is the same run over direct SSE from the browser. It stays for
tests and for a page without the object.

## A page with the Durable Object

1. **List**: `useQuery(threadListQueryOptions(orgId))`; `createThread(orgId)`
   makes an empty thread to open.
2. **Open**: seed `useAgentChat` with
   `reconcileChatMessages(await getThreadChatMessages(org, thread), (await getThread(org, thread)).items)`.
   Keep `threadDetailQueryOptions` for `pending_confirmations`, and reconcile
   again when it refetches while nothing runs: Go can add app updates or team
   work outside the socket.
3. **Render**: `buildTimeline(chatMessagesToItems(chat.messages))`. Switch on
   `kind`; every entry has a unique, stable `key`. A message's `role` is
   `member`, `agent` or `apps` (a notice from the member's apps); a draft's
   `state` is `streaming` until its message replaces it, `interrupted` if its
   run ended first.
4. **Stream**: `chat.sendMessage({ text })`. A refusal comes back as a
   `run_failed` item. One with `code === sessionExpiredCode` means the socket's
   cookie expired before anything ran: refresh the session, `await
   reopenSocket(agent)`, regenerate, once. The timeline never shows it. A new
   thread's first message goes once the socket is open, not on mount:
   useChat's cleanup stops a request in flight, and React's development
   double mount runs that cleanup. A draft left `streaming` while nothing
   runs was cut short (Stop writes no end marker): show it as interrupted.
5. **Confirm**: `pendingConfirmationIds(thread.data, chatMessagesToItems(chat.messages))`
   lists what must be answered, and Go wants all of it in one run:
   `chat.sendMessage({ text: " ", metadata: { oryxaConfirmation: true } }, { body: { confirmations } })`.
6. **Artifacts**: `artifactContentUrl` for thumbnails rendered with the page;
   `fetchArtifactLink` for a click that may come after the session expired
   (it refreshes, and reports a missing file in the page).
7. **Delete**: `deleteThreadChat`, so the object's transcript goes too, then
   invalidate `threadKeys.all(org)`. After any run, invalidate
   `threadKeys.detail` and `threadKeys.list` (title, order).

With direct SSE, collect `onItem` items in `live` and render
`buildTimeline(detail.items, live)`: what the refetched history already holds
shows once. `startRun` throws `ApiError` with Go's code when a run is refused
(`run_in_progress`, `confirmation_pending`, `agents_unavailable`, ...) and
otherwise resolves how it ended: `completed`, `failed` (Go's words),
`aborted` (your signal; Go stops the run), or `incomplete`.

## The specialist chat, archived

`SpecialistChatAgent` (one object per specialist, named
`specialistAgentName(org, agent)`, holding the chat's current Go conversation;
REALTIME.md says why it is not one per conversation) has no page now: the
specialist screens and their client were archived on 2026-09-30, with the
rules for reading its transcript, reconciling it with Go's and starting it
over. They are in the git tag `archive/pre-simplify-2026-09-30`. The object, its Worker route and the wire
types in `lib/specialists` stay.

## What the Worker and the Durable Object do

`src/server.ts` and `src/thread-chat-agent.ts`, ported from `web/` with a few
changes. The Worker routes `/agents/thread-chat-agent/{org}~{thread}` (the
socket) and `.../get-messages` only for names `parseThreadAgentName` accepts
as they arrive (a percent-encoded name is refused), only for a same-origin
socket, and only after Go's `GET .../threads/{thread}` accepts the member's
cookie; it sets `X-Oryxa-Cookie`, `X-Oryxa-Organization` and
`X-Oryxa-Thread` itself. A refusal is JSON in Go's shape, so `ApiError`
carries its code: Go's 401, 403, 404 and 503 pass through, other failures
answer 502. `DELETE` forwards Go's `DELETE` first and clears the transcript
only once Go has deleted the thread.

The object streams each Go item as a `data-oryxa` part except the member's
echoed message; writes `{ type: "confirmation", call_id, status }` per answer
once Go accepts the run; turns Go's 401 into a `run_failed` with
`sessionExpiredCode`, and any other refusal into a `run_failed` in words for
the member with Go's code; and stores with `withoutSupersededDeltas`. It reads
Go with `readItemStream`, so a turn ends at `run_completed` although Go keeps
the response open up to 15 seconds after it while it writes memory, and a
stream that breaks before its last item ends with a `run_failed`.

`SpecialistChatAgent` (`src/specialist-chat-agent.ts`) is the same, through
the same Worker checks and the same relay (`src/chat-relay.ts`), with these
differences: the Worker asks Go's `GET .../agents/{agent}` and sets
`X-Oryxa-Agent` instead of `X-Oryxa-Thread`; its runs POST
`.../agents/{agent}/runs` with the `thread_id` it keeps; its deltas become
one progress marker per draft and a specialist's work result becomes
readable; and its `DELETE` clears the object alone, since Go keeps
specialist conversations.

## Go facts worth knowing

- One run per thread at a time; a message while confirmations wait is
  refused; every waiting confirmation is answered in one run.
- A `tool_result` never carries content. Go leaves `hint` empty when ADK had
  only its developer-facing default (`confirmationHint` writes words).
- `author` is the agent's display name, and sub-agents the primary agent
  hands the turn to (`planner`, `researcher`) write under their own names.
- Specialist conversations (hosted, not used by the page) have no delete
  route and no artifact route in Go, and no create route either: the first
  run allocates one.
