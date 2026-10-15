import { useId, useState } from "react"
import type { FormEvent } from "react"
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import type { InfiniteData } from "@tanstack/react-query"
import {
  ArrowUp,
  BookOpenText,
  FileText,
  Link2,
  LoaderCircle,
  Lock,
  MoreHorizontal,
  Plus,
  Puzzle,
  RotateCw,
  StickyNote,
  Trash2,
  X,
} from "lucide-react"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { ApiError } from "@/lib/api/client"
import type { Member } from "@/lib/auth/invitations"
import { uploadProblem } from "@/lib/files/client"
import {
  addDocument,
  addFileDocument,
  afterDeleteCorpus,
  afterDocumentsChange,
  answerCorpus,
  canDeleteCorpus,
  canSetAdminsOnlyAccess,
  canWriteCorpus,
  corpusDescription,
  corpusQueryOptions,
  deleteCorpus,
  deleteDocument,
  documentsInfiniteQueryOptions,
  externalSourceLink,
  indexableContentType,
  indexableFileAccept,
  isFromConnectedApps,
  isIndexing,
  maxDocumentBytes,
  maxDocumentTitleLength,
  maxQueryLength,
  maxSourceRefLength,
  ragErrorMessage,
  ragKeys,
  searchCorpus,
} from "@/lib/rag/client"
import type {
  Answered,
  Corpus,
  CorpusDocument,
  DocumentAccess,
  DocumentPage,
  RagViewer,
  SearchResults,
} from "@/lib/rag/client"
import {
  citationProvenance,
  citationSourceLabel,
  collectionTone,
  corpusScopeLabel,
  corpusTimeLabel,
  countLabel,
  documentTitle,
} from "./knowledge-format"
import { AppLogo } from "./app-logo"
import { LibraryFileTable } from "./library-file-table"

/** A citation's provenance (`knowledge-format.ts`'s `citationProvenance`), as
 * small muted chips one line under its title; nothing when there is none. */
function CitationProvenance({
  metadata,
}: {
  metadata: Partial<Record<string, string>> | undefined
}) {
  const chips = citationProvenance(metadata)
  if (!chips.length) return null
  return (
    <span className="workspace-knowledge-citation-provenance">
      {chips.map((chip) => (
        <span className="workspace-knowledge-chip" key={chip.key}>
          {chip.provider && (
            <span className="workspace-knowledge-chip-icon" aria-hidden="true">
              <AppLogo provider={chip.provider} name={chip.label} />
            </span>
          )}
          {chip.label}
        </span>
      ))}
    </span>
  )
}

export type ContentKind = "note" | "link" | "file"

const contentKinds: Array<{
  kind: ContentKind
  label: string
  icon: typeof StickyNote
}> = [
  { kind: "note", label: "Note", icon: StickyNote },
  { kind: "link", label: "Link", icon: Link2 },
  { kind: "file", label: "File", icon: FileText },
]

type ContentErrors = Partial<Record<"text" | "url" | "file", string>>

function isWebAddress(value: string): boolean {
  try {
    const url = new URL(value)
    return (url.protocol === "http:" || url.protocol === "https:") && !!url.host
  } catch {
    return false
  }
}

function contentErrors(
  kind: ContentKind,
  { text, url, file }: { text: string; url: string; file: File | null }
): ContentErrors {
  const errors: ContentErrors = {}
  if (kind !== "file") {
    if (!text.trim()) {
      errors.text =
        kind === "link"
          ? "Paste the page’s text: that is what gets searched."
          : "Write or paste the note’s text."
    } else if (new TextEncoder().encode(text).length > maxDocumentBytes) {
      errors.text = "That’s more than one document can hold (16 MB)."
    }
  }
  if (kind === "link") {
    const address = url.trim()
    if (!address) errors.url = "Add the page’s address."
    else if (address.length > maxSourceRefLength)
      errors.url = "Use a shorter address."
    else if (!isWebAddress(address))
      errors.url = "Use a full web address, starting with https://."
  }
  if (kind === "file") {
    const problem = !file
      ? "Choose a file."
      : (uploadProblem(file) ??
        (indexableContentType(file)
          ? undefined
          : "The Library takes text: a .txt, .md, .html, .csv or .json file. Convert PDFs and Office files to text first."))
    if (problem) errors.file = problem
  }
  return errors
}

