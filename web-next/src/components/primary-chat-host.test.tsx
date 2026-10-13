// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  act,
  cleanup,
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { StrictMode } from "react"
import type { ReactElement } from "react"
import { ORG, VIEWER, fakeAgentsBackend, primary } from "./agents-fake-backend"
import { AgentChat } from "./agent-chat"
import { PrimaryChatHistory } from "./agent-chat-history"
import { ConversationHeader } from "./conversation-header"
import { usePrimaryChatHost } from "./primary-chat-host"
import {
  MAX_WAITING_MESSAGES,
  claimPrimaryChatRequest,
  rememberedChoice,
  rememberChoice,
  requestPrimaryChat,
  usePrimaryChatActivity,
  usePrimaryChatRequest,
} from "@/lib/threads/chat-choice"
import { itemsToChatMessages } from "@/lib/threads/chat-messages"
import { threadKeys } from "@/lib/threads/client"
import type { Item, Thread, ThreadDetail } from "@/lib/threads/client"
import { agentChats } from "@/test/agent-chat"
import { roomSockets } from "@/test/room-socket"

// The shell holds the member's primary chat whatever the center shows: its
// conversation, what waits to be opened or sent, the draft and the unsent
// messages (`usePrimaryChatHost`), and keeps the chat mounted, hidden while
// another screen is in the center. It answers the rest of the app's requests
// to open a conversation. Its sockets and chats are stand-ins (src/test); Go
// is the in-memory agents service, with the member's primary threads added
// here.
vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
)
vi.mock(
  "@cloudflare/ai-chat/react",
  async () => (await import("@/test/agent-chat")).aiChatReact
)

const HOUR = 60 * 60 * 1000
const scope = { organizationId: ORG, userId: VIEWER }

/** A conversation last active `age` ago (a minute by default: fresh). */
function thread(id: string, title: string, age = 60_000): Thread {
  return { id, title, updated_at: new Date(Date.now() - age).toISOString() }
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

type Seed = {
  threads?: Thread[]
  /** Each member's own conversations, by user id, for a page more than one member uses in turn. */
  lists?: Record<string, Thread[]>
  /** What each thread holds in Go. */
  items?: Record<string, Item[]>
  pending?: Record<string, string[]>
  /** The first thread made; later ones are `th_new_2`, `th_new_3` and so on. */
  created?: Thread
  /** Transport fixtures resume their own explicitly viewed conversation. */
  rememberLatest?: boolean
}

/** A call that starts a conversation. */
const START = `POST /v1/organizations/${ORG}/threads`

/**
 * Go for the primary chat's routes (its threads), and the agents service for
 * everything else. `holdCreate` keeps the next thread from being made until
 * it is released; `failNextCreate` makes the next start fail.
 */
function stubGo(seed: Seed = {}) {
  if (seed.rememberLatest !== false) {
    for (const [userId, list] of Object.entries(
      seed.lists ?? { [VIEWER]: seed.threads ?? [] }
    )) {
      const last = list.find(
        (item) =>
          item.title.trim() && Date.now() - Date.parse(item.updated_at) < HOUR
      )
      if (last)
        rememberChoice({ organizationId: ORG, userId }, primary.id, last.id)
    }
  }
  const backend = fakeAgentsBackend({ agents: [primary] })
  let threads = seed.threads ?? []
  let made = 0
  let failing = 0
  let viewer = VIEWER
  const calls: string[] = []
  let gate: Promise<void> | undefined
  let agentsGate: Promise<void> | undefined
  let listGate: Promise<void> | undefined
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const method = init?.method ?? "GET"
      const path = url.pathname
      calls.push(`${method} ${path}`)
      if (path === `/v1/organizations/${ORG}/threads`) {
        if (method === "POST") {
          await gate
          if (failing > 0) {
            failing -= 1
            return json(
              {
                error: {
                  code: "unavailable",
                  message: "The agents service is down.",
                },
              },
              503
            )
          }
          made += 1
          const created =
            made === 1 && seed.created
              ? seed.created
              : thread(made === 1 ? "th_new" : `th_new_${made}`, "")
          threads = [created, ...threads]
          return json(created, 201)
        }
        await listGate
        return json({ threads: seed.lists?.[viewer] ?? threads })
      }
      const detail = new RegExp(`^/v1/organizations/${ORG}/threads/([^/]+)$`)
      const found = detail.exec(path)
      if (found) {
        const id = decodeURIComponent(found[1])
        const listed = (seed.lists?.[viewer] ?? threads).find(
          (item) => item.id === id
        )
        if (!listed) {
          return json({ error: { code: "not_found", message: "no" } }, 404)
        }
        const body: ThreadDetail = {
          ...listed,
          items: seed.items?.[id] ?? [],
          pending_confirmations: seed.pending?.[id] ?? [],
        }
        return json(body)
      }
      if (
        path.startsWith("/agents/thread-chat-agent/") &&
        path.endsWith("/get-messages")
      ) {
        return json([])
      }
      if (path === `/v1/organizations/${ORG}/agents`) await agentsGate
      return backend.fetch(String(input), init)
    })
  )
  return {
    backend,
    calls,
    holdCreate() {
      let release = () => {}
      gate = new Promise<void>((resolve) => {
        release = resolve
      })
      return () => {
        gate = undefined
        release()
      }
    },
    /** The next conversation to be started fails. */
    failNextCreate() {
      failing += 1
    },
    /** From now on the requests are another member's (their own conversations answer). */
    as(userId: string) {
      viewer = userId
    },
    /** Keeps the conversation list from answering until released. */
    holdList() {
      let release = () => {}
      listGate = new Promise<void>((resolve) => {
        release = resolve
      })
      return () => {
        listGate = undefined
        release()
      }
    },
    /** Keeps the agents list from answering until released. */
    holdAgents() {
      let release = () => {}
      agentsGate = new Promise<void>((resolve) => {
        release = resolve
      })
      return () => {
        agentsGate = undefined
        release()
      }
    },
  }
}

