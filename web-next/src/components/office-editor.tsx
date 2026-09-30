import { useEffect, useId, useRef, useState } from "react"
import type { ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { ApiError } from "@/lib/api/client"
import { fetchArtifactEditorSession } from "@/lib/threads/client"

type DocumentEditor = { destroyEditor?: () => void }
type DocsAPI = {
  DocEditor: new (id: string, config: Record<string, unknown>) => DocumentEditor
}

declare global {
  interface Window {
    DocsAPI?: DocsAPI
  }
}

const loadingScripts = new Map<string, Promise<void>>()

function loadEditorScript(serverUrl: string): Promise<void> {
  if (window.DocsAPI?.DocEditor) return Promise.resolve()
  let url: URL
  try {
    url = new URL("/web-apps/apps/api/documents/api.js", serverUrl)
  } catch {
    return Promise.reject(new Error("Invalid document server URL"))
  }
  if (!/^https?:$/.test(url.protocol)) {
    return Promise.reject(new Error("Invalid document server URL"))
  }
  const key = url.toString()
  const existing = loadingScripts.get(key)
  if (existing) return existing
  const script = document.createElement("script")
  script.src = key
  script.async = true
  const loaded = new Promise<void>((resolve, reject) => {
    script.onload = () => {
      if (window.DocsAPI?.DocEditor) resolve()
      else reject(new Error("Document editor did not initialize"))
    }
    script.onerror = () => reject(new Error("Document editor could not load"))
  }).catch((error: unknown) => {
    loadingScripts.delete(key)
    script.remove()
    throw error
  })
  loadingScripts.set(key, loaded)
  document.head.appendChild(script)
  return loaded
}

/** The established Office editor fills the file viewer. */
export function OfficeEditor({
  organizationId,
  threadId,
  name,
  mode,
  fallback,
}: {
  organizationId: string
  threadId: string
  name: string
  mode: "view" | "edit"
  fallback?: ReactNode
}) {
  const id = `oryxa-office-${useId().replace(/:/g, "")}`
  const hostRef = useRef<HTMLDivElement>(null)
  const [editorError, setEditorError] = useState("")
  const [ready, setReady] = useState(false)
  const session = useQuery({
    queryKey: ["artifact-editor", organizationId, threadId, name, mode],
    queryFn: ({ signal }) =>
      fetchArtifactEditorSession(organizationId, threadId, name, mode, signal),
    staleTime: Infinity,
    refetchOnMount: "always",
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    retry: false,
  })

  useEffect(() => {
    if (!session.data || session.isFetching || session.isError) return
    let cancelled = false
    let editor: DocumentEditor | undefined
    const host = hostRef.current
    setReady(false)
    setEditorError("")
    void loadEditorScript(session.data.server_url).then(
      () => {
        if (cancelled || !window.DocsAPI) return
        try {
          if (!host) return
          const mount = document.createElement("div")
          mount.id = id
          mount.className = "workspace-office-editor-mount"
          host.replaceChildren(mount)
          editor = new window.DocsAPI.DocEditor(id, {
            ...session.data.config,
            events: {
              onDocumentReady: () => setReady(true),
              onError: (event: { data?: { errorDescription?: string } }) => {
                editor?.destroyEditor?.()
                setEditorError(
                  event.data?.errorDescription ||
                    "Could not open this document."
                )
              },
            },
          })
        } catch {
          setEditorError("Could not start the document editor.")
        }
      },
      () => {
        if (!cancelled) setEditorError("Could not reach the document editor.")
      }
    )
    return () => {
      cancelled = true
      editor?.destroyEditor?.()
      host?.replaceChildren()
    }
  }, [id, session.data, session.isFetching, session.isError])

  const unavailable =
    session.error instanceof ApiError && session.error.status === 503
  return (
    <div className="workspace-office-editor">
      {session.isFetching && <p role="status">Opening document…</p>}
      {session.isError && !fallback && (
        <p role="alert">
          {unavailable
            ? "Document editing is not configured yet. Download this file to work on it."
            : session.error instanceof ApiError && session.error.status === 415
              ? "This format cannot be edited here."
              : "Could not open this document. Try again."}
        </p>
      )}
      {session.isError && fallback}
      {editorError && !fallback && <p role="alert">{editorError}</p>}
      {editorError && fallback}
      {session.data && !session.isFetching && !ready && !editorError && (
        <p role="status">Loading editor…</p>
      )}
      {!session.isError && !editorError && (
        <div ref={hostRef} className="workspace-office-editor-canvas" />
      )}
    </div>
  )
}
