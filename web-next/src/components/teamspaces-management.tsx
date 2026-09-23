import { useState } from "react"
import type { FormEvent } from "react"
import { ArrowRight, Pause, Play, Plus, Search, UsersRound } from "lucide-react"
import { z } from "zod"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { IdentityAvatar, people, TeamspaceAvatar } from "./workspace-avatar"
import type { AvatarIdentity } from "./workspace-avatar"
import { cn } from "@/lib/utils"

export type WorkspaceRole = "admin" | "member"
export type TeamspaceStatus = "active" | "paused"

export type Teamspace = {
  id: string
  title: string
  description: string
  status: TeamspaceStatus
  owner: AvatarIdentity
  members: AvatarIdentity[]
  updated: string
}

export const initialTeamspaces: Teamspace[] = [
  {
    id: "product-sync",
    title: "Product sync",
    description:
      "A shared space for the product team's handoffs and decisions.",
    status: "active",
    owner: people.avery,
    members: [people.avery, people.jordan, people.sam],
    updated: "12 min ago",
  },
  {
    id: "launch-planning",
    title: "Launch planning",
    description: "Plans, owners, and updates for the next release.",
    status: "active",
    owner: people.jordan,
    members: [people.jordan, people.avery, people.sam, people.noah],
    updated: "Yesterday",
  },
  {
    id: "design-reviews",
    title: "Design reviews",
    description: "A quieter space for feedback on upcoming work.",
    status: "paused",
    owner: people.sam,
    members: [people.sam, people.avery, people.jordan],
    updated: "3 days ago",
  },
]

const newTeamspaceSchema = z.object({
  title: z.string().trim().min(2, "Use at least 2 characters.").max(60),
  description: z.string().trim().max(200),
})

type Filter = "all" | TeamspaceStatus
const filters: { key: Filter; label: string }[] = [
  { key: "all", label: "All" },
  { key: "active", label: "Active" },
  { key: "paused", label: "Paused" },
]

