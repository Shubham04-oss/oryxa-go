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
  Plus,
  Puzzle,
  RotateCw,
  StickyNote,
  Trash2,
  X,
} from "lucide-react"
import { siGithub, siGmail, siGoogledrive, siNotion } from "simple-icons"
import type { SimpleIcon } from "simple-icons"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { ApiError } from "@/lib/api/client"
import { formatBytes, uploadProblem } from "@/lib/files/client"
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
  documentOrigin,
  documentsInfiniteQueryOptions,
  externalSourceLink,
  indexableContentType,
  indexableFileAccept,
  isAdminsOnlyDocument,
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
  DocumentOrigin,
  DocumentPage,
  RagViewer,
  SearchResults,
} from "@/lib/rag/client"
import {
  citationProvenance,
  citationSourceLabel,
  corpusScopeLabel,
  corpusTimeLabel,
  countLabel,
  documentTitle,
  originLabel,
  relativeTime,
} from "./knowledge-format"

// simple-icons carries no Slack mark (removed for its trademark policy); a
// Slack chip falls back to its initial, same as any other unmapped app.
const appIcons: Partial<Record<string, SimpleIcon>> = {
  github: siGithub,
  gmail: siGmail,
  googledrive: siGoogledrive,
  notion: siNotion,
}

/** A connected app's brand mark, or its initial when this app has none. */
function AppLogo({ provider, name }: { provider: string; name: string }) {
  const icon = appIcons[provider.toLowerCase()]
  return icon ? (
    <svg viewBox="0 0 24 24" fill={`#${icon.hex}`}>
      <path d={icon.path} />
    </svg>
  ) : (
    <>{name.charAt(0)}</>
  )
}

function OriginIcon({ origin }: { origin: DocumentOrigin }) {
  if (origin.kind === "app") {
    return (
      <span
        className="workspace-knowledge-source-icon workspace-knowledge-app-icon"
        aria-hidden="true"
      >
        <AppLogo provider={origin.provider} name={origin.name} />
      </span>
    )
  }
  const Icon =
    origin.kind === "link"
      ? Link2
      : origin.kind === "file"
        ? FileText
        : StickyNote
  return (
    <span className="workspace-knowledge-source-icon" aria-hidden="true">
      <Icon size={17} strokeWidth={1.7} />
    </span>
  )
}

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

const statusLabels: Record<CorpusDocument["status"], string> = {
  pending: "Pending",
  ingesting: "Indexing",
  ready: "Ready",
  failed: "Failed",
}

