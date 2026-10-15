// A presentation (board ViewerSlides): the slide on a stage, a filmstrip of
// every slide under it, previous and next with "n / N" in the frame's bar,
// and the slide's speaker notes when it has any.
import { useEffect, useRef, useState } from "react"
import type { KeyboardEvent } from "react"
import { ChevronLeft, ChevronRight } from "lucide-react"
import { Button } from "@/components/ui/button"
import { ViewerTools } from "./file-viewer-frame"
import "./slides-viewer.css"

/** A slide drawn as an image, and its speaker notes. */
export type SlideImage = { url: string; notes?: string | null }

export function SlidesViewer({ slides }: { slides: readonly SlideImage[] }) {
  const [index, setIndex] = useState(0)
  const strip = useRef<HTMLOListElement>(null)
  const count = slides.length
  const slide = slides[Math.min(index, count - 1)]
  const notes = slide.notes?.trim()

  function go(next: number) {
    setIndex(Math.min(count - 1, Math.max(0, next)))
  }

  function keys(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "ArrowRight" || event.key === "PageDown") {
      event.preventDefault()
      go(index + 1)
    }
    if (event.key === "ArrowLeft" || event.key === "PageUp") {
      event.preventDefault()
      go(index - 1)
    }
  }

  // The slide on the stage stays in view in the filmstrip.
  useEffect(() => {
    const thumb = strip.current?.querySelector<HTMLElement>(
      `li:nth-child(${index + 1}) button`
    )
    // jsdom has no scrollIntoView; every browser does.
    if (thumb && "scrollIntoView" in thumb) {
      thumb.scrollIntoView({ block: "nearest", inline: "nearest" })
    }
  }, [index])

  return (
    <div className="slides-viewer">
      <ViewerTools>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Previous slide"
          disabled={index === 0}
          onClick={() => go(index - 1)}
        >
          <ChevronLeft />
        </Button>
        <span className="slides-count" aria-live="polite">
          {index + 1} / {count}
        </span>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Next slide"
          disabled={index >= count - 1}
          onClick={() => go(index + 1)}
        >
          <ChevronRight />
        </Button>
      </ViewerTools>
      <div
        className="slides-stage"
        tabIndex={0}
        role="region"
        aria-label="Slide. Arrow keys move between slides."
        onKeyDown={keys}
      >
        <img src={slide.url} alt={`Slide ${index + 1}`} />
      </div>
      {notes && (
        <section className="slides-notes" aria-label="Speaker notes">
          <h3>Speaker notes</h3>
          <p>{notes}</p>
        </section>
      )}
      {count > 1 && (
        <nav className="slides-strip" aria-label="Slides">
          <ol ref={strip}>
            {slides.map((item, number) => (
              <li key={item.url}>
                <Button
                  variant="ghost"
                  className="slides-thumb"
                  aria-label={`Slide ${number + 1}`}
                  aria-current={number === index ? "true" : undefined}
                  onClick={() => go(number)}
                >
                  <img src={item.url} alt="" />
                  <span aria-hidden="true">{number + 1}</span>
                </Button>
              </li>
            ))}
          </ol>
        </nav>
      )}
    </div>
  )
}
