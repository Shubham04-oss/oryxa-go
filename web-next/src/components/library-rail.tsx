// The Library's left pane: All files, then each collection (a rag corpus)
// with its colour and how many documents it holds.
import { Plus } from "lucide-react"
import { Button } from "@/components/ui/button"
import type { Corpus } from "@/lib/rag/client"
import { cn } from "@/lib/utils"
import { collectionTone, libraryFileCount } from "./knowledge-format"

function RailItem({
  label,
  count,
  current,
  tone,
  collectionId,
  onSelect,
}: {
  label: string
  count: number
  current: boolean
  /** A collection's colour; All files has the muted dot. */
  tone?: number
  /** Set for a collection, so the agent knows what the member points at. */
  collectionId?: string
  onSelect: () => void
}) {
  return (
    <li>
      <Button
        variant="ghost"
        className="library-rail-item"
        aria-current={current ? "page" : undefined}
        data-screen-context-kind={collectionId ? "knowledge_base" : undefined}
        data-screen-context-id={collectionId}
        data-screen-context-name={collectionId ? label : undefined}
        onClick={onSelect}
      >
        <span
          className="library-rail-dot"
          data-tone={tone}
          aria-hidden="true"
        />
        <span className="library-rail-label">{label}</span>{" "}
        <span className="library-rail-count">
          {count.toLocaleString("en")}
          <span className="sr-only"> files</span>
        </span>
      </Button>
    </li>
  )
}

export function LibraryRail({
  corpora,
  selectedId,
  onSelect,
  onCreate,
  className,
}: {
  corpora: readonly Corpus[]
  /** The open collection; null is All files. */
  selectedId: string | null
  onSelect: (corpus: Corpus | null) => void
  /** Offered when the member can make a collection here. */
  onCreate?: () => void
  className?: string
}) {
  return (
    <nav className={cn("library-rail", className)} aria-label="Collections">
      <div className="library-rail-heading">
        <span>Collections</span>
        {onCreate && (
          <Button
            variant="ghost"
            size="icon-xs"
            className="library-rail-add"
            aria-label="New collection"
            title="New collection"
            onClick={onCreate}
          >
            <Plus strokeWidth={1.8} />
          </Button>
        )}
      </div>
      <ul>
        <RailItem
          label="All files"
          count={libraryFileCount(corpora)}
          current={selectedId === null}
          onSelect={() => onSelect(null)}
        />
        {corpora.map((corpus) => (
          <RailItem
            key={corpus.id}
            label={corpus.name}
            count={corpus.document_count}
            tone={collectionTone(corpus.id)}
            collectionId={corpus.id}
            current={selectedId === corpus.id}
            onSelect={() => onSelect(corpus)}
          />
        ))}
      </ul>
    </nav>
  )
}
