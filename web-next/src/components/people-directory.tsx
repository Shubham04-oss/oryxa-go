// Team → People (DESIGN.md §3): everyone in the member's organization, to
// message. Choosing someone opens your DM with them in the main canvas (the
// shell's DirectMessageView); this is the directory in front of it.
import { useEffect, useId, useRef, useState } from "react"
import { MessageCircle, Search } from "lucide-react"
import { PersonAvatar } from "./direct-message"
import { filterPeople, personContext } from "./direct-message-people"
import type { DmPeople, DmPerson } from "./direct-message-people"
import { usePeerOnline } from "./direct-message-presence"

export function PeopleDirectory({
  people,
  onMessage,
  focusPersonId,
  onFocused,
}: {
  people: DmPeople
  onMessage: (personId: string) => void
  /** Back from their conversation: focus returns to this person. */
  focusPersonId?: string | null
  onFocused?: () => void
}) {
  const [query, setQuery] = useState("")
  const listRef = useRef<HTMLUListElement>(null)
  const matches = filterPeople(people.people, query)
  const listed = people.status === "ready" && people.people.length > 0

  useEffect(() => {
    if (!focusPersonId || !listed) return
    Array.from(
      listRef.current?.querySelectorAll<HTMLButtonElement>(
        "[data-person-id]"
      ) ?? []
    )
      .find((button) => button.dataset.personId === focusPersonId)
      ?.focus()
    onFocused?.()
  }, [focusPersonId, listed, onFocused])

  if (!listed) {
    return (
      <div className="workspace-people-directory">
        <DirectoryState people={people} />
      </div>
    )
  }

  return (
    <div className="workspace-people-directory">
      <label className="workspace-people-search">
        <Search size={17} strokeWidth={1.7} aria-hidden="true" />
        <input
          type="search"
          aria-label="Search people"
          placeholder="Search people"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
      </label>
      {matches.length > 0 ? (
        <ul
          className="workspace-people-directory-list"
          aria-label="People"
          ref={listRef}
        >
          {matches.map((person) => (
            <li key={person.id}>
              <PersonRow person={person} onMessage={onMessage} />
            </li>
          ))}
        </ul>
      ) : (
        <p className="workspace-people-directory-empty">
          No people match your search.
        </p>
      )}
    </div>
  )
}

function PersonRow({
  person,
  onMessage,
}: {
  person: DmPerson
  onMessage: (personId: string) => void
}) {
  const online = usePeerOnline(person.id)
  const contextId = useId()
  return (
    <button
      type="button"
      data-person-id={person.id}
      aria-label={`Message ${person.name}`}
      aria-describedby={contextId}
      onClick={() => onMessage(person.id)}
    >
      <PersonAvatar id={person.id} name={person.name} online={online} />
      <span className="workspace-people-directory-copy">
        <strong>{person.name}</strong>
        <small id={contextId}>{personContext(person)}</small>
      </span>
      <span className="workspace-people-message" aria-hidden="true">
        <MessageCircle size={14} strokeWidth={1.8} />
        <span>Message</span>
      </span>
    </button>
  )
}

/** People before there is anyone to list: why, honestly. */
function DirectoryState({ people }: { people: DmPeople }) {
  switch (people.status) {
    case "none":
      return (
        <p className="workspace-people-directory-empty">
          Sign in to a workspace to message its people.
        </p>
      )
    case "loading":
      return (
        <p className="workspace-people-directory-empty" role="status">
          Loading people…
        </p>
      )
    case "error":
      return (
        <div className="workspace-people-directory-empty" role="alert">
          <p>Couldn't load the people in your organization.</p>
          <button
            className="workspace-teamspace-secondary-button"
            type="button"
            onClick={people.refetch}
          >
            Try again
          </button>
        </div>
      )
    case "ready":
      return (
        <p className="workspace-people-directory-empty">
          No one else is in this organization yet. Invite people from Settings.
        </p>
      )
  }
}
