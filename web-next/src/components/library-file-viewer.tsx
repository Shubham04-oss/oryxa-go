// A Library file, open in the shared frame. An uploaded file is read from
// files; a connected app's document from knowledge, which keeps its text.
// Rag does not give back an inline note's or a link's text, so those say so.
import { lazy, Suspense, useEffect, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import type { Member } from "@/lib/auth/invitations"
import { downloadFile, fileContentUrl, formatBytes } from "@/lib/files/client"
import { knowledgeDocumentQueryOptions } from "@/lib/knowledge/client"
import { documentOrigin, externalSourceLink } from "@/lib/rag/client"
import type { Corpus, CorpusDocument } from "@/lib/rag/client"
import { FileViewerFrame } from "./file-viewer-frame"
import type { Passage } from "./passage-view"
import type { FileViewerCrumb, FileViewerDownload } from "./file-viewer-frame"
import {
  documentKindName,
  documentTitle,
  memberLabel,
  originLabel,
} from "./knowledge-format"

const DocumentPreview = lazy(() =>
  import("./document-preview").then((module) => ({
    default: module.DocumentPreview,
  }))
)

/** A file open in the Library, with the collection it was opened from. */
export type OpenLibraryFile = {
  document: CorpusDocument
  collection?: Pick<Corpus, "id" | "name">
  /** A cited passage to open on, as byte offsets into the file's text. */
  passage?: Passage
}

/** A link only when it is a web address, so nothing else is followed. */
function webAddress(value: string | undefined): string | undefined {
  if (!value) return undefined
  try {
    const url = new URL(value)
    return url.protocol === "http:" || url.protocol === "https:"
      ? url.href
      : undefined
  } catch {
    return undefined
  }
}

function Preview({
  url,
  text,
  name,
  contentType,
  passage,
}: {
  url?: string
  text?: string
  name: string
  contentType: string
  passage?: Passage
}) {
  return (
    <Suspense fallback={<p role="status">Opening preview…</p>}>
      <DocumentPreview
        url={url}
        text={text}
        name={name}
        contentType={contentType}
        passage={passage}
      />
    </Suspense>
  )
}

/** A connected app's document: its text from knowledge. */
function AppDocument({
  organizationId,
  knowledgeId,
  document,
  passage,
  onOriginal,
}: {
  organizationId: string
  knowledgeId: string
  document: CorpusDocument
  passage?: Passage
  onOriginal: (href: string | undefined) => void
}) {
  const query = useQuery(
    knowledgeDocumentQueryOptions(organizationId, knowledgeId)
  )
  const mime = query.data?.mime || document.content_type
  const original = webAddress(query.data?.url)
  useEffect(() => onOriginal(original), [original, onOriginal])
  if (query.isPending) return <p role="status">Opening preview…</p>
  if (query.isError)
    return <p role="alert">Couldn’t read this document. Try again.</p>
  return (
    <Preview
      text={query.data.body}
      name={documentKindName({ ...document, content_type: mime })}
      contentType={mime}
      passage={passage}
    />
  )
}

export function LibraryFileViewer({
  organizationId,
  file,
  members,
  onBack,
  onOpenCollection,
  onOpenLibrary,
}: {
  organizationId: string
  file: OpenLibraryFile
  members: readonly Member[]
  onBack: () => void
  onOpenCollection?: (collection: Pick<Corpus, "id" | "name">) => void
  onOpenLibrary: () => void
}) {
  const { document, collection } = file
  const title = documentTitle(document)
  const origin = documentOrigin(document)
  const owner = memberLabel(members, document.created_by)
  const knowledgeId = document.metadata?.knowledge_document_id?.trim()
  const [appOriginal, setAppOriginal] = useState<string>()
  const [downloadError, setDownloadError] = useState("")

  const fileId = origin.kind === "file" ? origin.fileId : undefined
  const download: FileViewerDownload | undefined = fileId
    ? {
        onDownload: () => {
          setDownloadError("")
          // A request that can refresh the session goes first, then the
          // browser follows the content address (lib/files downloadFile).
          void downloadFile(organizationId, fileId).then(
            (url) => window.location.assign(url),
            () => setDownloadError("Couldn’t download this file. Try again.")
          )
        },
      }
    : undefined
  const originalHref =
    origin.kind === "app"
      ? (appOriginal ?? externalSourceLink(document.source))
      : origin.kind === "link"
        ? origin.href
        : undefined

  const crumbs: FileViewerCrumb[] = [
    { label: "Library", onSelect: onOpenLibrary },
    ...(collection
      ? [
          {
            label: collection.name,
            onSelect: onOpenCollection
              ? () => onOpenCollection(collection)
              : undefined,
          },
        ]
      : []),
  ]
  const meta = [
    owner,
    originLabel(origin, document),
    formatBytes(document.byte_size),
  ]
    .filter(Boolean)
    .join(" · ")

  return (
    <FileViewerFrame
      name={title}
      kindName={documentKindName(document)}
      meta={meta}
      crumbs={crumbs}
      backLabel={
        collection ? `Back to ${collection.name}` : "Back to the Library"
      }
      onBack={onBack}
      download={download}
      originalHref={originalHref}
      context={{ kind: "document", id: document.id, name: title }}
    >
      {downloadError ? (
        <p role="alert">{downloadError}</p>
      ) : fileId ? (
        <Preview
          url={fileContentUrl(organizationId, fileId)}
          name={documentKindName(document)}
          contentType={document.content_type}
          passage={file.passage}
        />
      ) : origin.kind === "app" && knowledgeId ? (
        <AppDocument
          organizationId={organizationId}
          knowledgeId={knowledgeId}
          document={document}
          passage={file.passage}
          onOriginal={setAppOriginal}
        />
      ) : origin.kind === "link" ? (
        <p>
          The Library keeps this link’s text for answers, not for reading here.
          {originalHref ? " Open the original to see the page." : ""}
        </p>
      ) : (
        <p>Preview isn’t available for notes yet.</p>
      )}
    </FileViewerFrame>
  )
}
