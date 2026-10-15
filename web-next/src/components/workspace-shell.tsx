import { useCallback, useEffect, useRef, useState } from "react"
import type { ComponentType, CSSProperties } from "react"
import {
  ArrowLeft,
  ArrowRight,
  BookOpenText,
  Bot,
  ChartNoAxesColumn,
  Folder,
  House,
  Inbox,
  LayoutGrid,
  LogOut,
  MoreHorizontal,
  PanelLeftClose,
  PanelLeftOpen,
  Puzzle,
  Settings2,
  Sparkles,
  UsersRound,
  Workflow,
} from "lucide-react"
import { AgentsCanvas, AgentsFocus, isAgentsSection } from "./agents-mode"
import { FocusHeader } from "./agents-parts"
import { PrimaryAgentFileViewer } from "./primary-agent-files"
import { BrandChip } from "./brand-chip"
import { HomeOverview } from "./home-overview"
import { ThemeMenuItem } from "./theme-menu-item"
import type { PrimaryAgentFileTarget } from "./primary-agent-files"
import { KnowledgeBases } from "./knowledge-bases"
import type { SelectedKnowledgeBase } from "./knowledge-bases"
import { RequestsCanvas } from "./requests-view"
import { openRequest, useRequestsEntry } from "./requests-view-open"
import { TeamspacesManagement } from "./teamspaces-management"
import type { WorkspaceRole } from "./teamspaces-management"
import { TeamspaceFocus } from "./teamspace-focus"
import { LivePane } from "./live-work"
import type { LiveTarget } from "@/lib/live/children"
import { CallProvider } from "./call-provider"
import { CallSurface } from "./call-surfaces"
import { statusLabel } from "./teamspace-format"
import { DirectMessageView, PersonAvatar } from "./direct-message"
import {
  RecentConversations,
  recentDirectMessages,
} from "./direct-message-inbox"
import { useDmPeople } from "./direct-message-people"
import { usePeerOnline } from "./direct-message-presence"
import { IdentityAvatar, people, TeamspaceAvatar } from "./workspace-avatar"
import { WorkspaceTools } from "./workspace-tools"
import { WorkspaceAgentPanel } from "./workspace-agent-panel"
import type {
  AgentPanelChatRequest,
  AgentPanelTabRequest,
} from "./workspace-agent-panel"
import { PeopleDirectory } from "./people-directory"
import { WorkspaceSettings } from "./workspace-settings"
import type { SettingsPage } from "./workspace-settings"
import { useTeamspaces } from "@/hooks/use-teamspaces"
import { useQuery } from "@tanstack/react-query"
import { agentsQueryOptions } from "@/lib/agents/queries"
import { useAgentsMode } from "@/hooks/use-agents-mode"
import type { AgentsFocusTarget } from "@/hooks/use-agents-mode"
import { useNeedsYouCount } from "@/hooks/use-needs-you"
import { useVisibleHeight } from "@/hooks/use-visible-height"
import { signOut } from "@/lib/auth/client"
import type { Organization, Role, Session } from "@/lib/auth/client"
import { useInbox } from "@/lib/dms"
import { isNameId } from "@/lib/dms/names"
import { OpenRequestProvider } from "@/lib/dms/request-navigation"
import type { AvatarIdentity } from "./workspace-avatar"
import { cn } from "@/lib/utils"
import { captureScreenContext } from "@/lib/agents/screen-context"
import type { ScreenContext, ScreenItem } from "@/lib/agents/screen-context"
import {
  requestPrimaryChat,
  usePrimaryChatActivity,
} from "@/lib/threads/chat-choice"
import type { PrimaryChatTarget } from "@/lib/threads/chat-choice"

type Section =
  | "overview"
  | "knowledge"
  | "tools"
  | "teamspaces"
  | "people"
  | "agents"
  | "multi-agents"
  | "specialists"
  | "requests"
type PrimaryMode = "home" | "team" | "agents"

type NavigationItem = {
  key: Section
  label: string
  icon: ComponentType<{ size?: number; strokeWidth?: number }>
  count?: string
}

const navigation: Record<PrimaryMode, NavigationItem[]> = {
  home: [
    { key: "overview", label: "My workspace", icon: LayoutGrid },
    { key: "knowledge", label: "Library", icon: Sparkles },
    { key: "tools", label: "Tools", icon: Puzzle },
  ],
  team: [
    { key: "teamspaces", label: "Teamspaces", icon: Folder },
    { key: "people", label: "People", icon: UsersRound },
  ],
  agents: [
    { key: "agents", label: "Agent overview", icon: ChartNoAxesColumn },
    { key: "multi-agents", label: "Multi-agent teams", icon: Workflow },
    { key: "specialists", label: "Specialists", icon: Bot },
    { key: "requests", label: "Requests", icon: Inbox },
  ],
}

