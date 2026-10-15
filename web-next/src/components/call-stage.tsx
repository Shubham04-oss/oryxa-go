// The parley on the workspace's stage (founder's redesign, 15 Oct 2026,
// "HuddleShare" on the design canvas): the call in the main column, with the
// sidebars still beside it. A shared screen is the focused tile with a
// column of named cams; with no share, a grid of cams. The dock is the same
// as the full page's. The page underneath stays mounted (and inert), so a
// room's conversation can move into the Room pane and back.
import { useEffect, useRef, useState } from "react"
import type { CSSProperties } from "react"
import { Maximize2, PictureInPicture2 } from "lucide-react"
import { roomKey, useCall } from "./call-provider"
import type { ActiveCall } from "./call-provider"
import {
  AgentCam,
  CallCam,
  CallDock,
  CallTranscript,
  DevicePicker,
  RoomChatPane,
  ShareCam,
  recordingLine,
  speakingMember,
  useDuration,
} from "./call-surfaces"
import type { Side } from "./call-surfaces"
import { IdentityAvatar, TeamspaceAvatar } from "./workspace-avatar"
import { personIdentity } from "./teamspace-format"
import { Button } from "@/components/ui/button"
import { Toggle } from "@/components/ui/toggle"
import { useRoomCall } from "@/lib/call/use-room-call"
import "./call.css"
import "./call-stage.css"

/** How many faces the stage's header shows before "+N". */
const FACES = 5

/**
 * Where the workspace's main column can show the parley's stage: placed in
 * the column, beside its content. While the stage is open the content stays
 * mounted underneath, inert. `place` names what the column shows; going
 * somewhere else leaves the stage for the mini player.
 */
export function CallStageHost({ place }: { place: string }) {
  const { active, setStageHosted, show } = useCall()
  const slot = useRef<HTMLDivElement>(null)
  useEffect(() => {
    setStageHosted(true)
    return () => setStageHosted(false)
  }, [setStageHosted])
  const onStage = active?.surface === "stage" && active.phase === "in"
  const was = useRef(place)
  useEffect(() => {
    if (was.current === place) return
    was.current = place
    if (onStage) show("pip")
  }, [place, onStage, show])
  // The column's own content can't be reached under the stage.
  useEffect(() => {
    const parent = slot.current?.parentElement
    if (!onStage || !parent) return
    const under = [...parent.children].filter(
      (element): element is HTMLElement =>
        element !== slot.current &&
        element instanceof HTMLElement &&
        !element.inert
    )
    for (const element of under) element.inert = true
    return () => {
      for (const element of under) element.inert = false
    }
  })
  return (
    <div ref={slot} className="call-stage-slot">
      {onStage && <CallStage active={active} />}
    </div>
  )
}

export function CallStage({ active }: { active: ActiveCall }) {
  const { show, markRoomSeen, roomUnread } = useCall()
  const { call: view } = useRoomCall(active.organizationId, active.room)
  const [captions, setCaptions] = useState(true)
  const [choosing, setChoosing] = useState(false)
  const [side, setSide] = useState<Side>(null)
  const [fill, setFill] = useState(false)
  const duration = useDuration(view?.startedAt)
  const speaking = speakingMember(active)
  const share = active.tiles.find((tile) => tile.screenTrack)
  const agent = view?.agent
  const recording = recordingLine(view)
  const people = view?.participants ?? []
  const title = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    title.current?.focus()
  }, [])
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented) show("pip")
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [show])
  useEffect(() => {
    if (side === "room") markRoomSeen()
  }, [side, roomUnread, markRoomSeen])

  const size = share ? "small" : "default"
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
  const count = cams.length
  const cols = count <= 1 ? 1 : count <= 4 ? 2 : count <= 9 ? 3 : 4
  const owner = share ? (share.self ? "Your" : `${share.name}'s`) : ""

  return (
    <section
      className="call-stage"
      aria-label={`${active.roomName} parley`}
      data-fill={fill ? "true" : undefined}
    >
      <header className="call-stage-head">
        <Button
          ref={title}
          variant="ghost"
          size="icon-sm"
          aria-label="Mini player"
          title="Mini player"
          onClick={() => show("pip")}
        >
          <PictureInPicture2 aria-hidden="true" />
        </Button>
        <div className="call-stage-crumbs">
          <TeamspaceAvatar name={active.roomName} />
          <strong>{active.roomName}</strong>
          <span aria-hidden="true">·</span>
          <span className="call-stage-live">
            <span className="call-live-dot" aria-hidden="true" />
            Parley{duration ? ` · ${duration}` : ""}
          </span>
          {recording && <span className="call-recording">{recording}</span>}
        </div>
        <div className="call-grow" />
        {people.length > 0 && (
          <span
            className="call-stage-faces"
            role="img"
            aria-label={`${people.length} in the parley`}
          >
            {people.slice(0, FACES).map((person) => (
              <IdentityAvatar
                key={person.member}
                identity={personIdentity(person.member, person.name)}
              />
            ))}
            {people.length > FACES && <small>+{people.length - FACES}</small>}
          </span>
        )}
        <Button variant="outline" onClick={() => show("full")}>
          <Maximize2 aria-hidden="true" />
          Full page
        </Button>
      </header>
      <div className="call-stage-body">
        {share ? (
          <div className="call-split">
            <div className="call-show">
              <div className="call-show-label">
                <strong>{owner} screen</strong>
                <span className="call-grow" />
                <Toggle
                  variant="outline"
                  size="sm"
                  pressed={fill}
                  onPressedChange={setFill}
                  aria-label={fill ? "Show the people" : "Fill the stage"}
                >
                  <Maximize2 aria-hidden="true" />
                  Fill
                </Toggle>
              </div>
              <ShareCam tile={share} named={false} />
            </div>
            {!fill && (
              <div
                className="call-col"
                role="group"
                aria-label="People in the parley"
              >
                {cams}
              </div>
            )}
          </div>
        ) : (
          <div
            className="call-gallery"
            role="group"
            aria-label="People in the parley"
            style={{ "--cols": cols } as CSSProperties}
          >
            {cams}
          </div>
        )}
        {captions && active.caption && (
          <div className="call-caption" role="status">
            <b>{active.caption.name}</b>
            {active.caption.text}
          </div>
        )}
        {side === "transcript" && <CallTranscript active={active} />}
        {side === "room" && (
          <RoomChatPane
            room={roomKey(active.organizationId, active.room)}
            roomName={active.roomName}
          />
        )}
        {choosing && <DevicePicker onClose={() => setChoosing(false)} />}
        {active.privateAnswer && (
          <div className="call-private" role="status">
            <b>Only you can see this</b>
            {active.privateAnswer}
          </div>
        )}
      </div>
      <CallDock
        mode="stage"
        active={active}
        view={view}
        captions={captions}
        onCaptions={setCaptions}
        side={side}
        onSide={setSide}
        choosing={choosing}
        onChoosing={setChoosing}
      />
    </section>
  )
}
