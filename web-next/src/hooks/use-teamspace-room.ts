// The seam between a teamspace's room and the page that shows it.
//
// The room is the teamspace's Durable Object (src/teamspace-room-agent.ts,
// REALTIME.md), one per teamspace, shared by everyone on its team who has it
// open. This hook holds its socket: it says whether the tab is visible (the
// object reads Go as a member, which keeps them present, only while one of
// their tabs is), sends posts, and hands the page what the object sends. A
// page renders { messages, send, presence, status } and nothing else.
import {
  useCallback,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
} from "react"
import { useQueryClient } from "@tanstack/react-query"
import type { QueryClient } from "@tanstack/react-query"
import { useAgent } from "agents/react"
import { ApiError } from "@/lib/api/client"
import { isUnavailableHere } from "@/lib/agents/policy"
import { getSession } from "@/lib/auth/client"
import { checkTeamspaceRoom, mergeRoomMessages } from "@/lib/rooms/client"
import type { RoomMessage, RoomSeat } from "@/lib/rooms/client"
import {
  ROOM_HISTORY_LIMIT,
  TEAMSPACE_ROOM_AGENT,
  hasLanded,
  parseRoomServerMessage,
  roomGoneCloseCode,
  teamspaceRoomAgentName,
} from "@/lib/rooms/room-agent"
import type {
  RoomClientMessage,
  RoomPostResult,
  RoomServerMessage,
  TeamspaceRoomState,
} from "@/lib/rooms/room-agent"
import type { Teamspace } from "@/lib/teamspaces/client"
import { canUseRoom } from "@/lib/teamspaces/policy"
import { teamspaceKeys } from "@/lib/teamspaces/queries"
import { reopenSocket } from "@/lib/threads/agent-socket"
import { socketProtocol } from "@/lib/socket-protocol"
import { sessionExpiredCode } from "@/lib/threads/chat-messages"

export type { RoomMessage, RoomSeat }

/** Where the member stands with the room. */
export type RoomConnection =
  /** Nothing to open: no organization or teamspace yet. */
  | "closed"
  /** They manage the teamspace without being on its team, so it has no seat for them. */
  | "not-member"
  /** The teamspace is paused; its room answers 400 until it is resumed. */
  | "paused"
  /** The socket is opening, or has not been sent the room yet. */
  | "connecting"
  /** The room is open, and arrives as Go records it. */
  | "live"
  /** 403 or 404: they are no longer on the team, or the teamspace is gone. */
  | "gone"
  /** This server does not run rooms (503 rooms_unavailable). */
  | "unavailable"
  /** The socket dropped or Go could not be read and the room is trying again, or the session has ended. */
  | "error"

/** A post the room has not answered yet. */
export type OutgoingMessage = {
  /** Local only, for rendering. */
  id: string
  body: string
  /** The room has recorded it (a read returned it); its agents may still be answering. */
  landed: boolean
}

export type RoomPresence = {
  /**
   * Every seat: each person on the team, the agent that speaks for them, and
   * each active contributed specialist. A person is `present` when they have
   * the room open in a visible tab right now, or Go read the room for them
   * in the last 90 seconds, which is also when their agent keeps quiet.
   */
  seats: RoomSeat[]
  /** The people present, the member included. Agents are never present. */
  here: RoomSeat[]
  /** The member's own seat. Without one they can read but not post. */
  me?: RoomSeat
}

export type RoomStatus = {
  connection: RoomConnection
  /** Why the room is not live, in words for the member. */
  error?: string
  /** Posts still waiting for the room's answer, oldest first. */
  outgoing: OutgoingMessage[]
  /** Why the last post failed, until the next one goes out. */
  sendError?: string
  /** A post would be taken now: the room is open and the member has a seat. */
  canPost: boolean
}

export type TeamspaceRoom = {
  /** What was said, oldest first. */
  messages: RoomMessage[]
  /**
   * Says `body` in the room. Resolves true once the room has the message,
   * false when it does not (the page keeps the draft). Several posts may be
   * out at once; each shows in `status.outgoing` until the room answers it.
   */
  send: (body: string) => Promise<boolean>
  presence: RoomPresence
  status: RoomStatus
}

