// The PDF viewer (board ViewerPdf): a rail of page thumbnails, the pages one
// under another, and in the frame's bar the page counter, zoom and find.
// Everything is drawn by pdf.js in the browser; find reads each page's text
// layer (getTextContent) and marks what matches over the drawn page.
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import type { CSSProperties, RefObject } from "react"
import {
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronUp,
  Minus,
  Plus,
  Search,
  X,
} from "lucide-react"
import type {
  PDFDocumentLoadingTask,
  PDFDocumentProxy,
  PDFPageProxy,
  RenderTask,
} from "pdfjs-dist"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"
import { ViewerTools } from "./file-viewer-frame"
import "./pdf-viewer.css"

// PDF.js and its worker are megabytes only a browser runs; the Worker's build
// (SSR) leaves each import in a branch it never takes (document-preview.tsx).
function unavailableOnTheServer(): never {
  throw new Error("The PDF viewer runs in the browser.")
}
const loadPdfjs = () =>
  import.meta.env.SSR ? unavailableOnTheServer() : import("pdfjs-dist")
const loadPdfWorker = () =>
  import.meta.env.SSR
    ? unavailableOnTheServer()
    : import("pdfjs-dist/build/pdf.worker.min.mjs?url")

type Pdfjs = Awaited<ReturnType<typeof loadPdfjs>>

/** The zoom steps − and + move between, as scales of the page's own size. */
export const zoomSteps = [0.5, 0.75, 1, 1.25, 1.5, 2, 3] as const
const minScale = 0.25
const maxScale = 4

/** The next step in `direction` from `scale`, or the end it is already at. */
export function nextZoom(scale: number, direction: 1 | -1): number {
  if (direction > 0) {
    return zoomSteps.find((step) => step > scale + 0.001) ?? zoomSteps.at(-1)!
  }
  return (
    [...zoomSteps].reverse().find((step) => step < scale - 0.001) ??
    zoomSteps[0]
  )
}

/** One piece of a page's text, as getTextContent gives it. */
export type PageText = {
  str: string
  transform: number[]
  width: number
}

/** A place the query was found: a page, a piece of its text, and where in it. */
export type PdfMatch = {
  page: number
  item: number
  start: number
  length: number
}

/**
 * Where `query` appears in each page's text, ignoring case. A match is found
 * within one piece of text; pdf.js splits lines into pieces, so a phrase that
 * runs across two pieces is not found.
 */
export function findMatches(
  pages: ReadonlyArray<readonly PageText[] | undefined>,
  query: string
): PdfMatch[] {
  const needle = query.trim().toLocaleLowerCase()
  if (!needle) return []
  const matches: PdfMatch[] = []
  pages.forEach((items, page) => {
    items?.forEach((item, index) => {
      const haystack = item.str.toLocaleLowerCase()
      let from = haystack.indexOf(needle)
      while (from >= 0) {
        matches.push({ page, item: index, start: from, length: needle.length })
        from = haystack.indexOf(needle, from + needle.length)
      }
    })
  })
  return matches
}

type PageSize = { width: number; height: number }

/** Draws one page into a canvas at `scale`, while `active`. */
function usePageCanvas(
  page: PDFPageProxy | undefined,
  scale: number,
  active: boolean
) {
  const canvas = useRef<HTMLCanvasElement>(null)
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    const element = canvas.current
    if (!page || !element) return
    if (!active) {
      // Out of view, a page gives its pixels back.
      element.width = 0
      element.height = 0
      return
    }
    const viewport = page.getViewport({ scale })
    const pixelRatio = window.devicePixelRatio || 1
    element.width = Math.ceil(viewport.width * pixelRatio)
    element.height = Math.ceil(viewport.height * pixelRatio)
    const context = element.getContext("2d")
    if (!context) {
      setFailed(true)
      return
    }
    const render: RenderTask = page.render({
      canvas: element,
      canvasContext: context,
      viewport,
      transform: [pixelRatio, 0, 0, pixelRatio, 0, 0],
    })
    render.promise.catch((error: unknown) => {
      if ((error as { name?: string }).name !== "RenderingCancelledException")
        setFailed(true)
    })
    return () => render.cancel()
  }, [page, scale, active])
  return { canvas, failed }
}