type HostProps = {
  organizationId?: string
  viewerId?: string
  /** Whether the center shows the member's agent: false while it shows another screen. */
  visible?: boolean
  /** The sidebar's Recent list is shown too. */
  history?: boolean
}

/** The chat as the shell hosts it: its header, the Recent list when asked, and the chat itself. */
function Host({
  organizationId,
  viewerId,
  visible,
  history,
}: HostProps & { visible: boolean; history: boolean }) {
  const held = usePrimaryChatHost({ organizationId, viewerId })
  if (!organizationId) return null
  return (
    <>
      <ConversationHeader
        organizationId={organizationId}
        scope={held.scope}
        chat={held.chat}
        dispatch={held.dispatch}
      />
      {history && (
        <PrimaryChatHistory
          organizationId={organizationId}
          scope={held.scope}
          chat={held.chat}
          dispatch={held.dispatch}
          current={visible}
        />
      )}
      <AgentChat
        key={held.owner}
        organizationId={organizationId}
        scope={held.scope}
        visible={visible}
        agentId={held.agentId}
        agentName={held.agentName}
        chat={held.chat}
        dispatch={held.dispatch}
        onComposingChange={vi.fn()}
      />
    </>
  )
}

function withQueries(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const wrap = (element: ReactElement) => (
    <QueryClientProvider client={client}>{element}</QueryClientProvider>
  )
  const view = render(wrap(ui))
  return {
    ...view,
    client,
    rerender: (next: ReactElement) => view.rerender(wrap(next)),
  }
}

/** The shell's chat for the member of `ORG`, unless told another member or none. */
function host(props: HostProps = {}) {
  return (
    <Host
      organizationId={"organizationId" in props ? props.organizationId : ORG}
      viewerId={"viewerId" in props ? props.viewerId : VIEWER}
      visible={props.visible ?? true}
      history={props.history ?? false}
    />
  )
}

const leoBox = () =>
  screen.getByRole<HTMLTextAreaElement>("textbox", { name: "Message Leo" })
const blank = "Ask anything to start a conversation."
const title = () => document.querySelector("h1")?.textContent

function chatName(threadId: string) {
  return `${ORG}~${threadId}`
}

/** The latest socket on a chat object, once one has been asked for. */
async function socketOn(name: string) {
  await waitFor(() => expect(roomSockets.named(name).length).toBeGreaterThan(0))
  return roomSockets.named(name).at(-1)!
}

function type(box: HTMLTextAreaElement, text: string) {
  fireEvent.change(box, { target: { value: text } })
}

/** The words of every message in the log that was not sent, in order. */
const notSent = () =>
  Array.from(
    screen.getByRole("log").querySelectorAll('[data-status="not-sent"]')
  ).map((message) => message.textContent)
const down = "Not sent. The agents service is down."

beforeEach(() => {
  // Requests an earlier test left unclaimed must not reach this one.
  const { result, unmount } = renderHook(() => usePrimaryChatRequest())
  while (result.current) {
    const { id } = result.current
    act(() => {
      claimPrimaryChatRequest(id)
    })
  }
  unmount()
  roomSockets.reset()
  agentChats.reset()
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  window.sessionStorage.clear()
})

