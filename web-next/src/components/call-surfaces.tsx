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
  Minimize2,
  MonitorUp,
  PhoneOff,
  ScrollText,
  Settings2,
  Sparkles,
  Video,
  VideoOff,
  X,
} from "lucide-react"
import { roomKey, useCall } from "./call-provider"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Toggle } from "@/components/ui/toggle"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import type { ActiveCall, EndedCall } from "./call-provider"
import { IdentityAvatar, TeamspaceAvatar } from "./workspace-avatar"
import { Popup, PopupFoot, PopupHead, PopupIcon } from "./popup"
import { Button } from "@/components/ui/button"
import { agentIdentity, personIdentity } from "./teamspace-format"
import { callDuration, whoIsIn } from "@/lib/call/client"
import type { CallView } from "@/lib/call/client"
import type { CallDevices, CallTile, DeviceKind } from "@/lib/call/media"
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

/** A small label at a tile's top right: sharing, camera off, the agent. */
export function CallFlag({
  icon,
  agent = false,
  children,
}: {
  icon?: React.ReactNode
  agent?: boolean
  children?: React.ReactNode
}) {
  return (
    <span className="call-flag" data-agent={agent ? "true" : undefined}>
      {icon}
      {children}
    </span>
  )
}

/**
 * What a person's tile says about them on its own: they're sharing, or (on
 * a big tile) their camera is off. A flag passed in takes its place.
 */
function flagOf(
  tile: CallTile | undefined,
  size: "default" | "big" | "small"
): React.ReactNode {
  if (tile?.screenTrack)
    return (
      <CallFlag icon={<MonitorUp size={11} aria-hidden="true" />}>
        Sharing
      </CallFlag>
    )
  if (tile && !tile.video && size === "big")
    return (
      <CallFlag icon={<VideoOff size={11} aria-hidden="true" />}>
        Camera off
      </CallFlag>
    )
  return undefined
}

