import Markdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";

// Links leave the conversation for a new tab, and tell the page they open
// nothing about where they came from.
const components: Components = {
	a: ({ children, href }) => (
		<a href={href} rel="noopener noreferrer" target="_blank">
			{children}
		</a>
	),
};

const remarkPlugins = [remarkGfm];

/**
 * Agents answer in Markdown: lists, emphasis, code and tables. It renders as
 * React elements, never as HTML — HTML in an answer shows as the text it is,
 * and a link whose protocol is not a safe one (`javascript:` and the like)
 * loses its target — so what a model writes cannot run in the page.
 */
export function MessageMarkdown({ text }: { text: string }) {
	return (
		<div className="thread-markdown">
			<Markdown components={components} remarkPlugins={remarkPlugins}>
				{text}
			</Markdown>
		</div>
	);
}