describe("what the chat keeps while another screen is in the center", () => {
  it("keeps the chat's conversation, so hiding it does not decide it again", async () => {
    // No member, so nothing is remembered in the browser: the shell alone
    // holds where the chat was.
    stubGo({ threads: [thread("th_1", "Plan the launch")] })
    const view = withQueries(host({ viewerId: undefined, history: true }))
    fireEvent.click(
      await screen.findByRole("button", { name: /^Plan the launch/ })
    )
    ;(await socketOn(chatName("th_1"))).open()
    await screen.findByRole("textbox", { name: "Message Leo" })

    const sockets = roomSockets.named(chatName("th_1")).length

    // A new conversation, though a fresh one exists.
    fireEvent.click(screen.getByRole("button", { name: "New conversation" }))
    expect(screen.getByText(blank)).toBeTruthy()

    view.rerender(host({ viewerId: undefined, visible: false }))
    view.rerender(host({ viewerId: undefined, visible: true }))

    // Still the new conversation, not the fresh one the rules would pick.
    expect(await screen.findByText(blank)).toBeTruthy()
    expect(leoBox().hasAttribute("disabled")).toBe(false)
    expect(roomSockets.named(chatName("th_1"))).toHaveLength(sockets)
  })

  it("keeps the member's unsent message while another screen is shown", async () => {
    stubGo({ threads: [thread("th_1", "Plan the launch")] })
    const view = withQueries(host())
    ;(await socketOn(chatName("th_1"))).open()
    await screen.findByRole("textbox", { name: "Message Leo" })
    type(leoBox(), "Half a thought")

    view.rerender(host({ visible: false }))
    view.rerender(host({ visible: true }))
    expect(leoBox().value).toBe("Half a thought")
  })

  it("keeps the open conversation's socket, and its run, while hidden", async () => {
    stubGo({ threads: [thread("th_1", "Plan the launch")] })
    const view = withQueries(host())
    const socket = await socketOn(chatName("th_1"))
    socket.open()
    agentChats.named(chatName("th_1")).update({ status: "streaming" })
    const activity = renderHook(() => usePrimaryChatActivity(scope))
    await waitFor(() =>
      expect(activity.result.current).toEqual({
        threadId: "th_1",
        state: "running",
      })
    )

    view.rerender(host({ visible: false }))
    expect(roomSockets.named(chatName("th_1"))).toHaveLength(1)
    expect(activity.result.current).toEqual({
      threadId: "th_1",
      state: "running",
    })
  })

  it("sends a first message into its own conversation though the chat was hidden before it was made", async () => {
    const go = stubGo({ threads: [] })
    const view = withQueries(host())
    await screen.findByText(blank)
    await screen.findByRole("textbox", { name: "Message Leo" })
    const release = go.holdCreate()

    type(leoBox(), "Plan the launch")
    fireEvent.keyDown(leoBox(), { key: "Enter" })
    await waitFor(() => expect(go.calls).toContain(START))
    view.rerender(host({ visible: false }))

    // Back with the chat while the conversation is still being made: the
    // message is still on its way, in view, and the composer does not take a
    // second one over it.
    view.rerender(host({ visible: true }))
    expect(
      screen.getByRole("log").querySelector('[data-from="member"]')?.textContent
    ).toBe("Plan the launch")
    expect(leoBox().hasAttribute("disabled")).toBe(true)

    // Once it exists, the message goes into it: nothing was put back into a
    // composer, or sent astray.
    act(() => release())
    const socket = await socketOn(chatName("th_new"))
    socket.open()
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).toHaveBeenCalledExactlyOnceWith({ text: "Plan the launch" })
    expect(leoBox().value).toBe("")
  })

  it("sends a first message whose conversation is made while another screen is shown", async () => {
    const go = stubGo({ threads: [] })
    const view = withQueries(host())
    await screen.findByRole("textbox", { name: "Message Leo" })
    const release = go.holdCreate()
    type(leoBox(), "Plan the launch")
    fireEvent.keyDown(leoBox(), { key: "Enter" })
    await waitFor(() => expect(go.calls).toContain(START))

    // The member goes to Requests before the conversation exists.
    view.rerender(host({ visible: false }))
    act(() => release())
    ;(await socketOn(chatName("th_new"))).open()
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).toHaveBeenCalledExactlyOnceWith({ text: "Plan the launch" })
  })
})

describe("a link straight to another screen", () => {
  it("chooses the conversation, but opens no socket until the chat is shown", async () => {
    stubGo({ threads: [thread("th_1", "Plan the launch")] })
    const view = withQueries(host({ visible: false }))
    await waitFor(() => expect(title()).toBe("Plan the launch"))
    await act(async () => {})
    expect(roomSockets.all).toHaveLength(0)

    view.rerender(host({ visible: true }))
    await socketOn(chatName("th_1"))
  })
})

