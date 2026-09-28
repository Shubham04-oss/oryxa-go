// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactElement } from "react"
import {
  PrimaryAgentFiles,
  PrimaryAgentFileViewer,
} from "./primary-agent-files"
import { threadKeys } from "@/lib/threads/client"
import type { ThreadDetail } from "@/lib/threads/client"
import { ApiError } from "@/lib/api/client"

const fetchArtifactLink = vi.hoisted(() => vi.fn())
const fetchArtifactEditorSession = vi.hoisted(() => vi.fn())
vi.mock("@/lib/threads/client", async (importOriginal) => ({
  ...(await importOriginal()),
  fetchArtifactLink,
  fetchArtifactEditorSession,
}))
vi.mock("./document-preview", () => ({
  DocumentPreview: ({ name }: { name: string }) => <div>Preview {name}</div>,
}))

afterEach(() => {
  cleanup()
  fetchArtifactLink.mockReset()
  fetchArtifactEditorSession.mockReset()
  delete window.DocsAPI
})

function withThread(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { staleTime: Infinity, retry: false } },
  })
  const detail: ThreadDetail = {
    id: "thread-1",
    title: "Draft",
    updated_at: "2026-09-28T12:00:00Z",
    pending_confirmations: [],
    items: [
      { type: "artifact", id: "event-1", artifact: "notes.txt", version: 1 },
      { type: "artifact", id: "event-2", artifact: "draft.txt", version: 2 },
    ],
  }
  client.setQueryData(threadKeys.detail("org-1", "thread-1"), detail)
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

describe("primary agent work files", () => {
  it("opens a selected file in the work viewer with its thread and version", async () => {
    const onOpenFile = vi.fn()
    withThread(
      <PrimaryAgentFiles
        organizationId="org-1"
        threadId="thread-1"
        onOpenFile={onOpenFile}
      />
    )
    fireEvent.click(await screen.findByRole("button", { name: "draft.txt" }))
    expect(onOpenFile).toHaveBeenCalledWith({
      threadId: "thread-1",
      name: "draft.txt",
      version: 2,
    })
  })

  it("uses the whole panel to preview the newest file and returns to overview", async () => {
    fetchArtifactEditorSession.mockRejectedValue(
      new ApiError("Not configured", 503, "document_editor_unavailable")
    )
    fetchArtifactLink.mockResolvedValue({
      url: "/artifacts/draft.txt",
      contentType: "text/plain",
      inline: true,
    })
    const onBack = vi.fn()
    withThread(
      <PrimaryAgentFileViewer
        organizationId="org-1"
        threadId="thread-1"
        onBack={onBack}
      />
    )
    await waitFor(() =>
      expect(fetchArtifactLink).toHaveBeenCalledWith("org-1", "thread-1", {
        name: "draft.txt",
        version: undefined,
      })
    )
    expect(await screen.findByText("Preview draft.txt")).toBeTruthy()
    expect(fetchArtifactEditorSession).not.toHaveBeenCalled()
    expect(screen.getByRole("button", { name: "Edit draft.txt" })).toBeTruthy()
    fireEvent.click(
      screen.getByRole("button", { name: "Back to Agent overview" })
    )
    expect(onBack).toHaveBeenCalledOnce()
  })

  it("opens a presentation for viewing, then enters and leaves editing on request", async () => {
    const created = vi.fn()
    const destroyed = vi.fn()
    window.DocsAPI = {
      DocEditor: class {
        constructor(id: string, config: Record<string, unknown>) {
          created(id, config)
          // Docs replaces the supplied element with its iframe. React must
          // continue to own only the stable wrapper around it.
          const mount = document.getElementById(id)
          if (!mount) throw new Error("Missing editor mount")
          mount.replaceWith(document.createElement("iframe"))
        }
        destroyEditor() {
          destroyed()
        }
      },
    }
    fetchArtifactLink.mockResolvedValue({
      url: "/artifacts/deck.pptx",
      contentType:
        "application/vnd.openxmlformats-officedocument.presentationml.presentation",
      inline: false,
    })
    fetchArtifactEditorSession.mockImplementation(
      (_org: string, _thread: string, _name: string, mode: string) =>
        Promise.resolve({
          server_url: "https://office.test",
          version: 3,
          config: {
            documentType: "slide",
            editorConfig: { mode },
            token: "signed-config",
          },
        })
    )
    const view = withThread(
      <PrimaryAgentFileViewer
        organizationId="org-1"
        file={{ threadId: "thread-1", name: "deck.pptx" }}
        onBack={vi.fn()}
      />
    )
    expect(await screen.findByText("Preview deck.pptx")).toBeTruthy()
    expect(created).not.toHaveBeenCalled()
    expect(fetchArtifactEditorSession).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Edit deck.pptx" }))
    await waitFor(() => expect(created).toHaveBeenCalledOnce())
    expect(created.mock.calls[0][1]).toMatchObject({
      documentType: "slide",
      editorConfig: { mode: "edit" },
      token: "signed-config",
    })
    expect(fetchArtifactEditorSession.mock.calls[0][3]).toBe("edit")
    fireEvent.click(screen.getByRole("button", { name: "View deck.pptx" }))
    expect(await screen.findByText("Preview deck.pptx")).toBeTruthy()
    expect(created).toHaveBeenCalledOnce()
    expect(destroyed).toHaveBeenCalledOnce()
    view.unmount()
    expect(destroyed).toHaveBeenCalledOnce()
  })
})
