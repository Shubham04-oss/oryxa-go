// The workspace's left sidebar (DESIGN.md §2; boards Sidebar and Rail): the
// workspace switcher, the member's own pages, each teamspace, their messages,
// their agents, the Library and who they are. Built on the library's sidebar
// (components/ui/sidebar.tsx). Every count is a real read; one that has not
// loaded, or failed, is left out rather than guessed.
import type { ReactElement, ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  ArrowLeft,
  ArrowRight,
  BookOpenText,
  Bot,
  ChevronsUpDown,
  Inbox as InboxIcon,
  LayoutGrid,
  LogOut,
  MessageSquare,
  PanelLeftClose,
  Plus,
  Puzzle,
  Repeat,
  Search,
  Settings,
  SquarePen,
  UsersRound,
  Workflow,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupAction,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
} from "@/components/ui/sidebar"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { ThemeMenuItem } from "./theme-menu-item"
import { LiveDot, LiveNow, useLiveTeamspaces } from "./workspace-sidebar-live"
import { IdentityAvatar, TeamspaceAvatar } from "./workspace-avatar"
import { TeamspaceHoverCard } from "./teamspace-hover-card"
import { unreadCount, unreadLabel, unreadWords } from "./teamspace-format"
import type { AvatarIdentity } from "./workspace-avatar"
import { PersonAvatar } from "./direct-message"
import { countLabel } from "./direct-message-format"
import type { DmPeople } from "./direct-message-people"
import { usePeerOnline } from "./direct-message-presence"
import { waitsOnOwner } from "./request-card-format"
import { requestListQueryOptions } from "./requests-view-queries"
import {
  AGENT_REQUESTS_LIMIT,
  agentTeamsQueryOptions,
  agentWorkflowsQueryOptions,
  agentsQueryOptions,
} from "@/lib/agents/queries"
import { primaryAgent, specialistAgents } from "@/lib/agents/policy"
import type { Organization } from "@/lib/auth/client"
import type { Conversation, Inbox } from "@/lib/dms"
import type { Teamspace } from "@/lib/teamspaces/client"
import { cn } from "@/lib/utils"

/** The pages the shell shows in its canvas, each named by the sidebar. */
export type SidebarSection =
  | "overview"
  | "knowledge"
  | "tools"
  | "teamspaces"
  | "people"
  | "agents"
  | "multi-agents"
  | "specialists"
  | "requests"

/**
 * Where a sidebar entry leads: a canvas page, or "chats", the Chats page (the
 * agent session with the member's chats listed beside it).
 */
export type SidebarPage = SidebarSection | "chats"

/** The area a canvas page belongs to, for the shell's per-area memory. */
export function sectionArea(
  section: SidebarSection
): "home" | "team" | "agents" {
  switch (section) {
    case "overview":
    case "knowledge":
    case "tools":
      return "home"
    case "teamspaces":
    case "people":
      return "team"
    default:
      return "agents"
  }
}

/** How many DMs Messages lists before only unread ones are added. */
export const SIDEBAR_DIRECT_MESSAGES = 3
/** Messages never lists more than this many, unread or not. */
const SIDEBAR_DIRECT_MESSAGES_MAX = 6

/**
 * The DMs Messages lists, newest first: the latest three, plus any older one
 * that holds unread messages, up to six.
 */
export function sidebarDirectMessages(
  conversations: readonly Conversation[]
): Conversation[] {
  return conversations
    .filter((conversation) => conversation.kind === "dm")
    .filter(
      (conversation, index) =>
        index < SIDEBAR_DIRECT_MESSAGES || conversation.unread > 0
    )
    .slice(0, SIDEBAR_DIRECT_MESSAGES_MAX)
}

