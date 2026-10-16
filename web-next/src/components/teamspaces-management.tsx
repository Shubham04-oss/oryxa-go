import { useEffect, useRef, useState } from "react"
import type { FormEvent } from "react"
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import type { QueryClient } from "@tanstack/react-query"
import {
  ArrowRight,
  Ellipsis,
  Pause,
  Play,
  Plus,
  Search,
  Settings2,
  Trash2,
  UsersRound,
} from "lucide-react"
import { AvatarGroup, AvatarGroupCount } from "@/components/ui/avatar"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Kbd } from "@/components/ui/kbd"
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select"
import { Textarea } from "@/components/ui/textarea"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { IdentityAvatar, TeamspaceAvatar } from "./workspace-avatar"
import {
  agentIdentity,
  lastMessageLine,
  memberName,
  personIdentity,
  statusLabel,
  timeAgo,
  unreadCount,
  unreadLabel,
  unreadWords,
} from "./teamspace-format"
import { ApiError } from "@/lib/api/client"
import { isUnavailableHere } from "@/lib/agents/policy"
import { getMembers } from "@/lib/auth/invitations"
import { callKey, readCall } from "@/lib/call/client"
import type { CallView } from "@/lib/call/client"
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

// The list reads each open teamspace's parley without the room's live log,
// so it asks again on a slower beat than an open room does.
const parleyRefreshMs = 60_000

// How many teammates' faces a card shows before "+N".
const maxFaces = 4

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

function people(count: number) {
  return `${count} ${count === 1 ? "person" : "people"}`
}

/** How long a parley has run, as the card's chip says it. */
function parleyLength(startedAt: string, now = Date.now()) {
  const minutes = Math.max(
    1,
    Math.floor((now - new Date(startedAt).getTime()) / 60_000)
  )
  if (minutes < 60) return `${minutes} min`
  return `${Math.floor(minutes / 60)} hr`
}

/** The subtitle under the title: how many are open, and where a parley is. */
function summaryLine(activeCount: number, liveIn: Teamspace[]) {
  const active = `${activeCount} active`
  if (liveIn.length === 0) return `${active}.`
  if (liveIn.length === 1) {
    return `${active}, and a parley is live in ${liveIn[0].name}.`
  }
  return `${active}, and ${liveIn.length} parleys are live.`
}

/** A typed character belongs to the field (or editor) it was typed in. */
function isTypingTarget(target: EventTarget | null) {
  if (!(target instanceof HTMLElement)) return false
  return (
    target.isContentEditable ||
    ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName)
  )
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

