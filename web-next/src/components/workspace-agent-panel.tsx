import { useEffect, useMemo, useReducer, useRef, useState } from "react"
import type { ComponentType, Dispatch, KeyboardEvent, ReactNode } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ArrowLeft,
  ArrowRight,
  MessageCircle,
  PanelRightClose,
  PanelRightOpen,
  UsersRound,
} from "lucide-react"
import { AgentChat } from "./agent-chat"
import { AgentComposer } from "./agent-composer"
import { AgentConversation } from "./agent-conversation"
import { AgentSwitcher, FALLBACK_AGENT_NAME } from "./agent-switcher"
import type { AgentSwitcherEntry } from "./agent-switcher"
import { countLabel, firstName } from "./direct-message-format"
import { DirectMessagesTab, dmPanelList } from "./direct-message-inbox"
import type { DmPanelView } from "./direct-message-inbox"
import type { DmPeople } from "./direct-message-people"
import { SideConversationView, emptySideDraft } from "./side-conversation"
import type { SideDraft } from "./side-conversation"
import type { Teammate } from "./side-conversation-format"
import { TeamspaceTeammates } from "./side-conversation-teammates"
import { SpecialistChat } from "./specialist-chat"
import type { ChatSpecialist } from "./specialist-chat"
import { IdentityAvatar } from "./workspace-avatar"
import type { AvatarIdentity } from "./workspace-avatar"
import { useVisibleHeight } from "@/hooks/use-visible-height"
import type { ManagedAgent } from "@/lib/agents/managed-agents"
import { primaryAgent, specialistAgents } from "@/lib/agents/policy"
import { agentsQueryOptions } from "@/lib/agents/queries"
import type { Inbox } from "@/lib/dms"
import { isNameId, sideConversationName } from "@/lib/dms/names"
import type { Teamspace } from "@/lib/teamspaces/client"
import { teamspaceKeys, teamspaceQueryOptions } from "@/lib/teamspaces/queries"
import {
  claimPrimaryChatRequest,
  initialPrimaryChat,
  primaryChatReducer,
  sameChatScope,
  shownThread,
  usePrimaryChatRequest,
  useRememberedChoice,
} from "@/lib/threads/chat-choice"
import type {
  ChatScope,
  PrimaryChat,
  PrimaryChatAction,
} from "@/lib/threads/chat-choice"
import { cn } from "@/lib/utils"
import type { ScreenContext } from "@/lib/agents/screen-context"

export type AgentPanelTab = "agents" | "dms" | "teams"

/**
 * The shell bringing a tab forward (Team's "All conversations" opens DMs).
 * Each request is a new object, and each is honoured once.
 */
export type AgentPanelTabRequest = { tab: AgentPanelTab }

/** What the DMs tab lists and opens: the member's inbox, and whom it can name. */
export type PanelDirectMessages = {
  viewerId: string
  inbox: Inbox
  people: DmPeople
}

/** A side conversation open in the Agents tab: with whom (as the teamspace names them), in which teamspace. */
type OpenSide = {
  teamspaceId: string
  teamspaceName?: string
  peerId: string
  peerName: string
}

/**
 * The shell asking the panel to chat with one of the member's specialists
 * (Agents mode's `onChat`): the Agents tab comes forward with it selected.
 * Each request is a new object, and each is honoured once.
 */
export type AgentPanelChatRequest = { agentId: string }

/** Avatar seed for the primary agent until its id has loaded. */
const PRIMARY_AGENT_SEED = "primary-agent"

const panelTabs: Array<{
  key: AgentPanelTab
  label: string
  icon?: ComponentType<{ size?: number; strokeWidth?: number }>
  empty?: string
}> = [
  { key: "agents", label: "Agents" },
  {
    key: "dms",
    label: "DMs",
    icon: MessageCircle,
    empty: "Direct messages will appear here.",
  },
  {
    key: "teams",
    label: "Teams",
    icon: UsersRound,
    empty: "Team rooms will appear here.",
  },
]

