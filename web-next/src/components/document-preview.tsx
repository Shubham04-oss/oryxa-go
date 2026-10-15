import { useEffect, useMemo, useState } from "react"
import type { PresentationData } from "@office-kit/pptx"
import Papa from "papaparse"
import { AgentMarkdown } from "./agent-timeline"
import { CodeViewer, codeExtensions } from "./code-viewer"
import { ImageViewer, MediaPlayer } from "./media-viewer"
import { PassageView } from "./passage-view"
import type { Passage } from "./passage-view"
import { PdfViewer } from "./pdf-viewer"
import { SheetViewer } from "./sheet-viewer"
import type { Sheet } from "./sheet-viewer"
import { SlidesViewer } from "./slides-viewer"
import type { SlideImage } from "./slides-viewer"

// The Word, Excel and PowerPoint readers and the PDF worker script are
// megabytes that only a browser ever runs. `import.meta.env.SSR` is decided
// when the app is built: in the Worker's build it is true, so each import
// below sits in a branch that is never taken and is left out, with the
// library behind it, instead of being uploaded with the Worker on every
// deploy. Every import keeps its own conditional, in the same expression, so
// the bundler can see that its branch is dead.
function unavailableOnTheServer(): never {
  throw new Error("The document preview runs in the browser.")
}
const loadDocxPreview = () =>
  import.meta.env.SSR ? unavailableOnTheServer() : import("docx-preview")
const loadPresentationLibraries = () =>
  import.meta.env.SSR
    ? unavailableOnTheServer()
    : Promise.all([
        import("@office-kit/pptx"),
        import("@office-kit/pptx-preview"),
      ])
const loadExcel = () =>
  import.meta.env.SSR ? unavailableOnTheServer() : import("exceljs")

type PreviewKind =
  | "image"
  | "video"
  | "audio"
  | "code"
  | "pdf"
  | "docx"
  | "pptx"
  | "xlsx"
  | "csv"
  | "markdown"
  | "text"
  | "unsupported"

// An SVG shows as its source (code), so what it draws is never fetched.
const imageExtensions = new Set(["png", "jpg", "jpeg", "gif", "webp", "avif"])
const videoExtensions = new Set(["mp4", "m4v", "mov", "webm"])
const audioExtensions = new Set([
  "mp3",
  "m4a",
  "wav",
  "ogg",
  "oga",
  "aac",
  "flac",
])

export function previewKind(name: string, contentType: string): PreviewKind {
  const extension = name.split(".").at(-1)?.toLowerCase() ?? ""
  if (
    /^image\/(png|jpeg|gif|webp|avif)\b/i.test(contentType) ||
    imageExtensions.has(extension)
  )
    return "image"
  if (/^video\//i.test(contentType) || videoExtensions.has(extension))
    return "video"
  if (/^audio\//i.test(contentType) || audioExtensions.has(extension))
    return "audio"
  if (extension === "pdf") return "pdf"
  if (extension === "docx") return "docx"
  if (extension === "pptx" || extension === "ppsx") return "pptx"
  if (extension === "xlsx" || extension === "xlsm") return "xlsx"
  if (extension === "csv") return "csv"
  if (extension === "md" || extension === "markdown") return "markdown"
  if (codeExtensions.has(extension)) return "code"
  if (extension === "txt" || /^text\/plain\b/i.test(contentType)) return "text"
  return "unsupported"
}

/**
 * The most of a file the viewer reads into memory to decode. A larger one
 * would freeze the tab while it was read and drawn, so the viewer says so and
 * leaves the file to a download.
 */
export const MAX_PREVIEW_BYTES = 25 * 1024 * 1024
const tooLargeMessage = `This file is over ${MAX_PREVIEW_BYTES / (1024 * 1024)} MB, too large to preview here. Download it to open it.`

class PreviewTooLarge extends Error {}

/** The response's body, unless it runs past the cap: it is not read further than that. */
async function readCapped(response: Response): Promise<ArrayBuffer> {
  // A server that says how big the file is saves reading any of it.
  if (Number(response.headers.get("content-length")) > MAX_PREVIEW_BYTES) {
    void response.body?.cancel()
    throw new PreviewTooLarge()
  }
  if (!response.body) {
    const bytes = await response.arrayBuffer()
    if (bytes.byteLength > MAX_PREVIEW_BYTES) throw new PreviewTooLarge()
    return bytes
  }
  // One that does not, or is wrong about it, is stopped at the cap.
  const reader = response.body.getReader()
  const chunks: Uint8Array[] = []
  let total = 0
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    total += value.byteLength
    if (total > MAX_PREVIEW_BYTES) {
      void reader.cancel()
      throw new PreviewTooLarge()
    }
    chunks.push(value)
  }
  const bytes = new Uint8Array(total)
  let offset = 0
  for (const chunk of chunks) {
    bytes.set(chunk, offset)
    offset += chunk.byteLength
  }
  return bytes.buffer
}

function useFileBytes(url: string, enabled: boolean) {
  const [result, setResult] = useState<{ bytes?: ArrayBuffer; error?: string }>(
    {}
  )
  useEffect(() => {
    if (!enabled) return
    const controller = new AbortController()
    setResult({})
    void fetch(url, { credentials: "include", signal: controller.signal })
      .then(async (response) => {
        if (!response.ok)
          throw new Error(`File request failed: ${response.status}`)
        const bytes = await readCapped(response)
        if (!controller.signal.aborted) setResult({ bytes })
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted)
          setResult({
            error:
              error instanceof PreviewTooLarge
                ? tooLargeMessage
                : "Could not load this preview.",
          })
      })
    return () => controller.abort()
  }, [url, enabled])
  return result
}