export function TeamspacesManagement({
  role,
  spaces,
  onCreate,
  onStatusChange,
  onOpen,
  selectedId,
  onSelect,
}: {
  role: WorkspaceRole
  spaces: Teamspace[]
  onCreate: (space: Teamspace) => void
  onStatusChange: (id: string, status: TeamspaceStatus) => void
  onOpen: (space: Teamspace) => void
  selectedId: string | null
  onSelect: (id: string | null) => void
}) {
  const [filter, setFilter] = useState<Filter>("all")
  const [query, setQuery] = useState("")
  const [isCreating, setIsCreating] = useState(false)
  const [title, setTitle] = useState("")
  const [description, setDescription] = useState("")
  const [titleError, setTitleError] = useState("")

  const isAdmin = role === "admin"
  const selectedSpace = spaces.find((space) => space.id === selectedId)
  const activeCount = spaces.filter((space) => space.status === "active").length
  const pausedCount = spaces.length - activeCount
  const filteredSpaces = spaces.filter(
    (space) =>
      (filter === "all" || space.status === filter) &&
      `${space.title} ${space.description} ${space.owner.name}`
        .toLocaleLowerCase()
        .includes(query.trim().toLocaleLowerCase())
  )

  function createTeamspace(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!isAdmin) return

    const result = newTeamspaceSchema.safeParse({ title, description })
    if (!result.success) {
      setTitleError(
        result.error.issues.find((issue) => issue.path[0] === "title")
          ?.message ?? "Check this field."
      )
      return
    }
    if (
      spaces.some(
        (space) =>
          space.title.toLocaleLowerCase() ===
          result.data.title.toLocaleLowerCase()
      )
    ) {
      setTitleError("A teamspace with this name already exists.")
      return
    }

    const space: Teamspace = {
      id: crypto.randomUUID(),
      title: result.data.title,
      description: result.data.description,
      status: "active",
      owner: people.avery,
      members: [people.avery],
      updated: "Just now",
    }
    onCreate(space)
    onSelect(space.id)
    setIsCreating(false)
    setTitle("")
    setDescription("")
    setTitleError("")
    setFilter("all")
    setQuery("")
  }

  return (
    <div className="workspace-teamspaces-page">
      {selectedSpace ? (
        <>
          <div className="workspace-teamspace-detail-actions">
            {selectedSpace.status === "active" && (
              <button
                className="workspace-teamspace-primary-button"
                type="button"
                onClick={() => onOpen(selectedSpace)}
              >
                Open teamspace
                <ArrowRight size={16} strokeWidth={1.7} />
              </button>
            )}
            {isAdmin && (
              <button
                className="workspace-teamspace-secondary-button"
                type="button"
                onClick={() =>
                  onStatusChange(
                    selectedSpace.id,
                    selectedSpace.status === "active" ? "paused" : "active"
                  )
                }
              >
                {selectedSpace.status === "active" ? (
                  <Pause size={15} strokeWidth={1.8} />
                ) : (
                  <Play size={15} strokeWidth={1.8} />
                )}
                {selectedSpace.status === "active"
                  ? "Pause teamspace"
                  : "Resume teamspace"}
              </button>
            )}
          </div>
          <div className="workspace-teamspace-detail-grid">
            <section aria-label="Teamspace members">
              <div className="workspace-teamspace-detail-section-head">
                <h3>Members</h3>
                <span>{selectedSpace.members.length}</span>
              </div>
              <ul className="workspace-teamspace-members">
                {selectedSpace.members.map((member) => (
                  <li key={member.id}>
                    <IdentityAvatar identity={member} />
                    <span>{member.name}</span>
                    {member.id === selectedSpace.owner.id && (
                      <small>Owner</small>
                    )}
                  </li>
                ))}
              </ul>
            </section>
            <section aria-label="Teamspace details">
              <div className="workspace-teamspace-detail-section-head">
                <h3>Details</h3>
              </div>
              <div className="workspace-teamspace-detail-facts">
                <div>
                  <span>Status</span>
                  <strong>
                    {selectedSpace.status === "active" ? "Active" : "Paused"}
                  </strong>
                </div>
                <div>
                  <span>Owner</span>
                  <strong>{selectedSpace.owner.name}</strong>
                </div>
                <div>
                  <span>Last activity</span>
                  <strong>{selectedSpace.updated}</strong>
                </div>
              </div>
            </section>
          </div>
        </>
      ) : (
        <>
          {isAdmin && (
            <div className="workspace-teamspaces-intro">
              <button
                className="workspace-teamspace-primary-button"
                type="button"
                aria-expanded={isCreating}
                onClick={() => setIsCreating((current) => !current)}
              >
                <Plus size={17} strokeWidth={1.8} />
                New teamspace
              </button>
            </div>
          )}

          {isAdmin && isCreating && (
            <form
              className="workspace-teamspace-form"
              onSubmit={createTeamspace}
            >
              <div>
                <h3>New teamspace</h3>
                <p>Create a space with yourself as its first member.</p>
              </div>
              <label htmlFor="teamspace-title">Name</label>
              <Input
                id="teamspace-title"
                value={title}
                onChange={(event) => {
                  setTitle(event.target.value)
                  setTitleError("")
                }}
                aria-invalid={Boolean(titleError)}
                aria-describedby={
                  titleError ? "teamspace-title-error" : undefined
                }
                placeholder="e.g. Customer success"
                maxLength={60}
              />
              {titleError && (
                <p
                  className="workspace-teamspace-error"
                  id="teamspace-title-error"
                >
                  {titleError}
                </p>
              )}
              <label htmlFor="teamspace-description">Description</label>
              <Textarea
                id="teamspace-description"
                value={description}
                onChange={(event) => setDescription(event.target.value)}
                placeholder="What will your team do here?"
                maxLength={200}
                rows={2}
              />
              <div className="workspace-teamspace-form-actions">
                <button type="button" onClick={() => setIsCreating(false)}>
                  Cancel
                </button>
                <button type="submit">Create teamspace</button>
              </div>
            </form>
          )}

          <div
            className="workspace-teamspace-metrics"
            aria-label="Teamspace summary"
          >
            <div>
              <span>Active</span>
              <strong>{activeCount}</strong>
              <small>Open for collaboration</small>
            </div>
            <div>
              <span>Paused</span>
              <strong>{pausedCount}</strong>
              <small>Not currently in use</small>
            </div>
            <div>
              <span>Total spaces</span>
              <strong>{spaces.length}</strong>
              <small>Across your team</small>
            </div>
          </div>

          <div className="workspace-teamspace-toolbar">
            <div
              className="workspace-teamspace-filters"
              aria-label="Teamspace status filter"
            >
              {filters.map((item) => (
                <button
                  type="button"
                  key={item.key}
                  className={cn(filter === item.key && "is-active")}
                  aria-pressed={filter === item.key}
                  onClick={() => setFilter(item.key)}
                >
                  {item.label}
                </button>
              ))}
            </div>
            <label className="workspace-teamspace-search">
              <Search size={17} strokeWidth={1.7} aria-hidden="true" />
              <span className="sr-only">Search teamspaces</span>
              <Input
                type="search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search teamspaces"
              />
            </label>
          </div>

          {filteredSpaces.length ? (
            <div className="workspace-teamspace-list">
              {filteredSpaces.map((space) => (
                <button
                  className="workspace-teamspace-row"
                  type="button"
                  key={space.id}
                  aria-label={`Manage ${space.title}`}
                  onClick={() => onSelect(space.id)}
                >
                  <TeamspaceAvatar name={space.title} />
                  <span className="workspace-teamspace-row-main">
                    <strong>{space.title}</strong>
                    <small>{space.description}</small>
                    <span>
                      Owned by {space.owner.name} · {space.updated}
                    </span>
                  </span>
                  <span
                    className="workspace-teamspace-row-members"
                    aria-label={`${space.members.length} members`}
                  >
                    <UsersRound size={15} strokeWidth={1.7} />
                    {space.members.length}
                  </span>
                  <span
                    className={cn("workspace-teamspace-status", space.status)}
                  >
                    {space.status === "active" ? "Active" : "Paused"}
                  </span>
                  <ArrowRight size={17} strokeWidth={1.7} aria-hidden="true" />
                </button>
              ))}
            </div>
          ) : (
            <div className="workspace-teamspace-empty">
              <UsersRound size={23} strokeWidth={1.5} />
              <strong>No teamspaces found</strong>
              <p>Try another search or status filter.</p>
            </div>
          )}
        </>
      )}
    </div>
  )
}
