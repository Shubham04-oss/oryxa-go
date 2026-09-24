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
import { previewSharedSpaces, sharedSpacesOf } from "./people-spaces"
import { IdentityAvatar, people, TeamspaceAvatar } from "./workspace-avatar"
import { WorkspaceTools } from "./workspace-tools"
import { WorkspaceAgentPanel } from "./workspace-agent-panel"
import type { AgentPanelChatRequest } from "./workspace-agent-panel"
import { getSharedPeople, PeopleDirectory } from "./people-directory"
import { WorkspaceSettings } from "./workspace-settings"
import { useTeamspaces } from "@/hooks/use-teamspaces"
import { useAgentsMode } from "@/hooks/use-agents-mode"
import type { AgentsFocusTarget } from "@/hooks/use-agents-mode"
import { signOut } from "@/lib/auth/client"
import type { Organization, Role, Session } from "@/lib/auth/client"
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
}: {
  session?: Session
  role?: WorkspaceRole
  organizationRole?: Role
  selectedOrganization?: Organization
  preview?: boolean
  initialMode?: PrimaryMode
  initialSection?: Section
} = {}) {
  const [isMobile, setIsMobile] = useState(false)
  const [leftExpanded, setLeftExpanded] = useState(true)
  const [rightExpanded, setRightExpanded] = useState(false)
  const [activeMode, setActiveMode] = useState<PrimaryMode>(initialMode)
  const [sectionsByMode, setSectionsByMode] = useState<
    Record<PrimaryMode, Section>
  >({
    ...defaultSections,
    [initialMode]: initialSection || defaultSections[initialMode],
  })
  const teamspaces = useTeamspaces(selectedOrganization?.id)
  const [knowledgeBaseSelection, setKnowledgeBaseSelection] =
    useState<SelectedKnowledgeBase | null>(null)
  const [signingOut, setSigningOut] = useState(false)
  const [signOutError, setSignOutError] = useState("")
  const [profileMenuOpen, setProfileMenuOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [conversationIds, setConversationIds] = useState<string[]>([])
  const [activeConversationId, setActiveConversationId] = useState<
    string | null
  >(null)
  const [conversationMessages, setConversationMessages] = useState<
    Partial<Record<string, string[]>>
  >({})
  const conversationListRef = useRef<HTMLDivElement>(null)
  const agentsMode = useAgentsMode()
  const [chatRequest, setChatRequest] = useState<AgentPanelChatRequest | null>(
    null
  )

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
  // People lists who shares a teamspace with the member; only the dev
  // preview, which has no workspace to read, lists examples.
  const peopleSpaces = preview
    ? previewSharedSpaces
    : sharedSpacesOf(teamspaces.spaces)
  const sharedPeople = getSharedPeople(peopleSpaces, currentIdentity)
  // Agents mode's focused work belongs to that mode and its workspace.
  const agentWork =
    activeMode === "agents" && selectedOrganization ? agentsMode.focus : null
  const isFocused = Boolean(
    agentWork || (teamspaces.openId && selectedOrganization)
  )
  const selectedPerson =
    section === "people"
      ? sharedPeople.find((person) => person.id === activeConversationId)
      : undefined
  const selectedKnowledgeBase =
    section === "knowledge" ? (knowledgeBaseSelection ?? undefined) : undefined
  const selectedTeamspace =
    section === "teamspaces" ? teamspaces.selected : undefined
  const detailTitle =
    selectedPerson?.name ||
    selectedKnowledgeBase?.name ||
    selectedTeamspace?.name
  const detailKind = selectedPerson
    ? "Direct message"
    : selectedKnowledgeBase
      ? "Knowledge base"
      : selectedTeamspace
        ? `Teamspace · ${statusLabel(selectedTeamspace)}`
        : undefined
  const conversations = conversationIds
    .map((id) => sharedPeople.find((person) => person.id === id))
    .filter((person): person is AvatarIdentity => Boolean(person))

  useEffect(() => {
    if (conversationListRef.current) conversationListRef.current.scrollTop = 0
  }, [conversationIds])

  function selectMode(nextMode: PrimaryMode) {
    setActiveMode(nextMode)
    teamspaces.close()
    if (nextMode !== activeMode) {
      setKnowledgeBaseSelection(null)
      teamspaces.select(null)
    }
  }

  function selectSection(nextSection: Section) {
    setActiveConversationId(null)
    setKnowledgeBaseSelection(null)
    teamspaces.reset()
    setSectionsByMode((current) => ({
      ...current,
      [activeMode]: nextSection,
    }))
    if (isMobile) setLeftExpanded(false)
  }

  function openConversation(personId: string) {
    if (!sharedPeople.some((person) => person.id === personId)) return
    setConversationIds((current) => [
      personId,
      ...current.filter((id) => id !== personId),
    ])
    setActiveConversationId(personId)
    setActiveMode("team")
    setSectionsByMode((current) => ({ ...current, team: "people" }))
    teamspaces.reset()
    if (isMobile) setLeftExpanded(false)
  }

  function sendConversationMessage(personId: string, message: string) {
    if (!preview || !sharedPeople.some((person) => person.id === personId))
      return
    setConversationMessages((current) => ({
      ...current,
      [personId]: [...(current[personId] || []), message],
    }))
    setConversationIds((current) => [
      personId,
      ...current.filter((id) => id !== personId),
    ])
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
    if (selectedPerson) setActiveConversationId(null)
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
                    section === item.key &&
                      (item.key !== "people" || !activeConversationId) &&
                      "is-active"
                  )}
                  key={item.key}
                  type="button"
                  title={leftExpanded ? undefined : item.label}
                  aria-label={leftExpanded ? undefined : item.label}
                  aria-current={
                    section === item.key &&
                    (item.key !== "people" || !activeConversationId)
                      ? "page"
                      : undefined
                  }
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
            <div className="workspace-conversations" aria-label="Conversations">
              {leftExpanded && (
                <div className="workspace-conversations-heading">
                  <span>Conversations</span>
                  {conversations.length > 0 && (
                    <small>{conversations.length}</small>
                  )}
                </div>
              )}
              {conversations.length ? (
                <div
                  className="workspace-conversations-list"
                  ref={conversationListRef}
                >
                  {conversations.map((person) => (
                    <button
                      className={cn(
                        "workspace-conversation-item",
                        section === "people" &&
                          activeConversationId === person.id &&
                          "is-active"
                      )}
                      type="button"
                      key={person.id}
                      aria-label={`Open conversation with ${person.name}`}
                      aria-current={
                        section === "people" &&
                        activeConversationId === person.id
                          ? "page"
                          : undefined
                      }
                      title={leftExpanded ? undefined : person.name}
                      onClick={() => openConversation(person.id)}
                    >
                      <IdentityAvatar identity={person} />
                      {leftExpanded && (
                        <span className="workspace-conversation-copy">
                          <strong>{person.name}</strong>
                          <small>
                            {conversationMessages[person.id]?.at(-1) ||
                              "New conversation"}
                          </small>
                        </span>
                      )}
                    </button>
                  ))}
                </div>
              ) : (
                leftExpanded && (
                  <p className="workspace-conversations-empty">
                    Start a chat from People.
                  </p>
                )
              )}
            </div>
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

      <div className="workspace-middle">
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
                      selectedPerson
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
                    {selectedPerson && (
                      <IdentityAvatar identity={selectedPerson} />
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
                      <small>{detailKind}</small>
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
              {section === "people" && (
                <PeopleDirectory
                  spaces={peopleSpaces}
                  currentIdentity={currentIdentity}
                  preview={preview}
                  selectedId={activeConversationId}
                  messages={conversationMessages}
                  onOpenConversation={openConversation}
                  onSendMessage={sendConversationMessage}
                />
              )}
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
