// The parley on the workspace's stage (founder's redesign, 15 Oct 2026,
// "HuddleShare" on the design canvas): the call in the main column, with the
// sidebars still beside it. A shared screen is the focused tile with a
// column of named cams; with no share, a grid of cams. The dock is the same
// as the full page's. The page underneath stays mounted (and inert), so a
// room's conversation can move into the Room pane and back.
import { useEffect, useRef, useState } from "react"
import type { CSSProperties } from "react"
import {
  Globe,
  Hand,
  Maximize2,
  Mic,
  MicOff,
  MonitorUp,
  PhoneOff,
  PictureInPicture2,
  Video,
  VideoOff,
} from "lucide-react"
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
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@/components/ui/resizable"
import { BrowserPane } from "./browser-pane"
import { BrowserSteps, useRoomChildren } from "./live-work"
import { useRoomCall } from "@/lib/call/use-room-call"
import { askedForHand, childLabel } from "@/lib/live/children"
import type { LiveChild } from "@/lib/live/children"
import type { LiveEvent, LiveRoom } from "@/lib/live/client"
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

/**
 * An agent's browser in the parley (HuddleBrowse): its live page, whether
 * it's working or asking for a hand (take over and hand back are the
 * browser pane's), and its last steps from the room's log.
 */
function ParleyBrowser({
  organizationId,
  room,
  child,
  events,
  viewerId,
  agentName,
}: {
  organizationId: string
  room: LiveRoom
  child: LiveChild
  events: readonly LiveEvent[]
  viewerId: string
  agentName?: string
}) {
  const label = childLabel(child, viewerId)
  const asked = askedForHand(events, child.name)
  return (
    <section className="call-show call-browser" aria-label={label}>
      <div className="call-show-label">
        <Globe size={14} aria-hidden="true" />
        <strong>{label}</strong>
        <span className="call-browser-status" data-asked={asked || undefined}>
          {asked ? (
            <>
              <Hand size={11} aria-hidden="true" /> Needs a hand
            </>
          ) : (
            <>
              <span className="call-live-dot" aria-hidden="true" /> Working
            </>
          )}
        </span>
      </div>
      <BrowserPane
        organizationId={organizationId}
        room={room}
        child={child}
        events={events}
        agentName={agentName}
        compact
      />
      <BrowserSteps events={events} browser={child.name} running />
    </section>
  )
}

/** "Parley, room, Raya's screen and Browser". */
function namesOf(names: string[]): string {
  return names.length < 2
    ? (names[0] ?? "")
    : `${names.slice(0, -1).join(", ")} and ${names.at(-1)}`
}

/**
 * Everything at once (HuddleMix): the people with compact controls and the
 * room's conversation on the left, the shared screen and browsers stacked
 * on the right, with grips between them.
 */
