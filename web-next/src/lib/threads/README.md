# Conversations data layer

Primary-agent threads (`lib/threads`) and specialist conversations
(`lib/specialists`). No UI: typed calls, stream parsing and pure timeline
logic. The Go contract is `agents/primary/internal/httpserver/server.go` and
`agents/primary/internal/threads`; every exported call names its handler.

| Module | What it offers |
| --- | --- |
| `client.ts` | Go's types (`Thread`, `ThreadDetail`, `Item`, `RunInput`, `ConfirmationAnswer`, `ThreadErrorCode`); `listThreads`, `createThread`, `getThread`, `deleteThread`; the Durable Object's HTTP side (`deleteThreadChat`, `getThreadChatMessages`); `startRun` (direct SSE); `artifactContentUrl`, `fetchArtifactLink`; `threadKeys` and `thread*QueryOptions` |
| `event-stream.ts` | `createEventStreamReader` (incremental SSE parser for a POST's body) and `readItemStream`, the one read loop for the browser and the Durable Object |
| `timeline.ts` | `buildTimeline`, `unseenLiveItems`, `pendingConfirmationIds`, and small naming helpers |
| `chat-messages.ts` | The Durable Object's transcript: `ThreadChatMessage`, `threadAgentName`, `itemsToChatMessages`, `chatMessagesToItems`, `reconcileChatMessages`, `withoutSupersededDeltas`, `sessionExpiredCode` |
| `agent-socket.ts` | `reopenSocket`, to carry a refreshed session to the Durable Object |
| `../specialists` | `listSpecialistThreads`, `getSpecialistThread`, `startSpecialistRun` (direct SSE, `X-Oryxa-Thread`), keys and query options; `parseSpecialistResult` for a specialist's JSON answer |

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
tests and for a page without the object; specialists always stream this way.

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
   reopenSocket(agent)`, regenerate, once. The timeline never shows it.
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

## What the Durable Object must do for these helpers

As `web/src/thread-chat-agent.ts` and `web/src/server.ts` do: route
`/agents/thread-chat-agent/{org}~{thread}` only for names
`parseThreadAgentName` accepts, after checking the cookie with Go's
`GET .../threads/{thread}`; purge the transcript on `DELETE`, then forward
Go's `DELETE`; serve `get-messages`. Stream each Go item as a `data-oryxa`
part except the member's echoed message; write `{ type: "confirmation",
call_id, status }` per answer once Go accepts the run; turn Go's 401 into a
`run_failed` with `sessionExpiredCode`; store with `withoutSupersededDeltas`.
Read Go with `readItemStream`, which returns at `run_completed`: Go keeps the
response open up to 15 seconds after it while it writes memory.

## Go facts worth knowing

- One run per thread at a time; a message while confirmations wait is
  refused; every waiting confirmation is answered in one run.
- A `tool_result` never carries content. Go leaves `hint` empty when ADK had
  only its developer-facing default (`confirmationHint` writes words).
- `author` is the agent's display name, and sub-agents the primary agent
  hands the turn to (`planner`, `researcher`) write under their own names.
- Specialist conversations have no delete route and no artifact route in Go.