export function WorkspaceAgentPanel({
  expanded,
  onExpand,
  onCollapse,
  organizationId,
  chatRequest,
  tabRequest,
  directMessages,
  teamspaceId,
  primaryWork,
  onOpenAgentOverview,
  onPrimaryThreadChange,
  onPrimaryFileCreated,
  onPrimaryComposingChange,
  getScreenContext,
}: {
  expanded: boolean
  onExpand: () => void
  onCollapse: () => void
  /** The signed-in member's organization; without one the chat stays off. */
  organizationId?: string
  /** The latest request to chat with a specialist here; the shell expands the panel with it. */
  chatRequest?: AgentPanelChatRequest | null
  /** The latest request to bring a tab forward; the shell expands the panel with it. */
  tabRequest?: AgentPanelTabRequest | null
  /** The member's direct messages; without them (no session) the DMs tab says what it will hold. */
  directMessages?: PanelDirectMessages
  /**
   * The teamspace the page shows, when it shows one: the Agents tab then
   * offers its teammates, each opening a side conversation.
   */
  teamspaceId?: string
  primaryWork?: boolean
  onOpenAgentOverview?: () => void
  onPrimaryThreadChange?: (threadId: string | null) => void
  onPrimaryFileCreated?: (file: {
    threadId: string
    name: string
    version?: number
  }) => void
  onPrimaryComposingChange?: (composing: boolean) => void
  getScreenContext?: () => ScreenContext
}) {
  const [tab, setTab] = useState<AgentPanelTab>("agents")
  const visibleTabs = primaryWork
    ? panelTabs.filter((item) => item.key !== "agents")
    : panelTabs
  const activeTab = primaryWork && tab === "agents" ? "dms" : tab
  // A collapsed panel keeps its primary chat alive until its run finishes.
  const hasOpened = useRef(false)
  // Agent overview moves this mounted conversation into its left column.
  // Opening a file can enter that view before the side panel was ever opened.
  if (expanded || primaryWork) hasOpened.current = true
  // Whom the Agents tab talks to: one of the member's specialists by id,
  // or null for their primary agent.
  const [agentId, setAgentId] = useState<string | null>(null)
  const [answeredRequest, setAnsweredRequest] =
    useState<AgentPanelChatRequest | null>(null)
  // A side conversation open in the Agents tab, in place of the member's
  // own agents until they go back; its drafts (by the conversation's name,
  // shared with the DMs tab) outlast it.
  const [agentsSide, setAgentsSide] = useState<OpenSide | null>(null)
  const [sidePicked, setSidePicked] = useState(false)
  const [sideReturnTo, setSideReturnTo] = useState<string | null>(null)
  const [sideDrafts, setSideDrafts] = useState<
    Partial<Record<string, SideDraft>>
  >({})
  // What the primary chat holds while the member looks at another agent: the
  // conversation it is on, what waits to be opened or sent, and every agent's
  // unsent message (chat-choice.ts).
  const [chat, dispatch] = useReducer(
    primaryChatReducer,
    undefined,
    initialPrimaryChat
  )
  if (chatRequest && chatRequest !== answeredRequest) {
    setAnsweredRequest(chatRequest)
    setTab("agents")
    setAgentId(chatRequest.agentId)
    setAgentsSide(null)
  }
  // The DMs tab's place (its list, the picker, or a conversation) and its
  // drafts, kept here so they outlast the panel collapsing.
  const [dmView, setDmView] = useState<DmPanelView>(dmPanelList)
  const [dmDrafts, setDmDrafts] = useState<Partial<Record<string, string>>>({})
  const [answeredTabRequest, setAnsweredTabRequest] =
    useState<AgentPanelTabRequest | null>(null)
  if (tabRequest && tabRequest !== answeredTabRequest) {
    setAnsweredTabRequest(tabRequest)
    setTab(tabRequest.tab)
    // "All conversations" means the list, not the one last open.
    if (tabRequest.tab === "dms") setDmView(dmPanelList)
  }
  const unread = directMessages?.inbox.unreadTotal ?? 0
  const [composing, setComposing] = useState(false)
  const visibleHeight = useVisibleHeight(expanded && composing)
  const tabRefs = useRef<
    Partial<Record<AgentPanelTab, HTMLButtonElement | null>>
  >({})
  const focusTabOnExpand = useRef(false)
  const agents = useQuery({
    ...agentsQueryOptions(organizationId ?? ""),
    enabled: Boolean(organizationId),
  })
  const viewerId = directMessages?.viewerId
  // Whose choices the primary chat remembers for the session: this member's,
  // in this organization.
  const scope = useMemo<ChatScope | undefined>(
    () =>
      organizationId && viewerId
        ? { organizationId, userId: viewerId }
        : undefined,
    [organizationId, viewerId]
  )
  // On a teamspace page, its teammates: read as the page reads it (one
  // query, shared with the focused view), what the list knows shown at once.
  const queryClient = useQueryClient()
  const offersTeammates = Boolean(organizationId && viewerId && teamspaceId)
  const space = useQuery({
    ...teamspaceQueryOptions(organizationId ?? "", teamspaceId ?? ""),
    enabled: offersTeammates,
    placeholderData: () =>
      queryClient
        .getQueryData<Teamspace[]>(teamspaceKeys.list(organizationId ?? ""))
        ?.find((listed) => listed.id === teamspaceId),
  })
  const primary = agents.data ? primaryAgent(agents.data) : undefined
  const agentName = primary?.name
  const agentSeed = primary?.id ?? PRIMARY_AGENT_SEED
  // What keys the primary chat's remembered choices: the agent's id, known
  // once the agents list has answered (a stand-in only when it could not).
  // Nothing is chosen or remembered before, so no choice sits under a key
  // that changes.
  const chatAgent = agents.isPending ? undefined : agentSeed
  // The tab's own icon always shows the primary agent's voxel avatar, with
  // "Primary agent" as an accessible-name fallback only (never shown as text).
  const tabIconLabel = agentName ?? FALLBACK_AGENT_NAME
  const tabIcon: AvatarIdentity = {
    id: agentSeed,
    name: tabIconLabel,
    initials: tabIconLabel
      .split(/\s+/)
      .slice(0, 2)
      .map((part) => part.charAt(0).toUpperCase())
      .join(""),
    kind: "agent",
  }

  // Whether the Agents tab's chat is in front of the member: the Agents tab
  // of an expanded panel, or the conversation column of the focused Agent
  // overview, and no side conversation over it. A file saved while it isn't
  // stays a chip in the chat: opening it would close the view the member is
  // in.
  const agentsVisible =
    (primaryWork || (expanded && tab === "agents")) && !agentsSide

  // The shell follows the primary chat's conversation: Agent overview lists
  // its files.
  const shown = shownThread(chat)
  useEffect(() => {
    if (shown !== undefined) onPrimaryThreadChange?.(shown)
  }, [shown, onPrimaryThreadChange])

  // The rest of the app asking the primary chat to open a conversation (Home
  // does, then expands the panel). Each request is acted on once: claiming it
  // outlasts this panel. What it asks for is held here, in order, not in the
  // chat, so a request made before the chat first mounted is still applied,
  // and one made while another's message is on its way waits its turn.
  const request = usePrimaryChatRequest()
  useEffect(() => {
    if (!request || !scope || !chatAgent) return
    if (!sameChatScope(request.scope, scope)) return
    if (!claimPrimaryChatRequest(request.id)) return
    setTab("agents")
    setAgentId(null)
    setAgentsSide(null)
    dispatch({ type: "request", request, screen: getScreenContext?.() })
  }, [request, scope, chatAgent])
  useRememberedChoice(chat, scope, chatAgent)

  // A rail button unmounts as the panel opens, so its focus moves to the tab
  // it opened rather than falling back to the page.
  useEffect(() => {
    if (!expanded || !focusTabOnExpand.current) return
    focusTabOnExpand.current = false
    tabRefs.current[activeTab]?.focus()
  }, [expanded, activeTab])

  // A chat asked for elsewhere (a team member's Chat button) brings the
  // Agents tab forward, and focus with it, as the rail's buttons do.
  useEffect(() => {
    if (chatRequest) tabRefs.current.agents?.focus()
  }, [chatRequest])
  useEffect(() => {
    if (tabRequest) tabRefs.current[tabRequest.tab]?.focus()
  }, [tabRequest])

  // The member opened the side conversation just now: focus goes into it
  // then, not when the tab or the panel comes back to it later.
  useEffect(() => {
    if (tab !== "agents" || !expanded) setSidePicked(false)
  }, [tab, expanded])

  /** The side conversation's object name with `peerId` in `teamspace`, when the ids can make one. */
  function sideName(teamspace: string, peerId: string): string | undefined {
    if (
      !isNameId(organizationId) ||
      !isNameId(viewerId) ||
      !isNameId(teamspace) ||
      !isNameId(peerId) ||
      viewerId === peerId
    ) {
      return undefined
    }
    return sideConversationName(organizationId, teamspace, viewerId, peerId)
  }

  function changeSideDraft(name: string, draft: SideDraft) {
    setSideDrafts((current) => ({ ...current, [name]: draft }))
  }

  /** Opens the side conversation with a teammate here, asking one of their agents when one was picked. */
  function openTeammate(teammate: Teammate, pickedAgentId?: string) {
    if (!teamspaceId) return
    const name = sideName(teamspaceId, teammate.userId)
    if (!name) return
    if (pickedAgentId) {
      setSideDrafts((current) => ({
        ...current,
        [name]: {
          ...(current[name] ?? emptySideDraft),
          target: `agent:${pickedAgentId}`,
        },
      }))
    }
    setAgentsSide({
      teamspaceId,
      teamspaceName:
        space.data?.id === teamspaceId ? space.data.name : undefined,
      peerId: teammate.userId,
      peerName: teammate.name,
    })
    setSidePicked(true)
  }

  function openTab(next: AgentPanelTab) {
    setTab(next)
    focusTabOnExpand.current = true
    onExpand()
  }

  function moveBetweenTabs(
    event: KeyboardEvent<HTMLButtonElement>,
    index: number
  ) {
    const last = visibleTabs.length - 1
    const moves: Partial<Record<string, number>> = {
      ArrowRight: index === last ? 0 : index + 1,
      ArrowLeft: index === 0 ? last : index - 1,
      Home: 0,
      End: last,
    }
    const next = moves[event.key]
    if (next === undefined) return
    event.preventDefault()
    setTab(visibleTabs[next].key)
    tabRefs.current[visibleTabs[next].key]?.focus()
  }

  return (
    <aside
      id="workspace-agent-panel"
      className="workspace-right"
      aria-label="Agent panel"
      style={
        expanded && !primaryWork && visibleHeight
          ? { height: visibleHeight }
          : undefined
      }
    >
      <div className="workspace-right-top">
        {expanded && (
          <div
            className="workspace-switcher"
            role="tablist"
            aria-label="Agent panel views"
          >
            {visibleTabs.map((item, index) => {
              const Icon = item.icon
              const selected = activeTab === item.key
              return (
                <button
                  className={cn(
                    "workspace-switcher-item",
                    selected && "is-selected"
                  )}
                  type="button"
                  role="tab"
                  key={item.key}
                  id={`workspace-agent-tab-${item.key}`}
                  ref={(element) => {
                    tabRefs.current[item.key] = element
                  }}
                  aria-label={tabLabel(item.key, item.label, unread)}
                  aria-selected={selected}
                  aria-controls={`workspace-agent-view-${item.key}`}
                  tabIndex={selected ? 0 : -1}
                  title={selected ? undefined : item.label}
                  onClick={() => setTab(item.key)}
                  onKeyDown={(event) => moveBetweenTabs(event, index)}
                >
                  {Icon ? (
                    <Icon size={18} strokeWidth={1.8} />
                  ) : (
                    <IdentityAvatar identity={tabIcon} />
                  )}
                  {selected && <span>{item.label}</span>}
                  {item.key === "dms" && unread > 0 && (
                    <UnreadBadge count={unread} />
                  )}
                </button>
              )
            })}
          </div>
        )}
        <button
          className="workspace-icon-button"
          type="button"
          aria-label={
            expanded
              ? "Collapse agent panel"
              : unread > 0
                ? `Expand agent panel, ${unread} unread`
                : "Expand agent panel"
          }
          aria-controls="workspace-agent-panel"
          aria-expanded={expanded}
          onClick={expanded ? onCollapse : onExpand}
        >
          {expanded ? (
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
              {/* A phone has no rail, so its edge control carries the DMs' unread. */}
              {unread > 0 && (
                <span className="workspace-panel-unread" aria-hidden="true" />
              )}
            </>
          )}
        </button>
      </div>

      {hasOpened.current &&
        panelTabs.map((item) => (
          <div
            className="workspace-agent-view"
            role={primaryWork && item.key === "agents" ? "region" : "tabpanel"}
            key={item.key}
            id={`workspace-agent-view-${item.key}`}
            aria-labelledby={
              primaryWork && item.key === "agents"
                ? undefined
                : `workspace-agent-tab-${item.key}`
            }
            aria-label={
              primaryWork && item.key === "agents"
                ? `Conversation with ${agentName ?? "primary agent"}`
                : undefined
            }
            data-primary-chat={
              primaryWork && item.key === "agents" ? "true" : undefined
            }
            tabIndex={0}
            hidden={
              item.key === "agents" && primaryWork
                ? false
                : !expanded || activeTab !== item.key
            }
          >
            {item.key === "agents" ? (
              <>
                {/* Mounted only while it shows, as a DM is: an open
                    conversation is what makes the member online in it. */}
                {agentsSide &&
                  organizationId &&
                  directMessages &&
                  tab === "agents" &&
                  expanded && (
                    <AgentsTabSide
                      key={`${agentsSide.teamspaceId}~${agentsSide.peerId}`}
                      organizationId={organizationId}
                      directMessages={directMessages}
                      open={agentsSide}
                      name={sideName(agentsSide.teamspaceId, agentsSide.peerId)}
                      teamspaceName={
                        space.data?.id === agentsSide.teamspaceId
                          ? space.data.name
                          : agentsSide.teamspaceName
                      }
                      drafts={sideDrafts}
                      onDraftChange={changeSideDraft}
                      onComposingChange={setComposing}
                      autoFocus={sidePicked}
                      onBack={() => {
                        setSidePicked(false)
                        setAgentsSide(null)
                        // Back to the teammate it was with, or, away from
                        // their teamspace now, to the tab.
                        if (offersTeammates) setSideReturnTo(agentsSide.peerId)
                        else tabRefs.current.agents?.focus()
                      }}
                    />
                  )}
                <div
                  className="workspace-agents-home"
                  hidden={Boolean(agentsSide)}
                >
                  <AgentsView
                    organizationId={organizationId}
                    scope={scope}
                    chatAgent={chatAgent}
                    agents={agents.data}
                    agentId={agentId}
                    onSelectAgent={setAgentId}
                    chat={chat}
                    dispatch={dispatch}
                    agentName={agentName}
                    agentSeed={agentSeed}
                    onComposingChange={(value) => {
                      setComposing(value)
                      if (primaryWork) onPrimaryComposingChange?.(value)
                    }}
                    inAgentOverview={primaryWork}
                    onOpenAgentOverview={onOpenAgentOverview}
                    onPrimaryFileCreated={
                      agentsVisible && !agentId
                        ? onPrimaryFileCreated
                        : undefined
                    }
                    getScreenContext={getScreenContext}
                    teammates={
                      offersTeammates && directMessages ? (
                        <TeamspaceTeammates
                          space={
                            space.data?.id === teamspaceId
                              ? space.data
                              : undefined
                          }
                          failed={space.isError}
                          viewerId={directMessages.viewerId}
                          nameOf={(userId) =>
                            directMessages.people.find(userId)?.name
                          }
                          unreadOf={(peerId) => {
                            const name = teamspaceId
                              ? sideName(teamspaceId, peerId)
                              : undefined
                            return (
                              directMessages.inbox.conversations.find(
                                (conversation) => conversation.name === name
                              )?.unread ?? 0
                            )
                          }}
                          onOpen={openTeammate}
                          focusPeerId={agentsSide ? null : sideReturnTo}
                          onFocused={() => setSideReturnTo(null)}
                        />
                      ) : undefined
                    }
                  />
                </div>
              </>
            ) : item.key === "dms" && organizationId && directMessages ? (
              // Mounted only while it shows: an open conversation is what
              // makes the member online in it (REALTIME.md, "Presence").
              activeTab === "dms" &&
              expanded && (
                <DirectMessagesTab
                  organizationId={organizationId}
                  viewerId={directMessages.viewerId}
                  inbox={directMessages.inbox}
                  people={directMessages.people}
                  view={dmView}
                  onViewChange={setDmView}
                  drafts={dmDrafts}
                  onDraftChange={(peerId, draft) =>
                    setDmDrafts((current) => ({ ...current, [peerId]: draft }))
                  }
                  sideDrafts={sideDrafts}
                  onSideDraftChange={changeSideDraft}
                  onComposingChange={setComposing}
                />
              )
            ) : (
              <p className="workspace-agent-empty">{item.empty}</p>
            )}
          </div>
        ))}
      {!expanded && (
        <div className="workspace-right-rail">
          {visibleTabs.map((item) => {
            const Icon = item.icon
            return (
              <button
                className="workspace-rail-button"
                type="button"
                key={item.key}
                aria-label={`Open ${tabLabel(item.key, item.label, unread)}`}
                title={item.label}
                onClick={() => openTab(item.key)}
              >
                {Icon ? (
                  <Icon size={18} strokeWidth={1.7} />
                ) : (
                  <IdentityAvatar identity={tabIcon} />
                )}
                {item.key === "dms" && unread > 0 && (
                  <UnreadBadge count={unread} />
                )}
              </button>
            )
          })}
        </div>
      )}
    </aside>
  )
}

