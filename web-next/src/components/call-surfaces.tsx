// A parley's surfaces (B6; founder's redesign, 14 Oct 2026, "Parley" on the
// design canvas): the strip that joins from a teamspace, the mini player
// that follows the member to other pages, and the full page. The state is
// CallProvider's; the call's view is the edge's (useRoomCall).
import { useEffect, useRef, useState } from "react"
import type { CSSProperties } from "react"
import {
  ArrowLeft,
  Captions,
  Circle,
  Maximize2,
  MessageSquareText,
  Mic,
  MicOff,
  MonitorUp,
  PhoneOff,
  ScrollText,
  Sparkles,
  Video,
  VideoOff,
  X,
} from "lucide-react"
import { roomKey, useCall } from "./call-provider"
import type { ActiveCall } from "./call-provider"
import { IdentityAvatar, TeamspaceAvatar } from "./workspace-avatar"
import { agentIdentity, personIdentity } from "./teamspace-format"
import { callDuration, whoIsIn } from "@/lib/call/client"
import type { CallView } from "@/lib/call/client"
import type { CallTile } from "@/lib/call/media"
import { useRoomCall } from "@/lib/call/use-room-call"
import type { LiveRoom } from "@/lib/live/client"
import "./call.css"

const TINTS = 6

/** A person's tile colour, the same for them everywhere. */
export function tintOf(id: string): string {
  let hash = 0
  for (const char of id) hash = (hash * 31 + char.charCodeAt(0)) >>> 0
  return `var(--call-tint-${(hash % TINTS) + 1})`
}

/** The time since `startedAt`, ticking each second. */
function useDuration(startedAt: string | undefined): string | undefined {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!startedAt) return
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [startedAt])
  return startedAt ? callDuration(startedAt, now) : undefined
}

function CallVideo({
  track,
  label,
}: {
  track: MediaStreamTrack
  label: string
}) {
  const video = useRef<HTMLVideoElement>(null)
  useEffect(() => {
    if (video.current) video.current.srcObject = new MediaStream([track])
  }, [track])
  return <video ref={video} autoPlay playsInline muted aria-label={label} />
}

function Wave() {
  return (
    <span className="call-wave" aria-hidden="true">
      <i />
      <i />
      <i />
      <i />
    </span>
  )
}

/** A person in the call: their camera, or their avatar on their colour. */
export function CallCam({
  member,
  name,
  tile,
  size = "default",
  speaking = false,
  flag,
}: {
  member: string
  name: string
  tile?: CallTile
  size?: "default" | "big" | "small"
  speaking?: boolean
  flag?: React.ReactNode
}) {
  const muted = tile ? !tile.audio : false
  const label = `${tile?.self ? "You" : name}${speaking ? ", speaking" : ""}${muted ? ", muted" : ""}`
  return (
    <div
      className="call-cam"
      role="group"
      aria-label={label}
      data-size={size}
      data-self={tile?.self ? "true" : undefined}
      data-speaking={speaking ? "true" : undefined}
      style={{ "--c": tintOf(member) } as CSSProperties}
    >
      {tile?.videoTrack ? (
        <CallVideo track={tile.videoTrack} label={`${name}'s camera`} />
      ) : (
        <IdentityAvatar identity={personIdentity(member, name)} />
      )}
      {flag}
      {size !== "small" && (
        <span className="call-name">
          {speaking ? (
            <Wave />
          ) : muted ? (
            <span className="is-muted">
              <MicOff size={12} strokeWidth={2} aria-hidden="true" />
            </span>
          ) : (
            <Mic size={12} strokeWidth={2} aria-hidden="true" />
          )}
          {tile?.self ? "You" : name}
        </span>
      )}
    </div>
  )
}

