import { useEffect, useRef } from "react"

const PIXEL_SIZE = 5
const MOVING_TEXT = [
  "FIND THE SIGNAL",
  "WORK IN MOTION",
  "PEOPLE + AGENTS",
  "MAKE SPACE",
  "MOVE FORWARD",
  "CREATE TOGETHER",
]

function noise(x: number, y: number): number {
  const value = Math.sin(x * 127.1 + y * 311.7) * 43758.5453
  return value - Math.floor(value)
}

function drawScene(
  context: CanvasRenderingContext2D,
  time: number,
  width: number,
  height: number
) {
  context.clearRect(0, 0, width, height)

  const compact = width < 152
  const centerX = width * (compact ? 0.5 : 0.27)
  const centerY = height * (compact ? 0.2 : 0.5)
  const cycloneRadius = compact
    ? Math.min(width * 0.17, 18)
    : Math.min(height * 0.19, width * 0.14, 32)

  const specks = Math.min(470, Math.floor((width * height) / 65))
  for (let i = 0; i < specks; i++) {
    const speed = 2 + noise(i, 4) * 9
    const x = (noise(i, 7) * width + time * speed) % width
    const y = noise(i, 18) * height + Math.sin(time * 1.2 + i) * 1.5
    context.fillStyle = noise(i, 30) > 0.78 ? "#858e9a" : "#444b54"
    context.fillRect(x, y, noise(i, 9) > 0.91 ? 2 : 1, 1)
  }

  const hue = 205 + Math.sin(time * 0.15) * 18
  const extent = Math.ceil(cycloneRadius * 1.05)
  for (
    let y = Math.max(0, Math.floor(centerY - extent));
    y < Math.min(height, Math.ceil(centerY + extent));
    y++
  ) {
    for (
      let x = Math.max(0, Math.floor(centerX - extent));
      x < Math.min(width, Math.ceil(centerX + extent));
      x++
    ) {
      const dx = (x - centerX) / cycloneRadius
      const dy = (y - centerY) / cycloneRadius
      const radius = Math.hypot(dx, dy)
      const angle = Math.atan2(dy, dx)
      const grain = noise(x, y)
      const edge = 1 + Math.sin(angle * 7 + time * 0.45) * 0.025
      if (radius > edge + (grain - 0.5) * 0.04) continue
      if (radius > 0.82 && grain < (radius - 0.82) * 1.35) continue

      if (radius < 0.115) {
        context.fillStyle = "#111820"
        context.fillRect(x, y, 1, 1)
        continue
      }

      const spiral = angle * 3 + radius * 12.4 - time * 1.65
      const cloudBand = (Math.sin(spiral) + 1) / 2
      const fineBand = Math.sin(angle * 6 + radius * 19 - time * 1.1)
      const eyeWall = Math.max(0, 1 - Math.abs(radius - 0.19) / 0.09) * 25
      const lightness = Math.max(
        18,
        Math.min(
          84,
          25 +
            cloudBand * 38 +
            fineBand * 6 +
            eyeWall +
            grain * 11 -
            Math.max(0, radius - 0.8) * 30
        )
      )
      context.fillStyle = `hsl(${hue} ${12 + cloudBand * 15}% ${lightness}%)`
      context.fillRect(x, y, 1, 1)
    }
  }

  for (let arm = 0; arm < 3; arm++) {
    for (let step = 0; step < 36; step++) {
      if (noise(step, arm + 40) < 0.55) continue
      const radius = 0.22 + step * 0.021
      const angle = arm * ((Math.PI * 2) / 3) + radius * 4.9 - time * 0.55
      const x = Math.round(centerX + Math.cos(angle) * radius * cycloneRadius)
      const y = Math.round(centerY + Math.sin(angle) * radius * cycloneRadius)
      context.fillStyle = "#b1c0ca"
      context.fillRect(x, y, 1, 1)
    }
  }

  for (let i = 0; i < 95; i++) {
    const radius = cycloneRadius * (0.95 + noise(i, 7) * 0.25)
    const angle = noise(i, 18) * Math.PI * 2 - time * 0.3
    const x = Math.round(centerX + Math.cos(angle) * radius)
    const y = Math.round(centerY + Math.sin(angle) * radius)
    if (x < 0 || y < 0 || x >= width || y >= height) continue
    context.fillStyle = noise(i, 30) > 0.7 ? "#a3adb9" : "#555e6b"
    context.fillRect(x, y, 1, 1)
  }

  context.font = "bold 5px monospace"
  context.textBaseline = "middle"
  context.textAlign = "start"
  for (let i = 0; i < 7; i++) {
    const message = MOVING_TEXT[i % MOVING_TEXT.length]
    const textWidth = context.measureText(message).width
    const travel = width + textWidth + 20
    const offset = (time * (22 + i * 3) + i * 43) % travel
    const x = i % 2 === 0 ? width - offset : offset - textWidth - 20
    const y = height * (0.1 + i * 0.13)
    context.fillStyle = i % 3 === 0 ? "#c4ced7" : "#929daa"
    context.fillText(message, x, y)
  }
}

export function AuthPixelArt() {
  const canvas = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const element = canvas.current
    const context = element?.getContext("2d", { alpha: true })
    if (!element || !context) return

    let width = 0
    let height = 0
    const resize = () => {
      width = Math.max(1, Math.ceil(element.clientWidth / PIXEL_SIZE))
      height = Math.max(1, Math.ceil(element.clientHeight / PIXEL_SIZE))
      element.width = width
      element.height = height
      context.imageSmoothingEnabled = false
      drawScene(context, 0, width, height)
    }
    const observer = new ResizeObserver(resize)
    observer.observe(element)
    resize()

    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      return () => observer.disconnect()
    }

    let frame = 0
    let lastDraw = 0
    const animate = (now: number) => {
      if (document.visibilityState === "visible" && now - lastDraw >= 80) {
        drawScene(context, now / 1000, width, height)
        lastDraw = now
      }
      frame = window.requestAnimationFrame(animate)
    }
    frame = window.requestAnimationFrame(animate)
    return () => {
      window.cancelAnimationFrame(frame)
      observer.disconnect()
    }
  }, [])

  return (
    <div className="auth-art" aria-hidden="true">
      <canvas className="auth-art-canvas" ref={canvas} />
    </div>
  )
}
