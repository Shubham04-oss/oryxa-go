# Tool-group status investigation, 2026-09-30

[Finding 8](findings-2026-09-30.md) reports “12 tool calls · Failed” beside a
good answer, with no failing tool visible in Go logs. It supplies no thread/run
ID or captured tool-result item. The original incident's cause is **unconfirmed**.

The demonstrated bug is narrower: `responseError` treated a present empty or
whitespace-only error string as “the tool failed.” Both live SSE and restored
history therefore marked a successful-shaped result `failed: true`. The fix
treats those strings like an absent error. Nonempty and structured errors keep
their failure flag and bounded explanation. No frontend status rule changes.

## Status path and producer checks

- [`threads/items.go`](../../agents/primary/internal/threads/items.go), lines
  202–220 and 382–404: canonical function responses become tool-result items;
  `responseError` controls their `failed` and `error` fields.
- [`timeline.ts`](../../web-next/src/lib/threads/timeline.ts), lines 286–313,
  and [`agent-timeline.tsx`](../../web-next/src/components/agent-timeline.tsx),
  lines 78–87 and 212–223: tool-result flags become call states; waiting,
  running, interrupted, then failed take precedence in a group. A good final
  answer does not erase a real tool failure.
- [`apps/internal/httpserver/server.go`](../../apps/internal/httpserver/server.go),
  lines 387–398: `RunView` omits an empty action error. The pinned ADK MCP adapter
  (`google.golang.org/adk/v2@v2.4.0/tool/mcptoolset/tool.go`, lines 130–154)
  wraps successful content under `output` and turns MCP `IsError` into a Go
  error. No current Apps success producer of a top-level blank error was found.
- [`agents/agent_system.go`](../../agents/primary/internal/agents/agent_system.go),
  lines 122–130, omits an empty delegation error. ADK's function-tool conversion
  (`google.golang.org/adk/v2@v2.4.0/internal/typeutil/convert.go`, lines 27–52)
  marshals through JSON, preserving that omission. Sandbox success uses
  `is_error: false`, not a top-level `error` field.
- ADK catches ordinary tool errors and creates `{"error": err.Error()}`
  (`google.golang.org/adk/v2@v2.4.0/internal/llminternal/base_flow.go`, lines
  1205–1212, 1257–1273 and 1374–1418). The model can recover and produce a good
  answer while the run completes. Absence of a `run_failed` log consequently
  does not establish that every tool call succeeded.

## Other outcome categories

Existing pending-confirmation filtering and frontend decline handling remain
unchanged. Unanswered calls remain Interrupted when the run ends. The synthetic
interruption responses in
[`agents/dangling_calls.go`](../../agents/primary/internal/agents/dangling_calls.go)
repair the outgoing model request, rather than add stored tool-result events.

A separate semantic case exists in `agent_system_continue_assignment`:
[`agentsystem/service.go`](../../agents/primary/internal/agentsystem/service.go),
lines 264 and 372–375, can return `needs_input` or `blocked` with a nonempty
`SystemDelegationResult.Error` and a nil Go error. The generic tool classifier
would call that Failed. This patch does not infer a different outcome from that
text or suppress it. Attribution to finding 8 needs the flagged tool name/error
or a redacted canonical event before changing the outcome contract.

## Regression coverage

[`items_test.go`](../../agents/primary/internal/threads/items_test.go) reproduces
empty/whitespace errors in live and history conversion, round-trips twelve
successful calls through session JSON, preserves structured/bounded errors,
and runs an actual ADK recovery where a tool fails but the final answer and run
succeed. The chat regression in
[`agent-timeline.test.tsx`](../../web-next/src/components/agent-timeline.test.tsx)
checks twelve-call history, live, and reconciled rendering, keeping a genuine
failure visible beside the same good answer.
