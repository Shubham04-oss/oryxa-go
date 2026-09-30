import { useCallback, useEffect, useRef, useState } from "react"
import type { ComponentType, CSSProperties } from "react"
import {
  ArrowLeft,
  ArrowRight,
  BookOpenText,
  ChartNoAxesColumn,
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
} from "lucide-react"
import { AgentsCanvas } from "./agents-mode"
import { FocusHeader } from "./agents-parts"
import { PrimaryAgentFileViewer } from "./primary-agent-files"
import { HomeOverview } from "./home-overview"
import type { PrimaryAgentFileTarget } from "./primary-agent-files"
import { KnowledgeBases } from "./knowledge-bases"
import type { SelectedKnowledgeBase } from "./knowledge-bases"
import { RequestsCanvas } from "./requests-view"
import { useRequestsEntry } from "./requests-view-open"
import { IdentityAvatar, people } from "./workspace-avatar"
import { WorkspaceTools } from "./workspace-tools"
import { WorkspaceAgentPanel } from "./workspace-agent-panel"
import type { AgentPanelHistoryRequest } from "./workspace-agent-panel"
import { WorkspaceSettings } from "./workspace-settings"
import type { SettingsPage } from "./workspace-settings"
import { useVisibleHeight } from "@/hooks/use-visible-height"
import { signOut } from "@/lib/auth/client"
import type { Organization, Role, Session } from "@/lib/auth/client"
import type { AvatarIdentity } from "./workspace-avatar"
import { cn } from "@/lib/utils"
import { captureScreenContext } from "@/lib/agents/screen-context"
import type { ScreenContext, ScreenItem } from "@/lib/agents/screen-context"
import {
  requestPrimaryChat,
  usePrimaryChatActivity,
} from "@/lib/threads/chat-choice"
import type { PrimaryChatTarget } from "@/lib/threads/chat-choice"

/** What the workspace's member may do in it: the admin sees what an admin manages. */
type WorkspaceRole = "admin" | "member"

type Section = "overview" | "knowledge" | "tools" | "agents" | "requests"
type PrimaryMode = "home" | "agents"

type NavigationItem = {
  key: Section
  label: string
  icon: ComponentType<{ size?: number; strokeWidth?: number }>
}

const navigation: Record<PrimaryMode, NavigationItem[]> = {
  home: [
    { key: "overview", label: "Today", icon: LayoutGrid },
    { key: "knowledge", label: "Knowledge bases", icon: Sparkles },
    { key: "tools", label: "Tools", icon: Puzzle },
  ],
  agents: [
    { key: "agents", label: "Agent overview", icon: ChartNoAxesColumn },
    { key: "requests", label: "Requests", icon: Inbox },
  ],
}

const primaryModes: Array<{
  key: PrimaryMode
  label: string
  icon: NavigationItem["icon"]
}> = [
  { key: "home", label: "Home", icon: House },
  { key: "agents", label: "Agents", icon: ChartNoAxesColumn },
]

const defaultSections: Record<PrimaryMode, Section> = {
  home: "overview",
  agents: "agents",
}

const headings: Record<Section, string> = {
  overview: "Today",
  agents: "Agent overview",
  requests: "Requests",
  knowledge: "Knowledge bases",
  tools: "Tools",
}

/**
 * The workspace: Home and Agents in the left navigation, the member's agent
 * in the right panel, and Settings over it all. A request named by a link or
 * a note opens in Agents → Requests: `openRequest` (requests-view-open.ts) is
 * what a note calls, and the shell below listens for it.
 */
export function WorkspaceShell(
  props: NonNullable<Parameters<typeof WorkspaceShellContent>[0]> = {}
) {
  return <WorkspaceShellContent key={props.session?.user.id} {...props} />
}

