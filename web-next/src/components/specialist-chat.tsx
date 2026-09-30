// The member's chat with one of their own specialists, through the
// specialist's `SpecialistChatAgent` Durable Object (REALTIME.md). The
// focused view's naked conversation and the agent panel's Agents tab are
// both this component, each with its own socket to the same object, which
// sends every socket the same conversation. It reuses the primary agent's
// chat pieces: the timeline, the conversation log and the pill composer.
import { useEffect, useMemo, useRef, useState } from "react"
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query"
import { useAgentChat } from "@cloudflare/ai-chat/react"
import { useAgent } from "agents/react"
import { SquarePen } from "lucide-react"
import { failureText, lastUserMessageId } from "./agent-chat"
import { AgentChatHistory } from "./agent-chat-history"
import { AgentComposer } from "./agent-composer"
import { AgentConversation } from "./agent-conversation"
import { AgentActivity, AgentTimeline } from "./agent-timeline"
import { ApiError } from "@/lib/api/client"
import type { Session } from "@/lib/auth/client"
import { getSession } from "@/lib/auth/client"
import { getAgent } from "@/lib/agents/managed-agents"
import type { ManagedAgent } from "@/lib/agents/managed-agents"
import { agentKeys } from "@/lib/agents/queries"
import {
  readableSpecialistItems,
  specialistAgentName,
} from "@/lib/specialists/chat-messages"
import type { SpecialistChatState } from "@/lib/specialists/chat-messages"
import {
  selectSpecialistChat,
  deleteSpecialistThread,
  specialistThreadsQueryOptions,
  getSpecialistChatMessages,
  specialistKeys,
  specialistThreadQueryOptions,
} from "@/lib/specialists/client"
import { reopenSocket } from "@/lib/threads/agent-socket"
import {
  chatMessagesToItems,
  reconcileChatMessages,
  sessionExpiredCode,
} from "@/lib/threads/chat-messages"
import type { ThreadChatMessage } from "@/lib/threads/chat-messages"
import {
  openingThread,
  rememberedView,
  useRememberedView,
} from "@/lib/threads/chat-choice"
import type { ChatScope } from "@/lib/threads/chat-choice"
import type { ConfirmationAnswer, ThreadDetail } from "@/lib/threads/client"
import { socketProtocol } from "@/lib/socket-protocol"
import { buildTimeline, pendingConfirmationIds } from "@/lib/threads/timeline"
import type { ScreenContext } from "@/lib/agents/screen-context"

/** What the chat needs of a specialist, as the member's agents list has it. */
export type ChatSpecialist = Pick<ManagedAgent, "id" | "name" | "status">

/**
 * `panel`: the agent panel's Agents tab, under one muted row that names the
 * conversation (DESIGN.md §3). `focus`: the focused view's naked
 * conversation, under its presence row.
 */
export type SpecialistChatLayout = "panel" | "focus"

type Connection = "connecting" | "open" | "reconnecting"

/**
 * Why this page can no longer use the chat: the specialist is gone (the
 * Worker's check with Go answers 404), or the session is over.
 */
type Refusal = "gone" | "session_ended"

type Transcript =
  | { status: "loading" }
  | { status: "ready"; messages: ThreadChatMessage[] }
  | { status: "failed"; error: unknown }

const sessionEndedWords = "Your session has ended. Sign in again to continue."

function refusalOf(error: unknown): Refusal | undefined {
  if (!(error instanceof ApiError)) return undefined
  if (error.status === 404) return "gone"
  // apiFetch has already refreshed the session once and been refused again.
  if (error.status === 401) return "session_ended"
  return undefined
}

/**
 * The transcript the object stored, read once per view. Not through
 * useAgentChat's `getInitialMessages`: that caches one promise per object
 * name for the whole page and drops it when any view holding it unmounts,
 * so a second view of the same chat would start from the first view's old
 * copy, and the view left behind would suspend and read it again on its
 * next render. Each view reads its own and hands it over as `messages`.
 */
function useTranscript(
  organizationId: string,
  agentId: string,
  attempt: number,
  enabled: boolean
): Transcript {
  const [transcript, setTranscript] = useState<Transcript>({
    status: "loading",
  })
  useEffect(() => {
    if (!enabled) return
    const controller = new AbortController()
    setTranscript({ status: "loading" })
    getSpecialistChatMessages(organizationId, agentId, {
      signal: controller.signal,
    }).then(
      (messages) => {
        if (!controller.signal.aborted) {
          setTranscript({ status: "ready", messages })
        }
      },
      (error: unknown) => {
        if (!controller.signal.aborted) {
          setTranscript({ status: "failed", error })
        }
      }
    )
    return () => controller.abort()
  }, [organizationId, agentId, attempt, enabled])
  return transcript
}

