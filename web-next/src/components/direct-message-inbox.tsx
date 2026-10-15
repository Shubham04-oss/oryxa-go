// The member's conversations as the page lists them (REALTIME.md, the
// inbox): the agent panel's DMs tab, with its "New message" picker and the
// compact conversation. The sidebar's Messages (workspace-sidebar.tsx) lists
// the latest. They all read the one inbox the shell holds (useInbox), and
// name people from the organization's members (useDmPeople).
import { useEffect, useId, useRef, useState } from "react"
import { ArrowLeft, Search, SquarePen } from "lucide-react"
import { signInAgain } from "./conversation-log"
import { DirectMessageView, PersonAvatar, UnreadCount } from "./direct-message"
import { conversationPreview, listTime } from "./direct-message-format"
import { filterPeople, personContext } from "./direct-message-people"
import type { DmPeople } from "./direct-message-people"
import { usePeerOnline } from "./direct-message-presence"
import { SideConversationView, emptySideDraft } from "./side-conversation"
import type { SideDraft } from "./side-conversation"
import { fullTime } from "./teamspace-format"
import type { Conversation, Inbox } from "@/lib/dms"
import { parseSideConversationName, peerIn } from "@/lib/dms/names"
import { cn } from "@/lib/utils"

/** Now, again every minute, so "5m" becomes "6m". */
export function useMinuteClock(): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(timer)
  }, [])
  return now
}

/**
 * What the DMs tab shows: its list, the picker for a new message, one DM
 * (`peerId`), or one side conversation (`side`, its object's name).
 */
export type DmPanelView = {
  peerId: string | null
  picking: boolean
  side?: string
}

export const dmPanelList: DmPanelView = { peerId: null, picking: false }

type FocusTarget = { peerId: string } | { side: string } | "new" | null

/**
 * The agent panel's DMs tab (DESIGN.md §2): the member's conversations,
 * "New message" to pick anyone in the organization, and a conversation (a
 * DM, or a side conversation in a teamspace) in its compact layout with the
 * way back to the list. Its place (`view`) and drafts are the panel's, so
 * they outlast the panel collapsing.
 */
export function DirectMessagesTab({
  organizationId,
  viewerId,
  inbox,
  people,
  view,
  onViewChange,
  drafts,
  onDraftChange,
  sideDrafts,
  onSideDraftChange,
  onComposingChange,
}: {
  organizationId: string
  viewerId: string
  inbox: Inbox
  people: DmPeople
  view: DmPanelView
  onViewChange: (view: DmPanelView) => void
  drafts: Partial<Record<string, string>>
  onDraftChange: (peerId: string, draft: string) => void
  /** Side conversations' drafts, by the conversation's name. */
  sideDrafts: Partial<Record<string, SideDraft>>
  onSideDraftChange: (name: string, draft: SideDraft) => void
  onComposingChange?: (composing: boolean) => void
}) {
  const [focusTarget, setFocusTarget] = useState<FocusTarget>(null)
  // The member opened this conversation just now (not the tab returning to it).
  const [picked, setPicked] = useState(false)

  function open(peerId: string) {
    setPicked(true)
    onViewChange({ peerId, picking: false })
  }

  function openSide(name: string) {
    setPicked(true)
    onViewChange({ peerId: null, picking: false, side: name })
  }

  if (view.side) {
    const name = view.side
    // The conversation's object needs only its ids, whichever page is open.
    const ids = parseSideConversationName(name)
    const peerId = ids ? peerIn(ids, viewerId) : undefined
    const peer = peerId ? people.peer(peerId) : undefined
    const back = () => {
      setPicked(false)
      setFocusTarget({ side: name })
      onViewChange(dmPanelList)
    }
    if (!ids || !peerId) {
      return (
        <p className="workspace-agent-empty" role="status">
          This conversation isn't available.{" "}
          <button
            className="workspace-agent-text-button"
            type="button"
            onClick={back}
          >
            Back to conversations
          </button>
        </p>
      )
    }
    if (!peer) {
      return (
        <p className="workspace-agent-empty" role="status">
          Loading the conversation…
        </p>
      )
    }
    return (
      <SideConversationView
        key={name}
        organizationId={organizationId}
        viewerId={viewerId}
        teamspaceId={ids.teamspaceId}
        peer={peer}
        teamspaceName={
          inbox.conversations.find((conversation) => conversation.name === name)
            ?.teamspaceName
        }
        draft={sideDrafts[name] ?? emptySideDraft}
        onDraftChange={(draft) => onSideDraftChange(name, draft)}
        onComposingChange={onComposingChange}
        autoFocus={picked}
        backLabel="Back to conversations"
        onBack={back}
      />
    )
  }
  if (view.peerId) {
    const peerId = view.peerId
    const peer = people.peer(peerId)
    if (!peer) {
      return (
        <p className="workspace-agent-empty" role="status">
          Loading the conversation…
        </p>
      )
    }
    return (
      <DirectMessageView
        key={peerId}
        organizationId={organizationId}
        viewerId={viewerId}
        peer={peer}
        layout="panel"
        draft={drafts[peerId] ?? ""}
        onDraftChange={(draft) => onDraftChange(peerId, draft)}
        onComposingChange={onComposingChange}
        autoFocus={picked}
        onBack={() => {
          setPicked(false)
          setFocusTarget({ peerId })
          onViewChange(dmPanelList)
        }}
      />
    )
  }
  if (view.picking) {
    return (
      <PeoplePicker
        people={people}
        onPick={open}
        onBack={() => {
          setFocusTarget("new")
          onViewChange(dmPanelList)
        }}
      />
    )
  }
  return (
    <InboxList
      inbox={inbox}
      people={people}
      viewerId={viewerId}
      focusTarget={focusTarget}
      onFocused={() => setFocusTarget(null)}
      onOpen={open}
      onOpenSide={openSide}
      onNew={() => onViewChange({ peerId: null, picking: true })}
    />
  )
}