/** Why the room is not live, as the object or the Worker said. */
type Refusal = {
  connection: "paused" | "gone" | "unavailable" | "error"
  error?: string
  /** The socket stays shut until the page opens another room. */
  settled?: boolean
}

/** What the room's socket has said since the page asked for this room. */
type Live = {
  /** The room it is about: all of it starts over for another. */
  room: string
  messages: RoomMessage[]
  /** The object's synced state: Go's seats and who is here now. */
  shared?: TeamspaceRoomState
  /** The socket has been sent the room since it last opened. */
  open: boolean
  /** The socket has been sent the room at least once. */
  seen: boolean
  /** It closed since it was last sent the room, or before it ever was. */
  dropped: boolean
  refusal?: Refusal
}

/** A post the page shows as outgoing. */
type Pending = {
  /** For rendering; it stays while the post is said again under another id. */
  key: string
  body: string
  /** The last seq the room held when it went out. */
  after: number
}

/** A post whose answer never came: the socket lost it, or closed for good. */
type Lost = {
  id: string
  ok: false
  status: 0
  code?: undefined
  message: string
}

type Answer = RoomPostResult | Lost

/** A post sent, and the socket it went on (counted from 1 as each opens). */
type Waiter = { resolve: (answer: Answer) => void; socket: number }

const sessionEnded = "Your session has ended. Sign in again to read this room."
const cannotReach = "Couldn't reach the room."

const noSeats: RoomSeat[] = []

function initialLive(room: string): Live {
  return { room, messages: [], open: false, seen: false, dropped: false }
}

let postCounter = 0

/** An id for a post, the page's own: the room answers with it. */
function newPostId(): string {
  postCounter += 1
  const random =
    typeof crypto !== "undefined" && "randomUUID" in crypto
      ? crypto.randomUUID()
      : Math.random().toString(36).slice(2)
  return `post-${random}-${postCounter}`
}

function tabVisible(): boolean {
  return (
    typeof document === "undefined" || document.visibilityState === "visible"
  )
}

/**
 * The room of `teamspace`, over its Durable Object's socket, while the
 * calling component is mounted. Keep it mounted for as long as the room is
 * on screen: while this tab is visible the room reads Go as `viewerId`,
 * which is what keeps them present, and 90 seconds after the last such read
 * their own agent may answer for them. It opens only when the member may use
 * the room (`canUseRoom`): on the team, in an active teamspace.
 */
