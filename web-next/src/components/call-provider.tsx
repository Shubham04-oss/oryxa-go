// One parley at a time, for the whole workspace (B6): the member joins from
// a teamspace, and the call follows them to other pages as a mini player
// until they leave. The media is RealtimeKit's (lib/call/media); the call's
// state is the edge's (lib/call/client).
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import { createPortal } from "react-dom"
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
import type {
  CallView,
  TranscriptSegment,
  VoiceTrouble,
} from "@/lib/call/client"
import { joinMedia } from "@/lib/call/media"
import { CALL_REFRESH_MS } from "@/lib/call/use-room-call"
import type {
  CallDevices,
  CallMedia,
  CallSignal,
  CallTile,
  DeviceKind,
} from "@/lib/call/media"
import { livePath, liveSocketUrl } from "@/lib/live/client"
import type { LiveRoom } from "@/lib/live/client"

/** How long a caption stays once its speaker stops. */
export const CAPTION_MS = 6_000
/** How often the member's own transcript goes to the room. */
export const TRANSCRIPT_FLUSH_MS = 1_500
/** Failed sends in a row before the member is told their words aren't arriving. */
export const TRANSCRIPT_FAILURES_SHOWN = 2
/** How many unsent segments are kept for a retry; the oldest go first. */
const TRANSCRIPT_KEPT = 200
/** How long Leave waits for the member's last words to reach the room. */
const LAST_WORDS_MS = 2_000
const VOICE_RETRY_MS = [2_000, 4_000, 8_000, 15_000]

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
  /** A line under "Parley" on the full page: the teamspace's description. */
  subtitle?: string
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
  /** What the agent heard the member say to it, for a moment (T3). */
  agentHeard?: string
  /** Why the agent stayed quiet after the member spoke to it (T2). */
  agentTrouble?: VoiceTrouble
  /** The member's words have stopped reaching the room (T4). */
  transcriptFailing?: boolean
  /**
   * Where the call is open: the mini player (or the strip on its room's
   * page), the stage in the workspace's main column, or the full page.
   */
  surface: CallSurfaceKind
  /** Whose hands are up, by member. */
  hands: string[]
  /** Reactions showing on their senders' tiles, for a moment each. */
  reactions: Reaction[]
  /** Hands and reactions can be sent in this call (the preset allows it). */
  signals: boolean
}

export type Reaction = { id: number; member: string; emoji: string }

/** How long a reaction shows on its sender's tile. */
export const REACTION_MS = 4_000

export type CallSurfaceKind = "pip" | "stage" | "full"

export type JoinOptions = {
  organizationId: string
  room: LiveRoom
  roomName: string
  /** A line under "Parley" on the full page: the teamspace's description. */
  subtitle?: string
  member: string
  name?: string
  /** The names the room shows for its agents: the call's agent answers to them. */
  agents?: string[]
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
  /** Raises or lowers the member's hand, for everyone in the call. */
  setHand: (up: boolean) => Promise<void>
  /** Shows a reaction on the member's tile, for everyone in the call. */
  react: (emoji: string) => Promise<void>
  setAgent: (present: boolean) => Promise<void>
  /**
   * Opens the call as `surface`. The stage needs a host in the workspace
   * (CallStageHost); without one the call opens full page instead.
   */
  show: (surface: CallSurfaceKind) => void
  /** The devices the member can choose between; none outside a call. */
  devices: () => Promise<CallDevices | undefined>
  setDevice: (kind: DeviceKind, deviceId: string) => Promise<void>
  onDevicesChange: (listener: () => void) => () => void
  dismiss: () => void
}

/** A parley that just ended, as the member saw it. */
export type EndedCall = {
  roomName: string
  startedAt?: string
  endedAt: string
  /** How many people were in it at the end. */
  people: number
  /** It was being recorded: the recording and transcript are kept. */
  recorded: boolean
  /** The agent leaves notes in the room for it. */
  notes: boolean
}

