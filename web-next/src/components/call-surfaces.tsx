// A parley's surfaces (B6; founder's redesign, 14 Oct 2026, "Parley" on the
// design canvas): the strip that joins from a teamspace, the mini player
// that follows the member to other pages, and the full page. The state is
// CallProvider's; the call's view is the edge's (useRoomCall).
import { useEffect, useRef, useState } from "react"
import type { CSSProperties } from "react"
import {
  ArrowLeft,
  Captions,
  Hand,
  SmilePlus,
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
import { roomKey, useCall, useSignalsOf } from "./call-provider"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Toggle } from "@/components/ui/toggle"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import type { ActiveCall, EndedCall } from "./call-provider"
import { IdentityAvatar, TeamspaceAvatar } from "./workspace-avatar"
import { Popup, PopupFoot, PopupHead, PopupIcon } from "./popup"
import { notifyParleyStarted } from "./workspace-toasts"
import { agentIdentity, personIdentity } from "./teamspace-format"
import { callDuration, wakeNames, whoIsIn } from "@/lib/call/client"
import type { CallView, VoiceTrouble } from "@/lib/call/client"
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
export function useDuration(startedAt: string | undefined): string | undefined {
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
  size: "default" | "big" | "small",
  hand: boolean
): React.ReactNode {
  if (hand)
    return (
      <CallFlag icon={<Hand size={11} aria-hidden="true" />}>
        {size === "small" ? undefined : "Raised hand"}
      </CallFlag>
    )
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
  const { hand, reaction } = useSignalsOf(member)
  const label = `${tile?.self ? "You" : name}${speaking ? ", speaking" : ""}${muted ? ", muted" : ""}${tile?.screenTrack ? ", sharing" : ""}${hand ? ", hand raised" : ""}`
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
      {named && (flag ?? flagOf(tile, size, hand))}
      {reaction && (
        <span
          key={reaction.id}
          className="call-reaction"
          role="img"
          aria-label={`Reacted ${reaction.emoji}`}
        >
          {reaction.emoji}
        </span>
      )}
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

/**
 * What the agent is doing, in a word or two: it promises notes only when
 * the call will leave them (T1), and says when it can't answer (T2).
 */
export function agentDoing(
  agent: CallView["agent"] | undefined,
  speaking: boolean,
  short = false
): string {
  if (speaking) return short ? "Speaking" : "speaking"
  if (agent?.canAnswer === false)
    return short ? "Can't answer" : "can't answer right now"
  if (agent?.notes) return short ? "Notes" : "taking notes"
  return short ? "Listening" : "listening"
}

/** 'Say "Arlo, …" to ask': how to talk to the agent (T3). */
export function wakeHint(
  agent: CallView["agent"] | undefined
): string | undefined {
  if (!agent?.present || agent.canAnswer === false) return undefined
  const names = wakeNames(agent)
  if (names.length === 0) return undefined
  return `Say ${names.map((name) => `“${name}, …”`).join(" or ")} to ask`
}

/** Why the agent stayed quiet, for the member who spoke to it (T2). */
export function troubleLine(name: string, reason: VoiceTrouble): string {
  if (reason === "model_failed")
    return `${name} couldn't work that out. Try again.`
  return `${name} can't answer in calls right now.`
}

/** The agent in the call: it never has a camera, only its voice. */
export function AgentCam({
  name,
  speaking,
  size = "default",
  named = true,
  flagged = named,
  agent,
}: {
  name: string
  speaking: boolean
  size?: "default" | "big" | "small"
  named?: boolean
  /** Its Listening / Speaking flag; the join strip's faces go without. */
  flagged?: boolean
  /** The call's view of it: whether it takes notes or can answer. */
  agent?: CallView["agent"]
}) {
  const doing = agentDoing(agent, speaking, true)
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
      {flagged && (
        <CallFlag
          agent
          icon={speaking ? <Wave /> : <Sparkles size={11} aria-hidden="true" />}
        >
          {size === "small" || speaking
            ? doing
            : doing === "Notes"
              ? "Taking notes"
              : doing}
        </CallFlag>
      )}
      {named && <span className="call-name">{name}</span>}
    </div>
  )
}

