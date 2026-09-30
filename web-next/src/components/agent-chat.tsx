import {
  Suspense,
  useEffect,
  useEffectEvent,
  useMemo,
  useRef,
  useState,
} from "react"
import type { Dispatch } from "react"
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
  FRESH_CONVERSATION_MS,
  PRIMARY_DRAFT,
  firstMessage,
  openingThread,
  publishPrimaryChatActivity,
  rememberedChoice,
  shownThread,
} from "@/lib/threads/chat-choice"
import type {
  ChatScope,
  PrimaryChat,
  PrimaryChatAction,
  PrimaryChatActivity,
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
 *
 * What it is on, what waits to be opened or sent, and the unsent messages are
 * the panel's (`chat`, moved by `dispatch`), so they outlast a switch to
 * another agent and every step names the request it belongs to.
 */
export function AgentChat({
  organizationId,
  scope,
  agentId,
  agentName,
  chat,
  dispatch,
  onComposingChange,
  onOpenOverview,
  inOverview = false,
  onFileCreated,
  getScreenContext,
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
  onOpenOverview?: () => void
  inOverview?: boolean
  onFileCreated?: (file: {
    threadId: string
    name: string
    version?: number
  }) => void
  getScreenContext?: () => ScreenContext
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
  const first = firstMessage(chat)
  const draft = chat.drafts[PRIMARY_DRAFT] ?? ""
  const onDraftChange = (text: string) =>
    dispatch({ type: "draft", key: PRIMARY_DRAFT, text })
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

  // Once the list is here: the member's choice, else the latest conversation
  // while it is fresh, else (after asking Go) one that still waits on them.
  // Unless something else moved the chat while this was asked: the reducer
  // takes a decision only for a chat still undecided.
  useEffect(() => {
    if (threadId !== undefined || !threads.data) return
    if (scope && !agentId) return
    let stale = false
    void openingThread({
      threads: threads.data,
      choice: scope && agentId ? rememberedChoice(scope, agentId) : null,
      now: Date.now(),
      waiting: (id) => waitsOnMember(queryClient, organizationId, id),
    }).then((chosen) => {
      if (!stale) dispatch({ type: "decide", thread: chosen })
    })
    return () => {
      stale = true
    }
  }, [threadId, threads.data, agentId])

  // The choice is not worth a wait without end: the member can type at once
  // (see `OpeningConversation`), and if it takes this long a new chat opens.
  // What arrives later finds the chat decided, and changes nothing.
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
  // gone by then (the panel keeps the rest), and nothing it says can change
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
  const title =
    (threadId &&
      threads.data?.find((thread) => thread.id === threadId)?.title) ||
    "New conversation"

  function startNew() {
    dispatch({ type: "move", thread: null })
  }

  // The message its conversation's chat has yet to take.
  const waiting =
    first && first.threadId === threadId && !first.taken ? first.text : null

  let body
  if (threadId && listed) {
    body = (
      <Suspense
        fallback={
          <OpeningConversation
            agentName={agentName}
            pending={waiting}
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
          onFileCreated={onFileCreated}
          onIdle={startNew}
          inOverview={inOverview}
          touched={chat.touched}
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
          state={first || chat.unsent ? "ready" : "empty"}
          error={chat.notice}
        >
          {(first?.text ?? chat.unsent) && (
            <p className="workspace-agent-message" data-from="member">
              {first?.text ?? chat.unsent}
            </p>
          )}
        </AgentConversation>
        <AgentComposer
          label={composerLabel(agentName)}
          placeholder={composerLabel(agentName)}
          value={draft}
          onChange={onDraftChange}
          onSubmit={(message) =>
            dispatch({
              type: "submit",
              text: message,
              screen: getScreenContext?.(),
            })
          }
          disabled={first !== null}
          status={first ? "Connecting…" : undefined}
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
          disabled={!threadId || first !== null}
          onClick={startNew}
        >
          <SquarePen size={15} strokeWidth={1.7} />
        </button>
      </div>
      {body}
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
  onIdle,
  inOverview,
  touched,
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
  /** The member came back to a conversation left alone for an hour: the chat moves on to a new one. */
  onIdle: () => void
  /** The focused Agent overview is open, with its viewer or editor: the member is at work in it. */
  inOverview: boolean
  /** Counts what the member or the app just did here (a request, a first message), such as opening the conversation already open. */
  touched: number
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
  const lastActivity = useRef(Date.now())

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
    lastActivity.current = Date.now()
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
    lastActivity.current = Date.now()
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
  // A tab left open overnight comes back to a new chat rather than
  // yesterday's: when it shows again (its tab visible, its window focused)
  // after an hour with nothing done here, and nothing streaming or waiting
  // on the member. Nothing is deleted. What counts as something done is what
  // the member does anywhere in the app: sending, hearing an answer, opening
  // this conversation (even when it is already open: a request for it), and
  // pointer and keyboard in the page. A file's viewer or editor is a frame of
  // its own that takes the pointer and the keys, and focus coming back from it
  // is not the member returning: while the focused Agent overview is open the
  // chat is never left, and closing it starts the hour afresh.
  // How much the conversation holds, so that a reconnect handing over the
  // same messages again is not taken for something heard.
  const said = useMemo(
    () =>
      chat.messages.reduce((total, item) => total + 1 + item.parts.length, 0),
    [chat.messages]
  )
  useEffect(() => {
    lastActivity.current = Date.now()
  }, [said, running, touched, inOverview])
  const cameBack = useEffectEvent(() => {
    if (document.visibilityState === "hidden") return
    if (running || actionable.size > 0 || pending || inOverview) return
    if (Date.now() - lastActivity.current < FRESH_CONVERSATION_MS) return
    onIdle()
  })
  useEffect(() => {
    const onReturn = () => cameBack()
    const onActivity = () => {
      lastActivity.current = Date.now()
    }
    document.addEventListener("visibilitychange", onReturn)
    window.addEventListener("focus", onReturn)
    const options = { capture: true, passive: true }
    for (const type of ["pointerdown", "keydown", "wheel"]) {
      document.addEventListener(type, onActivity, options)
    }
    return () => {
      document.removeEventListener("visibilitychange", onReturn)
      window.removeEventListener("focus", onReturn)
      for (const type of ["pointerdown", "keydown", "wheel"]) {
        document.removeEventListener(type, onActivity, options)
      }
    }
  }, [])

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