function InboxList({
  inbox,
  people,
  viewerId,
  focusTarget,
  onFocused,
  onOpen,
  onOpenSide,
  onNew,
}: {
  inbox: Inbox
  people: DmPeople
  viewerId: string
  focusTarget: FocusTarget
  onFocused: () => void
  onOpen: (peerId: string) => void
  onOpenSide: (name: string) => void
  onNew: () => void
}) {
  const now = useMinuteClock()
  const listRef = useRef<HTMLUListElement>(null)
  const newRef = useRef<HTMLButtonElement>(null)

  // Back from a conversation or the picker, focus returns to where it left.
  useEffect(() => {
    if (!focusTarget) return
    const rows = Array.from(
      listRef.current?.querySelectorAll<HTMLButtonElement>(
        ".workspace-dm-row"
      ) ?? []
    )
    const row =
      focusTarget === "new"
        ? undefined
        : "side" in focusTarget
          ? rows.find((button) => button.dataset.dmSide === focusTarget.side)
          : rows.find((button) => button.dataset.dmPeer === focusTarget.peerId)
    ;(row ?? newRef.current)?.focus()
    onFocused()
  }, [focusTarget, onFocused])

  const { conversations, status } = inbox
  const naming = people.status === "loading"
  const listed = conversations.length > 0 && !naming

  return (
    <>
      <div className="workspace-dm-bar">
        <p className="workspace-agent-thread-title">Direct messages</p>
        <button
          className="workspace-agent-new"
          type="button"
          aria-label="New message"
          title="New message"
          ref={newRef}
          onClick={onNew}
        >
          <SquarePen size={15} strokeWidth={1.7} />
        </button>
      </div>
      {status === "session_expired" ? (
        <p className="workspace-dm-list-note" role="alert">
          Your session has ended. Sign in again to see your messages.{" "}
          <button type="button" onClick={signInAgain}>
            Sign in
          </button>
        </p>
      ) : listed ? (
        <>
          {status === "error" && (
            <p className="workspace-dm-list-note" role="status">
              Can't reach your conversations right now. They may be out of date.
            </p>
          )}
          <ul
            className="workspace-dm-list"
            aria-label="Conversations"
            ref={listRef}
          >
            {conversations.map((conversation) => (
              <li key={conversation.name}>
                <DmRow
                  conversation={conversation}
                  name={people.nameOf(conversation.peerId) ?? "Someone"}
                  viewerId={viewerId}
                  now={now}
                  onOpen={onOpen}
                  onOpenSide={onOpenSide}
                />
              </li>
            ))}
          </ul>
        </>
      ) : status === "live" && !naming ? (
        <div className="workspace-dm-inbox-empty">
          <p>No conversations yet.</p>
          <p>Message anyone in your organization.</p>
          <button
            className="workspace-agent-pill"
            type="button"
            onClick={onNew}
          >
            Start a conversation
          </button>
        </div>
      ) : status === "error" ? (
        <p className="workspace-agent-empty" role="status">
          Can't reach your conversations right now.
        </p>
      ) : (
        <p className="workspace-agent-empty" role="status">
          Loading conversations…
        </p>
      )}
    </>
  )
}

/**
 * One conversation in a list: the person, when it last moved, its preview and
 * its unread count. The DMs tab lists them, and so does Messages beside the
 * open DM, where the open one is the raised tile (`active`).
 */