export function AddContentForm({
  organizationId,
  corpus,
  viewer,
  onAdded,
  onCancel,
}: {
  organizationId: string
  corpus: Corpus
  viewer: RagViewer
  onAdded: (document: CorpusDocument, kind: ContentKind) => void
  onCancel: () => void
}) {
  const id = useId()
  const queryClient = useQueryClient()
  const [kind, setKind] = useState<ContentKind>("note")
  const [title, setTitle] = useState("")
  const [text, setText] = useState("")
  const [url, setUrl] = useState("")
  const [file, setFile] = useState<File | null>(null)
  const [access, setAccess] = useState<DocumentAccess>("members")
  const [errors, setErrors] = useState<ContentErrors>({})
  const offerAdminsOnly = canSetAdminsOnlyAccess(corpus, viewer)
  const adminsOnly = offerAdminsOnly && access === "admins"

  const add = useMutation({
    mutationFn: () => {
      const documentAccess = adminsOnly ? "admins" : undefined
      if (kind === "file") {
        if (!file) throw new Error("Choose a file.")
        return addFileDocument(organizationId, corpus, {
          file,
          title,
          access: documentAccess,
        })
      }
      if (kind === "link") {
        return addDocument(organizationId, corpus.id, {
          kind: "external",
          url,
          title,
          text,
          access: documentAccess,
        })
      }
      return addDocument(organizationId, corpus.id, {
        kind: "inline",
        title,
        text,
        access: documentAccess,
      })
    },
    // A request that is out cannot be recalled, so the document exists once
    // it lands whether or not the form still waits for it, and the lists have
    // to know. What the form does with it (`submit`) is another matter.
    onSuccess: () => {
      void afterDocumentsChange(queryClient, organizationId, corpus.id)
    },
  })

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = contentErrors(kind, { text, url, file })
    setErrors(found)
    if (Object.keys(found).length) return
    // Runs only while this form still waits for the request. Cancel closes
    // the form, which drops it: an upload that lands after that neither
    // closes the form a member has opened since nor tells them of a document
    // they backed out of.
    add.mutate(undefined, {
      onSuccess: (document) => onAdded(document, kind),
    })
  }

  const describedBy = (field: keyof ContentErrors) =>
    errors[field] ? `${id}-${field}-error` : undefined
  const fieldError = (field: keyof ContentErrors) =>
    errors[field] && (
      <p className="workspace-knowledge-error" id={`${id}-${field}-error`}>
        {errors[field]}
      </p>
    )

  return (
    <form
      className="workspace-knowledge-form workspace-knowledge-add"
      onSubmit={submit}
      noValidate
    >
      <fieldset className="workspace-knowledge-choice" disabled={add.isPending}>
        <legend>What to add</legend>
        {contentKinds.map((option) => {
          const Icon = option.icon
          return (
            <label key={option.kind}>
              <input
                type="radio"
                name={`${id}-kind`}
                value={option.kind}
                checked={kind === option.kind}
                onChange={() => {
                  setKind(option.kind)
                  setErrors({})
                  add.reset()
                }}
              />
              <Icon size={15} strokeWidth={1.7} aria-hidden="true" />
              {option.label}
            </label>
          )
        })}
      </fieldset>

      {kind === "link" && (
        <>
          <label htmlFor={`${id}-url`}>Address</label>
          <Input
            id={`${id}-url`}
            type="url"
            inputMode="url"
            value={url}
            onChange={(event) => {
              setUrl(event.target.value)
              setErrors((current) => ({ ...current, url: undefined }))
            }}
            aria-invalid={Boolean(errors.url)}
            aria-describedby={describedBy("url")}
            placeholder="https://"
            maxLength={maxSourceRefLength}
            disabled={add.isPending}
          />
          {fieldError("url")}
        </>
      )}

      {kind === "file" && (
        <>
          <label htmlFor={`${id}-file`}>File</label>
          <input
            className="workspace-knowledge-file"
            id={`${id}-file`}
            type="file"
            accept={indexableFileAccept}
            aria-invalid={Boolean(errors.file)}
            aria-describedby={describedBy("file") ?? `${id}-file-hint`}
            disabled={add.isPending}
            onChange={(event) => {
              setFile(event.target.files?.[0] ?? null)
              setErrors((current) => ({ ...current, file: undefined }))
            }}
          />
          {fieldError("file") || (
            <p className="workspace-knowledge-hint" id={`${id}-file-hint`}>
              Text, Markdown, HTML, CSV or JSON, up to 8 MB. It’s kept in your
              files, visible to the same people as this collection.
            </p>
          )}
        </>
      )}

      <label htmlFor={`${id}-title`}>
        Title <span className="workspace-knowledge-optional">optional</span>
      </label>
      <Input
        id={`${id}-title`}
        value={title}
        onChange={(event) => setTitle(event.target.value)}
        placeholder={
          kind === "file"
            ? "Defaults to the file’s name"
            : kind === "link"
              ? "e.g. Pricing page"
              : "e.g. Decision log"
        }
        maxLength={maxDocumentTitleLength}
        disabled={add.isPending}
      />

      {kind !== "file" && (
        <>
          <label htmlFor={`${id}-text`}>
            {kind === "link" ? "Text from the page" : "Text"}
          </label>
          <Textarea
            id={`${id}-text`}
            value={text}
            onChange={(event) => {
              setText(event.target.value)
              setErrors((current) => ({ ...current, text: undefined }))
            }}
            aria-invalid={Boolean(errors.text)}
            aria-describedby={
              describedBy("text") ??
              (kind === "link" ? `${id}-link-hint` : undefined)
            }
            placeholder={
              kind === "link"
                ? "Paste the part of the page worth searching"
                : "What should your agents know?"
            }
            rows={kind === "link" ? 5 : 4}
            disabled={add.isPending}
          />
          {fieldError("text") ||
            (kind === "link" && (
              <p className="workspace-knowledge-hint" id={`${id}-link-hint`}>
                Oryxa doesn’t fetch pages, so paste the text you want found. The
                link is kept so answers can point back to it; adding the same
                link again replaces its text.
              </p>
            ))}
        </>
      )}

      {offerAdminsOnly && (
        <fieldset
          className="workspace-knowledge-choice"
          disabled={add.isPending}
        >
          <legend>Who can find it</legend>
          <label>
            <input
              type="radio"
              name={`${id}-access`}
              value="members"
              checked={access === "members"}
              onChange={() => setAccess("members")}
            />
            Everyone with access
          </label>
          <label>
            <input
              type="radio"
              name={`${id}-access`}
              value="admins"
              checked={access === "admins"}
              onChange={() => setAccess("admins")}
            />
            <Lock size={15} strokeWidth={1.7} aria-hidden="true" />
            Admins only
          </label>
        </fieldset>
      )}
      {offerAdminsOnly && (
        <p className="workspace-knowledge-hint">
          {access === "admins"
            ? "Only workspace owners and admins can find this."
            : "Everyone with access to this collection can find it."}
        </p>
      )}

      {add.isError && (
        <p className="workspace-knowledge-error" role="alert">
          {ragErrorMessage(
            add.error,
            kind === "file"
              ? "Could not upload that file. Try again."
              : "Could not add that. Try again.",
            {
              notFound:
                kind === "file"
                  ? "The uploaded file couldn’t be read back. Try again."
                  : "This collection is no longer available.",
            }
          )}
        </p>
      )}
      <div className="library-actions">
        <Button variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={add.isPending}>
          {add.isPending
            ? kind === "file"
              ? "Uploading…"
              : "Adding…"
            : kind === "file"
              ? "Upload file"
              : kind === "link"
                ? "Add link"
                : "Add note"}
        </Button>
      </div>
    </form>
  )
}

