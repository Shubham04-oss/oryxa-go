import { lazy, Suspense, useEffect, useMemo, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Pencil } from "lucide-react"
import { Button } from "@/components/ui/button"
import { ApiError } from "@/lib/api/client"
import {
  fetchArtifactLink,
  threadDetailQueryOptions,
} from "@/lib/threads/client"
import type { ArtifactLink } from "@/lib/threads/client"
import { artifactFileName, buildTimeline } from "@/lib/threads/timeline"
import type { ArtifactEntry } from "@/lib/threads/timeline"
import { FileKindBadge } from "./file-kind"
import { FileViewerFrame } from "./file-viewer-frame"
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
 * Follow the chat's current thread. A new chat has none, and neither has one
 * that hasn't decided yet (`threadId` undefined, such as before the panel was
 * first expanded): which conversation it opens is the chat's to decide, not
 * something to guess from the list here.
 */
function useCurrentFiles(organizationId: string, threadId?: string | null) {
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

/** File entries on Agent overview; opening one uses its active work panel. */
export function PrimaryAgentFiles({
  organizationId,
  threadId,
  onOpenFile,
}: {
  organizationId: string
  threadId?: string | null
  onOpenFile?: (file: PrimaryAgentFileTarget) => void
}) {
  const { currentThreadId, files, status, error, pending } = useCurrentFiles(
    organizationId,
    threadId
  )
  return (
    <section
      className="workspace-primary-files-section"
      aria-labelledby="agents-overview-files"
    >
      <p className="workspace-eyebrow" id="agents-overview-files">
        Current conversation files
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
              <FileKindBadge name={file.name} size="small" />
              <span>{artifactFileName(file.name)}</span>
            </button>
          ))}
        </div>
      )}
    </section>
  )
}

/** The active viewer occupying the whole Agent overview work panel. */
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
  } = useCurrentFiles(organizationId, threadId)
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
    <FileViewerFrame
      name={name}
      backLabel="Back to Agent overview"
      onBack={onBack}
      actions={
        editable && link ? (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => setEditingFileKey(editing ? null : currentFileKey)}
            aria-label={editing ? `View ${name}` : `Edit ${name}`}
          >
            {!editing && <Pencil strokeWidth={1.8} aria-hidden="true" />}
            {editing ? "View" : "Edit"}
          </Button>
        ) : undefined
      }
      download={
        link
          ? { href: link.url, fileName: link.inline ? undefined : name }
          : undefined
      }
      context={
        current ? { kind: "artifact", id: current.name, name } : undefined
      }
    >
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
    </FileViewerFrame>
  )
}
