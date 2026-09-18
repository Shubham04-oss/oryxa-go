import { describe, expect, it } from "vitest";
import { createEventStreamReader, type ServerSentEvent } from "./event-stream";

const encoder = new TextEncoder();

function bytes(text: string): Uint8Array {
	return encoder.encode(text);
}

/** Feeds `chunks` (already-encoded pieces) through a fresh reader and collects every event, `end()` included. */
function readAll(chunks: Uint8Array[]): ServerSentEvent[] {
	const reader = createEventStreamReader();
	const events: ServerSentEvent[] = [];
	for (const chunk of chunks) {
		events.push(...reader.push(chunk));
	}
	events.push(...reader.end());
	return events;
}

describe("createEventStreamReader", () => {
	it("parses one event terminated by a blank line", () => {
		const events = readAll([
			bytes('event: message\ndata: {"type":"message"}\n\n'),
		]);
		expect(events).toEqual([{ data: '{"type":"message"}', event: "message" }]);
	});

	it("joins several data lines with \\n", () => {
		const events = readAll([
			bytes("event: message\ndata: line one\ndata: line two\n\n"),
		]);
		expect(events).toEqual([{ data: "line one\nline two", event: "message" }]);
	});

	it("accepts \\r\\n line endings", () => {
		const events = readAll([
			bytes('event: run_started\r\ndata: {"type":"run_started"}\r\n\r\n'),
		]);
		expect(events).toEqual([
			{ data: '{"type":"run_started"}', event: "run_started" },
		]);
	});

	it("ignores keep-alive comment lines wherever they appear", () => {
		const events = readAll([
			bytes(
				": keep-alive\n\nevent: message\n: keep-alive\ndata: hello\n\n: keep-alive\n\n",
			),
		]);
		expect(events).toEqual([{ data: "hello", event: "message" }]);
	});

	it("parses several events delivered in one chunk", () => {
		const events = readAll([
			bytes("event: a\ndata: 1\n\nevent: b\ndata: 2\n\n"),
		]);
		expect(events).toEqual([
			{ data: "1", event: "a" },
			{ data: "2", event: "b" },
		]);
	});

	it("reassembles an event split across chunks mid-line", () => {
		const whole = 'event: message\ndata: {"text":"hel';
		const rest = 'lo"}\n\n';
		const events = readAll([bytes(whole), bytes(rest)]);
		expect(events).toEqual([{ data: '{"text":"hello"}', event: "message" }]);
	});

	it("reassembles a line split at every possible byte offset", () => {
		const whole = 'event: message\ndata: {"text":"hello world"}\n\n';
		const encoded = bytes(whole);
		for (let cut = 1; cut < encoded.length; cut += 1) {
			const events = readAll([encoded.slice(0, cut), encoded.slice(cut)]);
			expect(events).toEqual([
				{ data: '{"text":"hello world"}', event: "message" },
			]);
		}
	});

	it("does not mistake a chunk boundary inside \\r\\n for a blank line", () => {
		// The first chunk ends right after the "\r" half of "data: hello\r\n".
		const events = readAll([
			bytes("data: hello\r"),
			bytes("\ndata: world\n\n"),
		]);
		expect(events).toEqual([{ data: "hello\nworld", event: "" }]);
	});

	it("treats a trailing lone \\r as its own line ending once the stream ends", () => {
		const reader = createEventStreamReader();
		expect(reader.push(bytes("data: hello\r"))).toEqual([]);
		expect(reader.end()).toEqual([{ data: "hello", event: "" }]);
	});

	it("reassembles a multi-byte UTF-8 character split across chunks", () => {
		// "🙂" encodes as four UTF-8 bytes (F0 9F 99 82); 0x9F is a
		// continuation byte, not a lead byte, so cutting right before it
		// splits the character after its first byte.
		const encoded = bytes('event: message\ndata: {"text":"hi 🙂"}\n\n');
		const cut = encoded.indexOf(0x9f);
		expect(cut).toBeGreaterThan(0);
		const events = readAll([encoded.slice(0, cut), encoded.slice(cut)]);
		expect(events).toEqual([{ data: '{"text":"hi 🙂"}', event: "message" }]);
	});

	it("dispatches a final event with no trailing blank line only at end()", () => {
		const reader = createEventStreamReader();
		expect(
			reader.push(
				bytes('event: run_completed\ndata: {"type":"run_completed"}'),
			),
		).toEqual([]);
		expect(reader.end()).toEqual([
			{ data: '{"type":"run_completed"}', event: "run_completed" },
		]);
	});

	it("dispatches a final event whose last data line does end in a newline", () => {
		const reader = createEventStreamReader();
		expect(reader.push(bytes("event: message\ndata: last\n"))).toEqual([]);
		expect(reader.end()).toEqual([{ data: "last", event: "message" }]);
	});

	it("emits nothing extra when the stream ends cleanly on a blank line", () => {
		const reader = createEventStreamReader();
		expect(reader.push(bytes("event: message\ndata: hi\n\n"))).toEqual([
			{ data: "hi", event: "message" },
		]);
		expect(reader.end()).toEqual([]);
	});

	it("mixes \\n and \\r\\n endings within the same stream", () => {
		const events = readAll([
			bytes("event: a\r\ndata: 1\r\n\r\nevent: b\ndata: 2\n\n"),
		]);
		expect(events).toEqual([
			{ data: "1", event: "a" },
			{ data: "2", event: "b" },
		]);
	});
});