export type WorkspaceSidebarProps = {
  expanded: boolean
  onExpandedChange: (expanded: boolean) => void
  /** The organization to read, only with a session. */
  organizationId?: string
  viewerId?: string
  organizations: readonly Organization[]
  selectedOrganization?: Organization
  identity: AvatarIdentity
  /** What the footer says under the member's name. */
  roleLabel: string
  /** The canvas page on screen, when no teamspace or DM in the list is. */
  section: SidebarPage | null
  /** The teamspace open as the page. */
  openTeamspaceId?: string | null
  /** The DM open in the canvas. */
  activePeerId?: string | null
  teamspaces: readonly Teamspace[]
  /** Undefined until the list has loaded. */
  teamspacesLoaded: boolean
  inbox: Inbox
  people: DmPeople
  onNavigate: (page: SidebarPage) => void
  onOpenTeamspace: (id: string) => void
  onOpenDirectMessage: (peerId: string) => void
  onOpenSettings: () => void
  onSignOut?: () => void
  signingOut?: boolean
  signOutError?: string
  /** "Search or jump to": shown only once something answers it. */
  onSearch?: () => void
}

export function WorkspaceSidebar(props: WorkspaceSidebarProps) {
  const { expanded, onExpandedChange } = props
  return (
    <aside
      id="workspace-navigation"
      className="workspace-left workspace-sidebar"
      aria-label="Workspace navigation"
    >
      <SidebarProvider
        open={expanded}
        onOpenChange={onExpandedChange}
        className="workspace-sidebar-provider"
      >
        <Sidebar collapsible="none" className="workspace-sidebar-panel">
          {expanded ? (
            <ExpandedSidebar {...props} />
          ) : (
            <CollapsedSidebar {...props} />
          )}
        </Sidebar>
      </SidebarProvider>
      {props.signOutError && (
        <p className="workspace-profile-error" role="alert">
          {props.signOutError}
        </p>
      )}
    </aside>
  )
}

/** The counts the sidebar shows, from the reads the pages themselves make. */
function useSidebarCounts(organizationId?: string, viewerId?: string) {
  const enabled = Boolean(organizationId && viewerId)
  const org = organizationId ?? ""
  const inbox = useQuery({
    ...requestListQueryOptions(org, "inbox"),
    enabled,
  })
  const agents = useQuery({ ...agentsQueryOptions(org), enabled })
  const teams = useQuery({ ...agentTeamsQueryOptions(org), enabled })
  const workflows = useQuery({ ...agentWorkflowsQueryOptions(org), enabled })

  const now = Date.now()
  const waiting = inbox.data
    ? inbox.data.filter((request) => waitsOnOwner(request, viewerId ?? "", now))
        .length
    : undefined
  return {
    // A full page may hold more behind it: say so rather than undercount.
    requests:
      waiting === undefined || waiting === 0
        ? undefined
        : inbox.data && inbox.data.length >= AGENT_REQUESTS_LIMIT
          ? `${waiting}+`
          : String(waiting),
    primary: agents.data ? primaryAgent(agents.data) : undefined,
    specialists: agents.data ? specialistAgents(agents.data).length : undefined,
    teams: teams.data?.length,
    workflows: workflows.data?.length,
  }
}

