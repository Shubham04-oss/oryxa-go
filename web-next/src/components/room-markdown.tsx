// Loaded on demand by teamspace-room.tsx: the Markdown pipeline is most of
// the room's weight, and only an agent's answer needs it.
import Markdown from "react-markdown"
import type { Components } from "react-markdown"
import remarkGfm from "remark-gfm"

// Links leave the room for a new tab and tell the page they open nothing
// about where they came from. An image an agent writes is offered as a link
// rather than loaded, as in agent-timeline.tsx, so an answer cannot fetch
// from any server on its own: an `<img>` would tell that server the address
// of everyone who reads the room.
const components: Components = {
  a: ({ children, href }) => (
    <a href={href} rel="noopener noreferrer" target="_blank">
      {children}
    </a>
  ),
  img: ({ alt, src }) =>
    typeof src === "string" && src ? (
      <a href={src} rel="noopener noreferrer" target="_blank">
        {alt || src}
      </a>
    ) : null,
}
const remarkPlugins = [remarkGfm]

/**
 * Agents answer in Markdown. It renders as React elements, never as HTML,
 * and react-markdown drops link targets with unsafe protocols, so nothing an
 * agent writes can run in the page.
 */
export default function RoomMarkdown({ text }: { text: string }) {
  return (
    <div className="workspace-room-markdown">
      <Markdown components={components} remarkPlugins={remarkPlugins}>
        {text}
      </Markdown>
    </div>
  )
}
