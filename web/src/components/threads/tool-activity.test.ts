import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { TimelineEntry, ToolEntry } from "../../lib/threads/timeline";
import { groupToolActivity, ToolActivity } from "./tool-activity";

function tool(callId: string, state: ToolEntry["state"] = "done"): ToolEntry {
	return {
		arguments: { query: callId },
		callId,
		kind: "tool",
		resultText: `result ${callId}`,
		state,
		tool: `tool_${callId}`,
	};
}

describe("tool activity", () => {
	it("groups only consecutive tool calls", () => {
		const message: TimelineEntry = {
			author: "krude",
			id: "message-1",
			kind: "message",
			pending: false,
			text: "Here is what I found.",
		};
		const grouped = groupToolActivity([
			tool("one"),
			tool("two"),
			message,
			tool("three"),
		]);

		expect(grouped).toHaveLength(3);
		expect(grouped[0]).toMatchObject({
			kind: "tool_activity",
			tools: [{ callId: "one" }, { callId: "two" }],
		});
		expect(grouped[1]).toBe(message);
		expect(grouped[2]).toMatchObject({
			kind: "tool_activity",
			tools: [{ callId: "three" }],
		});
	});

	it("renders one rotating tool reel without exposing technical details", () => {
		const html = renderToStaticMarkup(
			createElement(ToolActivity, {
				tools: [tool("one"), tool("two", "failed")],
			}),
		);

		expect(html).toContain("2 calls · Failed");
		expect(html).toContain("Tool one");
		expect(html).toContain("Tool two");
		expect(html.indexOf("Tool one")).toBeLessThan(html.indexOf("Tool two"));
		expect(html).toContain('data-slot="word-rotate"');
		expect(html).toContain('data-slot="word-rotate-active"');
		expect(html).toMatch(
			/data-slot="word-rotate-active"[^>]*>Tool two<\/span>/,
		);
		expect(html).not.toContain("<details");
		expect(html).not.toContain("result one");
		expect(html).not.toContain("&quot;query&quot;");
	});
});