describe("a first message on its way", () => {
  it("holds a request behind it, then opens what the request asked for", async () => {
    stubGo({ threads: [thread("th_b", "Somewhere else", 3 * HOUR)] })
    withQueries(host())
    await screen.findByText(blank)
    await screen.findByRole("textbox", { name: "Message Leo" })

    type(leoBox(), "Plan the launch")
    fireEvent.keyDown(leoBox(), { key: "Enter" })
    const socket = await socketOn(chatName("th_new"))

    // A link opens another conversation while the message waits for it.
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_b" }))
    await act(async () => {})
    expect(roomSockets.named(chatName("th_b"))).toHaveLength(0)

    // The message goes where it was written; then the request has its turn.
    socket.open()
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).toHaveBeenCalledExactlyOnceWith({ text: "Plan the launch" })
    await socketOn(chatName("th_b"))
    expect(
      agentChats.named(chatName("th_b")).sendMessage
    ).not.toHaveBeenCalled()
    expect(leoBox().value).toBe("")
    // The socket is made during render; the choice is saved in a passive
    // effect that may flush after waitFor saw the socket.
    await waitFor(() =>
      expect(rememberedChoice(scope, primary.id)).toBe("th_b")
    )
  })

  it("keeps the member from starting another conversation over it, until its conversation has it", async () => {
    stubGo({ threads: [] })
    withQueries(host())
    await screen.findByText(blank)
    await screen.findByRole("textbox", { name: "Message Leo" })
    type(leoBox(), "Plan the launch")
    fireEvent.keyDown(leoBox(), { key: "Enter" })
    const socket = await socketOn(chatName("th_new"))

    const button = () =>
      screen.getByRole("button", { name: "New conversation" })
    expect(button().hasAttribute("disabled")).toBe(true)
    fireEvent.click(button())
    socket.open()
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).toHaveBeenCalledExactlyOnceWith({ text: "Plan the launch" })
    await waitFor(() => expect(button().hasAttribute("disabled")).toBe(false))
  })
})

describe("a member who takes over the page", () => {
  const other = "user-other"
  const otherScope = { organizationId: ORG, userId: other }

  it("gets nothing of the last member's chat: not their conversation, draft or agents", async () => {
    const go = stubGo({
      lists: {
        [VIEWER]: [thread("th_a", "Their plan")],
        [other]: [thread("th_b", "Your plan")],
      },
    })
    const view = withQueries(host())
    ;(await socketOn(chatName("th_a"))).open()
    await screen.findByRole("textbox", { name: "Message Leo" })
    type(leoBox(), "Their draft")
    const sockets = roomSockets.all.length
    const seen = renderHook(() => usePrimaryChatActivity(otherScope))

    // Another tab signed in as someone else, and this one read the session
    // again: same page, same cache, same organization.
    go.as(other)
    const releaseList = go.holdList()
    const releaseAgents = go.holdAgents()
    view.rerender(host({ viewerId: other }))
    await act(async () => {})
    // Not their agent's name, their conversation or their draft, and nothing
    // for this member to hear of theirs.
    expect(screen.queryByRole("textbox", { name: "Message Leo" })).toBeNull()
    expect(screen.queryByText("Their plan")).toBeNull()
    expect(seen.result.current).toBeNull()
    expect(roomSockets.all).toHaveLength(sockets)
    expect(screen.queryByDisplayValue("Their draft")).toBeNull()

    // Their own list decides, once it has loaded.
    act(() => {
      releaseAgents()
      releaseList()
    })
    ;(await socketOn(chatName("th_b"))).open()
    expect(roomSockets.all.length).toBe(sockets + 1)
    expect(leoBox().value).toBe("")
    await waitFor(() =>
      expect(seen.result.current).toEqual({ threadId: "th_b", state: "idle" })
    )
    // The last member's remembered choice stays theirs.
    expect(rememberedChoice(otherScope, primary.id)).toBe("th_b")
  })

  it("does not carry a message the last member could not send", async () => {
    const go = stubGo({ threads: [] })
    go.failNextCreate()
    const view = withQueries(host())
    await screen.findByText(blank)
    await screen.findByRole("textbox", { name: "Message Leo" })
    act(() =>
      requestPrimaryChat(scope, { kind: "new", message: "Their private words" })
    )
    expect(await screen.findByText(down)).toBeTruthy()

    go.as(other)
    view.rerender(host({ viewerId: other }))
    expect(screen.queryByText("Their private words")).toBeNull()
    expect(screen.queryByText(down)).toBeNull()
    await screen.findByRole("textbox", { name: "Message Leo" })
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
  })

  it("keeps a member's chat through a moment the session couldn't be read", async () => {
    stubGo({ threads: [thread("th_a", "Their plan")] })
    const view = withQueries(host())
    ;(await socketOn(chatName("th_a"))).open()
    await screen.findByRole("textbox", { name: "Message Leo" })
    type(leoBox(), "Still here")

    // The session could not be read for a moment, then it was, unchanged.
    view.rerender(host({ organizationId: undefined, viewerId: undefined }))
    view.rerender(host())
    expect(leoBox().value).toBe("Still here")
  })
})

