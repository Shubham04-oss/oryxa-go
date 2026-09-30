import { lazy, Suspense, useEffect, useId, useMemo, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { ArrowLeft, Download, FileText, Pencil } from "lucide-react"
import { ApiError } from "@/lib/api/client"
import {
  fetchArtifactLink,
  threadDetailQueryOptions,
} from "@/lib/threads/client"
import type { ArtifactLink } from "@/lib/threads/client"
import { artifactFileName, buildTimeline } from "@/lib/threads/timeline"
import type { ArtifactEntry } from "@/lib/threads/timeline"
import { OfficeEditor } from "./office-editor"

const DocumentPreview = lazy(() =>
  import("./document-preview").then((module) => ({
    default: module.DocumentPreview,
  }))
)

export type PrimaryAgentFileTarget = {
  threadId: string
  name: string
  version?: number
}

// The formats an edit is saved back as they were: ONLYOFFICE returns these
// three in the format it was given (editableDocuments in
// agents/primary/internal/httpserver/documents.go). Any other type it converts
// or reworks when it saves, and a converted file cannot be written back as
// the file it was, so those open for viewing and downloading only.
const editableExtensions = new Set(["docx", "xlsx", "pptx"])

export function isEditableDocument(name: string): boolean {
  const dot = name.lastIndexOf(".")
  return dot >= 0 && editableExtensions.has(name.slice(dot + 1).toLowerCase())
}

/**
 * The files of the conversation the chat is on, newest first, each by its
 * latest version. A new chat has none, and neither has one that hasn't
 * decided yet (`threadId` undefined): which conversation it opens is the
 * chat's to decide, not something to guess from the list here.
 */
export function useConversationFiles(
  organizationId: string,
  threadId?: string | null
) {
  const currentThreadId = threadId ?? null
  const detail = useQuery({
    ...threadDetailQueryOptions(organizationId, currentThreadId ?? ""),
    enabled: Boolean(currentThreadId),
  })
  const files = useMemo(() => {
    if (!detail.data) return []
    const entries = buildTimeline(detail.data.items).filter(
      (entry): entry is ArtifactEntry => entry.kind === "artifact"
    )
    const latest = new Map<string, ArtifactEntry>()
    for (const entry of entries) latest.set(entry.name, entry)
    return [...latest.values()].reverse()
  }, [detail.data])
  const status = !currentThreadId
    ? "Start a conversation to see its files here."
    : detail.isPending
      ? "Loading files…"
      : detail.isError
        ? "Could not load this conversation's files."
        : ""
  const error = Boolean(currentThreadId) && detail.isError
  const pending = Boolean(currentThreadId) && detail.isPending
  return { currentThreadId, files, status, error, pending }
}

/**
 * The open conversation's files, as its header's Files shows them inline:
 * choosing one opens it in the viewer, in place of the conversation.
 */
export function PrimaryAgentFiles({
  id,
  organizationId,
  threadId,
  onOpenFile,
}: {
  /** The section's id, for the button that shows it. */
  id?: string
  organizationId: string
  threadId?: string | null
  onOpenFile?: (file: PrimaryAgentFileTarget) => void
}) {
  const headingId = useId()
  const { currentThreadId, files, status, error, pending } =
    useConversationFiles(organizationId, threadId)
  return (
    <section
      id={id}
      className="workspace-primary-files-section"
      aria-labelledby={headingId}
    >
      <p className="workspace-eyebrow" id={headingId}>
        Files in this conversation
      </p>
      {status ? (
        <p
          className={
            error ? "workspace-agents-error" : "workspace-agents-muted"
          }
          role={error ? "alert" : pending ? "status" : undefined}
        >
          {status}
        </p>
      ) : !files.length ? (
        <p className="workspace-agents-muted">
          Files the agent creates will appear here.
        </p>
      ) : (
        <div
          className="workspace-primary-file-list"
          aria-label="Conversation files"
        >
          {files.map((file) => (
            <button
              type="button"
              key={file.key}
              className="workspace-primary-file"
              data-screen-context-kind="artifact"
              data-screen-context-id={file.name}
              data-screen-context-name={artifactFileName(file.name)}
              onClick={() => {
                if (currentThreadId) {
                  onOpenFile?.({
                    threadId: currentThreadId,
                    name: file.name,
                    version: file.version,
                  })
                }
              }}
            >
              <FileText size={15} strokeWidth={1.7} />
              <span>{artifactFileName(file.name)}</span>
            </button>
          ))}
        </div>
      )}
    </section>
  )
}

/**
 * A file of the conversation, previewed in place of the conversation (the
 * chat stays mounted behind it), with Edit for the formats the editor saves
 * back as they were, and a download link. Back returns to the conversation.
 */
export function PrimaryAgentFileViewer({
  organizationId,
  threadId,
  file,
  onBack,
}: {
  organizationId: string
  threadId?: string | null
  file?: PrimaryAgentFileTarget
  onBack: () => void
}) {
  const {
    currentThreadId,
    files,
    status,
    error: listError,
    pending,
  } = useConversationFiles(organizationId, threadId)
  const current = file ?? files.at(0)
  const editable = current ? isEditableDocument(current.name) : false
  const activeThreadId = file?.threadId ?? currentThreadId
  const currentFileKey =
    current && activeThreadId ? `${activeThreadId}\0${current.name}` : ""
  const [editingFileKey, setEditingFileKey] = useState<string | null>(null)
  const editing = editable && editingFileKey === currentFileKey
  const [link, setLink] = useState<ArtifactLink>()
  const [error, setError] = useState("")
  useEffect(() => setEditingFileKey(null), [currentFileKey])
  useEffect(() => {
    setLink(undefined)
    setError("")
    if (!current || !activeThreadId) return
    let cancelled = false
    void fetchArtifactLink(organizationId, activeThreadId, {
      name: current.name,
      // The Office editor saves a new artifact version. An editable file's
      // download always follows the latest, even if the conversation event
      // names an older one.
      version: editable ? undefined : current.version,
    }).then(
      (value) => {
        if (!cancelled) setLink(value)
      },
      (failure: unknown) => {
        if (cancelled) return
        setError(
          failure instanceof ApiError && failure.status === 404
            ? "This file is no longer available."
            : "Couldn't open this file. Try again."
        )
      }
    )
    return () => {
      cancelled = true
    }
  }, [
    organizationId,
    activeThreadId,
    current?.name,
    current?.version,
    editable,
  ])
  const name = current ? artifactFileName(current.name) : "File"

  return (
    <div className="workspace-primary-file-viewer">
      <header className="workspace-primary-file-viewer-bar">
        <button
          type="button"
          className="workspace-icon-button"
          aria-label="Back to conversation"
          onClick={onBack}
        >
          <ArrowLeft size={17} strokeWidth={1.7} />
        </button>
        <FileText size={17} strokeWidth={1.7} aria-hidden="true" />
        <strong>{name}</strong>
        {editable && link && (
          <button
            type="button"
            className="workspace-primary-file-edit-button"
            onClick={() => setEditingFileKey(editing ? null : currentFileKey)}
            aria-label={editing ? `View ${name}` : `Edit ${name}`}
          >
            {!editing && (
              <Pencil size={13} strokeWidth={1.8} aria-hidden="true" />
            )}
            {editing ? "View" : "Edit"}
          </button>
        )}
        {link && (
          <a
            href={link.url}
            target="_blank"
            rel="noopener noreferrer"
            download={link.inline ? undefined : name}
            aria-label={`Open ${name}`}
          >
            <Download size={16} strokeWidth={1.7} />
          </a>
        )}
      </header>
      <div
        className="workspace-primary-file-viewer-content"
        data-screen-context-kind={current ? "artifact" : undefined}
        data-screen-context-id={current?.name}
        data-screen-context-name={current ? name : undefined}
        data-screen-context-selected={current ? "true" : undefined}
      >
        <div className="workspace-document-stage">
          {error ? (
            <p role="alert">{error}</p>
          ) : listError && !file ? (
            <p role="alert">{status}</p>
          ) : !current ? (
            <p role={pending ? "status" : undefined}>
              {pending ? "Opening file…" : "The agent has no file to show yet."}
            </p>
          ) : !link ? (
            <p role="status">Opening preview…</p>
          ) : editing && activeThreadId ? (
            <OfficeEditor
              organizationId={organizationId}
              threadId={activeThreadId}
              name={current.name}
              mode="edit"
            />
          ) : (
            <Suspense fallback={<p role="status">Opening preview…</p>}>
              <DocumentPreview
                key={currentFileKey}
                url={link.url}
                name={name}
                contentType={link.contentType}
              />
            </Suspense>
          )}
        </div>
      </div>
    </div>
  )
}