function WorkspaceShellContent({
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
  const viewerId = session?.user.id
  // The agent panel is the signed-in member's, in the organization they have
  // open.
  const panelOrganizationId = session ? selectedOrganization?.id : undefined
  const [isMobile, setIsMobile] = useState(false)
  const [leftExpanded, setLeftExpanded] = useState(initialMode !== "home")
  const [rightExpanded, setRightExpanded] = useState(false)
  const [activeMode, setActiveMode] = useState<PrimaryMode>(initialMode)
  const [sectionsByMode, setSectionsByMode] = useState<
    Record<PrimaryMode, Section>
  >({
    ...defaultSections,
    [initialMode]: initialSection || defaultSections[initialMode],
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
  const [primaryWork, setPrimaryWork] = useState(false)
  const [primaryComposing, setPrimaryComposing] = useState(false)
  const [primaryViewer, setPrimaryViewer] = useState<"overview" | "file">(
    "overview"
  )
  const [primaryFile, setPrimaryFile] = useState<PrimaryAgentFileTarget | null>(
    null
  )
  const [primaryThreadId, setPrimaryThreadId] = useState<
    string | null | undefined
  >()
  const [historyRequest, setHistoryRequest] =
    useState<AgentPanelHistoryRequest | null>(null)
  const primaryChatActivity = usePrimaryChatActivity({
    organizationId: panelOrganizationId ?? "",
    userId: viewerId ?? "",
  })

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
  // The focused Agent overview takes the page over: no left sidebar.
  const isFocused = primaryWork
  const primaryVisibleHeight = useVisibleHeight(primaryWork && primaryComposing)
  const selectedKnowledgeBase =
    section === "knowledge" ? (knowledgeBaseSelection ?? undefined) : undefined
  const detailTitle = selectedKnowledgeBase?.name
  const detailKind = selectedKnowledgeBase ? "Knowledge base" : undefined

  const screenView =
    primaryWork && primaryViewer === "file"
      ? "agents/file"
      : activeMode === "agents" && section === "agents"
        ? "agents/overview"
        : `${activeMode}/${section}`
  const screenSelection: ScreenItem | undefined =
    !primaryWork && selectedKnowledgeBase
      ? {
          kind: "knowledge_base",
          id: selectedKnowledgeBase.id,
          name: selectedKnowledgeBase.name,
        }
      : undefined
  const screenContextRef = useRef<() => ScreenContext>(() =>
    captureScreenContext("home/overview")
  )
  screenContextRef.current = () =>
    captureScreenContext(screenView, screenSelection)
  const getScreenContext = useCallback(() => screenContextRef.current(), [])

  function selectMode(nextMode: PrimaryMode) {
    setActiveMode(nextMode)
    if (nextMode !== activeMode) setKnowledgeBaseSelection(null)
  }

  function selectSection(nextSection: Section) {
    setKnowledgeBaseSelection(null)
    setSectionsByMode((current) => ({
      ...current,
      [activeMode]: nextSection,
    }))
    if (isMobile) setLeftExpanded(false)
  }

  function openPrimaryWork() {
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
    if (!selectedOrganization || !viewerId) return
    closePrimaryWork()
    expandAgentPanel()
    requestPrimaryChat(
      { organizationId: selectedOrganization.id, userId: viewerId },
      target
    )
  }

  /** Home's "All conversations": the agent panel, on the primary agent's history. */
  function openHomeHistory() {
    closePrimaryWork()
    expandAgentPanel()
    setHistoryRequest({ kind: "history" })
  }

  function navigateFromHome(mode: PrimaryMode, next: Section) {
    closePrimaryWork()
    setActiveMode(mode)
    setSectionsByMode((current) => ({ ...current, [mode]: next }))
    if (isMobile) setLeftExpanded(false)
  }

  function openHomeRequests(requestId?: string) {
    openRequestsSection(requestId)
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

  // Expanding the right panel collapses the left (DESIGN.md §2).
  function expandAgentPanel() {
    setLeftExpanded(false)
    setRightExpanded(true)
  }

  function closeDetail() {
    if (selectedKnowledgeBase) setKnowledgeBaseSelection(null)
  }

  // A request named by a link or a note (requests-view-open.ts) opens in
  // Agents → Requests, first; leaving Requests puts it away.
  function openRequestsSection(requestId?: string) {
    closePrimaryWork()
    setKnowledgeBaseSelection(null)
    setRequestFocus(requestId ?? null)
    setActiveMode("agents")
    setSectionsByMode((current) => ({ ...current, agents: "requests" }))
    if (isMobile) {
      setLeftExpanded(false)
      setRightExpanded(false)
    }
  }
  useEffect(() => {
    if (section !== "requests") setRequestFocus(null)
  }, [section])
  useRequestsEntry(openRequestsSection)

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
            aria-label={`${activeMode === "home" ? "Home" : "Agents"} navigation`}
          >
            {navigation[activeMode].map((item) => {
              const Icon = item.icon
              const current = section === item.key
              return (
                <button
                  className={cn("workspace-nav-item", current && "is-active")}
                  key={item.key}
                  type="button"
                  title={leftExpanded ? undefined : item.label}
                  aria-label={leftExpanded ? undefined : item.label}
                  aria-current={current ? "page" : undefined}
                  onClick={() => selectSection(item.key)}
                >
                  <Icon size={18} strokeWidth={1.7} />
                  {leftExpanded && (
                    <span className="workspace-nav-label">{item.label}</span>
                  )}
                </button>
              )
            })}
          </nav>

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

      <div className="workspace-middle">
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
                  {primaryViewer === "file" ? (
                    <PrimaryAgentFileViewer
                      organizationId={selectedOrganization.id}
                      threadId={primaryFile?.threadId ?? primaryThreadId}
                      file={primaryFile ?? undefined}
                      onBack={() => setPrimaryViewer("overview")}
                    />
                  ) : (
                    <AgentsCanvas
                      primaryThreadId={primaryThreadId}
                      organizationId={selectedOrganization.id}
                      viewerId={session?.user.id}
                      onOpenFile={openPrimaryFile}
                    />
                  )}
                </section>
              </div>
            </main>
          </div>
        ) : (
          <>
            <header className="workspace-header">
              {detailTitle ? (
                <>
                  <button
                    className="workspace-header-back"
                    type="button"
                    aria-label="All knowledge bases"
                    onClick={closeDetail}
                  >
                    <ArrowLeft size={17} strokeWidth={1.7} />
                  </button>
                  <div className="workspace-header-detail">
                    <span className="workspace-header-detail-icon">
                      <BookOpenText size={17} strokeWidth={1.7} />
                    </span>
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
              {section === "overview" && (
                <HomeOverview
                  key={JSON.stringify([selectedOrganization?.id, viewerId])}
                  organizationId={
                    session ? selectedOrganization?.id : undefined
                  }
                  viewerId={viewerId}
                  primaryChatActivity={primaryChatActivity}
                  onNewChat={(message) =>
                    openHomeConversation({ kind: "new", message })
                  }
                  onOpenConversation={(threadId) =>
                    openHomeConversation({ kind: "thread", threadId })
                  }
                  onOpenRequests={openHomeRequests}
                  onOpenAgent={openHomeHistory}
                  onOpenTools={() => navigateFromHome("home", "tools")}
                  onOpenKnowledge={() => navigateFromHome("home", "knowledge")}
                  onOpenSlack={() => {
                    setSettingsPage("slack")
                    setSettingsOpen(true)
                  }}
                  onOpenAccount={() => {
                    setSettingsPage("account")
                    setSettingsOpen(true)
                  }}
                />
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
              {section === "agents" && (
                <AgentsCanvas
                  primaryThreadId={primaryThreadId}
                  organizationId={
                    session ? selectedOrganization?.id : undefined
                  }
                  viewerId={session?.user.id}
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
      </div>

      <WorkspaceAgentPanel
        key={JSON.stringify([panelOrganizationId, viewerId])}
        organizationId={panelOrganizationId}
        viewerId={viewerId}
        expanded={rightExpanded}
        onExpand={expandAgentPanel}
        onCollapse={() => setRightExpanded(false)}
        primaryWork={primaryWork}
        onOpenAgentOverview={openAgentOverview}
        onPrimaryThreadChange={setPrimaryThreadId}
        onPrimaryFileCreated={openPrimaryFile}
        onPrimaryComposingChange={setPrimaryComposing}
        getScreenContext={getScreenContext}
        historyRequest={historyRequest}
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
