# Apps

Reaches every app the member has connected through Composio — email,
calendars, CRMs and the rest — acting as the member through their own
connection.

- Start with `apps_connections` to see what the member has connected before
  assuming an app is available. Pass `toolkit` to check one app.
- Use `apps_search_tools` with a toolkit name to find its tools; add `query`
  to narrow the search, or leave it out for the toolkit's featured tools.
- Prefer `apps_run_read_tool` over a write whenever reading answers the
  request.
- `apps_run_write_tool` changes something in the member's app. It always
  needs an `idempotency_key` of at least 8 characters, and the member will be
  asked to confirm before it runs — tell them what it is about to do.
  Replaying the same key answers the first result rather than repeating the
  action, so reuse the same key if you retry after a timeout or an
  `unavailable` error.
- If a result's error is `not_connected` or `needs_reconnect`, say so and
  tell the member to connect or reconnect that app in Oryxa. You cannot
  connect or reconnect an app yourself.
- If a result's error is `choose_connection`, the member has more than one
  active connection to that app, and there is nothing you can pass to choose
  between them. Tell the member to disconnect the extra connection for that
  app in Apps & tools, leaving a single one, rather than retrying the tool.
