import {
  Fragment,
  Suspense,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react"
import type { Dispatch, ReactElement, ReactNode } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import type { QueryClient } from "@tanstack/react-query"
import { useHydrated } from "@tanstack/react-router"
import { useAgentChat } from "@cloudflare/ai-chat/react"
import { useAgent } from "agents/react"
import { AgentComposer } from "./agent-composer"
import { AgentActivity, AgentTimeline } from "./agent-timeline"
import { AgentConversation } from "./agent-conversation"
import { ApiError } from "@/lib/api/client"
import { getSession } from "@/lib/auth/client"
import { reopenSocket } from "@/lib/threads/agent-socket"
import {
  PRIMARY_DRAFT,
  firstMessage,
  openingThread,
  publishPrimaryChatActivity,
  rememberedView,
  recentNewView,
  reopensConversation,
  useRememberedView,
  shownThread,
} from "@/lib/threads/chat-choice"
import type {
  ChatScope,
  PrimaryChat,
  PrimaryChatAction,
  PrimaryChatActivity,
  UnsentMessage,
} from "@/lib/threads/chat-choice"
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
  threadApprovalQueryOptions,
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

/**
 * The chat with no conversation open, for whoever shows it (the shell's desk):
 * a composer whose first message starts a conversation of its own, the first
 * messages that were not sent, and why the chat left a conversation.
 */
export type BlankChat = ComposerHandlers & {
  /** Sends the first message of a new conversation, with what the member is looking at. */
  onSubmit: (message: string) => void
  /** The first messages that were not sent, with Retry, Edit and Dismiss; none while there are none. */
  notSent?: ReactElement
  /** Why the chat left a conversation ("That conversation no longer exists."), or "". */
  notice: string
}

type ChatState = PrimaryChatActivity["state"]

/**
 * How long a socket has to be open, with nothing streaming, before the chat
 * calls a run over. A run still alive resumes as soon as the socket opens,
 * so a run that hasn't resumed by then has ended.
 */
const RUN_SETTLE_MS = 2000

function composerLabel(agentName?: string): string {
  return agentName ? `Message ${agentName}` : "Message your agent"
}

/** An API refusal's own words, or `fallback` for anything without them. */
function failureText(error: unknown, fallback: string): string {
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
 * How long the chat gives a first message's conversation to be made before it
 * says the start failed, so a request that never answers can't hold up what
 * waits behind it.
 */
const START_TIMEOUT_MS = 30_000

/**
 * How long after its conversation's chat has taken a first message the chat
 * moves on to what waits behind it. The socket writes the message a tick
 * after the chat sends it, and a conversation unmounted before then would
 * abort it.
 */
const SENT_SETTLE_MS = 400

/**
 * How long the chat looks for the conversation to open (the lists it waits
 * for, and Go's word on a conversation waiting on the member) before it
 * gives up and opens a new one, so the member is never held at a chat that
 * cannot start.
 */
const OPENING_FALLBACK_MS = 5000

/**
 * Whether a conversation waits on the member's confirmation, from Go's
 * detail of it. One that can't be read counts as not waiting.
 */
async function waitsOnMember(
  queryClient: QueryClient,
  organizationId: string,
  threadId: string,
  viewerId?: string
): Promise<boolean> {
  const detail = await queryClient
    .fetchQuery(threadApprovalQueryOptions(organizationId, threadId, viewerId))
    .catch(() => undefined)
  return (detail?.pending_confirmations.length ?? 0) > 0
}

/**
 * The member's chat with their primary agent, as the center shows it: the
 * open conversation's log and composer, or, with none open, whatever
 * `renderBlank` makes of the blank chat (the shell's desk). As the app opens
 * it opens the conversation left in this tab less than five minutes ago, then
 * the newest one waiting on the member's approval, otherwise none. While
 * mounted it keeps the open conversation (chat-choice.ts). It runs through
 * the thread's Durable Object (REALTIME.md), in the browser only.
 *
 * What it is on, what waits to be opened or sent, the draft and the unsent
 * messages are the shell's (`chat`, moved by `dispatch`), so they outlast
 * whatever else the center shows, and every step names the request it
 * belongs to. The shell keeps this mounted, hidden while another screen is in
 * the center, so a run goes on streaming and a first message on its way still
 * reaches its conversation.
 */
export function AgentChat({
  organizationId,
  scope,
  agentId,
  agentName,
  chat,
  dispatch,
  onComposingChange,
  visible = true,
  getScreenContext,
  renderBlank,
}: {
  organizationId: string
  /** Whose choices the chat remembers for the session; without it none are. */
  scope?: ChatScope
  /**
   * The primary agent's id, which keys those choices. With a `scope`, the
   * chat waits for it before it chooses a conversation, so a choice is never
   * kept under a stand-in.
   */
  agentId?: string
  agentName?: string
  chat: PrimaryChat
  dispatch: Dispatch<PrimaryChatAction>
  onComposingChange: (composing: boolean) => void
  /**
   * Whether the chat is in front of the member. Only then is their presence
   * recorded, and only once it has been does an open conversation connect.
   */
  visible?: boolean
  getScreenContext?: () => ScreenContext
  /** What the chat shows with no conversation open, in view; its own quiet blank chat without it. */
  renderBlank?: (blank: BlankChat) => ReactNode
}) {
  const queryClient = useQueryClient()
  const hydrated = useHydrated()
  // The list is this member's own entry in the cache: another member's
  // (same organization, the page taken over without a reload) is never read.
  const list = threadListQueryOptions(organizationId, scope?.userId)
  const threads = useQuery({ ...list, enabled: hydrated })
  // Nothing about a conversation is chosen, shown or published until this
  // member's list has loaded.
  const listed = threads.data !== undefined
  const threadId = shownThread(chat)
  useRememberedView(
    threadId,
    scope,
    agentId,
    visible,
    chat.chosen || firstMessage(chat) !== null
  )
  const first = firstMessage(chat)
  // A conversation's socket opens once the chat has been in front of the
  // member, or a first message needs it; from then on it stays, hidden or
  // not, so a run goes on streaming while another screen is in the center. A
  // link straight to another screen opens none meanwhile.
  const [connected, setConnected] = useState(visible)
  if (!connected && (visible || first !== null)) setConnected(true)
  const draft = chat.drafts[PRIMARY_DRAFT] ?? ""
  const onDraftChange = (text: string) =>
    dispatch({ type: "draft", key: PRIMARY_DRAFT, text })
  // The first messages that were not sent stay in view in whichever
  // conversation's log is open: they belong to none. Edit takes only an empty
  // composer, so what the member typed is never replaced.
  const notSent =
    chat.unsent.length > 0 ? (
      <NotSentMessages
        messages={chat.unsent}
        canEdit={draft.trim() === ""}
        dispatch={dispatch}
      />
    ) : undefined
  // What the open thread's chat says it is doing, and of which thread.
  const [reported, setReported] = useState<{
    threadId: string
    state: ChatState
  }>()

  // The rest of the app (Home) hears what this chat is doing: the thread it
  // has open and whether it works, waits on the member or is idle. A first
  // message counts as running until its conversation has taken it.
  const state: ChatState =
    first && !first.taken
      ? "running"
      : threadId && reported?.threadId === threadId
        ? reported.state
        : "idle"
  const scopeOrganization = scope?.organizationId
  const scopeUser = scope?.userId
  useEffect(() => {
    if (threadId === undefined || !listed) return
    publishPrimaryChatActivity(
      { threadId, state },
      scopeOrganization && scopeUser
        ? { organizationId: scopeOrganization, userId: scopeUser }
        : undefined
    )
  }, [threadId, listed, state, scopeOrganization, scopeUser])
  useEffect(() => () => publishPrimaryChatActivity(null), [])

  // Once the list is here: a last view younger than five minutes, otherwise
  // the newest pending approval, otherwise none (the desk).
  // Unless something else moved the chat while this was asked: the reducer
  // takes a decision only for a chat still undecided.
  useEffect(() => {
    if (threadId !== undefined || !threads.data) return
    if (scope && !agentId) return
    let stale = false
    const remembered = scope && agentId ? rememberedView(scope, agentId) : null
    const now = Date.now()
    void openingThread({
      threads: threads.data,
      choice: remembered?.choice ?? null,
      leftAt: remembered?.leftAt,
      now,
      waiting: (id) =>
        waitsOnMember(queryClient, organizationId, id, scope?.userId),
    }).then((chosen) => {
      if (!stale)
        dispatch({
          type: "decide",
          thread: chosen,
          resumeNew: chosen === null && recentNewView(remembered, now),
        })
    })
    return () => {
      stale = true
    }
  }, [threadId, threads.data, agentId])

  // The choice is not worth a wait without end: the member can type at once
  // (see `OpeningConversation`), and if it takes this long the chat opens
  // blank (the desk). What arrives later finds the chat decided, and changes
  // nothing.
  useEffect(() => {
    if (threadId !== undefined) return
    const timer = setTimeout(
      () => dispatch({ type: "decide", thread: null }),
      OPENING_FALLBACK_MS
    )
    return () => clearTimeout(timer)
  }, [threadId])

  // A first message needs a conversation of its own: this makes it, once,
  // whichever chat is mounted when its turn comes. What it reports is named
  // by the message's id, so it lands where it belongs even if this chat has
  // gone by then (the shell keeps the rest), and nothing it says can change
  // a message that is not the one it made a conversation for.
  const started = useRef<number>(undefined)
  useEffect(() => {
    if (!first || first.threadId || first.making) return
    if (started.current === first.id) return
    started.current = first.id
    const id = first.id
    dispatch({ type: "making", id })
    let gaveUp = false
    const timer = setTimeout(() => {
      gaveUp = true
      dispatch({
        type: "failed",
        id,
        notice: "Couldn't start a conversation. Try again.",
      })
    }, START_TIMEOUT_MS)
    createThread(organizationId).then(
      (thread) => {
        clearTimeout(timer)
        // Gave up on it: the thread stays empty, and no title says so.
        if (gaveUp) return
        queryClient.setQueryData<Thread[]>(list.queryKey, (held) => [
          thread,
          ...(held ?? []).filter((item) => item.id !== thread.id),
        ])
        void queryClient.invalidateQueries({
          queryKey: threadKeys.list(organizationId),
        })
        dispatch({ type: "made", id, threadId: thread.id })
      },
      (error: unknown) => {
        clearTimeout(timer)
        if (gaveUp) return
        dispatch({
          type: "failed",
          id,
          notice: failureText(
            error,
            "Couldn't start a conversation. Try again."
          ),
        })
      }
    )
  })

  // Once its conversation has taken a first message, a moment later the chat
  // moves on to whatever waits behind it (see `SENT_SETTLE_MS`). Whichever
  // chat is mounted then sets the moment going: one that unmounted before
  // its end leaves the message taken, and the next one starts it again.
  useEffect(() => {
    if (!first?.taken) return
    const id = first.id
    const timer = setTimeout(
      () => dispatch({ type: "sent", id }),
      SENT_SETTLE_MS
    )
    return () => clearTimeout(timer)
  }, [first?.id, first?.taken])

  const composer: ComposerHandlers = {
    draft,
    onDraftChange,
    onComposingChange,
  }

  // The message its conversation's chat has yet to take.
  const waiting =
    first && first.threadId === threadId && !first.taken ? first.text : null

  let body: ReactNode = null
  if (threadId && listed) {
    body = connected && (
      <Suspense
        fallback={
          <OpeningConversation
            agentName={agentName}
            pending={waiting}
            notSent={notSent}
            {...composer}
          />
        }
      >
        <ThreadChat
          key={threadId}
          organizationId={organizationId}
          threadId={threadId}
          agentName={agentName}
          pending={waiting}
          notSent={notSent}
          onPendingTaken={() => {
            if (first) dispatch({ type: "taken", id: first.id })
          }}
          pendingScreen={first?.screen}
          getScreenContext={getScreenContext}
          onMissing={() => {
            const notice = "That conversation no longer exists."
            if (first && first.threadId === threadId && !first.taken) {
              // Made for a message that never got in: it goes back to the
              // composer.
              dispatch({ type: "failed", id: first.id, notice })
            } else {
              dispatch({ type: "move", thread: null, notice })
            }
            void queryClient.invalidateQueries({
              queryKey: threadKeys.list(organizationId),
            })
          }}
          onStateChange={(id, next) =>
            setReported({ threadId: id, state: next })
          }
          {...composer}
        />
      </Suspense>
    )
  } else if (threadId === null && first) {
    // A first message on its way, before its conversation exists.
    body = (
      <>
        <AgentConversation agentName={agentName} state="ready">
          {notSent}
          <p className="workspace-agent-message" data-from="member">
            {first.text}
          </p>
        </AgentConversation>
        <AgentComposer
          label={composerLabel(agentName)}
          placeholder={composerLabel(agentName)}
          value={draft}
          onChange={onDraftChange}
          disabled
          status="Connecting…"
          onFocus={() => onComposingChange(true)}
          onBlur={() => onComposingChange(false)}
        />
      </>
    )
  } else if (visible) {
    // No conversation, a chat still choosing, or a conversation whose list
    // is loading. With a desk to show, a chat still choosing shows it unless
    // it is about to reopen a conversation (`chatView`).
    const view = renderBlank
      ? chatView(chat, scope, agentId)
      : threadId === null
        ? "blank"
        : "conversation"
    if (view === "blank") {
      const blank: BlankChat = {
        ...composer,
        onSubmit: (message) =>
          dispatch({
            type: "submit",
            text: message,
            screen: getScreenContext?.(),
          }),
        notSent,
        notice: chat.notice,
      }
      body = renderBlank ? (
        renderBlank(blank)
      ) : (
        <BlankConversation agentName={agentName} {...blank} />
      )
    } else if (view === "conversation") {
      body = (
        <OpeningConversation
          agentName={agentName}
          pending={null}
          notSent={notSent}
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
  }

  return body
}

/**
 * What the center shows for the primary chat: a conversation (open, being
 * made for a first message, or loading), the blank chat (the desk), or
 * nothing yet for the moment the member's agents take to load, before which
 * the chat can't know which it will be. A chat still choosing is blank unless
 * it is about to reopen the conversation the member just left.
 */
export type ChatView = "conversation" | "blank" | "choosing"

export function chatView(
  chat: PrimaryChat,
  scope?: ChatScope,
  agentId?: string
): ChatView {
  const shown = shownThread(chat)
  if (firstMessage(chat) !== null || typeof shown === "string") {
    return "conversation"
  }
  if (shown === null) return "blank"
  if (scope && !agentId) return "choosing"
  return reopensConversation(scope, agentId) ? "conversation" : "blank"
}

/**
 * The chat's own blank state, for a chat shown without a desk: an invitation
 * to start (or the first messages that were not sent) above the composer.
 */
function BlankConversation({
  agentName,
  draft,
  onDraftChange,
  onComposingChange,
  onSubmit,
  notSent,
  notice,
}: BlankChat & { agentName?: string }) {
  return (
    <>
      <AgentConversation
        agentName={agentName}
        state={notSent ? "ready" : "empty"}
        error={notice}
      >
        {notSent}
      </AgentConversation>
      <AgentComposer
        label={composerLabel(agentName)}
        placeholder={composerLabel(agentName)}
        value={draft}
        onChange={onDraftChange}
        onSubmit={onSubmit}
        onFocus={() => onComposingChange(true)}
        onBlur={() => onComposingChange(false)}
      />
    </>
  )
}

/**
 * The first messages that were not sent, each in a bubble ringed as a failed
 * message is, with what can be done about it: Retry sends it into a
 * conversation of its own, Edit puts it in the composer (offered only while
 * that is empty, so nothing the member typed is ever replaced), Dismiss lets
 * it go. Its words are all it has: no conversation was ever made for it.
 */
function NotSentMessages({
  messages,
  canEdit,
  dispatch,
}: {
  messages: readonly UnsentMessage[]
  canEdit: boolean
  dispatch: Dispatch<PrimaryChatAction>
}) {
  const prefix = useId()
  return (
    <>
      {messages.map((message) => {
        const textId = `${prefix}${message.id}`
        return (
          <Fragment key={message.id}>
            <p
              className="workspace-agent-message shadow-[inset_0_0_0_1px_#6b3b39]"
              data-from="member"
              data-status="not-sent"
              id={textId}
            >
              {message.text}
            </p>
            <p className="workspace-agent-error" role="status">
              Not sent. {message.notice}{" "}
              <button
                className="workspace-agent-text-button"
                type="button"
                aria-describedby={textId}
                onClick={() => dispatch({ type: "retry", id: message.id })}
              >
                Retry
              </button>{" "}
              {canEdit && (
                <>
                  <button
                    className="workspace-agent-text-button"
                    type="button"
                    aria-describedby={textId}
                    title="Put it in the message box to change it"
                    onClick={() => dispatch({ type: "edit", id: message.id })}
                  >
                    Edit
                  </button>{" "}
                </>
              )}
              <button
                className="workspace-agent-text-button"
                type="button"
                aria-describedby={textId}
                onClick={() => dispatch({ type: "dismiss", id: message.id })}
              >
                Dismiss
              </button>
            </p>
          </Fragment>
        )
      })}
    </>
  )
}

/**
 * The conversation while it loads: its first message if it has one, and a
 * composer that takes what the member types now and sends it once there is a
 * conversation to send to.
 */
function OpeningConversation({
  agentName,
  pending,
  notSent,
  error,
  onRetry,
  draft,
  onDraftChange,
  onComposingChange,
}: ComposerHandlers & {
  agentName?: string
  pending: string | null
  notSent?: ReactElement
  error?: string
  onRetry?: () => void
}) {
  return (
    <>
      <AgentConversation
        agentName={agentName}
        state={pending || notSent ? "ready" : "loading"}
        error={error}
        onRetry={onRetry}
      >
        {pending ? (
          <p className="workspace-agent-message" data-from="member">
            {pending}
          </p>
        ) : (
          notSent && (
            <p className="workspace-agent-empty">Loading the conversation…</p>
          )
        )}
        {notSent}
      </AgentConversation>
      <AgentComposer
        label={composerLabel(agentName)}
        placeholder={composerLabel(agentName)}
        value={draft}
        onChange={onDraftChange}
        sendDisabled
        status={error ? undefined : "Connecting…"}
        onFocus={() => onComposingChange(true)}
        onBlur={() => onComposingChange(false)}
      />
    </>
  )
}

type Connection = "connecting" | "open" | "reconnecting"

/** The member's latest message, which the log scrolls back to when it changes. */
function lastUserMessageId(
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
  notSent,
  pendingScreen,
  getScreenContext,
  onPendingTaken,
  onMissing,
  draft,
  onDraftChange,
  onComposingChange,
  onStateChange,
}: ComposerHandlers & {
  organizationId: string
  threadId: string
  agentName?: string
  pending: string | null
  /** The first messages that were not sent: shown at the end of the log, though none is this conversation's. */
  notSent?: ReactElement
  pendingScreen?: ScreenContext
  getScreenContext?: () => ScreenContext
  onPendingTaken: () => void
  onMissing: () => void
  /** What this thread is doing: a run streams, a confirmation waits on the member, or neither. */
  onStateChange: (threadId: string, state: ChatState) => void
}) {
  const queryClient = useQueryClient()
  const detail = useQuery(threadDetailQueryOptions(organizationId, threadId))
  const [connection, setConnection] = useState<Connection>("connecting")
  const [runError, setRunError] = useState("")
  const [resending, setResending] = useState(false)
  // Whether the member stopped the run, and whether the socket has been open
  // and quiet for a while: what tells a run that ended from one that is only
  // out of sight (see `ended`).
  const [stopped, setStopped] = useState(false)
  const [settled, setSettled] = useState(false)
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
  // When this conversation was last opened, sent to or heard from.

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
      if (isAbort) setStopped(true)
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

  // Whether the run is over, as far as this chat can tell: the member
  // stopped it, or nothing has streamed while the socket has been open a
  // moment (a run still alive resumes by then). Until then a call still
  // running stays as it was: a socket that drops, or a conversation opened
  // while its run works, is not a run that ended.
  useEffect(() => {
    if (running) setStopped(false)
  }, [running])
  useEffect(() => {
    if (connection !== "open" || running) {
      setSettled(false)
      return
    }
    const timer = setTimeout(() => setSettled(true), RUN_SETTLE_MS)
    return () => clearTimeout(timer)
  }, [connection, running])
  const ended = !running && (stopped || settled)

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
  // A file a run saves joins the conversation's Files at once: Go's copy of
  // the thread is read again as it arrives, not only once the run ends.
  const fileState = useRef<{ seen: Set<string>; ran: boolean }>({
    seen: new Set(),
    ran: false,
  })
  useEffect(() => {
    const state = fileState.current
    if (running) state.ran = true
    const newFile = files.some((file) => !state.seen.has(file.key))
    files.forEach((file) => state.seen.add(file.key))
    if (state.ran && newFile) {
      void queryClient.invalidateQueries({
        queryKey: threadKeys.detail(organizationId, threadId),
      })
    }
    if (!running) state.ran = false
  }, [
    files.map((file) => file.key).join("|"),
    running,
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
        state={
          entries.length > 0 || running || pending || notSent
            ? "ready"
            : "empty"
        }
        error={runError}
        follow={lastMemberMessage}
      >
        <AgentTimeline
          entries={entries}
          running={running}
          ended={ended}
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
        {notSent}
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
        disabled={actionable.size > 0}
        sendDisabled={connection !== "open"}
        streaming={running}
        onStop={() => {
          setStopped(true)
          void chat.stop()
        }}
        status={status}
        onFocus={() => onComposingChange(true)}
        onBlur={() => onComposingChange(false)}
      />
    </>
  )
}
