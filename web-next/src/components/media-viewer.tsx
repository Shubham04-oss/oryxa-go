// Images, video and audio (boards ViewerImage, ViewerVideo). An image fits
// the frame or zooms in steps; video and audio use the browser's own player,
// with speed (and full screen for video) in the frame's bar. A file the
// browser cannot play says so, rather than showing a broken player.
import { useEffect, useRef, useState } from "react"
import { Maximize, Minus, Plus } from "lucide-react"
import { Button } from "@/components/ui/button"
import { ViewerTools } from "./file-viewer-frame"
import "./media-viewer.css"

/** The zoom steps an image moves between, as scales of its own size. */
const imageSteps = [0.25, 0.5, 0.75, 1, 1.5, 2, 3, 4] as const

/** The next step in `direction` from `scale`, or the end it is already at. */
export function nextImageZoom(scale: number, direction: 1 | -1): number {
  if (direction > 0) {
    return imageSteps.find((step) => step > scale + 0.001) ?? imageSteps.at(-1)!
  }
  return (
    [...imageSteps].reverse().find((step) => step < scale - 0.001) ??
    imageSteps[0]
  )
}

export function ImageViewer({ url, name }: { url: string; name: string }) {
  // null: fit the frame; a number: that many times the image's own size.
  const [scale, setScale] = useState<number | null>(null)
  const [natural, setNatural] = useState<{ width: number; height: number }>()
  const [fitScale, setFitScale] = useState(1)
  const [failed, setFailed] = useState(false)
  const stage = useRef<HTMLDivElement>(null)

  // What "fit" comes to, for the percentage and the first step from it.
  useEffect(() => {
    const element = stage.current
    if (!element || !natural) return
    const measure = () => {
      const fit = Math.min(
        1,
        (element.clientWidth - 32) / natural.width,
        (element.clientHeight - 32) / natural.height
      )
      // Before the stage has a size, fit is the image's own.
      setFitScale(fit > 0 ? fit : 1)
    }
    measure()
    if (typeof ResizeObserver === "undefined") return
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    return () => observer.disconnect()
  }, [natural])

  const shown = scale ?? fitScale
  if (failed) return <p role="alert">This image can’t be shown here.</p>
  return (
    <div className="media-viewer">
      <ViewerTools>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Zoom out"
          disabled={shown <= imageSteps[0] + 0.001}
          onClick={() => setScale(nextImageZoom(shown, -1))}
        >
          <Minus />
        </Button>
        <span className="media-count">{Math.round(shown * 100)}%</span>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Zoom in"
          disabled={shown >= imageSteps.at(-1)! - 0.001}
          onClick={() => setScale(nextImageZoom(shown, 1))}
        >
          <Plus />
        </Button>
        <Button
          variant="ghost"
          size="sm"
          aria-pressed={scale === null}
          onClick={() => setScale(null)}
        >
          Fit
        </Button>
      </ViewerTools>
      <div
        ref={stage}
        className="media-stage"
        data-fit={scale === null ? "true" : undefined}
      >
        <img
          src={url}
          alt={name}
          style={
            scale !== null && natural
              ? { width: natural.width * scale, maxWidth: "none" }
              : undefined
          }
          onLoad={(event) =>
            setNatural({
              width: event.currentTarget.naturalWidth,
              height: event.currentTarget.naturalHeight,
            })
          }
          onError={() => setFailed(true)}
        />
      </div>
    </div>
  )
}

/** The speeds the player steps through, in order. */
export const playbackSpeeds = [1, 1.25, 1.5, 2, 0.75] as const

/** The speed after `speed`, wrapping round. */
export function nextSpeed(speed: number): number {
  const index = playbackSpeeds.findIndex((step) => step === speed)
  return playbackSpeeds[(index + 1) % playbackSpeeds.length]
}

export function MediaPlayer({
  url,
  name,
  kind,
  contentType,
}: {
  url: string
  name: string
  kind: "video" | "audio"
  contentType: string
}) {
  const player = useRef<HTMLVideoElement & HTMLAudioElement>(null)
  const [speed, setSpeed] = useState(1)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (player.current) player.current.playbackRate = speed
  }, [speed])

  if (failed) {
    return (
      <p role="alert">
        This {kind === "video" ? "video" : "recording"} can’t play here
        {contentType && contentType !== "application/octet-stream"
          ? ` (it’s stored as ${contentType})`
          : ", because it wasn’t stored as one the browser can play"}
        . Download it to {kind === "video" ? "watch" : "listen to"} it.
      </p>
    )
  }

  return (
    <div className="media-viewer">
      <ViewerTools>
        <Button
          variant="ghost"
          size="sm"
          aria-label={`Playback speed ${speed}×`}
          onClick={() => setSpeed(nextSpeed(speed))}
        >
          {speed}×
        </Button>
        {kind === "video" && (
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Full screen"
            onClick={() => void player.current?.requestFullscreen()}
          >
            <Maximize />
          </Button>
        )}
      </ViewerTools>
      <div className="media-stage" data-fit="true">
        {kind === "video" ? (
          <video
            ref={player}
            src={url}
            controls
            preload="metadata"
            aria-label={name}
            onError={() => setFailed(true)}
          />
        ) : (
          <audio
            ref={player}
            src={url}
            controls
            preload="metadata"
            aria-label={name}
            onError={() => setFailed(true)}
          />
        )}
      </div>
    </div>
  )
}