/** The agent in the call: it never has a camera, only its voice. */
function AgentCam({
  name,
  speaking,
  size = "default",
}: {
  name: string
  speaking: boolean
  size?: "default" | "big" | "small"
}) {
  return (
    <div
      className="call-cam"
      role="group"
      aria-label={`${name}, the agent${speaking ? ", speaking" : ""}`}
      data-size={size}
      data-speaking={speaking ? "true" : undefined}
      style={{ "--c": "var(--call-tint-6)" } as CSSProperties}
    >
      <IdentityAvatar identity={agentIdentity(`agent:${name}`, name)} />
      {size !== "small" && (
        <span className="call-flag" data-agent="true">
          {speaking ? <Wave /> : <Sparkles size={11} aria-hidden="true" />}
          {speaking ? "Speaking" : "Listening"}
        </span>
      )}
      {size !== "small" && <span className="call-name">{name}</span>}
    </div>
  )
}

/** A screen someone is sharing, as its own tile. */
function ShareCam({ tile }: { tile: CallTile }) {
  if (!tile.screenTrack) return null
  const owner = tile.self ? "Your" : `${tile.name}'s`
  return (
    <div
      className="call-cam"
      role="group"
      aria-label={`${owner} screen`}
      data-share="true"
    >
      <CallVideo track={tile.screenTrack} label={`${owner} screen`} />
      <span className="call-name">
        <MonitorUp size={12} aria-hidden="true" />
        {owner} screen
      </span>
    </div>
  )
}

function recordingLine(call: CallView | null): string | undefined {
  if (!call?.recording.on) return undefined
  const by = call.participants.find(
    (person) => person.member === call.recording.by
  )
  const since = call.recording.since
    ? new Date(call.recording.since).toLocaleTimeString([], {
        hour: "2-digit",
        minute: "2-digit",
      })
    : undefined
  return `Recording${by ? ` · turned on by ${by.name}` : ""}${since ? ` at ${since}` : ""}`
}

function isThisRoom(
  active: ActiveCall | null,
  organizationId: string,
  room: LiveRoom
) {
  return (
    active?.organizationId === organizationId &&
    active.room.kind === room.kind &&
    active.room.id === room.id
  )
}

/**
 * "Parley" in a room's header: starts one when there is none. While one
 * runs, the strip below offers to join it instead.
 */
export function ParleyButton({
  organizationId,
  room,
  roomName,
  member,
  name,
}: {
  organizationId: string
  room: LiveRoom
  roomName: string
  member: string
  name?: string
}) {
  const { active, join } = useCall()
  const { call, unavailable } = useRoomCall(organizationId, room)
  const here = isThisRoom(active, organizationId, room)
  if (call || (here && active?.phase !== "failed")) return null
  return (
    <button
      className="workspace-teamspace-parley"
      type="button"
      disabled={unavailable || active?.phase === "joining"}
      title={
        unavailable
          ? "Parleys aren't available in this teamspace yet."
          : undefined
      }
      onClick={() =>
        void join({
          organizationId,
          room,
          roomName,
          member,
          name,
          mic: true,
          camera: false,
        })
      }
    >
      <Mic size={13} strokeWidth={2} aria-hidden="true" />
      Parley
    </button>
  )
}

