// A multi-agent team's conversation, behind one small seam. Today it reads
// Go's team room every 5 s and posts over HTTP, where a post answers only
// once the agents have replied (up to about 90 s). REALTIME.md makes every
// conversation a Durable Object; when the team room's object exists it
// replaces what this hook does inside, and the room's UI keeps this shape.
import { useCallback } from "react"
import {
  useMutation,
  useMutationState,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import { agentsErrorMessage } from "@/components/agents-format"
import { ApiError } from "@/lib/api/client"
import { postAgentTeamMessage } from "@/lib/agents/agent-teams"
import type { AgentTeamMessage } from "@/lib/agents/agent-teams"
import { afterPostAgentTeamMessage } from "@/lib/agents/invalidation"
import { agentKeys, agentTeamMessagesQueryOptions } from "@/lib/agents/queries"

/** The longest line a team room takes (agentteamrooms.maxBodyRunes). */
export const AGENT_TEAM_MESSAGE_MAX_RUNES = 4000

export type AgentTeamRoom = {
  /** The conversation, oldest first (its latest 200 lines). */
  messages: AgentTeamMessage[]
  /** The first read has not answered yet. */
  loading: boolean
  /** Why the conversation cannot be shown. */
  readError?: string
  /** The member's line while the team answers it. */
  sending?: string
  /** Whether a read already shows that line: the room stores it first. */
  sendingStored: boolean
  /** Why the last line did not go through. */
  sendError?: string
  /** Posts one line; false when it was refused or failed. */
  send: (body: string) => Promise<boolean>
  /** Forgets the last failure, once the member moves on from it. */
  clearSendError: () => void
  /** Reads the conversation again. */
  reload: () => void
}

type Post = { body: string; afterSeq: number }

function postKey(organizationId: string, teamId: string) {
  return [...agentKeys.teamMessages(organizationId, teamId), "post"] as const
}

export function useAgentTeamRoom(
  organizationId: string,
  teamId: string
): AgentTeamRoom {
  const queryClient = useQueryClient()
  const read = useQuery({
    ...agentTeamMessagesQueryOptions(organizationId, teamId),
    enabled: Boolean(organizationId && teamId),
  })
  const mutationKey = postKey(organizationId, teamId)
  const post = useMutation({
    mutationKey,
    mutationFn: ({ body }: Post) =>
      postAgentTeamMessage(organizationId, teamId, body),
    onSuccess: (added) =>
      afterPostAgentTeamMessage(queryClient, organizationId, teamId, added),
  })
  // Pending posts from any mount, so leaving the room and coming back while
  // the team is still answering keeps showing it.
  const inFlight = useMutationState({
    filters: { mutationKey, status: "pending" },
    select: (mutation) => mutation.state.variables as Post | undefined,
  }).at(-1)

  const messages = read.data ?? []
  const lastSeq = messages.at(-1)?.seq ?? 0
  const gone = read.error instanceof ApiError && read.error.status === 404
  const readError =
    read.error && (!read.data || gone)
      ? agentsErrorMessage(read.error, "Could not load this conversation.", {
          not_found: "This team no longer exists.",
          unavailable_here: "Team conversations aren't set up on this server.",
        })
      : undefined
  const sendError = post.isError
    ? post.error instanceof ApiError
      ? agentsErrorMessage(
          post.error,
          "The team could not take that message.",
          {
            team_disabled: "This team is paused. Resume it to send messages.",
            not_found: "This team no longer exists.",
            unavailable_here:
              "Team conversations aren't set up on this server.",
          }
        )
      : // The connection dropped: the room may still have stored the line.
        "The connection dropped. Check the conversation before sending it again."
    : undefined

  const { mutateAsync, reset } = post
  const send = useCallback(
    async (body: string) => {
      try {
        await mutateAsync({ body, afterSeq: lastSeq })
        return true
      } catch {
        return false
      }
    },
    [mutateAsync, lastSeq]
  )
  const { refetch } = read
  const reload = useCallback(() => void refetch(), [refetch])

  return {
    messages,
    loading: read.isPending && read.fetchStatus !== "idle",
    readError,
    sending: inFlight?.body,
    sendingStored: Boolean(
      inFlight &&
      messages.some(
        (message) =>
          message.kind === "member" &&
          message.seq > inFlight.afterSeq &&
          message.body === inFlight.body
      )
    ),
    sendError,
    send,
    clearSendError: reset,
    reload,
  }
}
