import { useEffect, useRef, useState } from "react"
import type { ComponentType, KeyboardEvent } from "react"
import { useQuery } from "@tanstack/react-query"
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
import { countLabel } from "./direct-message-format"
import { DirectMessagesTab, dmPanelList } from "./direct-message-inbox"
import type { DmPanelView } from "./direct-message-inbox"
import type { DmPeople } from "./direct-message-people"
import { SpecialistChat } from "./specialist-chat"
import type { ChatSpecialist } from "./specialist-chat"
import { IdentityAvatar } from "./workspace-avatar"
import type { AvatarIdentity } from "./workspace-avatar"
import { useVisibleHeight } from "@/hooks/use-visible-height"
import type { ManagedAgent } from "@/lib/agents/managed-agents"
import { primaryAgent, specialistAgents } from "@/lib/agents/policy"
import { agentsQueryOptions } from "@/lib/agents/queries"
import type { Inbox } from "@/lib/dms"
import { cn } from "@/lib/utils"

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
  /** A side conversation picked from the list; the teamspace's side-conversation view wires in here. */
  onOpenSide: (name: string) => void
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
}) {
  const [tab, setTab] = useState<AgentPanelTab>("agents")
  // Whom the Agents tab talks to: one of the member's specialists by id,
  // or null for their primary agent.
  const [agentId, setAgentId] = useState<string | null>(null)
  const [answeredRequest, setAnsweredRequest] =
    useState<AgentPanelChatRequest | null>(null)
  if (chatRequest && chatRequest !== answeredRequest) {
    setAnsweredRequest(chatRequest)
    setTab("agents")
    setAgentId(chatRequest.agentId)
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
  const primary = agents.data ? primaryAgent(agents.data) : undefined
  const agentName = primary?.name
  const agentSeed = primary?.id ?? PRIMARY_AGENT_SEED
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

  // A rail button unmounts as the panel opens, so its focus moves to the tab
  // it opened rather than falling back to the page.
  useEffect(() => {
    if (!expanded || !focusTabOnExpand.current) return
    focusTabOnExpand.current = false
    tabRefs.current[tab]?.focus()
  }, [expanded, tab])

  // A chat asked for elsewhere (a team member's Chat button) brings the
  // Agents tab forward, and focus with it, as the rail's buttons do.
  useEffect(() => {
    if (chatRequest) tabRefs.current.agents?.focus()
  }, [chatRequest])
  useEffect(() => {
    if (tabRequest) tabRefs.current[tabRequest.tab]?.focus()
  }, [tabRequest])

  function openTab(next: AgentPanelTab) {
    setTab(next)
    focusTabOnExpand.current = true
    onExpand()
  }

  function moveBetweenTabs(
    event: KeyboardEvent<HTMLButtonElement>,
    index: number
  ) {
    const last = panelTabs.length - 1
    const moves: Partial<Record<string, number>> = {
      ArrowRight: index === last ? 0 : index + 1,
      ArrowLeft: index === 0 ? last : index - 1,
      Home: 0,
      End: last,
    }
    const next = moves[event.key]
    if (next === undefined) return
    event.preventDefault()
    setTab(panelTabs[next].key)
    tabRefs.current[panelTabs[next].key]?.focus()
  }

  return (
    <aside
      id="workspace-agent-panel"
      className="workspace-right"
      aria-label="Agent panel"
      style={visibleHeight ? { height: visibleHeight } : undefined}
    >
      <div className="workspace-right-top">
        {expanded && (
          <div
            className="workspace-switcher"
            role="tablist"
            aria-label="Agent panel views"
          >
            {panelTabs.map((item, index) => {
              const Icon = item.icon
              const selected = tab === item.key
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

      {expanded ? (
        panelTabs.map((item) => (
          <div
            className="workspace-agent-view"
            role="tabpanel"
            key={item.key}
            id={`workspace-agent-view-${item.key}`}
            aria-labelledby={`workspace-agent-tab-${item.key}`}
            tabIndex={0}
            hidden={tab !== item.key}
          >
            {item.key === "agents" ? (
              <AgentsView
                organizationId={organizationId}
                agents={agents.data}
                agentId={agentId}
                onSelectAgent={setAgentId}
                agentName={agentName}
                agentSeed={agentSeed}
                onComposingChange={setComposing}
              />
            ) : item.key === "dms" && organizationId && directMessages ? (
              // Mounted only while it shows: an open conversation is what
              // makes the member online in it (REALTIME.md, "Presence").
              tab === "dms" && (
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
                  onComposingChange={setComposing}
                  onOpenSide={directMessages.onOpenSide}
                />
              )
            ) : (
              <p className="workspace-agent-empty">{item.empty}</p>
            )}
          </div>
        ))
      ) : (
        <div className="workspace-right-rail">
          {panelTabs.map((item) => {
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
 * The Agents tab: the quick switch between the member's own agents (their
 * primary agent and their active specialists, DESIGN.md §2), and the chat
 * with the one in use: the primary agent's thread chat, or a specialist's
 * chat object (REALTIME.md).
 */
function AgentsView({
  organizationId,
  agents,
  agentId,
  onSelectAgent,
  agentName,
  agentSeed,
  onComposingChange,
}: {
  organizationId?: string
  /** The member's agents, once read. */
  agents?: ManagedAgent[]
  /** The specialist in use, or null for the primary agent. */
  agentId: string | null
  onSelectAgent: (agentId: string | null) => void
  agentName?: string
  agentSeed: string
  onComposingChange: (composing: boolean) => void
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
      {organizationId && inUse ? (
        <SpecialistChat
          key={inUse.id}
          layout="panel"
          organizationId={organizationId}
          specialist={inUse}
          missing={agents !== undefined && !listed}
          onComposingChange={onComposingChange}
        />
      ) : organizationId ? (
        <AgentChat
          organizationId={organizationId}
          agentName={agentName}
          onComposingChange={onComposingChange}
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