/** A running parley across the top of its room: who's in, and Join. */
export function ParleyStrip({
  organizationId,
  room,
  roomName,
  member,
  name,
}: {
  organizationId: string
  room: LiveRoom
  roomName: string
  member: string
  name?: string
}) {
  const { active, join, expand, leave, showingRoom } = useCall()
  const { call } = useRoomCall(organizationId, room)
  const key = roomKey(organizationId, room)
  // While the room's own page is on screen, its strip is the call's control
  // and the mini player stands aside.
  useEffect(() => {
    showingRoom(key)
    return () => showingRoom(null)
  }, [key, showingRoom])
  const [mic, setMic] = useState(true)
  const [camera, setCamera] = useState(false)
  const duration = useDuration(call?.startedAt)
  const here = isThisRoom(active, organizationId, room)

  if (here && active?.phase === "failed") {
    return (
      <div className="call-strip" role="region" aria-label="Parley">
        <div className="call-strip-text">
          <strong>Parley</strong>
          <p role="alert">{active.error}</p>
        </div>
      </div>
    )
  }
  if (!call) return null

  const others = call.participants.filter((person) => person.member !== member)
  const names = [
    ...others.map((person) => person.name.split(" ")[0] ?? person.name),
    ...(call.agent.present ? [call.agent.name] : []),
  ]
  const recording = recordingLine(call)

  if (here && active?.phase === "in") {
    return (
      <div className="call-strip" role="region" aria-label="Parley in progress">
        <div className="call-strip-text">
          <strong>
            <span className="call-live-dot" aria-hidden="true" />
            You're in the parley{duration ? ` · ${duration}` : ""}
          </strong>
          {recording ? (
            <span className="call-recording">{recording}</span>
          ) : (
            <span>{whoIsIn(names)}</span>
          )}
        </div>
        <div className="call-strip-acts">
          <button
            className="call-quiet"
            type="button"
            onClick={() => expand(true)}
          >
            Open
          </button>
          <button
            className="call-leave"
            type="button"
            onClick={() => void leave()}
          >
            <PhoneOff size={15} aria-hidden="true" />
            Leave
          </button>
        </div>
      </div>
    )
  }

  const joining = here && active?.phase === "joining"
  const start = (withMic: boolean, withCamera: boolean) =>
    void join({
      organizationId,
      room,
      roomName,
      member,
      name,
      mic: withMic,
      camera: withCamera,
    })

  return (
    <div className="call-strip" role="region" aria-label="Parley in progress">
      <div className="call-strip-cams" aria-hidden="true">
        {others.slice(0, 3).map((person) => (
          <CallCam
            key={person.member}
            member={person.member}
            name={person.name}
            size="small"
          />
        ))}
        {call.agent.present && (
          <AgentCam name={call.agent.name} speaking={false} size="small" />
        )}
      </div>
      <div className="call-strip-text">
        <strong>
          <span className="call-live-dot" aria-hidden="true" />
          Parley{duration ? ` · ${duration}` : ""}
        </strong>
        {recording ? (
          <span className="call-recording">{recording}</span>
        ) : (
          <span>{whoIsIn(names)}</span>
        )}
      </div>
      <div className="call-strip-acts">
        <button
          className="call-btn"
          type="button"
          data-state={mic ? undefined : "off"}
          aria-pressed={mic}
          aria-label={mic ? "Join with your mic on" : "Join muted"}
          onClick={() => setMic((value) => !value)}
        >
          {mic ? (
            <Mic size={16} aria-hidden="true" />
          ) : (
            <MicOff size={16} aria-hidden="true" />
          )}
        </button>
        <button
          className="call-btn"
          type="button"
          data-state={camera ? "on" : undefined}
          aria-pressed={camera}
          aria-label={
            camera ? "Join with your camera on" : "Join with your camera off"
          }
          onClick={() => setCamera((value) => !value)}
        >
          {camera ? (
            <Video size={16} aria-hidden="true" />
          ) : (
            <VideoOff size={16} aria-hidden="true" />
          )}
        </button>
        <span className="call-sep" aria-hidden="true" />
        <button
          className="call-quiet"
          type="button"
          disabled={joining}
          onClick={() => start(false, false)}
        >
          Listen in
        </button>
        <button
          className="call-join"
          type="button"
          disabled={joining}
          onClick={() => start(mic, camera)}
        >
          {joining ? "Joining…" : "Join"}
        </button>
      </div>
    </div>
  )
}

/** The member's parley, wherever they are: the mini player or the full page. */
export function CallSurface() {
  const call = useCall()
  const { active } = call
  const { call: view } = useRoomCall(active?.organizationId, active?.room, {
    enabled: active?.phase === "in",
  })
  if (!active) return null
  if (active.phase === "failed") return null
  if (active.expanded && active.phase === "in") {
    return <CallFullPage active={active} view={view} />
  }
  if (call.roomOnScreen === roomKey(active.organizationId, active.room))
    return null
  return <CallMiniPlayer active={active} view={view} />
}

function speakingMember(active: ActiveCall): string | undefined {
  return active.caption && !active.caption.agent
    ? active.caption.member
    : undefined
}

