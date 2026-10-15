// "Pick up where you left off" on My workspace (WorkspaceV3): four equal
// cards of the member's most recent work, from real reads only: files their
// agent saved, their titled chats and their Library collections. Fewer than
// four leaves the rest of the row empty; none says so in one line.
import type { ReactNode } from "react"
import { BookOpen, MessageSquare } from "lucide-react"
import { ThinkingOrb } from "thinking-orbs"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { FileKindBadge } from "./file-kind"
import type { PickupItem } from "./today-format"

export function HomePickup({
  items,
  ready,
  onOpenConversation,
  onOpenKnowledge,
  onOpenAgent,
  onNewChat,
  notices,
}: {
  items: PickupItem[]
  /** Every read the row draws from has answered: an empty row can say so. */
  ready: boolean
  onOpenConversation?: (threadId: string) => void
  onOpenKnowledge: () => void
  onOpenAgent: () => void
  onNewChat?: () => void
  /** The reads' own loading and error lines. */
  notices?: ReactNode
}) {
  return (
    <section className="home-pickup" aria-labelledby="home-pickup-title">
      <header className="home-section-head">
        <h2 id="home-pickup-title">Pick up where you left off</h2>
        <span className="home-grow" />
        <Button
          variant="ghost"
          size="sm"
          disabled={!onNewChat}
          onClick={onNewChat}
        >
          New chat
        </Button>
        <Button variant="ghost" size="sm" onClick={onOpenAgent}>
          All conversations
        </Button>
        <Button
          variant="ghost"
          size="sm"
          aria-label="Open Library"
          onClick={onOpenKnowledge}
        >
          Library
        </Button>
      </header>
      {notices}
      {items.length > 0 ? (
        <ul className="home-pickup-cards">
          {items.map((item) => {
            const open =
              item.kind === "knowledge"
                ? onOpenKnowledge
                : item.threadId && onOpenConversation
                  ? () => onOpenConversation(item.threadId!)
                  : undefined
            const body = (
              <>
                <span className="home-pickup-thumb" aria-hidden="true">
                  {item.kind === "file" ? (
                    <FileKindBadge name={item.title} />
                  ) : item.kind === "chat" ? (
                    item.running ? (
                      <ThinkingOrb size={32} state="working" />
                    ) : (
                      <MessageSquare />
                    )
                  ) : (
                    <BookOpen />
                  )}
                </span>
                <span className="home-pickup-meta">
                  <b>{item.title}</b>
                  <small>{item.detail}</small>
                </span>
              </>
            )
            return (
              <li key={item.key}>
                <Card className="home-pickup-card" size="sm">
                  {item.threadId || item.kind === "knowledge" ? (
                    <button type="button" disabled={!open} onClick={open}>
                      {body}
                    </button>
                  ) : (
                    <div role="status">{body}</div>
                  )}
                </Card>
              </li>
            )
          })}
        </ul>
      ) : (
        ready && (
          <p className="home-quiet">
            Files, chats and collections you work on land here.
          </p>
        )
      )}
    </section>
  )
}