type CallContextValue = {
  active: ActiveCall | null
  /** The parley that just ended, until the member dismisses it. */
  ended: EndedCall | null
  dismissEnded: () => void
  /** The room whose own page is on screen, which shows the call itself. */
  roomOnScreen: string | null
  showingRoom: (room: string | null) => void
  /** Where the full page shows its room's conversation, while it does. */
  roomChat: RoomChatSlot | null
  setRoomChat: (slot: RoomChatSlot | null) => void
  /**
   * New messages from others in the member's call's room since its Room pane
   * was last open; undefined while no page reports the room's messages.
   */
  roomUnread: number | undefined
  /** The Room pane is open: what the room says now has been seen. */
  markRoomSeen: () => void
  reportRoomActivity: (activity: RoomActivity | null) => void
  /** A CallStageHost is mounted: the stage has a place to open. */
  stageHosted: boolean
  setStageHosted: (hosted: boolean) => void
} & CallActions

/** The messages a room's page holds from others, by their seq. */
export type RoomActivity = { room: string; seqs: number[] }

/** The full page's place for the room's conversation, and whose room it is. */
export type RoomChatSlot = { room: string; element: HTMLElement }

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

/**
 * Where a room's page puts its conversation while the member's parley in
 * that room is open full page: the call's Room pane, or nowhere (it stays on
 * the page). One conversation, moved, so the room is never open twice.
 */
export function useRoomChatSlot(
  organizationId: string,
  room: LiveRoom
): HTMLElement | null {
  const value = useContext(CallContext)
  const slot = value?.roomChat
  return slot && slot.room === roomKey(organizationId, room)
    ? slot.element
    : null
}

/**
 * A room page's conversation, kept in one host element that sits in the page
 * (at `pageRef`) or, while the member's parley in that room shows its Room
 * pane, in that pane. Moving the host moves the conversation without
 * remounting it: its draft, scroll and socket carry over.
 */
export function useRoomChatHost(organizationId: string, room: LiveRoom) {
  const slot = useRoomChatSlot(organizationId, room)
  const [host, setHost] = useState<HTMLDivElement | null>(null)
  const pageRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const element = document.createElement("div")
    element.className = "workspace-chat-host"
    setHost(element)
    return () => {
      element.remove()
    }
  }, [])
  useLayoutEffect(() => {
    const target = slot ?? pageRef.current
    if (host && target && host.parentElement !== target)
      target.appendChild(host)
  })
  return {
    pageRef,
    render: (conversation: ReactNode) =>
      host ? createPortal(conversation, host) : null,
  }
}

/**
 * A room's page tells the call which messages others have sent, so the
 * parley's Room button can count the new ones. Nothing outside a CallProvider.
 */
export function useReportRoomActivity(
  organizationId: string | undefined,
  room: LiveRoom,
  seqs: number[]
) {
  const report = useContext(CallContext)?.reportRoomActivity
  const key = organizationId ? roomKey(organizationId, room) : undefined
  const joined = seqs.join(",")
  useEffect(() => {
    if (!report || !key) return
    report({ room: key, seqs: joined ? joined.split(",").map(Number) : [] })
  }, [joined, key, report])
  useEffect(() => {
    if (!report || !key) return
    return () => report(null)
  }, [key, report])
}

/**
 * A member's hand and latest reaction in the call the viewer is in; nothing
 * outside a CallProvider or a call.
 */
export function useSignalsOf(member: string): {
  hand: boolean
  reaction?: Reaction
} {
  const active = useContext(CallContext)?.active
  return {
    hand: active?.hands.includes(member) ?? false,
    reaction: active?.reactions.find((reaction) => reaction.member === member),
  }
}

/** Whether parleys are offered here: inside the workspace's CallProvider. */
export function useHasCalls(): boolean {
  return useContext(CallContext) !== null
}

/** The member's own words, gathered between flushes. */
/**
 * The member's own segments on their way to the room. A segment stays until
 * a send of it succeeds (T4): a failed send puts it back, unless a newer
 * version of it arrived meanwhile, and the oldest go first past a bound.
 */