/** A tab's name, with the DMs' unread total when there is one. */
function tabLabel(key: AgentPanelTab, label: string, unread: number): string {
  return key === "dms" && unread > 0 ? `${label}, ${unread} unread` : label
}

/** The DMs' unread total on their tab and rail icon: a small badge, "9+" past nine. */
function UnreadBadge({ count }: { count: number }) {
  return (
    <span className="workspace-unread-badge" aria-hidden="true">
      {countLabel(count, 9)}
    </span>
  )
}

/**
 * A side conversation opened from the Agents tab's teammates: the teammate
 * as the members list names them (the teamspace's name for them until it
 * can), with the way back to the tab.
 */
function AgentsTabSide({
  organizationId,
  directMessages,
  open,
  name,
  teamspaceName,
  drafts,
  onDraftChange,
  onComposingChange,
  autoFocus,
  onBack,
}: {
  organizationId: string
  directMessages: PanelDirectMessages
  open: OpenSide
  /** The conversation's object name, the key of its draft. */
  name: string | undefined
  teamspaceName?: string
  drafts: Partial<Record<string, SideDraft>>
  onDraftChange: (name: string, draft: SideDraft) => void
  onComposingChange: (composing: boolean) => void
  autoFocus: boolean
  onBack: () => void
}) {
  const known = directMessages.people.find(open.peerId)?.name
  const peerName = known ?? open.peerName
  return (
    <SideConversationView
      organizationId={organizationId}
      viewerId={directMessages.viewerId}
      teamspaceId={open.teamspaceId}
      peer={{ id: open.peerId, name: peerName, short: firstName(peerName) }}
      teamspaceName={teamspaceName}
      draft={(name ? drafts[name] : undefined) ?? emptySideDraft}
      onDraftChange={(draft) => {
        if (name) onDraftChange(name, draft)
      }}
      onComposingChange={onComposingChange}
      autoFocus={autoFocus}
      backLabel="Back to agents"
      onBack={onBack}
    />
  )
}

