// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import { DocumentPreview, previewKind } from "./document-preview"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
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