export function useTeamspaceRoom(
  organizationId: string | undefined,
  teamspace: Teamspace | undefined,
  viewerId: string | undefined
): TeamspaceRoom {
  const queryClient = useQueryClient()
  const open = Boolean(organizationId && teamspace && canUseRoom(teamspace))
  const room =
    open && organizationId && teamspace
      ? teamspaceRoomAgentName(organizationId, teamspace.id)
      : ""

  // What the socket has said. The handlers read it without waiting for a
  // render: a failed post is checked against the messages that arrived just
  // before its answer.
  const live = useRef<Live>(initialLive(room))
  if (live.current.room !== room) live.current = initialLive(room)
  const [, render] = useReducer((count: number) => count + 1, 0)
  const change = useCallback((forRoom: string, next: (held: Live) => Live) => {
    if (live.current.room !== forRoom) return
    live.current = next(live.current)
    render()
  }, [])

  const [pending, setPending] = useState<Pending[]>([])
  const [sendError, setSendError] = useState<string>()
  useEffect(() => setSendError(undefined), [room])

  const waiting = useRef(new Map<string, Waiter>())
  // Declared before the socket, so that unmounting marks the page gone
  // before the socket's own cleanup closes it.
  const mounted = useRef(false)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const sockets = useRef(0)
  const reopening = useRef(0)
  const renewing = useRef<Promise<boolean> | undefined>(undefined)
  const checking = useRef<Promise<void> | undefined>(undefined)
  // The room the page asks for now, for the socket's callbacks.
  const latestRoom = useRef(room)
  latestRoom.current = room

  const settled = Boolean(live.current.refusal?.settled)
  const agent = useAgent<TeamspaceRoomState>({
    protocol: socketProtocol(),
    agent: TEAMSPACE_ROOM_AGENT,
    name: room || "closed",
    enabled: open && !settled,
    onOpen: () => {
      sockets.current += 1
      say({ type: "room-visibility", visible: tabVisible() })
    },
    onStateUpdate: (state) => {
      change(latestRoom.current, (held) => ({ ...held, shared: state }))
    },
    onMessage: (event: MessageEvent) => {
      const message = parseRoomServerMessage(event.data)
      if (message) heard(latestRoom.current, message)
    },
    onClose: (event: CloseEvent) => {
      const forRoom = latestRoom.current
      if (!mounted.current || !forRoom || event.target !== socket.current) {
        return
      }
      // Until it opens again the room says it is trying, whether or not it
      // was ever open: a socket that cannot open is not left "connecting".
      change(forRoom, (held) => ({ ...held, open: false, dropped: true }))
      if (event.code === roomGoneCloseCode) {
        turnedAway(forRoom)
        return
      }
      // A browser is never told why an upgrade was refused (the socket
      // reports a plain close), so the Worker is asked: unless the page
      // closed the socket itself, to open it again or for good.
      if (reopening.current > 0 || live.current.refusal?.settled) return
      void check(forRoom)
    },
  })
  const socket = useRef(agent)
  socket.current = agent

  /** Sends on the socket. A visibility change goes only while it is open: it says so again as it opens. */
  function say(message: RoomClientMessage): void {
    const sending = socket.current
    if (
      message.type === "room-visibility" &&
      sending.readyState !== sending.OPEN
    ) {
      return
    }
    sending.send(JSON.stringify(message))
  }

  function heard(forRoom: string, message: RoomServerMessage): void {
    switch (message.type) {
      case "room-snapshot": {
        change(forRoom, (held) => ({
          ...held,
          messages: withSnapshot(held.messages, message.messages),
          open: true,
          seen: true,
          dropped: false,
          refusal: held.refusal?.settled ? held.refusal : undefined,
        }))
        // Posts sent on an earlier socket: the room says what became of
        // them, or does not know them (it never had them, or lost them in a
        // restart). One sent on this socket may reach it after this.
        const results = new Map(message.results.map((item) => [item.id, item]))
        for (const [id, waiter] of [...waiting.current]) {
          if (waiter.socket >= sockets.current) continue
          const result = results.get(id)
          if (result) finish(id, result)
          else if (!message.pending.includes(id)) finish(id, lost(id))
        }
        return
      }
      case "room-messages":
        change(forRoom, (held) => ({
          ...held,
          messages: mergeRoomMessages(held.messages, message.messages).slice(
            -ROOM_HISTORY_LIMIT
          ),
        }))
        return
      case "room-posted":
        finish(message.result.id, message.result)
        return
      case "room-status":
        switch (message.status) {
          case "live":
            change(forRoom, (held) => ({
              ...held,
              open: held.seen,
              refusal: held.refusal?.settled ? held.refusal : undefined,
            }))
            return
          case "error":
            change(forRoom, (held) =>
              held.refusal?.settled
                ? held
                : {
                    ...held,
                    refusal: { connection: "error", error: cannotReach },
                  }
            )
            return
          case "paused":
            change(forRoom, (held) => ({
              ...held,
              refusal: { connection: "paused" },
            }))
            // The teamspace itself changed: the page's copy is re-read, so
            // its header and gates follow.
            void refreshTeamspace(queryClient, forRoom)
            return
          case "unavailable":
            settle(forRoom, { connection: "unavailable", settled: true })
            return
          case "gone":
            turnedAway(forRoom)
            return
          case "session_expired":
            void renew(forRoom)
            return
        }
    }
  }

  function finish(id: string, answer: Answer): void {
    const waiter = waiting.current.get(id)
    if (!waiter) return
    waiting.current.delete(id)
    waiter.resolve(answer)
  }

  /** The room will not open again for this page: waiting posts are answered as lost. */
  function settle(forRoom: string, refusal: Refusal): void {
    change(forRoom, (held) => ({ ...held, refusal, open: false }))
    if (live.current.room !== forRoom) return
    for (const id of [...waiting.current.keys()]) finish(id, lost(id))
  }

  function turnedAway(forRoom: string): void {
    settle(forRoom, { connection: "gone", settled: true })
    void refreshTeamspace(queryClient, forRoom)
  }

  /** Why the socket closed: the Worker makes the socket's own check with Go. */
  function check(forRoom: string): Promise<void> {
    const [organization, space] = forRoom.split("~")
    checking.current ??= checkTeamspaceRoom(organization, space)
      .catch((error: unknown) => {
        if (!(error instanceof ApiError)) return
        if (error.status === 401) {
          settle(forRoom, {
            connection: "error",
            error: sessionEnded,
            settled: true,
          })
        } else if (error.status === 403 || error.status === 404) {
          turnedAway(forRoom)
        } else if (isUnavailableHere(error)) {
          settle(forRoom, { connection: "unavailable", settled: true })
        }
        // Anything else may pass: the socket keeps trying.
      })
      .finally(() => {
        checking.current = undefined
      })
    return checking.current
  }

  /**
   * The object no longer takes this socket's cookie. Reading the session
   * refreshes it, and a reopened socket carries the new one to the object.
   * Resolves whether that worked.
   */
  function renew(forRoom: string): Promise<boolean> {
    renewing.current ??= (async () => {
      try {
        await getSession()
      } catch (error) {
        if (error instanceof ApiError && error.status === 401) {
          settle(forRoom, {
            connection: "error",
            error: sessionEnded,
            settled: true,
          })
        }
        return false
      }
      if (latestRoom.current !== forRoom) return false
      reopening.current += 1
      try {
        await reopenSocket(socket.current)
        return true
      } catch {
        return false
      } finally {
        reopening.current -= 1
      }
    })().finally(() => {
      renewing.current = undefined
    })
    return renewing.current
  }

  // Whether this tab is visible is what the room reads Go for. (`say` reads
  // the socket through a ref, so the first one stays current.)
  useEffect(() => {
    if (!open) return
    const onChange = () =>
      say({ type: "room-visibility", visible: tabVisible() })
    document.addEventListener("visibilitychange", onChange)
    return () => document.removeEventListener("visibilitychange", onChange)
  }, [open])

  // A post nobody will answer any more resolves rather than waits forever.
  useEffect(() => {
    const posts = waiting.current
    return () => {
      for (const [id, waiter] of [...posts]) {
        posts.delete(id)
        waiter.resolve(lost(id))
      }
    }
  }, [room])

  const shown = live.current
  const messages = shown.messages
  const seats = useMemo(
    () =>
      shown.seen && shown.shared
        ? withLivePresence(shown.shared.seats, shown.shared.here)
        : noSeats,
    [shown.seen, shown.shared]
  )
  const me = viewerId
    ? seats.find((seat) => seat.kind === "person" && seat.user_id === viewerId)
    : undefined
  const here = seats.filter((seat) => seat.kind === "person" && seat.present)

  const connection = connectionOf(Boolean(organizationId), teamspace, shown)
  const canPost =
    Boolean(me) && (connection === "live" || connection === "error")

  const send = async (text: string): Promise<boolean> => {
    const body = text.trim()
    const forRoom = latestRoom.current
    if (!body || !forRoom || !canPost) return false
    const item: Pending = {
      key: newPostId(),
      body,
      after: live.current.messages.at(-1)?.seq ?? 0,
    }
    setPending((held) => [...held, item])
    setSendError(undefined)
    const post = (id: string) =>
      new Promise<Answer>((resolve) => {
        if (live.current.refusal?.settled) {
          resolve(lost(id))
          return
        }
        waiting.current.set(id, { resolve, socket: sockets.current })
        say({ type: "room-post", id, body })
      })
    try {
      let result = await post(item.key)
      if (!result.ok && result.code === sessionExpiredCode) {
        // Nothing was said: the socket's cookie had expired. Once the socket
        // carries a fresh one, it goes again, once.
        if (await renew(forRoom)) result = await post(newPostId())
      }
      if (live.current.room !== forRoom) return false
      if (result.ok) {
        const said = result.messages
        change(forRoom, (held) => ({
          ...held,
          messages: mergeRoomMessages(held.messages, said).slice(
            -ROOM_HISTORY_LIMIT
          ),
        }))
        // The room publishes what it concludes to the teamspace's ledger.
        const [organization, space] = forRoom.split("~")
        void queryClient.invalidateQueries({
          queryKey: teamspaceKeys.entries(organization, space),
        })
        return true
      }
      const landed = hasLanded(live.current.messages, item, viewerId)
      setSendError(sendErrorMessage(result, landed))
      if (result.status === 400) void refreshTeamspace(queryClient, forRoom)
      return landed
    } finally {
      setPending((held) => held.filter((outgoing) => outgoing.key !== item.key))
    }
  }

  return {
    messages,
    send,
    presence: { seats, here, me },
    status: {
      connection,
      error:
        connection === "error"
          ? (shown.refusal?.error ?? cannotReach)
          : undefined,
      outgoing: pending.map((item) => ({
        id: item.key,
        body: item.body,
        landed: hasLanded(messages, item, viewerId),
      })),
      sendError,
      canPost,
    },
  }
}

