import { useEffect, useState } from "react"
import type { FormEvent } from "react"
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import type { InfiniteData, UseQueryResult } from "@tanstack/react-query"
import { BookOpenText, Plus, RotateCw, Search } from "lucide-react"
import { z } from "zod"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Textarea } from "@/components/ui/textarea"
import { ApiError } from "@/lib/api/client"
import type { Role } from "@/lib/auth/client"
import type { Member } from "@/lib/auth/invitations"
import type { ScreenItem } from "@/lib/agents/screen-context"
import { membersQueryOptions } from "@/lib/auth/queries"
import { sourcesQueryOptions } from "@/lib/knowledge/client"
import {
  afterCreateCorpus,
  canCreateOrganizationCorpus,
  capabilitiesQueryOptions,
  corporaQueryOptions,
  createCorpus,
  documentQueryOptions,
  documentsPageSize,
  documentsPollInterval,
  listDocuments,
  ragErrorMessage,
  ragKeys,
} from "@/lib/rag/client"
import type {
  Corpus,
  CorpusDocument,
  CorpusScope,
  CreateCorpusInput,
  DocumentPage,
  RagViewer,
} from "@/lib/rag/client"
import {
  AddFilesDialog,
  canAddToCollection,
  filterFiles,
  KnowledgeBaseDetail,
} from "./knowledge-base-detail"
import type { CitedPassage, ContentKind } from "./knowledge-base-detail"
import {
  documentTitle,
  libraryFileCount,
  librarySummary,
  parseUpdated,
} from "./knowledge-format"
import { LibraryFileTable } from "./library-file-table"
import { LibraryFileViewer } from "./library-file-viewer"
import type { OpenLibraryFile } from "./library-file-viewer"
import type { LibraryFile } from "./library-file-table"
import { LibraryRail, librarySources } from "./library-rail"
import { LibrarySourceFiles } from "./library-source-files"
import "./library.css"

/** The collection open in the Library; null is All files. */
export type SelectedKnowledgeBase = { id: string; name: string }

const newBaseSchema = z.object({
  name: z
    .string()
    .trim()
    .min(2, "Use at least 2 characters.")
    .max(60, "Use at most 60 characters."),
  description: z.string().trim().max(200, "Use at most 200 characters."),
})

function createError(error: unknown): string {
  if (error instanceof ApiError && error.status === 409) {
    return "A collection with a very similar name already exists here. Try another name."
  }
  return ragErrorMessage(error, "Could not create this collection. Try again.")
}

/**
 * The header's title for the Library: its name, and how many files it holds
 * from how many sources once both are known.
 */
export function LibraryHeading({
  organizationId,
}: {
  organizationId?: string
}) {
  const corpora = useQuery({
    ...corporaQueryOptions(organizationId ?? ""),
    enabled: Boolean(organizationId),
  })
  const sources = useQuery({
    ...sourcesQueryOptions(organizationId ?? ""),
    enabled: Boolean(organizationId),
  })
  return (
    <div className="workspace-header-detail library-heading">
      <div>
        <h1>Library</h1>
        {corpora.data && (
          <small>
            {librarySummary(
              libraryFileCount(corpora.data),
              sources.data?.length
            )}
          </small>
        )}
      </div>
    </div>
  )
}

