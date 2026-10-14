import { useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import type { QueryClient } from "@tanstack/react-query"
import {
  ArrowRight,
  Pause,
  Play,
  Plus,
  Search,
  Trash2,
  UsersRound,
} from "lucide-react"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { IdentityAvatar, TeamspaceAvatar } from "./workspace-avatar"
import {
  agentIdentity,
  memberName,
  personIdentity,
  statusLabel,
  timeAgo,
} from "./teamspace-format"
import { ApiError } from "@/lib/api/client"
import { isUnavailableHere } from "@/lib/agents/policy"
import { getMembers } from "@/lib/auth/invitations"
import { listTeams } from "@/lib/settings/client"
import {
  createTeamspace,
  deleteTeamspace,
  updateTeamspace,
} from "@/lib/teamspaces/client"
import type { Teamspace, TeamspaceStatus } from "@/lib/teamspaces/client"
import {
  afterCreateTeamspace,
  afterDeleteTeamspace,
  afterUpdateTeamspace,
  teamspaceEntriesQueryOptions,
  teamspaceKeys,
  teamspacesQueryOptions,
} from "@/lib/teamspaces/queries"
import { cn } from "@/lib/utils"

export type WorkspaceRole = "admin" | "member"

// The service's own limits (agents/primary/internal/teamspaces/service.go).
const maxNameLength = 100
const maxDescriptionLength = 2_000

type Filter = "all" | TeamspaceStatus
const filters: { key: Filter; label: string }[] = [
  { key: "all", label: "All" },
  { key: "active", label: "Active" },
  { key: "disabled", label: "Paused" },
]

/** The member's teams, cached with Settings → Teams so both stay in step. */
export function teamsQueryOptions(organizationId: string) {
  return {
    queryKey: ["settings", "teams", organizationId] as const,
    queryFn: () => listTeams(organizationId),
    retry: false,
  }
}

/** Collapses runs of whitespace as the service does before it checks a name. */
function cleanLine(value: string) {
  return value.trim().split(/\s+/).filter(Boolean).join(" ")
}

function sentence(message: string) {
  const text = message.trim()
  if (!text) return text
  const capitalized = text.charAt(0).toUpperCase() + text.slice(1)
  return /[.!?]$/.test(capitalized) ? capitalized : `${capitalized}.`
}

/** What a refused teamspace change means, in the member's terms. */
function refusal(error: unknown, fallback: string, creating = false): string {
  if (isUnavailableHere(error)) return "Teamspaces aren't available right now."
  if (!(error instanceof ApiError)) return fallback
  switch (error.status) {
    case 400:
      return sentence(error.message)
    case 403:
      return creating
        ? "Only that team's admins can open a teamspace for it."
        : "Only a team admin can change this teamspace."
    case 404:
      return creating
        ? "That team isn't available to you anymore."
        : "This teamspace is no longer available to you."
    case 409:
      return error.code === "teamspace_conflict"
        ? "That team already has a teamspace, or another teamspace uses this name."
        : sentence(error.message)
    case 429:
      return "Too many changes at once. Try again in a moment."
  }
  return fallback
}

/**
 * After a refusal that says the page is out of date (the member's role
 * changed, the teamspace went, another one took the team or the name),
 * re-reads what the page shows so its gates follow.
 */
function refreshAfterRefusal(
  queryClient: QueryClient,
  organizationId: string,
  error: unknown
) {
  if (!(error instanceof ApiError) || ![403, 404, 409].includes(error.status)) {
    return
  }
  void queryClient.invalidateQueries({
    queryKey: teamspaceKeys.list(organizationId),
  })
  void queryClient.invalidateQueries({
    queryKey: teamsQueryOptions(organizationId).queryKey,
  })
}

function replaceInList(
  queryClient: QueryClient,
  organizationId: string,
  space: Teamspace
) {
  queryClient.setQueryData<Teamspace[]>(
    teamspaceKeys.list(organizationId),
    (current) =>
      current &&
      (current.some((item) => item.id === space.id)
        ? current.map((item) => (item.id === space.id ? space : item))
        : [...current, space].sort((left, right) =>
            left.name.localeCompare(right.name)
          ))
  )
}

export function TeamspacesManagement({
  organizationId,
  viewerId,
  selectedId,
  onSelect,
  onOpen,
}: {
  organizationId?: string
  viewerId?: string
  selectedId: string | null
  onSelect: (id: string | null) => void
  onOpen: (id: string) => void
}) {
  const [filter, setFilter] = useState<Filter>("all")
  const [query, setQuery] = useState("")
  const [isCreating, setIsCreating] = useState(false)
  const spaces = useQuery({
    ...teamspacesQueryOptions(organizationId ?? ""),
    enabled: Boolean(organizationId),
  })
  const teams = useQuery({
    ...teamsQueryOptions(organizationId ?? ""),
    enabled: Boolean(organizationId),
  })

  if (!organizationId) {
    return (
      <div className="workspace-teamspaces-page">
        <div className="workspace-teamspace-empty">
          <UsersRound size={23} strokeWidth={1.5} />
          <strong>No workspace to load</strong>
          <p>Teamspaces appear here once you're signed in to a workspace.</p>
        </div>
      </div>
    )
  }

  const list = spaces.data ?? []
  const selectedSpace = selectedId
    ? list.find((space) => space.id === selectedId)
    : undefined
  // Go lets whoever manages a team (its admins, and organization owners and
  // admins) open that team's one teamspace.
  const manageableTeams = (teams.data ?? []).filter((team) => team.can_manage)
  const takenTeams = new Set(list.map((space) => space.team_id))
  const openTeams = manageableTeams.filter((team) => !takenTeams.has(team.id))
  const canCreate = manageableTeams.length > 0

  if (selectedSpace) {
    return (
      <div className="workspace-teamspaces-page">
        <TeamspaceDetail
          key={selectedSpace.id}
          organizationId={organizationId}
          space={selectedSpace}
          viewerId={viewerId}
          onOpen={() => onOpen(selectedSpace.id)}
          onDeleted={() => onSelect(null)}
        />
      </div>
    )
  }

  const activeCount = list.filter((space) => space.status === "active").length
  const needle = query.trim().toLocaleLowerCase()
  const filteredSpaces = list.filter(
    (space) =>
      (filter === "all" || space.status === filter) &&
      `${space.name} ${space.description ?? ""} ${space.team_name}`
        .toLocaleLowerCase()
        .includes(needle)
  )
  const loaded = spaces.data !== undefined

  return (
    <div className="workspace-teamspaces-page">
      {canCreate && (
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

      {canCreate && isCreating && (
        <CreateTeamspaceForm
          organizationId={organizationId}
          teams={openTeams}
          onCancel={() => setIsCreating(false)}
          onCreated={(created) => {
            setIsCreating(false)
            setFilter("all")
            setQuery("")
            onSelect(created.id)
          }}
        />
      )}

      <div
        className="workspace-teamspace-metrics"
        aria-label="Teamspace summary"
      >
        <div>
          <span>Active</span>
          <strong>{loaded ? activeCount : "–"}</strong>
          <small>Open for collaboration</small>
        </div>
        <div>
          <span>Paused</span>
          <strong>{loaded ? list.length - activeCount : "–"}</strong>
          <small>Rooms closed for now</small>
        </div>
        <div>
          <span>Total spaces</span>
          <strong>{loaded ? list.length : "–"}</strong>
          <small>Across your teams</small>
        </div>
      </div>

      <div className="workspace-teamspace-toolbar">
        <div
          className="workspace-teamspace-filters"
          role="group"
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

      {spaces.isPending ? (
        <div className="workspace-teamspace-empty" role="status">
          <UsersRound size={23} strokeWidth={1.5} />
          <strong>Loading teamspaces…</strong>
        </div>
      ) : spaces.isError && !loaded ? (
        <div className="workspace-teamspace-empty" role="alert">
          <UsersRound size={23} strokeWidth={1.5} />
          <strong>
            {isUnavailableHere(spaces.error)
              ? "Teamspaces aren't set up on this server"
              : "Couldn't load teamspaces"}
          </strong>
          {!isUnavailableHere(spaces.error) && (
            <button
              className="workspace-teamspace-secondary-button"
              type="button"
              onClick={() => void spaces.refetch()}
            >
              Try again
            </button>
          )}
        </div>
      ) : filteredSpaces.length ? (
        <div className="workspace-teamspace-list">
          {filteredSpaces.map((space) => (
            <button
              className="workspace-teamspace-row"
              type="button"
              key={space.id}
              data-screen-context-kind="teamspace"
              data-screen-context-id={space.id}
              data-screen-context-name={space.name}
              aria-label={`Manage ${space.name}`}
              onClick={() => onSelect(space.id)}
            >
              <TeamspaceAvatar name={space.name} />
              <span className="workspace-teamspace-row-main">
                <strong>{space.name}</strong>
                <small>{space.description || "No description yet."}</small>
                <span>
                  {space.team_name} team · updated {timeAgo(space.updated_at)}
                </span>
              </span>
              <span
                className="workspace-teamspace-row-members"
                aria-label={`${space.whisperers.length} ${
                  space.whisperers.length === 1 ? "person" : "people"
                }`}
              >
                <UsersRound size={15} strokeWidth={1.7} />
                {space.whisperers.length}
              </span>
              <span
                className={cn(
                  "workspace-teamspace-status",
                  space.status === "active" ? "active" : "paused"
                )}
              >
                {statusLabel(space)}
              </span>
              <ArrowRight size={17} strokeWidth={1.7} aria-hidden="true" />
            </button>
          ))}
        </div>
      ) : list.length ? (
        <div className="workspace-teamspace-empty">
          <UsersRound size={23} strokeWidth={1.5} />
          <strong>No teamspaces found</strong>
          <p>Try another search or status filter.</p>
        </div>
      ) : (
        <div className="workspace-teamspace-empty">
          <UsersRound size={23} strokeWidth={1.5} />
          <strong>No teamspaces yet</strong>
          <p>
            {canCreate
              ? "Open one for a team you manage: its people and their agents talk there."
              : teams.data?.length === 0
                ? "Teamspaces belong to teams. Once you're on a team, its teamspace shows up here."
                : "A team admin can open a teamspace for a team you're on."}
          </p>
        </div>
      )}
    </div>
  )
}

function CreateTeamspaceForm({
  organizationId,
  teams,
  onCancel,
  onCreated,
}: {
  organizationId: string
  teams: Array<{ id: string; name: string }>
  onCancel: () => void
  onCreated: (created: Teamspace) => void
}) {
  const queryClient = useQueryClient()
  const [teamId, setTeamId] = useState(teams.length === 1 ? teams[0].id : "")
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [fieldError, setFieldError] = useState<{
    field: "team" | "name" | "description"
    message: string
  }>()
  const create = useMutation({
    mutationFn: () =>
      createTeamspace(organizationId, {
        team_id: teamId,
        name: cleanLine(name),
        description: description.trim(),
      }),
    onSuccess: async (created) => {
      replaceInList(queryClient, organizationId, created)
      onCreated(created)
      await afterCreateTeamspace(queryClient, organizationId, created)
    },
    onError: (error) => refreshAfterRefusal(queryClient, organizationId, error),
  })
  const chosenTeam = teams.find((team) => team.id === teamId)

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (create.isPending) return
    if (!chosenTeam) {
      setFieldError({ field: "team", message: "Choose a team." })
      return
    }
    const cleanName = cleanLine(name)
    if (!cleanName) {
      setFieldError({ field: "name", message: "Give the teamspace a name." })
      return
    }
    if ([...cleanName].length > maxNameLength) {
      setFieldError({
        field: "name",
        message: `Use at most ${maxNameLength} characters.`,
      })
      return
    }
    if ([...description.trim()].length > maxDescriptionLength) {
      setFieldError({
        field: "description",
        message: `Use at most ${maxDescriptionLength.toLocaleString()} characters.`,
      })
      return
    }
    setFieldError(undefined)
    create.mutate()
  }

  const errorFor = (field: "team" | "name" | "description") =>
    fieldError?.field === field ? fieldError.message : undefined

  return (
    <form className="workspace-teamspace-form" onSubmit={submit} noValidate>
      <div>
        <h3>New teamspace</h3>
        <p>
          A shared room for one team: everyone on it, and their agents, can talk
          and publish work there.
        </p>
      </div>
      {teams.length === 0 ? (
        <p className="workspace-teamspace-form-note">
          Every team you manage already has a teamspace. A team has one.
        </p>
      ) : (
        <>
          <label htmlFor="teamspace-team">Team</label>
          <select
            id="teamspace-team"
            className="workspace-teamspace-select"
            value={teamId}
            onChange={(event) => {
              setTeamId(event.target.value)
              setFieldError(undefined)
            }}
            aria-invalid={Boolean(errorFor("team"))}
            aria-describedby={
              errorFor("team") ? "teamspace-team-error" : undefined
            }
          >
            {teams.length > 1 && <option value="">Choose a team</option>}
            {teams.map((team) => (
              <option value={team.id} key={team.id}>
                {team.name}
              </option>
            ))}
          </select>
          {errorFor("team") && (
            <p className="workspace-teamspace-error" id="teamspace-team-error">
              {errorFor("team")}
            </p>
          )}
          <label htmlFor="teamspace-title">Name</label>
          <Input
            id="teamspace-title"
            value={name}
            onChange={(event) => {
              setName(event.target.value)
              setFieldError(undefined)
            }}
            aria-invalid={Boolean(errorFor("name"))}
            aria-describedby={
              errorFor("name") ? "teamspace-title-error" : undefined
            }
            placeholder={
              chosenTeam ? `e.g. ${chosenTeam.name} room` : "e.g. Launch room"
            }
            maxLength={maxNameLength}
          />
          {errorFor("name") && (
            <p className="workspace-teamspace-error" id="teamspace-title-error">
              {errorFor("name")}
            </p>
          )}
          <label htmlFor="teamspace-description">Description</label>
          <Textarea
            id="teamspace-description"
            value={description}
            onChange={(event) => {
              setDescription(event.target.value)
              setFieldError(undefined)
            }}
            aria-invalid={Boolean(errorFor("description"))}
            aria-describedby={
              errorFor("description")
                ? "teamspace-description-error"
                : undefined
            }
            placeholder="What will the team work on here?"
            maxLength={maxDescriptionLength}
            rows={2}
          />
          {errorFor("description") && (
            <p
              className="workspace-teamspace-error"
              id="teamspace-description-error"
            >
              {errorFor("description")}
            </p>
          )}
        </>
      )}
      {create.isError && (
        <p className="workspace-teamspace-error" role="alert">
          {refusal(
            create.error,
            "Couldn't create the teamspace. Try again.",
            true
          )}
        </p>
      )}
      <div className="workspace-teamspace-form-actions">
        <button type="button" onClick={onCancel}>
          Cancel
        </button>
        <button type="submit" disabled={create.isPending || teams.length === 0}>
          {create.isPending ? "Creating…" : "Create teamspace"}
        </button>
      </div>
    </form>
  )
}

type Confirming = "pause" | "delete" | null

function TeamspaceDetail({
  organizationId,
  space,
  viewerId,
  onOpen,
  onDeleted,
}: {
  organizationId: string
  space: Teamspace
  viewerId?: string
  onOpen: () => void
  onDeleted: () => void
}) {
  const queryClient = useQueryClient()
  const [confirming, setConfirming] = useState<Confirming>(null)
  const entries = useQuery({
    ...teamspaceEntriesQueryOptions(organizationId, space.id),
    // The ledger is for the team's members; anyone else is refused 403.
    enabled: space.is_member,
  })
  const creatorName =
    space.created_by_user_id === viewerId
      ? "You"
      : memberName(space, space.created_by_user_id)
  // The creator may not be on the team (an organization admin can open a
  // teamspace for any team); their name is then in the organization's list.
  const members = useQuery({
    queryKey: ["organizations", organizationId, "members"],
    queryFn: () => getMembers(organizationId),
    enabled: creatorName === undefined,
    retry: false,
  })
  const creatorRecord = members.data?.find(
    (member) => member.user_id === space.created_by_user_id
  )
  const creator =
    creatorName ??
    (creatorRecord
      ? creatorRecord.display_name || creatorRecord.email
      : members.data
        ? "A former member"
        : members.isError
          ? "Unknown"
          : "…")

  const changeStatus = useMutation({
    mutationFn: (status: TeamspaceStatus) =>
      updateTeamspace(organizationId, space.id, { status }),
    onSuccess: async (updated) => {
      setConfirming(null)
      replaceInList(queryClient, organizationId, updated)
      await afterUpdateTeamspace(queryClient, organizationId, updated)
    },
    onError: (error) => refreshAfterRefusal(queryClient, organizationId, error),
  })
  const remove = useMutation({
    mutationFn: () => deleteTeamspace(organizationId, space.id),
    onSuccess: async () => {
      queryClient.setQueryData<Teamspace[]>(
        teamspaceKeys.list(organizationId),
        (current) => current?.filter((item) => item.id !== space.id)
      )
      onDeleted()
      await afterDeleteTeamspace(queryClient, organizationId, space.id)
    },
    onError: (error) => refreshAfterRefusal(queryClient, organizationId, error),
  })
  const busy = changeStatus.isPending || remove.isPending
  const active = space.status === "active"
  const ownAgents = space.whisperers
  const contributions = space.contributions
  const latestEntry = entries.data?.[0]

  return (
    <>
      <div className="workspace-teamspace-detail-actions">
        {active && (
          <button
            className="workspace-teamspace-primary-button"
            type="button"
            onClick={onOpen}
          >
            Open teamspace
            <ArrowRight size={16} strokeWidth={1.7} />
          </button>
        )}
        {space.can_manage && confirming === null && (
          <>
            {active ? (
              <button
                className="workspace-teamspace-secondary-button"
                type="button"
                disabled={busy}
                onClick={() => setConfirming("pause")}
              >
                <Pause size={15} strokeWidth={1.8} />
                Pause teamspace
              </button>
            ) : (
              <button
                className="workspace-teamspace-secondary-button"
                type="button"
                disabled={busy}
                onClick={() => changeStatus.mutate("active")}
              >
                <Play size={15} strokeWidth={1.8} />
                {changeStatus.isPending ? "Resuming…" : "Resume teamspace"}
              </button>
            )}
            <button
              className="workspace-teamspace-secondary-button"
              type="button"
              disabled={busy}
              onClick={() => setConfirming("delete")}
            >
              <Trash2 size={15} strokeWidth={1.8} />
              Delete teamspace
            </button>
          </>
        )}
      </div>

      {confirming === "pause" && (
        <div
          className="workspace-teamspace-confirm"
          role="group"
          aria-label="Confirm pausing"
        >
          <p>
            Pause {space.name}? Its room closes for everyone until it is
            resumed. Its shared work stays.
          </p>
          <button type="button" onClick={() => setConfirming(null)}>
            Cancel
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={() => changeStatus.mutate("disabled")}
          >
            {changeStatus.isPending ? "Pausing…" : "Pause teamspace"}
          </button>
        </div>
      )}
      {confirming === "delete" && (
        <div
          className="workspace-teamspace-confirm"
          role="group"
          aria-label="Confirm deletion"
        >
          <p>
            Delete {space.name}? Its shared work goes with it, for everyone.
            Specialists contributed to it stay with their owners. This can't be
            undone.
          </p>
          <button type="button" onClick={() => setConfirming(null)}>
            Cancel
          </button>
          <button type="button" disabled={busy} onClick={() => remove.mutate()}>
            {remove.isPending ? "Deleting…" : "Delete teamspace"}
          </button>
        </div>
      )}
      {(changeStatus.isError || remove.isError) && (
        <p className="workspace-teamspace-action-error" role="alert">
          {refusal(
            changeStatus.error ?? remove.error,
            remove.isError
              ? "Couldn't delete the teamspace. Try again."
              : "Couldn't change the teamspace. Try again."
          )}
        </p>
      )}
      {!active && (
        <p className="workspace-teamspace-note">
          {space.can_manage
            ? "Paused: its room is closed. Resume it to open the room again."
            : "Paused by a team admin: its room is closed until they resume it."}
        </p>
      )}

      <div className="workspace-teamspace-detail-grid">
        <section aria-label="Teamspace members">
          <div className="workspace-teamspace-detail-section-head">
            <h3>Members</h3>
            <span>{ownAgents.length}</span>
          </div>
          {ownAgents.length ? (
            <ul className="workspace-teamspace-members">
              {ownAgents.map((whisperer) => {
                const name = whisperer.user_display_name || "Teammate"
                return (
                  <li key={whisperer.user_id}>
                    <IdentityAvatar
                      identity={personIdentity(whisperer.user_id, name)}
                    />
                    <span>{name}</span>
                    {(whisperer.user_id === viewerId ||
                      whisperer.user_id === space.created_by_user_id) && (
                      <small>
                        {[
                          whisperer.user_id === viewerId && "You",
                          whisperer.user_id === space.created_by_user_id &&
                            "Created it",
                        ]
                          .filter(Boolean)
                          .join(" · ")}
                      </small>
                    )}
                  </li>
                )
              })}
            </ul>
          ) : (
            <p className="workspace-teamspace-section-empty">
              No one on {space.team_name} has an active agent yet, so the room
              has no seats.
            </p>
          )}
        </section>
        <section aria-label="Teamspace details">
          <div className="workspace-teamspace-detail-section-head">
            <h3>Details</h3>
          </div>
          <div className="workspace-teamspace-detail-facts">
            <div>
              <span>Status</span>
              <strong>{statusLabel(space)}</strong>
            </div>
            <div>
              <span>Team</span>
              <strong>{space.team_name}</strong>
            </div>
            <div>
              <span>Created by</span>
              <strong>{creator}</strong>
            </div>
            <div>
              <span>Your access</span>
              <strong>
                {space.can_manage
                  ? space.is_member
                    ? "Member · manages it"
                    : "Manages it"
                  : "Member"}
              </strong>
            </div>
            <div>
              <span>Last updated</span>
              <strong>{timeAgo(space.updated_at)}</strong>
            </div>
            {space.is_member && (
              <div>
                <span>Latest shared work</span>
                <strong>
                  {latestEntry
                    ? timeAgo(latestEntry.created_at)
                    : entries.isPending
                      ? "…"
                      : entries.isError
                        ? "Unavailable"
                        : "None yet"}
                </strong>
              </div>
            )}
          </div>
        </section>
        <section
          className="workspace-teamspace-agents"
          aria-label="Teamspace agents"
        >
          <div className="workspace-teamspace-detail-section-head">
            <h3>Agents</h3>
            <span>
              {ownAgents.length +
                contributions.filter((item) => item.status === "active").length}
            </span>
          </div>
          <ul className="workspace-teamspace-members">
            {ownAgents.map((whisperer) => (
              <li key={whisperer.agent_id}>
                <IdentityAvatar
                  identity={agentIdentity(
                    whisperer.agent_id,
                    whisperer.agent_name
                  )}
                />
                <span>{whisperer.agent_name}</span>
                <small>
                  {whisperer.user_id === viewerId
                    ? "Your agent"
                    : `${whisperer.user_display_name || "A teammate"}'s agent`}
                </small>
              </li>
            ))}
            {contributions.map((contribution) => (
              <li key={contribution.agent_id}>
                <IdentityAvatar
                  identity={agentIdentity(
                    contribution.agent_id,
                    contribution.name
                  )}
                />
                <span className="workspace-teamspace-agent-name">
                  {contribution.name}
                  <em>{contribution.role}</em>
                </span>
                <small>
                  {contribution.status !== "active"
                    ? "Paused specialist"
                    : contribution.delegation_mode === "automatic"
                      ? "Specialist · works on its own"
                      : "Specialist · asks its owner"}
                </small>
              </li>
            ))}
          </ul>
          {contributions.length === 0 && (
            <p className="workspace-teamspace-section-empty">
              No one has contributed a specialist yet.
            </p>
          )}
        </section>
      </div>
    </>
  )
}