function connectionOf(
  known: boolean,
  teamspace: Teamspace | undefined,
  live: Live
): RoomConnection {
  if (!known || !teamspace) return "closed"
  if (teamspace.status !== "active") return "paused"
  if (!teamspace.is_member) return "not-member"
  if (live.refusal) return live.refusal.connection
  if (live.open) return "live"
  return live.dropped ? "error" : "connecting"
}

/**
 * The room as a socket opens: the object's recent window. What the page
 * held is kept when the two meet or overlap, and dropped when messages it
 * never saw would fall between them.
 */
function withSnapshot(
  held: readonly RoomMessage[],
  window: readonly RoomMessage[]
): RoomMessage[] {
  const first = window.at(0)?.seq
  const last = held.at(-1)?.seq
  if (first === undefined || last === undefined || last < first - 1) {
    return window.slice(-ROOM_HISTORY_LIMIT)
  }
  return mergeRoomMessages(held, window).slice(-ROOM_HISTORY_LIMIT)
}

/** Go's seats, with the people who have the room open and visible right now counted present at once. */
function withLivePresence(
  seats: readonly RoomSeat[],
  here: readonly string[]
): RoomSeat[] {
  const now = new Set(here)
  return seats.map((seat) =>
    seat.kind === "person" && !seat.present && now.has(seat.user_id)
      ? { ...seat, present: true }
      : seat
  )
}

