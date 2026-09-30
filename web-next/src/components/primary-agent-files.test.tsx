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
  isEditableDocument,
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
  vi.unstubAllGlobals()
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

describe("which conversation's files Files shows", () => {
  it("shows none for a new chat, without looking for the latest conversation", () => {
    const fetcher = vi.fn()
    vi.stubGlobal("fetch", fetcher)
    withThread(<PrimaryAgentFiles organizationId="org-1" threadId={null} />)
    const message = screen.getByText(
      "Start a conversation to see its files here."
    )
    // Nothing is loading, so nothing is announced as loading.
    expect(message.getAttribute("role")).toBeNull()
    expect(screen.queryByRole("button", { name: "draft.txt" })).toBeNull()
    expect(fetcher).not.toHaveBeenCalled()
  })

  it("shows none until the chat has chosen one, and does not guess from the list", () => {
    const fetcher = vi.fn()
    vi.stubGlobal("fetch", fetcher)
    withThread(<PrimaryAgentFiles organizationId="org-1" />)
    expect(
      screen.getByText("Start a conversation to see its files here.")
    ).toBeTruthy()
    expect(fetcher).not.toHaveBeenCalled()
  })

  it("lists the files of the thread the chat is on", async () => {
    withThread(<PrimaryAgentFiles organizationId="org-1" threadId="thread-1" />)
    expect(
      await screen.findByRole("button", { name: "draft.txt" })
    ).toBeTruthy()
    expect(screen.getByRole("button", { name: "notes.txt" })).toBeTruthy()
  })

  it("has no file to preview for a new chat", () => {
    withThread(
      <PrimaryAgentFileViewer
        organizationId="org-1"
        threadId={null}
        onBack={vi.fn()}
      />
    )
    expect(screen.getByText("The agent has no file to show yet.")).toBeTruthy()
    expect(fetchArtifactLink).not.toHaveBeenCalled()
  })
})

describe("a conversation's files", () => {
  it("opens a selected file in the viewer with its thread and version", async () => {
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

  it("previews the newest file in place of the conversation and returns to it", async () => {
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
    // A text file is never edited, so nothing writes a later version of it:
    // the version the conversation named is the one to show.
    await waitFor(() =>
      expect(fetchArtifactLink).toHaveBeenCalledWith("org-1", "thread-1", {
        name: "draft.txt",
        version: 2,
      })
    )
    expect(await screen.findByText("Preview draft.txt")).toBeTruthy()
    expect(fetchArtifactEditorSession).not.toHaveBeenCalled()
    expect(screen.queryByRole("button", { name: "Edit draft.txt" })).toBeNull()
    fireEvent.click(
      screen.getByRole("button", { name: "Back to conversation" })
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

describe("which files can be edited", () => {
  it("names only the formats ONLYOFFICE saves back as they were", () => {
    for (const name of [
      "report.docx",
      "budget.xlsx",
      "deck.pptx",
      "REPORT.DOCX",
      "notes.v2.pptx",
    ])
      expect(isEditableDocument(name), name).toBe(true)
    for (const name of [
      "old.doc",
      "notes.odt",
      "letter.rtf",
      "old.xls",
      "macros.xlsm",
      "sheet.ods",
      "data.csv",
      "old.ppt",
      "show.ppsx",
      "talk.odp",
      "paper.pdf",
      "notes.txt",
      "plan.md",
      "image.png",
      "noextension",
      "docx",
    ])
      expect(isEditableDocument(name), name).toBe(false)
  })

  it.each([
    ["report.docx", true],
    ["budget.xlsx", true],
    ["deck.pptx", true],
    ["old.doc", false],
    ["notes.odt", false],
    ["data.csv", false],
    ["paper.pdf", false],
    ["plan.md", false],
  ])("offers Edit on %s: %s", async (name: string, editable: boolean) => {
    fetchArtifactLink.mockResolvedValue({
      url: `/artifacts/${name}`,
      contentType: "application/octet-stream",
      inline: false,
    })
    withThread(
      <PrimaryAgentFileViewer
        organizationId="org-1"
        threadId="thread-1"
        file={{ threadId: "thread-1", name, version: 3 }}
        onBack={vi.fn()}
      />
    )
    expect(await screen.findByText(`Preview ${name}`)).toBeTruthy()
    expect(!!screen.queryByRole("button", { name: `Edit ${name}` })).toBe(
      editable
    )
    // Only a file that can be edited follows its latest version, which is
    // what an edit saves; any other is the version it was made as.
    expect(fetchArtifactLink).toHaveBeenCalledWith("org-1", "thread-1", {
      name,
      version: editable ? undefined : 3,
    })
  })
})
