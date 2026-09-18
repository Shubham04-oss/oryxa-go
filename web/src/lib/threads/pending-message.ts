// The home composer creates a thread before it exists, so its first message
// cannot be sent in the same request. This hands that draft to the
// conversation page that opens next, keyed by the new thread's id, and it is
// consumed exactly once — a re-render, a remount, or React StrictMode's extra
// invocation all find nothing left to send a second time.
const pendingMessages = new Map<string, string>();

export function stashPendingMessage(threadId: string, message: string): void {
	pendingMessages.set(threadId, message);
}

/** `threadId`'s stashed first message, if any, removing it so it is only ever taken once. */
export function takePendingMessage(threadId: string): string | undefined {
	const message = pendingMessages.get(threadId);
	pendingMessages.delete(threadId);
	return message;
}