function DocumentRow({
  document,
  canRemove,
  confirming,
  removing,
  removeError,
  onAskRemove,
  onCancelRemove,
  onRemove,
}: {
  document: CorpusDocument
  canRemove: boolean
  confirming: boolean
  removing: boolean
  removeError?: string
  onAskRemove: () => void
  onCancelRemove: () => void
  onRemove: () => void
}) {
  const origin = documentOrigin(document)
  const title = documentTitle(document)
  const href = externalSourceLink(document.source)
  const retrying = document.status === "pending" && document.attempts > 0

  return (
    <li
      className="workspace-knowledge-document"
      data-status={document.status}
      data-screen-context-kind="document"
      data-screen-context-id={document.id}
      data-screen-context-name={title}
    >
      <OriginIcon origin={origin} />
      <span className="workspace-knowledge-source-name">
        {href ? (
          <a href={href} target="_blank" rel="noreferrer noopener">
            <strong>{title}</strong>
          </a>
        ) : (
          <strong>{title}</strong>
        )}
        <small>
          {originLabel(origin, document)} · {formatBytes(document.byte_size)} ·
          Added {relativeTime(document.created_at)}
        </small>
        {document.status === "failed" && (
          <small className="workspace-knowledge-document-error">
            {document.failure_reason || "Indexing gave up without a reason."}
          </small>
        )}
        {retrying && (
          <small>
            {countLabel(document.attempts, "attempt")} didn’t finish. It will be
            tried again.
          </small>
        )}
      </span>
      <span
        className="workspace-knowledge-status"
        data-status={document.status}
      >
        {isIndexing(document) ? (
          <LoaderCircle
            className="workspace-knowledge-spin"
            size={12}
            strokeWidth={2}
            aria-hidden="true"
          />
        ) : (
          <span className="workspace-knowledge-status-dot" aria-hidden="true" />
        )}
        {statusLabels[document.status]}
      </span>
      {isAdminsOnlyDocument(document) && (
        <span className="workspace-knowledge-admins-badge">
          <Lock size={11} strokeWidth={1.8} aria-hidden="true" />
          Admins only
        </span>
      )}
      {canRemove && !confirming && (
        <button
          className="workspace-knowledge-remove"
          type="button"
          aria-label={`Remove ${title}`}
          title="Remove"
          onClick={onAskRemove}
        >
          <X size={15} strokeWidth={1.8} />
        </button>
      )}
      {confirming && (
        <div className="workspace-knowledge-confirm">
          <p>
            Remove “{title}”? Answers stop drawing on it, and it can’t be
            brought back.
          </p>
          {removeError && (
            <p className="workspace-knowledge-error" role="alert">
              {removeError}
            </p>
          )}
          <div className="workspace-knowledge-form-actions">
            <button type="button" onClick={onCancelRemove}>
              Cancel
            </button>
            <button
              className="is-danger"
              type="button"
              disabled={removing}
              onClick={onRemove}
            >
              {removing ? "Removing…" : "Remove"}
            </button>
          </div>
        </div>
      )}
    </li>
  )
}

type ContentKind = "note" | "link" | "file"

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
          : "Knowledge bases take text: a .txt, .md, .html, .csv or .json file. Convert PDFs and Office files to text first."))
    if (problem) errors.file = problem
  }
  return errors
}

function AddContentForm({
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
    onSuccess: (document) => onAdded(document, kind),
  })

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = contentErrors(kind, { text, url, file })
    setErrors(found)
    if (Object.keys(found).length) return
    add.mutate()
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
      <div>
        <h3>Add content</h3>
        <p>
          It becomes searchable once it’s indexed, usually within a few seconds.
        </p>
      </div>
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
              files, visible to the same people as this knowledge base.
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
            : "Everyone with access to this knowledge base can find it."}
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
                  : "This knowledge base is no longer available.",
            }
          )}
        </p>
      )}
      <div className="workspace-knowledge-form-actions">
        <button type="button" onClick={onCancel}>
          Cancel
        </button>
        <button type="submit" disabled={add.isPending}>
          {add.isPending
            ? kind === "file"
              ? "Uploading…"
              : "Adding…"
            : kind === "file"
              ? "Upload file"
              : kind === "link"
                ? "Add link"
                : "Add note"}
        </button>
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
              answering ? "Ask a question about this knowledge base" : label
            }
            value={question}
            maxLength={maxQueryLength}
            onChange={(event) => setQuestion(event.target.value)}
          />
          <button
            className="workspace-composer-send"
            type="submit"
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
          </button>
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
                : "Could not search this knowledge base. Try again.",
              { notFound: "This knowledge base is no longer available." }
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