type Presence = {
  connection: "live" | "connecting" | "paused" | "gone" | "error"
  label: string
}

/**
 * The row above the conversation: its name, and a quiet button that starts
 * a new one. In the focused view it is the presence row, with the chat's
 * state in a pill.
 */
function ChatBar({
  layout,
  title,
  presence,
  canStartOver,
  onStartOver,
}: {
  layout: SpecialistChatLayout
  title: string
  presence: Presence
  canStartOver: boolean
  onStartOver?: () => void
}) {
  const startOver = (
    <button
      className="workspace-agent-new workspace-agent-new-chat"
      type="button"
      aria-label="New conversation"
      title="New conversation"
      disabled={!canStartOver}
      onClick={onStartOver}
    >
      <SquarePen size={15} strokeWidth={1.7} /> New chat
    </button>
  )
  const name = (
    <p className="workspace-agent-thread-title" title={title}>
      {title}
    </p>
  )
  if (layout === "panel") {
    return (
      <div className="workspace-agent-thread-bar">
        {name}
        {startOver}
      </div>
    )
  }
  return (
    <div className="workspace-conversation-presence">
      <span
        className="workspace-presence-pill"
        data-connection={presence.connection}
      >
        {presence.label}
      </span>
      {name}
      {startOver}
    </div>
  )
}

/** Why the composer cannot send, when that is for good rather than for now. */
function blockedWords(
  specialist: ChatSpecialist,
  refusal: Refusal | undefined
): string | undefined {
  if (refusal === "gone") return `${specialist.name} no longer exists.`
  if (refusal === "session_ended") return sessionEndedWords
  if (specialist.status !== "active") {
    return `${specialist.name} is paused. Resume it to chat with it.`
  }
  return undefined
}

function composerPlaceholder(
  specialist: ChatSpecialist,
  refusal: Refusal | undefined
): string {
  if (refusal === "gone") return `${specialist.name} no longer exists`
  if (specialist.status !== "active") return `${specialist.name} is paused`
  return `Message ${specialist.name}`
}

function presenceOf(
  specialist: ChatSpecialist,
  refusal: Refusal | undefined,
  connection: Connection,
  running: boolean
): Presence {
  if (refusal === "gone") return { connection: "gone", label: "Deleted" }
  if (refusal === "session_ended") {
    return { connection: "error", label: "Signed out" }
  }
  if (specialist.status !== "active") {
    return { connection: "paused", label: "Paused" }
  }
  if (connection !== "open") {
    return {
      connection: "connecting",
      label: connection === "connecting" ? "Connecting…" : "Reconnecting…",
    }
  }
  return { connection: "live", label: running ? "Working" : "Ready" }
}

type ComposerHandlers = {
  /** The composer's text, kept above the chat so it survives a reload. */
  draft: string
  onDraftChange: (value: string) => void
  onComposingChange?: (composing: boolean) => void
}

/**
 * One specialist's chat. It reads the stored transcript first, then opens
 * the socket; a specialist that is gone or paused says so, and a paused one
 * stays readable with the composer off.
 */
