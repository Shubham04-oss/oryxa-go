/**
 * One `event: <type>\ndata: <payload>\n\n` record off the wire. `event` is ""
 * when the stream omitted the field; `data` joins every `data:` line with
 * "\n", per the SSE spec.
 */
export interface ServerSentEvent {
	data: string;
	event: string;
}

interface ParserState {
	buffer: string;
	dataLines: string[];
	eventType: string;
	hasField: boolean;
}

function freshState(): ParserState {
	return { buffer: "", dataLines: [], eventType: "", hasField: false };
}

function parseField(line: string): { field: string; value: string } {
	const colon = line.indexOf(":");
	if (colon === -1) {
		return { field: line, value: "" };
	}
	const value = line.slice(colon + 1);
	return {
		field: line.slice(0, colon),
		value: value.startsWith(" ") ? value.slice(1) : value,
	};
}

/**
 * One line off `state.buffer`, terminated by `\n`, `\r\n`, or a lone `\r`.
 * Returns undefined when no complete line is available yet — including when
 * the buffer ends in a bare `\r` that could still turn out to be the first
 * half of a `\r\n` split across a chunk boundary, unless `atEnd` says no more
 * bytes are coming.
 */
function takeLine(state: ParserState, atEnd: boolean): string | undefined {
	const { buffer } = state;
	const newline = buffer.indexOf("\n");
	const carriageReturn = buffer.indexOf("\r");

	if (carriageReturn !== -1 && (newline === -1 || carriageReturn < newline)) {
		if (carriageReturn === buffer.length - 1 && !atEnd) {
			return undefined;
		}
		const terminatorLength = buffer[carriageReturn + 1] === "\n" ? 2 : 1;
		state.buffer = buffer.slice(carriageReturn + terminatorLength);
		return buffer.slice(0, carriageReturn);
	}
	if (newline !== -1) {
		state.buffer = buffer.slice(newline + 1);
		return buffer.slice(0, newline);
	}
	return undefined;
}

function consumeLine(
	state: ParserState,
	line: string,
	events: ServerSentEvent[],
): void {
	if (line === "") {
		if (state.hasField) {
			events.push({ data: state.dataLines.join("\n"), event: state.eventType });
		}
		state.dataLines = [];
		state.eventType = "";
		state.hasField = false;
		return;
	}
	// A line starting with ":" is a comment (the stream's keep-alive), never a field.
	if (line.startsWith(":")) {
		return;
	}
	const { field, value } = parseField(line);
	state.hasField = true;
	if (field === "event") {
		state.eventType = value;
	} else if (field === "data") {
		state.dataLines.push(value);
	}
}

function drain(state: ParserState, atEnd: boolean): ServerSentEvent[] {
	const events: ServerSentEvent[] = [];
	for (;;) {
		const line = takeLine(state, atEnd);
		if (line === undefined) {
			break;
		}
		consumeLine(state, line, events);
	}
	// A final line with no trailing newline at all still counts, at end of stream.
	if (atEnd && state.buffer.length > 0) {
		const line = state.buffer;
		state.buffer = "";
		consumeLine(state, line, events);
	}
	// A final event with no trailing blank line still dispatches, at end of stream.
	if (atEnd && state.hasField) {
		events.push({ data: state.dataLines.join("\n"), event: state.eventType });
		state.dataLines = [];
		state.eventType = "";
		state.hasField = false;
	}
	return events;
}

/**
 * A pure, incremental parser for a `text/event-stream` body read as raw
 * bytes — the run stream is a POST's response, so `EventSource` cannot read
 * it; this is what a `ReadableStream` reader feeds instead. Call `push` for
 * every chunk as it arrives, in order, and `end` once after the stream
 * closes, since the last event may have no trailing blank line to signal it.
 *
 * Handles chunks split anywhere: mid-line, mid `\r\n`, or mid UTF-8
 * character (a `TextDecoder` in streaming mode absorbs that last one).
 */
export function createEventStreamReader(): {
	push(chunk: Uint8Array): ServerSentEvent[];
	end(): ServerSentEvent[];
} {
	const decoder = new TextDecoder();
	const state = freshState();

	return {
		end(): ServerSentEvent[] {
			state.buffer += decoder.decode();
			return drain(state, true);
		},
		push(chunk: Uint8Array): ServerSentEvent[] {
			state.buffer += decoder.decode(chunk, { stream: true });
			return drain(state, false);
		},
	};
}
