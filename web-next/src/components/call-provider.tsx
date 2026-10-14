// One parley at a time, for the whole workspace (B6): the member joins from
// a teamspace, and the call follows them to other pages as a mini player
// until they leave. The media is RealtimeKit's (lib/call/media); the call's
// state is the edge's (lib/call/client).
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import type { ReactNode } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { ApiError } from "@/lib/api/client"
import {
  MAX_SEGMENTS,
  callKey,
  endCall,
  joinCall,
  leaveCall,
  parseVoiceFrame,
  readCall,
  sendTranscript,
  setAgentPresent,
  setRecording,
} from "@/lib/call/client"
import type { TranscriptSegment } from "@/lib/call/client"
import { joinMedia } from "@/lib/call/media"
import { CALL_REFRESH_MS } from "@/lib/call/use-room-call"
import type { CallMedia, CallTile } from "@/lib/call/media"
import { livePath, liveSocketUrl } from "@/lib/live/client"
import type { LiveRoom } from "@/lib/live/client"

/** How long a caption stays once its speaker stops. */
export const CAPTION_MS = 6_000
/** How often the member's own transcript goes to the room. */
export const TRANSCRIPT_FLUSH_MS = 1_500
const VOICE_RETRY_MS = 2_000

export type Caption = {
  name: string
  text: string
  /** Who said it: a member's user id, or the agent. */
  member?: string
  agent?: boolean
}

/** One line of the parley's transcript, as this page heard it. */
export type HeardLine = {
  id: string
  name: string
  text: string
  partial: boolean
  agent?: boolean
}

/** How many lines the transcript pane keeps. */
export const HEARD_LINES = 200

function addLine(lines: HeardLine[], line: HeardLine): HeardLine[] {
  const index = lines.findIndex((existing) => existing.id === line.id)
  const next =
    index === -1
      ? [...lines, line]
      : lines.map((existing, at) => (at === index ? line : existing))
  return next.slice(-HEARD_LINES)
}

export type ActiveCall = {
  organizationId: string
  room: LiveRoom
  roomName: string
  member: string
  phase: "joining" | "in" | "failed"
  callId?: string
  error?: string
  mic: boolean
  camera: boolean
  screen: boolean
  tiles: CallTile[]
  agentSpeaking: boolean
  caption?: Caption
  heard: HeardLine[]
  /** An answer the agent found with the member's own access, for them only. */
  privateAnswer?: string
  expanded: boolean
}

export type JoinOptions = {
  organizationId: string
  room: LiveRoom
  roomName: string
  member: string
  name?: string
  mic: boolean
  camera: boolean
}

type CallActions = {
  join: (options: JoinOptions) => Promise<void>
  leave: () => Promise<void>
  end: () => Promise<void>
  setMic: (on: boolean) => Promise<void>
  setCamera: (on: boolean) => Promise<void>
  setScreen: (on: boolean) => Promise<void>
  setRecording: (on: boolean) => Promise<void>
  setAgent: (present: boolean) => Promise<void>
  expand: (expanded: boolean) => void
  dismiss: () => void
}

type CallContextValue = {
  active: ActiveCall | null
  /** The room whose own page is on screen, which shows the call itself. */
  roomOnScreen: string | null
  showingRoom: (room: string | null) => void
} & CallActions

/** A room's key, for telling whether a page shows the member's call. */
export function roomKey(organizationId: string, room: LiveRoom): string {
  return `${organizationId}/${room.kind}/${room.id}`
}

const CallContext = createContext<CallContextValue | null>(null)

/** The parley the member is in, and what they can do with it. */
export function useCall(): CallContextValue {
  const value = useContext(CallContext)
  if (!value) throw new Error("useCall outside CallProvider")
  return value
}

/** Whether parleys are offered here: inside the workspace's CallProvider. */
export function useHasCalls(): boolean {
  return useContext(CallContext) !== null
}

/** The member's own words, gathered between flushes. */
function transcriptBuffer() {
  const pending = new Map<string, TranscriptSegment>()
  return {
    add(segment: TranscriptSegment) {
      pending.set(segment.id, segment)
    },
    take(): TranscriptSegment[] {
      const segments = [...pending.values()].slice(0, MAX_SEGMENTS)
      for (const segment of segments) pending.delete(segment.id)
      return segments
    },
    clear() {
      pending.clear()
    },
  }
}

