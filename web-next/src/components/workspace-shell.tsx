import { useEffect, useRef, useState } from "react"
import type { ComponentType, FormEvent } from "react"
import {
  ArrowLeft,
  ArrowRight,
  ArrowUpRight,
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
  PanelRightClose,
  PanelRightOpen,
  Paperclip,
  Puzzle,
  Send,
  Settings2,
  Sparkles,
  UsersRound,
  Workflow,
} from "lucide-react"
import { WorkspaceFocus, workItems } from "./workspace-focus"
import type { WorkId, WorkItem } from "./workspace-focus"
import { initialKnowledgeBases, KnowledgeBases } from "./knowledge-bases"
import type { KnowledgeBase } from "./knowledge-bases"
import {
  initialTeamspaces,
  TeamspacesManagement,
} from "./teamspaces-management"
import type {
  Teamspace,
  TeamspaceStatus,
  WorkspaceRole,
} from "./teamspaces-management"
import {
  agents,
  IdentityAvatar,
  people,
  TeamspaceAvatar,
} from "./workspace-avatar"
import { WorkspaceTools } from "./workspace-tools"
import { getSharedPeople, PeopleDirectory } from "./people-directory"
import { WorkspaceSettings } from "./workspace-settings"
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

const workEntries: Partial<Record<Section, WorkId[]>> = {
  agents: ["launch-crew", "research-specialist"],
  "multi-agents": ["launch-crew"],
  specialists: ["research-specialist"],
}

const activity = [
  {
    identity: people.avery,
    name: "Avery",
    action: "updated the launch timeline",
    detail: "Website refresh · 12 min ago",
  },
  {
    identity: people.jordan,
    name: "Jordan",
    action: "added three new tasks",
    detail: "Acme onboarding · 34 min ago",
  },
  {
    identity: people.sam,
    name: "Sam",
    action: "approved the campaign brief",
    detail: "Q4 campaign · 1 hr ago",
  },
  {
    identity: people.noah,
    name: "Noah",
    action: "shared a customer note",
    detail: "Research hub · 2 hrs ago",
  },
]