/** A person in the call: their camera, or their avatar on their colour. */
export function CallCam({
  member,
  name,
  tile,
  size = "default",
  speaking = false,
  flag,
  named = true,
}: {
  member: string
  name: string
  tile?: CallTile
  size?: "default" | "big" | "small"
  speaking?: boolean
  flag?: React.ReactNode
  /** Its name, mic and flags; a face in a row of faces has none. */
  named?: boolean
}) {
  const muted = tile ? !tile.audio : false
  const label = `${tile?.self ? "You" : name}${speaking ? ", speaking" : ""}${muted ? ", muted" : ""}${tile?.screenTrack ? ", sharing" : ""}`
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
      {named && (flag ?? flagOf(tile, size))}
      {named && (
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
  named = true,
}: {
  name: string
  speaking: boolean
  size?: "default" | "big" | "small"
  named?: boolean
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
      {named && (
        <CallFlag
          agent
          icon={speaking ? <Wave /> : <Sparkles size={11} aria-hidden="true" />}
        >
          {speaking ? "Speaking" : size === "small" ? "Notes" : "Taking notes"}
        </CallFlag>
      )}
      {named && <span className="call-name">{name}</span>}
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
  subtitle,
  member,
  name,
}: {
  organizationId: string
  room: LiveRoom
  roomName: string
  /** The line under "Parley" on the full page: the room's description. */
  subtitle?: string
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
          subtitle,
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
  subtitle,
  member,
  name,
}: {
  organizationId: string
  room: LiveRoom
  roomName: string
  /** The line under "Parley" on the full page: the room's description. */
  subtitle?: string
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
      subtitle,
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
            named={false}
          />
        ))}
        {call.agent.present && (
          <AgentCam
            name={call.agent.name}
            speaking={false}
            size="small"
            named={false}
          />
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
  const { active, ended } = call
  // Every hook runs on every render; only then does the surface choose.
  const { call: view } = useRoomCall(active?.organizationId, active?.room, {
    enabled: active?.phase === "in",
  })
  if (!active && ended) {
    return <CallEnded ended={ended} onDone={call.dismissEnded} />
  }
  if (!active) return null
  if (active.phase === "failed") return null
  if (active.expanded && active.phase === "in") {
    return <CallFullPage active={active} view={view} />
  }
  if (call.roomOnScreen === roomKey(active.organizationId, active.room))
    return null
  return <CallMiniPlayer active={active} view={view} />
}

function minutesBetween(
  from: string | undefined,
  to: string
): string | undefined {
  if (!from) return undefined
  const minutes = Math.round((Date.parse(to) - Date.parse(from)) / 60_000)
  if (!Number.isFinite(minutes) || minutes < 0) return undefined
  return minutes < 1 ? "under a minute" : `${minutes} min`
}

/**
 * A parley just ended (founder's redesign, "Parley ended" on the pop-ups
 * board): the Popup with the sea of bottles, how long it ran and who was in
 * it, and what was kept. The agent's notes, the save-recording switch, "Edit
 * first" and "Post notes to the room" join it with an API for the notes.
 */
function CallEnded({
  ended,
  onDone,
}: {
  ended: EndedCall
  onDone: () => void
}) {
  const done = useRef<HTMLButtonElement>(null)
  const length = minutesBetween(ended.startedAt, ended.endedAt)
  const kicker = [
    "Parley ended",
    length,
    `${ended.people} ${ended.people === 1 ? "person" : "people"}`,
  ]
    .filter(Boolean)
    .join(" · ")
  return (
    <Popup
      open
      onOpenChange={(open) => {
        if (!open) onDone()
      }}
      scene="sea"
      initialFocus={done}
    >
      <PopupIcon>
        <TeamspaceAvatar name={ended.roomName} />
      </PopupIcon>
      <PopupHead kicker={kicker} title={`What's kept from ${ended.roomName}`}>
        {ended.recorded
          ? `The recording and transcript, kept with ${ended.roomName}. Only people in it can open them.`
          : `The transcript, kept with ${ended.roomName}.`}
      </PopupHead>
      <PopupFoot>
        <Button ref={done} onClick={onDone}>
          Done
        </Button>
      </PopupFoot>
    </Popup>
  )
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

/** The member's microphone, camera and speaker, while they're in the call. */
function DevicePicker({ onClose }: { onClose: () => void }) {
  const { devices, setDevice, onDevicesChange } = useCall()
  const [list, setList] = useState<CallDevices | undefined>()
  const [problem, setProblem] = useState("")

  useEffect(() => {
    let current = true
    const read = () =>
      void devices()
        .then((found) => {
          if (current) setList(found)
        })
        .catch(() => {
          if (current) setProblem("Couldn't read your devices.")
        })
    read()
    const stop = onDevicesChange(read)
    return () => {
      current = false
      stop()
    }
  }, [devices, onDevicesChange])

  const choose = (kind: DeviceKind, deviceId: string) => {
    setProblem("")
    void setDevice(kind, deviceId)
      .then(() => devices().then(setList))
      .catch(() => setProblem("Couldn't switch to that device."))
  }

  const groups: {
    kind: DeviceKind
    label: string
    items: MediaDeviceInfo[]
  }[] = list
    ? [
        { kind: "audioinput", label: "Microphone", items: list.microphones },
        { kind: "videoinput", label: "Camera", items: list.cameras },
        { kind: "audiooutput", label: "Speaker", items: list.speakers },
      ]
    : []

  return (
    <div className="call-devices" role="dialog" aria-label="Devices">
      <div className="call-devices-head">
        <strong>Devices</strong>
        <button
          type="button"
          className="call-btn"
          aria-label="Close devices"
          onClick={onClose}
        >
          <X size={14} aria-hidden="true" />
        </button>
      </div>
      {!list && !problem && (
        <p className="call-devices-note">Reading your devices…</p>
      )}
      {groups.map((group) => (
        <label key={group.kind} className="call-device">
          <span>{group.label}</span>
          {group.items.length === 0 ? (
            <small>
              {group.kind === "audiooutput"
                ? "This browser plays through its default speaker."
                : "None found."}
            </small>
          ) : (
            <select
              value={list?.current[group.kind] ?? ""}
              onChange={(event) => choose(group.kind, event.target.value)}
            >
              {group.items.map((item, index) => (
                <option key={item.deviceId || index} value={item.deviceId}>
                  {item.label || `${group.label} ${index + 1}`}
                </option>
              ))}
            </select>
          )}
        </label>
      ))}
      {problem && (
        <p className="call-devices-note" role="alert">
          {problem}
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

type Layout = "gallery" | "speaker" | "shared"

/** What sits beside the call: what's been said, the room's conversation, or neither. */
type Side = "transcript" | "room" | null

/** "5 people, Krude taking notes": who the call has, for the dock. */
function dockLine(active: ActiveCall, view: CallView | null): string {
  const people = view?.participants.length ?? active.tiles.length
  const agent = view?.agent
  return `${people} ${people === 1 ? "person" : "people"}${
    agent?.present
      ? `, ${agent.name} ${active.agentSpeaking ? "speaking" : "taking notes"}`
      : ""
  }`
}

/**
 * The parley's controls, one dock wherever the call is open: the member's
 * mic, camera and screen, recording, the agent, devices and Leave; then
 * captions, the room (with what's new in it), the transcript, End for
 * everyone, and moving between the stage and the full page.
 */
export function CallDock({
  mode,
  active,
  view,
  captions,
  onCaptions,
  side,
  onSide,
  choosing,
  onChoosing,
}: {
  /** Where it is: the stage in the workspace, or the full page. */
  mode: "stage" | "full"
  active: ActiveCall
  view: CallView | null
  captions: boolean
  onCaptions: (on: boolean) => void
  /** The side pane, where the surface has one. */
  side?: Side
  onSide?: (side: Side) => void
  choosing: boolean
  onChoosing: (on: boolean) => void
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
    roomUnread,
  } = useCall()
  const [busy, setBusy] = useState(false)
  const duration = useDuration(view?.startedAt)
  const agent = view?.agent
  const act = (work: () => Promise<void>) => {
    setBusy(true)
    void work()
      .catch(() => undefined)
      .finally(() => setBusy(false))
  }
  const unread = side === "room" ? 0 : (roomUnread ?? 0)

  return (
    <div className="call-dock" role="toolbar" aria-label="Parley controls">
      <div className="call-dock-side">
        <span className="call-live-dot" aria-hidden="true" />
        {duration && <span className="call-time">{duration}</span>}
        <span>·</span>
        <span>{dockLine(active, view)}</span>
      </div>
      <div className="call-ctrls">
        <Toggle
          className="call-btn"
          data-state={active.mic ? undefined : "off"}
          pressed={!active.mic}
          aria-label={active.mic ? "Mute" : "Unmute"}
          onPressedChange={() => void setMic(!active.mic)}
        >
          {active.mic ? (
            <Mic size={18} aria-hidden="true" />
          ) : (
            <MicOff size={18} aria-hidden="true" />
          )}
        </Toggle>
        <Toggle
          className="call-btn"
          data-state={active.camera ? undefined : "off"}
          pressed={active.camera}
          aria-label={active.camera ? "Turn camera off" : "Turn camera on"}
          onPressedChange={() => void setCamera(!active.camera)}
        >
          {active.camera ? (
            <Video size={18} aria-hidden="true" />
          ) : (
            <VideoOff size={18} aria-hidden="true" />
          )}
        </Toggle>
        <Toggle
          className="call-btn"
          data-state={active.screen ? "on" : undefined}
          pressed={active.screen}
          aria-label={active.screen ? "Stop sharing" : "Share screen"}
          onPressedChange={() => void setScreen(!active.screen)}
        >
          <MonitorUp size={18} aria-hidden="true" />
        </Toggle>
        <Toggle
          className="call-btn"
          data-state={view?.recording.on ? "off" : undefined}
          pressed={view?.recording.on ?? false}
          aria-label={view?.recording.on ? "Stop recording" : "Record"}
          disabled={busy}
          onPressedChange={() => act(() => setRecording(!view?.recording.on))}
        >
          <Circle
            size={16}
            fill={view?.recording.on ? "currentColor" : "none"}
            aria-hidden="true"
          />
        </Toggle>
        <Toggle
          className="call-btn"
          data-state={agent?.present ? "on" : undefined}
          pressed={agent?.present ?? false}
          aria-label={
            agent?.present
              ? `Dismiss ${agent.name}`
              : `Ask ${agent?.name ?? "the agent"} in`
          }
          disabled={busy}
          onPressedChange={() => act(() => setAgent(!agent?.present))}
        >
          <Sparkles size={18} aria-hidden="true" />
        </Toggle>
        <Toggle
          className="call-btn"
          pressed={choosing}
          aria-label="Devices"
          onPressedChange={onChoosing}
        >
          <Settings2 size={16} aria-hidden="true" />
        </Toggle>
        <Button className="call-leave" onClick={() => void leave()}>
          <PhoneOff size={16} aria-hidden="true" />
          Leave
        </Button>
      </div>
      <div className="call-dock-side">
        <Toggle
          className="call-btn"
          data-state={captions ? "on" : undefined}
          pressed={captions}
          aria-label={captions ? "Hide captions" : "Show captions"}
          onPressedChange={onCaptions}
        >
          <Captions size={16} aria-hidden="true" />
        </Toggle>
        {onSide && (
          <Toggle
            className="call-btn"
            pressed={side === "room"}
            aria-label={
              side === "room"
                ? "Hide the room"
                : unread > 0
                  ? `Show the room, ${unread} new`
                  : "Show the room"
            }
            onPressedChange={(on) => onSide(on ? "room" : null)}
          >
            <MessageSquareText size={16} aria-hidden="true" />
            {unread > 0 && (
              <Badge className="call-badge" aria-hidden="true">
                {unread > 9 ? "9+" : unread}
              </Badge>
            )}
          </Toggle>
        )}
        {onSide && (
          <Toggle
            className="call-btn"
            pressed={side === "transcript"}
            aria-label={
              side === "transcript"
                ? "Hide the transcript"
                : "Show the transcript"
            }
            onPressedChange={(on) => onSide(on ? "transcript" : null)}
          >
            <ScrollText size={16} aria-hidden="true" />
          </Toggle>
        )}
        <Button className="call-quiet" disabled={busy} onClick={() => act(end)}>
          <X size={14} aria-hidden="true" />
          End for everyone
        </Button>
        <Button
          className="call-btn"
          aria-label={mode === "full" ? "Exit full page" : "Full page"}
          onClick={() => expand(mode !== "full")}
        >
          {mode === "full" ? (
            <Minimize2 size={16} aria-hidden="true" />
          ) : (
            <Maximize2 size={16} aria-hidden="true" />
          )}
        </Button>
      </div>
    </div>
  )
}

function CallFullPage({
  active,
  view,
}: {
  active: ActiveCall
  view: CallView | null
}) {
  const { expand, markRoomSeen, roomUnread } = useCall()
  const [chosen, setLayout] = useState<Layout>("gallery")
  const [captions, setCaptions] = useState(true)
  const [choosing, setChoosing] = useState(false)
  const [side, setSide] = useState<Side>("transcript")
  const speaking = speakingMember(active)
  const share = active.tiles.find((tile) => tile.screenTrack)
  // "Shared" needs something shared; without one it's the gallery.
  const layout = chosen === "shared" && !share ? "gallery" : chosen
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

  // While the room is open beside the call, what it says has been seen.
  useEffect(() => {
    if (side === "room") markRoomSeen()
  }, [side, roomUnread, markRoomSeen])

  const size = layout === "shared" ? "small" : "big"
  const cams = [
    ...active.tiles.map((tile) => (
      <CallCam
        key={tile.id}
        member={tile.member}
        name={tile.name}
        tile={tile}
        size={size}
        speaking={tile.member === speaking}
      />
    )),
    ...(agent?.present
      ? [
          <AgentCam
            key="agent"
            name={agent.name}
            speaking={active.agentSpeaking}
            size={size}
          />,
        ]
      : []),
  ]
  const shareTile = share ? <ShareCam key="share" tile={share} /> : null

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
          {recording ? (
            <span className="call-recording">{recording}</span>
          ) : (
            active.subtitle && <small>{active.subtitle}</small>
          )}
        </div>
        <div className="call-grow" />
        <ToggleGroup
          className="call-seg"
          aria-label="Layout"
          value={[layout]}
          onValueChange={(value: string[]) => {
            const next = value[0]
            if (next) setLayout(next as Layout)
          }}
        >
          <ToggleGroupItem value="gallery">Gallery</ToggleGroupItem>
          <ToggleGroupItem value="speaker">Speaker</ToggleGroupItem>
          <ToggleGroupItem
            value="shared"
            disabled={!share}
            title={share ? undefined : "Nobody is sharing a screen"}
          >
            Shared
          </ToggleGroupItem>
        </ToggleGroup>
      </header>
      <div className="call-body">
        {layout === "shared" ? (
          <div
            className="call-gallery"
            aria-label="People in the parley"
            data-layout="shared"
          >
            {shareTile}
            <div className="call-film">{cams}</div>
            {captions && active.caption && (
              <div className="call-caption" role="status">
                <b>{active.caption.name}</b>
                {active.caption.text}
              </div>
            )}
          </div>
        ) : (
          <div
            className="call-gallery"
            aria-label="People in the parley"
            data-layout={
              layout === "speaker" && count > 1 ? "speaker" : "gallery"
            }
            style={
              {
                "--cols": cols,
                "--rows": Math.max(count - 1, 1),
              } as CSSProperties
            }
          >
            {shareTile}
            {cams}
            {captions && active.caption && (
              <div className="call-caption" role="status">
                <b>{active.caption.name}</b>
                {active.caption.text}
              </div>
            )}
          </div>
        )}
        {side === "transcript" && <CallTranscript active={active} />}
        {choosing && <DevicePicker onClose={() => setChoosing(false)} />}
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
      <CallDock
        mode="full"
        active={active}
        view={view}
        captions={captions}
        onCaptions={setCaptions}
        side={side}
        onSide={setSide}
        choosing={choosing}
        onChoosing={setChoosing}
      />
    </div>
  )
}

/** What's been said in the call, as this page heard it. */
function CallTranscript({ active }: { active: ActiveCall }) {
  return (
    <section className="call-transcript" aria-label="Transcript">
      <h2>Transcript</h2>
      {active.heard.length === 0 ? (
        <p>What's said shows here as people speak.</p>
      ) : (
        <ol>
          {active.heard.map((line) => (
            <li key={line.id} data-partial={line.partial ? "true" : undefined}>
              <b>{line.name}</b>
              {line.text}
            </li>
          ))}
        </ol>
      )}
    </section>
  )
}