/** A new collection: a short name, a description and who can use it. */
function CreateCollectionDialog({
  organizationId,
  organizationName,
  viewer,
  onCreated,
  onClose,
}: {
  organizationId: string
  organizationName?: string
  viewer: RagViewer
  onCreated: (corpus: Corpus) => void
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [scope, setScope] = useState<CorpusScope>("member")
  const [nameError, setNameError] = useState("")
  const [formError, setFormError] = useState("")
  const canShare = canCreateOrganizationCorpus(viewer)

  const create = useMutation({
    mutationFn: (input: CreateCorpusInput) =>
      createCorpus(organizationId, input),
    // A request that is out cannot be recalled, so the collection exists once
    // it lands whether or not the dialog still waits for it, and the rail has
    // to know. What the dialog does with it (`submit`) is another matter.
    onSuccess: (created) => {
      void afterCreateCorpus(queryClient, organizationId, created)
    },
  })

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const result = newBaseSchema.safeParse({ name, description })
    if (!result.success) {
      const issue = (field: string) =>
        result.error.issues.find((item) => item.path[0] === field)?.message
      setNameError(issue("name") ?? "")
      setFormError(issue("description") ?? "")
      return
    }
    setFormError("")
    // These run only while the dialog still waits for this request. Cancel
    // closes it, which drops them: a collection made after that is not
    // opened, and its failure is not shown in a dialog opened since.
    create.mutate(
      {
        name: result.data.name,
        description: result.data.description,
        scope: canShare ? scope : "member",
      },
      {
        onSuccess: onCreated,
        onError: (error) => setFormError(createError(error)),
      }
    )
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="library-dialog">
        <DialogHeader>
          <DialogTitle>New collection</DialogTitle>
          <DialogDescription>
            Name the collection, then add what your agents should know.
          </DialogDescription>
        </DialogHeader>
        <form
          className="workspace-knowledge-form library-dialog-form"
          onSubmit={submit}
          noValidate
        >
          <label htmlFor="knowledge-base-name">Short name</label>
          <Input
            id="knowledge-base-name"
            value={name}
            onChange={(event) => {
              setName(event.target.value)
              setNameError("")
            }}
            aria-invalid={Boolean(nameError)}
            aria-describedby={
              nameError
                ? "knowledge-base-name-hint knowledge-base-name-error"
                : "knowledge-base-name-hint"
            }
            placeholder="e.g. Engineering handbook"
            maxLength={60}
            disabled={create.isPending}
          />
          <p className="workspace-knowledge-hint" id="knowledge-base-name-hint">
            Short label, 2–60 characters. Add facts and notes next.
          </p>
          {nameError && (
            <p
              className="workspace-knowledge-error"
              id="knowledge-base-name-error"
            >
              {nameError}
            </p>
          )}
          <label htmlFor="knowledge-base-description">Description</label>
          <Textarea
            id="knowledge-base-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            placeholder="What belongs here?"
            maxLength={200}
            rows={2}
            disabled={create.isPending}
          />
          {canShare ? (
            <fieldset
              className="workspace-knowledge-choice"
              disabled={create.isPending}
            >
              <legend>Who can use it</legend>
              <label>
                <input
                  type="radio"
                  name="knowledge-base-scope"
                  value="member"
                  checked={scope === "member"}
                  onChange={() => setScope("member")}
                />
                Just you
              </label>
              <label>
                <input
                  type="radio"
                  name="knowledge-base-scope"
                  value="organization"
                  checked={scope === "organization"}
                  onChange={() => setScope("organization")}
                />
                Everyone in {organizationName || "this workspace"}
              </label>
            </fieldset>
          ) : null}
          <p className="workspace-knowledge-hint">
            {canShare && scope === "organization"
              ? "Everyone in the workspace can search it. Only admins can change what’s in it."
              : "Only you can see it, along with your workspace’s admins."}
          </p>
          {formError && (
            <p className="workspace-knowledge-error" role="alert">
              {formError}
            </p>
          )}
          <div className="library-actions">
            <Button variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending ? "Creating…" : "Create collection"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * Every collection's files in one table. There is no route that lists an
 * organization's documents, so this reads the first page of each collection
 * and says so when a collection holds more than that page.
 */
function AllFiles({
  organizationId,
  corpora,
  members,
  filter,
  canCreate,
  onCreate,
  onOpen,
}: {
  organizationId: string
  corpora: readonly Corpus[]
  members: readonly Member[]
  filter: string
  canCreate: boolean
  onCreate: () => void
  onOpen: (file: LibraryFile) => void
}) {
  const pages = useQueries({
    queries: corpora.map((corpus) => ({
      queryKey: [...ragKeys.documents(organizationId, corpus.id), "first"],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        listDocuments(organizationId, corpus.id, {
          limit: documentsPageSize,
          signal,
        }),
      refetchInterval: (query: { state: { data?: DocumentPage } }) =>
        documentsPollInterval(query.state.data ? [query.state.data] : []),
      refetchIntervalInBackground: false,
    })),
  })

  if (!corpora.length) {
    return (
      <div className="workspace-knowledge-empty workspace-empty-scene">
        <BookOpenText size={23} strokeWidth={1.5} />
        <strong>Your Library is empty</strong>
        <p>
          Make a collection, then add notes, links or text files for your agents
          to search.
        </p>
        {canCreate && (
          <Button variant="outline" onClick={onCreate}>
            <Plus strokeWidth={1.8} />
            New collection
          </Button>
        )}
      </div>
    )
  }

  const loaded: LibraryFile[] = []
  const failed: Corpus[] = []
  for (const [index, page] of pages.entries()) {
    const corpus = corpora[index]
    if (page.data) {
      for (const document of page.data.documents) {
        loaded.push({ document, collection: corpus })
      }
    } else if (page.isError) failed.push(corpus)
  }
  const pending = pages.some((page) => page.isPending)
  const truncated = pages.some((page) => Boolean(page.data?.next_cursor))
  loaded.sort((a, b) => parseUpdated(b.document) - parseUpdated(a.document))
  const files = filterFiles(loaded, filter)

  return (
    <div className="library-collection">
      {failed.length > 0 && (
        <div className="workspace-knowledge-alert" role="alert">
          <span>
            Couldn’t load the files in{" "}
            {failed.map((item) => item.name).join(", ")}.
          </span>
          <Button
            variant="outline"
            size="sm"
            onClick={() =>
              pages.forEach((page) => page.isError && void page.refetch())
            }
          >
            <RotateCw strokeWidth={1.8} />
            Try again
          </Button>
        </div>
      )}
      {pending && !loaded.length ? (
        <div className="library-skeleton" role="status">
          <span className="sr-only">Loading files…</span>
          {[0, 1, 2, 3].map((item) => (
            <Skeleton key={item} className="library-skeleton-row" />
          ))}
        </div>
      ) : loaded.length ? (
        files.length ? (
          <>
            <LibraryFileTable
              label="All files"
              files={files}
              members={members}
              onOpen={onOpen}
            />
            {truncated && (
              <p className="workspace-knowledge-hint library-footnote">
                Showing the latest from each collection. Open a collection to
                see all of its files.
              </p>
            )}
          </>
        ) : (
          <p className="workspace-knowledge-loading">
            No files match “{filter.trim()}”.
          </p>
        )
      ) : failed.length ? null : (
        <div className="workspace-knowledge-empty">
          <BookOpenText size={23} strokeWidth={1.5} />
          <strong>No files yet</strong>
          <p>
            Open a collection and add notes, links or text files for your agents
            to search.
          </p>
        </div>
      )}
    </div>
  )
}

type AddTarget = { collectionId?: string }

function LibraryPage({
  organizationId,
  organizationName,
  viewer,
  corpora,
  answering,
  selected,
  creating,
  adding,
  onSelect,
  onCreate,
  onCreated,
  onCloseCreate,
  onAdd,
  onCloseAdd,
  onOpenTools,
  onOpenFileChange,
}: {
  organizationId: string
  organizationName?: string
  viewer: RagViewer
  corpora: Corpus[]
  answering?: boolean
  selected: SelectedKnowledgeBase | null
  creating: boolean
  adding: AddTarget | null
  onSelect: (base: SelectedKnowledgeBase | null) => void
  onCreate: () => void
  onCreated: (corpus: Corpus) => void
  onCloseCreate: () => void
  onAdd: (target: AddTarget) => void
  onCloseAdd: () => void
  onOpenTools?: () => void
  onOpenFileChange?: (file: ScreenItem | null) => void
}) {
  const queryClient = useQueryClient()
  const [filter, setFilter] = useState("")
  const [notice, setNotice] = useState("")
  // The connected app whose files are open, by provider; a collection or
  // All files clears it.
  const [sourceProvider, setSourceProvider] = useState<string | null>(null)
  // The file open in the viewer, over the whole page.
  const [openFile, setOpenFile] = useState<OpenLibraryFile | null>(null)
  const members = useQuery(membersQueryOptions(organizationId))
  const sourcesQuery = useQuery(sourcesQueryOptions(organizationId))
  const sources = librarySources(sourcesQuery.data ?? [])
  const source = selected
    ? undefined
    : sources.find((item) => item.provider === sourceProvider)
  const writable = corpora.filter((corpus) =>
    canAddToCollection(corpus, viewer)
  )
  const fileCount = libraryFileCount(corpora)

  // A new place to look starts with no filter and no notice from the last.
  useEffect(() => {
    setFilter("")
    setNotice("")
    if (selected) setSourceProvider(null)
  }, [selected?.id, sourceProvider])

  // The shell tells the agent which file is open, and forgets it when the
  // Library goes.
  const openId = openFile?.document.id
  const openName = openFile ? documentTitle(openFile.document) : ""
  useEffect(() => {
    onOpenFileChange?.(
      openId ? { kind: "document", id: openId, name: openName } : null
    )
  }, [openId, openName, onOpenFileChange])
  useEffect(() => () => onOpenFileChange?.(null), [onOpenFileChange])

  // A cited passage opens in its file: the document from the loaded list,
  // or else read on its own.
  async function openPassage(
    collection: SelectedKnowledgeBase,
    passage: CitedPassage
  ) {
    setNotice("")
    const loaded = queryClient
      .getQueryData<InfiniteData<DocumentPage>>(
        ragKeys.documents(organizationId, collection.id)
      )
      ?.pages.flatMap((page) => page.documents)
      .find((item) => item.id === passage.documentId)
    try {
      const document =
        loaded ??
        (await queryClient.fetchQuery(
          documentQueryOptions(
            organizationId,
            collection.id,
            passage.documentId
          )
        ))
      setOpenFile({
        document,
        collection,
        passage: { start: passage.start, end: passage.end },
      })
    } catch {
      setNotice("Couldn’t open that passage’s file. It may have been removed.")
    }
  }

  function openAdding() {
    setNotice("")
    const current = selected
      ? writable.find((corpus) => corpus.id === selected.id)
      : undefined
    onAdd({
      collectionId:
        current?.id ?? (writable.length === 1 ? writable[0].id : undefined),
    })
  }

  function added(document: CorpusDocument, kind: ContentKind, corpus: Corpus) {
    const known = queryClient
      .getQueryData<InfiniteData<DocumentPage>>(
        ragKeys.documents(organizationId, corpus.id)
      )
      ?.pages.some((page) =>
        page.documents.some((item) => item.id === document.id)
      )
    onCloseAdd()
    setNotice(
      !known
        ? ""
        : kind === "note"
          ? "That note is already in this collection."
          : "That was already here. If its text changed, it’s being indexed again."
    )
  }

  if (openFile) {
    return (
      <div className="library-viewer">
        <LibraryFileViewer
          key={openFile.document.id}
          organizationId={organizationId}
          file={openFile}
          members={members.data ?? []}
          onBack={() => setOpenFile(null)}
          onOpenLibrary={() => {
            setOpenFile(null)
            setSourceProvider(null)
            onSelect(null)
          }}
          onOpenCollection={(collection) => {
            setOpenFile(null)
            setSourceProvider(null)
            onSelect({ id: collection.id, name: collection.name })
          }}
        />
      </div>
    )
  }

  return (
    <div className="library">
      <LibraryRail
        corpora={corpora}
        sources={sources}
        selectedId={selected?.id ?? null}
        selectedSource={source?.provider ?? null}
        onSelect={(corpus) => {
          setSourceProvider(null)
          onSelect(corpus ? { id: corpus.id, name: corpus.name } : null)
        }}
        onSelectSource={(provider) => {
          setSourceProvider(provider)
          if (selected) onSelect(null)
        }}
        onCreate={onCreate}
      />
      <section
        className="library-main"
        aria-label={selected?.name ?? source?.name ?? "All files"}
      >
        <div className="library-bar">
          <label className="library-search">
            <Search size={16} strokeWidth={1.7} aria-hidden="true" />
            <span className="sr-only">Search files</span>
            <Input
              type="search"
              value={filter}
              onChange={(event) => setFilter(event.target.value)}
              placeholder={
                selected
                  ? `Search ${selected.name}`
                  : source
                    ? `Search ${source.name}`
                    : `Search ${librarySummary(fileCount)}`
              }
            />
          </label>
          {writable.length > 0 && (
            <Button onClick={openAdding}>
              <Plus strokeWidth={1.8} />
              Add files
            </Button>
          )}
        </div>
        {notice && (
          <p className="workspace-knowledge-status-line" role="status">
            {notice}
          </p>
        )}
        {selected ? (
          <KnowledgeBaseDetail
            key={selected.id}
            organizationId={organizationId}
            corpusId={selected.id}
            name={selected.name}
            viewer={viewer}
            members={members.data ?? []}
            filter={filter}
            answering={answering}
            onAdd={() => {
              setNotice("")
              onAdd({ collectionId: selected.id })
            }}
            onClose={() => onSelect(null)}
            onOpenTools={onOpenTools}
            onOpen={(file) =>
              setOpenFile({
                document: file.document,
                collection: { id: selected.id, name: selected.name },
              })
            }
            onOpenPassage={(passage) => void openPassage(selected, passage)}
          />
        ) : source ? (
          <LibrarySourceFiles
            key={source.provider}
            organizationId={organizationId}
            source={source}
            corpora={corpora}
            members={members.data ?? []}
            filter={filter}
            onOpenTools={onOpenTools}
            onOpen={setOpenFile}
          />
        ) : (
          <AllFiles
            organizationId={organizationId}
            corpora={corpora}
            members={members.data ?? []}
            filter={filter}
            canCreate
            onCreate={onCreate}
            onOpen={setOpenFile}
          />
        )}
      </section>
      {creating && (
        <CreateCollectionDialog
          organizationId={organizationId}
          organizationName={organizationName}
          viewer={viewer}
          onCreated={onCreated}
          onClose={onCloseCreate}
        />
      )}
      {adding && (
        <AddFilesDialog
          organizationId={organizationId}
          collections={writable}
          collectionId={adding.collectionId}
          viewer={viewer}
          onChooseCollection={(collectionId) => onAdd({ collectionId })}
          onAdded={added}
          onClose={onCloseAdd}
        />
      )}
    </div>
  )
}

export function KnowledgeBases({
  organizationId,
  organizationName,
  organizationRole,
  userId,
  selected,
  onSelect,
  onOpenTools,
  onOpenFileChange,
}: {
  /** From the shell; without one there is nothing to read. */
  organizationId?: string
  organizationName?: string
  organizationRole?: Role
  userId?: string
  selected: SelectedKnowledgeBase | null
  onSelect: (base: SelectedKnowledgeBase | null) => void
  onOpenTools?: () => void
  /** Which file is open in the viewer, for the agent's screen context. */
  onOpenFileChange?: (file: ScreenItem | null) => void
}) {
  const viewer: RagViewer = { userId, role: organizationRole }
  const [creating, setCreating] = useState(false)
  const [adding, setAdding] = useState<AddTarget | null>(null)
  const capabilities = useQuery({
    ...capabilitiesQueryOptions(),
    enabled: Boolean(organizationId),
  })
  const corpora: UseQueryResult<Corpus[]> = useQuery({
    ...corporaQueryOptions(organizationId ?? ""),
    enabled: Boolean(organizationId),
  })
  // Rag answers its capabilities without a session; when neither read gets
  // through, the service itself is missing rather than the member's access.
  const unavailable =
    !organizationId || (corpora.isError && capabilities.isError)

  if (unavailable) {
    return (
      <div className="workspace-knowledge-page">
        <div className="workspace-knowledge-notice" role="status">
          {organizationId
            ? "Layout preview · The knowledge service isn’t reachable here, so there is no Library to show."
            : "Layout preview · Sign in to a workspace to see its Library."}
        </div>
      </div>
    )
  }

  if (corpora.isPending) {
    return (
      <div className="workspace-knowledge-page">
        <p className="sr-only" role="status">
          Loading the Library…
        </p>
        <div className="library" aria-hidden="true">
          <div className="library-rail">
            {[0, 1, 2].map((item) => (
              <Skeleton key={item} className="library-skeleton-row" />
            ))}
          </div>
          <div className="library-main">
            {[0, 1, 2].map((item) => (
              <Skeleton key={item} className="library-skeleton-row" />
            ))}
          </div>
        </div>
      </div>
    )
  }

  if (corpora.isError) {
    return (
      <div className="workspace-knowledge-page">
        <div className="workspace-knowledge-alert" role="alert">
          <span>
            {ragErrorMessage(corpora.error, "Couldn’t load your Library.")}
          </span>
          <Button
            variant="outline"
            size="sm"
            onClick={() => void corpora.refetch()}
          >
            <RotateCw strokeWidth={1.8} />
            Try again
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="workspace-knowledge-page library-page">
      <LibraryPage
        organizationId={organizationId}
        organizationName={organizationName}
        viewer={viewer}
        corpora={corpora.data}
        answering={capabilities.data?.answering}
        selected={selected}
        creating={creating}
        adding={adding}
        onSelect={(base) => {
          setAdding(null)
          onSelect(base)
        }}
        onCreate={() => setCreating(true)}
        onCreated={(created) => {
          setCreating(false)
          onSelect({ id: created.id, name: created.name })
          // Only the completed create opens adding by itself, on Note.
          if (canAddToCollection(created, viewer)) {
            setAdding({ collectionId: created.id })
          }
        }}
        onCloseCreate={() => setCreating(false)}
        onAdd={setAdding}
        onCloseAdd={() => setAdding(null)}
        onOpenTools={onOpenTools}
        onOpenFileChange={onOpenFileChange}
      />
    </div>
  )
}
