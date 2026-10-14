// A shell in a room's sandbox (A8): xterm.js on the signed WebSocket the
// edge gives a member (edge/src/sandbox, bridgeTerminal). Keystrokes go as
// they are; `{"resize":[cols,rows]}` resizes the shell. xterm loads only
// when a terminal opens.
import { useEffect, useRef, useState } from "react"
import { openTerminal } from "@/lib/live/children"
import type { LiveRoom } from "@/lib/live/client"
import "@xterm/xterm/css/xterm.css"

type Status = "connecting" | "open" | "closed" | "failed"

export function SandboxTerminal({
  organizationId,
  room,
  sandbox,
}: {
  organizationId: string
  room: LiveRoom
  sandbox: string
}) {
  const host = useRef<HTMLDivElement>(null)
  const [status, setStatus] = useState<Status>("connecting")
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    // An object, so the async work below sees the cleanup's change.
    const life = { disposed: false }
    const gone = () => life.disposed
    let socket: WebSocket | undefined
    const cleanups: (() => void)[] = []
    setStatus("connecting")

    void (async () => {
      const [{ Terminal }, { FitAddon }] = await Promise.all([
        import("@xterm/xterm"),
        import("@xterm/addon-fit"),
      ])
      if (gone() || !host.current) return
      const styles = getComputedStyle(host.current)
      const terminal = new Terminal({
        cursorBlink: true,
        fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
        fontSize: 12.5,
        theme: {
          background: styles.getPropertyValue("--terminal-bg").trim(),
          foreground: styles.getPropertyValue("--terminal-fg").trim(),
          cursor: styles.getPropertyValue("--terminal-fg").trim(),
        },
      })
      const fit = new FitAddon()
      terminal.loadAddon(fit)
      terminal.open(host.current)
      fit.fit()
      cleanups.push(() => terminal.dispose())

      let link
      try {
        link = await openTerminal(organizationId, room, sandbox, {
          cols: terminal.cols,
          rows: terminal.rows,
        })
      } catch {
        if (!gone()) setStatus("failed")
        return
      }
      if (gone()) return
      const url = new URL(link.url)
      url.protocol = url.protocol === "http:" ? "ws:" : "wss:"
      url.searchParams.set("cols", String(terminal.cols))
      url.searchParams.set("rows", String(terminal.rows))
      try {
        socket = new WebSocket(url)
      } catch {
        setStatus("failed")
        return
      }
      socket.binaryType = "arraybuffer"
      const decoder = new TextDecoder()
      socket.addEventListener("open", () => {
        setStatus("open")
        terminal.focus()
      })
      socket.addEventListener("message", (message) => {
        terminal.write(
          typeof message.data === "string"
            ? message.data
            : decoder.decode(message.data as ArrayBuffer, { stream: true })
        )
      })
      socket.addEventListener("close", () => {
        if (!gone()) setStatus("closed")
      })
      const input = terminal.onData((data) => {
        if (socket?.readyState === WebSocket.OPEN) socket.send(data)
      })
      const resize = terminal.onResize(({ cols, rows }) => {
        if (socket?.readyState === WebSocket.OPEN) {
          socket.send(JSON.stringify({ resize: [cols, rows] }))
        }
      })
      const observer = new ResizeObserver(() => fit.fit())
      observer.observe(host.current)
      cleanups.push(
        () => input.dispose(),
        () => resize.dispose(),
        () => observer.disconnect()
      )
    })()

    return () => {
      life.disposed = true
      socket?.close()
      for (const cleanup of cleanups.reverse()) cleanup()
    }
  }, [organizationId, room.kind, room.id, sandbox, attempt])

  return (
    <div className="sandbox-terminal">
      <div
        ref={host}
        className="sandbox-terminal-screen"
        role="application"
        aria-label="Sandbox terminal"
      />
      {status !== "open" && (
        <p
          className="sandbox-terminal-status"
          role={status === "failed" ? "alert" : "status"}
        >
          {status === "connecting"
            ? "Opening a shell…"
            : status === "failed"
              ? "Couldn't open a shell in this sandbox."
              : "The shell closed."}
          {status !== "connecting" && (
            <button
              type="button"
              onClick={() => setAttempt((value) => value + 1)}
            >
              Open a new shell
            </button>
          )}
        </p>
      )}
    </div>
  )
}