/**
 * The Agents tab: the quick switch between the member's own agents (their
 * primary agent and their active specialists, DESIGN.md §2), on a teamspace
 * page that teamspace's teammates (`teammates`), and the chat with the one
 * in use: the primary agent's thread chat, or a specialist's chat object
 * (REALTIME.md).
 */
function AgentsView({
  organizationId,
  scope,
  chatAgent,
  agents,
  agentId,
  onSelectAgent,
  chat,
  dispatch,
  agentName,
  agentSeed,
  onComposingChange,
  teammates,
  inAgentOverview,
  onOpenAgentOverview,
  onPrimaryFileCreated,
  getScreenContext,
}: {
  organizationId?: string
  scope?: ChatScope
  /** The primary agent's id once known: what its chat's remembered choices are kept under. */
  chatAgent?: string
  /** The member's agents, once read. */
  agents?: ManagedAgent[]
  /** The specialist in use, or null for the primary agent. */
  agentId: string | null
  onSelectAgent: (agentId: string | null) => void
  /** What the primary chat holds, and how it moves (chat-choice.ts). */
  chat: PrimaryChat
  dispatch: Dispatch<PrimaryChatAction>
  agentName?: string
  agentSeed: string
  onComposingChange: (composing: boolean) => void
  /** On a teamspace page: its teammates, below the switch. */
  teammates?: ReactNode
  inAgentOverview?: boolean
  onOpenAgentOverview?: () => void
  onPrimaryFileCreated?: (file: {
    threadId: string
    name: string
    version?: number
  }) => void
  getScreenContext?: () => ScreenContext
}) {
  const specialists = agents ? specialistAgents(agents) : []
  const selectedId = agentId === agentSeed ? null : agentId
  const listed = selectedId
    ? specialists.find((agent) => agent.id === selectedId)
    : undefined
  // The specialist in use as last listed, so one paused or deleted
  // elsewhere keeps its name and place while its chat says what happened.
  const [lastListed, setLastListed] = useState<ManagedAgent>()
  if (listed && listed !== lastListed) setLastListed(listed)
  const inUse: ChatSpecialist | undefined = selectedId
    ? (listed ??
      (lastListed?.id === selectedId
        ? lastListed
        : { id: selectedId, name: "Specialist", status: "active" }))
    : undefined
  const entries: AgentSwitcherEntry[] = [
    { id: agentSeed, name: agentName, avatarSeed: agentSeed },
    ...specialists
      .filter((agent) => agent.status === "active" || agent.id === selectedId)
      .map((agent) => ({
        id: agent.id,
        name: agent.name,
        avatarSeed: agent.id,
      })),
    ...(inUse && !listed
      ? [{ id: inUse.id, name: inUse.name, avatarSeed: inUse.id }]
      : []),
  ]

  return (
    <>
      <AgentSwitcher
        agents={entries}
        selectedId={selectedId ?? agentSeed}
        onSelect={(id) => onSelectAgent(id === agentSeed ? null : id)}
      />
      {teammates}
      {organizationId && inUse ? (
        <SpecialistChat
          key={inUse.id}
          layout="panel"
          organizationId={organizationId}
          specialist={inUse}
          missing={agents !== undefined && !listed}
          draft={chat.drafts[inUse.id] ?? ""}
          onDraftChange={(text) =>
            dispatch({ type: "draft", key: inUse.id, text })
          }
          onComposingChange={onComposingChange}
          getScreenContext={getScreenContext}
        />
      ) : organizationId ? (
        <AgentChat
          organizationId={organizationId}
          scope={scope}
          agentId={chatAgent}
          agentName={agentName}
          chat={chat}
          dispatch={dispatch}
          onComposingChange={onComposingChange}
          inOverview={inAgentOverview}
          onOpenOverview={onOpenAgentOverview}
          onFileCreated={onPrimaryFileCreated}
          getScreenContext={getScreenContext}
        />
      ) : (
        <OfflineChat
          agentName={agentName}
          onComposingChange={onComposingChange}
        />
      )}
    </>
  )
}

/** No signed-in organization (the development preview): nothing to chat with. */
function OfflineChat({
  agentName,
  onComposingChange,
}: {
  agentName?: string
  onComposingChange: (composing: boolean) => void
}) {
  const [draft, setDraft] = useState("")
  const composerLabel = agentName
    ? `Message ${agentName}`
    : "Message your agent"

  return (
    <>
      <AgentConversation agentName={agentName} state="empty" />
      <AgentComposer
        label={composerLabel}
        placeholder={composerLabel}
        value={draft}
        onChange={setDraft}
        disabled
        status="Chat isn't connected yet."
        onFocus={() => onComposingChange(true)}
        onBlur={() => onComposingChange(false)}
      />
    </>
  )
}
