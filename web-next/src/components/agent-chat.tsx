import { Suspense, useEffect, useMemo, useRef, useState } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import type { QueryClient } from "@tanstack/react-query"
import { useHydrated } from "@tanstack/react-router"
import { useAgentChat } from "@cloudflare/ai-chat/react"
import { useAgent } from "agents/react"
import { Expand, SquarePen } from "lucide-react"
import { AgentComposer } from "./agent-composer"
import { AgentActivity, AgentTimeline } from "./agent-timeline"
import { AgentConversation } from "./agent-conversation"
import { ApiError } from "@/lib/api/client"
import { getSession } from "@/lib/auth/client"
import { reopenSocket } from "@/lib/threads/agent-socket"
import {
  PRIMARY_CHAT_AGENT,
  openingThread,
  publishPrimaryChatActivity,
  rememberChoice,
  rememberedChoice,
} from "@/lib/threads/chat-choice"
import type { ChatScope, PrimaryChatActivity } from "@/lib/threads/chat-choice"
import {
  chatMessagesToItems,
  reconcileChatMessages,
  sessionExpiredCode,
  threadAgentName,
} from "@/lib/threads/chat-messages"
import type { ThreadChatMessage } from "@/lib/threads/chat-messages"
import {
  createThread,
  getThreadChatMessages,
  threadDetailQueryOptions,
  threadKeys,
  threadListQueryOptions,
} from "@/lib/threads/client"
import type { ConfirmationAnswer, Thread } from "@/lib/threads/client"
import { buildTimeline, pendingConfirmationIds } from "@/lib/threads/timeline"
import type { ArtifactEntry } from "@/lib/threads/timeline"
import { socketProtocol } from "@/lib/socket-protocol"
import type { ScreenContext } from "@/lib/agents/screen-context"

type ComposerHandlers = {
  /** The composer's text, kept here so it survives the conversation loading. */
  draft: string
  onDraftChange: (value: string) => void
  onComposingChange: (composing: boolean) => void
}

type ChatState = PrimaryChatActivity["state"]

function composerLabel(agentName?: string): string {
  return agentName ? `Message ${agentName}` : "Message your agent"
}

/** An API refusal's own words, or `fallback` for anything without them. */
export function failureText(error: unknown, fallback: string): string {
  return error instanceof ApiError && error.message !== "Request failed"
    ? error.message
    : fallback
}

/**
 * The stored transcript, with whatever Go holds that it lacks. It never
 * rejects: useAgentChat keeps this promise for the thread, and a rejected one
 * would be read again on every mount. What fails to load here shows through
 * the thread query instead, and the idle reconcile catches up.
 */
async function loadTranscript(
  queryClient: QueryClient,
  organizationId: string,
  threadId: string
): Promise<ThreadChatMessage[]> {
  const [stored, detail] = await Promise.all([
    getThreadChatMessages(organizationId, threadId).catch(
      (): ThreadChatMessage[] => []
    ),
    queryClient
      .fetchQuery(threadDetailQueryOptions(organizationId, threadId))
      .catch(() => undefined),
  ])
  return reconcileChatMessages(stored, detail?.items ?? [])
}

/**
 * Whether a conversation waits on the member's confirmation, from Go's
 * detail of it. One that can't be read counts as not waiting.
 */
async function waitsOnMember(
  queryClient: QueryClient,
  organizationId: string,
  threadId: string
): Promise<boolean> {
  const detail = await queryClient
    .fetchQuery(threadDetailQueryOptions(organizationId, threadId))
    .catch(() => undefined)
  return (detail?.pending_confirmations.length ?? 0) > 0
}

/**
 * The Agents tab's chat with the member's primary agent. It opens the
 * conversation the member chose in this browser session, or their latest one
 * while it is fresh or still waits on them, and otherwise a new one that
 * their first message starts (chat-choice.ts). It runs through the thread's
 * Durable Object (REALTIME.md), in the browser only.
 */
