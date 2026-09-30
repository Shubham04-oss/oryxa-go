import { useEffect, useMemo, useReducer, useRef, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  ArrowLeft,
  ArrowRight,
  PanelRightClose,
  PanelRightOpen,
} from "lucide-react"
import { AgentChat } from "./agent-chat"
import { AgentComposer } from "./agent-composer"
import { AgentConversation } from "./agent-conversation"
import { agentIdentity } from "./agents-format"
import { IdentityAvatar } from "./workspace-avatar"
import { useVisibleHeight } from "@/hooks/use-visible-height"
import { primaryAgent } from "@/lib/agents/policy"
import { agentsQueryOptions } from "@/lib/agents/queries"
import {
  claimPrimaryChatRequest,
  initialPrimaryChat,
  primaryChatReducer,
  shownThread,
  usePrimaryChatRequest,
} from "@/lib/threads/chat-choice"
import type { ChatScope } from "@/lib/threads/chat-choice"
import type { ScreenContext } from "@/lib/agents/screen-context"

/**
 * The shell asking the panel to open the primary agent's conversation
 * history (Home's "All conversations"). Each request is a new object, and
 * each is honoured once.
 */
export type AgentPanelHistoryRequest = { kind: "history" }

/** Avatar seed for the primary agent until its id has loaded. */
const PRIMARY_AGENT_SEED = "primary-agent"

/** Accessible name for the primary agent until its real name has loaded. */
const FALLBACK_AGENT_NAME = "Primary agent"

/** Why a request's message was not sent: the chat was already holding as many as it takes. */
const TOO_MANY_WAITING = "Too many messages were waiting."

/**
 * The member's own agents. The entry is keyed by member as well as by
 * organization, so a member who takes over the page another one used (same
 * organization, same cache) never reads the last one's agents: their names
 * and the primary agent's id the chat keeps its choices under. The key still
 * starts with `agentKeys.agentList`, so what invalidates that list reaches
 * it.
 */
function ownAgentsQueryOptions(
  organizationId: string,
  viewerId: string | undefined
) {
  const options = agentsQueryOptions(organizationId)
  return {
    ...options,
    // Its query function reads no key, so the extra part only names the entry.
    queryKey: [
      ...options.queryKey,
      viewerId ?? "",
    ] as unknown as typeof options.queryKey,
  }
}

/**
 * The agent panel: the member's primary agent, its chat and the history of
 * its conversations (DESIGN.md §2, §3). Expanded, it is the right-hand
 * column; collapsed, a rail with one button to open it. While the Agent
 * overview is the focused view, the same mounted chat sits in that view's
 * conversation column and the panel offers nothing else.
 */