type AskResult =
  | { kind: "answer"; question: string; answered: Answered }
  | { kind: "search"; question: string; results: SearchResults }

function excerpt(text: string, limit = 280): string {
  const flat = text.replace(/\s+/g, " ").trim()
  return flat.length > limit ? `${flat.slice(0, limit - 1).trimEnd()}…` : flat
}

function AskKnowledgeBase({
  organizationId,
  corpus,
  answering,
  indexing,
}: {
  organizationId: string
  corpus: Corpus
  answering: boolean
  indexing: boolean
}) {
  const [question, setQuestion] = useState("")
  const ask = useMutation({
    mutationFn: async (query: string): Promise<AskResult> =>
      answering
        ? {
            kind: "answer",
            question: query,
            answered: await answerCorpus(organizationId, corpus.id, { query }),
          }
        : {
            kind: "search",
            question: query,
            results: await searchCorpus(organizationId, corpus.id, {
              query,
              topK: 5,
            }),
          },
  })
  const label = answering ? `Ask ${corpus.name}` : `Search ${corpus.name}`
  const canSend = question.trim().length > 0 && !ask.isPending

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (canSend) ask.mutate(question.trim())
  }

  const result = ask.data
  const nothingYet = indexing
    ? " yet. Some documents are still being indexed."
    : "."

  return (
    <section className="workspace-knowledge-ask" aria-label={label}>
      <form onSubmit={submit}>
        <div className="workspace-composer">
          <input
            aria-label={label}
            placeholder={
              answering ? "Ask a question about this collection" : label
            }
            value={question}
            maxLength={maxQueryLength}
            onChange={(event) => setQuestion(event.target.value)}
          />
          <Button
            className="workspace-composer-send"
            type="submit"
            size="icon-sm"
            aria-label={answering ? "Ask" : "Search"}
            disabled={!canSend}
          >
            {ask.isPending ? (
              <LoaderCircle
                className="workspace-knowledge-spin"
                size={15}
                strokeWidth={2}
              />
            ) : (
              <ArrowUp size={16} strokeWidth={2} />
            )}
          </Button>
        </div>
      </form>
      <div className="workspace-knowledge-answer-region" aria-live="polite">
        {ask.isPending && (
          <p className="workspace-knowledge-answer-status" role="status">
            Looking through {corpus.name}…
          </p>
        )}
        {ask.isError && (
          <p className="workspace-knowledge-error" role="alert">
            {ragErrorMessage(
              ask.error,
              answering
                ? "Could not answer that. Try again."
                : "Could not search this collection. Try again.",
              { notFound: "This collection is no longer available." }
            )}
          </p>
        )}
        {result && (
          <div className="workspace-knowledge-answer">
            <p className="workspace-knowledge-answer-question">
              {result.question}
            </p>
            {result.kind === "answer" ? (
              result.answered.answer.grounded ? (
                <>
                  <p className="workspace-knowledge-answer-text">
                    {result.answered.answer.text}
                  </p>
                  <ol
                    className="workspace-knowledge-citations"
                    aria-label="Sources"
                  >
                    {result.answered.answer.citations.map((citation) => {
                      const href = externalSourceLink(citation.source)
                      const title =
                        citation.title?.trim() ||
                        citationSourceLabel(citation.source)
                      return (
                        <li key={`${citation.index}-${citation.chunk_id}`}>
                          <span className="workspace-knowledge-citation-index">
                            {citation.index}
                          </span>
                          <span>
                            {href ? (
                              <a
                                href={href}
                                target="_blank"
                                rel="noreferrer noopener"
                              >
                                {title}
                              </a>
                            ) : (
                              <strong>{title}</strong>
                            )}
                            <small>
                              {citationSourceLabel(citation.source)}
                            </small>
                            <CitationProvenance metadata={citation.metadata} />
                          </span>
                        </li>
                      )
                    })}
                  </ol>
                </>
              ) : (
                <p className="workspace-knowledge-answer-text">
                  Nothing in {corpus.name} answers that{nothingYet}
                </p>
              )
            ) : result.results.matches.length ? (
              <>
                <p className="workspace-knowledge-hint">
                  This workspace has no answering model, so these are the
                  passages that match best.
                </p>
                <ol
                  className="workspace-knowledge-passages"
                  aria-label="Matching passages"
                >
                  {result.results.matches.map((match) => {
                    const href = externalSourceLink(match.document.source)
                    const title =
                      match.document.title?.trim() ||
                      citationSourceLabel(match.document.source)
                    return (
                      <li key={match.chunk.id}>
                        <blockquote>{excerpt(match.chunk.text)}</blockquote>
                        <small>
                          {href ? (
                            <a
                              href={href}
                              target="_blank"
                              rel="noreferrer noopener"
                            >
                              {title}
                            </a>
                          ) : (
                            title
                          )}
                          {match.chunk.heading_path?.length
                            ? ` · ${match.chunk.heading_path.join(" › ")}`
                            : ""}
                        </small>
                      </li>
                    )
                  })}
                </ol>
              </>
            ) : (
              <p className="workspace-knowledge-answer-text">
                Nothing in {corpus.name} matches that{nothingYet}
              </p>
            )}
          </div>
        )}
      </div>
    </section>
  )
}

