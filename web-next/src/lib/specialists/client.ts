import { queryOptions } from "@tanstack/react-query"
import { apiRequest } from "@/lib/api/client"
import {
  retryUnlessRefused,
  runRequestBody,
  streamRunRequest,
} from "@/lib/threads/client"
import type {
  RunInput,
  StartRunOptions,
  Thread,
  ThreadDetail,
} from "@/lib/threads/client"
import type { RunResult } from "@/lib/threads/event-stream"

// A specialist is one of the member's persistent agents (managed agents in
// Go). Each has its own conversations, kept apart from the primary agent's
// threads: agents/primary/internal/managedagents/service.go runs them on the
// same thread engine under the agent's own session namespace, so they are
// the same `Thread`, `ThreadDetail` and `Item` as lib/threads.

const agentPath = (organizationId: string, agentId: string) =>
  `/v1/organizations/${encodeURIComponent(organizationId)}/agents/${encodeURIComponent(agentId)}`

/** GET /v1/organizations/{org}/agents/{agent}/threads, most recently active first. agents/primary/internal/httpserver/server.go listAgentThreads */
export async function listSpecialistThreads(
  organizationId: string,
  agentId: string,
  { signal }: { signal?: AbortSignal } = {}
): Promise<Thread[]> {
  const { threads } = await apiRequest<{ threads: Thread[] }>(
    `${agentPath(organizationId, agentId)}/threads`,
    { signal }
  )
  return threads
}

/** GET /v1/organizations/{org}/agents/{agent}/threads/{thread}, with its history. agents/primary/internal/httpserver/server.go getAgentThread */
export function getSpecialistThread(
  organizationId: string,
  agentId: string,
  threadId: string,
  { signal }: { signal?: AbortSignal } = {}
): Promise<ThreadDetail> {
  return apiRequest<ThreadDetail>(
    `${agentPath(organizationId, agentId)}/threads/${encodeURIComponent(threadId)}`,
    { signal }
  )
}

/** A specialist run's body: a thread run's, plus the conversation it continues. */
export type SpecialistRunInput = RunInput & {
  /** The conversation to continue. Leave it out to start a new one; Go allocates it and names it in `X-Oryxa-Thread`. */
  thread_id?: string
}

export type StartSpecialistRunOptions = StartRunOptions & {
  /** Called once, before any item, with the conversation this run belongs to. */
  onThread?: (threadId: string) => void
}

export type SpecialistRunResult = RunResult & {
  /**
   * The conversation the run belongs to: Go's `X-Oryxa-Thread`, or the
   * `thread_id` sent. Missing only when a new conversation's run was aborted
   * before Go answered, or a proxy dropped the header.
   */
  threadId?: string
}

/**
 * POST /v1/organizations/{org}/agents/{agent}/runs, reading the answer as it
 * streams. agents/primary/internal/httpserver/server.go runAgent
 *
 * The same stream, results and errors as `startRun` (lib/threads), plus
 * `agent_disabled` (409) for a paused specialist and `not_found` for an agent
 * or conversation that is not the member's. The input is checked before
 * sending: without `thread_id`, Go allocates the conversation before it reads
 * the input, so an empty message would leave an empty conversation behind.
 *
 * A specialist answers with its JSON work result as the message text (see
 * `parseSpecialistResult`), so its deltas stream raw JSON.
 */
export async function startSpecialistRun(
  organizationId: string,
  agentId: string,
  input: SpecialistRunInput,
  { onThread, ...options }: StartSpecialistRunOptions
): Promise<SpecialistRunResult> {
  const body = runRequestBody(input)
  let threadId = input.thread_id || undefined
  const result = await streamRunRequest(
    `${agentPath(organizationId, agentId)}/runs`,
    threadId ? { thread_id: threadId, ...body } : body,
    {
      ...options,
      onResponse: (response) => {
        threadId = response.headers.get("X-Oryxa-Thread") || threadId
        if (threadId) onThread?.(threadId)
      },
    }
  )
  return { ...result, threadId }
}

/**
 * Query keys, organization-scoped and per specialist. The list and a
 * conversation do not prefix each other; `agent` covers both.
 */
export const specialistKeys = {
  all: (organizationId: string) => ["specialists", organizationId] as const,
  agent: (organizationId: string, agentId: string) =>
    ["specialists", organizationId, agentId] as const,
  threads: (organizationId: string, agentId: string) =>
    ["specialists", organizationId, agentId, "threads"] as const,
  thread: (organizationId: string, agentId: string, threadId: string) =>
    ["specialists", organizationId, agentId, "thread", threadId] as const,
}

export function specialistThreadsQueryOptions(
  organizationId: string,
  agentId: string
) {
  return queryOptions({
    queryKey: specialistKeys.threads(organizationId, agentId),
    queryFn: ({ signal }) =>
      listSpecialistThreads(organizationId, agentId, { signal }),
    retry: retryUnlessRefused,
  })
}

export function specialistThreadQueryOptions(
  organizationId: string,
  agentId: string,
  threadId: string
) {
  return queryOptions({
    queryKey: specialistKeys.thread(organizationId, agentId, threadId),
    queryFn: ({ signal }) =>
      getSpecialistThread(organizationId, agentId, threadId, { signal }),
    retry: retryUnlessRefused,
  })
}