export function transcriptBuffer() {
  const pending = new Map<string, TranscriptSegment>()
  const keep = () => {
    for (const id of pending.keys()) {
      if (pending.size <= TRANSCRIPT_KEPT) break
      pending.delete(id)
    }
  }
  return {
    add(segment: TranscriptSegment) {
      pending.delete(segment.id)
      pending.set(segment.id, segment)
      keep()
    },
    take(): TranscriptSegment[] {
      const segments = [...pending.values()].slice(0, MAX_SEGMENTS)
      for (const segment of segments) pending.delete(segment.id)
      return segments
    },
    /** Puts back segments a send failed for; partial ones are stale by now. */
    putBack(segments: TranscriptSegment[]) {
      const newer = new Map(pending)
      pending.clear()
      for (const segment of segments) {
        if (!segment.partial && !newer.has(segment.id))
          pending.set(segment.id, segment)
      }
      for (const [id, segment] of newer) pending.set(id, segment)
      keep()
    },
    get size() {
      return pending.size
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
  const [roomChat, setRoomChat] = useState<RoomChatSlot | null>(null)
  const [ended, setEnded] = useState<EndedCall | null>(null)
  const [activity, setActivity] = useState<RoomActivity | null>(null)
  const [stageHosted, setStageHosted] = useState(false)
  /** The last message seen in each room, by seq. */
  const [seenIn, setSeen] = useState<Record<string, number>>({})
  /** The last view of the member's call, for the summary once it ends. */
  const lastView = useRef<CallView | null>(null)
  const media = useRef<CallMedia | null>(null)
  const cleanups = useRef<(() => void)[]>([])
  /** Sends what the member said last, before they leave the call. */
  const lastWords = useRef<(() => Promise<void>) | null>(null)
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

  /** Keeps what the "Parley ended" summary shows of the member's call. */
  const remember = useCallback(() => {
    const current = activeRef.current
    if (current?.phase !== "in") return
    const seen = lastView.current
    setEnded({
      roomName: current.roomName,
      startedAt: seen?.startedAt,
      endedAt: new Date().toISOString(),
      people: Math.max(seen?.participants.length ?? 0, current.tiles.length),
      recorded: seen?.recording.on ?? false,
      notes: seen?.agent.notes ?? false,
    })
  }, [])

  const teardownAndLeave = useCallback(async () => {
    const current = activeRef.current
    // A join still under way sees this and leaves for itself.
    attempt.current += 1
    // Words not sent yet would be lost: the room refuses them once the
    // member has left (T4).
    const send = lastWords.current
    lastWords.current = null
    if (current?.phase === "in" && send) {
      await Promise.race([
        send().catch(() => undefined),
        new Promise((resolve) => setTimeout(resolve, LAST_WORDS_MS)),
      ])
    }
    // The last one out ends the call on the edge: they get its summary too,
    // as everyone does when it ends another way (T16).
    const others = lastView.current?.participants.filter(
      (person) => person.member !== current?.member
    )
    if (current?.phase === "in" && others?.length === 0) remember()
    await teardown()
    setActive(null)
    if (current?.phase === "in") {
      await leaveCall(current.organizationId, current.room).catch(
        () => undefined
      )
      refreshCall(current.organizationId, current.room)
    }
  }, [refreshCall, remember, teardown])

  const setHandOf = useCallback(
    (member: string, up: boolean) =>
      setActive((current) =>
        current
          ? {
              ...current,
              hands: up
                ? [...current.hands.filter((hand) => hand !== member), member]
                : current.hands.filter((hand) => hand !== member),
            }
          : current
      ),
    []
  )
  const reactionId = useRef(0)
  const showReaction = useCallback((member: string, emoji: string) => {
    reactionId.current += 1
    const id = reactionId.current
    setActive((current) =>
      current
        ? {
            ...current,
            reactions: [
              ...current.reactions.filter(
                (reaction) => reaction.member !== member
              ),
              { id, member, emoji },
            ],
          }
        : current
    )
    setTimeout(
      () =>
        setActive((current) =>
          current
            ? {
                ...current,
                reactions: current.reactions.filter(
                  (reaction) => reaction.id !== id
                ),
              }
            : current
        ),
      REACTION_MS
    )
  }, [])
  // A signal the preset refuses turns hands and reactions off for this call.
  const sendSignal = useCallback(
    async (signal: CallSignal, undo: () => void) => {
      const current = media.current
      if (!current?.signal) return
      try {
        await current.signal(signal)
      } catch {
        undo()
        update({ signals: false })
      }
    },
    [update]
  )
  const setHand = useCallback(
    async (up: boolean) => {
      const member = activeRef.current?.member
      if (!member) return
      setHandOf(member, up)
      await sendSignal({ kind: "hand", up }, () => setHandOf(member, !up))
    },
    [sendSignal, setHandOf]
  )
  const react = useCallback(
    async (emoji: string) => {
      const member = activeRef.current?.member
      if (!member) return
      showReaction(member, emoji)
      await sendSignal({ kind: "reaction", emoji }, () => undefined)
    },
    [sendSignal, showReaction]
  )

  const join = useCallback(
    async (options: JoinOptions) => {
      if (activeRef.current?.phase === "in") await teardownAndLeave()
      setEnded(null)
      setActive({
        organizationId: options.organizationId,
        room: options.room,
        roomName: options.roomName,
        subtitle: options.subtitle,
        member: options.member,
        phase: "joining",
        mic: options.mic,
        camera: options.camera,
        screen: false,
        tiles: [],
        heard: [],
        agentSpeaking: false,
        hands: [],
        reactions: [],
        signals: false,
        surface: "pip",
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
          options.name ?? viewerName,
          options.agents
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
      update({
        phase: "in",
        callId,
        tiles: joined.tiles(),
        signals: Boolean(joined.signal && joined.onSignal),
      })
      refreshCall(options.organizationId, options.room)

      cleanups.current.push(
        joined.onChange(() => update({ tiles: joined.tiles() }))
      )
      // Hands and reactions from everyone else.
      if (joined.onSignal)
        cleanups.current.push(
          joined.onSignal((member, signal) => {
            if (signal.kind === "hand") setHandOf(member, signal.up)
            else showReaction(member, signal.emoji)
          })
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
      // One send at a time; a failed one goes back in the buffer, and the
      // member hears about it once it keeps failing (T4).
      let sending: Promise<void> | undefined
      let failures = 0
      const send = (): Promise<void> => {
        if (sending) return sending
        const segments = buffer.take()
        if (segments.length === 0) return Promise.resolve()
        sending = sendTranscript(
          options.organizationId,
          options.room,
          callId,
          segments
        )
          .then(() => {
            if (failures >= TRANSCRIPT_FAILURES_SHOWN)
              update({ transcriptFailing: false })
            failures = 0
          })
          .catch((error: unknown) => {
            // The call is over or the member is out of it: nothing to retry.
            if (error instanceof ApiError && [403, 409].includes(error.status))
              return
            buffer.putBack(segments)
            failures += 1
            if (failures === TRANSCRIPT_FAILURES_SHOWN)
              update({ transcriptFailing: true })
          })
          .finally(() => {
            sending = undefined
          })
        return sending
      }
      const flush = setInterval(() => void send(), TRANSCRIPT_FLUSH_MS)
      lastWords.current = async () => {
        await sending?.catch(() => undefined)
        await send()
      }
      let heardTimer: ReturnType<typeof setTimeout> | undefined
      cleanups.current.push(() => {
        clearInterval(flush)
        clearTimeout(captionTimer)
        clearTimeout(heardTimer)
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
          onHeard: (text) => {
            update({ agentHeard: text, agentTrouble: undefined })
            clearTimeout(heardTimer)
            heardTimer = setTimeout(
              () => update({ agentHeard: undefined }),
              CAPTION_MS
            )
          },
          onTrouble: (reason) => update({ agentTrouble: reason }),
          onState: () => refreshCall(options.organizationId, options.room),
          onEnded: () => void endedElsewhere(id),
        })
      )
    },
    [
      joinMediaWith,
      refreshCall,
      setHandOf,
      showReaction,
      teardownAndLeave,
      update,
      viewerName,
    ]
  )

  /**
   * Someone else ended the call: there is no seat to give back, only the
   * media and the page's own state to clear.
   */
  const endedElsewhere = useCallback(
    async (id: number) => {
      if (attempt.current !== id) return
      attempt.current += 1
      remember()
      await teardown()
      setActive(null)
    },
    [remember, teardown]
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
    if (inCall && view.data && view.data.callId === inCall.callId) {
      lastView.current = view.data
    }
  }, [inCall, view.data])
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
    remember()
    await endCall(current.organizationId, current.room).catch(() => undefined)
    await teardown()
    setActive(null)
    refreshCall(current.organizationId, current.room)
  }, [refreshCall, remember, teardown])

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

  // The media is a ref, so these never change: a picker that reads devices
  // isn't re-run by every caption or tile update.
  const devices = useCallback(async () => media.current?.devices(), [])
  const setDevice = useCallback(async (kind: DeviceKind, deviceId: string) => {
    await media.current?.setDevice(kind, deviceId)
  }, [])
  const onDevicesChange = useCallback(
    (listener: () => void) =>
      media.current?.onDevicesChange(listener) ?? (() => undefined),
    []
  )

  const activeRoom = active ? roomKey(active.organizationId, active.room) : null
  const roomActivity =
    activity && activity.room === activeRoom ? activity : undefined
  const latest = roomActivity ? Math.max(0, ...roomActivity.seqs) : 0
  const seenUpTo = activeRoom ? seenIn[activeRoom] : undefined
  // A room first reported counts nothing that came before it.
  useEffect(() => {
    if (activeRoom && roomActivity && seenUpTo === undefined)
      setSeen((current) => ({ ...current, [activeRoom]: latest }))
  }, [activeRoom, latest, roomActivity, seenUpTo])
  const roomUnread =
    roomActivity && seenUpTo !== undefined
      ? roomActivity.seqs.filter((seq) => seq > seenUpTo).length
      : undefined
  const markRoomSeen = useCallback(() => {
    if (!activeRoom || !roomActivity) return
    setSeen((current) =>
      current[activeRoom] === latest
        ? current
        : { ...current, [activeRoom]: latest }
    )
  }, [activeRoom, latest, roomActivity])

  const show = useCallback(
    (surface: CallSurfaceKind) =>
      update({
        surface: surface === "stage" && !stageHosted ? "full" : surface,
      }),
    [stageHosted, update]
  )

  const value = useMemo<CallContextValue>(
    () => ({
      active,
      ended,
      dismissEnded: () => setEnded(null),
      roomOnScreen,
      showingRoom,
      roomChat,
      setRoomChat,
      roomUnread,
      markRoomSeen,
      reportRoomActivity: setActivity,
      join,
      leave: teardownAndLeave,
      end,
      setMic: toggle("mic"),
      setCamera: toggle("camera"),
      setScreen: toggle("screen"),
      setRecording: record,
      setHand,
      react,
      setAgent: agent,
      show,
      stageHosted,
      setStageHosted,
      devices,
      setDevice,
      onDevicesChange,
      dismiss: () =>
        setActive((current) => (current?.phase === "in" ? current : null)),
    }),
    [
      active,
      agent,
      devices,
      end,
      ended,
      join,
      markRoomSeen,
      onDevicesChange,
      record,
      roomChat,
      roomUnread,
      roomOnScreen,
      react,
      setDevice,
      setHand,
      show,
      stageHosted,
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
    onHeard: (text: string) => void
    onTrouble: (reason: VoiceTrouble) => void
    onState: () => void
    /** The call ended for everyone. */
    onEnded: () => void
  }
): () => void {
  let stopped = false
  let socket: WebSocket | undefined
  let retry: ReturnType<typeof setTimeout> | undefined
  let retries = 0
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
    socket.addEventListener("open", () => {
      retries = 0
    })
    socket.addEventListener("message", (message) => {
      const frame = parseVoiceFrame(message.data)
      // A frame from a newer edge is ignored, never shown as private.
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
      } else if (frame.type === "voice.private") {
        on.onPrivate(frame.text)
      } else if (frame.type === "voice.heard") {
        on.onHeard(frame.text)
      } else {
        on.onTrouble(frame.reason)
      }
    })
    socket.addEventListener("close", (event) => {
      socket = undefined
      // The room closed it on purpose: the member left, was removed or the
      // call ended. Reopening would only be refused (T7).
      if (stopped || event.code === 1000 || event.code === 4403) return
      const delay =
        VOICE_RETRY_MS[Math.min(retries, VOICE_RETRY_MS.length - 1)] ?? 15_000
      retries += 1
      retry = setTimeout(open, delay)
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