const primaryModes: Array<{
  key: PrimaryMode
  label: string
  icon: NavigationItem["icon"]
}> = [
  { key: "home", label: "Home", icon: House },
  { key: "team", label: "Team", icon: UsersRound },
  { key: "agents", label: "Agents", icon: ChartNoAxesColumn },
]

const defaultSections: Record<PrimaryMode, Section> = {
  home: "overview",
  team: "teamspaces",
  agents: "agents",
}

const headings: Record<Section, string> = {
  overview: "My workspace",
  teamspaces: "Teamspaces",
  agents: "Agent overview",
  "multi-agents": "Multi-agent teams",
  specialists: "Specialists",
  requests: "Requests",
  people: "People",
  knowledge: "Library",
  tools: "Tools",
}

/**
 * The workspace. A request named in a DM opens in Agents → Requests: what
 * shows a DM reads `useOpenRequest` from the provider here, and its function
 * is `openRequest` (requests-view-open.ts), which the shell below listens for.
 */
export function WorkspaceShell(
  props: NonNullable<Parameters<typeof WorkspaceShellContent>[0]> = {}
) {
  return (
    <OpenRequestProvider value={openRequest}>
      <CallProvider viewerName={props.session?.user.display_name || undefined}>
        <WorkspaceShellContent key={props.session?.user.id} {...props} />
        <CallSurface />
      </CallProvider>
    </OpenRequestProvider>
  )
}

