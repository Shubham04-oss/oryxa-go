// A room's sandbox, as a member sees it (A8): its files, a preview of a
// port it serves, and a shell. The agent runs code there; the member reads,
// looks and types into a terminal of their own.
import { lazy, Suspense, useState } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { ArrowUpRight, ChevronRight, Folder, RefreshCw } from "lucide-react"
import { FileKindBadge } from "./file-kind"
import { listFiles, openPreview, readFile } from "@/lib/live/children"
import type { FileEntry } from "@/lib/live/children"
import type { LiveRoom } from "@/lib/live/client"
import { cn } from "@/lib/utils"

const SandboxTerminal = lazy(() =>
  import("./sandbox-terminal").then((module) => ({
    default: module.SandboxTerminal,
  }))
)

type Tab = "Files" | "Preview" | "Terminal"
const tabs: Tab[] = ["Files", "Preview", "Terminal"]

export function SandboxPane({
  organizationId,
  room,
  sandbox,
}: {
  organizationId: string
  room: LiveRoom
  sandbox: string
}) {
  const [tab, setTab] = useState<Tab>("Files")
  return (
    <div className="live-pane">
      <div className="live-pane-tabs" role="tablist" aria-label="Sandbox views">
        {tabs.map((item) => (
          <button
            key={item}
            type="button"
            role="tab"
            aria-selected={tab === item}
            className={cn(tab === item && "is-active")}
            onClick={() => setTab(item)}
          >
            {item}
          </button>
        ))}
      </div>
      <div className="live-pane-body">
        {tab === "Files" && (
          <SandboxFiles
            organizationId={organizationId}
            room={room}
            sandbox={sandbox}
          />
        )}
        {tab === "Preview" && (
          <SandboxPreview
            organizationId={organizationId}
            room={room}
            sandbox={sandbox}
          />
        )}
        {tab === "Terminal" && (
          <Suspense fallback={<p className="live-quiet">Opening a shell…</p>}>
            <SandboxTerminal
              organizationId={organizationId}
              room={room}
              sandbox={sandbox}
            />
          </Suspense>
        )}
      </div>
    </div>
  )
}

function formatSize(size: number | undefined): string {
  if (size === undefined) return ""
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  return `${(size / 1024 / 1024).toFixed(1)} MB`
}