describe("a request to open a conversation", () => {
  it("opens the conversation it names on the primary agent, and remembers the choice", async () => {
    stubGo({
      threads: [thread("th_2", "Newer"), thread("th_1", "Older", 3 * HOUR)],
    })
    withQueries(host())
    ;(await socketOn(chatName("th_2"))).open()
    await screen.findByRole("textbox", { name: "Message Leo" })

    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_1" }))

    await socketOn(chatName("th_1"))
    expect(title()).toBe("Older")
    expect(rememberedChoice(scope, primary.id)).toBe("th_1")
  })

  it("applies a request made while the chat was hidden, once it is shown", async () => {
    stubGo({
      threads: [thread("th_2", "Newer"), thread("th_1", "Older", 3 * HOUR)],
    })
    const view = withQueries(host({ visible: false }))
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_1" }))
    // The chat has not been shown: no socket yet.
    await waitFor(() => expect(title()).toBe("Older"))
    expect(roomSockets.all).toHaveLength(0)

    view.rerender(host({ visible: true }))
    await socketOn(chatName("th_1"))
    // It never opened the fresh conversation first.
    expect(roomSockets.named(chatName("th_2"))).toHaveLength(0)
    expect(rememberedChoice(scope, primary.id)).toBe("th_1")
  })

  it("applies a request made before the shell first mounted", async () => {
    stubGo({
      threads: [thread("th_2", "Newer"), thread("th_1", "Older", 3 * HOUR)],
    })
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_1" }))
    withQueries(host())
    await socketOn(chatName("th_1"))
    expect(roomSockets.named(chatName("th_2"))).toHaveLength(0)
  })

  it("acts on a request once: nothing re-applies it when the shell renders or mounts again", async () => {
    stubGo({ threads: [thread("th_1", "Older", 3 * HOUR)] })
    const view = withQueries(host())
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_1" }))
    ;(await socketOn(chatName("th_1"))).open()

    // The member goes to a new conversation instead.
    fireEvent.click(screen.getByRole("button", { name: "New conversation" }))
    expect(screen.getByText(blank)).toBeTruthy()
    view.rerender(host({ visible: true }))
    expect(screen.getByText(blank)).toBeTruthy()

    // A shell mounted afresh does not open it again.
    view.unmount()
    roomSockets.reset()
    withQueries(host())
    expect(await screen.findByText(blank)).toBeTruthy()
    expect(roomSockets.all).toHaveLength(0)
  })

  it("starts a new conversation with the message a request brings, once, as the composer would", async () => {
    const go = stubGo({ threads: [thread("th_1", "Plan the launch")] })
    withQueries(host())
    ;(await socketOn(chatName("th_1"))).open()

    act(() =>
      requestPrimaryChat(scope, {
        kind: "new",
        message: "  What changed today?  ",
      })
    )
    await waitFor(() => expect(go.calls).toContain(START))
    const socket = await socketOn(chatName("th_new"))
    // Its words wait in view for the new thread's socket, as a typed
    // message's do.
    expect(
      screen.getByRole("log").querySelector('[data-from="member"]')?.textContent
    ).toBe("What changed today?")
    const chat = agentChats.named(chatName("th_new"))
    expect(chat.sendMessage).not.toHaveBeenCalled()

    socket.open()
    expect(chat.sendMessage).toHaveBeenCalledExactlyOnceWith({
      text: "What changed today?",
    })
    socket.open()
    expect(chat.sendMessage).toHaveBeenCalledOnce()
    expect(rememberedChoice(scope, primary.id)).toBe("th_new")

    // Nothing waits to be sent into the next new conversation.
    fireEvent.click(screen.getByRole("button", { name: "New conversation" }))
    await act(async () => {})
    expect(
      go.calls.filter((call) => call.startsWith("POST /v1/organizations/"))
    ).toHaveLength(1)
  })

  it("leaves what the member had typed alone when a request brings a message", async () => {
    stubGo({ threads: [] })
    withQueries(host())
    await screen.findByText(blank)
    await screen.findByRole("textbox", { name: "Message Leo" })
    type(leoBox(), "Half a thought")

    act(() =>
      requestPrimaryChat(scope, { kind: "new", message: "What changed today?" })
    )
    const socket = await socketOn(chatName("th_new"))
    socket.open()
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).toHaveBeenCalledExactlyOnceWith({ text: "What changed today?" })
    expect(leoBox().value).toBe("Half a thought")
  })

  it("makes each of two quick requests' messages a conversation of its own, sends both, and ends on the second's", async () => {
    const go = stubGo({ threads: [thread("th_old", "Old", 5 * HOUR)] })
    withQueries(host())
    await screen.findByRole("textbox", { name: "Message Leo" })
    const release = go.holdCreate()

    // The second arrives while the first's conversation is still being made.
    act(() =>
      requestPrimaryChat(scope, { kind: "new", message: "First question" })
    )
    await waitFor(() =>
      expect(go.calls.filter((call) => call === START)).toHaveLength(1)
    )
    act(() =>
      requestPrimaryChat(scope, { kind: "new", message: "Second question" })
    )
    await act(async () => {})
    expect(go.calls.filter((call) => call === START)).toHaveLength(1)

    // The first is made a conversation and sent into it.
    act(() => release())
    ;(await socketOn(chatName("th_new"))).open()
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).toHaveBeenCalledExactlyOnceWith({ text: "First question" })

    // Then the second has its own, and the chat ends on it.
    ;(await socketOn(chatName("th_new_2"))).open()
    expect(
      agentChats.named(chatName("th_new_2")).sendMessage
    ).toHaveBeenCalledExactlyOnceWith({ text: "Second question" })
    expect(go.calls.filter((call) => call === START)).toHaveLength(2)
    expect(roomSockets.all.at(-1)?.options.name).toBe(chatName("th_new_2"))
    await waitFor(() =>
      expect(rememberedChoice(scope, primary.id)).toBe("th_new_2")
    )
    // Neither message went into the other's conversation.
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).toHaveBeenCalledOnce()
  })

  it("keeps every request made while the agents list loads, and makes and sends each into a conversation of its own, in order", async () => {
    const go = stubGo({ threads: [thread("th_old", "Old", 5 * HOUR)] })
    const release = go.holdAgents()
    withQueries(host())
    act(() =>
      requestPrimaryChat(scope, { kind: "new", message: "First question" })
    )
    act(() =>
      requestPrimaryChat(scope, { kind: "new", message: "Second question" })
    )
    await act(async () => {})
    // The chat takes none until it knows the agent: nothing is made yet.
    expect(go.calls.filter((call) => call === START)).toHaveLength(0)
    expect(roomSockets.all).toHaveLength(0)

    act(() => release())
    ;(await socketOn(chatName("th_new"))).open()
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).toHaveBeenCalledExactlyOnceWith({ text: "First question" })
    ;(await socketOn(chatName("th_new_2"))).open()
    expect(
      agentChats.named(chatName("th_new_2")).sendMessage
    ).toHaveBeenCalledExactlyOnceWith({ text: "Second question" })

    expect(go.calls.filter((call) => call === START)).toHaveLength(2)
    expect(roomSockets.all.at(-1)?.options.name).toBe(chatName("th_new_2"))
    await waitFor(() =>
      expect(rememberedChoice(scope, primary.id)).toBe("th_new_2")
    )
    expect(notSent()).toEqual([])
    // Neither went into the other's conversation, and each went once.
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).toHaveBeenCalledOnce()
  })

  it("is not held up by a request for another member", async () => {
    stubGo({
      threads: [thread("th_2", "Newer"), thread("th_1", "Older", 3 * HOUR)],
    })
    act(() =>
      requestPrimaryChat(
        { organizationId: ORG, userId: "someone-else" },
        { kind: "thread", threadId: "th_2" }
      )
    )
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_1" }))
    withQueries(host())
    await socketOn(chatName("th_1"))
    expect(roomSockets.named(chatName("th_2"))).toHaveLength(0)
    expect(rememberedChoice(scope, primary.id)).toBe("th_1")
  })

  it("refuses the message after the last it has room for, and keeps its words as not sent", async () => {
    const go = stubGo({ threads: [] })
    const releaseAgents = go.holdAgents()
    // The first conversation is never made here, so what follows only waits.
    go.holdCreate()
    withQueries(host())
    for (let count = 1; count <= MAX_WAITING_MESSAGES + 1; count += 1) {
      act(() =>
        requestPrimaryChat(scope, { kind: "new", message: `Message ${count}` })
      )
    }
    act(() => releaseAgents())

    expect(
      await screen.findByText("Not sent. Too many messages were waiting.")
    ).toBeTruthy()
    expect(notSent()).toEqual([`Message ${MAX_WAITING_MESSAGES + 1}`])
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy()
    // The messages ahead of it are all still on their way, in one line.
    expect(go.calls.filter((call) => call === START)).toHaveLength(1)
    expect(
      screen
        .getByRole("log")
        .querySelector('[data-from="member"]:not([data-status])')?.textContent
    ).toBe("Message 1")
  })

  it("keeps a request's words as not sent when its conversation can't be made, and leaves an empty composer empty", async () => {
    const go = stubGo({ threads: [] })
    go.failNextCreate()
    withQueries(host())
    await screen.findByText(blank)
    await screen.findByRole("textbox", { name: "Message Leo" })

    act(() =>
      requestPrimaryChat(scope, { kind: "new", message: "What changed today?" })
    )
    expect(await screen.findByText(down)).toBeTruthy()
    expect(notSent()).toEqual(["What changed today?"])
    expect(leoBox().value).toBe("")
    expect(roomSockets.all).toHaveLength(0)
  })

  it("keeps a request's words as not sent, and leaves the composer alone, when the composer holds something", async () => {
    const go = stubGo({ threads: [] })
    go.failNextCreate()
    withQueries(host())
    await screen.findByText(blank)
    await screen.findByRole("textbox", { name: "Message Leo" })
    type(leoBox(), "Half a thought")

    act(() =>
      requestPrimaryChat(scope, { kind: "new", message: "What changed today?" })
    )
    expect(await screen.findByText(down)).toBeTruthy()
    expect(leoBox().value).toBe("Half a thought")
    // Nothing hides what could not be sent.
    expect(notSent()).toEqual(["What changed today?"])
  })

  it("keeps a message that failed in view beside the member's draft, through the next request and a move to another conversation, and sends it again into a conversation of its own", async () => {
    const go = stubGo({ threads: [thread("th_old", "Old plan")] })
    withQueries(host())
    ;(await socketOn(chatName("th_old"))).open()
    await screen.findByRole("textbox", { name: "Message Leo" })
    type(leoBox(), "Half a thought")
    const release = go.holdCreate()
    go.failNextCreate()

    // The first request's conversation can't be made. The second arrives
    // while that is tried, and is made and sent after it.
    act(() =>
      requestPrimaryChat(scope, { kind: "new", message: "Failed first" })
    )
    await waitFor(() =>
      expect(go.calls.filter((call) => call === START)).toHaveLength(1)
    )
    act(() =>
      requestPrimaryChat(scope, { kind: "new", message: "Following one" })
    )
    act(() => release())
    ;(await socketOn(chatName("th_new"))).open()
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).toHaveBeenCalledExactlyOnceWith({ text: "Following one" })

    // The draft is the member's still; the first message is in view, unsent.
    expect(leoBox().value).toBe("Half a thought")
    expect(await screen.findByText(down)).toBeTruthy()
    expect(notSent()).toEqual(["Failed first"])
    expect(
      agentChats.all.flatMap((chat) =>
        chat.sendMessage.mock.calls.map(([message]) => message)
      )
    ).toEqual([{ text: "Following one" }])

    // Another request, and another conversation, do not push it out.
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_old" }))
    await waitFor(() => expect(title()).toBe("Old plan"))
    expect(notSent()).toEqual(["Failed first"])
    fireEvent.click(screen.getByRole("button", { name: "New conversation" }))
    expect(notSent()).toEqual(["Failed first"])
    expect(leoBox().value).toBe("Half a thought")

    // Retry sends it into a conversation of its own, and it is no longer listed.
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))
    ;(await socketOn(chatName("th_new_2"))).open()
    expect(
      agentChats.named(chatName("th_new_2")).sendMessage
    ).toHaveBeenCalledExactlyOnceWith({ text: "Failed first" })
    expect(notSent()).toEqual([])
    expect(leoBox().value).toBe("Half a thought")
    expect(go.calls.filter((call) => call === START)).toHaveLength(3)
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).toHaveBeenCalledOnce()
  })

  it("counts a request for the conversation already open as the member being there", async () => {
    vi.useFakeTimers({ toFake: ["Date"] })
    let visibility: DocumentVisibilityState = "visible"
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => visibility,
    })
    stubGo({ threads: [thread("th_1", "Plan the launch")] })
    withQueries(host())
    ;(await socketOn(chatName("th_1"))).open()
    const comeBack = (after: number) => {
      vi.setSystemTime(Date.now() + after)
      visibility = "visible"
      act(() => {
        document.dispatchEvent(new Event("visibilitychange"))
      })
    }
    await waitFor(() => expect(title()).toBe("Plan the launch"))

    // Nearly an hour on, a link asks for the conversation that is open.
    vi.setSystemTime(Date.now() + 50 * 60_000)
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_1" }))
    comeBack(50 * 60_000)
    expect(title()).toBe("Plan the launch")

    // A mounted chat keeps its view even after a long absence.
    comeBack(3 * HOUR)
    expect(title()).toBe("Plan the launch")
  })

  it("waits for the agents list, so what it remembers is kept under the agent's id", async () => {
    const go = stubGo({
      rememberLatest: false,
      threads: [thread("th_2", "Newer"), thread("th_1", "Older", 3 * HOUR)],
    })
    const release = go.holdAgents()
    withQueries(host())
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_1" }))
    // Neither the chat nor the request has chosen anything yet.
    await act(async () => {})
    expect(roomSockets.all).toHaveLength(0)
    expect(window.sessionStorage.length).toBe(0)

    act(() => release())
    await socketOn(chatName("th_1"))
    expect(roomSockets.named(chatName("th_2"))).toHaveLength(0)
    expect(rememberedChoice(scope, primary.id)).toBe("th_1")
  })

  it("opens a blank chat, and remembers it, for a request with no message", async () => {
    const go = stubGo({ threads: [thread("th_1", "Plan the launch")] })
    withQueries(host())
    ;(await socketOn(chatName("th_1"))).open()

    act(() => requestPrimaryChat(scope, { kind: "new" }))
    expect(await screen.findByText(blank)).toBeTruthy()
    expect(rememberedChoice(scope, primary.id)).toBe("new")
    await act(async () => {})
    expect(go.calls.some((call) => call.startsWith("POST"))).toBe(false)
  })

  it("ignores a request for another member", async () => {
    stubGo({
      threads: [thread("th_2", "Newer"), thread("th_1", "Older", 3 * HOUR)],
    })
    withQueries(host())
    ;(await socketOn(chatName("th_2"))).open()

    act(() =>
      requestPrimaryChat(
        { organizationId: ORG, userId: "someone-else" },
        { kind: "thread", threadId: "th_1" }
      )
    )
    await act(async () => {})
    expect(roomSockets.named(chatName("th_1"))).toHaveLength(0)
    expect(rememberedChoice(scope, primary.id)).toBe("th_2")
  })
})

