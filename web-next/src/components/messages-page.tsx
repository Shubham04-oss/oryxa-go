// Messages (DmNew board, DESIGN.md §3 "Direct messages"): an open DM in the
// main canvas sits between the member's DM list and a card about the person.
// The DM itself is the shell's DirectMessageView, unchanged: a person-to-
// person messenger, mine on the right, nobody named. The list is the inbox's
// DMs, with "Find a person" to open anyone; side conversations stay in the
// agent panel's DMs tab, where their teamspace is the context. The card lives
// in the canvas and never takes the agent panel's place, so At a glance stays
// one tab away; a narrow canvas drops the card first, then the list.
import { useId, useRef, useState } from "react"
import type { ReactNode } from "react"
import { Search, SquarePen } from "lucide-react"
import { DmRow, PickerRow, useMinuteClock } from "./direct-message-inbox"
import { filterPeople, personContext } from "./direct-message-people"
import type {
  DmPeer,
  DmPeople,
  DmPerson,
  DmPersonAgent,
} from "./direct-message-people"
import { PersonCard } from "./person-card"
import { Button } from "@/components/ui/button"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
import type { Conversation, Inbox } from "@/lib/dms"

/** The member's DMs, latest first: the conversations Messages lists. */
export function directMessagesOf(inbox: Inbox): Conversation[] {
  return inbox.conversations.filter(
    (conversation) => conversation.kind === "dm"
  )
}

/** How many DM messages wait to be read, side conversations aside. */
export function unreadDirectMessages(inbox: Inbox): number {
  return directMessagesOf(inbox).reduce(
    (total, conversation) => total + conversation.unread,
    0
  )
}

export function MessagesPage({
  organizationId,
  viewerId,
  inbox,
  people,
  peer,
  onOpen,
  onAskAgent,
  children,
}: {
  organizationId: string
  viewerId: string
  inbox: Inbox
  people: DmPeople
  /** The person whose DM is open. */
  peer: DmPeer
  /** Opens the DM with someone, in place of this one. */
  onOpen: (peerId: string) => void
  /** Ask one of the person's agents; without it the card offers no Ask. */
  onAskAgent?: (person: DmPerson, agent: DmPersonAgent) => void
  /** The open DM. */
  children: ReactNode
}) {
  return (
    <div className="messages-page">
      <div className="messages-page-grid">
        <MessagesList
          inbox={inbox}
          people={people}
          viewerId={viewerId}
          activePeerId={peer.id}
          onOpen={onOpen}
        />
        <div className="messages-page-conversation">{children}</div>
        <aside
          className="messages-page-person"
          aria-label={`About ${peer.name}`}
        >
          <PersonCard
            organizationId={organizationId}
            viewerId={viewerId}
            peer={peer}
            person={people.find(peer.id)}
            onAskAgent={onAskAgent}
          />
        </aside>
      </div>
    </div>
  )
}

/**
 * The DM list beside the open DM: each conversation's person, preview and
 * unread count, the open one raised. "Find a person" narrows it and lists
 * anyone else in the organization who matches, to start a conversation.
 */
export function MessagesList({
  inbox,
  people,
  viewerId,
  activePeerId,
  onOpen,
}: {
  inbox: Inbox
  people: DmPeople
  viewerId: string
  activePeerId: string | null
  onOpen: (peerId: string) => void
}) {
  const now = useMinuteClock()
  const [query, setQuery] = useState("")
  const searchRef = useRef<HTMLInputElement>(null)
  const headingId = useId()
  const conversations = directMessagesOf(inbox)
  const unread = unreadDirectMessages(inbox)
  const naming = people.status === "loading"
  const needle = query.trim()
  const shown = needle
    ? conversations.filter((conversation) => {
        const person = people.find(conversation.peerId)
        return person
          ? filterPeople([person], needle).length > 0
          : (people.nameOf(conversation.peerId) ?? "")
              .toLocaleLowerCase()
              .includes(needle.toLocaleLowerCase())
      })
    : conversations
  const talkedWith = new Set(conversations.map((each) => each.peerId))
  const others = needle
    ? filterPeople(people.people, needle).filter(
        (person) => !talkedWith.has(person.id)
      )
    : []

  return (
    <section className="messages-list" aria-labelledby={headingId}>
      <div className="messages-list-head">
        <h2 id={headingId}>
          Messages
          {unread > 0 && (
            <span className="messages-list-unread">
              {" · "}
              {unread} unread
            </span>
          )}
        </h2>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="New message"
          title="New message"
          onClick={() => searchRef.current?.focus()}
        >
          <SquarePen strokeWidth={1.7} />
        </Button>
      </div>
      <InputGroup className="messages-list-search">
        <InputGroupAddon>
          <Search strokeWidth={1.7} aria-hidden="true" />
        </InputGroupAddon>
        <InputGroupInput
          ref={searchRef}
          type="search"
          aria-label="Find a person"
          placeholder="Find a person"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Escape") setQuery("")
          }}
        />
      </InputGroup>
      <div className="messages-list-scroll">
        {inbox.status === "session_expired" ? (
          <p className="messages-list-note" role="alert">
            Sign in again to see your messages.
          </p>
        ) : naming ||
          (inbox.status === "connecting" && !conversations.length) ? (
          <p className="messages-list-note" role="status">
            Loading conversations…
          </p>
        ) : (
          <>
            {inbox.status === "error" && (
              <p className="messages-list-note" role="status">
                Can't reach your conversations right now.
              </p>
            )}
            {shown.length > 0 ? (
              <ul className="workspace-dm-list" aria-label="Direct messages">
                {shown.map((conversation) => (
                  <li key={conversation.name}>
                    <DmRow
                      conversation={conversation}
                      name={people.nameOf(conversation.peerId) ?? "Someone"}
                      viewerId={viewerId}
                      now={now}
                      active={conversation.peerId === activePeerId}
                      onOpen={onOpen}
                    />
                  </li>
                ))}
              </ul>
            ) : (
              !needle &&
              inbox.status !== "error" && (
                <p className="messages-list-note">
                  Your other conversations will show here.
                </p>
              )
            )}
            {others.length > 0 && (
              <>
                <h3 className="messages-list-group">Start a conversation</h3>
                <ul className="workspace-dm-list" aria-label="People">
                  {others.map((person) => (
                    <li key={person.id}>
                      <PickerRow
                        id={person.id}
                        name={person.name}
                        context={personContext(person)}
                        onPick={(id) => {
                          setQuery("")
                          onOpen(id)
                        }}
                      />
                    </li>
                  ))}
                </ul>
              </>
            )}
            {needle && shown.length === 0 && others.length === 0 && (
              <p className="messages-list-note">No one matches your search.</p>
            )}
          </>
        )}
      </div>
    </section>
  )
}
