// The Library's left pane: All files, then each collection (a rag corpus)
// with its colour and how many documents it holds, then the connected apps
// the member's agents read from (knowledge sources).
import { Plus } from "lucide-react"
import { Button } from "@/components/ui/button"
import type { Source } from "@/lib/knowledge/client"
import { connectedAppName } from "@/lib/rag/client"
import type { Corpus } from "@/lib/rag/client"
import { cn } from "@/lib/utils"
import { AppLogo } from "./app-logo"
import { collectionTone, libraryFileCount } from "./knowledge-format"

/** One connected app in the rail: every source of its provider, together. */
export type LibrarySource = {
  provider: string
  name: string
  /** How many of its sources there are, and how many are paused. */
  sources: number
  paused: number
  /** Whether a paused one waits on the member reconnecting the app. */
  needsReconnect: boolean
}

/** The member's sources as the rail lists them: one entry per provider. */
export function librarySources(sources: readonly Source[]): LibrarySource[] {
  const byProvider = new Map<string, LibrarySource>()
  for (const source of sources) {
    if (source.status === "removed") continue
    const provider = source.provider.toLowerCase()
    const entry = byProvider.get(provider) ?? {
      provider,
      name: connectedAppName(provider),
      sources: 0,
      paused: 0,
      needsReconnect: false,
    }
    entry.sources += 1
    if (source.status === "paused") {
      entry.paused += 1
      if (
        source.reason === "needs_reconnect" ||
        source.reason === "scope_required"
      ) {
        entry.needsReconnect = true
      }
    }
    byProvider.set(provider, entry)
  }
  return [...byProvider.values()].sort((a, b) => a.name.localeCompare(b.name))
}

/** The one quiet word a source gets in the rail, when it isn't running. */
function sourceHint(source: LibrarySource): string | undefined {
  if (source.paused < source.sources) return undefined
  return source.needsReconnect ? "Reconnect" : "Paused"
}

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
  sources = [],
  selectedId,
  selectedSource = null,
  onSelect,
  onSelectSource,
  onCreate,
  className,
}: {
  corpora: readonly Corpus[]
  /** The connected apps; none hides the group. */
  sources?: readonly LibrarySource[]
  /** The open collection; null is All files, unless a source is open. */
  selectedId: string | null
  /** The open source's provider. */
  selectedSource?: string | null
  onSelect: (corpus: Corpus | null) => void
  onSelectSource?: (provider: string) => void
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
            size="icon-sm"
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
          current={selectedId === null && selectedSource === null}
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
      {sources.length > 0 && onSelectSource && (
        <>
          <div className="library-rail-heading library-rail-heading-sources">
            <span>Sources</span>
          </div>
          <ul aria-label="Sources">
            {sources.map((source) => {
              const hint = sourceHint(source)
              return (
                <li key={source.provider}>
                  <Button
                    variant="ghost"
                    className="library-rail-item"
                    aria-current={
                      selectedSource === source.provider ? "page" : undefined
                    }
                    onClick={() => onSelectSource(source.provider)}
                  >
                    <span className="library-rail-logo" aria-hidden="true">
                      <AppLogo provider={source.provider} name={source.name} />
                    </span>
                    <span className="library-rail-label">{source.name}</span>
                    {hint && (
                      <>
                        {" "}
                        <span className="library-rail-hint">{hint}</span>
                      </>
                    )}
                  </Button>
                </li>
              )
            })}
          </ul>
        </>
      )}
    </nav>
  )
}
