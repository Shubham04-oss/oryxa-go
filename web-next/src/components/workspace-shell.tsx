import { useCallback, useEffect, useRef, useState } from "react"
import type { CSSProperties, ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { useHydrated } from "@tanstack/react-router"
import { ArrowLeft, BookOpenText, Menu } from "lucide-react"
import { AgentChat, chatView } from "./agent-chat"
import type { BlankChat } from "./agent-chat"
import { PrimaryChatHistory } from "./agent-chat-history"
import { ConversationHeader } from "./conversation-header"
import { HomeOverview, deskInboxQueryOptions } from "./home-overview"
import { KnowledgeBases } from "./knowledge-bases"
import type { SelectedKnowledgeBase } from "./knowledge-bases"
import {
  PrimaryAgentFileViewer,
  PrimaryAgentFiles,
  useConversationFiles,
} from "./primary-agent-files"
import type { PrimaryAgentFileTarget } from "./primary-agent-files"
import { PrimaryAgentPaused } from "./primary-agent-paused"
import { usePrimaryChatHost } from "./primary-chat-host"
import { waitsOnOwner } from "./request-card-format"
import { RequestsCanvas } from "./requests-view"
import { requestsLinkOf, useRequestsEntry } from "./requests-view-open"
import { people } from "./workspace-avatar"
import type { AvatarIdentity } from "./workspace-avatar"
import { WorkspaceSettings } from "./workspace-settings"
import type { SettingsPage } from "./workspace-settings"
import { WorkspaceSidebar } from "./workspace-sidebar"
import type { WorkspaceArea } from "./workspace-sidebar"
import { WorkspaceTools } from "./workspace-tools"
import { useVisibleHeight } from "@/hooks/use-visible-height"
import { signOut } from "@/lib/auth/client"
import type { Organization, Role, Session } from "@/lib/auth/client"
import { captureScreenContext } from "@/lib/agents/screen-context"
import type { ScreenContext, ScreenItem } from "@/lib/agents/screen-context"
import { AGENT_REQUESTS_POLL_MS } from "@/lib/agents/queries"
import { authorizedData } from "@/lib/home/queries"
import {
  firstMessage,
  requestPrimaryChat,
  shownThread,
  usePrimaryChatActivity,
} from "@/lib/threads/chat-choice"

export type { WorkspaceArea } from "./workspace-sidebar"

/** What the workspace's member may do in it: the admin sees what an admin manages. */
type WorkspaceRole = "admin" | "member"

const headings: Record<Exclude<WorkspaceArea, "agent">, string> = {
  requests: "Requests",
  knowledge: "Knowledge bases",
  tools: "Tools",
}

/** Where the member's choice of a collapsed sidebar is kept, in this browser. */
const SIDEBAR_KEY = "oryxa.sidebar"

function readCollapsed(): boolean {
  try {
    return window.localStorage.getItem(SIDEBAR_KEY) === "collapsed"
  } catch {
    return false
  }
}

function keepCollapsed(collapsed: boolean) {
  try {
    if (collapsed) window.localStorage.setItem(SIDEBAR_KEY, "collapsed")
    else window.localStorage.removeItem(SIDEBAR_KEY)
  } catch {
    /* Storage may be unavailable; the sidebar still works. */
  }
}

/** The page's address, read once as the shell opens (never during a server render). */
function openingAddress(): URLSearchParams {
  return new URLSearchParams(
    typeof window === "undefined" ? "" : window.location.search
  )
}

/** The screen an address names: `?view=requests|knowledge|tools`, or the member's agent. */
function areaOf(params: URLSearchParams): WorkspaceArea {
  const view = params.get("view")
  return view === "requests" || view === "knowledge" || view === "tools"
    ? view
    : "agent"
}

/** Settings' page an address names: `?view=billing` (billing sends members back here) or `?view=slack` (Slack's install does). */
function settingsPageOf(params: URLSearchParams): SettingsPage | undefined {
  const view = params.get("view")
  return view === "billing" || view === "slack" ? view : undefined
}

/**
 * How many Inbox requests wait on the member, for the sidebar: the desk's own
 * read of their Inbox (so the two agree), re-read on focus and every few
 * seconds, so an ask made in Slack shows at once, wherever the member is.
 * While the desk is shown its own reads keep it fresh. Nothing while it loads
 * or was refused.
 */
function useRequestsWaiting(
  organizationId: string | undefined,
  viewerId: string | undefined,
  poll: boolean
): number | undefined {
  const hydrated = useHydrated()
  const viewer = viewerId ?? ""
  const inbox = useQuery({
    ...deskInboxQueryOptions(organizationId ?? "", viewer),
    enabled: hydrated && Boolean(organizationId && viewerId),
    refetchInterval: poll ? AGENT_REQUESTS_POLL_MS : false,
  })
  const rows = authorizedData(inbox)
  if (!rows) return undefined
  const now = Date.now()
  return rows.filter((request) => waitsOnOwner(request, viewer, now)).length
}

/**
 * The workspace: one sidebar and one central screen (DESIGN.md §2). The
 * sidebar is the only navigation; the center shows exactly one thing: the
 * desk, a conversation with the member's primary agent, Requests, Knowledge
 * or Tools. Settings opens over it all from the profile menu. A request named
 * by a link or a note opens in Requests: `openRequest` (requests-view-open.ts)
 * is what a note calls, and the shell listens for it.
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
  initialArea,
}: {
  session?: Session
  role?: WorkspaceRole
  organizationRole?: Role
  selectedOrganization?: Organization
  preview?: boolean
  /** The screen to open on; otherwise the address's, otherwise the member's agent. */
  initialArea?: WorkspaceArea
} = {}) {
  const viewerId = session?.user.id
  // The chat is the signed-in member's, in the organization they have open.
  const organizationId = session ? selectedOrganization?.id : undefined
  const [address] = useState(openingAddress)
  const [isMobile, setIsMobile] = useState(false)
  const [collapsed, setCollapsed] = useState(readCollapsed)
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const [area, setArea] = useState<WorkspaceArea>(
    () => initialArea ?? areaOf(address)
  )
  const [knowledgeBaseSelection, setKnowledgeBaseSelection] =
    useState<SelectedKnowledgeBase | null>(null)
  // The request a link or a note named, which Requests opens first.
  const [requestFocus, setRequestFocus] = useState<string | null>(
    () => requestsLinkOf(address.toString())?.requestId ?? null
  )
  const [signingOut, setSigningOut] = useState(false)
  const [signOutError, setSignOutError] = useState("")
  const [profileMenuOpen, setProfileMenuOpen] = useState(false)
  // The page Settings opens on, when a link asked for one.
  const [settingsPage, setSettingsPage] = useState<SettingsPage | undefined>(
    () => settingsPageOf(address)
  )
  const [settingsOpen, setSettingsOpen] = useState(
    () => settingsPageOf(address) !== undefined
  )
  // The open conversation's files, shown inline under the header, and the
  // one the viewer shows in place of the conversation.
  const [filesOpen, setFilesOpen] = useState(false)
  const [viewerFile, setViewerFile] = useState<PrimaryAgentFileTarget | null>(
    null
  )
  const [composing, setComposing] = useState(false)
  const menuButton = useRef<HTMLButtonElement>(null)
  const sidebarToggle = useRef<HTMLButtonElement>(null)
  const filesId = "workspace-conversation-files"

  // What the agent is sent as the member's screen, read as they send.
  const screenContextRef = useRef<() => ScreenContext>(() =>
    captureScreenContext("home/overview")
  )
  const getScreenContext = useCallback(() => screenContextRef.current(), [])
  const host = usePrimaryChatHost({
    organizationId,
    viewerId,
    getScreenContext,
  })
  const { chat, dispatch, scope } = host
  const shown = shownThread(chat)
  const first = firstMessage(chat)
  // The member's agent in the center: the desk (no conversation open), a
  // conversation, or, for the moment their agents take to load, nothing yet.
  const view = organizationId ? chatView(chat, scope, host.agentId) : "blank"
  const conversation = view === "conversation"
  const viewing =
    viewerFile && viewerFile.threadId === shown ? viewerFile : null
  const primaryChatActivity = usePrimaryChatActivity({
    organizationId: organizationId ?? "",
    userId: viewerId ?? "",
  })
  const files = useConversationFiles(
    organizationId ?? "",
    typeof shown === "string" ? shown : null
  )
  const requestsWaiting = useRequestsWaiting(
    organizationId,
    viewerId,
    !(area === "agent" && view === "blank")
  )
  const visibleHeight = useVisibleHeight(composing)

  const selectedKnowledgeBase =
    area === "knowledge" ? (knowledgeBaseSelection ?? undefined) : undefined
  const screenView =
    area === "agent"
      ? view !== "conversation"
        ? "home/overview"
        : viewing
          ? "agents/file"
          : "agents/chat"
      : area === "requests"
        ? "agents/requests"
        : `home/${area}`
  const screenSelection: ScreenItem | undefined = selectedKnowledgeBase
    ? {
        kind: "knowledge_base",
        id: selectedKnowledgeBase.id,
        name: selectedKnowledgeBase.name,
      }
    : undefined
  useEffect(() => {
    screenContextRef.current = () =>
      captureScreenContext(screenView, screenSelection)
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
      if (!media.matches) setMobileNavOpen(false)
    }
    syncViewport()
    media.addEventListener("change", syncViewport)
    return () => media.removeEventListener("change", syncViewport)
  }, [])

  // A link to one conversation (`?chat=<thread id>`) opens it, once: the
  // address forgets it, so a reload follows the opening rules again.
  useEffect(() => {
    const threadId = address.get("chat")
    if (!threadId || !scope) return
    requestPrimaryChat(scope, { kind: "thread", threadId })
    setArea("agent")
    const url = new URL(window.location.href)
    url.searchParams.delete("chat")
    window.history.replaceState(
      window.history.state,
      "",
      `${url.pathname}${url.search}${url.hash}`
    )
    // The address is read once, as the shell opens.
  }, [])

  function closeSettings() {
    setSettingsOpen(false)
    setSettingsPage(undefined)
  }

  function openSettings(page?: SettingsPage) {
    setProfileMenuOpen(false)
    setMobileNavOpen(false)
    setSettingsPage(page)
    setSettingsOpen(true)
  }

  function closeMobileNav() {
    setMobileNavOpen(false)
    menuButton.current?.focus()
  }

  useEffect(() => {
    if (!settingsOpen && !profileMenuOpen && !mobileNavOpen) return
    function onEscape(event: KeyboardEvent) {
      if (event.key !== "Escape") return
      if (settingsOpen) closeSettings()
      else if (profileMenuOpen) setProfileMenuOpen(false)
      else closeMobileNav()
    }
    window.addEventListener("keydown", onEscape)
    return () => window.removeEventListener("keydown", onEscape)
  }, [settingsOpen, profileMenuOpen, mobileNavOpen])

  // The phone's navigation takes focus as it opens.
  useEffect(() => {
    if (mobileNavOpen) sidebarToggle.current?.focus()
  }, [mobileNavOpen])

  /** Shows `next` in the center; a phone's navigation closes behind it. */
  function go(next: WorkspaceArea) {
    setArea(next)
    setKnowledgeBaseSelection(null)
    setProfileMenuOpen(false)
    setComposing(false)
    if (isMobile) setMobileNavOpen(false)
  }

  /** New chat: the desk, with the chat on no conversation. */
  function startNewChat() {
    dispatch({ type: "move", thread: null })
    setViewerFile(null)
    go("agent")
  }

  /** A conversation the member chose (the Recent list, a confirmation on the desk). */
  function showConversation() {
    setViewerFile(null)
    go("agent")
  }

  function openConversation(threadId: string) {
    dispatch({ type: "move", thread: threadId })
    showConversation()
  }

  /** Every conversation: the sidebar's Recent list, brought into view. */
  function revealHistory() {
    if (isMobile) setMobileNavOpen(true)
    else if (collapsed) toggleCollapsed()
  }

  function toggleCollapsed() {
    setCollapsed((current) => {
      keepCollapsed(!current)
      return !current
    })
  }

  // A request named by a link or a note (requests-view-open.ts) opens in
  // Requests, first; leaving Requests puts it away.
  function openRequestsSection(requestId?: string) {
    go("requests")
    setRequestFocus(requestId ?? null)
  }
  useRequestsEntry(openRequestsSection)
  useEffect(() => {
    if (area !== "requests") setRequestFocus(null)
  }, [area])

  // The address follows the screen, so a reload stays on it: Requests (on
  // the request it opened), Knowledge or Tools. The member's agent has none:
  // a reload there follows the opening rules.
  useEffect(() => {
    const url = new URL(window.location.href)
    const params = url.searchParams
    const before = params.toString()
    if (area === "agent") params.delete("view")
    else params.set("view", area)
    if (area === "requests" && requestFocus) params.set("request", requestFocus)
    else params.delete("request")
    if (params.toString() === before) return
    const search = params.toString()
    window.history.replaceState(
      window.history.state,
      "",
      `${url.pathname}${search ? `?${search}` : ""}${url.hash}`
    )
  }, [area, requestFocus])

  const heading =
    area === "agent"
      ? view === "conversation"
        ? "Conversation"
        : "Today"
      : headings[area]
  const detailTitle = selectedKnowledgeBase?.name

  let title: ReactNode
  if (area === "agent" && conversation && organizationId) {
    title = (
      <ConversationHeader
        organizationId={organizationId}
        scope={scope}
        chat={chat}
        dispatch={dispatch}
        files={{
          count: files.files.length,
          open: filesOpen,
          onToggle: () => setFilesOpen((open) => !open),
          controls: filesId,
        }}
        onNewChat={() => setViewerFile(null)}
      />
    )
  } else if (area === "agent" && view === "choosing") {
    title = null
  } else if (detailTitle) {
    title = (
      <div className="workspace-header-detail">
        <span className="workspace-header-detail-icon">
          <BookOpenText size={17} strokeWidth={1.7} />
        </span>
        <div>
          <h1>{detailTitle}</h1>
          <small>Knowledge base</small>
        </div>
      </div>
    )
  } else {
    title = <h1>{heading}</h1>
  }

  function renderDesk(blank?: BlankChat) {
    return (
      <div className="workspace-desk">
        <HomeOverview
          organizationId={organizationId}
          viewerId={viewerId}
          primaryChatActivity={primaryChatActivity}
          chat={blank}
          agent={host.primary}
          onOpenRequests={openRequestsSection}
          onOpenHistory={revealHistory}
          onOpenConversation={organizationId ? openConversation : undefined}
          onOpenTools={() => go("tools")}
          onOpenKnowledge={() => go("knowledge")}
          onOpenSlack={() => openSettings("slack")}
          onOpenAccount={() => openSettings("account")}
        />
      </div>
    )
  }

  const recent = organizationId ? (
    <PrimaryChatHistory
      organizationId={organizationId}
      scope={scope}
      chat={chat}
      dispatch={dispatch}
      current={area === "agent" && conversation}
      onOpened={showConversation}
    />
  ) : undefined

  return (
    <div
      className="workspace-shell"
      data-sidebar={collapsed ? "collapsed" : "expanded"}
      data-mobile-nav={isMobile && mobileNavOpen ? "open" : undefined}
      data-composing={visibleHeight ? "true" : undefined}
      style={
        visibleHeight
          ? ({ "--visible-height": `${visibleHeight}px` } as CSSProperties)
          : undefined
      }
    >
      {(!isMobile || mobileNavOpen) && (
        <WorkspaceSidebar
          collapsed={!isMobile && collapsed}
          mobile={isMobile}
          onToggle={isMobile ? closeMobileNav : toggleCollapsed}
          toggleRef={sidebarToggle}
          session={session}
          selectedOrganization={selectedOrganization}
          area={area}
          onNewChat={startNewChat}
          newChatDisabled={first !== null}
          onNavigate={(next) => go(next)}
          requestsWaiting={requestsWaiting}
          recent={recent}
          identity={currentIdentity}
          roleLabel={
            session
              ? role === "admin"
                ? "Workspace admin"
                : "Member"
              : "Preview admin"
          }
          profileMenuOpen={profileMenuOpen}
          onProfileMenuOpenChange={setProfileMenuOpen}
          onOpenSettings={() => openSettings()}
          onSignOut={session ? () => void handleSignOut() : undefined}
          signingOut={signingOut}
          signOutError={signOutError}
        />
      )}
      {isMobile && mobileNavOpen && (
        <div
          className="workspace-scrim"
          aria-hidden="true"
          onClick={closeMobileNav}
        />
      )}

      <div className="workspace-middle">
        <header
          className="workspace-header"
          data-actions={
            area === "agent" && conversation ? "conversation" : undefined
          }
        >
          <div className="workspace-header-start">
            {isMobile && (
              <button
                className="workspace-icon-button workspace-menu-button"
                type="button"
                ref={menuButton}
                aria-label="Open navigation"
                aria-controls="workspace-navigation"
                aria-expanded={mobileNavOpen}
                onClick={() => setMobileNavOpen(true)}
              >
                <Menu size={17} strokeWidth={1.7} />
              </button>
            )}
            {detailTitle && (
              <button
                className="workspace-header-back"
                type="button"
                aria-label="All knowledge bases"
                onClick={() => setKnowledgeBaseSelection(null)}
              >
                <ArrowLeft size={17} strokeWidth={1.7} />
              </button>
            )}
          </div>
          {title}
        </header>
        <main
          className="workspace-canvas"
          data-area={area}
          aria-label={`${detailTitle || heading} canvas`}
        >
          {area === "requests" && (
            <RequestsCanvas
              organizationId={organizationId}
              viewerId={session?.user.id}
              focusRequestId={requestFocus}
              onFocusDone={() => setRequestFocus(null)}
            />
          )}
          {area === "knowledge" && (
            <KnowledgeBases
              organizationId={selectedOrganization?.id}
              organizationName={selectedOrganization?.name}
              organizationRole={organizationRole}
              userId={session?.user.id}
              selected={selectedKnowledgeBase ?? null}
              onSelect={setKnowledgeBaseSelection}
              onOpenTools={() => go("tools")}
            />
          )}
          {area === "tools" && (
            <WorkspaceTools organizationId={selectedOrganization?.id} />
          )}
          {!organizationId && area === "agent" && renderDesk()}
          {organizationId && (
            <div className="workspace-agent-area" hidden={area !== "agent"}>
              {conversation && filesOpen && typeof shown === "string" && (
                <div className="workspace-conversation-files">
                  <PrimaryAgentFiles
                    id={filesId}
                    organizationId={organizationId}
                    threadId={shown}
                    onOpenFile={setViewerFile}
                  />
                </div>
              )}
              {viewing && (
                <section
                  className="workspace-file-viewer"
                  aria-label="File viewer"
                >
                  <PrimaryAgentFileViewer
                    organizationId={organizationId}
                    threadId={viewing.threadId}
                    file={viewing}
                    onBack={() => setViewerFile(null)}
                  />
                </section>
              )}
              {conversation && host.primary?.status === "disabled" && (
                <div className="workspace-conversation-notice">
                  <PrimaryAgentPaused
                    organizationId={organizationId}
                    agent={host.primary}
                  />
                </div>
              )}
              <div
                className="workspace-agent-column"
                hidden={Boolean(viewing)}
                data-view={view}
                {...(conversation
                  ? { role: "region", "aria-label": "Agent chat" }
                  : {})}
              >
                <AgentChat
                  key={host.owner}
                  organizationId={organizationId}
                  scope={scope}
                  visible={area === "agent"}
                  agentId={host.agentId}
                  agentName={host.agentName}
                  chat={chat}
                  dispatch={dispatch}
                  onComposingChange={setComposing}
                  getScreenContext={getScreenContext}
                  renderBlank={renderDesk}
                />
              </div>
            </div>
          )}
        </main>
      </div>

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
            closeSettings()
            go("tools")
          }}
        />
      )}
    </div>
  )
}