function lost(id: string): Lost {
  return { id, ok: false, status: 0, message: "The room did not answer." }
}

async function refreshTeamspace(queryClient: QueryClient, room: string) {
  const [organizationId, teamspaceId] = room.split("~")
  if (!organizationId || !teamspaceId) return
  await Promise.all([
    queryClient.invalidateQueries({
      queryKey: teamspaceKeys.teamspace(organizationId, teamspaceId),
      exact: true,
    }),
    queryClient.invalidateQueries({
      queryKey: teamspaceKeys.list(organizationId),
    }),
  ])
}

/** The server's own wording, as a sentence. */
function sentence(message: string): string {
  const text = message.trim()
  if (!text) return text
  const capitalized = text.charAt(0).toUpperCase() + text.slice(1)
  return /[.!?]$/.test(capitalized) ? capitalized : `${capitalized}.`
}

function sendErrorMessage(
  result: Exclude<Answer, { ok: true }>,
  landed: boolean
): string {
  if (landed) {
    return "Your message is in the room, but the agents' answers didn't come back."
  }
  if (
    isUnavailableHere(new ApiError(result.message, result.status, result.code))
  ) {
    return "Rooms aren't available on this server."
  }
  switch (result.status) {
    case 400:
      return sentence(result.message)
    case 401:
      return "Your session has ended. Sign in again to post."
    case 403:
      return "Only teammates with an active agent can post in this room."
    case 404:
      return "This room is no longer available to you."
    case 429:
      return "Too many messages at once. Try again in a moment."
  }
  return "Couldn't send your message. Try again."
}