/** Whether `element` is on screen in `root`, or near it; true without IO. */
function useNearView(
  element: RefObject<HTMLElement | null>,
  root: RefObject<HTMLElement | null>
) {
  const [near, setNear] = useState(typeof IntersectionObserver === "undefined")
  useEffect(() => {
    if (typeof IntersectionObserver === "undefined" || !element.current) return
    const observer = new IntersectionObserver(
      ([entry]) => setNear(entry.isIntersecting),
      { root: root.current, rootMargin: "600px 0px" }
    )
    observer.observe(element.current)
    return () => observer.disconnect()
  }, [element, root])
  return near
}

function PdfPage({
  pdfjs,
  pdf,
  index,
  size,
  scale,
  scroller,
  text,
  matches,
  activeMatch,
  pageRef,
}: {
  pdfjs: Pdfjs
  pdf: PDFDocumentProxy
  index: number
  size: PageSize
  scale: number
  scroller: RefObject<HTMLElement | null>
  text: readonly PageText[] | undefined
  matches: readonly PdfMatch[]
  activeMatch: PdfMatch | undefined
  pageRef: (element: HTMLElement | null) => void
}) {
  const box = useRef<HTMLElement | null>(null)
  const near = useNearView(box, scroller)
  const [page, setPage] = useState<PDFPageProxy>()
  useEffect(() => {
    if (!near || page) return
    let cancelled = false
    void pdf.getPage(index + 1).then((loaded) => {
      if (!cancelled) setPage(loaded)
    })
    return () => {
      cancelled = true
    }
  }, [near, page, pdf, index])
  const { canvas, failed } = usePageCanvas(page, scale, near)
  const viewport = useMemo(() => page?.getViewport({ scale }), [page, scale])
  const style: CSSProperties = {
    width: size.width * scale,
    height: size.height * scale,
  }
  return (
    <section
      ref={(element) => {
        box.current = element
        pageRef(element)
      }}
      className="pdf-page"
      style={style}
      aria-label={`Page ${index + 1}`}
      data-page={index + 1}
    >
      {failed ? (
        <p role="alert">Could not render this page.</p>
      ) : (
        <canvas ref={canvas} style={style} aria-hidden="true" />
      )}
      {viewport &&
        text &&
        matches.map((match) => {
          const item = text[match.item]
          const tx = pdfjs.Util.transform(viewport.transform, item.transform)
          const height = Math.hypot(tx[2], tx[3])
          const charWidth =
            item.str.length > 0 ? (item.width * scale) / item.str.length : 0
          const current =
            activeMatch?.page === match.page &&
            activeMatch.item === match.item &&
            activeMatch.start === match.start
          return (
            <mark
              key={`${match.item}-${match.start}`}
              className="pdf-match"
              data-current={current ? "true" : undefined}
              style={{
                left: tx[4] + match.start * charWidth,
                top: tx[5] - height,
                width: Math.max(2, match.length * charWidth),
                height,
              }}
            />
          )
        })}
    </section>
  )
}

function PdfThumb({
  pdf,
  index,
  size,
  current,
  scroller,
  onSelect,
}: {
  pdf: PDFDocumentProxy
  index: number
  size: PageSize
  current: boolean
  scroller: RefObject<HTMLElement | null>
  onSelect: () => void
}) {
  const box = useRef<HTMLButtonElement | null>(null)
  const near = useNearView(box, scroller)
  const [page, setPage] = useState<PDFPageProxy>()
  useEffect(() => {
    if (!near || page) return
    let cancelled = false
    void pdf.getPage(index + 1).then((loaded) => {
      if (!cancelled) setPage(loaded)
    })
    return () => {
      cancelled = true
    }
  }, [near, page, pdf, index])
  const scale = 88 / size.width
  const { canvas } = usePageCanvas(page, scale, near)
  return (
    <li>
      <Button
        ref={box}
        variant="ghost"
        className="pdf-thumb"
        aria-label={`Page ${index + 1}`}
        aria-current={current ? "page" : undefined}
        onClick={onSelect}
      >
        <canvas
          ref={canvas}
          style={{ width: 88, height: size.height * scale }}
          aria-hidden="true"
        />
        <span aria-hidden="true">{index + 1}</span>
      </Button>
    </li>
  )
}