/** A screen someone is sharing, as its own tile. */
export function ShareCam({
  tile,
  named = true,
}: {
  tile: CallTile
  /** Its own label; a share under a label bar goes without. */
  named?: boolean
}) {
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
      {named && (
        <span className="call-name">
          <MonitorUp size={12} aria-hidden="true" />
          {owner} screen
        </span>
      )}
    </div>
  )
}

export function recordingLine(call: CallView | null): string | undefined {
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
 * runs it stays, with the live dot: it joins (as the strip's Join does), or
 * opens the parley the member is already in.
 */
export function ParleyButton({
  organizationId,
  room,
  roomName,
  subtitle,
  member,
  name,
  agents,
  blocked,
}: {
  organizationId: string
  room: LiveRoom
  roomName: string
  /** The line under "Parley" on the full page: the room's description. */
  subtitle?: string
  member: string
  name?: string
  /** The names the room shows for its agents, which the call's agent answers to. */
  agents?: string[]
  /** Why the member can't parley here (not on the team, paused), if they can't. */
  blocked?: string
}) {
  const { active, join, show } = useCall()
  const { call, unavailable } = useRoomCall(organizationId, room, {
    enabled: !blocked,
  })
  const here = isThisRoom(active, organizationId, room)
  const inIt = here && active?.phase === "in"
  const live = Boolean(call) || inIt
  return (
    <Button
      variant="outline"
      className="workspace-teamspace-parley"
      data-live={live ? "true" : undefined}
      disabled={
        Boolean(blocked) || unavailable || (here && active?.phase === "joining")
      }
      aria-label={
        inIt ? "Open the parley" : call ? "Join the parley" : undefined
      }
      title={
        blocked ??
        (unavailable
          ? "Parleys aren't available in this teamspace yet."
          : undefined)
      }
      onClick={() => {
        if (inIt) {
          show("stage")
          return
        }
        void join({
          organizationId,
          room,
          roomName,
          subtitle,
          member,
          name,
          agents,
          mic: true,
          camera: false,
        })
      }}
    >
      {live ? (
        <span className="call-live-dot" aria-hidden="true" />
      ) : (
        <Mic size={13} strokeWidth={2} aria-hidden="true" />
      )}
      Parley
    </Button>
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
  agents,
  blocked,
}: {
  organizationId: string
  room: LiveRoom
  roomName: string
  /** The line under "Parley" on the full page: the room's description. */
  subtitle?: string
  member: string
  name?: string
  /** The names the room shows for its agents, which the call's agent answers to. */
  agents?: string[]
  /** Why the member can't parley here; the strip then shows nothing. */
  blocked?: string
}) {
  const { active, join, show, leave, showingRoom } = useCall()
  const { call, loading } = useRoomCall(organizationId, room, {
    enabled: !blocked,
  })
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

  // Someone else starting a parley while the member has the room open says
  // so, with Join (the first view read only sets what was already running).
  const seenCall = useRef<string | null | undefined>(undefined)
  const callId = call?.callId ?? null
  useEffect(() => {
    if (loading) return
    const before = seenCall.current
    seenCall.current = callId
    if (before === undefined || !call || callId === before) return
    if (call.startedBy === member) return
    const starter = call.participants.find(
      (person) => person.member === call.startedBy
    )
    if (!starter) return
    notifyParleyStarted({
      member: starter.member,
      name: starter.name,
      roomName,
      onJoin: () =>
        void join({
          organizationId,
          room,
          roomName,
          subtitle,
          member,
          name,
          agents,
          mic: true,
          camera: false,
        }),
    })
    // Only a new call matters; the rest is read when it arrives.
  }, [callId, loading])

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
  if (!call || blocked) return null

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
            <span>
              {whoIsIn(names)}
              {wakeHint(call.agent) ? ` · ${wakeHint(call.agent)}` : ""}
            </span>
          )}
        </div>
        <div className="call-strip-acts">
          <button
            className="call-quiet"
            type="button"
            onClick={() => show("stage")}
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
      agents,
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
          <AgentCam
            name={call.agent.name}
            speaking={false}
            size="small"
            flagged={false}
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
  if (active.surface === "full" && active.phase === "in") {
    return <CallFullPage active={active} view={view} />
  }
  // The stage is the workspace's own (CallStageHost).
  if (active.surface === "stage" && active.phase === "in" && call.stageHosted)
    return null
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
        {ended.notes
          ? " The agent's notes will appear in the room shortly."
          : ""}
      </PopupHead>
      <PopupFoot>
        <Button ref={done} onClick={onDone}>
          Done
        </Button>
      </PopupFoot>
    </Popup>
  )
}

