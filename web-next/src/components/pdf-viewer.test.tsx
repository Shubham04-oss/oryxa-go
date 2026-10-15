// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react"
import { FileViewerFrame } from "./file-viewer-frame"
import { findMatches, nextZoom, PdfViewer } from "./pdf-viewer"

type Matrix = number[]

// A stand-in for pdf.js: three US-letter-ish pages, each with one line.
const pageText: Record<number, string> = {
  1: "Introduction",
  2: "Pricing in EUR, and pricing in USD",
  3: "Appendix",
}
const renders = vi.hoisted(() => ({ count: 0 }))
vi.mock("pdfjs-dist", () => {
  const multiply = (a: Matrix, b: Matrix): Matrix => [
    a[0] * b[0] + a[2] * b[1],
    a[1] * b[0] + a[3] * b[1],
    a[0] * b[2] + a[2] * b[3],
    a[1] * b[2] + a[3] * b[3],
    a[0] * b[4] + a[2] * b[5] + a[4],
    a[1] * b[4] + a[3] * b[5] + a[5],
  ]
  const page = (number: number) => ({
    getViewport: ({ scale }: { scale: number }) => ({
      width: 600 * scale,
      height: 800 * scale,
      transform: [scale, 0, 0, -scale, 0, 800 * scale],
    }),
    render: () => {
      renders.count += 1
      return { promise: Promise.resolve(), cancel: () => {} }
    },
    getTextContent: () =>
      Promise.resolve({
        items: [
          {
            str: pageText[number],
            transform: [12, 0, 0, 12, 50, 700],
            width: pageText[number].length * 6,
          },
        ],
      }),
  })
  return {
    GlobalWorkerOptions: {},
    Util: { transform: multiply },
    getDocument: () => ({
      promise: Promise.resolve({
        numPages: 3,
        getPage: (number: number) => Promise.resolve(page(number)),
      }),
      destroy: () => Promise.resolve(),
    }),
  }
})
vi.mock("pdfjs-dist/build/pdf.worker.min.mjs?url", () => ({
  default: "/pdf.worker.js",
}))

beforeEach(() => {
  renders.count = 0
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(
    {} as CanvasRenderingContext2D
  )
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe("finding text in a PDF", () => {
  const pages = [
    [{ str: "Intro", transform: [], width: 30 }],
    [{ str: "Pricing in EUR, pricing in USD", transform: [], width: 180 }],
  ]

  it("finds every place, ignoring case, page by page", () => {
    expect(findMatches(pages, " PRICING ")).toEqual([
      { page: 1, item: 0, start: 0, length: 7 },
      { page: 1, item: 0, start: 16, length: 7 },
    ])
    expect(findMatches(pages, "")).toEqual([])
    expect(findMatches([undefined, ...pages], "intro")).toEqual([
      { page: 1, item: 0, start: 0, length: 5 },
    ])
  })
})

describe("zoom steps", () => {
  it("moves between steps and stops at the ends", () => {
    expect(nextZoom(1, 1)).toBe(1.25)
    expect(nextZoom(1, -1)).toBe(0.75)
    expect(nextZoom(1.1, -1)).toBe(1)
    expect(nextZoom(3, 1)).toBe(3)
    expect(nextZoom(0.5, -1)).toBe(0.5)
  })
})

describe("the PDF viewer", () => {
  async function open() {
    render(<PdfViewer bytes={new ArrayBuffer(8)} />)
    return screen.findByRole("toolbar", { name: "View" }, { timeout: 5000 })
  }

  it("shows every page with a thumbnail rail and a page counter", async () => {
    const tools = await open()
    expect(screen.getAllByRole("region", { name: /^Page \d$/ })).toHaveLength(3)
    const rail = screen.getByRole("navigation", { name: "Pages" })
    const thumbs = within(rail).getAllByRole("button")
    expect(thumbs).toHaveLength(3)
    expect(thumbs[0].getAttribute("aria-current")).toBe("page")
    expect(within(tools).getByText("1 / 3")).toBeTruthy()
    expect(
      within(tools).getByRole("button", {
        name: "Previous page",
      }).disabled
    ).toBe(true)

    fireEvent.click(within(tools).getByRole("button", { name: "Next page" }))
    expect(within(tools).getByText("2 / 3")).toBeTruthy()
    fireEvent.click(thumbs[2])
    expect(within(tools).getByText("3 / 3")).toBeTruthy()
    expect(thumbs[2].getAttribute("aria-current")).toBe("page")
    expect(renders.count).toBeGreaterThan(0)
  })

  it("zooms in steps and back to fit", async () => {
    const tools = await open()
    expect(within(tools).getByText("100%")).toBeTruthy()
    const fit = within(tools).getByRole("button", { name: "Fit" })
    expect(fit.getAttribute("aria-pressed")).toBe("true")

    fireEvent.click(within(tools).getByRole("button", { name: "Zoom in" }))
    expect(within(tools).getByText("125%")).toBeTruthy()
    expect(fit.getAttribute("aria-pressed")).toBe("false")
    const page = screen.getByRole("region", { name: "Page 1" })
    expect(page.style.width).toBe("750px")

    fireEvent.click(within(tools).getByRole("button", { name: "Zoom out" }))
    fireEvent.click(within(tools).getByRole("button", { name: "Zoom out" }))
    expect(within(tools).getByText("75%")).toBeTruthy()
    fireEvent.click(fit)
    expect(within(tools).getByText("100%")).toBeTruthy()
  })

  it("finds text, marks it on the page and steps through the matches", async () => {
    const tools = await open()
    fireEvent.click(
      within(tools).getByRole("button", { name: "Find in document" })
    )
    const field = within(tools).getByRole("searchbox", {
      name: "Find in document",
    })
    fireEvent.change(field, { target: { value: "pricing" } })
    expect(
      await within(tools).findByText("1 of 2", undefined, { timeout: 5000 })
    ).toBeTruthy()
    const page = screen.getByRole("region", { name: "Page 2" })
    const marks = page.querySelectorAll("mark")
    expect(marks).toHaveLength(2)
    expect(marks[0].getAttribute("data-current")).toBe("true")
    expect(await within(tools).findByText("2 / 3")).toBeTruthy()

    fireEvent.keyDown(field, { key: "Enter" })
    expect(within(tools).getByText("2 of 2")).toBeTruthy()
    expect(marks[1].getAttribute("data-current")).toBe("true")

    fireEvent.change(field, { target: { value: "nowhere" } })
    expect(within(tools).getByText("No matches")).toBeTruthy()
    expect(page.querySelectorAll("mark")).toHaveLength(0)

    fireEvent.keyDown(field, { key: "Escape" })
    expect(
      within(tools).getByRole("button", { name: "Find in document" })
    ).toBeTruthy()
  })
})

describe("the PDF viewer in the file frame", () => {
  it("puts its tools in the frame's bar", async () => {
    render(
      <FileViewerFrame name="brief.pdf" backLabel="Back" onBack={() => {}}>
        <PdfViewer bytes={new ArrayBuffer(8)} />
      </FileViewerFrame>
    )
    const bar = screen.getByRole("banner")
    expect(
      await within(bar).findByRole("button", { name: "Next page" })
    ).toBeTruthy()
    expect(screen.getAllByRole("toolbar", { name: "View" })).toHaveLength(1)
  })
})
