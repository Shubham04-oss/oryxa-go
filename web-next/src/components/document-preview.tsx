import { useEffect, useRef, useState } from "react"
import { ChevronLeft, ChevronRight } from "lucide-react"
import Markdown from "react-markdown"
import remarkGfm from "remark-gfm"
import type {
  PDFDocumentLoadingTask,
  PDFDocumentProxy,
  RenderTask,
} from "pdfjs-dist"
import type { PresentationData } from "@office-kit/pptx"
import Papa from "papaparse"

// PDF.js, the Word, Excel and PowerPoint readers and the PDF worker script are
// megabytes that only a browser ever runs. `import.meta.env.SSR` is decided
// when the app is built: in the Worker's build it is true, so each import
// below sits in a branch that is never taken and is left out, with the
// library behind it, instead of being uploaded with the Worker on every
// deploy. Every import keeps its own conditional, in the same expression, so
// the bundler can see that its branch is dead.
function unavailableOnTheServer(): never {
  throw new Error("The document preview runs in the browser.")
}
const loadPdfjs = () =>
  import.meta.env.SSR ? unavailableOnTheServer() : import("pdfjs-dist")
const loadPdfWorker = () =>
  import.meta.env.SSR
    ? unavailableOnTheServer()
    : import("pdfjs-dist/build/pdf.worker.min.mjs?url")
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
  | "pdf"
  | "docx"
  | "pptx"
  | "xlsx"
  | "csv"
  | "markdown"
  | "text"
  | "unsupported"

export function previewKind(name: string, contentType: string): PreviewKind {
  const extension = name.split(".").at(-1)?.toLowerCase()
  if (/^image\/(png|jpeg|gif|webp|avif)\b/i.test(contentType)) return "image"
  if (extension === "pdf") return "pdf"
  if (extension === "docx") return "docx"
  if (extension === "pptx" || extension === "ppsx") return "pptx"
  if (extension === "xlsx" || extension === "xlsm") return "xlsx"
  if (extension === "csv") return "csv"
  if (extension === "md" || extension === "markdown") return "markdown"
  if (extension === "txt" || /^text\/plain\b/i.test(contentType)) return "text"
  return "unsupported"
}

function PageNavigation({
  index,
  count,
  label,
  onChange,
}: {
  index: number
  count: number
  label: string
  onChange: (index: number) => void
}) {
  if (count < 2) return null
  return (
    <nav
      className="workspace-document-pages"
      aria-label={`${label} navigation`}
    >
      <button
        type="button"
        aria-label={`Previous ${label.toLowerCase()}`}
        disabled={index === 0}
        onClick={() => onChange(index - 1)}
      >
        <ChevronLeft size={17} />
      </button>
      <span>
        {label} {index + 1} of {count}
      </span>
      <button
        type="button"
        aria-label={`Next ${label.toLowerCase()}`}
        disabled={index === count - 1}
        onClick={() => onChange(index + 1)}
      >
        <ChevronRight size={17} />
      </button>
    </nav>
  )
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
        const bytes = await response.arrayBuffer()
        if (!controller.signal.aborted) setResult({ bytes })
      })
      .catch(() => {
        if (!controller.signal.aborted)
          setResult({ error: "Could not load this preview." })
      })
    return () => controller.abort()
  }, [url, enabled])
  return result
}

function PdfPreview({ bytes }: { bytes: ArrayBuffer }) {
  const canvas = useRef<HTMLCanvasElement>(null)
  const frame = useRef<HTMLDivElement>(null)
  const [pdf, setPdf] = useState<PDFDocumentProxy | null>(null)
  const [index, setIndex] = useState(0)
  const [error, setError] = useState("")
  const [width, setWidth] = useState(0)
  useEffect(() => {
    let cancelled = false
    let loading: PDFDocumentLoadingTask | undefined
    setPdf(null)
    setIndex(0)
    setError("")
    void Promise.all([loadPdfjs(), loadPdfWorker()])
      .then(async ([pdfjs, worker]) => {
        pdfjs.GlobalWorkerOptions.workerSrc = worker.default
        loading = pdfjs.getDocument({ data: new Uint8Array(bytes) })
        const document = await loading.promise
        if (!cancelled) setPdf(document)
      })
      .catch(() => {
        if (!cancelled) setError("Could not render this PDF.")
      })
    return () => {
      cancelled = true
      void loading?.destroy()
    }
  }, [bytes])
  useEffect(() => {
    if (!frame.current) return
    const observer = new ResizeObserver(() =>
      setWidth(frame.current?.clientWidth ?? 0)
    )
    observer.observe(frame.current)
    setWidth(frame.current.clientWidth)
    return () => observer.disconnect()
  }, [])
  useEffect(() => {
    if (!pdf || !canvas.current || !width) return
    let cancelled = false
    let render: RenderTask | undefined
    void pdf
      .getPage(index + 1)
      .then(async (page) => {
        if (cancelled || !canvas.current) return
        const base = page.getViewport({ scale: 1 })
        const viewport = page.getViewport({
          scale: Math.min(1.6, Math.max(0.25, (width - 32) / base.width)),
        })
        const pixelRatio = window.devicePixelRatio || 1
        const element = canvas.current
        element.width = Math.ceil(viewport.width * pixelRatio)
        element.height = Math.ceil(viewport.height * pixelRatio)
        element.style.width = `${viewport.width}px`
        element.style.height = `${viewport.height}px`
        const context = element.getContext("2d")
        if (!context) throw new Error("Canvas unavailable")
        render = page.render({
          canvas: element,
          canvasContext: context,
          viewport,
          transform: [pixelRatio, 0, 0, pixelRatio, 0, 0],
        })
        await render.promise
      })
      .catch(() => {
        if (!cancelled) setError("Could not render this PDF page.")
      })
    return () => {
      cancelled = true
      render?.cancel()
    }
  }, [pdf, index, width])
  return (
    <div className="workspace-document-preview">
      <div className="workspace-document-scroll" ref={frame}>
        {error ? (
          <p role="alert">{error}</p>
        ) : (
          <canvas ref={canvas} aria-label={`PDF page ${index + 1}`} />
        )}
      </div>
      <PageNavigation
        index={index}
        count={pdf?.numPages ?? 0}
        label="Page"
        onChange={setIndex}
      />
    </div>
  )
}