function CallMix({
  active,
  people,
  duration,
  cams,
  works,
  onCloseRoom,
}: {
  active: ActiveCall
  people: number
  duration?: string
  cams: React.ReactNode[]
  works: React.ReactNode[]
  onCloseRoom: () => void
}) {
  const { setMic, setCamera, setScreen, show, leave } = useCall()
  return (
    <div className="call-stage-body">
      <ResizablePanelGroup orientation="horizontal" className="call-mix">
        <ResizablePanel defaultSize="40" minSize="25">
          <div className="call-mix-left">
            <section
              className="call-show call-people"
              aria-label={`Parley, ${people} ${people === 1 ? "person" : "people"}`}
            >
              <div className="call-show-label">
                <strong>Parley</strong>
                <span>
                  {people} {people === 1 ? "person" : "people"}
                </span>
                <span className="call-stage-live">
                  <span className="call-live-dot" aria-hidden="true" />
                  {duration}
                </span>
              </div>
              <div
                className="call-mini"
                role="group"
                aria-label="People in the parley"
              >
                {cams}
              </div>
              <div
                className="call-mini-ctrls"
                role="toolbar"
                aria-label="Parley controls"
              >
                <Toggle
                  className="call-btn"
                  data-state={active.mic ? undefined : "off"}
                  pressed={!active.mic}
                  aria-label={active.mic ? "Mute" : "Unmute"}
                  onPressedChange={() => void setMic(!active.mic)}
                >
                  {active.mic ? (
                    <Mic aria-hidden="true" />
                  ) : (
                    <MicOff aria-hidden="true" />
                  )}
                </Toggle>
                <Toggle
                  className="call-btn"
                  data-state={active.camera ? undefined : "off"}
                  pressed={active.camera}
                  aria-label={
                    active.camera ? "Turn camera off" : "Turn camera on"
                  }
                  onPressedChange={() => void setCamera(!active.camera)}
                >
                  {active.camera ? (
                    <Video aria-hidden="true" />
                  ) : (
                    <VideoOff aria-hidden="true" />
                  )}
                </Toggle>
                <Toggle
                  className="call-btn"
                  data-state={active.screen ? "on" : undefined}
                  pressed={active.screen}
                  aria-label={active.screen ? "Stop sharing" : "Share screen"}
                  onPressedChange={() => void setScreen(!active.screen)}
                >
                  <MonitorUp aria-hidden="true" />
                </Toggle>
                <Button
                  variant="ghost"
                  className="call-btn"
                  aria-label="Full page"
                  onClick={() => show("full")}
                >
                  <Maximize2 aria-hidden="true" />
                </Button>
                <span className="call-grow" />
                <Button
                  variant="destructive"
                  size="sm"
                  className="call-leave"
                  onClick={() => void leave()}
                >
                  <PhoneOff aria-hidden="true" />
                  Leave
                </Button>
              </div>
            </section>
            <RoomChatPane
              room={roomKey(active.organizationId, active.room)}
              roomName={active.roomName}
              onClose={onCloseRoom}
            />
          </div>
        </ResizablePanel>
        <ResizableHandle withHandle className="call-grip" />
        <ResizablePanel defaultSize="60" minSize="30">
          <ResizablePanelGroup
            orientation="vertical"
            className="call-mix-right"
          >
            {works.flatMap((work, index) => [
              ...(index > 0
                ? [
                    <ResizableHandle
                      key={`grip-${index}`}
                      withHandle
                      className="call-grip"
                    />,
                  ]
                : []),
              <ResizablePanel key={`work-${index}`} minSize="20">
                {work}
              </ResizablePanel>,
            ])}
          </ResizablePanelGroup>
        </ResizablePanel>
      </ResizablePanelGroup>
      {active.privateAnswer && (
        <div className="call-private" role="status">
          <b>Only you can see this</b>
          {active.privateAnswer}
        </div>
      )}
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
  // The room's running browsers join the stage as tiles.
  const { children, log } = useRoomChildren(active.organizationId, active.room)
  const browsers = (children.data ?? []).filter(
    (child) => child.kind === "browser" && child.status === "running"
  )

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

  const size = share || browsers.length > 0 ? "small" : "default"
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
  // The room open beside shared work: everything as tiles (HuddleMix).
  const mix = side === "room" && (Boolean(share) || browsers.length > 0)
  const open = [
    "Parley",
    "room",
    ...(share ? [`${owner} screen`] : []),
    ...browsers.map((child) => childLabel(child, active.member)),
  ]

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
          {mix ? (
            <span>{namesOf(open)}</span>
          ) : (
            <span className="call-stage-live">
              <span className="call-live-dot" aria-hidden="true" />
              Parley{duration ? ` · ${duration}` : ""}
            </span>
          )}
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
        {!mix && (
          <Button variant="outline" onClick={() => show("full")}>
            <Maximize2 aria-hidden="true" />
            Full page
          </Button>
        )}
      </header>
      {mix ? (
        <CallMix
          active={active}
          people={view?.participants.length ?? active.tiles.length}
          duration={duration}
          cams={cams}
          works={[
            ...(share
              ? [
                  <div key="share" className="call-show">
                    <div className="call-show-label">
                      <strong>{owner} screen</strong>
                    </div>
                    <ShareCam tile={share} named={false} />
                  </div>,
                ]
              : []),
            ...browsers.map((child) => (
              <ParleyBrowser
                key={child.name}
                organizationId={active.organizationId}
                room={active.room}
                child={child}
                events={log.events}
                viewerId={active.member}
                agentName={agent?.name}
              />
            )),
          ]}
          onCloseRoom={() => setSide(null)}
        />
      ) : (
        <div className="call-stage-body">
          {browsers.length > 0 ? (
            <div className="call-browse">
              <div
                className="call-film"
                role="group"
                aria-label="People in the parley"
              >
                {cams}
              </div>
              <div className="call-works">
                {share && (
                  <div className="call-show">
                    <div className="call-show-label">
                      <strong>{owner} screen</strong>
                    </div>
                    <ShareCam tile={share} named={false} />
                  </div>
                )}
                {browsers.map((child) => (
                  <ParleyBrowser
                    key={child.name}
                    organizationId={active.organizationId}
                    room={active.room}
                    child={child}
                    events={log.events}
                    viewerId={active.member}
                    agentName={agent?.name}
                  />
                ))}
              </div>
            </div>
          ) : share ? (
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
      )}
      {!mix && (
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
      )}
    </section>
  )
}