export function speakingMember(active: ActiveCall): string | undefined {
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
  const { setMic, show, leave } = useCall()
  const duration = useDuration(view?.startedAt)
  const speaking = speakingMember(active)
  const share = active.tiles.find((tile) => tile.screenTrack)
  const shown = share
    ? []
    : [
        ...active.tiles.filter((tile) => !tile.self),
        ...active.tiles.filter((tile) => tile.self),
      ].slice(0, 4)
  // Over a share, the speaker's cam sits inset (else the sharer's own).
  const inset = share
    ? (active.tiles.find((tile) => !tile.self && tile.member === speaking) ??
      (share.self ? undefined : share))
    : undefined
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
          {inset && (
            <div className="call-pip-speaker">
              <CallCam
                member={inset.member}
                name={inset.name}
                tile={inset}
                size="small"
                speaking={inset.member === speaking}
                flag={false}
              />
            </div>
          )}
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
          onClick={() => show("stage")}
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
      <AgentNotices active={active} view={view} className="call-pip-note" />
    </div>
  )
}

/**
 * What the member should know about the agent and their own words: what
 * it heard them say (T3), why it stayed quiet (T2), and that their words
 * aren't reaching the room (T4).
 */
export function AgentNotices({
  active,
  view,
  className,
  inline = false,
}: {
  active: ActiveCall
  view: CallView | null
  className?: string
  /** In a line of text (the dock), not a block of its own. */
  inline?: boolean
}) {
  const Line = inline ? "span" : "p"
  const name = view?.agent.name ?? "The agent"
  return (
    <>
      {active.transcriptFailing && (
        <Line className={className} role="alert">
          Your words aren't reaching the room. The agent can't hear you;
          retrying.
        </Line>
      )}
      {active.agentTrouble && (
        <Line className={className} role="status">
          {troubleLine(name, active.agentTrouble)}
        </Line>
      )}
      {active.agentHeard && !active.agentTrouble && (
        <Line className={className} role="status">
          {name} heard: “{active.agentHeard}”
        </Line>
      )}
    </>
  )
}