type Zoom = { fit: true } | { fit: false; scale: number }

export function PdfViewer({ bytes }: { bytes: ArrayBuffer }) {
  const scroller = useRef<HTMLDivElement>(null)
  const thumbs = useRef<HTMLOListElement>(null)
  const pageElements = useRef<Array<HTMLElement | null>>([])
  const [pdfjs, setPdfjs] = useState<Pdfjs>()
  const [pdf, setPdf] = useState<PDFDocumentProxy | null>(null)
  const [sizes, setSizes] = useState<PageSize[]>([])
  const [error, setError] = useState("")
  const [index, setIndex] = useState(0)
  const [zoom, setZoom] = useState<Zoom>({ fit: true })
  const [width, setWidth] = useState(0)
  const [finding, setFinding] = useState(false)
  const [query, setQuery] = useState("")
  const [texts, setTexts] = useState<Array<PageText[] | undefined>>([])
  const [matchIndex, setMatchIndex] = useState(0)

  useEffect(() => {
    let cancelled = false
    let loading: PDFDocumentLoadingTask | undefined
    setPdf(null)
    setSizes([])
    setTexts([])
    setIndex(0)
    setError("")
    void Promise.all([loadPdfjs(), loadPdfWorker()])
      .then(async ([module, worker]) => {
        module.GlobalWorkerOptions.workerSrc = worker.default
        loading = module.getDocument({ data: new Uint8Array(bytes.slice(0)) })
        const document = await loading.promise
        const pageSizes: PageSize[] = []
        for (let number = 1; number <= document.numPages; number += 1) {
          const viewport = (await document.getPage(number)).getViewport({
            scale: 1,
          })
          pageSizes.push({ width: viewport.width, height: viewport.height })
        }
        if (cancelled) return
        setPdfjs(module)
        setSizes(pageSizes)
        setPdf(document)
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
    const element = scroller.current
    if (!element) return
    const measure = () => setWidth(element.clientWidth)
    measure()
    if (typeof ResizeObserver === "undefined") return
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    return () => observer.disconnect()
  }, [pdf])

  const widest = sizes.reduce((most, size) => Math.max(most, size.width), 0)
  const fitScale =
    widest && width
      ? Math.min(maxScale, Math.max(minScale, (width - 48) / widest))
      : 1
  const scale = zoom.fit ? fitScale : zoom.scale
  const count = pdf?.numPages ?? 0

  // Find reads every page's text once, the first time it is asked.
  useEffect(() => {
    if (!pdf || !finding || !query.trim() || texts.length === count) return
    const run = { cancelled: false }
    void (async () => {
      const read: PageText[][] = []
      for (let number = 1; number <= pdf.numPages; number += 1) {
        const content = await (await pdf.getPage(number)).getTextContent()
        read.push(
          content.items.flatMap((item) =>
            "str" in item
              ? [
                  {
                    str: item.str,
                    transform: item.transform,
                    width: item.width,
                  },
                ]
              : []
          )
        )
        if (run.cancelled) return
      }
      setTexts(read)
    })()
    return () => {
      run.cancelled = true
    }
  }, [pdf, finding, query, texts.length, count])

  const matches = useMemo(
    () => (finding ? findMatches(texts, query) : []),
    [finding, texts, query]
  )
  const activeMatch = matches.at(matchIndex)

  const goToPage = useCallback((page: number) => {
    setIndex(page)
    const element = pageElements.current[page]
    if (element && "scrollIntoView" in element) {
      element.scrollIntoView({ block: "start" })
    }
  }, [])

  useEffect(() => setMatchIndex(0), [query])
  useEffect(() => {
    if (activeMatch) goToPage(activeMatch.page)
  }, [activeMatch, goToPage])

  // The page counter follows the page at the top of the view.
  function trackPage() {
    const element = scroller.current
    if (!element) return
    const line = element.scrollTop + element.clientHeight / 3
    let current = 0
    pageElements.current.forEach((page, number) => {
      if (page && page.offsetTop <= line) current = number
    })
    setIndex(current)
  }

  // The thumbnail of the page in view stays in view in the rail.
  useEffect(() => {
    const thumb = thumbs.current?.querySelector<HTMLElement>(
      `li:nth-child(${index + 1}) button`
    )
    // jsdom has no scrollIntoView; every browser does.
    if (thumb && "scrollIntoView" in thumb) {
      thumb.scrollIntoView({ block: "nearest" })
    }
  }, [index])

  function stepMatch(direction: 1 | -1) {
    if (!matches.length) return
    setMatchIndex(
      (current) => (current + direction + matches.length) % matches.length
    )
  }

  if (error) return <p role="alert">{error}</p>

  return (
    <div className="pdf-viewer">
      {pdf && pdfjs && (
        <ViewerTools>
          <div className="pdf-tools-group" aria-label="Pages" role="group">
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Previous page"
              disabled={index === 0}
              onClick={() => goToPage(index - 1)}
            >
              <ChevronLeft />
            </Button>
            <span className="pdf-tools-count" aria-live="polite">
              {index + 1} / {count}
            </span>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Next page"
              disabled={index >= count - 1}
              onClick={() => goToPage(index + 1)}
            >
              <ChevronRight />
            </Button>
          </div>
          <div className="pdf-tools-group" aria-label="Zoom" role="group">
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Zoom out"
              disabled={scale <= zoomSteps[0] + 0.001}
              onClick={() =>
                setZoom({ fit: false, scale: nextZoom(scale, -1) })
              }
            >
              <Minus />
            </Button>
            <span className="pdf-tools-count">{Math.round(scale * 100)}%</span>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Zoom in"
              disabled={scale >= zoomSteps.at(-1)! - 0.001}
              onClick={() => setZoom({ fit: false, scale: nextZoom(scale, 1) })}
            >
              <Plus />
            </Button>
            <Button
              variant="ghost"
              size="sm"
              aria-pressed={zoom.fit}
              onClick={() => setZoom({ fit: true })}
            >
              Fit
            </Button>
          </div>
          {finding ? (
            <div className="pdf-tools-group pdf-find" role="search">
              <Input
                autoFocus
                type="search"
                aria-label="Find in document"
                placeholder="Find"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault()
                    stepMatch(event.shiftKey ? -1 : 1)
                  }
                  if (event.key === "Escape") {
                    setFinding(false)
                    setQuery("")
                  }
                }}
              />
              {query.trim() && (
                <span className="pdf-tools-count" aria-live="polite">
                  {texts.length < count
                    ? "Searching…"
                    : matches.length
                      ? `${matchIndex + 1} of ${matches.length}`
                      : "No matches"}
                </span>
              )}
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="Previous match"
                disabled={!matches.length}
                onClick={() => stepMatch(-1)}
              >
                <ChevronUp />
              </Button>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="Next match"
                disabled={!matches.length}
                onClick={() => stepMatch(1)}
              >
                <ChevronDown />
              </Button>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="Close find"
                onClick={() => {
                  setFinding(false)
                  setQuery("")
                }}
              >
                <X />
              </Button>
            </div>
          ) : (
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Find in document"
              onClick={() => setFinding(true)}
            >
              <Search />
            </Button>
          )}
        </ViewerTools>
      )}
      {pdf && count > 1 && (
        <nav className="pdf-thumbs" aria-label="Pages">
          <ol ref={thumbs}>
            {sizes.map((size, page) => (
              <PdfThumb
                key={page}
                pdf={pdf}
                index={page}
                size={size}
                current={page === index}
                scroller={thumbs}
                onSelect={() => goToPage(page)}
              />
            ))}
          </ol>
        </nav>
      )}
      <div
        ref={scroller}
        className={cn("pdf-pages", !pdf && "is-loading")}
        onScroll={trackPage}
      >
        {!pdf || !pdfjs ? (
          <p role="status">Opening PDF…</p>
        ) : (
          sizes.map((size, page) => (
            <PdfPage
              key={page}
              pdfjs={pdfjs}
              pdf={pdf}
              index={page}
              size={size}
              scale={scale}
              scroller={scroller}
              text={texts[page]}
              matches={matches.filter((match) => match.page === page)}
              activeMatch={activeMatch}
              pageRef={(element) => {
                pageElements.current[page] = element
              }}
            />
          ))
        )}
      </div>
    </div>
  )
}