function ExpandedSidebar({
  onExpandedChange,
  organizationId,
  viewerId,
  organizations,
  selectedOrganization,
  identity,
  roleLabel,
  section,
  openTeamspaceId,
  activePeerId,
  teamspaces,
  teamspacesLoaded,
  inbox,
  people,
  onNavigate,
  onOpenTeamspace,
  onOpenDirectMessage,
  onOpenSettings,
  onSignOut,
  signingOut,
  onSearch,
}: WorkspaceSidebarProps) {
  const counts = useSidebarCounts(organizationId, viewerId)
  const live = useLiveTeamspaces(organizationId, teamspaces)
  const directMessages = sidebarDirectMessages(inbox.conversations)
  const named = people.status !== "loading"
  const listedPeer =
    activePeerId &&
    named &&
    directMessages.some((conversation) => conversation.peerId === activePeerId)
      ? activePeerId
      : null
  // A page is current only while no teamspace or listed DM is the page.
  const page = openTeamspaceId || listedPeer ? null : section
  const messagesQuiet = messagesQuietLine(inbox, directMessages.length)

  return (
    <>
      <SidebarHeader className="workspace-sidebar-header">
        <div className="workspace-sidebar-top">
          <OrganizationSwitcher
            organizations={organizations}
            selected={selectedOrganization}
          />
          <Button
            className="workspace-sidebar-toggle"
            variant="outline"
            size="icon"
            aria-label="Collapse left sidebar"
            aria-controls="workspace-navigation"
            aria-expanded
            onClick={() => onExpandedChange(false)}
          >
            <PanelLeftClose className="workspace-panel-toggle-desktop" />
            <ArrowLeft className="workspace-panel-toggle-mobile" />
          </Button>
        </div>
        {onSearch && (
          <Button
            className="workspace-sidebar-search"
            variant="secondary"
            aria-label="Search or jump to"
            aria-keyshortcuts="Meta+K Control+K"
            onClick={onSearch}
          >
            <Search aria-hidden="true" />
            <span>Search or jump to</span>
            <kbd>⌘K</kbd>
          </Button>
        )}
      </SidebarHeader>

      <SidebarContent className="workspace-sidebar-content">
        <nav className="workspace-sidebar-nav" aria-label="Workspace">
          <SidebarGroup className="workspace-sidebar-tile">
            <SidebarMenu>
              <NavItem
                label="My workspace"
                icon={<LayoutGrid />}
                current={page === "overview"}
                onClick={() => onNavigate("overview")}
              />
              <NavItem
                label="Requests"
                icon={<InboxIcon />}
                current={page === "requests"}
                count={counts.requests}
                countLabel={
                  counts.requests ? `${counts.requests} waiting` : undefined
                }
                strong
                onClick={() => onNavigate("requests")}
              />
              <NavItem
                label="Chats"
                icon={<MessageSquare />}
                current={page === "chats"}
                onClick={() => onNavigate("chats")}
              />
            </SidebarMenu>
          </SidebarGroup>

          <LiveNow />

          <SidebarGroup
            className="workspace-sidebar-tile"
            role="group"
            aria-label="Teamspaces"
          >
            <SidebarGroupLabel
              className="workspace-sidebar-heading"
              render={
                <button
                  type="button"
                  aria-current={page === "teamspaces" ? "page" : undefined}
                  onClick={() => onNavigate("teamspaces")}
                />
              }
            >
              Teamspaces
            </SidebarGroupLabel>
            <SidebarGroupAction
              className="workspace-sidebar-heading-action"
              aria-label="Add a teamspace"
              title="Add a teamspace"
              onClick={() => onNavigate("teamspaces")}
            >
              <Plus />
            </SidebarGroupAction>
            <SidebarMenu>
              {teamspaces.map((space) => (
                <NavItem
                  key={space.id}
                  label={space.name}
                  icon={
                    <TeamspaceAvatar
                      name={space.name}
                      className="workspace-sidebar-mark"
                    />
                  }
                  current={openTeamspaceId === space.id}
                  live={live.has(space.id)}
                  {...teamspaceRowCount(space)}
                  onClick={() => onOpenTeamspace(space.id)}
                  wrap={(button) => (
                    <TeamspaceHoverCard space={space} viewerId={viewerId}>
                      {button}
                    </TeamspaceHoverCard>
                  )}
                />
              ))}
            </SidebarMenu>
            {teamspacesLoaded && teamspaces.length === 0 && (
              <p className="workspace-sidebar-quiet">No teamspaces yet.</p>
            )}
          </SidebarGroup>

          <SidebarGroup
            className="workspace-sidebar-tile"
            role="group"
            aria-label="Messages"
          >
            <SidebarGroupLabel className="workspace-sidebar-heading">
              Messages
            </SidebarGroupLabel>
            <SidebarGroupAction
              className="workspace-sidebar-heading-action"
              aria-label="Find someone to message"
              title="Find someone to message"
              onClick={() => onNavigate("people")}
            >
              <SquarePen />
            </SidebarGroupAction>
            <SidebarMenu>
              {named &&
                directMessages.map((conversation) => (
                  <DirectMessageItem
                    key={conversation.name}
                    conversation={conversation}
                    name={people.nameOf(conversation.peerId) ?? "Someone"}
                    current={listedPeer === conversation.peerId}
                    onOpen={onOpenDirectMessage}
                  />
                ))}
              <NavItem
                label="People"
                icon={<UsersRound />}
                current={page === "people"}
                quiet={
                  people.status === "ready"
                    ? String(people.people.length)
                    : undefined
                }
                onClick={() => onNavigate("people")}
              />
            </SidebarMenu>
            {messagesQuiet && (
              <p className="workspace-sidebar-quiet">{messagesQuiet}</p>
            )}
          </SidebarGroup>

          <SidebarGroup
            className="workspace-sidebar-tile"
            role="group"
            aria-label="Agents"
          >
            <SidebarGroupLabel className="workspace-sidebar-heading">
              Agents
            </SidebarGroupLabel>
            <SidebarGroupAction
              className="workspace-sidebar-heading-action"
              aria-label="Add a specialist"
              title="Add a specialist"
              onClick={() => onNavigate("specialists")}
            >
              <Plus />
            </SidebarGroupAction>
            <SidebarMenu>
              <NavItem
                label={counts.primary?.name ?? "Your agent"}
                icon={
                  counts.primary ? (
                    <IdentityAvatar
                      identity={agentIdentity(
                        counts.primary.id,
                        counts.primary.name
                      )}
                      className="workspace-sidebar-mark"
                    />
                  ) : (
                    <Bot />
                  )
                }
                quiet={counts.primary ? "Your agent" : undefined}
                current={page === "agents"}
                onClick={() => onNavigate("agents")}
              />
              <NavItem
                label="Specialists"
                icon={<Bot />}
                current={page === "specialists"}
                quiet={optionalCount(counts.specialists)}
                onClick={() => onNavigate("specialists")}
              />
              <NavItem
                label="Agent teams"
                icon={<Workflow />}
                current={page === "multi-agents"}
                quiet={optionalCount(counts.teams)}
                onClick={() => onNavigate("multi-agents")}
              />
              <NavItem
                label="Workflows"
                icon={<Repeat />}
                quiet={optionalCount(counts.workflows)}
                onClick={() => onNavigate("agents")}
              />
            </SidebarMenu>
          </SidebarGroup>

          <SidebarGroup
            className="workspace-sidebar-tile"
            role="group"
            aria-label="Library"
          >
            <SidebarGroupLabel className="workspace-sidebar-heading">
              Library
            </SidebarGroupLabel>
            <div className="workspace-sidebar-grid">
              <LibraryTile
                label="Knowledge"
                icon={<BookOpenText size={17} strokeWidth={1.7} />}
                current={page === "knowledge"}
                onClick={() => onNavigate("knowledge")}
              />
              <LibraryTile
                label="Tools"
                icon={<Puzzle size={17} strokeWidth={1.7} />}
                current={page === "tools"}
                onClick={() => onNavigate("tools")}
              />
            </div>
          </SidebarGroup>
        </nav>
      </SidebarContent>

      <SidebarFooter className="workspace-sidebar-footer">
        <ProfileMenu
          identity={identity}
          roleLabel={roleLabel}
          onOpenSettings={onOpenSettings}
          onSignOut={onSignOut}
          signingOut={signingOut}
        />
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Settings"
          title="Settings"
          onClick={onOpenSettings}
        >
          <Settings />
        </Button>
      </SidebarFooter>
    </>
  )
}

