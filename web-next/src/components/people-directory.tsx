// Team → People (PeopleNew board, DESIGN.md §3): everyone in the member's
// organization and the agents of theirs the member can reach, as cards or a
// plain list. Message opens your DM with them in the main canvas (the shell's
// DirectMessageView); "Ask <agent>" files a request to their agent
// (ask-agent-dialog.tsx). Only what Oryxa knows is shown: presence is the
// DM-known "Online", and an agent is one a teamspace you share seats for them.
import { useEffect, useRef, useState } from "react"
import { LayoutGrid, List, MessageCircle, Search, UserPlus } from "lucide-react"
import { AskAgentDialog } from "./ask-agent-dialog"
import type { AskAgentTarget } from "./ask-agent-dialog"
import { PersonAvatar } from "./direct-message"
import { roleLabel } from "./direct-message-format"
import { filterPeople, personContext } from "./direct-message-people"
import type { DmPeople, DmPerson, DmPersonAgent } from "./direct-message-people"
import { usePeerOnline } from "./direct-message-presence"
import { agentIdentity } from "./teamspace-format"
import { IdentityAvatar } from "./workspace-avatar"
import { Button } from "@/components/ui/button"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
import { Kbd } from "@/components/ui/kbd"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import type { Role } from "@/lib/auth/client"
import { cn } from "@/lib/utils"

/** The member looking: their own card, with their agent. */
export type PeopleViewer = {
  id: string
  name: string
  role?: Role
  /** Their primary agent, once their agents have loaded. */
  agent?: { id: string; name: string }
}

type View = "cards" | "list"

/** Everyone, or the people who share one teamspace with the member. */
const everyone = "\u0000everyone"

