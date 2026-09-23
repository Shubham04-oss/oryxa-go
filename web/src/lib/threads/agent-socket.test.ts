import { afterEach, describe, expect, it, vi } from "vitest";
import { reopenSocket } from "./agent-socket";

class FakeSocket extends EventTarget {
	reconnects = 0;

	reconnect() {
		this.reconnects += 1;
	}

	open() {
		this.dispatchEvent(new Event("open"));
	}
}

describe("reopenSocket", () => {
	afterEach(() => {
		vi.useRealTimers();
	});

	it("resolves once the new socket is open", async () => {
		const socket = new FakeSocket();
		const reopened = reopenSocket(socket);
		expect(socket.reconnects).toBe(1);
		socket.open();
		await expect(reopened).resolves.toBeUndefined();
	});

	it("gives up on a socket that does not open in time", async () => {
		vi.useFakeTimers();
		const socket = new FakeSocket();
		const reopened = reopenSocket(socket, 1000);
		const assertion = expect(reopened).rejects.toThrow("Could not reconnect");
		vi.advanceTimersByTime(1000);
		await assertion;
		// A late open after giving up is ignored rather than resolving anything.
		socket.open();
	});
});