/**
 * The collapsed rail (board Rail): the workspace's mark, which expands the
 * sidebar; search; then tiles of icons: the member's own pages, their
 * teamspaces, the DMs with unread messages, and their agent and the Library;
 * Settings and the member's avatar at the foot. A name is each icon's tooltip
 * and accessible name; a count is a small badge.
 */
function CollapsedSidebar({
  onExpandedChange,
  organizationId,
  viewerId,
  organizations,
  selectedOrganization,
  identity,
  roleLabel,
  section,
  openTeamspaceId,
  activePeerId,
  teamspaces,
  inbox,
  people,
  onNavigate,
  onOpenTeamspace,
  onOpenDirectMessage,
  onOpenSettings,
  onSignOut,
  signingOut,
  onSearch,
}: WorkspaceSidebarProps) {
  const counts = useSidebarCounts(organizationId, viewerId)
  const live = useLiveTeamspaces(organizationId, teamspaces)
  const workspaceName =
    (selectedOrganization ?? organizations.at(0))?.name ?? "Workspace"
  // The rail lists only the DMs that wait on the member.
  const unread =
    people.status === "loading"
      ? []
      : sidebarDirectMessages(inbox.conversations).filter(
          (conversation) => conversation.unread > 0
        )
  const listedPeer = unread.some(
    (conversation) => conversation.peerId === activePeerId
  )
    ? activePeerId
    : null
  const page = openTeamspaceId || listedPeer ? null : section
  return (
    <>
      <SidebarHeader className="workspace-sidebar-header">
        <Button
          className="workspace-sidebar-toggle"
          variant="outline"
          size="icon"
          aria-label="Expand left sidebar"
          title={workspaceName}
          aria-controls="workspace-navigation"
          aria-expanded={false}
          onClick={() => onExpandedChange(true)}
        >
          <span
            className="workspace-sidebar-org-mark workspace-panel-toggle-desktop"
            aria-hidden="true"
          >
            {workspaceInitial(workspaceName)}
          </span>
          <ArrowRight className="workspace-panel-toggle-mobile" />
        </Button>
      </SidebarHeader>
      <SidebarContent className="workspace-sidebar-content">
        <nav className="workspace-sidebar-nav" aria-label="Workspace">
          {onSearch && (
            <SidebarMenu className="workspace-sidebar-rail-search">
              <NavItem
                label="Search or jump to"
                icon={<Search />}
                collapsed
                onClick={onSearch}
              />
            </SidebarMenu>
          )}
          <SidebarGroup className="workspace-sidebar-tile">
            <SidebarMenu>
              <NavItem
                label="My workspace"
                icon={<LayoutGrid />}
                current={page === "overview"}
                collapsed
                onClick={() => onNavigate("overview")}
              />
              <NavItem
                label="Requests"
                icon={<InboxIcon />}
                current={page === "requests"}
                count={counts.requests}
                countLabel={
                  counts.requests ? `${counts.requests} waiting` : undefined
                }
                strong
                collapsed
                onClick={() => onNavigate("requests")}
              />
              <NavItem
                label="Chats"
                icon={<MessageSquare />}
                current={page === "chats"}
                collapsed
                onClick={() => onNavigate("chats")}
              />
            </SidebarMenu>
          </SidebarGroup>
          {teamspaces.length > 0 && (
            <SidebarGroup
              className="workspace-sidebar-tile"
              role="group"
              aria-label="Teamspaces"
            >
              <SidebarMenu>
                {teamspaces.map((space) => (
                  <NavItem
                    key={space.id}
                    label={space.name}
                    icon={
                      <TeamspaceAvatar
                        name={space.name}
                        className="workspace-sidebar-mark"
                      />
                    }
                    current={openTeamspaceId === space.id}
                    live={live.has(space.id)}
                    {...teamspaceRowCount(space)}
                    collapsed
                    onClick={() => onOpenTeamspace(space.id)}
                  />
                ))}
              </SidebarMenu>
            </SidebarGroup>
          )}
          {unread.length > 0 && (
            <SidebarGroup
              className="workspace-sidebar-tile"
              role="group"
              aria-label="Unread messages"
            >
              <SidebarMenu>
                {unread.map((conversation) => (
                  <DirectMessageItem
                    key={conversation.name}
                    conversation={conversation}
                    name={people.nameOf(conversation.peerId) ?? "Someone"}
                    current={listedPeer === conversation.peerId}
                    collapsed
                    onOpen={onOpenDirectMessage}
                  />
                ))}
              </SidebarMenu>
            </SidebarGroup>
          )}
          <SidebarGroup className="workspace-sidebar-tile">
            <SidebarMenu>
              <NavItem
                label={
                  counts.primary
                    ? `${counts.primary.name}, your agent`
                    : "Your agent"
                }
                icon={
                  counts.primary ? (
                    <IdentityAvatar
                      identity={agentIdentity(
                        counts.primary.id,
                        counts.primary.name
                      )}
                      className="workspace-sidebar-mark"
                    />
                  ) : (
                    <Bot />
                  )
                }
                current={page === "agents"}
                collapsed
                onClick={() => onNavigate("agents")}
              />
              <NavItem
                label="Library"
                icon={<BookOpenText />}
                current={page === "knowledge" || page === "tools"}
                collapsed
                onClick={() => onNavigate("knowledge")}
              />
            </SidebarMenu>
          </SidebarGroup>
        </nav>
      </SidebarContent>
      <SidebarFooter className="workspace-sidebar-footer">
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Settings"
          title="Settings"
          onClick={onOpenSettings}
        >
          <Settings />
        </Button>
        <ProfileMenu
          identity={identity}
          roleLabel={roleLabel}
          onOpenSettings={onOpenSettings}
          onSignOut={onSignOut}
          signingOut={signingOut}
          collapsed
        />
      </SidebarFooter>
    </>
  )
}