export function PeopleDirectory({
  organizationId,
  organizationName,
  people,
  viewer,
  onMessage,
  onInvite,
  onOpenSettings,
  focusPersonId,
  onFocused,
}: {
  /** Where requests to someone's agent are filed; without it there is no Ask. */
  organizationId?: string
  organizationName?: string
  people: DmPeople
  /** The member's own card leads; without it there is none. */
  viewer?: PeopleViewer
  onMessage: (personId: string) => void
  /** Invitations live in Settings → People; only for who may invite. */
  onInvite?: () => void
  /** The own card's Settings. */
  onOpenSettings?: () => void
  /** Back from their conversation: focus returns to this person. */
  focusPersonId?: string | null
  onFocused?: () => void
}) {
  const [query, setQuery] = useState("")
  const [space, setSpace] = useState(everyone)
  const [view, setView] = useState<View>("cards")
  const [asking, setAsking] = useState<AskAgentTarget | null>(null)
  const listRef = useRef<HTMLUListElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const listed = people.status === "ready" && people.people.length > 0
  const spaces = [
    ...new Set(people.people.flatMap((each) => each.spaces)),
  ].sort((left, right) => left.localeCompare(right))
  const inSpace =
    space === everyone
      ? people.people
      : people.people.filter((person) => person.spaces.includes(space))
  const needle = query.trim().toLocaleLowerCase()
  // By their name or email, or their agent's name.
  const matches = inSpace.filter(
    (person) =>
      filterPeople([person], query).length > 0 ||
      person.agents.some((agent) =>
        agent.name.toLocaleLowerCase().includes(needle)
      )
  )
  const showViewer =
    viewer &&
    space === everyone &&
    (!needle ||
      viewer.name.toLocaleLowerCase().includes(needle) ||
      Boolean(viewer.agent?.name.toLocaleLowerCase().includes(needle)))
  const agentCount =
    new Set(people.people.flatMap((each) => each.agents.map((a) => a.id)))
      .size + (viewer?.agent ? 1 : 0)
  const ask =
    organizationId === undefined
      ? undefined
      : (person: DmPerson, agent: DmPersonAgent) => setAsking({ person, agent })

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

  // "/" finds someone, from anywhere on the page but a field.
  useEffect(() => {
    if (!listed) return
    function onKey(event: KeyboardEvent) {
      if (event.key !== "/" || event.metaKey || event.ctrlKey) return
      const target = event.target
      if (
        target instanceof HTMLElement &&
        (target.isContentEditable ||
          ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName))
      ) {
        return
      }
      event.preventDefault()
      searchRef.current?.focus()
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [listed])

  if (!listed) {
    return (
      <div className="workspace-people-directory">
        <DirectoryState people={people} onInvite={onInvite} />
      </div>
    )
  }

  const total = people.people.length + (viewer ? 1 : 0)

  return (
    <div className="workspace-people-directory" data-view={view}>
      <div className="people-head">
        <h2>
          {organizationName ? `${organizationName} · ` : ""}
          {total} {total === 1 ? "person" : "people"}
          {agentCount > 0 &&
            `, ${agentCount} ${agentCount === 1 ? "agent" : "agents"} you can reach`}
        </h2>
        <div className="people-head-actions">
          {onInvite && (
            <Button
              variant="outline"
              className="people-pill"
              onClick={onInvite}
            >
              <UserPlus aria-hidden="true" />
              Invite
            </Button>
          )}
          <ToggleGroup
            className="people-segments"
            aria-label="Show people as"
            value={[view]}
            onValueChange={(value: string[]) => {
              if (value[0]) setView(value[0] as View)
            }}
          >
            <ToggleGroupItem value="cards" aria-label="Cards">
              <LayoutGrid aria-hidden="true" />
            </ToggleGroupItem>
            <ToggleGroupItem value="list" aria-label="List">
              <List aria-hidden="true" />
            </ToggleGroupItem>
          </ToggleGroup>
        </div>
      </div>
      <div className="people-toolbar">
        <InputGroup className="people-search">
          <InputGroupAddon>
            <Search strokeWidth={1.7} aria-hidden="true" />
          </InputGroupAddon>
          <InputGroupInput
            ref={searchRef}
            type="search"
            aria-label="Search people"
            placeholder="Search people and agents"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Escape") setQuery("")
            }}
          />
          <InputGroupAddon align="inline-end">
            <Kbd aria-hidden="true">/</Kbd>
          </InputGroupAddon>
        </InputGroup>
        {spaces.length > 0 && (
          <ToggleGroup
            className="people-segments people-spaces"
            aria-label="Teamspace"
            value={[space]}
            onValueChange={(value: string[]) => setSpace(value[0] ?? everyone)}
          >
            <ToggleGroupItem value={everyone}>Everyone</ToggleGroupItem>
            {spaces.map((name) => (
              <ToggleGroupItem key={name} value={name}>
                {name}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
        )}
      </div>
      {matches.length > 0 || showViewer ? (
        <ul
          className={cn(
            view === "cards"
              ? "people-cards"
              : "workspace-people-directory-list"
          )}
          aria-label="People"
          ref={listRef}
        >
          {showViewer && (
            <li>
              <ViewerEntry
                viewer={viewer}
                view={view}
                onOpenSettings={onOpenSettings}
              />
            </li>
          )}
          {matches.map((person) => (
            <li key={person.id}>
              {view === "cards" ? (
                <PersonCardEntry
                  person={person}
                  onMessage={onMessage}
                  onAsk={ask}
                />
              ) : (
                <PersonRow person={person} onMessage={onMessage} onAsk={ask} />
              )}
            </li>
          ))}
        </ul>
      ) : (
        <p className="workspace-people-directory-empty">
          No people match your search.
        </p>
      )}
      {asking && organizationId && (
        <AskAgentDialog
          organizationId={organizationId}
          target={asking}
          onClose={() => setAsking(null)}
        />
      )}
    </div>
  )
}

/** Their agent, as a quiet row: its avatar, its name and whose it is. */
function AgentLine({
  agent,
  owner,
}: {
  agent: { id: string; name: string }
  owner: string
}) {
  return (
    <span className="people-agent">
      <IdentityAvatar identity={agentIdentity(agent.id, agent.name)} />
      <span>
        <strong>{agent.name}</strong>
        <small>{owner}</small>
      </span>
    </span>
  )
}

function PersonCardEntry({
  person,
  onMessage,
  onAsk,
}: {
  person: DmPerson
  onMessage: (personId: string) => void
  onAsk?: (person: DmPerson, agent: DmPersonAgent) => void
}) {
  const online = usePeerOnline(person.id)
  const agent = person.agents.at(0)
  const short = person.name.split(/\s+/)[0]
  return (
    <article
      className="people-card"
      aria-label={person.name}
      data-screen-context-kind="person"
      data-screen-context-id={person.id}
      data-screen-context-name={person.name}
    >
      <div className="people-card-who">
        <PersonAvatar id={person.id} name={person.name} online={online} />
        <span>
          <strong>{person.name}</strong>
          <small>{personContext(person)}</small>
          {online && <small className="people-online">Online</small>}
        </span>
      </div>
      {agent && <AgentLine agent={agent} owner={`${short}'s agent`} />}
      <div className="people-card-actions">
        <Button
          variant="outline"
          size="sm"
          className="people-pill"
          data-person-id={person.id}
          aria-label={`Message ${person.name}`}
          onClick={() => onMessage(person.id)}
        >
          <MessageCircle aria-hidden="true" />
          Message
        </Button>
        {agent && onAsk && (
          <Button
            variant="outline"
            size="sm"
            className="people-pill"
            onClick={() => onAsk(person, agent)}
          >
            Ask {agent.name}
          </Button>
        )}
      </div>
    </article>
  )
}

function PersonRow({
  person,
  onMessage,
  onAsk,
}: {
  person: DmPerson
  onMessage: (personId: string) => void
  onAsk?: (person: DmPerson, agent: DmPersonAgent) => void
}) {
  const online = usePeerOnline(person.id)
  const agent = person.agents.at(0)
  return (
    <div
      className="people-row"
      data-screen-context-kind="person"
      data-screen-context-id={person.id}
      data-screen-context-name={person.name}
    >
      <PersonAvatar id={person.id} name={person.name} online={online} />
      <span className="workspace-people-directory-copy">
        <strong>{person.name}</strong>
        <small>{personContext(person)}</small>
      </span>
      {agent && (
        <span className="people-row-agent">
          <IdentityAvatar identity={agentIdentity(agent.id, agent.name)} />
          {agent.name}
        </span>
      )}
      <span className="people-row-actions">
        <Button
          variant="outline"
          size="sm"
          className="people-pill"
          data-person-id={person.id}
          aria-label={`Message ${person.name}`}
          onClick={() => onMessage(person.id)}
        >
          <MessageCircle aria-hidden="true" />
          Message
        </Button>
        {agent && onAsk && (
          <Button
            variant="outline"
            size="sm"
            className="people-pill"
            onClick={() => onAsk(person, agent)}
          >
            Ask {agent.name}
          </Button>
        )}
      </span>
    </div>
  )
}

/** The member's own card: who they are, their agent, and Settings. */
function ViewerEntry({
  viewer,
  view,
  onOpenSettings,
}: {
  viewer: PeopleViewer
  view: View
  onOpenSettings?: () => void
}) {
  const context = viewer.role ? `${roleLabel(viewer.role)} · You` : "You"
  const settings = onOpenSettings && (
    <Button
      variant="outline"
      size="sm"
      className="people-pill"
      onClick={onOpenSettings}
    >
      Settings
    </Button>
  )
  if (view === "list") {
    return (
      <div className="people-row is-viewer">
        <PersonAvatar id={viewer.id} name={viewer.name} />
        <span className="workspace-people-directory-copy">
          <strong>{viewer.name}</strong>
          <small>{context}</small>
        </span>
        {viewer.agent && (
          <span className="people-row-agent">
            <IdentityAvatar
              identity={agentIdentity(viewer.agent.id, viewer.agent.name)}
            />
            {viewer.agent.name}
          </span>
        )}
        {settings}
      </div>
    )
  }
  return (
    <article
      className="people-card is-viewer"
      aria-label={`${viewer.name} (you)`}
    >
      <div className="people-card-who">
        <PersonAvatar id={viewer.id} name={viewer.name} />
        <span>
          <strong>{viewer.name}</strong>
          <small>{context}</small>
        </span>
      </div>
      {viewer.agent && <AgentLine agent={viewer.agent} owner="Your agent" />}
      {settings && <div className="people-card-actions">{settings}</div>}
    </article>
  )
}

/** People before there is anyone to list: why, honestly. */
function DirectoryState({
  people,
  onInvite,
}: {
  people: DmPeople
  onInvite?: () => void
}) {
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
        <div className="workspace-people-directory-empty">
          <p>No one else is in this organization yet.</p>
          {onInvite ? (
            <Button
              variant="outline"
              className="people-pill"
              onClick={onInvite}
            >
              <UserPlus aria-hidden="true" />
              Invite people
            </Button>
          ) : (
            <p>An owner or admin can invite people from Settings.</p>
          )}
        </div>
      )
  }
}