function joinError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 403) return "You can't join a parley in this room yet."
    if (error.status === 503) return "Parleys aren't available right now."
    if (error.status === 401) return "Sign in again to join."
  }
  if (error instanceof DOMException && error.name === "NotAllowedError") {
    return "Allow the microphone and camera to join."
  }
  return "Couldn't join the parley."
}

export function CallProvider({
  children,
  viewerName,
  joinMediaWith = joinMedia,
}: {
  children: ReactNode
  /** The member's name, which the call shows when a page names none. */
  viewerName?: string
  /** The media to join with; tests stand in for RealtimeKit. */
  joinMediaWith?: typeof joinMedia
}) {
  const queryClient = useQueryClient()
  const [active, setActive] = useState<ActiveCall | null>(null)
  const [roomOnScreen, showingRoom] = useState<string | null>(null)
  const media = useRef<CallMedia | null>(null)
  const cleanups = useRef<(() => void)[]>([])
  const activeRef = useRef<ActiveCall | null>(null)
  activeRef.current = active
  // Each join is one attempt; leaving or ending, or another join, makes it
  // stale. The edge holds a member's seat until /call/leave, so a stale join
  // gives back whatever it got.
  const attempt = useRef(0)
  /** When the member's media joined: a view older than this can't end it. */
  const joinedAt = useRef(0)

  const update = useCallback(
    (change: Partial<ActiveCall>) =>
      setActive((current) => (current ? { ...current, ...change } : current)),
    []
  )

  const teardown = useCallback(async () => {
    for (const cleanup of cleanups.current.splice(0)) cleanup()
    const current = media.current
    media.current = null
    await current?.leave().catch(() => undefined)
  }, [])

  const refreshCall = useCallback(
    (organizationId: string, room: LiveRoom) =>
      void queryClient.invalidateQueries({
        queryKey: callKey(organizationId, room),
      }),
    [queryClient]
  )

  const teardownAndLeave = useCallback(async () => {
    const current = activeRef.current
    // A join still under way sees this and leaves for itself.
    attempt.current += 1
    await teardown()
    setActive(null)
    if (current?.phase === "in") {
      await leaveCall(current.organizationId, current.room).catch(
        () => undefined
      )
      refreshCall(current.organizationId, current.room)
    }
  }, [refreshCall, teardown])

  const join = useCallback(
    async (options: JoinOptions) => {
      if (activeRef.current?.phase === "in") await teardownAndLeave()
      setActive({
        organizationId: options.organizationId,
        room: options.room,
        roomName: options.roomName,
        member: options.member,
        phase: "joining",
        mic: options.mic,
        camera: options.camera,
        screen: false,
        tiles: [],
        heard: [],
        agentSpeaking: false,
        expanded: false,
      })
      const id = (attempt.current += 1)
      const stale = () => attempt.current !== id
      const giveBack = () =>
        leaveCall(options.organizationId, options.room).catch(() => undefined)

      let callId: string
      let token: string
      try {
        const answer = await joinCall(
          options.organizationId,
          options.room,
          options.name ?? viewerName
        )
        callId = answer.callId
        token = answer.token
      } catch (error) {
        if (!stale()) update({ phase: "failed", error: joinError(error) })
        return
      }
      // The edge gave the member a seat: from here on, every way out leaves.
      if (stale()) {
        void giveBack()
        return
      }
      let joined: CallMedia
      try {
        joined = await joinMediaWith({
          token,
          mic: options.mic,
          camera: options.camera,
        })
      } catch (error) {
        void giveBack()
        if (!stale()) update({ phase: "failed", error: joinError(error) })
        return
      }
      if (stale()) {
        void joined.leave().catch(() => undefined)
        void giveBack()
        return
      }
      joinedAt.current = Date.now()
      media.current = joined
      update({ phase: "in", callId, tiles: joined.tiles() })
      refreshCall(options.organizationId, options.room)

      cleanups.current.push(
        joined.onChange(() => update({ tiles: joined.tiles() }))
      )

      // Captions for everyone; the member's own words also go to the room,
      // which is how the agent hears them.
      const buffer = transcriptBuffer()
      let captionTimer: ReturnType<typeof setTimeout> | undefined
      cleanups.current.push(
        joined.onTranscript((segment) => {
          setActive((current) =>
            current
              ? {
                  ...current,
                  caption: {
                    name: segment.name,
                    text: segment.text,
                    member: segment.member,
                  },
                  heard: addLine(current.heard, {
                    id: segment.id,
                    name: segment.name,
                    text: segment.text,
                    partial: segment.partial,
                  }),
                }
              : current
          )
          clearTimeout(captionTimer)
          captionTimer = setTimeout(
            () => update({ caption: undefined }),
            CAPTION_MS
          )
          if (segment.member !== options.member) return
          buffer.add({
            id: segment.id,
            speaker: options.member,
            text: segment.text,
            partial: segment.partial,
            at: segment.at,
          })
        })
      )
      const flush = setInterval(() => {
        const segments = buffer.take()
        if (segments.length === 0) return
        void sendTranscript(
          options.organizationId,
          options.room,
          callId,
          segments
        ).catch(() => undefined)
      }, TRANSCRIPT_FLUSH_MS)
      cleanups.current.push(() => {
        clearInterval(flush)
        clearTimeout(captionTimer)
        buffer.clear()
      })

      cleanups.current.push(
        followVoice(options.organizationId, options.room, {
          onSpeaking: (utteranceId, name, text) =>
            setActive((current) =>
              current
                ? {
                    ...current,
                    agentSpeaking: true,
                    caption: { name, text, agent: true },
                    heard: addLine(current.heard, {
                      id: utteranceId,
                      name,
                      text,
                      partial: false,
                      agent: true,
                    }),
                  }
                : current
            ),
          onQuiet: () => update({ agentSpeaking: false }),
          onPrivate: (text) => update({ privateAnswer: text }),
          onState: () => refreshCall(options.organizationId, options.room),
          onEnded: () => void endedElsewhere(id),
        })
      )
    },
    [joinMediaWith, refreshCall, teardownAndLeave, update, viewerName]
  )

  /**
   * Someone else ended the call: there is no seat to give back, only the
   * media and the page's own state to clear.
   */
  const endedElsewhere = useCallback(
    async (id: number) => {
      if (attempt.current !== id) return
      attempt.current += 1
      await teardown()
      setActive(null)
    },
    [teardown]
  )

  // The edge's view of the call the member is in: once a view read after
  // they joined has no call, or another one, theirs has ended.
  const inCall = active?.phase === "in" ? active : undefined
  const view = useQuery({
    queryKey: callKey(
      inCall?.organizationId ?? "",
      inCall?.room ?? { kind: "thread", id: "" }
    ),
    queryFn: ({ signal }) =>
      readCall(
        inCall?.organizationId ?? "",
        inCall?.room ?? { kind: "thread", id: "" },
        signal
      ),
    enabled: inCall !== undefined,
    refetchInterval: CALL_REFRESH_MS,
  })
  useEffect(() => {
    if (!inCall || !view.isSuccess) return
    if (view.dataUpdatedAt <= joinedAt.current) return
    if (view.data && view.data.callId === inCall.callId) return
    void endedElsewhere(attempt.current)
  }, [endedElsewhere, inCall, view.data, view.dataUpdatedAt, view.isSuccess])

  const end = useCallback(async () => {
    const current = activeRef.current
    if (!current) return
    attempt.current += 1
    await endCall(current.organizationId, current.room).catch(() => undefined)
    await teardown()
    setActive(null)
    refreshCall(current.organizationId, current.room)
  }, [refreshCall, teardown])

  const toggle = useCallback(
    (key: "mic" | "camera" | "screen") => async (on: boolean) => {
      const current = media.current
      if (!current) return
      update({ [key]: on })
      try {
        if (key === "mic") await current.setMic(on)
        if (key === "camera") await current.setCamera(on)
        if (key === "screen") await current.setScreen(on)
      } catch {
        // Permission refused, or the share picker was closed.
        update({ [key]: !on })
      }
    },
    [update]
  )

  const record = useCallback(
    async (on: boolean) => {
      const current = activeRef.current
      if (!current) return
      await setRecording(current.organizationId, current.room, on)
      refreshCall(current.organizationId, current.room)
    },
    [refreshCall]
  )

  const agent = useCallback(
    async (present: boolean) => {
      const current = activeRef.current
      if (!current) return
      await setAgentPresent(current.organizationId, current.room, present)
      refreshCall(current.organizationId, current.room)
    },
    [refreshCall]
  )

  // Leaving the page leaves the call, so the edge frees the seat.
  useEffect(() => {
    const leaveOnUnload = () => {
      const current = activeRef.current
      if (current?.phase !== "in") return
      // No body: Chrome refuses a beacon with a JSON one, and leave needs none.
      navigator.sendBeacon(
        livePath(current.organizationId, current.room, "/call/leave")
      )
    }
    window.addEventListener("pagehide", leaveOnUnload)
    return () => {
      window.removeEventListener("pagehide", leaveOnUnload)
      void teardown()
    }
  }, [teardown])

  const value = useMemo<CallContextValue>(
    () => ({
      active,
      roomOnScreen,
      showingRoom,
      join,
      leave: teardownAndLeave,
      end,
      setMic: toggle("mic"),
      setCamera: toggle("camera"),
      setScreen: toggle("screen"),
      setRecording: record,
      setAgent: agent,
      expand: (expanded) => update({ expanded }),
      dismiss: () =>
        setActive((current) => (current?.phase === "in" ? current : null)),
    }),
    [
      active,
      agent,
      end,
      join,
      record,
      roomOnScreen,
      teardownAndLeave,
      toggle,
      update,
    ]
  )

  return <CallContext.Provider value={value}>{children}</CallContext.Provider>
}