export function DmRow({
  conversation,
  name,
  viewerId,
  now,
  active = false,
  onOpen,
  onOpenSide,
}: {
  conversation: Conversation
  name: string
  viewerId: string
  now: number
  /** The conversation open beside the list. */
  active?: boolean
  onOpen: (peerId: string) => void
  onOpenSide?: (name: string) => void
}) {
  const online = usePeerOnline(conversation.peerId)
  const previewId = useId()
  const side = conversation.kind === "side"
  const unread = conversation.unread
  const when = conversation.lastMessage?.createdAt ?? conversation.updatedAt
  const label = side
    ? `Open side conversation with ${name} in ${conversation.teamspaceName ?? "a teamspace"}`
    : `Open conversation with ${name}`
  return (
    <button
      className={cn("workspace-dm-row", active && "is-active")}
      type="button"
      data-unread={unread > 0 ? "true" : undefined}
      data-dm-peer={side ? undefined : conversation.peerId}
      data-dm-side={side ? conversation.name : undefined}
      aria-label={unread > 0 ? `${label}, ${unread} unread` : label}
      aria-describedby={previewId}
      aria-current={active ? "page" : undefined}
      onClick={() =>
        side ? onOpenSide?.(conversation.name) : onOpen(conversation.peerId)
      }
    >
      <PersonAvatar id={conversation.peerId} name={name} online={online} />
      <span className="workspace-dm-row-main">
        <span className="workspace-dm-row-top">
          <strong>{name}</strong>
          {side && (
            <span className="workspace-dm-context">
              {conversation.teamspaceName ?? "Teamspace"}
            </span>
          )}
          <time dateTime={when} title={fullTime(when)}>
            {listTime(when, now)}
          </time>
        </span>
        <span className="workspace-dm-row-bottom">
          <span className="workspace-dm-row-preview" id={previewId}>
            {conversationPreview(conversation, viewerId)}
          </span>
          {unread > 0 && <UnreadCount count={unread} />}
        </span>
      </span>
    </button>
  )
}

/** "New message": anyone in the organization, searched by name or email. */
function PeoplePicker({
  people,
  onPick,
  onBack,
}: {
  people: DmPeople
  onPick: (peerId: string) => void
  onBack: () => void
}) {
  const [query, setQuery] = useState("")
  const inputRef = useRef<HTMLInputElement>(null)
  const matches = filterPeople(people.people, query)

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  return (
    <>
      <div className="workspace-dm-bar">
        <button
          className="workspace-agent-new"
          type="button"
          aria-label="Back to conversations"
          onClick={onBack}
        >
          <ArrowLeft size={15} strokeWidth={1.7} />
        </button>
        <h2 className="workspace-dm-bar-title">New message</h2>
      </div>
      <label className="workspace-people-search workspace-dm-picker-search">
        <Search size={16} strokeWidth={1.7} aria-hidden="true" />
        <input
          ref={inputRef}
          type="search"
          aria-label="Search people"
          placeholder="Search people"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Escape") onBack()
          }}
        />
      </label>
      {people.status === "loading" ? (
        <p className="workspace-agent-empty" role="status">
          Loading people…
        </p>
      ) : people.status === "error" ? (
        <p className="workspace-agent-empty" role="alert">
          Couldn't load the people in your organization.{" "}
          <button
            className="workspace-agent-text-button"
            type="button"
            onClick={people.refetch}
          >
            Try again
          </button>
        </p>
      ) : matches.length === 0 ? (
        <p className="workspace-agent-empty">
          {query.trim()
            ? "No people match your search."
            : "No one else is in this organization yet."}
        </p>
      ) : (
        <ul className="workspace-dm-list" aria-label="People">
          {matches.map((person) => (
            <li key={person.id}>
              <PickerRow
                id={person.id}
                name={person.name}
                context={personContext(person)}
                onPick={onPick}
              />
            </li>
          ))}
        </ul>
      )}
    </>
  )
}

/** Someone to start a conversation with, by name, with what you share as context. */
export function PickerRow({
  id,
  name,
  context,
  onPick,
}: {
  id: string
  name: string
  context: string
  onPick: (peerId: string) => void
}) {
  const online = usePeerOnline(id)
  const contextId = useId()
  return (
    <button
      className="workspace-dm-row"
      type="button"
      aria-label={`Message ${name}`}
      aria-describedby={contextId}
      onClick={() => onPick(id)}
    >
      <PersonAvatar id={id} name={name} online={online} />
      <span className="workspace-dm-row-main">
        <span className="workspace-dm-row-top">
          <strong>{name}</strong>
        </span>
        <span className="workspace-dm-row-bottom">
          <span className="workspace-dm-row-preview" id={contextId}>
            {context}
          </span>
        </span>
      </span>
    </button>
  )
}