export function KnowledgeBaseDetail({
  organizationId,
  corpusId,
  name,
  viewer,
  answering = false,
  onClose,
  onOpenTools,
}: {
  organizationId: string
  corpusId: string
  /** The name the list showed, for the moments before the corpus loads. */
  name: string
  viewer: RagViewer
  /** Whether this deployment can write answers (`capabilities.answering`). */
  answering?: boolean
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
  const [adding, setAdding] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [confirmingDocument, setConfirmingDocument] = useState<string | null>(
    null
  )
  const [notice, setNotice] = useState("")

  const removeCorpus = useMutation({
    mutationFn: () => deleteCorpus(organizationId, corpusId),
    onSuccess: () => {
      onClose()
      void afterDeleteCorpus(queryClient, organizationId, corpusId)
    },
  })
  const removeDocument = useMutation({
    mutationFn: (documentId: string) =>
      deleteDocument(organizationId, corpusId, documentId),
    onSuccess: (_, documentId) => {
      setConfirmingDocument(null)
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
              ? "This knowledge base is no longer here"
              : "Couldn’t open this knowledge base"}
          </strong>
          <p>
            {gone
              ? "It was deleted, or you no longer have access to it."
              : ragErrorMessage(corpusQuery.error, "Try again in a moment.")}
          </p>
          <div className="workspace-knowledge-form-actions">
            {!gone && (
              <button type="button" onClick={() => void corpusQuery.refetch()}>
                Try again
              </button>
            )}
            <button type="button" onClick={onClose}>
              All knowledge bases
            </button>
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
  const canWrite = !fromApps && canWriteCorpus(corpus, viewer)
  const canDelete = !fromApps && canDeleteCorpus(corpus, viewer)
  const description = corpusDescription(corpus)
  const documentCount = Math.max(corpus.document_count, documents.length)
  const indexingCount = documents.filter(isIndexing).length
  const hasDocuments = documentCount > 0

  function openAdding() {
    setAdding(true)
    setConfirmDelete(false)
    setNotice("")
  }

  function added(document: CorpusDocument, kind: ContentKind) {
    const known = documents.some((item) => item.id === document.id)
    setAdding(false)
    setNotice(
      !known
        ? ""
        : kind === "note"
          ? "That note is already in this knowledge base."
          : "That was already here. If its text changed, it’s being indexed again."
    )
    void afterDocumentsChange(queryClient, organizationId, corpusId)
  }

  return (
    <div className="workspace-knowledge-detail">
      <div className="workspace-knowledge-detail-bar">
        <div className="workspace-knowledge-detail-copy">
          {description && <p>{description}</p>}
          <small>
            {corpusScopeLabel(corpus, viewer)} ·{" "}
            {countLabel(documentCount, "document")} · {corpusTimeLabel(corpus)}
          </small>
        </div>
        <div className="workspace-knowledge-detail-actions">
          {fromApps && onOpenTools && (
            <button
              className="workspace-knowledge-quiet-button"
              type="button"
              onClick={onOpenTools}
            >
              <Puzzle size={15} strokeWidth={1.7} />
              Manage in Tools
            </button>
          )}
          {canWrite && (
            <button
              className="workspace-knowledge-create-button"
              type="button"
              aria-expanded={adding}
              onClick={() => (adding ? setAdding(false) : openAdding())}
            >
              <Plus size={17} strokeWidth={1.8} />
              Add content
            </button>
          )}
          {canDelete && (
            <button
              className="workspace-knowledge-icon-button"
              type="button"
              aria-label="Delete knowledge base"
              title="Delete knowledge base"
              aria-expanded={confirmDelete}
              onClick={() => {
                setConfirmDelete(true)
                setAdding(false)
              }}
            >
              <Trash2 size={16} strokeWidth={1.7} />
            </button>
          )}
        </div>
      </div>

      {fromApps && (
        <p className="workspace-knowledge-notice">
          Filled from your connected apps as they sync. Choose what gets indexed
          in Tools.
        </p>
      )}

      {confirmDelete && (
        <div className="workspace-knowledge-confirm workspace-knowledge-confirm-panel">
          <p>
            Delete “{corpus.name}”?{" "}
            {hasDocuments
              ? `Its ${countLabel(documentCount, "document")} and everything indexed from them go with it.`
              : "It has nothing in it yet."}{" "}
            This can’t be undone.
          </p>
          {removeCorpus.isError && (
            <p className="workspace-knowledge-error" role="alert">
              {ragErrorMessage(
                removeCorpus.error,
                "Could not delete this knowledge base. Try again.",
                { notFound: "It was already deleted." }
              )}
            </p>
          )}
          <div className="workspace-knowledge-form-actions">
            <button
              type="button"
              onClick={() => {
                setConfirmDelete(false)
                removeCorpus.reset()
              }}
            >
              Cancel
            </button>
            <button
              className="is-danger"
              type="button"
              disabled={removeCorpus.isPending}
              onClick={() => removeCorpus.mutate()}
            >
              {removeCorpus.isPending ? "Deleting…" : "Delete knowledge base"}
            </button>
          </div>
        </div>
      )}

      {adding && canWrite && (
        <AddContentForm
          organizationId={organizationId}
          corpus={corpus}
          viewer={viewer}
          onAdded={added}
          onCancel={() => setAdding(false)}
        />
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

      <div className="workspace-knowledge-section-heading">
        <div>
          <h3>Documents</h3>
          <p>
            {fromApps
              ? "What your connected apps have indexed here."
              : "Notes, links and files this knowledge base answers from."}
          </p>
        </div>
        <span>
          {documentCount}
          {indexingCount > 0 && ` · ${indexingCount} indexing`}
        </span>
      </div>

      {notice && (
        <p className="workspace-knowledge-status-line" role="status">
          {notice}
        </p>
      )}

      {documentsQuery.isPending ? (
        <p className="workspace-knowledge-loading" role="status">
          Loading documents…
        </p>
      ) : documentsQuery.isError && !documents.length ? (
        <div className="workspace-knowledge-alert" role="alert">
          <span>
            {ragErrorMessage(
              documentsQuery.error,
              "Couldn’t load this knowledge base’s documents."
            )}
          </span>
          <button type="button" onClick={() => void documentsQuery.refetch()}>
            <RotateCw size={14} strokeWidth={1.8} />
            Try again
          </button>
        </div>
      ) : documents.length ? (
        <>
          <ul className="workspace-knowledge-source-list">
            {documents.map((document) => (
              <DocumentRow
                key={document.id}
                document={document}
                canRemove={canWrite}
                confirming={confirmingDocument === document.id}
                removing={
                  removeDocument.isPending &&
                  removeDocument.variables === document.id
                }
                removeError={
                  removeDocument.isError &&
                  removeDocument.variables === document.id
                    ? ragErrorMessage(
                        removeDocument.error,
                        "Could not remove it. Try again.",
                        { notFound: "It was already removed." }
                      )
                    : undefined
                }
                onAskRemove={() => {
                  removeDocument.reset()
                  setConfirmingDocument(document.id)
                }}
                onCancelRemove={() => setConfirmingDocument(null)}
                onRemove={() => removeDocument.mutate(document.id)}
              />
            ))}
          </ul>
          {documentsQuery.hasNextPage && (
            <button
              className="workspace-knowledge-more"
              type="button"
              disabled={documentsQuery.isFetchingNextPage}
              onClick={() => void documentsQuery.fetchNextPage()}
            >
              {documentsQuery.isFetchingNextPage ? "Loading…" : "Load more"}
            </button>
          )}
        </>
      ) : (
        <div className="workspace-knowledge-empty">
          {fromApps ? (
            <Puzzle size={23} strokeWidth={1.5} />
          ) : (
            <FileText size={23} strokeWidth={1.5} />
          )}
          <strong>
            {fromApps ? "Nothing indexed yet" : "No documents yet"}
          </strong>
          <p>
            {fromApps
              ? "Documents show up here as your connected apps sync."
              : canWrite
                ? "Add a note, a link or a text file, and your agents can search it."
                : "Workspace admins decide what goes in here."}
          </p>
          {canWrite && !adding && (
            <button
              className="workspace-knowledge-quiet-button"
              type="button"
              onClick={openAdding}
            >
              <Plus size={15} strokeWidth={1.8} />
              Add content
            </button>
          )}
        </div>
      )}
    </div>
  )
}