/** What a card's More menu asked the detail to start with. */
type Intent = { id: string; confirming: Confirming }

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
  const queryClient = useQueryClient()
  const [filter, setFilter] = useState<Filter>("all")
  const [query, setQuery] = useState("")
  const [isCreating, setIsCreating] = useState(false)
  const [intent, setIntent] = useState<Intent | null>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const spaces = useQuery({
    ...teamspacesQueryOptions(organizationId ?? ""),
    enabled: Boolean(organizationId),
  })
  const teams = useQuery({
    ...teamsQueryOptions(organizationId ?? ""),
    enabled: Boolean(organizationId),
  })
  const list = spaces.data ?? []
  const selectedSpace = selectedId
    ? list.find((space) => space.id === selectedId)
    : undefined

  // A parley only runs in an open teamspace, and the edge answers only the
  // team's members. One light read per such card, under the same key the
  // room's own call view uses, so opening the room starts from it.
  const listening = organizationId && !selectedSpace ? list : []
  const parleyable = listening.filter(
    (space) => space.status === "active" && space.is_member
  )
  const parleys = useQueries({
    queries: parleyable.map((space) => {
      const room = { kind: "teamspace" as const, id: space.id }
      return {
        queryKey: callKey(organizationId ?? "", room),
        queryFn: ({ signal }: { signal: AbortSignal }) =>
          readCall(organizationId ?? "", room, signal),
        refetchInterval: parleyRefreshMs,
        retry: false,
      }
    }),
  })
  const liveCalls = new Map<string, CallView>()
  parleyable.forEach((space, index) => {
    const call = parleys[index].data
    if (call) liveCalls.set(space.id, call)
  })

  const resume = useMutation({
    mutationFn: (space: Teamspace) =>
      updateTeamspace(organizationId ?? "", space.id, { status: "active" }),
    onSuccess: async (updated) => {
      if (!organizationId) return
      replaceInList(queryClient, organizationId, updated)
      await afterUpdateTeamspace(queryClient, organizationId, updated)
    },
    onError: (error) => {
      if (organizationId)
        refreshAfterRefusal(queryClient, organizationId, error)
    },
  })

  // "/" jumps to the search, unless the member is typing somewhere.
  const listShown = Boolean(organizationId) && !selectedSpace
  useEffect(() => {
    if (!listShown) return
    function onKey(event: KeyboardEvent) {
      if (
        event.key !== "/" ||
        event.metaKey ||
        event.ctrlKey ||
        event.altKey ||
        event.defaultPrevented ||
        isTypingTarget(event.target)
      ) {
        return
      }
      event.preventDefault()
      searchRef.current?.focus()
    }
    document.addEventListener("keydown", onKey)
    return () => document.removeEventListener("keydown", onKey)
  }, [listShown])

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
          initialConfirming={
            intent?.id === selectedSpace.id ? intent.confirming : null
          }
          onOpen={() => onOpen(selectedSpace.id)}
          onDeleted={() => {
            setIntent(null)
            onSelect(null)
          }}
        />
      </div>
    )
  }

  function manage(space: Teamspace, confirming: Confirming = null) {
    setIntent({ id: space.id, confirming })
    onSelect(space.id)
  }

  const activeCount = list.filter((space) => space.status === "active").length
  const counts: Record<Filter, number> = {
    all: list.length,
    active: activeCount,
    disabled: list.length - activeCount,
  }
  const liveIn = list.filter((space) => liveCalls.has(space.id))
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
      <div className="workspace-teamspaces-scene" aria-hidden="true" />
      {/* The shell's header already names the page (DESIGN.md §2), so the
          canvas opens on a compact row: the summary and New teamspace. */}
      <div className="workspace-teamspaces-hello">
        {(loaded || spaces.isPending) && (
          <p aria-label="Teamspace summary">
            {loaded ? summaryLine(activeCount, liveIn) : "Loading…"}
          </p>
        )}
        {canCreate && (
          <Button
            type="button"
            aria-haspopup="dialog"
            aria-expanded={isCreating}
            onClick={() => setIsCreating(true)}
          >
            <Plus data-icon="inline-start" />
            New teamspace
          </Button>
        )}
      </div>

      {canCreate && isCreating && (
        <CreateTeamspaceDialog
          organizationId={organizationId}
          teams={openTeams}
          onClose={() => setIsCreating(false)}
          onCreated={(created) => {
            setIsCreating(false)
            setFilter("all")
            setQuery("")
            setIntent(null)
            onSelect(created.id)
          }}
        />
      )}

      <div className="workspace-teamspace-toolbar">
        <label className="workspace-teamspace-search">
          <Search size={16} strokeWidth={1.7} aria-hidden="true" />
          <span className="sr-only">Search teamspaces</span>
          <Input
            ref={searchRef}
            type="search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Escape" && query) {
                event.preventDefault()
                setQuery("")
              }
            }}
            placeholder="Search teamspaces"
            aria-keyshortcuts="/"
          />
          <Kbd aria-hidden="true">/</Kbd>
        </label>
        <ToggleGroup
          className="workspace-teamspace-filters"
          aria-label="Teamspace status filter"
          value={[filter]}
          onValueChange={(value) => {
            // A segmented control: pressing the chosen filter keeps it.
            const next = value[0] as Filter | undefined
            if (next) setFilter(next)
          }}
        >
          {filters.map((item) => (
            <ToggleGroupItem
              key={item.key}
              value={item.key}
              className={cn(filter === item.key && "is-active")}
            >
              {item.label}
              {loaded && " "}
              {loaded && <span>{counts[item.key]}</span>}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      </div>

      {resume.isError && (
        <p className="workspace-teamspace-action-error" role="alert">
          {refusal(resume.error, "Couldn't resume the teamspace. Try again.")}
        </p>
      )}

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
            <Button
              variant="outline"
              type="button"
              onClick={() => void spaces.refetch()}
            >
              Try again
            </Button>
          )}
        </div>
      ) : filteredSpaces.length ? (
        <div className="workspace-teamspace-list">
          {filteredSpaces.map((space) => (
            <TeamspaceCard
              key={space.id}
              space={space}
              call={liveCalls.get(space.id)}
              viewerId={viewerId}
              resuming={resume.isPending && resume.variables.id === space.id}
              onOpen={() => onOpen(space.id)}
              onManage={() => manage(space)}
              onPause={() => manage(space, "pause")}
              onResume={() => resume.mutate(space)}
              onDelete={() => manage(space, "delete")}
            />
          ))}
          {canCreate && filter === "all" && !needle && (
            <Card className="workspace-teamspace-new">
              <strong>Open a new teamspace</strong>
              <span>
                A room, its files and its parleys, for people and their agents.
              </span>
              <Button
                type="button"
                aria-haspopup="dialog"
                onClick={() => setIsCreating(true)}
              >
                New teamspace
              </Button>
            </Card>
          )}
        </div>
      ) : list.length ? (
        <div className="workspace-teamspace-empty">
          <UsersRound size={23} strokeWidth={1.5} />
          <strong>No teamspaces found</strong>
          <p>Try another search or status filter.</p>
        </div>
      ) : (
        <div className="workspace-teamspace-empty workspace-empty-scene">
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

/**
 * One teamspace in the grid. The card opens the teamspace; managing it
 * (details, pause or resume, delete) is in its More menu.
 */
function TeamspaceCard({
  space,
  call,
  viewerId,
  resuming,
  onOpen,
  onManage,
  onPause,
  onResume,
  onDelete,
}: {
  space: Teamspace
  call?: CallView
  viewerId?: string
  resuming: boolean
  onOpen: () => void
  onManage: () => void
  onPause: () => void
  onResume: () => void
  onDelete: () => void
}) {
  const active = space.status === "active"
  // What the member hasn't read in the room, and its newest message, when
  // the list carries them (#389).
  const unread = unreadCount(space)
  const last = space.last_message
  return (
    <Card
      role="article"
      className="workspace-teamspace-card"
      data-status={active ? "active" : "paused"}
      data-screen-context-kind="teamspace"
      data-screen-context-id={space.id}
      data-screen-context-name={space.name}
      aria-label={space.name}
    >
      <div className="workspace-teamspace-card-top">
        <TeamspaceAvatar name={space.name} />
        <span className="workspace-teamspace-card-title">
          <Button
            className="workspace-teamspace-card-open"
            variant="ghost"
            type="button"
            aria-label={`Open ${space.name}`}
            onClick={onOpen}
          >
            <strong>{space.name}</strong>
          </Button>
          <small>
            {space.team_name} team ·{" "}
            {active ? people(space.whisperers.length) : "paused"}
          </small>
        </span>
        {unread > 0 && (
          <Badge
            className="workspace-teamspace-unread"
            aria-label={unreadWords(unread)}
          >
            {unreadLabel(unread)}
          </Badge>
        )}
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                className="workspace-teamspace-card-more"
                variant="ghost"
                size="icon-sm"
              />
            }
            aria-label={`More for ${space.name}`}
          >
            <Ellipsis size={16} strokeWidth={1.8} aria-hidden="true" />
          </DropdownMenuTrigger>
          <DropdownMenuContent className="workspace-teamspace-menu" align="end">
            <DropdownMenuItem onClick={onOpen}>
              <ArrowRight aria-hidden="true" />
              Open teamspace
            </DropdownMenuItem>
            <DropdownMenuItem onClick={onManage}>
              <Settings2 aria-hidden="true" />
              Manage details
            </DropdownMenuItem>
            {space.can_manage && (
              <>
                <DropdownMenuSeparator />
                {active ? (
                  <DropdownMenuItem onClick={onPause}>
                    <Pause aria-hidden="true" />
                    Pause teamspace
                  </DropdownMenuItem>
                ) : (
                  <DropdownMenuItem disabled={resuming} onClick={onResume}>
                    <Play aria-hidden="true" />
                    {resuming ? "Resuming…" : "Resume teamspace"}
                  </DropdownMenuItem>
                )}
                <DropdownMenuItem variant="destructive" onClick={onDelete}>
                  <Trash2 aria-hidden="true" />
                  Delete teamspace
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <p className="workspace-teamspace-card-purpose">
        {active
          ? space.description || "No description yet."
          : "Paused. Its room is closed until a team admin resumes it."}
      </p>
      {last && (
        <p className="workspace-teamspace-card-last">
          <span>{lastMessageLine(last, viewerId)}</span>
          <time dateTime={last.created_at}>{timeAgo(last.created_at)}</time>
        </p>
      )}
      {call && (
        <div className="workspace-teamspace-card-live">
          <Badge variant="secondary">
            <span className="workspace-teamspace-live-dot" aria-hidden="true" />
            Parley · {parleyLength(call.startedAt)}
          </Badge>
        </div>
      )}
      <div className="workspace-teamspace-card-foot">
        <span>{active ? "" : "Paused"}</span>
        <TeamspaceFaces space={space} />
      </div>
    </Card>
  )
}

/** The teammates with a seat in the room, as a small stack of faces. */
export function TeamspaceFaces({ space }: { space: Teamspace }) {
  const shown = space.whisperers.slice(0, maxFaces)
  const more = space.whisperers.length - shown.length
  if (shown.length === 0) return null
  return (
    <AvatarGroup
      className="workspace-teamspace-faces"
      role="img"
      aria-label={shown
        .map((whisperer) => whisperer.user_display_name || "Teammate")
        .concat(more > 0 ? [`${more} more`] : [])
        .join(", ")}
    >
      {shown.map((whisperer) => (
        <IdentityAvatar
          key={whisperer.user_id}
          identity={personIdentity(
            whisperer.user_id,
            whisperer.user_display_name || "Teammate"
          )}
        />
      ))}
      {more > 0 && <AvatarGroupCount>+{more}</AvatarGroupCount>}
    </AvatarGroup>
  )
}

/** Creating a teamspace, as a pop-up over the list. */
function CreateTeamspaceDialog({
  organizationId,
  teams,
  onClose,
  onCreated,
}: {
  organizationId: string
  teams: Array<{ id: string; name: string }>
  onClose: () => void
  onCreated: (created: Teamspace) => void
}) {
  const [pending, setPending] = useState(false)
  return (
    <Dialog
      open
      disablePointerDismissal={pending}
      onOpenChange={(open) => {
        if (!open && !pending) onClose()
      }}
    >
      <DialogContent
        className="workspace-teamspace-sheet"
        showCloseButton={!pending}
      >
        <div className="workspace-teamspace-sheet-scene" aria-hidden="true" />
        <div className="workspace-teamspace-sheet-head">
          <DialogTitle>New teamspace</DialogTitle>
          <DialogDescription>
            A shared room for one team: everyone on it, and their agents, can
            talk and publish work there.
          </DialogDescription>
        </div>
        <CreateTeamspaceForm
          organizationId={organizationId}
          teams={teams}
          onCancel={onClose}
          onCreated={onCreated}
          onPendingChange={setPending}
        />
      </DialogContent>
    </Dialog>
  )
}

function CreateTeamspaceForm({
  organizationId,
  teams,
  onCancel,
  onCreated,
  onPendingChange,
}: {
  organizationId: string
  teams: Array<{ id: string; name: string }>
  onCancel: () => void
  onCreated: (created: Teamspace) => void
  /** Whether a create is on its way, so the pop-up holds still meanwhile. */
  onPendingChange: (pending: boolean) => void
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

  useEffect(() => {
    onPendingChange(create.isPending)
  }, [create.isPending, onPendingChange])

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
      {teams.length === 0 ? (
        <p className="workspace-teamspace-form-note">
          Every team you manage already has a teamspace. A team has one.
        </p>
      ) : (
        <>
          <FieldLabel htmlFor="teamspace-team">Team</FieldLabel>
          <NativeSelect
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
            {teams.length > 1 && (
              <NativeSelectOption value="">Choose a team</NativeSelectOption>
            )}
            {teams.map((team) => (
              <NativeSelectOption value={team.id} key={team.id}>
                {team.name}
              </NativeSelectOption>
            ))}
          </NativeSelect>
          {errorFor("team") && (
            <p className="workspace-teamspace-error" id="teamspace-team-error">
              {errorFor("team")}
            </p>
          )}
          <FieldLabel htmlFor="teamspace-title">Name</FieldLabel>
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
          <FieldLabel htmlFor="teamspace-description">Description</FieldLabel>
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
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={create.isPending || teams.length === 0}>
          {create.isPending ? "Creating…" : "Create teamspace"}
        </Button>
      </div>
    </form>
  )
}

type Confirming = "pause" | "delete" | null

function TeamspaceDetail({
  organizationId,
  space,
  viewerId,
  initialConfirming = null,
  onOpen,
  onDeleted,
}: {
  organizationId: string
  space: Teamspace
  viewerId?: string
  /** A confirm the card's More menu asked for (pause or delete). */
  initialConfirming?: Confirming
  onOpen: () => void
  onDeleted: () => void
}) {
  const queryClient = useQueryClient()
  const [confirming, setConfirming] = useState<Confirming>(
    space.can_manage ? initialConfirming : null
  )
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
          <Button type="button" onClick={onOpen}>
            Open teamspace
            <ArrowRight data-icon="inline-end" />
          </Button>
        )}
        {space.can_manage && confirming === null && (
          <>
            {active ? (
              <Button
                variant="outline"
                type="button"
                disabled={busy}
                onClick={() => setConfirming("pause")}
              >
                <Pause data-icon="inline-start" />
                Pause teamspace
              </Button>
            ) : (
              <Button
                variant="outline"
                type="button"
                disabled={busy}
                onClick={() => changeStatus.mutate("active")}
              >
                <Play data-icon="inline-start" />
                {changeStatus.isPending ? "Resuming…" : "Resume teamspace"}
              </Button>
            )}
            {/* Destructive, so pushed to the far end of the row. */}
            <Button
              className="ml-auto"
              variant="destructive"
              type="button"
              disabled={busy}
              onClick={() => setConfirming("delete")}
            >
              <Trash2 data-icon="inline-start" />
              Delete teamspace
            </Button>
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
          <div className="workspace-teamspace-confirm-actions">
            <Button
              type="button"
              variant="outline"
              onClick={() => setConfirming(null)}
            >
              Cancel
            </Button>
            <Button
              type="button"
              disabled={busy}
              onClick={() => changeStatus.mutate("disabled")}
            >
              {changeStatus.isPending ? "Pausing…" : "Pause teamspace"}
            </Button>
          </div>
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
          <div className="workspace-teamspace-confirm-actions">
            <Button
              type="button"
              variant="outline"
              onClick={() => setConfirming(null)}
            >
              Cancel
            </Button>
            <Button
              type="button"
              variant="destructive"
              disabled={busy}
              onClick={() => remove.mutate()}
            >
              {remove.isPending ? "Deleting…" : "Delete teamspace"}
            </Button>
          </div>
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