function workspaceInitial(name: string): string {
  return name.trim().charAt(0).toUpperCase() || "W"
}

function optionalCount(count: number | undefined): string | undefined {
  return count === undefined ? undefined : String(count)
}

function agentIdentity(id: string, name: string): AvatarIdentity {
  return {
    id,
    name,
    initials: name.slice(0, 2).toUpperCase(),
    kind: "agent",
  }
}

/** A teamspace's unread room messages as a row's strong count, when the list carries them. */
export function teamspaceRowCount(space: Teamspace) {
  const unread = unreadCount(space)
  return unread > 0
    ? {
        count: unreadLabel(unread),
        countLabel: unreadWords(unread),
        strong: true,
      }
    : {}
}

/**
 * One row: an icon, its name, and a count. `strong` counts are things that
 * wait on the member; quiet ones are sizes. Collapsed, the name is the
 * tooltip and the accessible name, and a count is a small badge.
 */
function NavItem({
  label,
  icon,
  current = false,
  count,
  countLabel: spokenCount,
  strong = false,
  quiet,
  collapsed = false,
  live = false,
  onClick,
  wrap,
}: {
  label: string
  icon: ReactNode
  current?: boolean
  count?: string
  /** How a screen reader hears the count ("3 waiting"). */
  countLabel?: string
  strong?: boolean
  quiet?: string
  collapsed?: boolean
  /** A parley runs here: a green dot, and said in the name. */
  live?: boolean
  onClick: () => void
  /** Wraps the row's button, as a hover card does (a teamspace's). */
  wrap?: (button: ReactElement) => ReactElement
}) {
  const name = live ? `${label}, parley live` : label
  const button = (
    <SidebarMenuButton
      className="workspace-sidebar-item"
      isActive={current}
      aria-current={current ? "page" : undefined}
      // The name alone: a planet or avatar icon names itself too.
      aria-label={spokenCount ? `${name}, ${spokenCount}` : name}
      tooltip={collapsed ? label : undefined}
      onClick={onClick}
    >
      {icon}
      {collapsed ? (
        <span className="sr-only">{label}</span>
      ) : (
        <span className="workspace-sidebar-label">{label}</span>
      )}
    </SidebarMenuButton>
  )
  return (
    <SidebarMenuItem>
      {wrap ? wrap(button) : button}
      {count && (
        <SidebarMenuBadge
          className={cn(
            "workspace-sidebar-count",
            strong && "is-strong",
            collapsed && "is-badge"
          )}
          aria-hidden
        >
          {count}
        </SidebarMenuBadge>
      )}
      {live && <LiveDot className={collapsed ? "is-badge" : undefined} />}
      {quiet && !collapsed && (
        <SidebarMenuBadge className="workspace-sidebar-quiet-count">
          {quiet}
        </SidebarMenuBadge>
      )}
    </SidebarMenuItem>
  )
}