export function AgentChat({
  organizationId,
  scope,
  agentName,
  onComposingChange,
  onOpenOverview,
  inOverview = false,
  onThreadChange,
  onFileCreated,
  getScreenContext,
}: {
  organizationId: string
  /** Whose choices the chat remembers for the session; without it none are. */
  scope?: ChatScope
  agentName?: string
  onComposingChange: (composing: boolean) => void
  onOpenOverview?: () => void
  inOverview?: boolean
  onThreadChange?: (threadId: string | null) => void
  onFileCreated?: (file: {
    threadId: string
    name: string
    version?: number
  }) => void
  getScreenContext?: () => ScreenContext
}) {
  const queryClient = useQueryClient()
  const hydrated = useHydrated()
  const threads = useQuery({
    ...threadListQueryOptions(organizationId),
    enabled: hydrated,
  })
  // `undefined` until the rules in chat-choice.ts decide it; `null` for a
  // conversation not started yet; then the thread the tab stays on, whatever
  // the list does.
  const [threadId, setThreadId] = useState<string | null | undefined>()
  const [draft, setDraft] = useState("")
  // The first message of a new conversation, until its thread's socket
  // takes it.
  const [pending, setPending] = useState<string | null>(null)
  const [pendingScreen, setPendingScreen] = useState<ScreenContext>()
  const [starting, setStarting] = useState(false)
  const [notice, setNotice] = useState("")
  // What the open thread's chat says it is doing, and of which thread.
  const [reported, setReported] = useState<{
    threadId: string
    state: ChatState
  }>()

  // The rest of the app (Home) hears what this chat is doing: the thread it
  // has open and whether it works, waits on the member or is idle. A first
  // message counts as running until its thread's socket takes it.
  const state: ChatState =
    starting || pending !== null
      ? "running"
      : threadId && reported?.threadId === threadId
        ? reported.state
        : "idle"
  useEffect(() => {
    if (threadId !== undefined) publishPrimaryChatActivity({ threadId, state })
  }, [threadId, state])
  useEffect(() => () => publishPrimaryChatActivity(null), [])

  // Once the list is here: the member's choice, else the latest conversation
  // while it is fresh, else (after asking Go) one that still waits on them.
  useEffect(() => {
    if (threadId !== undefined || !threads.data) return
    let stale = false
    void openingThread({
      threads: threads.data,
      choice: scope ? rememberedChoice(scope, PRIMARY_CHAT_AGENT) : null,
      now: Date.now(),
      waiting: (id) => waitsOnMember(queryClient, organizationId, id),
    }).then((chosen) => {
      if (!stale) setThreadId(chosen)
    })
    return () => {
      stale = true
    }
  }, [threadId, threads.data])

  useEffect(() => {
    if (threadId !== undefined) onThreadChange?.(threadId)
  }, [threadId, onThreadChange])

  const composer: ComposerHandlers = {
    draft,
    onDraftChange: setDraft,
    onComposingChange,
  }
  const title =
    (threadId &&
      threads.data?.find((thread) => thread.id === threadId)?.title) ||
    "New conversation"

  async function start(message: string) {
    setStarting(true)
    setNotice("")
    setPending(message)
    setPendingScreen(getScreenContext?.())
    setDraft("")
    try {
      const thread = await createThread(organizationId)
      queryClient.setQueryData<Thread[]>(
        threadKeys.list(organizationId),
        (list) => [
          thread,
          ...(list ?? []).filter((item) => item.id !== thread.id),
        ]
      )
      void queryClient.invalidateQueries({
        queryKey: threadKeys.list(organizationId),
      })
      setThreadId(thread.id)
      if (scope) rememberChoice(scope, PRIMARY_CHAT_AGENT, thread.id)
    } catch (error) {
      setPending(null)
      setDraft(message)
      setNotice(failureText(error, "Couldn't start a conversation. Try again."))
    } finally {
      setStarting(false)
    }
  }

  function startNew() {
    setThreadId(null)
    setPending(null)
    setNotice("")
    if (scope) rememberChoice(scope, PRIMARY_CHAT_AGENT, null)
  }

  let body
  if (threadId) {
    body = (
      <Suspense
        fallback={
          <OpeningConversation
            agentName={agentName}
            pending={pending}
            {...composer}
          />
        }
      >
        <ThreadChat
          key={threadId}
          organizationId={organizationId}
          threadId={threadId}
          agentName={agentName}
          pending={pending}
          onPendingTaken={() => setPending(null)}
          pendingScreen={pendingScreen}
          getScreenContext={getScreenContext}
          onMissing={() => {
            setThreadId(null)
            if (scope) rememberChoice(scope, PRIMARY_CHAT_AGENT, null)
            setNotice("That conversation no longer exists.")
            void queryClient.invalidateQueries({
              queryKey: threadKeys.list(organizationId),
            })
          }}
          onFileCreated={onFileCreated}
          onStateChange={(id, next) =>
            setReported({ threadId: id, state: next })
          }
          {...composer}
        />
      </Suspense>
    )
  } else if (threadId === null) {
    body = (
      <>
        <AgentConversation
          agentName={agentName}
          state={pending ? "ready" : "empty"}
          error={notice}
        >
          {pending && (
            <p className="workspace-agent-message" data-from="member">
              {pending}
            </p>
          )}
        </AgentConversation>
        <AgentComposer
          label={composerLabel(agentName)}
          placeholder={composerLabel(agentName)}
          value={draft}
          onChange={setDraft}
          onSubmit={(message) => void start(message)}
          disabled={starting}
          status={starting ? "Connecting…" : undefined}
          onFocus={() => onComposingChange(true)}
          onBlur={() => onComposingChange(false)}
        />
      </>
    )
  } else {
    body = (
      <OpeningConversation
        agentName={agentName}
        pending={null}
        error={
          threads.isError
            ? failureText(threads.error, "Couldn't load your conversations.")
            : undefined
        }
        onRetry={threads.isError ? () => void threads.refetch() : undefined}
        {...composer}
      />
    )
  }

  return (
    <>
      <div className="workspace-agent-thread-bar">
        <p className="workspace-agent-thread-title" title={title}>
          {title}
        </p>
        {onOpenOverview && !inOverview && (
          <button
            className="workspace-agent-new"
            type="button"
            aria-label="Open Agent overview"
            title="Open Agent overview"
            onClick={onOpenOverview}
          >
            <Expand size={15} strokeWidth={1.7} />
          </button>
        )}
        <button
          className="workspace-agent-new"
          type="button"
          aria-label="New conversation"
          title="New conversation"
          disabled={!threadId || starting}
          onClick={startNew}
        >
          <SquarePen size={15} strokeWidth={1.7} />
        </button>
      </div>
      {body}
    </>
  )
}

