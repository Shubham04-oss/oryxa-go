// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import { codeLanguage, CodeViewer } from "./code-viewer"
import { previewKind } from "./document-preview"
import {
  ImageViewer,
  MediaPlayer,
  nextImageZoom,
  nextSpeed,
} from "./media-viewer"

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe("which viewer a file gets", () => {
  it("sends images, video, audio and code to their own viewers", () => {
    expect(previewKind("banner.png", "application/octet-stream")).toBe("image")
    expect(previewKind("photo", "image/webp")).toBe("image")
    // An SVG is shown as its source, never drawn as a page.
    expect(previewKind("logo.svg", "image/svg+xml")).toBe("code")
    expect(previewKind("walkthrough.mp4", "application/octet-stream")).toBe(
      "video"
    )
    expect(previewKind("clip", "video/webm")).toBe("video")
    expect(previewKind("memo.m4a", "application/octet-stream")).toBe("audio")
    expect(previewKind("retry.go", "application/octet-stream")).toBe("code")
    expect(previewKind("page.html", "text/html")).toBe("code")
    expect(previewKind("data.json", "application/json")).toBe("code")
    expect(previewKind("notes.md", "text/markdown")).toBe("markdown")
    expect(previewKind("prices.csv", "text/csv")).toBe("csv")
  })

  it("names a code file's language by its extension", () => {
    expect(codeLanguage("billing/retry.go")).toBe("go")
    expect(codeLanguage("App.TSX")).toBe("typescript")
    expect(codeLanguage("README")).toBeUndefined()
  })
})

describe("an image", () => {
  it("fits by default and zooms in steps from what fit comes to", () => {
    render(<ImageViewer url="/banner.png" name="Launch banner" />)
    const image = screen.getByRole("img", { name: "Launch banner" })
    Object.defineProperty(image, "naturalWidth", { value: 800 })
    Object.defineProperty(image, "naturalHeight", { value: 400 })
    fireEvent.load(image)

    const fit = screen.getByRole("button", { name: "Fit" })
    expect(fit.getAttribute("aria-pressed")).toBe("true")
    expect(screen.getByText("100%")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Zoom in" }))
    expect(screen.getByText("150%")).toBeTruthy()
    expect(image.style.width).toBe("1200px")
    expect(fit.getAttribute("aria-pressed")).toBe("false")
    fireEvent.click(fit)
    expect(image.style.width).toBe("")
  })

  it("says so when it can't be shown", () => {
    render(<ImageViewer url="/gone.png" name="Gone" />)
    fireEvent.error(screen.getByRole("img", { name: "Gone" }))
    expect(screen.getByRole("alert").textContent).toBe(
      "This image can’t be shown here."
    )
  })

  it("steps zoom and stops at the ends", () => {
    expect(nextImageZoom(1, 1)).toBe(1.5)
    expect(nextImageZoom(0.6, -1)).toBe(0.5)
    expect(nextImageZoom(4, 1)).toBe(4)
    expect(nextImageZoom(0.25, -1)).toBe(0.25)
  })
})

describe("video and audio", () => {
  it("plays in the browser's own player, with speed and full screen", () => {
    const { container } = render(
      <MediaPlayer
        url="/walkthrough.mp4"
        name="Onboarding walkthrough.mp4"
        kind="video"
        contentType="video/mp4"
      />
    )
    const video = container.querySelector("video")!
    expect(video.controls).toBe(true)
    fireEvent.click(screen.getByRole("button", { name: "Playback speed 1×" }))
    expect(video.playbackRate).toBe(1.25)
    expect(screen.getByRole("button", { name: "Full screen" })).toBeTruthy()
    expect(nextSpeed(2)).toBe(0.75)
    expect(nextSpeed(0.75)).toBe(1)
  })

  it("says plainly when the stored type can't play", () => {
    const { container } = render(
      <MediaPlayer
        url="/walkthrough.mp4"
        name="Onboarding walkthrough.mp4"
        kind="video"
        contentType="application/octet-stream"
      />
    )
    fireEvent.error(container.querySelector("video")!)
    expect(screen.getByRole("alert").textContent).toBe(
      "This video can’t play here, because it wasn’t stored as one the browser can play. Download it to watch it."
    )
  })

  it("gives audio no full screen", () => {
    const { container } = render(
      <MediaPlayer
        url="/memo.m4a"
        name="memo.m4a"
        kind="audio"
        contentType="audio/mp4"
      />
    )
    expect(container.querySelector("audio")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Full screen" })).toBeNull()
  })
})

describe("a code file", () => {
  const source =
    "package billing\n\n// Retry once.\nfunc retry() int {\n\treturn 3\n}"

  it("numbers its lines and highlights it", async () => {
    const { container } = render(
      <CodeViewer text={source} name="billing/retry.go" />
    )
    const lines = container.querySelectorAll(".code-lines li")
    expect(lines).toHaveLength(6)
    expect(lines[5].textContent).toBe("6")
    await waitFor(() =>
      expect(container.querySelector(".hljs-keyword")?.textContent).toBe(
        "package"
      )
    )
    expect(container.querySelector(".hljs-comment")?.textContent).toBe(
      "// Retry once."
    )
  })

  it("never runs what it shows", async () => {
    const { container } = render(
      <CodeViewer
        text={'<script>window.ran = true</script><img src=x onerror="1">'}
        name="page.html"
      />
    )
    await waitFor(() =>
      expect(container.querySelector(".hljs-tag")).toBeTruthy()
    )
    expect(container.querySelector("script")).toBeNull()
    expect(container.querySelector("img")).toBeNull()
  })

  it("wraps, and copies the file's text", async () => {
    const writeText = vi.fn(() => Promise.resolve())
    vi.stubGlobal("navigator", { clipboard: { writeText } })
    const { container } = render(<CodeViewer text={source} name="retry.go" />)
    fireEvent.click(screen.getByRole("button", { name: "Wrap" }))
    expect(
      container.querySelector(".code-viewer")?.getAttribute("data-wrap")
    ).toBe("true")
    fireEvent.click(screen.getByRole("button", { name: "Copy" }))
    expect(writeText).toHaveBeenCalledWith(source)
    expect(await screen.findByRole("button", { name: "Copied" })).toBeTruthy()
  })
})