export function WorkspaceAgentPanel({
  expanded,
  onExpand,
  onCollapse,
  organizationId,
  viewerId,
  historyRequest,
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
  /** The signed-in member: whose choices the chat keeps, and whose agents it reads. */
  viewerId?: string
  /** The latest request to open the conversation history; the shell expands the panel with it. */
  historyRequest?: AgentPanelHistoryRequest | null
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
  // A collapsed panel keeps its primary chat alive until its run finishes.
  const hasOpened = useRef(false)
  // Agent overview moves this mounted conversation into its left column.
  // Opening a file can enter that view before the side panel was ever opened.
  if (expanded || primaryWork) hasOpened.current = true
  // What the primary chat holds: the conversation it is on, what waits to be
  // opened or sent, and the unsent message (chat-choice.ts).
  const [chat, dispatch] = useReducer(
    primaryChatReducer,
    undefined,
    initialPrimaryChat
  )
  // A request for the history is honoured once: the chat is told to open it,
  // and says when it has.
  const [answeredHistoryRequest, setAnsweredHistoryRequest] =
    useState<AgentPanelHistoryRequest | null>(null)
  const [openHistoryRequest, setOpenHistoryRequest] =
    useState<AgentPanelHistoryRequest | null>(null)
  if (historyRequest && historyRequest !== answeredHistoryRequest) {
    setAnsweredHistoryRequest(historyRequest)
    setOpenHistoryRequest(historyRequest)
  }
  const [composing, setComposing] = useState(false)
  const visibleHeight = useVisibleHeight(expanded && composing)
  const toggleRef = useRef<HTMLButtonElement>(null)
  const focusToggleOnExpand = useRef(false)
  const agents = useQuery({
    ...ownAgentsQueryOptions(organizationId ?? "", viewerId),
    enabled: Boolean(organizationId),
  })
  // Whose choices the primary chat remembers for the session: this member's,
  // in this organization.
  const scope = useMemo<ChatScope | undefined>(
    () =>
      organizationId && viewerId
        ? { organizationId, userId: viewerId }
        : undefined,
    [organizationId, viewerId]
  )
  // Whose page this is. Another member taking it over without a reload
  // (another tab signed in as someone else, and this one read the session
  // again) inherits nothing the last one held here: not the chat's
  // conversation or its drafts. It is all reset in the render that first sees
  // the new member, so no render shows the old one's. The same member seen
  // again after the session was briefly unreadable keeps theirs.
  const owner = scope ? `${scope.organizationId}~${scope.userId}` : undefined
  const [ownedBy, setOwnedBy] = useState(owner)
  if (owner && owner !== ownedBy) {
    setOwnedBy(owner)
    if (ownedBy) {
      dispatch({ type: "reset" })
      setOpenHistoryRequest(null)
    }
  }
  const primary = agents.data ? primaryAgent(agents.data) : undefined
  const agentName = primary?.name
  const agentSeed = primary?.id ?? PRIMARY_AGENT_SEED
  // What keys the primary chat's remembered choices: the agent's id, known
  // once the agents list has answered (a stand-in only when it could not).
  // Nothing is chosen or remembered before, so no choice sits under a key
  // that changes.
  const chatAgent = agents.isPending ? undefined : agentSeed
  // The panel's identity is always the primary agent's voxel avatar, with
  // "Primary agent" as an accessible-name fallback only (never shown as text).
  const identity = agentIdentity({
    id: agentSeed,
    name: agentName ?? FALLBACK_AGENT_NAME,
  })

  // Whether the chat is in front of the member: the expanded panel, or the
  // conversation column of the focused Agent overview. A file saved while it
  // isn't stays a chip in the chat: opening it would close the view the
  // member is in.
  const chatVisible = Boolean(primaryWork) || expanded

  // The shell follows the primary chat's conversation: Agent overview lists
  // its files.
  const shown = shownThread(chat)
  useEffect(() => {
    // Until the chat has a conversation it has none: the overview lists no
    // files, and never the last member's.
    onPrimaryThreadChange?.(shown ?? null)
  }, [shown, onPrimaryThreadChange])

  // The rest of the app asking the primary chat to open a conversation (Home
  // does, then expands the panel). Its requests wait in a queue, and this
  // takes them oldest first, one at a time, once the member's agents have
  // loaded, however many were made meanwhile or before the panel mounted.
  // Each is acted on once: claiming it outlasts this panel. What it asks for
  // is held here, in order, not in the chat, so a request made before the chat
  // first mounted is still applied, and one made while another's message is on
  // its way waits its turn.
  const request = usePrimaryChatRequest(scope)
  useEffect(() => {
    if (!request || !scope || !chatAgent) return
    if (!claimPrimaryChatRequest(request.id)) return
    const screen = getScreenContext?.()
    dispatch(
      request.refused
        ? { type: "refused", request, notice: TOO_MANY_WAITING, screen }
        : { type: "request", request, screen }
    )
  }, [request, scope, chatAgent])

  // A rail button unmounts as the panel opens, so its focus moves to the
  // panel's toggle rather than falling back to the page.
  useEffect(() => {
    if (!expanded || !focusToggleOnExpand.current) return
    focusToggleOnExpand.current = false
    toggleRef.current?.focus()
  }, [expanded])

  // The history asked for elsewhere (Home's "All conversations") brings the
  // panel forward, and focus with it, as the rail's button does.
  useEffect(() => {
    if (historyRequest) toggleRef.current?.focus()
  }, [historyRequest])

  function openFromRail() {
    focusToggleOnExpand.current = true
    onExpand()
  }

  /** The field is in use or not: the panel fits the visible height, and the focused Agent overview hears of it. */
  function reportComposing(value: boolean) {
    setComposing(value)
    if (primaryWork) onPrimaryComposingChange?.(value)
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
      {!primaryWork && (
        <div className="workspace-right-top">
          {expanded && (
            <div className="workspace-switcher">
              <span className="workspace-switcher-item is-selected">
                <IdentityAvatar identity={identity} />
                {agentName && <span>{agentName}</span>}
              </span>
            </div>
          )}
          <button
            className="workspace-icon-button"
            type="button"
            ref={toggleRef}
            aria-label={
              expanded ? "Collapse agent panel" : "Expand agent panel"
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
              </>
            )}
          </button>
        </div>
      )}

      {hasOpened.current && (
        <div
          className="workspace-agent-view"
          role="region"
          aria-label="Agent chat"
          data-primary-chat={primaryWork ? "true" : undefined}
          tabIndex={0}
          hidden={primaryWork ? false : !expanded}
        >
          {organizationId ? (
            <AgentChat
              openHistoryRequest={openHistoryRequest}
              onHistoryOpened={() => setOpenHistoryRequest(null)}
              key={owner}
              organizationId={organizationId}
              scope={scope}
              visible={chatVisible}
              agentId={chatAgent}
              agentName={agentName}
              chat={chat}
              dispatch={dispatch}
              onComposingChange={reportComposing}
              inOverview={primaryWork}
              onOpenOverview={onOpenAgentOverview}
              onFileCreated={chatVisible ? onPrimaryFileCreated : undefined}
              getScreenContext={getScreenContext}
            />
          ) : (
            <OfflineChat
              agentName={agentName}
              onComposingChange={reportComposing}
            />
          )}
        </div>
      )}
      {!expanded && !primaryWork && (
        <div className="workspace-right-rail">
          <button
            className="workspace-rail-button"
            type="button"
            aria-label="Open agent chat"
            title="Agent chat"
            onClick={openFromRail}
          >
            <IdentityAvatar identity={identity} />
          </button>
        </div>
      )}
    </aside>
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