/** The conversation while it loads: its first message if it has one, and a composer that waits. */
function OpeningConversation({
  agentName,
  pending,
  error,
  onRetry,
  draft,
  onDraftChange,
  onComposingChange,
}: ComposerHandlers & {
  agentName?: string
  pending: string | null
  error?: string
  onRetry?: () => void
}) {
  return (
    <>
      <AgentConversation
        agentName={agentName}
        state={pending ? "ready" : "loading"}
        error={error}
        onRetry={onRetry}
      >
        {pending && (
          <p className="workspace-agent-message" data-from="member">
            {pending}
          </p>
        )}
      </AgentConversation>
      <AgentComposer
        label={composerLabel(agentName)}
        placeholder={composerLabel(agentName)}
        value={draft}
        onChange={onDraftChange}
        disabled
        status={error ? undefined : "Connecting…"}
        onFocus={() => onComposingChange(true)}
        onBlur={() => onComposingChange(false)}
      />
    </>
  )
}

type Connection = "connecting" | "open" | "reconnecting"

/** The member's latest message, which the log scrolls back to when it changes. */
export function lastUserMessageId(
  messages: readonly ThreadChatMessage[]
): string | undefined {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    if (messages[index].role === "user") return messages[index].id
  }
  return undefined
}

/**
 * One thread's chat, through its Durable Object. It suspends until the
 * stored transcript loads (useAgentChat's `getInitialMessages`).
 */
