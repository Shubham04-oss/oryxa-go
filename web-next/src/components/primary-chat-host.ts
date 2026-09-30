import { useEffect, useMemo, useReducer, useRef, useState } from "react"
import type { Dispatch } from "react"
import { useQuery } from "@tanstack/react-query"
import { agentIdentity } from "./agents-format"
import type { AvatarIdentity } from "./workspace-avatar"
import { primaryAgent } from "@/lib/agents/policy"
import { agentsQueryOptions } from "@/lib/agents/queries"
import type { ManagedAgent } from "@/lib/agents/managed-agents"
import type { ScreenContext } from "@/lib/agents/screen-context"
import {
  claimPrimaryChatRequest,
  initialPrimaryChat,
  primaryChatReducer,
  usePrimaryChatRequest,
} from "@/lib/threads/chat-choice"
import type {
  ChatScope,
  PrimaryChat,
  PrimaryChatAction,
} from "@/lib/threads/chat-choice"

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
export function ownAgentsQueryOptions(
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

/** What the shell holds for the member's primary chat, whichever screen the center shows. */
export type PrimaryChatHost = {
  /** Whose choices the chat remembers: this member, in this organization. */
  scope?: ChatScope
  /** The same, as one key: the chat is mounted afresh for another owner. */
  owner?: string
  /** The conversation the chat is on, what waits, the draft and the unsent messages. */
  chat: PrimaryChat
  dispatch: Dispatch<PrimaryChatAction>
  /** The member's primary agent, once their agents have loaded. */
  primary?: ManagedAgent
  /** Its name, once known; never a stand-in. */
  agentName?: string
  /**
   * What keys the chat's remembered choices: the agent's id once the agents
   * list has answered (a stand-in only when it could not), undefined before.
   */
  agentId?: string
  /** The primary agent's voxel avatar. */
  identity: AvatarIdentity
}

/**
 * The member's primary chat, held by the shell so it outlasts whatever the
 * center shows (DESIGN.md §2, §3): the conversation it is on, the requests
 * and first messages waiting their turn, the unsent messages and the draft,
 * in one reducer (`primaryChatReducer`). It takes the rest of the app's
 * requests to open a conversation (`requestPrimaryChat`) from their queue,
 * oldest first, once the member's agents have loaded, and forgets everything
 * when another member takes the page over.
 */
export function usePrimaryChatHost({
  organizationId,
  viewerId,
  getScreenContext,
}: {
  /** The signed-in member's organization; without one the chat stays off. */
  organizationId?: string
  /** The signed-in member: whose choices the chat keeps, and whose agents it reads. */
  viewerId?: string
  /** What the member is looking at, sent with a message a request brings. */
  getScreenContext?: () => ScreenContext
}): PrimaryChatHost {
  const [chat, dispatch] = useReducer(
    primaryChatReducer,
    undefined,
    initialPrimaryChat
  )
  const agents = useQuery({
    ...ownAgentsQueryOptions(organizationId ?? "", viewerId),
    enabled: Boolean(organizationId),
  })
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
    if (ownedBy) dispatch({ type: "reset" })
  }
  const primary = agents.data ? primaryAgent(agents.data) : undefined
  const agentSeed = primary?.id ?? PRIMARY_AGENT_SEED
  // Nothing is chosen or remembered before the agents list answers, so no
  // choice sits under a key that changes.
  const agentId = agents.isPending ? undefined : agentSeed
  const identity = agentIdentity({
    id: agentSeed,
    name: primary?.name ?? FALLBACK_AGENT_NAME,
  })

  // The rest of the app asking the primary chat to open a conversation (a
  // link to one, for instance). Its requests wait in a queue, and this takes
  // them oldest first, one at a time, once the member's agents have loaded,
  // however many were made meanwhile or before the shell mounted. Each is
  // acted on once: claiming it outlasts this shell. What it asks for is held
  // in the reducer, in order, so a request made while another's message is on
  // its way waits its turn.
  const request = usePrimaryChatRequest(scope)
  const screen = useRef(getScreenContext)
  useEffect(() => {
    screen.current = getScreenContext
  })
  useEffect(() => {
    if (!request || !scope || !agentId) return
    if (!claimPrimaryChatRequest(request.id)) return
    const context = screen.current?.()
    dispatch(
      request.refused
        ? {
            type: "refused",
            request,
            notice: TOO_MANY_WAITING,
            screen: context,
          }
        : { type: "request", request, screen: context }
    )
  }, [request, scope, agentId])

  return {
    scope,
    owner,
    chat,
    dispatch,
    primary,
    agentName: primary?.name,
    agentId,
    identity,
  }
}
