/** The part of the chat socket (the Agents SDK's `useAgent` result) that reopening it needs. */
export type ReopenableSocket = {
  addEventListener: (type: "open", listener: () => void) => void
  removeEventListener: (type: "open", listener: () => void) => void
  reconnect: () => void
}

/** How long a reopened socket may take before the page gives up on it. */
export const reopenTimeoutMs = 10_000

/**
 * Closes the chat socket and opens a new one, resolving once it is open. The
 * Durable Object keeps the cookie a socket was opened with, so a new socket is
 * how a refreshed session reaches it: after a `run_failed` with
 * `sessionExpiredCode`, refresh the session, `await reopenSocket(agent)`, then
 * send again. Rejects if the socket is not open within `timeoutMs`, or if
 * `reconnect` throws.
 */
export function reopenSocket(
  socket: ReopenableSocket,
  timeoutMs = reopenTimeoutMs
): Promise<void> {
  return new Promise((resolve, reject) => {
    const stop = () => {
      clearTimeout(timer)
      socket.removeEventListener("open", opened)
    }
    function opened() {
      stop()
      resolve()
    }
    const timer = setTimeout(() => {
      stop()
      reject(new Error("Could not reconnect to your primary agent. Try again."))
    }, timeoutMs)
    socket.addEventListener("open", opened)
    try {
      socket.reconnect()
    } catch (error) {
      stop()
      reject(error instanceof Error ? error : new Error(String(error)))
    }
  })
}