function DocxPreview({ bytes }: { bytes: ArrayBuffer }) {
  const host = useRef<HTMLDivElement>(null)
  const [error, setError] = useState("")
  const [ready, setReady] = useState(false)
  useEffect(() => {
    const element = host.current
    if (!element) return
    let cancelled = false
    element.replaceChildren()
    setError("")
    setReady(false)
    void loadDocxPreview()
      .then(({ renderAsync }) =>
        renderAsync(bytes, element, undefined, { renderAltChunks: false })
      )
      .then(() => {
        if (!cancelled) setReady(true)
      })
      .catch(() => {
        if (!cancelled) setError("Could not render this document.")
      })
    return () => {
      cancelled = true
      element.replaceChildren()
    }
  }, [bytes])
  return (
    <div className="workspace-document-scroll workspace-document-word">
      {error && <p role="alert">{error}</p>}
      {!error && !ready && <p role="status">Rendering document…</p>}
      <div ref={host} />
    </div>
  )
}

function PptxPreview({ bytes }: { bytes: ArrayBuffer }) {
  const [slides, setSlides] = useState<string[]>([])
  const [index, setIndex] = useState(0)
  const [error, setError] = useState("")
  const [ready, setReady] = useState(false)
  useEffect(() => {
    let cancelled = false
    let urls: string[] = []
    setSlides([])
    setIndex(0)
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
          setSlides(urls)
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
  return (
    <div className="workspace-document-preview">
      <div className="workspace-document-scroll workspace-document-slides">
        {error ? (
          <p role="alert">{error}</p>
        ) : slides.length ? (
          <img src={slides[index]} alt={`Slide ${index + 1}`} />
        ) : (
          <p role={ready ? undefined : "status"}>
            {ready
              ? "This presentation has no slides."
              : "Rendering presentation…"}
          </p>
        )}
      </div>
      <PageNavigation
        index={index}
        count={slides.length}
        label="Slide"
        onChange={setIndex}
      />
    </div>
  )
}

type Sheet = { name: string; rows: string[][]; clipped: boolean }

function TablePreview({ sheets }: { sheets: Sheet[] }) {
  const [selected, setSelected] = useState(0)
  const sheet = sheets[selected]
  return (
    <div className="workspace-document-preview">
      <div className="workspace-document-scroll workspace-document-grid">
        <table>
          <tbody>
            {sheet.rows.map((row, rowIndex) => (
              <tr key={rowIndex}>
                {row.map((cell, columnIndex) => (
                  <td key={columnIndex}>{cell}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
        {sheet.clipped && <p>Showing the first 300 rows and 40 columns.</p>}
      </div>
      {sheets.length > 1 && (
        <div
          className="workspace-document-sheets"
          role="tablist"
          aria-label="Sheets"
        >
          {sheets.map((item, index) => (
            <button
              type="button"
              role="tab"
              aria-selected={selected === index}
              key={item.name}
              onClick={() => setSelected(index)}
            >
              {item.name}
            </button>
          ))}
        </div>
      )}
    </div>
  )
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
    <TablePreview sheets={sheets} />
  ) : (
    <p role={ready ? undefined : "status"}>
      {ready ? "This spreadsheet is empty." : "Rendering spreadsheet…"}
    </p>
  )
}

export function DocumentPreview({
  url,
  name,
  contentType,
}: {
  url: string
  name: string
  contentType: string
}) {
  const kind = previewKind(name, contentType)
  const { bytes, error } = useFileBytes(
    url,
    kind !== "image" && kind !== "unsupported"
  )
  if (kind === "image")
    return (
      <div className="workspace-document-scroll workspace-document-image">
        <img src={url} alt={name} />
      </div>
    )
  if (kind === "unsupported")
    return (
      <p>
        Preview is not available for this format. Use Edit or download the file.
      </p>
    )
  if (error) return <p role="alert">{error}</p>
  if (!bytes) return <p role="status">Opening preview…</p>
  if (kind === "pdf") return <PdfPreview bytes={bytes} />
  if (kind === "docx") return <DocxPreview bytes={bytes} />
  if (kind === "pptx") return <PptxPreview bytes={bytes} />
  if (kind === "xlsx") return <SpreadsheetPreview bytes={bytes} />
  const text = new TextDecoder().decode(bytes)
  if (kind === "markdown")
    return (
      <article className="workspace-document-scroll workspace-document-prose">
        <Markdown remarkPlugins={[remarkGfm]}>{text}</Markdown>
      </article>
    )
  if (kind === "csv") {
    // CSV is data, not HTML: React escapes every parsed cell.
    const parsed = Papa.parse<string[]>(text, { skipEmptyLines: false }).data
    const rows = parsed.slice(0, 300)
    return (
      <TablePreview
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
      {text}
    </pre>
  )
}
