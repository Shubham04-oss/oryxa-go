// One connected app's files in the Library: what knowledge indexed from it,
// read from the collection knowledge fills (the corpus with origin
// "knowledge") and filtered by each document's `metadata.provider`.
import { useInfiniteQuery } from "@tanstack/react-query"
import { Puzzle, RotateCw } from "lucide-react"
import { Button } from "@/components/ui/button"
import type { Member } from "@/lib/auth/invitations"
import {
  documentsInfiniteQueryOptions,
  isFromConnectedApps,
  ragErrorMessage,
} from "@/lib/rag/client"
import type { Corpus } from "@/lib/rag/client"
import { AppLogo } from "./app-logo"
import { filterFiles } from "./knowledge-base-detail"
import { countLabel } from "./knowledge-format"
import { LibraryFileTable } from "./library-file-table"
import type { LibraryFile } from "./library-file-table"
import type { LibrarySource } from "./library-rail"

function SourceHeader({
  source,
  onOpenTools,
}: {
  source: LibrarySource
  onOpenTools?: () => void
}) {
  const allPaused = source.paused === source.sources
  return (
    <div className="library-collection-head">
      <div className="library-collection-copy">
        <h2>
          <span className="library-rail-logo" aria-hidden="true">
            <AppLogo provider={source.provider} name={source.name} />
          </span>
          {source.name}
        </h2>
        <small>
          {countLabel(source.sources, "source")}
          {source.paused > 0 &&
            ` · ${allPaused ? "paused" : `${source.paused} paused`}`}
        </small>
        {allPaused && (
          <p>
            {source.needsReconnect
              ? `Your agents stopped reading ${source.name} until it's reconnected.`
              : `Your agents aren't reading ${source.name} right now.`}
          </p>
        )}
      </div>
      {onOpenTools && (
        <div className="library-collection-actions">
          <Button variant="outline" onClick={onOpenTools}>
            <Puzzle strokeWidth={1.7} />
            Manage in Tools
          </Button>
        </div>
      )}
    </div>
  )
}

export function LibrarySourceFiles({
  organizationId,
  source,
  corpora,
  members,
  filter,
  onOpenTools,
  onOpen,
}: {
  organizationId: string
  source: LibrarySource
  corpora: readonly Corpus[]
  members: readonly Member[]
  filter: string
  onOpenTools?: () => void
  onOpen?: (file: LibraryFile) => void
}) {
  const corpus = corpora.find(isFromConnectedApps)
  const documentsQuery = useInfiniteQuery({
    ...documentsInfiniteQueryOptions(organizationId, corpus?.id ?? ""),
    enabled: Boolean(corpus),
  })
  const documents =
    documentsQuery.data?.pages.flatMap((page) => page.documents) ?? []
  const fromSource = documents.filter(
    (document) =>
      document.metadata?.provider?.trim().toLowerCase() === source.provider
  )
  const files = filterFiles(
    fromSource.map((document) => ({ document })),
    filter
  )

  return (
    <div className="library-collection">
      <SourceHeader source={source} onOpenTools={onOpenTools} />
      {!corpus ? (
        <div className="workspace-knowledge-empty">
          <strong>Nothing from {source.name} is indexed yet</strong>
          <p>Files show up here as your connected apps sync.</p>
        </div>
      ) : documentsQuery.isPending ? (
        <p className="workspace-knowledge-loading" role="status">
          Loading files…
        </p>
      ) : documentsQuery.isError && !documents.length ? (
        <div className="workspace-knowledge-alert" role="alert">
          <span>
            {ragErrorMessage(
              documentsQuery.error,
              `Couldn’t load the files from ${source.name}.`
            )}
          </span>
          <Button
            variant="outline"
            size="sm"
            onClick={() => void documentsQuery.refetch()}
          >
            <RotateCw strokeWidth={1.8} />
            Try again
          </Button>
        </div>
      ) : (
        <>
          {files.length ? (
            <LibraryFileTable
              label={`Files from ${source.name}`}
              files={files}
              members={members}
              onOpen={
                onOpen && ((file) => onOpen({ ...file, collection: corpus }))
              }
            />
          ) : fromSource.length ? (
            <p className="workspace-knowledge-loading">
              No files here match “{filter.trim()}”.
            </p>
          ) : (
            <p className="workspace-knowledge-loading">
              {documentsQuery.hasNextPage
                ? `None of the latest files your apps indexed came from ${source.name}.`
                : `Nothing from ${source.name} is indexed yet.`}
            </p>
          )}
          {documentsQuery.hasNextPage && (
            <>
              <p className="workspace-knowledge-hint library-footnote">
                Showing what’s loaded so far from your connected apps.
              </p>
              <Button
                variant="outline"
                className="library-more"
                disabled={documentsQuery.isFetchingNextPage}
                onClick={() => void documentsQuery.fetchNextPage()}
              >
                {documentsQuery.isFetchingNextPage ? "Loading…" : "Load more"}
              </Button>
            </>
          )}
        </>
      )}
    </div>
  )
}