export function SpecialistChat({
  organizationId,
  specialist,
  scope: givenScope,
  visible = true,
  missing = false,
  layout,
  draft: heldDraft,
  onDraftChange: holdDraft,
  onComposingChange,
  getScreenContext,
}: {
  organizationId: string
  specialist: ChatSpecialist
  scope?: ChatScope
  visible?: boolean
  /** The member's agents no longer include it: it was deleted. */
  missing?: boolean
  layout: SpecialistChatLayout
  /**
   * The composer's text, when the parent keeps it (the panel does, so it
   * outlasts a switch to another agent); otherwise the chat keeps its own.
   */
  draft?: string
  onDraftChange?: (value: string) => void
  onComposingChange?: (composing: boolean) => void
  getScreenContext?: () => ScreenContext
}) {
  const queryClient = useQueryClient()
  const userId = queryClient.getQueryData<Session>(["auth", "session"])?.user.id
  const scope = givenScope ?? (userId ? { organizationId, userId } : undefined)
  const [attempt, setAttempt] = useState(0)
  const [ownDraft, setOwnDraft] = useState("")
  const draft = heldDraft ?? ownDraft
  const setDraft = holdDraft ?? setOwnDraft
  const transcript = useTranscript(
    organizationId,
    specialist.id,
    attempt,
    !missing
  )
  const composer: ComposerHandlers = {
    draft,
    onDraftChange: setDraft,
    onComposingChange,
  }

  if (!missing && transcript.status === "ready") {
    return (
      <SpecialistChatSession
        key={attempt}
        organizationId={organizationId}
        specialist={specialist}
        initialMessages={transcript.messages}
        layout={layout}
        scope={scope}
        visible={visible}
        getScreenContext={getScreenContext}
        {...composer}
      />
    )
  }
  const refusal: Refusal | undefined = missing
    ? "gone"
    : transcript.status === "failed"
      ? refusalOf(transcript.error)
      : undefined
  const failed = transcript.status === "failed" && !refusal
  return (
    <ClosedChat
      organizationId={organizationId}
      layout={layout}
      specialist={specialist}
      refusal={refusal}
      loading={!refusal && transcript.status === "loading"}
      error={
        failed
          ? failureText(transcript.error, "Couldn't load this conversation.")
          : undefined
      }
      onRetry={failed ? () => setAttempt((count) => count + 1) : undefined}
      {...composer}
    />
  )
}

/** The chat before its socket opens: loading, unreadable, or gone. */
function ClosedChat({
  organizationId,
  layout,
  specialist,
  refusal,
  loading,
  error,
  onRetry,
  draft,
  onDraftChange,
  onComposingChange,
}: ComposerHandlers & {
  organizationId: string
  layout: SpecialistChatLayout
  specialist: ChatSpecialist
  refusal: Refusal | undefined
  loading: boolean
  error?: string
  onRetry?: () => void
}) {
  const blocked = blockedWords(specialist, refusal)
  const threads = useQuery({
    ...specialistThreadsQueryOptions(organizationId, specialist.id),
    enabled: refusal !== "gone" && refusal !== "session_ended",
  })
  return (
    <>
      <ChatBar
        layout={layout}
        title={loading ? "Loading…" : "Conversation"}
        presence={
          error
            ? { connection: "error", label: "Unavailable" }
            : presenceOf(specialist, refusal, "connecting", false)
        }
        canStartOver={false}
      />
      <AgentChatHistory
        threads={threads.data ?? []}
        loading={threads.isPending}
        error={
          threads.isError
            ? failureText(threads.error, "Couldn't load your conversations.")
            : undefined
        }
        onRetry={() => void threads.refetch()}
        disabled
        onOpen={() => {}}
        onDelete={async () => {}}
      />
      <AgentConversation
        agentName={specialist.name}
        state={loading ? "loading" : "ready"}
        error={error}
        onRetry={onRetry}
      />
      <AgentComposer
        label={`Message ${specialist.name}`}
        placeholder={composerPlaceholder(specialist, refusal)}
        value={draft}
        onChange={onDraftChange}
        disabled
        status={blocked ?? (loading ? "Connecting…" : undefined)}
        onFocus={() => onComposingChange?.(true)}
        onBlur={() => onComposingChange?.(false)}
      />
    </>
  )
}

/**
 * The chat over its socket. It opens with the transcript the page read,
 * and from then on the object keeps it in step: the member's messages from
 * any view, the turn streaming (the progress marker while the specialist
 * works, then its readable result), and starting over.
 */