/** The member's microphone, camera and speaker, while they're in the call. */
export function DevicePicker({ onClose }: { onClose: () => void }) {
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
export function RoomChatPane({
  room,
  roomName,
  onClose,
}: {
  room: string
  roomName: string
  /** A close X in its header, where the pane is a tile of its own. */
  onClose?: () => void
}) {
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
      <h2>
        Room
        {onClose && (
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Hide the room"
            onClick={onClose}
          >
            <X aria-hidden="true" />
          </Button>
        )}
      </h2>
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
export type Side = "transcript" | "room" | null

/** The reactions a member can send, in the order the menu offers them. */
export const REACTIONS = ["👍", "👏", "😂", "🎉", "❤️"]

/**
 * Raise hand and React, while the call can carry them (the member's preset
 * allows broadcasts); nothing otherwise.
 */
export function CallSignals({ active }: { active: ActiveCall }) {
  const { setHand, react } = useCall()
  if (!active.signals) return null
  const up = active.hands.includes(active.member)
  return (
    <>
      <Toggle
        className="call-btn"
        data-state={up ? "on" : undefined}
        pressed={up}
        aria-label={up ? "Lower hand" : "Raise hand"}
        onPressedChange={(on) => void setHand(on)}
      >
        <Hand size={17} aria-hidden="true" />
      </Toggle>
      <DropdownMenu>
        <DropdownMenuTrigger className="call-btn" aria-label="React">
          <SmilePlus size={17} aria-hidden="true" />
        </DropdownMenuTrigger>
        <DropdownMenuContent
          side="top"
          positionerClassName="call-menu"
          className="call-reactions"
        >
          {REACTIONS.map((emoji) => (
            <DropdownMenuItem
              key={emoji}
              aria-label={`React ${emoji}`}
              onClick={() => void react(emoji)}
            >
              {emoji}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
    </>
  )
}

/** "5 people, Krude listening": who the call has, for the dock. */
export function dockLine(active: ActiveCall, view: CallView | null): string {
  const people = view?.participants.length ?? active.tiles.length
  const agent = view?.agent
  return `${people} ${people === 1 ? "person" : "people"}${
    agent?.present
      ? `, ${agent.name} ${agentDoing(agent, active.agentSpeaking)}`
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
    show,
    stageHosted,
    leave,
    end,
    roomUnread,
  } = useCall()
  const [busy, setBusy] = useState(false)
  const [problem, setProblem] = useState("")
  const duration = useDuration(view?.startedAt)
  const agent = view?.agent
  const hint = wakeHint(agent)
  // A control that fails says so, instead of quietly doing nothing (T16).
  const act = (work: () => Promise<void>, failed: string) => {
    setBusy(true)
    setProblem("")
    void work()
      .catch(() => setProblem(failed))
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
        {problem ? (
          <span className="call-dock-note" role="alert">
            {problem}
          </span>
        ) : active.transcriptFailing ||
          active.agentTrouble ||
          active.agentHeard ? (
          <AgentNotices
            active={active}
            view={view}
            className="call-dock-note"
            inline
          />
        ) : (
          hint && <span className="call-hint">· {hint}</span>
        )}
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
        <CallSignals active={active} />
        <Toggle
          className="call-btn"
          data-state={view?.recording.on ? "off" : undefined}
          pressed={view?.recording.on ?? false}
          aria-label={view?.recording.on ? "Stop recording" : "Record"}
          disabled={busy}
          onPressedChange={() =>
            act(
              () => setRecording(!view?.recording.on),
              view?.recording.on
                ? "Couldn't stop the recording. Try again."
                : "Couldn't start recording. Try again."
            )
          }
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
          onPressedChange={() =>
            act(
              () => setAgent(!agent?.present),
              agent?.present
                ? `Couldn't dismiss ${agent.name}. Try again.`
                : `Couldn't ask ${agent?.name ?? "the agent"} in. Try again.`
            )
          }
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
        <Button
          variant="destructive"
          className="call-leave"
          onClick={() => void leave()}
        >
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
        <Button
          variant="destructive"
          className="call-quiet"
          disabled={busy}
          onClick={() => act(end, "Couldn't end the parley. Try again.")}
        >
          <X size={14} aria-hidden="true" />
          End for everyone
        </Button>
        <Button
          variant="ghost"
          className="call-btn"
          aria-label={mode === "full" ? "Exit full page" : "Full page"}
          onClick={() =>
            show(mode === "full" ? (stageHosted ? "stage" : "pip") : "full")
          }
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
  const { show, stageHosted, markRoomSeen, roomUnread } = useCall()
  // Out of the full page is the stage, where the workspace has one.
  const back = stageHosted ? "stage" : "pip"
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
  const backButton = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    backButton.current?.focus()
  }, [])
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") show(back)
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [show, back])

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
            agent={agent}
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
          ref={backButton}
          className="call-back"
          type="button"
          onClick={() => show(back)}
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
export function CallTranscript({ active }: { active: ActiveCall }) {
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
