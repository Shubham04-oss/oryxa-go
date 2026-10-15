// The Library's file table: one row per document, with its kind, name and
// where it came from, who added it and when it last changed.
import type { ReactNode } from "react"
import { LoaderCircle, Lock } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { Member } from "@/lib/auth/invitations"
import { formatBytes } from "@/lib/files/client"
import {
  documentOrigin,
  isAdminsOnlyDocument,
  isIndexing,
} from "@/lib/rag/client"
import type { Corpus, CorpusDocument } from "@/lib/rag/client"
import { FileKindBadge } from "./file-kind"
import {
  collectionTone,
  countLabel,
  documentKindName,
  documentTitle,
  memberLabel,
  originLabel,
  relativeTime,
} from "./knowledge-format"
import { personIdentity } from "./people-format"
import { IdentityAvatar } from "./workspace-avatar"

export type LibraryFile = {
  document: CorpusDocument
  /** Set in All files, where rows come from every collection. */
  collection?: Pick<Corpus, "id" | "name">
}

function StatusTags({ document }: { document: CorpusDocument }) {
  return (
    <>
      {isIndexing(document) && (
        <Badge variant="secondary" className="library-tag" data-tone="indexing">
          <LoaderCircle className="library-spin" aria-hidden="true" />
          Indexing…
        </Badge>
      )}
      {document.status === "failed" && (
        <Badge variant="secondary" className="library-tag" data-tone="failed">
          Failed
        </Badge>
      )}
      {isAdminsOnlyDocument(document) && (
        <Badge variant="secondary" className="library-tag">
          <Lock aria-hidden="true" />
          Admins only
        </Badge>
      )}
    </>
  )
}

function FileRow({
  file,
  members,
  actions,
  onOpen,
}: {
  file: LibraryFile
  members: readonly Member[]
  actions?: ReactNode
  onOpen?: () => void
}) {
  const { document, collection } = file
  const title = documentTitle(document)
  const origin = documentOrigin(document)
  const owner = memberLabel(members, document.created_by)
  const retrying = document.status === "pending" && document.attempts > 0
  return (
    <TableRow
      className="library-row"
      data-status={document.status}
      data-screen-context-kind="document"
      data-screen-context-id={document.id}
      data-screen-context-name={title}
    >
      <TableCell className="library-cell-kind">
        <FileKindBadge name={documentKindName(document)} />
      </TableCell>
      <TableCell className="library-cell-name">
        <span className="library-name">
          {onOpen ? (
            <Button
              variant="link"
              className="library-open"
              aria-label={`Open ${title}`}
              onClick={onOpen}
            >
              <strong>{title}</strong>
            </Button>
          ) : (
            <strong>{title}</strong>
          )}
          <StatusTags document={document} />
        </span>
        <small className="library-subline">
          {collection && (
            <>
              <span
                className="library-rail-dot"
                data-tone={collectionTone(collection.id)}
                aria-hidden="true"
              />
              {collection.name} ·{" "}
            </>
          )}
          {originLabel(origin, document)} · {formatBytes(document.byte_size)}
        </small>
        {document.status === "failed" && (
          <small className="library-subline" data-tone="failed">
            {document.failure_reason || "Indexing gave up without a reason."}
          </small>
        )}
        {retrying && (
          <small className="library-subline">
            {countLabel(document.attempts, "attempt")} didn’t finish. It will be
            tried again.
          </small>
        )}
      </TableCell>
      <TableCell className="library-cell-owner">
        {owner ? (
          <span className="library-owner">
            <IdentityAvatar
              identity={personIdentity(document.created_by, owner)}
            />
            <span>{owner}</span>
          </span>
        ) : (
          <span className="library-muted">—</span>
        )}
      </TableCell>
      <TableCell className="library-cell-updated">
        {relativeTime(document.updated_at)}
      </TableCell>
      <TableCell className="library-cell-actions">{actions}</TableCell>
    </TableRow>
  )
}

export function LibraryFileTable({
  files,
  members,
  label,
  actions,
  onOpen,
}: {
  files: readonly LibraryFile[]
  members: readonly Member[]
  /** Names the table for assistive technology. */
  label: string
  /** A row's own controls, such as its More menu. */
  actions?: (file: LibraryFile) => ReactNode
  /** Opens a file in the viewer. */
  onOpen?: (file: LibraryFile) => void
}) {
  return (
    <Table className="library-table" aria-label={label}>
      <TableHeader>
        <TableRow className="library-row library-row-head">
          <TableHead className="library-cell-kind">
            <span className="sr-only">Kind</span>
          </TableHead>
          <TableHead>Name</TableHead>
          <TableHead className="library-cell-owner">Owner</TableHead>
          <TableHead className="library-cell-updated">Updated</TableHead>
          <TableHead className="library-cell-actions">
            <span className="sr-only">Actions</span>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {files.map((file) => (
          <FileRow
            key={file.document.id}
            file={file}
            members={members}
            actions={actions?.(file)}
            onOpen={onOpen ? () => onOpen(file) : undefined}
          />
        ))}
      </TableBody>
    </Table>
  )
}