/**
 * What a Word document is drawn into: a page of its own, with nothing the
 * document holds able to reach out of it. The sandbox on the frame that shows
 * it gives it no scripts, popups or navigation of the app; this page adds that
 * it may fetch nothing and load only what the document itself carries (its
 * images and fonts, as blob or data URLs). The styles are the app's own
 * surround for the pages: the dark ground, no shadow, no padding of the
 * library's.
 */
const docxFrameDocument = `<!doctype html><html><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data: blob:; font-src data: blob:; style-src 'unsafe-inline'">
<style>
html, body { margin: 0; background: #252525; }
body { box-sizing: border-box; padding: 20px; }
body .docx-wrapper { padding: 0; background: transparent; }
body .docx-wrapper > section.docx { max-width: 100%; margin: 0 auto 16px; box-shadow: none; }
</style></head><body></body></html>`

// Where a link in a document may go: a web page, a mail address, or a place in
// the document itself. Anything else (javascript:, data:, a relative path)
// loses its address.
const linkTarget = /^(?:https?:|mailto:|#)/i

function neutralizeLinks(root: HTMLElement) {
  for (const link of root.querySelectorAll("a[href], area[href]")) {
    const href = link.getAttribute("href")?.trim() ?? ""
    if (linkTarget.test(href)) {
      link.setAttribute("href", href)
      link.setAttribute("rel", "noopener noreferrer")
      link.removeAttribute("target")
    } else {
      link.removeAttribute("href")
    }
    link.removeAttributeNS("http://www.w3.org/1999/xlink", "href")
  }
}

function DocxPreview({ bytes, name }: { bytes: ArrayBuffer; name: string }) {
  // The frame's body, once its document has loaded: what the Word document is
  // drawn into, and all of the page it may touch.
  const [body, setBody] = useState<HTMLElement | null>(null)
  const [error, setError] = useState("")
  const [ready, setReady] = useState(false)
  useEffect(() => {
    if (!body) return
    let cancelled = false
    const frameDocument = body.ownerDocument
    body.replaceChildren()
    setError("")
    setReady(false)
    // The frame may not open a page, so a link is opened from here, by the
    // app, in a tab of its own; a place in the document stays in the frame.
    const openLink = (event: MouseEvent) => {
      const link = (event.target as Element | null)?.closest("a[href]")
      const href = link?.getAttribute("href") ?? ""
      if (!link || href.startsWith("#")) return
      event.preventDefault()
      // Every address was checked when the document was drawn; one that
      // reached this far another way is not opened from the app.
      if (!linkTarget.test(href)) return
      if (/^mailto:/i.test(href)) window.location.href = href
      else window.open(href, "_blank", "noopener,noreferrer")
    }
    frameDocument.addEventListener("click", openLink)
    void loadDocxPreview()
      .then(({ renderAsync }) =>
        renderAsync(bytes, body, undefined, { renderAltChunks: false })
      )
      .then(() => {
        if (cancelled) return
        neutralizeLinks(body)
        setReady(true)
      })
      .catch(() => {
        if (!cancelled) setError("Could not render this document.")
      })
    return () => {
      cancelled = true
      frameDocument.removeEventListener("click", openLink)
      body.replaceChildren()
    }
  }, [bytes, body])
  return (
    <div className="workspace-document-scroll workspace-document-word">
      {error && <p role="alert">{error}</p>}
      {!error && !ready && <p role="status">Rendering document…</p>}
      <iframe
        className="workspace-document-frame"
        title={name}
        sandbox="allow-same-origin"
        srcDoc={docxFrameDocument}
        data-ready={ready}
        onLoad={(event) =>
          setBody(event.currentTarget.contentDocument?.body ?? null)
        }
      />
    </div>
  )
}

function PptxPreview({ bytes }: { bytes: ArrayBuffer }) {
  const [slides, setSlides] = useState<SlideImage[]>([])
  const [error, setError] = useState("")
  const [ready, setReady] = useState(false)
  useEffect(() => {
    let cancelled = false
    let urls: string[] = []
    setSlides([])
    setError("")
    setReady(false)
    void loadPresentationLibraries()
      .then(async ([pptx, preview]) => {
        const presentation: PresentationData =
          await pptx.loadPresentation(bytes)
        const pages = pptx.getSlides(presentation)
        urls = pages.map((slide) =>
          URL.createObjectURL(
            new Blob([preview.renderSlideToSvg(presentation, slide)], {
              type: "image/svg+xml",
            })
          )
        )
        if (cancelled) urls.forEach(URL.revokeObjectURL)
        else {
          setSlides(
            urls.map((url, number) => ({
              url,
              notes: pptx.getSlideNotes(pages[number]),
            }))
          )
          setReady(true)
        }
      })
      .catch(() => {
        if (!cancelled) setError("Could not render this presentation.")
      })
    return () => {
      cancelled = true
      urls.forEach(URL.revokeObjectURL)
    }
  }, [bytes])
  if (error) return <p role="alert">{error}</p>
  if (!slides.length)
    return (
      <p role={ready ? undefined : "status"}>
        {ready ? "This presentation has no slides." : "Rendering presentation…"}
      </p>
    )
  return <SlidesViewer slides={slides} />
}

/** A cell's formula, when it has one; shared formulas give their own. */
function cellFormula(cell: { formula?: unknown }): string | undefined {
  return typeof cell.formula === "string" && cell.formula
    ? cell.formula
    : undefined
}

function SpreadsheetPreview({ bytes }: { bytes: ArrayBuffer }) {
  const [sheets, setSheets] = useState<Sheet[]>([])
  const [error, setError] = useState("")
  const [ready, setReady] = useState(false)
  useEffect(() => {
    let cancelled = false
    setSheets([])
    setError("")
    setReady(false)
    void loadExcel()
      .then(async (module) => {
        const workbook = new module.default.Workbook()
        await workbook.xlsx.load(bytes)
        const parsed = workbook.worksheets.map((worksheet) => {
          const rowCount = Math.min(worksheet.rowCount, 300)
          const columnCount = Math.min(worksheet.columnCount, 40)
          const rowIndexes = Array.from(
            { length: rowCount },
            (_, index) => index
          )
          const columnIndexes = Array.from(
            { length: columnCount },
            (_, index) => index
          )
          return {
            name: worksheet.name,
            clipped:
              worksheet.rowCount > rowCount ||
              worksheet.columnCount > columnCount,
            rows: rowIndexes.map((rowIndex) =>
              columnIndexes.map(
                (columnIndex) =>
                  worksheet.getRow(rowIndex + 1).getCell(columnIndex + 1).text
              )
            ),
            formulas: rowIndexes.map((rowIndex) =>
              columnIndexes.map((columnIndex) =>
                cellFormula(
                  worksheet.getRow(rowIndex + 1).getCell(columnIndex + 1)
                )
              )
            ),
          }
        })
        if (!cancelled) {
          setSheets(parsed)
          setReady(true)
        }
      })
      .catch(() => {
        if (!cancelled) setError("Could not render this spreadsheet.")
      })
    return () => {
      cancelled = true
    }
  }, [bytes])
  return error ? (
    <p role="alert">{error}</p>
  ) : sheets.length ? (
    <SheetViewer sheets={sheets} />
  ) : (
    <p role={ready ? undefined : "status"}>
      {ready ? "This spreadsheet is empty." : "Rendering spreadsheet…"}
    </p>
  )
}

export function DocumentPreview({
  url = "",
  text,
  name,
  contentType,
  passage,
}: {
  /** Where the file's bytes are read from. */
  url?: string
  /** The file's text, when the page already holds it; nothing is fetched. */
  text?: string
  name: string
  contentType: string
  /**
   * A cited passage, as byte offsets into the file's text: a text file
   * opens on it, marked. Other kinds open as they are.
   */
  passage?: Passage
}) {
  const kind = previewKind(name, contentType)
  const fetched = useFileBytes(
    url,
    text === undefined &&
      kind !== "image" &&
      kind !== "video" &&
      kind !== "audio" &&
      kind !== "unsupported"
  )
  const held = useMemo(
    () =>
      text === undefined ? undefined : new TextEncoder().encode(text).buffer,
    [text]
  )
  const bytes = held ?? fetched.bytes
  const error = held ? undefined : fetched.error
  if (kind === "image") return <ImageViewer url={url} name={name} />
  if (kind === "video" || kind === "audio")
    return (
      <MediaPlayer
        url={url}
        name={name}
        kind={kind}
        contentType={contentType}
      />
    )
  if (kind === "unsupported")
    return (
      <p>
        Preview isn’t available for this format. Download the file to open it.
      </p>
    )
  if (error) return <p role="alert">{error}</p>
  if (!bytes) return <p role="status">Opening preview…</p>
  if (kind === "pdf") return <PdfViewer bytes={bytes} />
  if (kind === "docx") return <DocxPreview bytes={bytes} name={name} />
  if (kind === "pptx") return <PptxPreview bytes={bytes} />
  if (kind === "xlsx") return <SpreadsheetPreview bytes={bytes} />
  const decoded = new TextDecoder().decode(bytes)
  const view = textView(kind, decoded, name)
  if (passage) {
    return (
      <PassageView
        bytes={bytes}
        passage={passage}
        wholeLabel={kind === "markdown" ? "Formatted" : "Whole file"}
      >
        {view}
      </PassageView>
    )
  }
  return view
}

/** A text file as its kind reads best: Markdown, a table, code or text. */
function textView(kind: PreviewKind, decoded: string, name: string) {
  if (kind === "code") return <CodeViewer text={decoded} name={name} />
  if (kind === "markdown")
    return (
      <article className="workspace-document-scroll workspace-document-prose">
        <AgentMarkdown text={decoded} />
      </article>
    )
  if (kind === "csv") {
    // CSV is data, not HTML: React escapes every parsed cell.
    const parsed = Papa.parse<string[]>(decoded, { skipEmptyLines: false }).data
    const rows = parsed.slice(0, 300)
    return (
      <SheetViewer
        sheets={[
          {
            name: "CSV",
            rows: rows.map((row) => row.slice(0, 40)),
            clipped: parsed.length > 300 || rows.some((row) => row.length > 40),
          },
        ]}
      />
    )
  }
  return (
    <pre className="workspace-document-scroll workspace-document-text">
      {decoded}
    </pre>
  )
}