describe("the primary agent", () => {
  it("is named on the chat once its list has loaded, and never by a stand-in", async () => {
    const go = stubGo({ threads: [] })
    const release = go.holdAgents()
    withQueries(host({ organizationId: ORG, viewerId: undefined }))
    await screen.findByText(blank)
    expect(screen.queryByRole("textbox", { name: "Message Leo" })).toBeNull()
    expect(screen.queryByText("Primary agent")).toBeNull()

    act(() => release())
    expect(
      await screen.findByRole("textbox", { name: "Message Leo" })
    ).toBeTruthy()
  })
})

describe("in StrictMode, as the dev server runs the app", () => {
  function strict(ui: ReactElement) {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    return render(
      <StrictMode>
        <QueryClientProvider client={client}>{ui}</QueryClientProvider>
      </StrictMode>
    )
  }

  it("sends a message a request brought before the shell mounted, once, into one conversation", async () => {
    const go = stubGo({ threads: [thread("th_1", "Old", 5 * HOUR)] })
    act(() =>
      requestPrimaryChat(scope, { kind: "new", message: "What changed today?" })
    )
    strict(host())
    ;(await socketOn(chatName("th_new"))).open()
    await act(async () => {})
    expect(go.calls.filter((call) => call === START)).toHaveLength(1)
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).toHaveBeenCalledOnce()
  })

  it("opens a conversation a request named before the shell mounted, once", async () => {
    stubGo({
      threads: [thread("th_2", "Newer"), thread("th_1", "Older", 3 * HOUR)],
    })
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_1" }))
    strict(host())
    await socketOn(chatName("th_1"))
    await act(async () => {})
    expect(roomSockets.named(chatName("th_2"))).toHaveLength(0)
    expect(rememberedChoice(scope, primary.id)).toBe("th_1")
  })

  it("sends a typed first message once", async () => {
    const go = stubGo({ threads: [] })
    strict(host())
    await screen.findByText(blank)
    await screen.findByRole("textbox", { name: "Message Leo" })
    type(leoBox(), "Plan the launch")
    fireEvent.keyDown(leoBox(), { key: "Enter" })
    ;(await socketOn(chatName("th_new"))).open()
    await act(async () => {})
    expect(go.calls.filter((call) => call === START)).toHaveLength(1)
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).toHaveBeenCalledOnce()
  })
})

describe("a file the agent saves", () => {
  it("reads the conversation again as it arrives, so its Files list it at once", async () => {
    const saved: Item[] = [
      { type: "message", id: "ev_1", author: "member", text: "Write it up" },
      { type: "artifact", id: "ev_f", artifact: "report.pdf", version: 2 },
    ]
    stubGo({ threads: [thread("th_1", "Write it up")] })
    const view = withQueries(host())
    ;(await socketOn(chatName("th_1"))).open()
    const chat = agentChats.named(chatName("th_1"))
    const invalidate = vi.spyOn(view.client, "invalidateQueries")

    chat.update({ status: "streaming" })
    chat.update({ messages: itemsToChatMessages(saved) })
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: threadKeys.detail(ORG, "th_1"),
    })
    // The file is a chip in the conversation, as it always was.
    expect(screen.getByText("report.pdf")).toBeTruthy()
  })
})
