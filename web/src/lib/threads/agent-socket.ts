/** The part of the chat socket that reopening it needs. */
export interface ReopenableSocket {
	addEventListener(type: "open", listener: () => void): void;
	removeEventListener(type: "open", listener: () => void): void;
	reconnect(): void;
}

/** How long a reopened socket may take before the page gives up on it. */
export const reopenTimeoutMs = 10_000;

/**
 * Closes the chat socket and opens a new one, resolving once it is open. The
 * Durable Object keeps the cookie a socket was opened with, so a new socket
 * is how a refreshed session reaches it.
 */
export function reopenSocket(
	socket: ReopenableSocket,
	timeoutMs = reopenTimeoutMs,
): Promise<void> {
	return new Promise((resolve, reject) => {
		const timer = setTimeout(() => {
			socket.removeEventListener("open", opened);
			reject(
				new Error("Could not reconnect to your primary agent. Try again."),
			);
		}, timeoutMs);
		function opened() {
			clearTimeout(timer);
			socket.removeEventListener("open", opened);
			resolve();
		}
		socket.addEventListener("open", opened);
		socket.reconnect();
	});
}