function WorkspaceShellContent({
  session,
  role = session ? "member" : "admin",
  organizationRole,
  selectedOrganization,
  preview = false,
  initialMode = "home",
  initialSection,
  initialDirectMessage,
  onDirectMessageChange,
}: {
  session?: Session
  role?: WorkspaceRole
  organizationRole?: Role
  selectedOrganization?: Organization
  preview?: boolean
  initialMode?: PrimaryMode
  initialSection?: Section
  /** The person whose DM opens in the main canvas first (the page's `?dm=`). */
  initialDirectMessage?: string
  /** The DM open in the main canvas changed (null: none), for the page's address. */
  onDirectMessageChange?: (peerId: string | null) => void
} = {}) {
  const viewerId = session?.user.id
  // DMs are the signed-in member's, in the organization they have open.
  const dmOrganizationId = session ? selectedOrganization?.id : undefined
  // A DM the page may open: with someone other than the member, by an id a
  // DM's name can hold.
  const openable = (peerId: string | null | undefined): string | null =>
    dmOrganizationId && isNameId(peerId) && peerId !== viewerId ? peerId : null
  const firstDm = openable(initialDirectMessage)
  const startMode: PrimaryMode = firstDm ? "team" : initialMode
  const [isMobile, setIsMobile] = useState(false)
  const [leftExpanded, setLeftExpanded] = useState(startMode !== "home")
  const [rightExpanded, setRightExpanded] = useState(false)
  const [activeMode, setActiveMode] = useState<PrimaryMode>(startMode)
  const [sectionsByMode, setSectionsByMode] = useState<
    Record<PrimaryMode, Section>
  >({
    ...defaultSections,
    [initialMode]: initialSection || defaultSections[initialMode],
    ...(firstDm ? { team: "people" as const } : {}),
  })
  const teamspaces = useTeamspaces(selectedOrganization?.id)
  const ownAgents = useQuery({
    ...agentsQueryOptions(selectedOrganization?.id ?? ""),
    enabled: Boolean(selectedOrganization),
  })
  const [knowledgeBaseSelection, setKnowledgeBaseSelection] =
    useState<SelectedKnowledgeBase | null>(null)
  const [signingOut, setSigningOut] = useState(false)
  const [signOutError, setSignOutError] = useState("")
  const [profileMenuOpen, setProfileMenuOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  // The page Settings opens on, when a link asked for one.
  const [settingsPage, setSettingsPage] = useState<SettingsPage>()
  // The request a link or a note named, which Requests opens first.
  const [requestFocus, setRequestFocus] = useState<string | null>(null)
  const agentsMode = useAgentsMode()
  const [primaryWork, setPrimaryWork] = useState(false)
  const [primaryComposing, setPrimaryComposing] = useState(false)
  const [primaryViewer, setPrimaryViewer] = useState<
    "overview" | "file" | "live"
  >("overview")
  const [primaryLive, setPrimaryLive] = useState<LiveTarget | null>(null)
  const [primaryFile, setPrimaryFile] = useState<PrimaryAgentFileTarget | null>(
    null
  )
  const [primaryThreadId, setPrimaryThreadId] = useState<
    string | null | undefined
  >()
  const [chatRequest, setChatRequest] = useState<AgentPanelChatRequest | null>(
    null
  )
  const [tabRequest, setTabRequest] = useState<AgentPanelTabRequest | null>(
    null
  )
  // Direct messages: the member's inbox (Team's recent conversations, the
  // DMs tab and its badge), whom they can message, and who is known to be
  // online. The DM open in the main canvas is `dmPeerId`, under People.
  const inbox = useInbox(dmOrganizationId, viewerId)
  const dmPeople = useDmPeople(dmOrganizationId, viewerId, teamspaces.spaces)
  const directMessages = viewerId
    ? { viewerId, inbox, people: dmPeople }
    : undefined
  const primaryChatActivity = usePrimaryChatActivity({
    organizationId: dmOrganizationId ?? "",
    userId: directMessages?.viewerId ?? "",
  })
  const [dmPeerId, setDmPeerId] = useState<string | null>(firstDm)
  // The member opened it just now: focus goes into it.
  const [dmOpenedByMember, setDmOpenedByMember] = useState(false)
  // Back from a DM, focus returns to that person in People.
  const [dmReturnTo, setDmReturnTo] = useState<string | null>(null)
  const [dmDrafts, setDmDrafts] = useState<Partial<Record<string, string>>>({})
  const [dmComposing, setDmComposing] = useState(false)

  const currentIdentity: AvatarIdentity = session
    ? {
        id: session.user.id,
        name: session.user.display_name || session.user.email,
        initials: (session.user.display_name || session.user.email)
          .split(/\s+/)
          .slice(0, 2)
          .map((part) => part.charAt(0).toUpperCase())
          .join(""),
        kind: "user",
      }
    : people.avery

  async function handleSignOut() {
    setSigningOut(true)
    setSignOutError("")
    try {
      await signOut()
      window.location.replace("/auth/sign-in")
    } catch {
      setSignOutError("Could not sign out. Try again.")
      setSigningOut(false)
    }
  }

  useEffect(() => {
    const media = window.matchMedia("(max-width: 800px)")
    function syncViewport() {
      setIsMobile(media.matches)
      if (media.matches) setLeftExpanded(false)
    }
    syncViewport()
    media.addEventListener("change", syncViewport)
    return () => media.removeEventListener("change", syncViewport)
  }, [])

  useEffect(() => {
    const view = new URLSearchParams(window.location.search).get("view")
    // /settings/billing, where billing sends a member back, lands here.
    if (view === "billing") {
      setSettingsPage("billing")
      setSettingsOpen(true)
      return
    }
    // Slack's install sends its admin back here (slack-settings.tsx).
    if (view === "slack") {
      setSettingsPage("slack")
      setSettingsOpen(true)
      return
    }
    if (view !== "tools") return
    setActiveMode("home")
    setSectionsByMode((current) => ({ ...current, home: "tools" }))
  }, [])

  function closeSettings() {
    setSettingsOpen(false)
    setSettingsPage(undefined)
  }

  useEffect(() => {
    if (!settingsOpen && !profileMenuOpen) return
    function onEscape(event: KeyboardEvent) {
      if (event.key !== "Escape") return
      if (settingsOpen) closeSettings()
      else setProfileMenuOpen(false)
    }
    window.addEventListener("keydown", onEscape)
    return () => window.removeEventListener("keydown", onEscape)
  }, [settingsOpen, profileMenuOpen])

  const section = sectionsByMode[activeMode]
  const heading = headings[section]
  // Agents mode's focused work belongs to that mode and its workspace.
  const agentWork =
    activeMode === "agents" && selectedOrganization ? agentsMode.focus : null
  const isFocused = Boolean(
    primaryWork || agentWork || (teamspaces.openId && selectedOrganization)
  )
  // The teamspace on the page: the agent panel offers its teammates.
  const pageTeamspaceId =
    !primaryWork && !agentWork && selectedOrganization && teamspaces.openId
      ? teamspaces.openId
      : undefined
  // The DM in the main canvas, while People is on screen.
  const canvasPeerId =
    section === "people" && dmOrganizationId && viewerId ? dmPeerId : null
  // Who they are, once the members list can say: until then the header and
  // the canvas wait, rather than greet "Someone".
  const canvasPeer = canvasPeerId ? dmPeople.peer(canvasPeerId) : undefined
  const canvasPeerName = canvasPeer?.name
  const canvasPeerOnline = usePeerOnline(canvasPeerId ?? undefined)
  // On a small screen with the keyboard up, the DM gets the height it leaves.
  const middleHeight = useVisibleHeight(Boolean(canvasPeerId) && dmComposing)
  const primaryVisibleHeight = useVisibleHeight(primaryWork && primaryComposing)
  const recentPeerIds = recentDirectMessages(inbox.conversations).map(
    (conversation) => conversation.peerId
  )
  const selectedKnowledgeBase =
    section === "knowledge" ? (knowledgeBaseSelection ?? undefined) : undefined
  const selectedTeamspace =
    section === "teamspaces" ? teamspaces.selected : undefined
  const detailTitle = canvasPeerId
    ? (canvasPeerName ?? "Direct message")
    : selectedKnowledgeBase?.name || selectedTeamspace?.name
  const detailKind = canvasPeerId
    ? canvasPeerName === undefined
      ? undefined
      : canvasPeerOnline
        ? "Online"
        : "Direct message"
    : selectedKnowledgeBase
      ? "Knowledge base"
      : selectedTeamspace
        ? `Teamspace · ${statusLabel(selectedTeamspace)}`
        : undefined

  const screenView =
    primaryWork && primaryViewer === "file"
      ? "agents/file"
      : agentWork
        ? `agents/${agentWork.kind}`
        : teamspaces.openId
          ? "team/teamspace"
          : activeMode === "agents" && section === "agents"
            ? "agents/overview"
            : `${activeMode}/${section}`
  const screenSelection: ScreenItem | undefined = primaryWork
    ? primaryViewer === "file" && primaryFile
      ? {
          kind: "file",
          id: `${primaryFile.threadId}/${primaryFile.name}`,
          name: primaryFile.name,
        }
      : undefined
    : agentWork
      ? {
          kind: agentWork.kind,
          id: agentWork.kind === "team" ? agentWork.teamId : agentWork.agentId,
          name:
            agentWork.kind === "specialist"
              ? (ownAgents.data?.find((agent) => agent.id === agentWork.agentId)
                  ?.name ?? "")
              : "",
        }
      : teamspaces.openId
        ? {
            kind: "teamspace",
            id: teamspaces.openId,
            name:
              teamspaces.spaces.find((space) => space.id === teamspaces.openId)
                ?.name ?? "",
          }
        : canvasPeerId && canvasPeerName
          ? { kind: "person", id: canvasPeerId, name: canvasPeerName }
          : selectedKnowledgeBase
            ? {
                kind: "knowledge_base",
                id: selectedKnowledgeBase.id,
                name: selectedKnowledgeBase.name,
              }
            : selectedTeamspace
              ? {
                  kind: "teamspace",
                  id: selectedTeamspace.id,
                  name: selectedTeamspace.name,
                }
              : undefined
  const screenContextRef = useRef<() => ScreenContext>(() =>
    captureScreenContext("home/overview")
  )
  screenContextRef.current = () =>
    captureScreenContext(screenView, screenSelection)
  const getScreenContext = useCallback(() => screenContextRef.current(), [])
  // Where the member is, for the agent panel's "Only you · in …", and what
  // its composer says the agent will be shown.
  const panelPlace = primaryWork
    ? "Agent overview"
    : screenSelection?.name || detailTitle || heading
  const panelLookingAt = screenSelection?.name || panelPlace

  // The page's address follows the DM in the main canvas, so a refresh
  // returns to it.
  const reportedDm = useRef(canvasPeerId)
  useEffect(() => {
    if (reportedDm.current === canvasPeerId) return
    reportedDm.current = canvasPeerId
    onDirectMessageChange?.(canvasPeerId)
  }, [canvasPeerId, onDirectMessageChange])

  /** A nav item is the page: People only while no recent conversation in the nav is. */
  function navCurrent(key: Section): boolean {
    if (section !== key) return false
    return (
      key !== "people" || !canvasPeerId || !recentPeerIds.includes(canvasPeerId)
    )
  }

  /** The one sidebar: each page belongs to a mode, and picking it opens both. */
  function selectNav(mode: PrimaryMode, next: Section) {
    if (next === "requests") {
      openRequestsSection()
      if (isMobile) setLeftExpanded(false)
      return
    }
    closePrimaryWork()
    agentsMode.close()
    if (mode !== activeMode) {
      setKnowledgeBaseSelection(null)
      teamspaces.select(null)
    }
    setActiveMode(mode)
    selectSection(next, mode)
  }

  function selectSection(nextSection: Section, mode = activeMode) {
    setDmPeerId(null)
    setKnowledgeBaseSelection(null)
    teamspaces.reset()
    setSectionsByMode((current) => ({
      ...current,
      [mode]: nextSection,
    }))
    if (isMobile) setLeftExpanded(false)
  }

  /** Opens the DM with `peerId` in the main canvas, under People. */
  function openDirectMessage(peerId: string) {
    const peer = openable(peerId)
    if (!peer) return
    setDmPeerId(peer)
    setDmOpenedByMember(true)
    setActiveMode("team")
    setSectionsByMode((current) => ({ ...current, team: "people" }))
    teamspaces.reset()
    if (isMobile) setLeftExpanded(false)
  }

  function closeDirectMessage() {
    setDmReturnTo(dmPeerId)
    setDmPeerId(null)
    setDmOpenedByMember(false)
    setDmComposing(false)
  }

  /** Team's "All conversations": the agent panel's DMs tab. */
  function showAllConversations() {
    expandAgentPanel()
    setTabRequest({ tab: "dms" })
  }

  function openWork(target: AgentsFocusTarget) {
    teamspaces.close()
    setPrimaryWork(false)
    setPrimaryComposing(false)
    agentsMode.open(target)
    setRightExpanded(false)
    if (isMobile) setLeftExpanded(false)
  }

  function openPrimaryWork() {
    teamspaces.close()
    agentsMode.close()
    setPrimaryWork(true)
    setActiveMode("agents")
    setSectionsByMode((current) => ({ ...current, agents: "agents" }))
    setLeftExpanded(false)
    setRightExpanded(false)
  }

  function openAgentOverview() {
    setPrimaryViewer("overview")
    setPrimaryFile(null)
    openPrimaryWork()
  }

  function openHomeConversation(target: PrimaryChatTarget) {
    if (!selectedOrganization || !directMessages) return
    closePrimaryWork()
    expandAgentPanel()
    requestPrimaryChat(
      {
        organizationId: selectedOrganization.id,
        userId: directMessages.viewerId,
      },
      target
    )
  }

  function openHomeHistory() {
    closePrimaryWork()
    expandAgentPanel()
    setTabRequest({ tab: "agents", history: true })
  }

  function navigateFromHome(mode: PrimaryMode, next: Section) {
    closePrimaryWork()
    teamspaces.close()
    agentsMode.close()
    setActiveMode(mode)
    setSectionsByMode((current) => ({ ...current, [mode]: next }))
    if (isMobile) setLeftExpanded(false)
  }

  function openHomeRequests(requestId?: string) {
    openRequestsSection(requestId)
  }

  function openPrimaryLive(target: LiveTarget) {
    setPrimaryLive(target)
    setPrimaryViewer("live")
    openPrimaryWork()
  }

  function openPrimaryFile(file?: PrimaryAgentFileTarget) {
    setPrimaryViewer("file")
    setPrimaryFile(file ?? null)
    openPrimaryWork()
  }

  function closePrimaryWork() {
    setPrimaryWork(false)
    setPrimaryComposing(false)
  }

  function navigatePrimarySection(
    next: "agents" | "multi-agents" | "specialists"
  ) {
    closePrimaryWork()
    selectSection(next)
  }

  /** Opens the agent panel on the Agents tab, talking to this specialist. */
  function chatWith(agentId: string) {
    expandAgentPanel()
    setChatRequest({ agentId })
  }

  // Expanding the right panel collapses the left (DESIGN.md §2).
  function expandAgentPanel() {
    setLeftExpanded(false)
    setRightExpanded(true)
  }

  function openTeamspace(id: string) {
    teamspaces.open(id)
    setRightExpanded(false)
    if (isMobile) setLeftExpanded(false)
  }

  function closeDetail() {
    if (canvasPeerId) closeDirectMessage()
    if (selectedKnowledgeBase) setKnowledgeBaseSelection(null)
    if (selectedTeamspace) teamspaces.select(null)
  }

  // A request named by a link or a note (requests-view-open.ts) opens in
  // Agents → Requests, first; leaving Requests puts it away.
  function openRequestsSection(requestId?: string) {
    closePrimaryWork()
    teamspaces.reset()
    agentsMode.close()
    setDmPeerId(null)
    setKnowledgeBaseSelection(null)
    setRequestFocus(requestId ?? null)
    setActiveMode("agents")
    setSectionsByMode((current) => ({ ...current, agents: "requests" }))
    // The open request takes the width: no agent panel on Requests.
    setRightExpanded(false)
    if (isMobile) setLeftExpanded(false)
  }
  useEffect(() => {
    if (section !== "requests") setRequestFocus(null)
  }, [section])
  useRequestsEntry(openRequestsSection)
  // Requests' header says how many wait on the member (the same read the
  // agent panel counts).
  const requestsWaiting = useNeedsYouCount(
    session && section === "requests" ? selectedOrganization?.id : undefined,
    viewerId
  )

  // Today's widgets, on the page and in the agent panel's At a glance tab.
  const homeOverviewKey = JSON.stringify([selectedOrganization?.id, viewerId])
  const homeOverviewProps = {
    organizationId: session ? selectedOrganization?.id : undefined,
    viewerId,
    viewerName: session ? currentIdentity.name : undefined,
    primaryChatActivity,
    onNewChat: (message?: string) =>
      openHomeConversation({ kind: "new", message }),
    onOpenConversation: (threadId: string) =>
      openHomeConversation({ kind: "thread", threadId }),
    onOpenRequests: openHomeRequests,
    onOpenAgent: openHomeHistory,
    onOpenTools: () => navigateFromHome("home", "tools"),
    onOpenKnowledge: () => navigateFromHome("home", "knowledge"),
    onOpenSlack: () => {
      setSettingsPage("slack")
      setSettingsOpen(true)
    },
    onOpenAccount: () => {
      setSettingsPage("account")
      setSettingsOpen(true)
    },
  }

  return (
    <div
      className="workspace-shell"
      data-left={leftExpanded ? "expanded" : "collapsed"}
      data-right={rightExpanded ? "expanded" : "collapsed"}
      data-view={isFocused ? "focused" : "browse"}
      data-primary-work={primaryWork ? "true" : undefined}
      data-primary-composing={
        primaryWork && primaryComposing ? "true" : undefined
      }
      style={
        primaryVisibleHeight
          ? ({
              "--primary-visible-height": `${primaryVisibleHeight}px`,
            } as CSSProperties)
          : undefined
      }
    >
      {!isFocused && (
        <aside
          id="workspace-navigation"
          className="workspace-left"
          aria-label="Workspace navigation"
        >
          <div className="workspace-left-top">
            {leftExpanded ? (
              <>
                <span className="workspace-left-title">
                  {selectedOrganization?.name ?? "Workspace"}
                </span>
                <button
                  className="workspace-icon-button workspace-left-toggle"
                  type="button"
                  aria-label="Collapse left sidebar"
                  aria-controls="workspace-navigation"
                  aria-expanded={leftExpanded}
                  onClick={() => setLeftExpanded(false)}
                >
                  <PanelLeftClose
                    className="workspace-panel-toggle-desktop"
                    size={18}
                    strokeWidth={1.7}
                  />
                  <ArrowLeft
                    className="workspace-panel-toggle-mobile"
                    size={17}
                    strokeWidth={1.7}
                  />
                </button>
              </>
            ) : (
              <button
                className="workspace-mark workspace-mark-button"
                type="button"
                aria-label="Expand left sidebar"
                aria-controls="workspace-navigation"
                aria-expanded={leftExpanded}
                onClick={() => {
                  setLeftExpanded(true)
                  setRightExpanded(false)
                }}
              >
                <PanelLeftOpen
                  className="workspace-panel-toggle-desktop"
                  size={18}
                  strokeWidth={1.7}
                />
                <ArrowRight
                  className="workspace-panel-toggle-mobile"
                  size={17}
                  strokeWidth={1.7}
                />
              </button>
            )}
          </div>

          <div className="workspace-nav-groups">
            {primaryModes.map((mode) => (
              <nav
                className="workspace-navigation"
                key={mode.key}
                aria-label={`${mode.label} navigation`}
              >
                {leftExpanded && (
                  <span className="workspace-nav-heading" aria-hidden="true">
                    {mode.label}
                  </span>
                )}
                {navigation[mode.key].map((item) => {
                  const Icon = item.icon
                  const current =
                    activeMode === mode.key && navCurrent(item.key)
                  return (
                    <button
                      className={cn(
                        "workspace-nav-item",
                        current && "is-active"
                      )}
                      key={item.key}
                      type="button"
                      title={leftExpanded ? undefined : item.label}
                      aria-label={leftExpanded ? undefined : item.label}
                      aria-current={current ? "page" : undefined}
                      onClick={() => selectNav(mode.key, item.key)}
                    >
                      <Icon size={18} strokeWidth={1.7} />
                      {leftExpanded && (
                        <span className="workspace-nav-label">
                          {item.label}
                        </span>
                      )}
                      {leftExpanded &&
                        (item.count ||
                          (item.key === "teamspaces" &&
                            teamspaces.count !== undefined)) && (
                          <span className="workspace-nav-count">
                            {item.key === "teamspaces"
                              ? teamspaces.count
                              : item.count}
                          </span>
                        )}
                    </button>
                  )
                })}
              </nav>
            ))}

            <RecentConversations
              inbox={inbox}
              people={dmPeople}
              viewerId={viewerId}
              expanded={leftExpanded}
              activePeerId={canvasPeerId}
              onOpen={openDirectMessage}
              onShowAll={showAllConversations}
            />
          </div>

          <div className="workspace-profile-area">
            {profileMenuOpen && (
              <div
                className="workspace-profile-menu"
                role="menu"
                aria-label="User menu"
              >
                <ThemeMenuItem />
                <button
                  type="button"
                  role="menuitem"
                  onClick={() => {
                    setProfileMenuOpen(false)
                    setSettingsPage(undefined)
                    setSettingsOpen(true)
                  }}
                >
                  <Settings2 size={17} strokeWidth={1.7} /> Settings
                </button>
                {session && (
                  <button
                    type="button"
                    role="menuitem"
                    disabled={signingOut}
                    onClick={() => void handleSignOut()}
                  >
                    <LogOut size={17} strokeWidth={1.7} />{" "}
                    {signingOut ? "Signing out…" : "Sign out"}
                  </button>
                )}
              </div>
            )}
            <button
              className="workspace-profile"
              type="button"
              aria-label="Open user menu"
              aria-haspopup="menu"
              aria-expanded={profileMenuOpen}
              onClick={() => setProfileMenuOpen((open) => !open)}
            >
              <IdentityAvatar identity={currentIdentity} />
              {leftExpanded && (
                <>
                  <span className="workspace-profile-copy">
                    <strong>{currentIdentity.name}</strong>
                    <small>
                      {session
                        ? role === "admin"
                          ? "Workspace admin"
                          : "Member"
                        : "Preview admin"}
                    </small>
                  </span>
                  <MoreHorizontal size={17} strokeWidth={1.7} />
                </>
              )}
            </button>
          </div>
          {signOutError && (
            <p className="workspace-profile-error" role="alert">
              {signOutError}
            </p>
          )}
        </aside>
      )}

      <div
        className="workspace-middle"
        data-composing={middleHeight ? "true" : undefined}
        style={middleHeight ? { height: middleHeight } : undefined}
      >
        {primaryWork && selectedOrganization ? (
          <div className="workspace-focus workspace-primary-focus">
            <FocusHeader
              title="Agent overview"
              kind="Primary agent"
              onBack={closePrimaryWork}
            />
            <main className="workspace-focus-layout workspace-primary-focus-layout">
              <div
                className="workspace-conversation workspace-primary-conversation-slot"
                aria-hidden="true"
              />
              <div className="workspace-work-column">
                <section
                  className="workspace-work-panel"
                  aria-label="Agent overview work panel"
                >
                  {primaryViewer === "live" && primaryLive ? (
                    <LivePane
                      organizationId={selectedOrganization.id}
                      room={primaryLive.room}
                      child={primaryLive.child}
                      viewerId={session?.user.id}
                      onBack={() => setPrimaryViewer("overview")}
                    />
                  ) : primaryViewer === "file" ? (
                    <PrimaryAgentFileViewer
                      organizationId={selectedOrganization.id}
                      threadId={primaryFile?.threadId ?? primaryThreadId}
                      file={primaryFile ?? undefined}
                      onBack={() => setPrimaryViewer("overview")}
                    />
                  ) : (
                    <AgentsCanvas
                      section="agents"
                      primaryThreadId={primaryThreadId}
                      organizationId={selectedOrganization.id}
                      viewerId={session?.user.id}
                      onOpen={openWork}
                      onNavigate={navigatePrimarySection}
                      onOpenFile={openPrimaryFile}
                      onOpenLive={openPrimaryLive}
                    />
                  )}
                </section>
              </div>
            </main>
          </div>
        ) : agentWork && selectedOrganization ? (
          <AgentsFocus
            target={agentWork}
            organizationId={selectedOrganization.id}
            onBack={agentsMode.close}
            onOpen={openWork}
            onChat={chatWith}
            getScreenContext={getScreenContext}
          />
        ) : teamspaces.openId && selectedOrganization ? (
          <TeamspaceFocus
            key={teamspaces.openId}
            organizationId={selectedOrganization.id}
            teamspaceId={teamspaces.openId}
            viewerId={session?.user.id}
            onBack={teamspaces.close}
          />
        ) : (
          <>
            <header className="workspace-header">
              {detailTitle ? (
                <>
                  <button
                    className="workspace-header-back"
                    type="button"
                    aria-label={
                      canvasPeerId
                        ? "Back to people"
                        : selectedKnowledgeBase
                          ? "All knowledge bases"
                          : "All teamspaces"
                    }
                    onClick={closeDetail}
                  >
                    <ArrowLeft size={17} strokeWidth={1.7} />
                  </button>
                  <div className="workspace-header-detail">
                    {canvasPeerId && (
                      <PersonAvatar
                        id={canvasPeerId}
                        name={canvasPeerName ?? ""}
                        online={canvasPeerOnline}
                      />
                    )}
                    {selectedKnowledgeBase && (
                      <span className="workspace-header-detail-icon">
                        <BookOpenText size={17} strokeWidth={1.7} />
                      </span>
                    )}
                    {selectedTeamspace && (
                      <TeamspaceAvatar name={selectedTeamspace.name} />
                    )}
                    <div>
                      <h1>{detailTitle}</h1>
                      {detailKind && <small>{detailKind}</small>}
                    </div>
                  </div>
                </>
              ) : (
                <div className="workspace-header-detail">
                  <div>
                    <h1>{heading}</h1>
                    {section === "requests" &&
                      requestsWaiting !== undefined && (
                        <small>
                          {requestsWaiting > 0
                            ? `${requestsWaiting} waiting for you`
                            : "All clear"}
                        </small>
                      )}
                  </div>
                </div>
              )}
              {session && session.organizations.length > 1 && (
                <label className="workspace-organization-switcher">
                  <span className="sr-only">Workspace</span>
                  <select
                    value={
                      selectedOrganization?.id || session.organizations[0].id
                    }
                    onChange={(event) =>
                      window.location.assign(
                        `/?org=${encodeURIComponent(event.target.value)}`
                      )
                    }
                  >
                    {session.organizations.map((organization) => (
                      <option value={organization.id} key={organization.id}>
                        {organization.name}
                      </option>
                    ))}
                  </select>
                </label>
              )}
            </header>
            <main
              className="workspace-canvas"
              aria-label={`${detailTitle || heading} canvas`}
            >
              {section === "overview" && (
                <HomeOverview key={homeOverviewKey} {...homeOverviewProps} />
              )}
              {section === "knowledge" && (
                <KnowledgeBases
                  organizationId={selectedOrganization?.id}
                  organizationName={selectedOrganization?.name}
                  organizationRole={organizationRole}
                  userId={session?.user.id}
                  selected={selectedKnowledgeBase ?? null}
                  onSelect={setKnowledgeBaseSelection}
                  onOpenTools={() => selectSection("tools")}
                />
              )}
              {section === "tools" && (
                <WorkspaceTools organizationId={selectedOrganization?.id} />
              )}
              {section === "people" &&
                (canvasPeerId && !canvasPeer ? (
                  <p className="workspace-dm-waiting" role="status">
                    Loading the conversation…
                  </p>
                ) : canvasPeer && dmOrganizationId && viewerId ? (
                  <DirectMessageView
                    key={canvasPeer.id}
                    organizationId={dmOrganizationId}
                    viewerId={viewerId}
                    peer={canvasPeer}
                    layout="canvas"
                    draft={dmDrafts[canvasPeer.id] ?? ""}
                    onDraftChange={(draft) =>
                      setDmDrafts((current) => ({
                        ...current,
                        [canvasPeer.id]: draft,
                      }))
                    }
                    onComposingChange={setDmComposing}
                    autoFocus={dmOpenedByMember}
                  />
                ) : (
                  <PeopleDirectory
                    people={dmPeople}
                    onMessage={openDirectMessage}
                    focusPersonId={dmReturnTo}
                    onFocused={() => setDmReturnTo(null)}
                  />
                ))}
              {section === "teamspaces" && (
                <TeamspacesManagement
                  organizationId={selectedOrganization?.id}
                  viewerId={session?.user.id}
                  selectedId={teamspaces.selectedId}
                  onSelect={teamspaces.select}
                  onOpen={openTeamspace}
                />
              )}
              {isAgentsSection(section) && (
                <AgentsCanvas
                  section={section}
                  primaryThreadId={primaryThreadId}
                  organizationId={
                    session ? selectedOrganization?.id : undefined
                  }
                  viewerId={session?.user.id}
                  onOpen={openWork}
                  onNavigate={selectSection}
                  onOpenFile={openPrimaryFile}
                />
              )}
              {section === "requests" && (
                <RequestsCanvas
                  organizationId={
                    session ? selectedOrganization?.id : undefined
                  }
                  viewerId={session?.user.id}
                  focusRequestId={requestFocus}
                  onFocusDone={() => setRequestFocus(null)}
                />
              )}
            </main>
          </>
        )}
        <BrandChip />
      </div>

      <WorkspaceAgentPanel
        key={JSON.stringify([dmOrganizationId, viewerId])}
        organizationId={session ? selectedOrganization?.id : undefined}
        expanded={rightExpanded}
        onExpand={expandAgentPanel}
        onCollapse={() => setRightExpanded(false)}
        primaryWork={primaryWork}
        onOpenAgentOverview={openAgentOverview}
        onPrimaryThreadChange={setPrimaryThreadId}
        onPrimaryFileCreated={openPrimaryFile}
        onPrimaryComposingChange={setPrimaryComposing}
        getScreenContext={getScreenContext}
        chatRequest={chatRequest}
        tabRequest={tabRequest}
        directMessages={directMessages}
        teamspaceId={pageTeamspaceId}
        place={panelPlace}
        lookingAt={panelLookingAt}
        onOpenRequests={() => openRequestsSection()}
        glance={
          <HomeOverview
            key={homeOverviewKey}
            {...homeOverviewProps}
            variant="glance"
            teammates={directMessages ? dmPeople : undefined}
            onOpenPeople={() => navigateFromHome("team", "people")}
            onMessage={openDirectMessage}
            onOpenWorkflows={() => navigateFromHome("agents", "agents")}
            agentName={
              ownAgents.data?.find((agent) => agent.kind === "primary")?.name
            }
          />
        }
      />
      {settingsOpen && (
        <WorkspaceSettings
          key={JSON.stringify([
            "settings",
            selectedOrganization?.id ?? session?.organizations[0]?.id,
            viewerId,
          ])}
          onClose={closeSettings}
          initialPage={settingsPage}
          identity={currentIdentity}
          session={session}
          selectedOrganization={selectedOrganization}
          organizationRole={organizationRole}
          preview={preview}
          onSignOut={() => void handleSignOut()}
          signingOut={signingOut}
          signOutError={signOutError}
          onOpenTools={() => {
            setActiveMode("home")
            setSectionsByMode((current) => ({ ...current, home: "tools" }))
            closeSettings()
            if (isMobile) setLeftExpanded(false)
          }}
        />
      )}
    </div>
  )
}