function ThreadChat({
  organizationId,
  threadId,
  agentName,
  pending,
  pendingScreen,
  getScreenContext,
  onPendingTaken,
  onMissing,
  draft,
  onDraftChange,
  onComposingChange,
  onFileCreated,
  onStateChange,
}: ComposerHandlers & {
  organizationId: string
  threadId: string
  agentName?: string
  pending: string | null
  pendingScreen?: ScreenContext
  getScreenContext?: () => ScreenContext
  onPendingTaken: () => void
  onMissing: () => void
  /** What this thread is doing: a run streams, a confirmation waits on the member, or neither. */
  onStateChange: (threadId: string, state: ChatState) => void
  onFileCreated?: (file: {
    threadId: string
    name: string
    version?: number
  }) => void
}) {
  const queryClient = useQueryClient()
  const detail = useQuery(threadDetailQueryOptions(organizationId, threadId))
  const [connection, setConnection] = useState<Connection>("connecting")
  const [runError, setRunError] = useState("")
  const [resending, setResending] = useState(false)
  // Answers sent, until the run they went with ends.
  const [submitted, setSubmitted] = useState<Record<string, boolean>>({})
  // The last send, so it can go once more after the session is refreshed:
  // `expired` holds the Durable Object's word that it could not start, until
  // that turn ends, and `retried` makes sure it goes again only once.
  const lastSend = useRef<{
    body?: Record<string, unknown>
    expired: boolean
    retried: boolean
  }>({ expired: false, retried: false })

  const agent = useAgent({
    protocol: socketProtocol(),
    agent: "ThreadChatAgent",
    name: threadAgentName(organizationId, threadId),
    onOpen: () => setConnection("open"),
    // A dropped socket reconnects by itself with whatever cookie the browser
    // holds, and the Worker turns one away whose access cookie has expired.
    // Reading the session refreshes that cookie for the next attempt.
    onClose: (event: CloseEvent) => {
      setConnection("reconnecting")
      if (event.code !== 1000) void getSession().catch(() => undefined)
    },
  })

  const chat = useAgentChat<unknown, ThreadChatMessage>({
    agent,
    getInitialMessages: () =>
      loadTranscript(queryClient, organizationId, threadId),
    onData: (part) => {
      if (part.data.type !== "run_failed") return
      if (part.data.code !== sessionExpiredCode) return
      if (!lastSend.current.retried) {
        // The socket's cookie expired before Go started the run, so nothing
        // ran; it goes again once this turn ends.
        lastSend.current.retried = true
        lastSend.current.expired = true
        return
      }
      setRunError(
        part.data.error || "Your session has ended. Sign in again to continue."
      )
    },
    onError: (error) =>
      setRunError(error.message || "Your agent couldn't answer. Try again."),
    onFinish: ({ isAbort, isDisconnect, isError }) => {
      if (lastSend.current.expired) {
        lastSend.current.expired = false
        if (!isAbort && !isDisconnect && !isError) void resendWithFreshSession()
        return
      }
      setSubmitted({})
      void queryClient.invalidateQueries({
        queryKey: threadKeys.detail(organizationId, threadId),
      })
      void queryClient.invalidateQueries({
        queryKey: threadKeys.list(organizationId),
      })
    },
  })

  // Reading the session refreshes the expired cookie, the reopened socket
  // carries it to the Durable Object, and regenerating makes the same request
  // again without adding the member's message twice.
  async function resendWithFreshSession() {
    setResending(true)
    try {
      await getSession()
      await reopenSocket(agent)
      await chat.regenerate({ body: lastSend.current.body })
    } catch (error) {
      setSubmitted({})
      setRunError(
        failureText(error, "Your session has ended. Sign in again to continue.")
      )
    } finally {
      setResending(false)
    }
  }

  function send(text: string, screen = getScreenContext?.()) {
    setRunError("")
    const body = screen ? { screen_context: screen } : undefined
    lastSend.current = { body, expired: false, retried: false }
    void chat
      .sendMessage({ text }, ...(body ? [{ body }] : []))
      .catch((error: unknown) =>
        setRunError(
          failureText(error, "Your agent couldn't answer. Try again.")
        )
      )
  }

  function answer(answers: ConfirmationAnswer[]) {
    setRunError("")
    setSubmitted((current) => {
      const next = { ...current }
      for (const item of answers) next[item.id] = item.confirmed
      return next
    })
    const body = {
      confirmations: answers,
      screen_context: getScreenContext?.(),
    }
    lastSend.current = { body, expired: false, retried: false }
    void chat
      .sendMessage(
        { text: " ", metadata: { oryxaConfirmation: true } },
        { body }
      )
      .catch((error: unknown) => {
        setSubmitted({})
        setRunError(failureText(error, "Your answer wasn't sent. Try again."))
      })
  }

  const running =
    resending ||
    chat.status === "submitted" ||
    chat.status === "streaming" ||
    chat.isServerStreaming

  // A new conversation's first message goes once its socket is open. Not
  // on mount: useChat stops a request its effects' cleanup finds in flight,
  // which React's development double mount would do to this one.
  const pendingTaken = useRef(false)
  useEffect(() => {
    if (!pending || pendingTaken.current || connection !== "open") return
    pendingTaken.current = true
    onPendingTaken()
    send(pending, pendingScreen)
  })

  // Go can add to the thread outside this socket (updates from the member's
  // apps, a team, an answer given elsewhere): merge each history it sends
  // once nothing runs. Once per history, so what the merge cannot match
  // (such as an empty message) is never added twice.
  const reconciled = useRef<unknown>(undefined)
  useEffect(() => {
    const history = detail.data
    if (!history || running || connection !== "open") return
    if (reconciled.current === history) return
    reconciled.current = history
    const merged = reconcileChatMessages(chat.messages, history.items)
    if (merged !== chat.messages) chat.setMessages(merged)
  }, [chat, connection, detail.data, running])

  // A thread deleted elsewhere: its socket would only be turned away.
  const missing =
    detail.error instanceof ApiError && detail.error.status === 404
  const reportedMissing = useRef(false)
  useEffect(() => {
    if (!missing || reportedMissing.current) return
    reportedMissing.current = true
    onMissing()
  })

  const items = useMemo(
    () => chatMessagesToItems(chat.messages),
    [chat.messages]
  )
  const entries = useMemo(() => buildTimeline(items), [items])
  const files = entries.filter(
    (entry): entry is ArtifactEntry => entry.kind === "artifact"
  )
  const fileState = useRef<{ seen: Set<string>; ran: boolean }>({
    seen: new Set(),
    ran: false,
  })
  useEffect(() => {
    const state = fileState.current
    if (running) state.ran = true
    const newFile = [...files]
      .reverse()
      .find((file) => !state.seen.has(file.key))
    files.forEach((file) => state.seen.add(file.key))
    if (state.ran && newFile) {
      void queryClient.invalidateQueries({
        queryKey: threadKeys.detail(organizationId, threadId),
      })
      onFileCreated?.({
        threadId,
        name: newFile.name,
        version: newFile.version,
      })
    }
    if (!running) state.ran = false
  }, [
    files.map((file) => file.key).join("|"),
    running,
    onFileCreated,
    organizationId,
    queryClient,
    threadId,
  ])
  const actionable = useMemo(() => {
    const waiting = pendingConfirmationIds(detail.data, items)
    return new Set(waiting.filter((id) => !(id in submitted)))
  }, [detail.data, items, submitted])

  const state: ChatState = running
    ? "running"
    : actionable.size > 0
      ? "waiting"
      : "idle"
  // Said as it changes, and taken back when this thread's chat goes, so a
  // later visit doesn't start from what it last said.
  useEffect(() => {
    onStateChange(threadId, state)
    return () => onStateChange(threadId, "idle")
  }, [threadId, state])

  const lastEntry = entries.at(-1)
  const drafting =
    lastEntry?.kind === "message" && lastEntry.state === "streaming"
  const lastMemberMessage = lastUserMessageId(chat.messages)
  const label = composerLabel(agentName)
  const status =
    connection === "connecting"
      ? "Connecting…"
      : connection === "reconnecting"
        ? "Reconnecting…"
        : actionable.size > 1
          ? "Answer the confirmations above to continue."
          : actionable.size === 1
            ? "Answer the confirmation above to continue."
            : undefined

  return (
    <>
      <AgentConversation
        agentName={agentName}
        state={entries.length > 0 || running || pending ? "ready" : "empty"}
        error={runError}
        follow={lastMemberMessage}
      >
        <AgentTimeline
          entries={entries}
          running={running}
          agentName={agentName}
          actionable={actionable}
          submitted={submitted}
          answering={running || connection !== "open"}
          onAnswer={answer}
          organizationId={organizationId}
          threadId={threadId}
        />
        {pending && (
          <p className="workspace-agent-message" data-from="member">
            {pending}
          </p>
        )}
        {running && !drafting && <AgentActivity />}
      </AgentConversation>
      <AgentComposer
        label={label}
        placeholder={label}
        value={draft}
        onChange={onDraftChange}
        onSubmit={(message) => {
          onDraftChange("")
          send(message)
        }}
        disabled={connection !== "open" || actionable.size > 0}
        streaming={running}
        onStop={() => void chat.stop()}
        status={status}
        onFocus={() => onComposingChange(true)}
        onBlur={() => onComposingChange(false)}
      />
    </>
  )
}