function SandboxFiles({
  organizationId,
  room,
  sandbox,
}: {
  organizationId: string
  room: LiveRoom
  sandbox: string
}) {
  const [path, setPath] = useState("")
  const [open, setOpen] = useState<FileEntry | null>(null)
  const files = useQuery({
    queryKey: [
      "live",
      organizationId,
      room.kind,
      room.id,
      "sandbox",
      sandbox,
      "files",
      path,
    ],
    queryFn: ({ signal }) =>
      listFiles(organizationId, room, sandbox, path, signal),
  })
  const content = useQuery({
    queryKey: [
      "live",
      organizationId,
      room.kind,
      room.id,
      "sandbox",
      sandbox,
      "file",
      open?.path,
    ],
    queryFn: () => readFile(organizationId, room, sandbox, open?.path ?? ""),
    enabled: open !== null,
  })
  const crumbs = (files.data?.path ?? path).split("/").filter(Boolean)

  if (open) {
    const file = content.data
    return (
      <div className="sandbox-file">
        <div className="sandbox-file-bar">
          <button
            type="button"
            className="live-link"
            onClick={() => setOpen(null)}
          >
            Files
          </button>
          <ChevronRight size={13} aria-hidden="true" />
          <FileKindBadge name={open.name} size="small" />
          <strong>{open.name}</strong>
          <small>{formatSize(open.size)}</small>
        </div>
        {content.isPending ? (
          <p className="live-quiet">Reading…</p>
        ) : content.isError || !file || file.error ? (
          <p className="live-quiet" role="alert">
            {file?.error ?? "Couldn't read this file."}
          </p>
        ) : file.encoding === "base64" &&
          file.mimeType?.startsWith("image/") ? (
          <div className="sandbox-file-image">
            <img
              src={`data:${file.mimeType};base64,${file.content ?? ""}`}
              alt={open.name}
            />
          </div>
        ) : file.encoding === "base64" ? (
          <p className="live-quiet">
            A binary file ({formatSize(file.size)}). Ask the agent to save it to
            the conversation to download it.
          </p>
        ) : (
          <pre className="sandbox-file-text">
            {file.content}
            {file.truncated ? "\n…" : ""}
          </pre>
        )}
      </div>
    )
  }

  return (
    <div className="sandbox-files">
      <nav className="sandbox-crumbs" aria-label="Folder">
        <button type="button" className="live-link" onClick={() => setPath("")}>
          Sandbox
        </button>
        {crumbs.map((crumb, index) => (
          <span key={`${index}-${crumb}`}>
            <ChevronRight size={13} aria-hidden="true" />
            <button
              type="button"
              className="live-link"
              onClick={() => setPath(crumbs.slice(0, index + 1).join("/"))}
            >
              {crumb}
            </button>
          </span>
        ))}
        <button
          type="button"
          className="live-icon"
          aria-label="Refresh files"
          onClick={() => void files.refetch()}
        >
          <RefreshCw size={13} aria-hidden="true" />
        </button>
      </nav>
      {files.isPending ? (
        <p className="live-quiet">Loading files…</p>
      ) : files.isError ? (
        <p className="live-quiet" role="alert">
          Couldn't list this sandbox's files.
        </p>
      ) : files.data.entries.length === 0 ? (
        <p className="live-quiet">Nothing here yet.</p>
      ) : (
        <ul className="sandbox-entries" aria-label="Files">
          {[...files.data.entries]
            .sort(
              (a, b) =>
                Number(b.type === "directory") -
                  Number(a.type === "directory") || a.name.localeCompare(b.name)
            )
            .map((entry) => (
              <li key={entry.path}>
                <button
                  type="button"
                  onClick={() =>
                    entry.type === "directory"
                      ? setPath(entry.path)
                      : setOpen(entry)
                  }
                >
                  {entry.type === "directory" ? (
                    <Folder size={15} aria-hidden="true" />
                  ) : (
                    <FileKindBadge name={entry.name} size="small" />
                  )}
                  <span>{entry.name}</span>
                  <small>
                    {entry.type === "directory" ? "" : formatSize(entry.size)}
                  </small>
                </button>
              </li>
            ))}
        </ul>
      )}
      {files.data?.truncated && (
        <p className="live-quiet">Showing the first files only.</p>
      )}
    </div>
  )
}

function SandboxPreview({
  organizationId,
  room,
  sandbox,
}: {
  organizationId: string
  room: LiveRoom
  sandbox: string
}) {
  const [port, setPort] = useState("3000")
  const preview = useMutation({
    mutationFn: (value: number) =>
      openPreview(organizationId, room, sandbox, value),
  })
  const number = Number(port)
  const valid = Number.isInteger(number) && number >= 1 && number <= 65535
  return (
    <div className="sandbox-preview">
      <form
        className="sandbox-preview-bar"
        onSubmit={(event) => {
          event.preventDefault()
          if (valid) preview.mutate(number)
        }}
      >
        <label>
          <span>Port</span>
          <input
            inputMode="numeric"
            value={port}
            aria-invalid={!valid}
            onChange={(event) => setPort(event.target.value.replace(/\D/g, ""))}
          />
        </label>
        <button
          type="submit"
          className="live-button"
          disabled={!valid || preview.isPending}
        >
          {preview.isPending
            ? "Opening…"
            : preview.data
              ? "Reload"
              : "Open preview"}
        </button>
        {preview.data && (
          <a
            className="live-link"
            href={preview.data.url}
            target="_blank"
            rel="noopener noreferrer"
          >
            Open in a new tab
            <ArrowUpRight size={13} aria-hidden="true" />
          </a>
        )}
      </form>
      {preview.isError ? (
        <p className="live-quiet" role="alert">
          Couldn't open a preview. Is something listening on port {port}?
        </p>
      ) : preview.data ? (
        <iframe
          key={preview.data.url}
          className="sandbox-preview-frame"
          src={preview.data.url}
          title={`Preview of port ${port}`}
          sandbox="allow-scripts allow-forms allow-same-origin allow-popups"
        />
      ) : (
        <p className="live-quiet">
          When the agent serves something in this sandbox, open its port here.
        </p>
      )}
    </div>
  )
}
