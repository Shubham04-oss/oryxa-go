import { useEffect, useRef, useState } from "react"
import type { ComponentType } from "react"
import {
  ArrowLeft,
  ArrowRight,
  BookOpenText,
  Bot,
  ChartNoAxesColumn,
  Folder,
  House,
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
import { KnowledgeBases } from "./knowledge-bases"
import type { SelectedKnowledgeBase } from "./knowledge-bases"
import { TeamspacesManagement } from "./teamspaces-management"
import type { WorkspaceRole } from "./teamspaces-management"
import { TeamspaceFocus } from "./teamspace-focus"
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
import { useTeamspaces } from "@/hooks/use-teamspaces"
import { useAgentsMode } from "@/hooks/use-agents-mode"
import type { AgentsFocusTarget } from "@/hooks/use-agents-mode"
import { useVisibleHeight } from "@/hooks/use-visible-height"
import { signOut } from "@/lib/auth/client"
import type { Organization, Role, Session } from "@/lib/auth/client"
import { useInbox } from "@/lib/dms"
import { isNameId } from "@/lib/dms/names"
import type { AvatarIdentity } from "./workspace-avatar"
import { cn } from "@/lib/utils"

type Section =
  | "overview"
  | "knowledge"
  | "tools"
  | "teamspaces"
  | "people"
  | "agents"
  | "multi-agents"
  | "specialists"
type PrimaryMode = "home" | "team" | "agents"

type NavigationItem = {
  key: Section
  label: string
  icon: ComponentType<{ size?: number; strokeWidth?: number }>
  count?: string
}

const navigation: Record<PrimaryMode, NavigationItem[]> = {
  home: [
    { key: "overview", label: "Overview", icon: LayoutGrid },
    { key: "knowledge", label: "Knowledge bases", icon: Sparkles },
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
  overview: "Home",
  teamspaces: "Teamspaces",
  agents: "Agent overview",
  "multi-agents": "Multi-agent teams",
  specialists: "Specialists",
  people: "People",
  knowledge: "Knowledge bases",
  tools: "Tools",
}

const savedViews = ["Launch plan", "At-risk accounts", "Q4 pipeline"]

export function WorkspaceShell({
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
  const [leftExpanded, setLeftExpanded] = useState(true)
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
  const [knowledgeBaseSelection, setKnowledgeBaseSelection] =
    useState<SelectedKnowledgeBase | null>(null)
  const [signingOut, setSigningOut] = useState(false)
  const [signOutError, setSignOutError] = useState("")
  const [profileMenuOpen, setProfileMenuOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const agentsMode = useAgentsMode()
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
    if (new URLSearchParams(window.location.search).get("view") !== "tools")
      return
    setActiveMode("home")
    setSectionsByMode((current) => ({ ...current, home: "tools" }))
  }, [])

  useEffect(() => {
    if (!settingsOpen && !profileMenuOpen) return
    function onEscape(event: KeyboardEvent) {
      if (event.key !== "Escape") return
      if (settingsOpen) setSettingsOpen(false)
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
    agentWork || (teamspaces.openId && selectedOrganization)
  )
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

  function selectMode(nextMode: PrimaryMode) {
    setActiveMode(nextMode)
    teamspaces.close()
    if (nextMode !== activeMode) {
      setKnowledgeBaseSelection(null)
      teamspaces.select(null)
    }
  }

  function selectSection(nextSection: Section) {
    setDmPeerId(null)
    setKnowledgeBaseSelection(null)
    teamspaces.reset()
    setSectionsByMode((current) => ({
      ...current,
      [activeMode]: nextSection,
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

  /**
   * A side conversation picked in the DMs list. Its view (a teammate, both
   * agents, in a teamspace: REALTIME.md, "Side conversations") is built
   * with the teamspace page and wires in here.
   */
  function openSideConversation(name: string) {
    void name
  }

  function openWork(target: AgentsFocusTarget) {
    teamspaces.close()
    agentsMode.open(target)
    setRightExpanded(false)
    if (isMobile) setLeftExpanded(false)
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

  return (
    <div
      className="workspace-shell"
      data-left={leftExpanded ? "expanded" : "collapsed"}
      data-right={rightExpanded ? "expanded" : "collapsed"}
      data-view={isFocused ? "focused" : "browse"}
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
                <div
                  className="workspace-switcher"
                  role="group"
                  aria-label="Main areas"
                >
                  {primaryModes.map((mode) => {
                    const Icon = mode.icon
                    const selected = activeMode === mode.key
                    return (
                      <button
                        className={cn(
                          "workspace-switcher-item",
                          selected && "is-selected"
                        )}
                        type="button"
                        key={mode.key}
                        aria-label={mode.label}
                        aria-pressed={selected}
                        title={selected ? undefined : mode.label}
                        onClick={() => selectMode(mode.key)}
                      >
                        <Icon size={18} strokeWidth={1.8} />
                        {selected && <span>{mode.label}</span>}
                      </button>
                    )
                  })}
                </div>
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

          <nav
            className="workspace-navigation"
            aria-label={`${activeMode === "home" ? "Home" : activeMode === "team" ? "Team" : "Agents"} navigation`}
          >
            {navigation[activeMode].map((item) => {
              const Icon = item.icon
              return (
                <button
                  className={cn(
                    "workspace-nav-item",
                    navCurrent(item.key) && "is-active"
                  )}
                  key={item.key}
                  type="button"
                  title={leftExpanded ? undefined : item.label}
                  aria-label={leftExpanded ? undefined : item.label}
                  aria-current={navCurrent(item.key) ? "page" : undefined}
                  onClick={() => selectSection(item.key)}
                >
                  <Icon size={18} strokeWidth={1.7} />
                  {leftExpanded && (
                    <span className="workspace-nav-label">{item.label}</span>
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

          {activeMode === "team" && (
            <RecentConversations
              inbox={inbox}
              people={dmPeople}
              viewerId={viewerId}
              expanded={leftExpanded}
              activePeerId={canvasPeerId}
              onOpen={openDirectMessage}
              onShowAll={showAllConversations}
            />
          )}

          {leftExpanded && activeMode === "home" && (
            <div className="workspace-saved-views">
              <p className="workspace-eyebrow">Saved views</p>
              {savedViews.map((view) => (
                <span className="workspace-saved-view" key={view}>
                  <span className="workspace-view-dot" />
                  {view}
                </span>
              ))}
            </div>
          )}

          <div className="workspace-profile-area">
            {profileMenuOpen && (
              <div
                className="workspace-profile-menu"
                role="menu"
                aria-label="User menu"
              >
                <button
                  type="button"
                  role="menuitem"
                  onClick={() => {
                    setProfileMenuOpen(false)
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
        {agentWork && selectedOrganization ? (
          <AgentsFocus
            target={agentWork}
            organizationId={selectedOrganization.id}
            onBack={agentsMode.close}
            onOpen={openWork}
            onChat={chatWith}
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
                <h1>{heading}</h1>
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
                  organizationId={
                    session ? selectedOrganization?.id : undefined
                  }
                  viewerId={session?.user.id}
                  onOpen={openWork}
                  onNavigate={selectSection}
                />
              )}
            </main>
          </>
        )}
      </div>

      <WorkspaceAgentPanel
        organizationId={session ? selectedOrganization?.id : undefined}
        expanded={rightExpanded}
        onExpand={expandAgentPanel}
        onCollapse={() => setRightExpanded(false)}
        chatRequest={chatRequest}
        tabRequest={tabRequest}
        directMessages={
          viewerId
            ? {
                viewerId,
                inbox,
                people: dmPeople,
                onOpenSide: openSideConversation,
              }
            : undefined
        }
      />
      {settingsOpen && (
        <WorkspaceSettings
          onClose={() => setSettingsOpen(false)}
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
            setSettingsOpen(false)
            if (isMobile) setLeftExpanded(false)
          }}
        />
      )}
    </div>
  )
}
