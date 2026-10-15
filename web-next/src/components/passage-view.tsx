// A cited passage, open in its file: the file's text with the passage marked
// and scrolled to, and in the frame's bar a switch to the file as it usually
// reads (Markdown formatted, a table, highlighted code).
import { useEffect, useMemo, useRef, useState } from "react"
import type { ReactNode } from "react"
import { Button } from "@/components/ui/button"
import { ViewerTools } from "./file-viewer-frame"
import "./passage-view.css"

/** Byte offsets into a file's text (UTF-8), as rag's citations give them. */
export type Passage = { start: number; end: number }

/**
 * The text before, in and after a passage. Offsets are bytes, so the text is
 * cut as bytes and then decoded; an offset past the end is held to it.
 */
export function splitPassage(
  bytes: ArrayBuffer,
  passage: Passage
): { before: string; cited: string; after: string } | undefined {
  const all = new Uint8Array(bytes)
  const start = Math.max(0, Math.min(passage.start, all.length))
  const end = Math.max(start, Math.min(passage.end, all.length))
  if (end === start) return undefined
  const decode = (part: Uint8Array) => new TextDecoder().decode(part)
  return {
    before: decode(all.subarray(0, start)),
    cited: decode(all.subarray(start, end)),
    after: decode(all.subarray(end)),
  }
}

export function PassageView({
  bytes,
  passage,
  wholeLabel,
  children,
}: {
  bytes: ArrayBuffer
  passage: Passage
  /** What the switch calls the file's usual view. */
  wholeLabel: string
  /** The file's usual view. */
  children: ReactNode
}) {
  const parts = useMemo(() => splitPassage(bytes, passage), [bytes, passage])
  const [whole, setWhole] = useState(false)
  const mark = useRef<HTMLElement>(null)

  useEffect(() => {
    const element = mark.current
    // jsdom has no scrollIntoView; every browser does.
    if (!whole && element && "scrollIntoView" in element) {
      element.scrollIntoView({ block: "center" })
    }
  }, [whole])

  if (!parts) {
    return (
      <div className="passage-missing">
        <p role="status">The cited passage is no longer in this file.</p>
        {children}
      </div>
    )
  }

  return (
    <>
      <ViewerTools>
        <div className="passage-switch" role="group" aria-label="Show">
          <Button
            variant="ghost"
            size="sm"
            aria-pressed={!whole}
            onClick={() => setWhole(false)}
          >
            Cited passage
          </Button>
          <Button
            variant="ghost"
            size="sm"
            aria-pressed={whole}
            onClick={() => setWhole(true)}
          >
            {wholeLabel}
          </Button>
        </div>
      </ViewerTools>
      {whole ? (
        children
      ) : (
        <div className="workspace-document-scroll passage-text">
          {parts.before}
          <mark ref={mark} className="passage-mark" aria-label="Cited passage">
            {parts.cited}
          </mark>
          {parts.after}
        </div>
      )}
    </>
  )
}