function DirectMessageItem({
  conversation,
  name,
  current,
  collapsed = false,
  onOpen,
}: {
  conversation: Conversation
  name: string
  current: boolean
  collapsed?: boolean
  onOpen: (peerId: string) => void
}) {
  const online = usePeerOnline(conversation.peerId)
  const unread = conversation.unread
  return (
    <SidebarMenuItem>
      <SidebarMenuButton
        className="workspace-sidebar-item"
        isActive={current}
        aria-current={current ? "page" : undefined}
        aria-label={`Open conversation with ${name}${unread > 0 ? `, ${unread} unread` : ""}`}
        tooltip={collapsed ? name : undefined}
        onClick={() => onOpen(conversation.peerId)}
      >
        <PersonAvatar
          id={conversation.peerId}
          name={name}
          online={online}
          className="workspace-sidebar-mark"
        />
        {!collapsed && <span className="workspace-sidebar-label">{name}</span>}
      </SidebarMenuButton>
      {unread > 0 && (
        <SidebarMenuBadge
          className={cn("workspace-sidebar-count", collapsed && "is-badge")}
          aria-hidden
        >
          {countLabel(unread, 99)}
        </SidebarMenuBadge>
      )}
    </SidebarMenuItem>
  )
}

function messagesQuietLine(inbox: Inbox, count: number): string | undefined {
  switch (inbox.status) {
    case "live":
      return count === 0 ? "No messages yet." : undefined
    case "error":
      return "Can't reach your messages right now."
    case "session_expired":
      return "Sign in again to see your messages."
    default:
      return undefined
  }
}

