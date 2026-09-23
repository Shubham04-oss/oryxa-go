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
import { IdentityAvatar } from "./workspace-avatar"
import type { AvatarIdentity } from "./workspace-avatar"
import { useVisibleHeight } from "@/hooks/use-visible-height"
import { primaryAgent } from "@/lib/agents/policy"
import { agentsQueryOptions } from "@/lib/agents/queries"
import { cn } from "@/lib/utils"

type AgentPanelTab = "agents" | "dms" | "teams"

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
}: {
  expanded: boolean
  onExpand: () => void
  onCollapse: () => void
  /** The signed-in member's organization; without one the chat stays off. */
  organizationId?: string
}) {
  const [tab, setTab] = useState<AgentPanelTab>("agents")
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
                  aria-label={item.label}
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
                </button>
              )
            })}
          </div>
        )}
        <button
          className="workspace-icon-button"
          type="button"
          aria-label={expanded ? "Collapse agent panel" : "Expand agent panel"}
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
                agentName={agentName}
                agentSeed={agentSeed}
                onComposingChange={setComposing}
              />
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
                aria-label={`Open ${item.label}`}
                title={item.label}
                onClick={() => openTab(item.key)}
              >
                {Icon ? (
                  <Icon size={18} strokeWidth={1.7} />
                ) : (
                  <IdentityAvatar identity={tabIcon} />
                )}
              </button>
            )
          })}
        </div>
      )}
    </aside>
  )
}

function AgentsView({
  organizationId,
  agentName,
  agentSeed,
  onComposingChange,
}: {
  organizationId?: string
  agentName?: string
  agentSeed: string
  onComposingChange: (composing: boolean) => void
}) {
  return (
    <>
      {/* Only agents whose chat works are offered: the primary agent, until
          specialists have their own chat object (REALTIME.md), so the row
          stays hidden. */}
      <AgentSwitcher
        agents={[{ id: agentSeed, name: agentName, avatarSeed: agentSeed }]}
        selectedId={agentSeed}
        onSelect={() => {}}
      />
      {organizationId ? (
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
