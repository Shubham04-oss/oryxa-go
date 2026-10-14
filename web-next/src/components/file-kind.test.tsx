// @vitest-environment jsdom

import { render } from "@testing-library/react"
import { describe, expect, it } from "vitest"
import { FileKindBadge, fileKind, fileKindLabel } from "./file-kind"

describe("fileKind", () => {
  it("names a file's kind by its extension", () => {
    expect(fileKind("EU launch plan.pdf")).toBe("pdf")
    expect(fileKind("pricing.XLSX")).toBe("sheet")
    expect(fileKind("board.pptx")).toBe("slides")
    expect(fileKind("diagram.png")).toBe("image")
    expect(fileKind("demo.mp4")).toBe("video")
    expect(fileKind("server.go")).toBe("code")
    expect(fileKind("notes.md")).toBe("doc")
    expect(fileKind("README")).toBe("doc")
  })

  it("labels the badge with at most four letters", () => {
    expect(fileKindLabel("plan.pdf")).toBe("PDF")
    expect(fileKindLabel("data.markdown")).toBe("MARK")
    expect(fileKindLabel("README")).toBe("DOC")
    expect(fileKindLabel(".env")).toBe("DOC")
  })

  it("draws a badge hidden from assistive technology", () => {
    const { container } = render(<FileKindBadge name="plan.pdf" />)
    const badge = container.querySelector(".workspace-file-kind")
    expect(badge?.getAttribute("data-kind")).toBe("pdf")
    expect(badge?.getAttribute("aria-hidden")).toBe("true")
    expect(badge?.textContent).toBe("PDF")
  })
})
