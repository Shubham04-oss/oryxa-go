import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { MessageMarkdown } from "./message-markdown";

function render(text: string): string {
	return renderToStaticMarkup(createElement(MessageMarkdown, { text }));
}

describe("MessageMarkdown", () => {
	it("renders what Krude writes as structure, not asterisks", () => {
		const html = render(
			"According to the **launch plan**:\n\n* Owner: Priya\n* Date: `14 Oct`",
		);

		expect(html).toContain("<strong>launch plan</strong>");
		expect(html).toContain("<li>Owner: Priya</li>");
		expect(html).toContain("<code>14 Oct</code>");
		expect(html).not.toContain("**");
	});

	it("renders tables", () => {
		const html = render("| City | Cap |\n| --- | --- |\n| Metro | 7,000 |");

		expect(html).toContain("<th>City</th>");
		expect(html).toContain("<td>7,000</td>");
	});

	it("shows HTML as text rather than running it", () => {
		const html = render(
			'Look: <img src=x onerror="alert(1)"> <script>alert(2)</script>',
		);

		expect(html).not.toContain("<img");
		expect(html).not.toContain("<script");
		expect(html).toContain("&lt;script&gt;");
	});

	it("opens links in a new tab and drops unsafe ones", () => {
		const html = render(
			"[docs](https://example.com/docs) [trap](javascript:alert(1))",
		);

		expect(html).toContain(
			'<a href="https://example.com/docs" rel="noopener noreferrer" target="_blank">docs</a>',
		);
		expect(html).not.toContain("javascript:");
	});
});
