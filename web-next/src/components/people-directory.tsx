import { useState } from "react"
import type { FormEvent } from "react"
import { ArrowRight, Search, Send } from "lucide-react"
import type { Teamspace } from "./teamspaces-management"
import { IdentityAvatar } from "./workspace-avatar"
import type { AvatarIdentity } from "./workspace-avatar"

export function PeopleDirectory({
  spaces,
  currentIdentity,
  preview,
  selectedId,
  messages,
  onOpenConversation,
  onSendMessage,
}: {
  spaces: Teamspace[]
  currentIdentity: AvatarIdentity
  preview: boolean
  selectedId: string | null
  messages: Partial<Record<string, string[]>>
  onOpenConversation: (personId: string) => void
  onSendMessage: (personId: string, text: string) => void
}) {
  const [query, setQuery] = useState("")
  const [draft, setDraft] = useState("")
  const sharedSpaces = getSharedSpaces(spaces, currentIdentity)
  const people = getSharedPeople(spaces, currentIdentity)
  const filteredPeople = people.filter((person) =>
    person.name.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase())
  )
  const selectedPerson = people.find((person) => person.id === selectedId)
  const selectedMessages = selectedPerson
    ? messages[selectedPerson.id] || []
    : []

  function sendMessage(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const text = draft.trim()
    if (!preview || !selectedPerson || !text) return
    onSendMessage(selectedPerson.id, text)
    setDraft("")
  }

  return (
    <div className="workspace-people-directory">
      {selectedPerson ? (
        <div className="workspace-dm">
          <div
            className="workspace-dm-messages"
            aria-label={`Conversation with ${selectedPerson.name}`}
          >
            {selectedMessages.length ? (
              selectedMessages.map((message, index) => (
                <div
                  className="workspace-dm-message"
                  key={`${selectedPerson.id}-${index}`}
                >
                  <IdentityAvatar identity={currentIdentity} />
                  <span>
                    <strong>You</strong>
                    <p>{message}</p>
                  </span>
                </div>
              ))
            ) : (
              <p className="workspace-dm-empty">
                Start a conversation with {selectedPerson.name}.
              </p>
            )}
          </div>
          <form className="workspace-dm-composer" onSubmit={sendMessage}>
            <input
              aria-label={`Message ${selectedPerson.name}`}
              placeholder={
                preview
                  ? "Write a preview message…"
                  : "Messaging is coming soon"
              }
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              disabled={!preview}
            />
            <button
              type="submit"
              aria-label="Send message"
              disabled={!preview || !draft.trim()}
            >
              <Send size={17} strokeWidth={1.8} />
            </button>
          </form>
          {preview && (
            <p className="workspace-dm-footnote">
              Preview messages stay in this page.
            </p>
          )}
        </div>
      ) : (
        <>
          <label className="workspace-people-search">
            <Search size={17} strokeWidth={1.7} />
            <input
              type="search"
              aria-label="Search people"
              placeholder="Search people"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </label>
          {filteredPeople.length ? (
            <ul className="workspace-people-directory-list">
              {filteredPeople.map((person) => {
                const commonSpaces = sharedSpaces.filter((space) =>
                  space.members.some((member) => member.id === person.id)
                )
                return (
                  <li key={person.id}>
                    <button
                      type="button"
                      onClick={() => onOpenConversation(person.id)}
                      aria-label={`Message ${person.name}`}
                    >
                      <IdentityAvatar identity={person} />
                      <span>
                        <strong>{person.name}</strong>
                        <small>
                          {commonSpaces.map((space) => space.title).join(" · ")}
                        </small>
                      </span>
                      <ArrowRight size={17} strokeWidth={1.7} />
                    </button>
                  </li>
                )
              })}
            </ul>
          ) : (
            <p className="workspace-people-directory-empty">
              {query
                ? "No people match your search."
                : "People you share a teamspace with will appear here."}
            </p>
          )}
        </>
      )}
    </div>
  )
}

function getSharedSpaces(spaces: Teamspace[], currentIdentity: AvatarIdentity) {
  return spaces.filter((space) =>
    space.members.some((member) => member.id === currentIdentity.id)
  )
}

export function getSharedPeople(
  spaces: Teamspace[],
  currentIdentity: AvatarIdentity
): AvatarIdentity[] {
  return Array.from(
    new Map(
      getSharedSpaces(spaces, currentIdentity).flatMap((space) =>
        space.members
          .filter((member) => member.id !== currentIdentity.id)
          .map((member) => [member.id, member] as const)
      )
    ).values()
  ).sort((first, second) => first.name.localeCompare(second.name))
}
