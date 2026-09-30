import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import type {
  QueryFunctionContext,
  UseQueryOptions,
} from "@tanstack/react-query"
import {
  BookOpen,
  Bot,
  CornerDownLeft,
  MessageSquare,
  Puzzle,
  User,
  Users,
} from "lucide-react"
import {
  Command,
  CommandDialog,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "./ui/command"
import {
  agentsQueryOptions,
  agentTeamsQueryOptions,
} from "@/lib/agents/queries"
import { isUnavailableHere } from "@/lib/agents/policy"
import { ApiError } from "@/lib/api/client"
import { connectionsQueryOptions, toolkitName } from "@/lib/apps/client"
import { membersQueryOptions } from "@/lib/auth/queries"
import { corporaQueryOptions } from "@/lib/rag/client"
import { teamspacesQueryOptions } from "@/lib/teamspaces/queries"
import {
  retryUnlessRefused,
  threadListQueryOptions,
} from "@/lib/threads/client"
import type { CommandPaletteScope } from "@/hooks/use-command-palette"
import "./command-palette.css"

export type CommandPaletteActions = {
  openConversation?: (threadId: string) => void
  openAgent?: (agentId: string) => void
  openAgentTeam?: (teamId: string) => void
  openTeamspace?: (teamspaceId: string) => void
  openKnowledgeBase?: (corpusId: string) => void
  openPerson?: (userId: string) => void
  openTool?: (connectionId: string) => void
}

/** The shell supplies only destinations it can open for this member. */
export type CommandPaletteNavigation = {
  id: string
  label: string
  keywords?: readonly string[]
  onSelect: () => void
}

const scanLimit = 100
const matchesPerSource = 4
const navigationLimit = 12
type Row = { id: string; label: string; detail?: string; select: () => void }
type ReadState<T> = {
  data?: T[]
  isPending: boolean
  isFetching: boolean
  isError: boolean
  error: unknown
  refetch: () => unknown
}

function rowsFrom<T>(query: ReadState<T>, row: (item: T) => Row | null): Row[] {
  // A fresh refusal must not leave a cached item available for navigation.
  if (query.isPending || query.isFetching || query.isError) return []
  return (query.data ?? [])
    .slice(0, scanLimit)
    .map(row)
    .filter((item) => item !== null)
}

function privateRead<T, TKey extends readonly unknown[]>(
  options: Pick<UseQueryOptions<T, unknown, T, TKey>, "queryKey" | "queryFn">,
  viewerId: string
) {
  const queryFn = options.queryFn
  return {
    queryKey: [...options.queryKey, "palette", viewerId],
    queryFn: (context: QueryFunctionContext) => {
      if (typeof queryFn !== "function")
        throw new Error("No search read configured")
      return queryFn({ ...context, queryKey: options.queryKey })
    },
    retry: retryUnlessRefused,
    staleTime: 0,
    refetchOnMount: "always" as const,
    refetchOnWindowFocus: false,
  }
}

function ReadNotice({
  name,
  query,
}: {
  name: string
  query: ReadState<unknown>
}) {
  if (query.isPending || query.isFetching)
    return <p role="status">Loading {name}…</p>
  if (!query.isError) return null
  const error = query.error
  const status = error instanceof ApiError ? error.status : undefined
  const absent = status === 404 || isUnavailableHere(error)
  const refused = status === 401 || status === 403
  const text = absent
    ? `${name} is not available here.`
    : status === 401
      ? `Sign in again to search ${name}.`
      : status === 403
        ? `You can't search ${name} in this workspace.`
        : `Couldn't load ${name}.`
  return (
    <p role="status">
      {text}
      {!absent && !refused && (
        <button type="button" onClick={() => void query.refetch()}>
          Try again
        </button>
      )}
    </p>
  )
}

/**
 * Unmounted draft: the shell owns navigation, chat delivery and permissions.
 * Closed/signed-out palettes create no query observers or service requests.
 */
export function CommandPalette({
  open,
  organizationId,
  viewerId,
  ...props
}: CommandPaletteScope & {
  open: boolean
  onOpenChange: (open: boolean) => void
  actions: CommandPaletteActions
  navigation?: readonly CommandPaletteNavigation[]
}) {
  if (!open || !organizationId || !viewerId) return null
  return (
    <PaletteContents
      key={JSON.stringify([organizationId, viewerId])}
      organizationId={organizationId}
      viewerId={viewerId}
      {...props}
    />
  )
}

function PaletteContents({
  organizationId,
  viewerId,
  onOpenChange,
  actions,
  navigation = [],
}: {
  organizationId: string
  viewerId: string
  onOpenChange: (open: boolean) => void
  actions: CommandPaletteActions
  navigation?: readonly CommandPaletteNavigation[]
}) {
  const [search, setSearch] = useState("")
  // Isolate member results and read afresh on every open. Exact-key corpus
  // invalidations do not reach this copy; reopening waits for a fresh read.
  const threadOptions = threadListQueryOptions(organizationId)
  const threads = useQuery({
    ...privateRead(threadOptions, viewerId),
    enabled: Boolean(actions.openConversation),
  })
  const agentOptions = agentsQueryOptions(organizationId)
  const agents = useQuery({
    ...privateRead(agentOptions, viewerId),
    enabled: Boolean(actions.openAgent),
  })
  const teamOptions = agentTeamsQueryOptions(organizationId)
  const teams = useQuery({
    ...privateRead(teamOptions, viewerId),
    enabled: Boolean(actions.openAgentTeam),
  })
  const spaceOptions = teamspacesQueryOptions(organizationId)
  const spaces = useQuery({
    ...privateRead(spaceOptions, viewerId),
    enabled: Boolean(actions.openTeamspace),
  })
  const baseOptions = corporaQueryOptions(organizationId)
  const bases = useQuery({
    ...privateRead(baseOptions, viewerId),
    enabled: Boolean(actions.openKnowledgeBase),
  })
  const memberOptions = membersQueryOptions(organizationId)
  const members = useQuery({
    ...privateRead(memberOptions, viewerId),
    enabled: Boolean(actions.openPerson),
  })
  const toolOptions = connectionsQueryOptions(organizationId)
  const tools = useQuery({
    ...privateRead(toolOptions, viewerId),
    enabled: Boolean(actions.openTool),
  })
  const groups = [
    {
      name: "Recent conversations",
      icon: MessageSquare,
      query: threads,
      enabled: Boolean(actions.openConversation),
      rows: rowsFrom(threads, (thread) =>
        "origin" in thread && thread.origin != null && thread.origin !== "chat"
          ? null
          : {
              id: thread.id,
              label: thread.title || "New conversation",
              select: () => actions.openConversation?.(thread.id),
            }
      ),
    },
    {
      name: "People",
      icon: User,
      query: members,
      enabled: Boolean(actions.openPerson),
      rows: rowsFrom(members, (member) => ({
        id: member.user_id,
        label: member.display_name || member.email,
        detail: member.email,
        select: () => actions.openPerson?.(member.user_id),
      })).filter((row) => row.id !== viewerId),
    },
    {
      name: "Agents",
      icon: Bot,
      query: agents,
      enabled: Boolean(actions.openAgent),
      rows: rowsFrom(agents, (agent) => ({
        id: agent.id,
        label: agent.name,
        detail:
          agent.status === "disabled"
            ? "Paused"
            : agent.kind === "primary"
              ? "Your agent"
              : "Specialist",
        select: () => actions.openAgent?.(agent.id),
      })),
    },
    {
      name: "Agent teams",
      icon: Users,
      query: teams,
      enabled: Boolean(actions.openAgentTeam),
      rows: rowsFrom(teams, (team) => ({
        id: team.id,
        label: team.name,
        detail: team.status === "disabled" ? "Paused" : undefined,
        select: () => actions.openAgentTeam?.(team.id),
      })),
    },
    {
      name: "Teamspaces",
      icon: Users,
      query: spaces,
      enabled: Boolean(actions.openTeamspace),
      rows: rowsFrom(spaces, (space) => ({
        id: space.id,
        label: space.name,
        detail: space.status === "disabled" ? "Paused" : undefined,
        select: () => actions.openTeamspace?.(space.id),
      })),
    },
    {
      name: "Knowledge bases",
      icon: BookOpen,
      query: bases,
      enabled: Boolean(actions.openKnowledgeBase),
      rows: rowsFrom(bases, (base) => ({
        id: base.id,
        label: base.name,
        select: () => actions.openKnowledgeBase?.(base.id),
      })),
    },
    {
      name: "Connected tools",
      icon: Puzzle,
      query: tools,
      enabled: Boolean(actions.openTool),
      rows: rowsFrom(tools, (tool) => ({
        id: tool.id,
        label: tool.label || toolkitName(tool.toolkit),
        detail: tool.status,
        select: () => actions.openTool?.(tool.id),
      })),
    },
  ].filter((group) => group.enabled)
  const term = search.trim().toLocaleLowerCase()
  const matches = (label: string, detail = "") =>
    `${label} ${detail}`.toLocaleLowerCase().includes(term)
  const pages = navigation
    .slice(0, navigationLimit)
    .filter((page) => matches(page.label, page.keywords?.join(" ")))
  const visible = groups.map((group) => ({
    ...group,
    matches: group.rows.filter((row) => matches(row.label, row.detail)),
  }))
  const resultCount = visible.reduce(
    (count, group) => count + group.matches.length,
    pages.length
  )
  const unsettled = groups.some(
    ({ query }) => query.isPending || query.isFetching || query.isError
  )
  const select = (action: () => void) => {
    onOpenChange(false)
    action()
  }

  return (
    <CommandDialog
      open
      title="Search workspace"
      description="Find your conversations, people and workspace items."
      onOpenChange={onOpenChange}
      className="workspace-command-palette"
      showCloseButton
    >
      <Command shouldFilter={false} label="Search workspace" loop>
        <CommandInput
          aria-label="Search workspace"
          placeholder="Search your workspace…"
          maxLength={100}
          value={search}
          onValueChange={setSearch}
        />
        <CommandList aria-label="Search results">
          {visible.map(({ name, icon: Icon, query, matches: found }) => (
            <div key={name} className="workspace-command-source">
              <ReadNotice name={name.toLocaleLowerCase()} query={query} />
              {found.length > 0 && (
                <CommandGroup heading={name}>
                  {found.slice(0, matchesPerSource).map((row) => (
                    <CommandItem
                      key={row.id}
                      value={`${name}:${row.id}`}
                      aria-label={`${row.label}${row.detail ? ` ${row.detail}` : ""}`}
                      onSelect={() => select(row.select)}
                    >
                      <Icon aria-hidden="true" />
                      <span className="workspace-command-label">
                        {row.label}
                        {row.detail && <small> {row.detail}</small>}
                      </span>
                    </CommandItem>
                  ))}
                  {found.length > matchesPerSource && (
                    <p>
                      Showing the first {matchesPerSource} matches. Keep typing
                      to narrow them.
                    </p>
                  )}
                </CommandGroup>
              )}
              {(query.data?.length ?? 0) > scanLimit && (
                <p>
                  Search covers the first {scanLimit} {name.toLocaleLowerCase()}
                  .
                </p>
              )}
            </div>
          ))}
          {pages.length > 0 && (
            <CommandGroup heading="Go to">
              {pages.map((page) => (
                <CommandItem
                  key={page.id}
                  value={`page:${page.id}`}
                  onSelect={() => select(page.onSelect)}
                >
                  <CornerDownLeft aria-hidden="true" />
                  {page.label}
                </CommandItem>
              ))}
            </CommandGroup>
          )}
          {resultCount === 0 && !unsettled && (
            <p className="workspace-command-empty" role="status">
              No matches in the items searched.
            </p>
          )}
        </CommandList>
      </Command>
    </CommandDialog>
  )
}