/** A destructive confirm: says what goes, and what failed when it did. */
export function ConfirmDialog({
  open,
  title,
  description,
  error,
  pending,
  confirmLabel,
  pendingLabel,
  onConfirm,
  onOpenChange,
}: {
  open: boolean
  title: string
  description: string
  error?: string
  pending: boolean
  confirmLabel: string
  pendingLabel: string
  onConfirm: () => void
  onOpenChange: (open: boolean) => void
}) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent className="library-dialog">
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <p className="workspace-knowledge-error" role="alert">
            {error}
          </p>
        )}
        <AlertDialogFooter className="library-dialog-footer">
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={pending}
            onClick={onConfirm}
          >
            {pending ? pendingLabel : confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

/**
 * Adding to the Library: the Note, Link and File form for one collection. Opened
 * from All files, it first asks which collection, among those the member may
 * add to.
 */
export function AddFilesDialog({
  organizationId,
  collections,
  collectionId,
  viewer,
  onChooseCollection,
  onAdded,
  onClose,
}: {
  organizationId: string
  /** The collections the member may add to. */
  collections: readonly Corpus[]
  /** The collection being added to; undefined asks for one first. */
  collectionId?: string
  viewer: RagViewer
  onChooseCollection: (collectionId: string) => void
  onAdded: (document: CorpusDocument, kind: ContentKind, corpus: Corpus) => void
  onClose: () => void
}) {
  const corpus = collections.find((item) => item.id === collectionId)
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="library-dialog library-add-dialog">
        <DialogHeader>
          <DialogTitle>
            {corpus ? `Add to ${corpus.name}` : "Add files"}
          </DialogTitle>
          <DialogDescription>
            {corpus
              ? "It becomes searchable once it’s indexed, usually within a few seconds."
              : "Choose the collection it belongs in."}
          </DialogDescription>
        </DialogHeader>
        {corpus ? (
          <AddContentForm
            key={corpus.id}
            organizationId={organizationId}
            corpus={corpus}
            viewer={viewer}
            onAdded={(document, kind) => onAdded(document, kind, corpus)}
            onCancel={onClose}
          />
        ) : (
          <ul className="library-choose" aria-label="Collections">
            {collections.map((item) => (
              <li key={item.id}>
                <Button
                  variant="ghost"
                  className="library-rail-item"
                  onClick={() => onChooseCollection(item.id)}
                >
                  <span
                    className="library-rail-dot"
                    data-tone={collectionTone(item.id)}
                    aria-hidden="true"
                  />
                  <span className="library-rail-label">{item.name}</span>
                  <span className="library-rail-count">
                    {corpusScopeLabel(item, viewer)}
                  </span>
                </Button>
              </li>
            ))}
          </ul>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** Whether the member may add to or remove from this collection here. */
export function canAddToCollection(corpus: Corpus, viewer: RagViewer): boolean {
  return !isFromConnectedApps(corpus) && canWriteCorpus(corpus, viewer)
}

/** Rows whose title holds the filter, ignoring case. */
export function filterFiles<T extends { document: CorpusDocument }>(
  files: readonly T[],
  filter: string
): T[] {
  const needle = filter.trim().toLocaleLowerCase()
  if (!needle) return [...files]
  return files.filter((file) =>
    documentTitle(file.document).toLocaleLowerCase().includes(needle)
  )
}

/** One collection in the Library's right pane: its header, Ask, and its files. */
export function KnowledgeBaseDetail({
  organizationId,
  corpusId,
  name,
  viewer,
  members,
  filter,
  answering = false,
  onAdd,
  onClose,
  onOpenTools,
}: {
  organizationId: string
  corpusId: string
  /** The name the rail showed, for the moments before the corpus loads. */
  name: string
  viewer: RagViewer
  /** The organization's members, to name who added each file. */
  members: readonly Member[]
  /** The Library's search, applied to the loaded titles. */
  filter: string
  /** Whether this deployment can write answers (`capabilities.answering`). */
  answering?: boolean
  /** Opens Add files on this collection. */
  onAdd: () => void
  onClose: () => void
  onOpenTools?: () => void
}) {
  const queryClient = useQueryClient()
  const corpusQuery = useQuery({
    ...corpusQueryOptions(organizationId, corpusId),
    placeholderData: () =>
      queryClient
        .getQueryData<Corpus[]>(ragKeys.corpora(organizationId))
        ?.find((item) => item.id === corpusId),
  })
  const documentsQuery = useInfiniteQuery(
    documentsInfiniteQueryOptions(organizationId, corpusId)
  )
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [removing, setRemoving] = useState<CorpusDocument | null>(null)

  const removeCorpus = useMutation({
    mutationFn: () => deleteCorpus(organizationId, corpusId),
    onSuccess: () => {
      setConfirmDelete(false)
      onClose()
      void afterDeleteCorpus(queryClient, organizationId, corpusId)
    },
  })
  const removeDocument = useMutation({
    mutationFn: (documentId: string) =>
      deleteDocument(organizationId, corpusId, documentId),
    onSuccess: (_, documentId) => {
      setRemoving(null)
      queryClient.setQueryData<InfiniteData<DocumentPage, string | undefined>>(
        ragKeys.documents(organizationId, corpusId),
        (data) =>
          data && {
            ...data,
            pages: data.pages.map((page) => ({
              ...page,
              documents: page.documents.filter(
                (item) => item.id !== documentId
              ),
            })),
          }
      )
      void afterDocumentsChange(queryClient, organizationId, corpusId)
    },
  })

  const corpus = corpusQuery.data
  const documents =
    documentsQuery.data?.pages.flatMap((page) => page.documents) ?? []

  if (!corpus) {
    if (corpusQuery.isError) {
      const gone =
        corpusQuery.error instanceof ApiError &&
        corpusQuery.error.status === 404
      return (
        <div className="workspace-knowledge-empty">
          <BookOpenText size={23} strokeWidth={1.5} />
          <strong>
            {gone
              ? "This collection is no longer here"
              : "Couldn’t open this collection"}
          </strong>
          <p>
            {gone
              ? "It was deleted, or you no longer have access to it."
              : ragErrorMessage(corpusQuery.error, "Try again in a moment.")}
          </p>
          <div className="library-actions">
            <Button variant="outline" onClick={onClose}>
              All files
            </Button>
            {!gone && (
              <Button onClick={() => void corpusQuery.refetch()}>
                Try again
              </Button>
            )}
          </div>
        </div>
      )
    }
    return (
      <p className="workspace-knowledge-loading" role="status">
        Opening {name}…
      </p>
    )
  }

  const fromApps = isFromConnectedApps(corpus)
  const canWrite = canAddToCollection(corpus, viewer)
  const canDelete = !fromApps && canDeleteCorpus(corpus, viewer)
  const description = corpusDescription(corpus)
  const documentCount = Math.max(corpus.document_count, documents.length)
  const indexingCount = documents.filter(isIndexing).length
  const hasDocuments = documentCount > 0
  const files = filterFiles(
    documents.map((document) => ({ document })),
    filter
  )

  return (
    <div className="library-collection">
      <div className="library-collection-head">
        <div className="library-collection-copy">
          <h2>
            <span
              className="library-rail-dot"
              data-tone={collectionTone(corpus.id)}
              aria-hidden="true"
            />
            {corpus.name}
          </h2>
          {description && <p>{description}</p>}
          <small>
            {corpusScopeLabel(corpus, viewer)} ·{" "}
            {countLabel(documentCount, "file")}
            {indexingCount > 0 && ` · ${indexingCount} indexing`} ·{" "}
            {corpusTimeLabel(corpus)}
          </small>
        </div>
        <div className="library-collection-actions">
          {canWrite && (
            <Button onClick={onAdd}>
              <Plus strokeWidth={1.8} />
              Add content
            </Button>
          )}
          {fromApps && onOpenTools && (
            <Button variant="outline" onClick={onOpenTools}>
              <Puzzle strokeWidth={1.7} />
              Manage in Tools
            </Button>
          )}
          {canDelete && (
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <Button
                    variant="ghost"
                    size="icon"
                    className="ml-auto"
                    aria-label={`More for ${corpus.name}`}
                  />
                }
              >
                <MoreHorizontal strokeWidth={1.8} />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem
                  variant="destructive"
                  onClick={() => {
                    removeCorpus.reset()
                    setConfirmDelete(true)
                  }}
                >
                  <Trash2 />
                  Delete collection
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
      </div>

      {fromApps && (
        <p className="workspace-knowledge-notice">
          Filled from your connected apps as they sync. Choose what gets indexed
          in Tools.
        </p>
      )}

      {hasDocuments && (
        <AskKnowledgeBase
          key={corpus.id}
          organizationId={organizationId}
          corpus={corpus}
          answering={answering}
          indexing={indexingCount > 0}
        />
      )}

      {documentsQuery.isPending ? (
        <p className="workspace-knowledge-loading" role="status">
          Loading files…
        </p>
      ) : documentsQuery.isError && !documents.length ? (
        <div className="workspace-knowledge-alert" role="alert">
          <span>
            {ragErrorMessage(
              documentsQuery.error,
              "Couldn’t load this collection’s files."
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
      ) : documents.length ? (
        files.length ? (
          <>
            <LibraryFileTable
              label={`Files in ${corpus.name}`}
              files={files}
              members={members}
              actions={({ document }) =>
                canWrite ? (
                  <DropdownMenu>
                    <DropdownMenuTrigger
                      render={
                        <Button
                          variant="ghost"
                          size="icon-sm"

                          aria-label={`More for ${documentTitle(document)}`}
                        />
                      }
                    >
                      <MoreHorizontal strokeWidth={1.8} />
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem
                        variant="destructive"
                        onClick={() => {
                          removeDocument.reset()
                          setRemoving(document)
                        }}
                      >
                        <X />
                        Remove
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                ) : null
              }
            />
            {documentsQuery.hasNextPage && (
              <Button
                variant="outline"
                className="library-more"
                disabled={documentsQuery.isFetchingNextPage}
                onClick={() => void documentsQuery.fetchNextPage()}
              >
                {documentsQuery.isFetchingNextPage ? "Loading…" : "Load more"}
              </Button>
            )}
          </>
        ) : (
          <p className="workspace-knowledge-loading">
            No files here match “{filter.trim()}”.
          </p>
        )
      ) : (
        <div className="workspace-knowledge-empty">
          {fromApps ? (
            <Puzzle size={23} strokeWidth={1.5} />
          ) : (
            <FileText size={23} strokeWidth={1.5} />
          )}
          <strong>{fromApps ? "Nothing indexed yet" : "No files yet"}</strong>
          <p>
            {fromApps
              ? "Files show up here as your connected apps sync."
              : canWrite
                ? "Add a note, a link or a text file, and your agents can search it."
                : "Workspace admins decide what goes in here."}
          </p>
          {canWrite && (
            <Button variant="outline" onClick={onAdd}>
              <Plus strokeWidth={1.8} />
              Add content
            </Button>
          )}
        </div>
      )}

      <ConfirmDialog
        open={confirmDelete}
        title={`Delete “${corpus.name}”?`}
        description={`${
          hasDocuments
            ? `Its ${countLabel(documentCount, "file")} and everything indexed from them go with it.`
            : "It has nothing in it yet."
        } This can’t be undone.`}
        error={
          removeCorpus.isError
            ? ragErrorMessage(
                removeCorpus.error,
                "Could not delete this collection. Try again.",
                { notFound: "It was already deleted." }
              )
            : undefined
        }
        pending={removeCorpus.isPending}
        confirmLabel="Delete collection"
        pendingLabel="Deleting…"
        onConfirm={() => removeCorpus.mutate()}
        onOpenChange={(open) => {
          if (!open && !removeCorpus.isPending) setConfirmDelete(false)
        }}
      />
      <ConfirmDialog
        open={removing !== null}
        title={`Remove “${removing ? documentTitle(removing) : ""}”?`}
        description="Answers stop drawing on it, and it can’t be brought back."
        error={
          removeDocument.isError
            ? ragErrorMessage(
                removeDocument.error,
                "Could not remove it. Try again.",
                { notFound: "It was already removed." }
              )
            : undefined
        }
        pending={removeDocument.isPending}
        confirmLabel="Remove"
        pendingLabel="Removing…"
        onConfirm={() => removing && removeDocument.mutate(removing.id)}
        onOpenChange={(open) => {
          if (!open && !removeDocument.isPending) setRemoving(null)
        }}
      />
    </div>
  )
}