function LibraryTile({
  label,
  icon,
  current,
  onClick,
}: {
  label: string
  icon: ReactNode
  current: boolean
  onClick: () => void
}) {
  return (
    <SidebarMenuButton
      className="workspace-sidebar-mini"
      isActive={current}
      aria-current={current ? "page" : undefined}
      onClick={onClick}
    >
      {icon}
      <span>{label}</span>
    </SidebarMenuButton>
  )
}

/** The workspace's name, and a menu to move to another the member belongs to. */
function OrganizationSwitcher({
  organizations,
  selected,
}: {
  organizations: readonly Organization[]
  selected?: Organization
}) {
  const current = selected ?? organizations.at(0)
  const name = current?.name ?? "Workspace"
  const mark = (
    <span className="workspace-sidebar-org-mark" aria-hidden="true">
      {workspaceInitial(name)}
    </span>
  )
  if (organizations.length < 2) {
    return (
      <div className="workspace-sidebar-org">
        {mark}
        <span className="workspace-sidebar-label">{name}</span>
      </div>
    )
  }
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        className="workspace-sidebar-org"
        aria-label={`Switch workspace: ${name}`}
      >
        {mark}
        <span className="workspace-sidebar-label">{name}</span>
        <ChevronsUpDown size={15} strokeWidth={1.8} aria-hidden="true" />
      </DropdownMenuTrigger>
      <DropdownMenuContent className="workspace-sidebar-menu">
        <DropdownMenuGroup>
          <DropdownMenuLabel>Workspaces</DropdownMenuLabel>
          <DropdownMenuRadioGroup
            value={current?.id}
            onValueChange={(id: string) => {
              if (id !== current?.id)
                window.location.assign(`/?org=${encodeURIComponent(id)}`)
            }}
          >
            {organizations.map((organization) => (
              <DropdownMenuRadioItem
                key={organization.id}
                value={organization.id}
              >
                {organization.name}
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** Who the member is, and their menu: theme, Settings, and signing out. */
function ProfileMenu({
  identity,
  roleLabel,
  onOpenSettings,
  onSignOut,
  signingOut,
  collapsed = false,
}: {
  identity: AvatarIdentity
  roleLabel: string
  onOpenSettings: () => void
  onSignOut?: () => void
  signingOut?: boolean
  collapsed?: boolean
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        className="workspace-sidebar-profile"
        aria-label="Open user menu"
      >
        <IdentityAvatar identity={identity} />
        {!collapsed && (
          <span className="workspace-sidebar-profile-copy">
            <strong>{identity.name}</strong>
            <small>{roleLabel}</small>
          </span>
        )}
      </DropdownMenuTrigger>
      <DropdownMenuContent
        className="workspace-sidebar-menu"
        side={collapsed ? "right" : "top"}
        aria-label="User menu"
      >
        <ThemeMenuItem />
        <DropdownMenuItem onClick={onOpenSettings}>
          <Settings size={17} strokeWidth={1.7} /> Settings
        </DropdownMenuItem>
        {onSignOut && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem disabled={signingOut} onClick={onSignOut}>
              <LogOut size={17} strokeWidth={1.7} />{" "}
              {signingOut ? "Signing out…" : "Sign out"}
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