function teamspaceWorkItem(space: Teamspace): WorkItem {
  if (space.id === "product-sync") {
    return { ...workItems["product-sync"], status: "Active" }
  }
  return {
    id: space.id,
    title: space.title,
    kind: "Teamspace",
    description: space.description || "A space for your team to work together.",
    status: "Active",
    participants: space.members,
    summary:
      space.description || "Your team can share work and decisions here.",
    tasks: [],
    files: [],
    activity: [],
    messages: [],
  }
}

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
  const [focusedWork, setFocusedWork] = useState<WorkItem | null>(null)
  const [note, setNote] = useState("")
  const [notes, setNotes] = useState<string[]>([])
  const [knowledgeBases, setKnowledgeBases] = useState(initialKnowledgeBases)
  const [teamspaces, setTeamspaces] = useState(initialTeamspaces)
  const [selectedKnowledgeBaseId, setSelectedKnowledgeBaseId] = useState<
    string | null
  >(null)
  const [selectedTeamspaceId, setSelectedTeamspaceId] = useState<string | null>(
    null
  )
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
  const visibleWorkEntries = workEntries[section]
  const sharedPeople = getSharedPeople(teamspaces, currentIdentity)
  const selectedPerson =
    section === "people"
      ? sharedPeople.find((person) => person.id === activeConversationId)
      : undefined
  const selectedKnowledgeBase =
    section === "knowledge"
      ? knowledgeBases.find((base) => base.id === selectedKnowledgeBaseId)
      : undefined
  const selectedTeamspace =
    section === "teamspaces"
      ? teamspaces.find((space) => space.id === selectedTeamspaceId)
      : undefined
  const detailTitle =
    selectedPerson?.name ||
    selectedKnowledgeBase?.name ||
    selectedTeamspace?.title
  const detailKind = selectedPerson
    ? "Direct message"
    : selectedKnowledgeBase
      ? "Knowledge base"
      : selectedTeamspace
        ? `Teamspace · ${selectedTeamspace.status === "active" ? "Active" : "Paused"}`
        : undefined
  const conversations = conversationIds
    .map((id) => sharedPeople.find((person) => person.id === id))
    .filter((person): person is AvatarIdentity => Boolean(person))

  useEffect(() => {
    if (conversationListRef.current) conversationListRef.current.scrollTop = 0
  }, [conversationIds])

  function selectMode(nextMode: PrimaryMode) {
    setActiveMode(nextMode)
    setFocusedWork(null)
    if (nextMode !== activeMode) {
      setSelectedKnowledgeBaseId(null)
      setSelectedTeamspaceId(null)
    }
  }

  function selectSection(nextSection: Section) {
    setFocusedWork(null)
    setActiveConversationId(null)
    setSelectedKnowledgeBaseId(null)
    setSelectedTeamspaceId(null)
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
    setFocusedWork(null)
    setSelectedTeamspaceId(null)
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

  function addNote(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const message = note.trim()
    if (!message) return
    setNotes((current) => [message, ...current])
    setNote("")
  }

  function openWork(workId: WorkId) {
    setFocusedWork(workItems[workId])
    setRightExpanded(false)
    if (isMobile) setLeftExpanded(false)
  }

  function openTeamspace(space: Teamspace) {
    if (space.status !== "active") return
    setSelectedTeamspaceId(null)
    setFocusedWork(teamspaceWorkItem(space))
    setRightExpanded(false)
    if (isMobile) setLeftExpanded(false)
  }

  function changeTeamspaceStatus(id: string, status: TeamspaceStatus) {
    if (role !== "admin") return
    setTeamspaces((current) =>
      current.map((space) =>
        space.id === id ? { ...space, status, updated: "Just now" } : space
      )
    )
  }

  function closeDetail() {
    if (selectedPerson) setActiveConversationId(null)
    if (selectedKnowledgeBase) setSelectedKnowledgeBaseId(null)
    if (selectedTeamspace) setSelectedTeamspaceId(null)
  }

  return (
    <div
      className="workspace-shell"
      data-left={leftExpanded ? "expanded" : "collapsed"}
      data-right={rightExpanded ? "expanded" : "collapsed"}
      data-view={focusedWork ? "focused" : "browse"}
    >
      {!focusedWork && (
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
                    (item.count || item.key === "teamspaces") && (
                      <span className="workspace-nav-count">
                        {item.key === "teamspaces"
                          ? teamspaces.length
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
        {focusedWork ? (
          <WorkspaceFocus
            work={focusedWork}
            onBack={() => setFocusedWork(null)}
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
                      <TeamspaceAvatar name={selectedTeamspace.title} />
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
                  bases={knowledgeBases}
                  selectedId={selectedKnowledgeBaseId}
                  onSelect={setSelectedKnowledgeBaseId}
                  onCreate={(base: KnowledgeBase) =>
                    setKnowledgeBases((current) => [base, ...current])
                  }
                />
              )}
              {section === "tools" && <WorkspaceTools />}
              {section === "people" && (
                <PeopleDirectory
                  spaces={teamspaces}
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
                  role={role}
                  spaces={teamspaces}
                  selectedId={selectedTeamspaceId}
                  onSelect={setSelectedTeamspaceId}
                  onCreate={(space) => {
                    if (role !== "admin") return
                    setTeamspaces((current) => [space, ...current])
                  }}
                  onStatusChange={changeTeamspaceStatus}
                  onOpen={openTeamspace}
                />
              )}
              {visibleWorkEntries && (
                <div className="workspace-entry-content">
                  <p className="workspace-eyebrow">Agentic work</p>
                  <div className="workspace-entry-grid">
                    {visibleWorkEntries.map((workId) => {
                      const work = workItems[workId]
                      return (
                        <button
                          className="workspace-entry"
                          type="button"
                          key={work.id}
                          aria-label={`Open ${work.title}`}
                          onClick={() => openWork(workId)}
                        >
                          <span className="workspace-entry-kind">
                            <IdentityAvatar
                              identity={
                                work.id === "research-specialist"
                                  ? agents.specialist
                                  : agents.planning
                              }
                            />
                            {work.kind}
                          </span>
                          <span className="workspace-entry-title">
                            <strong>{work.title}</strong>
                            <ArrowUpRight size={17} strokeWidth={1.7} />
                          </span>
                          <span className="workspace-entry-description">
                            {work.description}
                          </span>
                        </button>
                      )
                    })}
                  </div>
                </div>
              )}
            </main>
          </>
        )}
      </div>

      <aside
        id="workspace-activity"
        className="workspace-right"
        aria-label="Activity panel"
      >
        <div className="workspace-right-top">
          {rightExpanded && (
            <div>
              <h2>Activity</h2>
              <p>Today, across your team</p>
            </div>
          )}
          <button
            className="workspace-icon-button"
            type="button"
            aria-label={
              rightExpanded
                ? "Collapse activity panel"
                : "Expand activity panel"
            }
            aria-controls="workspace-activity"
            aria-expanded={rightExpanded}
            onClick={() => {
              if (rightExpanded) {
                setRightExpanded(false)
              } else {
                setLeftExpanded(false)
                setRightExpanded(true)
              }
            }}
          >
            {rightExpanded ? (
              <>
                <PanelRightClose
                  className="workspace-panel-toggle-desktop"
                  size={18}
                  strokeWidth={1.7}
                />
                <ArrowRight
                  className="workspace-panel-toggle-mobile"
                  size={17}
                  strokeWidth={1.7}
                />
              </>
            ) : (
              <>
                <PanelRightOpen
                  className="workspace-panel-toggle-desktop"
                  size={18}
                  strokeWidth={1.7}
                />
                <ArrowLeft
                  className="workspace-panel-toggle-mobile"
                  size={17}
                  strokeWidth={1.7}
                />
              </>
            )}
          </button>
        </div>

        {rightExpanded ? (
          <div className="workspace-activity-body">
            <div className="workspace-online">
              <span>Team online</span>
              <span>5 online</span>
            </div>
            <div
              className="workspace-avatar-row"
              aria-label="Online team members"
            >
              {Object.values(people).map((identity) => (
                <IdentityAvatar identity={identity} key={identity.id} />
              ))}
              <span className="workspace-avatar workspace-avatar-more">+2</span>
            </div>
            <div className="workspace-activity-list">
              {notes.map((message, index) => (
                <div
                  className="workspace-activity-item"
                  key={`${message}-${index}`}
                >
                  <IdentityAvatar identity={people.avery} />
                  <div>
                    <p>
                      <strong>You</strong> shared a quick note
                    </p>
                    <small>{message}</small>
                  </div>
                </div>
              ))}
              {activity.map((item) => (
                <div className="workspace-activity-item" key={item.detail}>
                  <IdentityAvatar identity={item.identity} />
                  <div>
                    <p>
                      <strong>{item.name}</strong> {item.action}
                    </p>
                    <small>{item.detail}</small>
                  </div>
                </div>
              ))}
            </div>
            <form className="workspace-quick-note" onSubmit={addNote}>
              <label htmlFor="quick-note">Quick note</label>
              <textarea
                id="quick-note"
                value={note}
                onChange={(event) => setNote(event.target.value)}
                placeholder="Share an update with your team..."
                rows={2}
              />
              <div className="workspace-note-actions">
                <Paperclip size={16} strokeWidth={1.7} aria-hidden="true" />
                <button
                  type="submit"
                  disabled={!note.trim()}
                  aria-label="Send quick note"
                >
                  <Send size={14} strokeWidth={1.8} />
                </button>
              </div>
            </form>
          </div>
        ) : (
          <div className="workspace-right-rail">
            <span className="workspace-rail-label">Activity</span>
            <div
              className="workspace-rail-avatars"
              aria-label="Online team members"
            >
              {[people.avery, people.jordan, people.sam].map((identity) => (
                <IdentityAvatar identity={identity} key={identity.id} />
              ))}
            </div>
          </div>
        )}
      </aside>
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
            setFocusedWork(null)
            setSettingsOpen(false)
          }}
        />
      )}
    </div>
  )
}