/**
 * The agent's voice: `/voice/live` sends each spoken sentence as MP3, in
 * order, and every client plays it. Returns the stop.
 */
function followVoice(
  organizationId: string,
  room: LiveRoom,
  on: {
    onSpeaking: (utteranceId: string, name: string, text: string) => void
    onQuiet: () => void
    onPrivate: (text: string) => void
    onState: () => void
    /** The call ended for everyone. */
    onEnded: () => void
  }
): () => void {
  let stopped = false
  let socket: WebSocket | undefined
  let retry: ReturnType<typeof setTimeout> | undefined
  let agentName = "Agent"
  let playing: HTMLAudioElement | undefined
  let current: string | undefined
  const queue: { utteranceId: string; data: string; last: boolean }[] = []

  const playNext = () => {
    if (playing || stopped) return
    const next = queue.shift()
    if (!next) return
    playing = new Audio(`data:audio/mpeg;base64,${next.data}`)
    const done = () => {
      playing = undefined
      if (next.last && queue.length === 0) on.onQuiet()
      playNext()
    }
    playing.addEventListener("ended", done)
    playing.addEventListener("error", done)
    void playing.play().catch(done)
  }

  const stop = (utteranceId: string) => {
    for (let index = queue.length - 1; index >= 0; index -= 1) {
      if (queue[index]?.utteranceId === utteranceId) queue.splice(index, 1)
    }
    if (current === utteranceId && playing) {
      playing.pause()
      playing = undefined
    }
    on.onQuiet()
  }

  const open = () => {
    if (stopped) return
    socket = new WebSocket(liveSocketUrl(organizationId, room, "/voice/live"))
    socket.addEventListener("message", (message) => {
      const frame = parseVoiceFrame(message.data)
      if (!frame) return
      if (frame.type === "voice.state") {
        agentName = frame.name
        on.onState()
      } else if (frame.type === "voice.speaking") {
        current = frame.utteranceId
        on.onSpeaking(frame.utteranceId, agentName, frame.text)
      } else if (frame.type === "voice.audio") {
        queue.push({
          utteranceId: frame.utteranceId,
          data: frame.data,
          last: frame.last,
        })
        playNext()
      } else if (frame.type === "voice.stop") {
        stop(frame.utteranceId)
        if (frame.reason === "call_ended") on.onEnded()
      } else {
        on.onPrivate(frame.text)
      }
    })
    socket.addEventListener("close", () => {
      socket = undefined
      if (!stopped) retry = setTimeout(open, VOICE_RETRY_MS)
    })
  }

  open()
  return () => {
    stopped = true
    clearTimeout(retry)
    socket?.close()
    playing?.pause()
    queue.length = 0
  }
}