function CallMiniPlayer({
  active,
  view,
}: {
  active: ActiveCall
  view: CallView | null
}) {
  const { setMic, expand, leave } = useCall()
  const duration = useDuration(view?.startedAt)
  const speaking = speakingMember(active)
  const share = active.tiles.find((tile) => tile.screenTrack)
  const shown = share
    ? []
    : [
        ...active.tiles.filter((tile) => !tile.self),
        ...active.tiles.filter((tile) => tile.self),
      ].slice(0, 4)
  const people = view?.participants.length ?? active.tiles.length
  const recording = recordingLine(view)
  return (
    <div
      className="call-pip"
      role="region"
      aria-label={`${active.roomName} parley, mini player`}
    >
      {active.phase === "in" && (
        <div className="call-pip-stage">
          {share && <ShareCam tile={share} />}
          {shown.map((tile) => (
            <CallCam
              key={tile.id}
              member={tile.member}
              name={tile.name}
              tile={tile}
              speaking={tile.member === speaking}
            />
          ))}
        </div>
      )}
      <div className="call-pip-bar">
        <div className="call-pip-title">
          <strong>
            <span className="call-live-dot" aria-hidden="true" />
            {active.roomName}
            {duration ? ` · ${duration}` : ""}
          </strong>
          <small>
            {active.phase === "joining"
              ? "Joining the parley…"
              : (recording ??
                (share
                  ? `${share.self ? "You are" : `${share.name} is`} sharing · ${people} ${people === 1 ? "person" : "people"}`
                  : `${people} ${people === 1 ? "person" : "people"}`))}
          </small>
        </div>
        <button
          className="call-btn"
          type="button"
          data-state={active.mic ? undefined : "off"}
          aria-pressed={!active.mic}
          aria-label={active.mic ? "Mute" : "Unmute"}
          disabled={active.phase !== "in"}
          onClick={() => void setMic(!active.mic)}
        >
          {active.mic ? (
            <Mic size={15} aria-hidden="true" />
          ) : (
            <MicOff size={15} aria-hidden="true" />
          )}
        </button>
        <button
          className="call-btn"
          type="button"
          aria-label="Back to the parley"
          disabled={active.phase !== "in"}
          onClick={() => expand(true)}
        >
          <Maximize2 size={15} aria-hidden="true" />
        </button>
        <button
          className="call-leave"
          type="button"
          aria-label="Leave"
          onClick={() => void leave()}
        >
          <PhoneOff size={15} aria-hidden="true" />
        </button>
      </div>
      {active.privateAnswer && (
        <p className="call-pip-note" role="status">
          Only you: {active.privateAnswer}
        </p>
      )}
    </div>
  )
}

/**
 * The room's conversation beside the call. The room's own page moves its
 * conversation in here while it's open (useRoomChatSlot), so there's only
 * ever one; from any other page the pane says where to find it.
 */
function RoomChatPane({ room, roomName }: { room: string; roomName: string }) {
  const { setRoomChat } = useCall()
  const [element, setElement] = useState<HTMLDivElement | null>(null)
  const [claimed, setClaimed] = useState(false)
  useEffect(() => {
    if (!element) return
    setRoomChat({ room, element })
    const watch = new MutationObserver(() =>
      setClaimed(element.childElementCount > 0)
    )
    watch.observe(element, { childList: true })
    setClaimed(element.childElementCount > 0)
    return () => {
      watch.disconnect()
      setRoomChat(null)
    }
  }, [element, room, setRoomChat])
  return (
    <section
      className="call-transcript call-room"
      aria-label={`${roomName} room`}
    >
      <h2>Room</h2>
      {!claimed && (
        <p>
          Open {roomName}'s page to talk in its room while you're in the parley.
        </p>
      )}
      <div ref={setElement} className="call-room-slot" />
    </section>
  )
}

type Layout = "gallery" | "speaker"