function SpecialistChatSession({
  organizationId,
  specialist,
  scope,
  visible,
  initialMessages,
  layout,
  draft,
  onDraftChange,
  onComposingChange,
  getScreenContext,
}: ComposerHandlers & {
  organizationId: string
  specialist: ChatSpecialist
  scope?: ChatScope
  visible: boolean
  initialMessages: ThreadChatMessage[]
  layout: SpecialistChatLayout
  getScreenContext?: () => ScreenContext
}) {
  const queryClient = useQueryClient()
  const [connection, setConnection] = useState<Connection>("connecting")
  // The Go conversation the object continues: undefined until the socket
  // says, null for none (a new conversation starts with the next message).
  const [threadId, setThreadId] = useState<string | null>()
  const [decided, setDecided] = useState(!scope)
  const [explicitNew, setExplicitNew] = useState(false)
  const openingChoice = useRef(
    scope ? rememberedView(scope, specialist.id) : null
  )
  const threads = useQuery({
    ...specialistThreadsQueryOptions(organizationId, specialist.id),
  })
  useRememberedView(
    threadId,
    scope,
    specialist.id,
    visible && decided,
    explicitNew
  )
  const historyDetails = useQueries({
    queries: (threads.data ?? []).map((thread) => ({
      ...specialistThreadQueryOptions(organizationId, specialist.id, thread.id),
      staleTime: 10_000,
      refetchInterval: (query: { state: { data?: ThreadDetail } }) =>
        (query.state.data?.pending_confirmations.length ?? 0) > 0
          ? 10_000
          : false,
      enabled: visible,
    })),
  })
  const held = new Set(
    (threads.data ?? [])
      .filter(
        (_, i) =>
          (historyDetails[i].data?.pending_confirmations.length ?? 0) > 0
      )
      .map((thread) => thread.id)
  )
  const [refusal, setRefusal] = useState<Refusal>()
  const [runError, setRunError] = useState("")
  const [resending, setResending] = useState(false)
  const [clearing, setClearing] = useState(false)
  // Answers sent, until the run they went with ends.
  const [submitted, setSubmitted] = useState<Record<string, boolean>>({})
  // This page's last send, so it can go once more after the session is
  // refreshed. `own` is true while that send's turn runs: the object
  // streams every turn to every socket, and a turn another view sent is
  // that view's to resend.
  const lastSend = useRef<{
    body?: Record<string, unknown>
    own: boolean
    expired: boolean
    retried: boolean
  }>({ own: false, expired: false, retried: false })
  const checking = useRef<Promise<void>>(undefined)
  const name = specialist.name

  const socket = useAgent<SpecialistChatState>({
    protocol: socketProtocol(),
    agent: "specialist-chat-agent",
    name: specialistAgentName(organizationId, specialist.id),
    enabled: refusal === undefined,
    onOpen: () => setConnection("open"),
    onClose: (event: CloseEvent) => {
      setConnection("reconnecting")
      if (event.code !== 1000) checkAccess()
    },
    onStateUpdate: (state) => setThreadId(state.threadId),
  })

  const chat = useAgentChat<SpecialistChatState, ThreadChatMessage>({
    agent: socket,
    // This view's own read (see useTranscript), not the shared cache.
    getInitialMessages: null,
    messages: initialMessages,
    onData: (part) => {
      const item = part.data
      if (item.type !== "run_failed") return
      if (item.code === sessionExpiredCode) {
        if (!lastSend.current.own) return
        if (!lastSend.current.retried) {
          // The socket's cookie expired before Go started the run, so
          // nothing ran; it goes again once this turn ends.
          lastSend.current.retried = true
          lastSend.current.expired = true
          return
        }
        setRunError(item.error || sessionEndedWords)
        return
      }
      // Paused or deleted elsewhere: the member's agents say which, and a
      // specialist that is gone closes the chat.
      if (item.code === "agent_disabled" || item.code === "not_found") {
        void queryClient.invalidateQueries({
          queryKey: agentKeys.agentList(organizationId),
        })
      }
      if (item.code === "not_found") checkAccess()
    },
    onError: (error) => {
      lastSend.current.own = false
      setRunError(error.message || `${name} couldn't answer. Try again.`)
    },
    onFinish: ({ isAbort, isDisconnect, isError }) => {
      if (lastSend.current.expired) {
        lastSend.current.expired = false
        if (!isAbort && !isDisconnect && !isError) {
          void resendWithFreshSession()
          return
        }
      }
      lastSend.current.own = false
      setSubmitted({})
    },
  })

  /**
   * The browser is never told why a socket was turned away, so Go is asked
   * what the Worker asks before it lets one through: whether the specialist
   * is still the member's own. Gone or signed out closes the chat; anything
   * else leaves the socket to keep trying, now with a refreshed cookie.
   */
  function checkAccess() {
    checking.current ??= getAgent(organizationId, specialist.id)
      .then(
        (agent) => {
          if (agent.status !== specialist.status) {
            void queryClient.invalidateQueries({
              queryKey: agentKeys.agentList(organizationId),
            })
          }
        },
        (error: unknown) => {
          const refused = refusalOf(error)
          if (!refused) return
          setRefusal(refused)
          if (refused === "gone") {
            void queryClient.invalidateQueries({
              queryKey: agentKeys.agentList(organizationId),
            })
          }
        }
      )
      .finally(() => {
        checking.current = undefined
      })
  }

  // Reading the session refreshes the expired cookie, the reopened socket
  // carries it to the object, and regenerating makes the same request again
  // without adding the member's message twice.
  async function resendWithFreshSession() {
    setResending(true)
    try {
      await getSession()
      await reopenSocket(socket)
      await chat.regenerate({ body: lastSend.current.body })
    } catch (error) {
      lastSend.current.own = false
      setSubmitted({})
      setRunError(failureText(error, sessionEndedWords))
    } finally {
      setResending(false)
    }
  }

  function send(text: string) {
    setRunError("")
    const body = getScreenContext
      ? { screen_context: getScreenContext() }
      : undefined
    lastSend.current = { body, own: true, expired: false, retried: false }
    void chat
      .sendMessage({ text }, ...(body ? [{ body }] : []))
      .catch((error: unknown) => {
        lastSend.current.own = false
        setRunError(failureText(error, `${name} couldn't answer. Try again.`))
      })
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
    lastSend.current = { body, own: true, expired: false, retried: false }
    void chat
      .sendMessage(
        { text: " ", metadata: { oryxaConfirmation: true } },
        { body }
      )
      .catch((error: unknown) => {
        lastSend.current.own = false
        setSubmitted({})
        setRunError(failureText(error, "Your answer wasn't sent. Try again."))
      })
  }

  // Starting over: the object clears its transcript, forgets its Go
  // conversation (Go keeps it) and sends every socket the empty chat and
  // its new state, so each view of this chat follows from its own socket.
  async function startOver() {
    setExplicitNew(true)
    setDecided(true)
    setClearing(true)
    setRunError("")
    try {
      await selectSpecialistChat(organizationId, specialist.id, null)
      setSubmitted({})
    } catch (error) {
      const refused = refusalOf(error)
      if (refused) setRefusal(refused)
      else {
        setRunError(
          failureText(error, "Couldn't start a new conversation. Try again.")
        )
      }
    } finally {
      setClearing(false)
    }
  }

  const running =
    resending ||
    chat.status === "submitted" ||
    chat.status === "streaming" ||
    chat.isServerStreaming

  useEffect(() => {
    if (!decided && running) {
      setDecided(true)
      return
    }
    if (
      decided ||
      threadId === undefined ||
      !threads.data ||
      running ||
      clearing
    )
      return
    const lifecycle = { stale: false }
    const isCurrent = () => !lifecycle.stale
    const choice = openingChoice.current
    void openingThread({
      threads: threads.data.map((thread) => ({ ...thread, origin: "chat" })),
      choice: choice?.choice ?? null,
      leftAt: choice?.leftAt,
      now: Date.now(),
      waiting: async (id) => {
        const detail = await queryClient
          .fetchQuery(
            specialistThreadQueryOptions(organizationId, specialist.id, id)
          )
          .catch(() => undefined)
        return (detail?.pending_confirmations.length ?? 0) > 0
      },
    }).then(async (chosen) => {
      if (!isCurrent()) return
      try {
        await selectSpecialistChat(organizationId, specialist.id, chosen)
      } catch (error) {
        if (isCurrent())
          setRunError(
            failureText(error, "Couldn't open the conversation. Try again.")
          )
      }
      if (isCurrent()) setDecided(true)
    })
    return () => {
      lifecycle.stale = true
    }
  }, [
    decided,
    threadId,
    threads.data,
    running,
    clearing,
    organizationId,
    specialist.id,
    queryClient,
  ])

  const detail = useQuery({
    ...specialistThreadQueryOptions(
      organizationId,
      specialist.id,
      threadId ?? ""
    ),
    enabled: Boolean(threadId),
  })
  // Go stores a specialist's answer as its raw JSON work result.
  const history = useMemo(
    () =>
      detail.data && {
        ...detail.data,
        items: readableSpecialistItems(detail.data.items),
      },
    [detail.data]
  )

  // Every turn that ends, whichever view sent it, may have changed the
  // conversation's title and its waiting confirmations: read Go's copy.
  const wasRunning = useRef(false)
  useEffect(() => {
    if (wasRunning.current && !running) {
      void queryClient.invalidateQueries({
        queryKey: specialistKeys.agent(organizationId, specialist.id),
      })
    }
    wasRunning.current = running
  }, [organizationId, queryClient, running, specialist.id])

  // What Go stored and the transcript lacks, such as a run a restart of the
  // object cut off: merged once per history Go sends, once nothing runs.
  const reconciled = useRef<unknown>(undefined)
  useEffect(() => {
    if (!history || running || connection !== "open") return
    if (history.id !== threadId || reconciled.current === history) return
    reconciled.current = history
    const merged = reconcileChatMessages(chat.messages, history.items)
    if (merged !== chat.messages) chat.setMessages(merged)
  }, [chat, connection, history, running, threadId])

  const items = useMemo(
    () => chatMessagesToItems(chat.messages),
    [chat.messages]
  )
  const entries = useMemo(() => buildTimeline(items), [items])
  const actionable = useMemo(() => {
    const waiting = pendingConfirmationIds(
      threadId ? history : undefined,
      items
    )
    return new Set(waiting.filter((id) => !(id in submitted)))
  }, [history, items, submitted, threadId])

  const lastEntry = entries.at(-1)
  // A specialist's draft streams as a marker with no words (its deltas are
  // raw JSON), so the conversation says it is working until the readable
  // answer lands and the turn ends.
  const writing =
    lastEntry?.kind === "message" &&
    lastEntry.state === "streaming" &&
    lastEntry.text !== ""
  const blocked = blockedWords(specialist, refusal)
  const open = connection === "open" && !refusal
  const title =
    threadId === null
      ? "New conversation"
      : detail.data?.title || (threadId ? "Conversation" : "Loading…")
  const status =
    blocked ??
    (connection === "connecting"
      ? "Connecting…"
      : connection === "reconnecting"
        ? "Reconnecting…"
        : clearing
          ? "Starting a new conversation…"
          : actionable.size > 1
            ? "Answer the confirmations above to continue."
            : actionable.size === 1
              ? "Answer the confirmation above to continue."
              : undefined)

  return (
    <>
      <ChatBar
        layout={layout}
        title={title}
        presence={presenceOf(specialist, refusal, connection, running)}
        canStartOver={open && !running && !clearing}
        onStartOver={() => void startOver()}
      />
      <AgentChatHistory
        threads={threads.data ?? []}
        selected={threadId}
        waiting={held}
        loading={threads.isPending}
        error={
          threads.isError
            ? failureText(threads.error, "Couldn't load your conversations.")
            : undefined
        }
        onRetry={() => void threads.refetch()}
        disabled={running || clearing || !open || !decided}
        onOpen={(id) => {
          setDecided(true)
          setClearing(true)
          setRunError("")
          void selectSpecialistChat(organizationId, specialist.id, id)
            .then(() => setSubmitted({}))
            .catch((error: unknown) =>
              setRunError(
                failureText(error, "Couldn't open this conversation.")
              )
            )
            .finally(() => setClearing(false))
        }}
        onDelete={async (id) => {
          await deleteSpecialistThread(organizationId, specialist.id, id)
          await queryClient.cancelQueries({
            queryKey: specialistKeys.threads(organizationId, specialist.id),
          })
          queryClient.setQueryData(
            specialistKeys.threads(organizationId, specialist.id),
            (old: typeof threads.data) =>
              old?.filter((thread) => thread.id !== id)
          )
          queryClient.removeQueries({
            queryKey: specialistKeys.thread(organizationId, specialist.id, id),
          })
          void queryClient.invalidateQueries({
            queryKey: specialistKeys.threads(organizationId, specialist.id),
          })
        }}
      />
      <AgentConversation
        agentName={name}
        state={entries.length > 0 || running ? "ready" : "empty"}
        error={runError}
        follow={lastUserMessageId(chat.messages)}
      >
        <AgentTimeline
          entries={entries}
          running={running}
          agentName={name}
          actionable={actionable}
          submitted={submitted}
          // Go refuses a paused specialist's runs, answers included.
          answering={running || !open || Boolean(blocked)}
          onAnswer={answer}
          organizationId={organizationId}
        />
        {running && !writing && <AgentActivity name={name} />}
      </AgentConversation>
      <AgentComposer
        label={`Message ${name}`}
        placeholder={composerPlaceholder(specialist, refusal)}
        value={draft}
        onChange={onDraftChange}
        onSubmit={(message) => {
          onDraftChange("")
          send(message)
        }}
        disabled={
          Boolean(blocked) ||
          !open ||
          clearing ||
          !decided ||
          actionable.size > 0
        }
        streaming={running}
        onStop={() => void chat.stop()}
        status={status}
        onFocus={() => onComposingChange?.(true)}
        onBlur={() => onComposingChange?.(false)}
      />
    </>
  )
}