function CallFullPage({
  active,
  view,
}: {
  active: ActiveCall
  view: CallView | null
}) {
  const {
    setMic,
    setCamera,
    setScreen,
    setRecording,
    setAgent,
    expand,
    leave,
    end,
  } = useCall()
  const [layout, setLayout] = useState<Layout>("gallery")
  const [captions, setCaptions] = useState(true)
  // The side pane: what's been said, the room's conversation, or neither.
  const [side, setSide] = useState<"transcript" | "room" | null>("transcript")
  const transcript = side === "transcript"
  const setTranscript = (toggle: (value: boolean) => boolean) =>
    setSide((current) =>
      toggle(current === "transcript") ? "transcript" : null
    )
  const [busy, setBusy] = useState(false)
  const duration = useDuration(view?.startedAt)
  const speaking = speakingMember(active)
  const share = active.tiles.find((tile) => tile.screenTrack)
  const agent = view?.agent
  const count = active.tiles.length + (agent?.present ? 1 : 0) + (share ? 1 : 0)
  const cols = count <= 1 ? 1 : count <= 4 ? 2 : count <= 9 ? 3 : 4
  const recording = recordingLine(view)
  const back = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    back.current?.focus()
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") expand(false)
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [expand])

  const act = (work: () => Promise<void>) => {
    setBusy(true)
    void work()
      .catch(() => undefined)
      .finally(() => setBusy(false))
  }

  const tiles = [
    ...(share ? [<ShareCam key="share" tile={share} />] : []),
    ...active.tiles.map((tile) => (
      <CallCam
        key={tile.id}
        member={tile.member}
        name={tile.name}
        tile={tile}
        size="big"
        speaking={tile.member === speaking}
      />
    )),
    ...(agent?.present
      ? [
          <AgentCam
            key="agent"
            name={agent.name}
            speaking={active.agentSpeaking}
            size="big"
          />,
        ]
      : []),
  ]

  return (
    <div
      className="call-full"
      role="dialog"
      aria-modal="true"
      aria-label={`${active.roomName} parley`}
    >
      <header className="call-top">
        <button
          ref={back}
          className="call-back"
          type="button"
          onClick={() => expand(false)}
        >
          <ArrowLeft size={15} aria-hidden="true" />
          {active.roomName}
        </button>
        <div className="call-title">
          <TeamspaceAvatar name={active.roomName} />
          <strong>Parley</strong>
          {recording && <span className="call-recording">{recording}</span>}
        </div>
        <div className="call-grow" />
        <div className="call-seg" role="group" aria-label="Layout">
          {(["gallery", "speaker"] as const).map((option) => (
            <button
              key={option}
              type="button"
              aria-pressed={layout === option}
              onClick={() => setLayout(option)}
            >
              {option === "gallery" ? "Gallery" : "Speaker"}
            </button>
          ))}
        </div>
      </header>
      <div className="call-body">
        <div
          className="call-gallery"
          aria-label="People in the parley"
          data-layout={
            layout === "speaker" && tiles.length > 1 ? "speaker" : "gallery"
          }
          style={
            {
              "--cols": cols,
              "--rows": Math.max(tiles.length - 1, 1),
            } as CSSProperties
          }
        >
          {tiles}
          {captions && active.caption && (
            <div className="call-caption" role="status">
              <b>{active.caption.name}</b>
              {active.caption.text}
            </div>
          )}
        </div>
        {transcript && (
          <section className="call-transcript" aria-label="Transcript">
            <h2>Transcript</h2>
            {active.heard.length === 0 ? (
              <p>What's said shows here as people speak.</p>
            ) : (
              <ol>
                {active.heard.map((line) => (
                  <li
                    key={line.id}
                    data-partial={line.partial ? "true" : undefined}
                  >
                    <b>{line.name}</b>
                    {line.text}
                  </li>
                ))}
              </ol>
            )}
          </section>
        )}
        {side === "room" && (
          <RoomChatPane
            room={roomKey(active.organizationId, active.room)}
            roomName={active.roomName}
          />
        )}
        {active.privateAnswer && (
          <div className="call-private" role="status">
            <b>Only you can see this</b>
            {active.privateAnswer}
          </div>
        )}
      </div>
      <div className="call-dock" role="toolbar" aria-label="Parley controls">
        <div className="call-dock-side">
          <span className="call-live-dot" aria-hidden="true" />
          {duration && <span className="call-time">{duration}</span>}
          <span>·</span>
          <span>
            {view?.participants.length ?? active.tiles.length}{" "}
            {(view?.participants.length ?? active.tiles.length) === 1
              ? "person"
              : "people"}
            {agent?.present ? `, ${agent.name} listening` : ""}
          </span>
        </div>
        <div className="call-ctrls">
          <button
            className="call-btn"
            type="button"
            data-state={active.mic ? undefined : "off"}
            aria-pressed={!active.mic}
            aria-label={active.mic ? "Mute" : "Unmute"}
            onClick={() => void setMic(!active.mic)}
          >
            {active.mic ? (
              <Mic size={18} aria-hidden="true" />
            ) : (
              <MicOff size={18} aria-hidden="true" />
            )}
          </button>
          <button
            className="call-btn"
            type="button"
            data-state={active.camera ? undefined : "off"}
            aria-pressed={active.camera}
            aria-label={active.camera ? "Turn camera off" : "Turn camera on"}
            onClick={() => void setCamera(!active.camera)}
          >
            {active.camera ? (
              <Video size={18} aria-hidden="true" />
            ) : (
              <VideoOff size={18} aria-hidden="true" />
            )}
          </button>
          <button
            className="call-btn"
            type="button"
            data-state={active.screen ? "on" : undefined}
            aria-pressed={active.screen}
            aria-label={active.screen ? "Stop sharing" : "Share screen"}
            onClick={() => void setScreen(!active.screen)}
          >
            <MonitorUp size={18} aria-hidden="true" />
          </button>
          <button
            className="call-btn"
            type="button"
            data-state={view?.recording.on ? "off" : undefined}
            aria-pressed={view?.recording.on ?? false}
            aria-label={view?.recording.on ? "Stop recording" : "Record"}
            disabled={busy}
            onClick={() => act(() => setRecording(!view?.recording.on))}
          >
            <Circle
              size={16}
              fill={view?.recording.on ? "currentColor" : "none"}
              aria-hidden="true"
            />
          </button>
          <button
            className="call-btn"
            type="button"
            data-state={agent?.present ? "on" : undefined}
            aria-pressed={agent?.present ?? false}
            aria-label={
              agent?.present
                ? `Dismiss ${agent.name}`
                : `Ask ${agent?.name ?? "the agent"} in`
            }
            disabled={busy}
            onClick={() => act(() => setAgent(!agent?.present))}
          >
            <Sparkles size={18} aria-hidden="true" />
          </button>
          <button
            className="call-leave"
            type="button"
            onClick={() => void leave()}
          >
            <PhoneOff size={16} aria-hidden="true" />
            Leave
          </button>
        </div>
        <div className="call-dock-side">
          <button
            className="call-btn"
            type="button"
            data-state={captions ? "on" : undefined}
            aria-pressed={captions}
            aria-label={captions ? "Hide captions" : "Show captions"}
            onClick={() => setCaptions((value) => !value)}
          >
            <Captions size={16} aria-hidden="true" />
          </button>
          <button
            className="call-btn"
            type="button"
            aria-pressed={side === "room"}
            aria-label={side === "room" ? "Hide the room" : "Show the room"}
            onClick={() =>
              setSide((current) => (current === "room" ? null : "room"))
            }
          >
            <MessageSquareText size={16} aria-hidden="true" />
          </button>
          <button
            className="call-btn"
            type="button"
            aria-pressed={transcript}
            aria-label={
              transcript ? "Hide the transcript" : "Show the transcript"
            }
            onClick={() => setTranscript((value) => !value)}
          >
            <ScrollText size={16} aria-hidden="true" />
          </button>
          <button
            className="call-quiet"
            type="button"
            disabled={busy}
            onClick={() => act(end)}
          >
            <X size={14} aria-hidden="true" />
            End for everyone
          </button>
        </div>
      </div>
    </div>
  )
}
