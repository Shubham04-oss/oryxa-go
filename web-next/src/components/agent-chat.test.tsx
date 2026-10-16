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
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { useReducer, useState } from "react"
import type { ReactElement } from "react"
import { AgentChat } from "./agent-chat"
import { WorkspaceAgentPanel } from "./workspace-agent-panel"
import {
  initialPrimaryChat,
  primaryChatReducer,
  rememberChoice,
  rememberedChoice,
  rememberedView,
  usePrimaryChatActivity,
} from "@/lib/threads/chat-choice"
import type { PrimaryChat } from "@/lib/threads/chat-choice"
import { itemsToChatMessages } from "@/lib/threads/chat-messages"
import {
  threadApprovalQueryOptions,
  threadKeys,
  threadListQueryOptions,
} from "@/lib/threads/client"
import type { Item, Thread, ThreadDetail } from "@/lib/threads/client"

// The Agents SDK's hooks stand in: `useAgent` hands back a socket the test
// opens and closes through the options the component passed, and
// `useAgentChat` hands back whatever `hooks.chat` holds.
const hooks = vi.hoisted(() => {
  const openListeners = new Set<() => void>()
  return {
    agentOptions: [] as Array<{
      name: string
      onOpen?: () => void
      onClose?: (event: { code: number }) => void
    }>,
    chatOptions: undefined as
      | undefined
      | {
          getInitialMessages: () => Promise<unknown>
          onData: (part: unknown) => void
          onFinish: (result: Record<string, unknown>) => void
        },
    chat: { messages: [] as unknown[], status: "ready" },
    rerenders: new Set<() => void>(),
    sendMessage: vi.fn(async () => undefined),
    stop: vi.fn(async () => undefined),
    regenerate: vi.fn(async () => undefined),
    setMessages: vi.fn(),
    openListeners,
    socket: {
      reconnect: vi.fn(() => {
        setTimeout(() => {
          for (const listener of [...openListeners]) listener()
        })
      }),
      addEventListener: (event: string, listener: () => void) => {
        if (event === "open") openListeners.add(listener)
      },
      removeEventListener: (event: string, listener: () => void) => {
        if (event === "open") openListeners.delete(listener)
      },
    },
  }
})

vi.mock("agents/react", () => ({
  useAgent: (options: (typeof hooks.agentOptions)[number]) => {
    hooks.agentOptions.push(options)
    return hooks.socket
  },
}))

vi.mock("@cloudflare/ai-chat/react", async () => {
  const React = await import("react")
  return {
    useAgentChat: (options: NonNullable<typeof hooks.chatOptions>) => {
      const [, rerender] = React.useReducer((count: number) => count + 1, 0)
      React.useEffect(() => {
        hooks.rerenders.add(rerender)
        return () => {
          hooks.rerenders.delete(rerender)
        }
      }, [])
      hooks.chatOptions = options
      return {
        messages: hooks.chat.messages,
        status: hooks.chat.status,
        isServerStreaming: false,
        sendMessage: hooks.sendMessage,
        stop: hooks.stop,
        regenerate: hooks.regenerate,
        setMessages: hooks.setMessages,
      }
    },
  }
})

function updateChat(next: Partial<typeof hooks.chat>) {
  act(() => {
    Object.assign(hooks.chat, next)
    for (const rerender of hooks.rerenders) rerender()
  })
}

function socketFor(name: string) {
  const options = hooks.agentOptions.filter((item) => item.name === name).at(-1)
  if (!options) throw new Error(`no socket for ${name}`)
  return options
}

function open(name: string) {
  act(() => socketFor(name).onOpen?.())
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const HOUR = 60 * 60 * 1000
/** As long as a socket is open and quiet before the chat calls a run over: a little over `RUN_SETTLE_MS`. */
const RUN_SETTLE = 2000

/** A conversation last active `age` ago (a minute by default: fresh). */
const thread = (
  id: string,
  title: string,
  age = 60_000,
  origin?: string
): Thread => ({
  id,
  title,
  updated_at: new Date(Date.now() - age).toISOString(),
  ...(origin ? { origin } : {}),
})

/** Go and the Worker's HTTP side, by route. */
function stubApi({
  threads = [] as Thread[],
  items = [] as Item[],
  pending = [] as string[],
  created = thread("th_new", ""),
  /** Conversations Go no longer has (the list may still name them). */
  gone = [] as string[],
  rememberLatest = true,
} = {}) {
  if (rememberLatest && rememberedChoice(scope, LEO) === null) {
    const last = threads.find(
      (item) => (!item.origin || item.origin === "chat") && item.title.trim()
    )
    if (last && Date.now() - Date.parse(last.updated_at) < HOUR)
      rememberChoice(scope, LEO, last.id)
  }
  const calls: string[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const method = init?.method ?? "GET"
      calls.push(`${method} ${url.pathname}`)
      const path = url.pathname
      if (path === "/v1/organizations/org_1/threads") {
        if (method === "POST") {
          threads = [created, ...threads]
          return json(created, 201)
        }
        return json({ threads })
      }
      const deletion = /^\/agents\/thread-chat-agent\/org_1~([^/]+)$/.exec(path)
      if (deletion && method === "DELETE") {
        threads = threads.filter((row) => row.id !== deletion[1])
        return new Response(null, { status: 204 })
      }
      if (path.endsWith("/get-messages")) return json([])
      const detail = /^\/v1\/organizations\/org_1\/threads\/([^/]+)$/.exec(path)
      if (detail) {
        if (gone.includes(detail[1])) {
          return json({ error: { code: "not_found", message: "no" } }, 404)
        }
        return json({
          ...thread(detail[1], ""),
          items,
          pending_confirmations: pending,
        })
      }
      if (path === "/v1/organizations/org_1/agents") {
        return json({
          agents: [
            {
              id: "agt_leo",
              organization_id: "org_1",
              owner_user_id: "user_1",
              kind: "primary",
              name: "Leo",
              description: "",
              instructions: "",
              status: "active",
              revision: 1,
              created_at: "",
              updated_at: "",
            },
          ],
        })
      }
      if (path === "/v1/auth/me") return json({ user: {}, organizations: [] })
      return json({ error: { code: "not_found", message: "no route" } }, 404)
    })
  )
  return calls
}

/** The list request, answered only when released. */
function holdMemberList() {
  let release = () => {}
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  const fetcher = vi.mocked(fetch)
  const answer = fetcher.getMockImplementation()!
  fetcher.mockImplementation(async (input, init) => {
    const url = new URL(String(input), "http://localhost")
    if (
      url.pathname === "/v1/organizations/org_1/threads" &&
      (init?.method ?? "GET") === "GET"
    ) {
      await gate
    }
    return answer(input, init)
  })
  return () => release()
}

function renderWithQueries(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return {
    ...render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>),
    client,
  }
}

const scope = { organizationId: "org_1", userId: "user_1" }
/** The primary agent, whose id keys the choices the chat remembers. */
const LEO = "agt_leo"

/**
 * The chat as the panel mounts it, holding its state in the reducer the panel
 * uses. `agent` is the primary agent's id, or null while the agents list is
 * out.
 */
function ChatHost({
  remember = true,
  agent = LEO,
  viewer = scope.userId,
  initial,
  inOverview = false,
}: {
  remember?: boolean
  agent?: string | null
  /** The member whose chat it is (same organization). */
  viewer?: string
  /** What the chat holds to begin with, when it is not a fresh chat. */
  initial?: PrimaryChat
  /** The focused Agent overview is open. */
  inOverview?: boolean
}) {
  const asViewer = { ...scope, userId: viewer }
  const [chat, dispatch] = useReducer(
    primaryChatReducer,
    initial,
    (start) => start ?? initialPrimaryChat()
  )
  return (
    <AgentChat
      organizationId="org_1"
      scope={remember ? asViewer : undefined}
      agentId={agent ?? undefined}
      agentName="Leo"
      chat={chat}
      dispatch={dispatch}
      inOverview={inOverview}
      onComposingChange={vi.fn()}
    />
  )
}

function renderChat() {
  return renderWithQueries(<ChatHost />)
}

const composer = () => screen.getByRole("textbox", { name: "Message Leo" })

/** The words of every message in the log that was not sent, in order. */
const notSentWords = () =>
  Array.from(
    screen.getByRole("log").querySelectorAll('[data-status="not-sent"]')
  ).map((message) => message.textContent)

function type(text: string) {
  fireEvent.change(composer(), { target: { value: text } })
  fireEvent.keyDown(composer(), { key: "Enter" })
}

beforeEach(() => {
  hooks.agentOptions.length = 0
  hooks.chatOptions = undefined
  hooks.chat.messages = []
  hooks.chat.status = "ready"
  hooks.openListeners.clear()
  for (const mock of [
    hooks.sendMessage,
    hooks.stop,
    hooks.regenerate,
    hooks.setMessages,
    hooks.socket.reconnect,
  ]) {
    mock.mockClear()
  }
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.sessionStorage.clear()
})

describe("AgentChat", () => {
  it("opens the member's latest conversation through its Durable Object", async () => {
    stubApi({
      threads: [thread("th_2", "Plan the launch"), thread("th_1", "Older")],
    })
    renderChat()

    await waitFor(() =>
      expect(hooks.agentOptions.map((item) => item.name)).toContain(
        "org_1~th_2"
      )
    )
    expect(hooks.agentOptions.every((item) => item.name === "org_1~th_2")).toBe(
      true
    )
    expect(await screen.findByText("Plan the launch")).toBeTruthy()

    // The member can type at once, but nothing goes before the socket is
    // open.
    expect(composer().hasAttribute("disabled")).toBe(false)
    expect(screen.getByText("Connecting…")).toBeTruthy()
    type("  Draft the brief  ")
    expect(hooks.sendMessage).not.toHaveBeenCalled()
    expect((composer() as HTMLTextAreaElement).value).toBe(
      "  Draft the brief  "
    )

    open("org_1~th_2")
    expect(screen.queryByText("Connecting…")).toBeNull()
    fireEvent.keyDown(composer(), { key: "Enter" })
    expect(hooks.sendMessage).toHaveBeenCalledExactlyOnceWith({
      text: "Draft the brief",
    })
    expect((composer() as HTMLTextAreaElement).value).toBe("")
  })

  it("offers Stop while a reply streams, and says when the socket reconnects", async () => {
    const calls = stubApi({ threads: [thread("th_1", "Hello")] })
    renderChat()
    await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
    open("org_1~th_1")

    updateChat({ status: "streaming" })
    fireEvent.click(screen.getByRole("button", { name: "Stop response" }))
    expect(hooks.stop).toHaveBeenCalledOnce()
    updateChat({ status: "ready" })
    expect(screen.getByRole("button", { name: "Send message" })).toBeTruthy()

    act(() => socketFor("org_1~th_1").onClose?.({ code: 1006 }))
    expect(screen.getByText("Reconnecting…")).toBeTruthy()
    // Typing goes on while the socket is out; sending waits for it.
    expect(composer().hasAttribute("disabled")).toBe(false)
    type("While away")
    expect(hooks.sendMessage).not.toHaveBeenCalled()
    // A dropped socket refreshes the session its reconnect will carry.
    await waitFor(() => expect(calls).toContain("GET /v1/auth/me"))
    open("org_1~th_1")
    expect(screen.queryByText("Reconnecting…")).toBeNull()
    fireEvent.keyDown(composer(), { key: "Enter" })
    expect(hooks.sendMessage).toHaveBeenCalledExactlyOnceWith({
      text: "While away",
    })
  })

  it("stops showing a tool call as running once its run is stopped", async () => {
    const items: Item[] = [
      { type: "message", id: "ev_1", author: "member", text: "Look it up" },
      { type: "tool_call", id: "ev_2", call_id: "c1", tool: "web_search" },
    ]
    stubApi({ threads: [thread("th_1", "Look it up")], items })
    hooks.chat.messages = itemsToChatMessages(items)
    hooks.chat.status = "streaming"
    renderChat()
    await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
    open("org_1~th_1")
    const row = () => document.querySelector(".agent-run-step")
    expect(row()?.getAttribute("data-state")).toBe("running")

    fireEvent.click(screen.getByRole("button", { name: "Stop response" }))
    expect(hooks.stop).toHaveBeenCalledOnce()
    updateChat({ status: "ready" })
    expect(row()?.getAttribute("data-state")).toBe("interrupted")
    expect(row()?.textContent).toContain("Interrupted")
  })

  describe("a tool call that only looks unfinished", () => {
    const items: Item[] = [
      { type: "message", id: "ev_1", author: "member", text: "Look it up" },
      { type: "tool_call", id: "ev_2", call_id: "c1", tool: "web_search" },
    ]
    const row = () => document.querySelector(".agent-run-step")
    const state = () => row()?.getAttribute("data-state")
    const settle = (ms: number) =>
      act(() => {
        vi.advanceTimersByTime(ms)
      })

    afterEach(() => {
      vi.useRealTimers()
    })

    it("stays running through a socket that drops mid-run, and after it comes back, until the run resumes", async () => {
      stubApi({ threads: [thread("th_1", "Look it up")], items })
      hooks.chat.messages = itemsToChatMessages(items)
      hooks.chat.status = "streaming"
      renderChat()
      await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
      vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
      open("org_1~th_1")
      expect(state()).toBe("running")

      // The transport closes the stream when the socket drops; Go goes on.
      act(() => socketFor("org_1~th_1").onClose?.({ code: 1006 }))
      updateChat({ status: "ready" })
      settle(5000)
      expect(state()).toBe("running")

      // The socket is back, and the run has not resumed yet.
      open("org_1~th_1")
      settle(RUN_SETTLE - 500)
      expect(state()).toBe("running")
      updateChat({ status: "streaming" })
      settle(5000)
      expect(state()).toBe("running")
    })

    it("stays running while the conversation of a live run opens, and reads interrupted once the socket has been quiet a moment", async () => {
      stubApi({ threads: [thread("th_1", "Look it up")], items })
      hooks.chat.messages = itemsToChatMessages(items)
      renderChat()
      await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
      vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
      // Not connected yet: nothing says the run is over.
      settle(10_000)
      expect(state()).toBe("running")

      open("org_1~th_1")
      settle(RUN_SETTLE - 500)
      expect(state()).toBe("running")
      // Nothing resumed: the run behind this call is over.
      settle(600)
      expect(state()).toBe("interrupted")
      expect(row()?.textContent).toContain("Interrupted")
    })

    it("reads a run interrupted at once when the member stops it, or the chat is aborted", async () => {
      stubApi({ threads: [thread("th_1", "Look it up")], items })
      hooks.chat.messages = itemsToChatMessages(items)
      hooks.chat.status = "streaming"
      renderChat()
      await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
      open("org_1~th_1")
      expect(state()).toBe("running")

      updateChat({ status: "ready" })
      act(() =>
        hooks.chatOptions?.onFinish({
          isAbort: true,
          isDisconnect: false,
          isError: false,
        })
      )
      expect(state()).toBe("interrupted")
    })

    it("runs again when the member sends after a stop", async () => {
      stubApi({ threads: [thread("th_1", "Look it up")], items })
      hooks.chat.messages = itemsToChatMessages(items)
      hooks.chat.status = "streaming"
      renderChat()
      await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
      open("org_1~th_1")
      fireEvent.click(screen.getByRole("button", { name: "Stop response" }))
      updateChat({ status: "ready" })
      expect(state()).toBe("interrupted")

      // The next turn streams, with a new call of its own running.
      hooks.chat.messages = itemsToChatMessages([
        ...items,
        { type: "message", id: "ev_3", author: "member", text: "Again" },
        { type: "tool_call", id: "ev_4", call_id: "c2", tool: "read_file" },
      ])
      updateChat({ status: "streaming" })
      const rows = document.querySelectorAll(".agent-run-step")
      expect(rows).toHaveLength(2)
      expect(rows[0].getAttribute("data-state")).toBe("interrupted")
      expect(rows[1].getAttribute("data-state")).toBe("running")
    })
  })

  it("starts a thread with the first message when the member has none", async () => {
    const calls = stubApi({ threads: [] })
    renderChat()

    expect(
      await screen.findByText("Ask anything to start a conversation.")
    ).toBeTruthy()
    // No thread, so no socket yet; the composer is ready all the same.
    expect(hooks.agentOptions).toHaveLength(0)
    expect(composer().hasAttribute("disabled")).toBe(false)
    expect(
      screen
        .getByRole("button", { name: "New conversation" })
        .hasAttribute("disabled")
    ).toBe(false)

    type("Plan the launch")
    await waitFor(() =>
      expect(calls).toContain("POST /v1/organizations/org_1/threads")
    )
    await waitFor(() =>
      expect(hooks.agentOptions.map((item) => item.name)).toContain(
        "org_1~th_new"
      )
    )
    // The message waits in view for the new thread's socket.
    expect(
      screen.getByRole("log").querySelector('[data-from="member"]')?.textContent
    ).toBe("Plan the launch")
    expect(hooks.sendMessage).not.toHaveBeenCalled()

    open("org_1~th_new")
    expect(hooks.sendMessage).toHaveBeenCalledExactlyOnceWith({
      text: "Plan the launch",
    })
    open("org_1~th_new")
    expect(hooks.sendMessage).toHaveBeenCalledOnce()
  })

  it("starts a new conversation on request, and keeps the previous one", async () => {
    const calls = stubApi({ threads: [thread("th_1", "Plan the launch")] })
    renderChat()
    await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
    open("org_1~th_1")

    fireEvent.click(screen.getByRole("button", { name: "New conversation" }))
    expect(
      screen.getByText("Ask anything to start a conversation.")
    ).toBeTruthy()
    expect(screen.getByText("New conversation")).toBeTruthy()
    expect(composer().hasAttribute("disabled")).toBe(false)
    // Nothing is created until there is something to say, and nothing goes.
    expect(calls.filter((call) => call.startsWith("POST"))).toEqual([])
    expect(calls.filter((call) => call.startsWith("DELETE"))).toEqual([])

    type("Something new")
    await waitFor(() =>
      expect(hooks.agentOptions.map((item) => item.name)).toContain(
        "org_1~th_new"
      )
    )
    open("org_1~th_new")
    expect(hooks.sendMessage).toHaveBeenCalledExactlyOnceWith({
      text: "Something new",
    })
    expect(calls.filter((call) => call.startsWith("DELETE"))).toEqual([])
  })

  it("sends confirmation answers in the chat request's body", async () => {
    const asked: Item = {
      type: "confirmation",
      id: "ev_2",
      call_id: "call_1",
      tool_call_id: "tool_1",
      tool: "send_email",
      arguments: { to: "sam@example.com" },
      status: "pending",
    }
    const items: Item[] = [
      { type: "message", id: "ev_1", author: "member", text: "Email Sam" },
      asked,
    ]
    stubApi({
      threads: [thread("th_1", "Email Sam")],
      items,
      pending: ["call_1"],
    })
    hooks.chat.messages = itemsToChatMessages(items)
    renderChat()
    await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
    open("org_1~th_1")

    const card = await screen.findByRole("group", {
      name: "Confirm Send email",
    })
    // Go refuses a message while a confirmation waits.
    expect(composer().hasAttribute("disabled")).toBe(true)
    expect(
      screen.getByText("Answer the confirmation above to continue.")
    ).toBeTruthy()

    fireEvent.click(within(card).getByRole("button", { name: "Approve" }))
    expect(hooks.sendMessage).toHaveBeenCalledExactlyOnceWith(
      { text: " ", metadata: { oryxaConfirmation: true } },
      { body: { confirmations: [{ id: "call_1", confirmed: true }] } }
    )
    expect(within(card).getByText("Approved")).toBeTruthy()
  })

  it("sends once more, silently, after the session expired before a run", async () => {
    const calls = stubApi({ threads: [thread("th_1", "Hello")] })
    renderChat()
    await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
    open("org_1~th_1")
    type("Hello")

    const expired = {
      type: "data-oryxa",
      data: {
        type: "run_failed",
        error: "Your session has ended. Sign in again to continue.",
        code: "session_expired",
      },
    }
    const finished = { isAbort: false, isDisconnect: false, isError: false }
    act(() => hooks.chatOptions?.onData(expired))
    act(() => hooks.chatOptions?.onFinish(finished))
    await waitFor(() => expect(hooks.regenerate).toHaveBeenCalledOnce())
    expect(hooks.regenerate).toHaveBeenCalledWith({ body: undefined })
    expect(calls).toContain("GET /v1/auth/me")
    expect(hooks.socket.reconnect).toHaveBeenCalledOnce()
    expect(screen.queryByRole("alert")).toBeNull()

    // Expired again: the member is told, and nothing is sent a third time.
    act(() => hooks.chatOptions?.onData(expired))
    act(() => hooks.chatOptions?.onFinish(finished))
    expect((await screen.findByRole("alert")).textContent).toContain(
      "Your session has ended."
    )
    expect(hooks.regenerate).toHaveBeenCalledOnce()
  })

  it("merges what Go added outside the socket once it is idle, once", async () => {
    const stored: Item[] = [
      { type: "message", id: "ev_1", author: "member", text: "Hello" },
      { type: "message", id: "ev_2", author: "Leo", text: "Hi there" },
    ]
    const update: Item = {
      type: "message",
      id: "ev_3",
      author: "apps",
      text: "Gmail: 2 new emails",
    }
    stubApi({ threads: [thread("th_1", "Hello")], items: [...stored, update] })
    hooks.chat.messages = itemsToChatMessages(stored)
    renderChat()
    await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
    // Nothing merges before the socket is open.
    expect(hooks.setMessages).not.toHaveBeenCalled()
    open("org_1~th_1")

    await waitFor(() => expect(hooks.setMessages).toHaveBeenCalledOnce())
    const merged = hooks.setMessages.mock.calls[0][0] as Array<{
      role: string
      parts: Array<{ data?: Item }>
    }>
    expect(merged).toHaveLength(3)
    expect(merged[2].parts[0].data).toEqual(update)
    updateChat({ status: "ready" })
    expect(hooks.setMessages).toHaveBeenCalledOnce()
  })

  it("loads the stored transcript with whatever Go holds that it lacks", async () => {
    const items: Item[] = [
      { type: "message", id: "ev_1", author: "member", text: "Hello" },
      { type: "message", id: "ev_2", author: "Leo", text: "Hi there" },
    ]
    stubApi({ threads: [thread("th_1", "Hello")], items })
    renderChat()
    await waitFor(() => expect(hooks.chatOptions).toBeDefined())
    const messages = (await hooks.chatOptions?.getInitialMessages()) as Array<{
      role: string
    }>
    expect(messages.map((message) => message.role)).toEqual([
      "user",
      "assistant",
    ])
  })
})

describe("which conversation the chat opens", () => {
  const blank = "Ask anything to start a conversation."
  const detailPath = (id: string) => `GET /v1/organizations/org_1/threads/${id}`

  async function opensThread(id: string) {
    await waitFor(() =>
      expect(hooks.agentOptions.map((item) => item.name)).toContain(
        `org_1~${id}`
      )
    )
    expect(
      hooks.agentOptions.every((item) => item.name === `org_1~${id}`)
    ).toBe(true)
  }

  it("opens a new chat once the latest conversation is an hour old", async () => {
    stubApi({ threads: [thread("th_1", "Yesterday's plan", 25 * HOUR)] })
    renderChat()

    expect(await screen.findByText(blank)).toBeTruthy()
    expect(screen.getByText("New conversation")).toBeTruthy()
    // No socket: nothing is opened until there is something to say.
    expect(hooks.agentOptions).toHaveLength(0)
    expect(composer().hasAttribute("disabled")).toBe(false)
  })

  it("reopens the latest conversation while it is fresh", async () => {
    stubApi({
      threads: [thread("th_2", "Plan the launch", 59 * 60_000)],
    })
    renderChat()
    await opensThread("th_2")
    expect(await screen.findByText("Plan the launch")).toBeTruthy()
  })

  it("never opens an update from the member's apps", async () => {
    stubApi({
      threads: [
        thread("th_w", "From your apps", 60_000, "wakeup"),
        thread("th_1", "Plan the launch", 5 * 60_000),
      ],
    })
    renderChat()
    await opensThread("th_1")
  })

  it("passes over a conversation nothing was ever sent in, as an abandoned start", async () => {
    stubApi({
      threads: [
        thread("th_empty", "", 30_000),
        thread("th_1", "Plan the launch", 5 * 60_000),
      ],
    })
    renderChat()
    await opensThread("th_1")
  })

  it("opens a new chat when the only recent conversation is an abandoned start", async () => {
    stubApi({
      threads: [
        thread("th_empty", "", 30_000),
        thread("th_1", "Yesterday's plan", 25 * HOUR),
      ],
    })
    renderChat()
    expect(await screen.findByText(blank)).toBeTruthy()
    expect(hooks.agentOptions).toHaveLength(0)
  })

  it("lets the member type while the choice is being made, and keeps what they typed", async () => {
    stubApi({ threads: [thread("th_1", "Plan the launch")] })
    const release = holdMemberList()
    renderChat()
    await screen.findByText("Loading the conversation…")

    // Nothing to send to yet, but the words are not held back.
    expect(composer().hasAttribute("disabled")).toBe(false)
    fireEvent.change(composer(), { target: { value: "Quick question" } })
    fireEvent.keyDown(composer(), { key: "Enter" })
    expect(hooks.agentOptions).toHaveLength(0)
    expect(hooks.sendMessage).not.toHaveBeenCalled()

    act(() => release())
    await opensThread("th_1")
    expect((composer() as HTMLTextAreaElement).value).toBe("Quick question")
    open("org_1~th_1")
    fireEvent.keyDown(composer(), { key: "Enter" })
    expect(hooks.sendMessage).toHaveBeenCalledExactlyOnceWith({
      text: "Quick question",
    })
  })

  describe("when the choice takes long", () => {
    afterEach(() => {
      vi.useRealTimers()
    })

    it("opens a new chat after five seconds, with what the member typed still in the composer, and does not go back for what arrives later", async () => {
      const calls = stubApi({ threads: [thread("th_1", "Plan the launch")] })
      const release = holdMemberList()
      // The clock is the test's from the start: the chat sets its timer as it
      // opens, and the query client's own timers move with it.
      vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
      const later = (ms: number) =>
        act(async () => {
          await vi.advanceTimersByTimeAsync(ms)
        })
      renderChat()
      await later(100)
      expect(screen.getByText("Loading the conversation…")).toBeTruthy()
      fireEvent.change(composer(), { target: { value: "Quick question" } })

      await later(4700)
      expect(screen.getByText("Loading the conversation…")).toBeTruthy()
      await later(300)
      expect(screen.getByText(blank)).toBeTruthy()
      expect(screen.getByText("New conversation")).toBeTruthy()
      expect((composer() as HTMLTextAreaElement).value).toBe("Quick question")

      // The list comes in at last: the chat does not leave the new chat.
      release()
      await later(100)
      expect(hooks.agentOptions).toHaveLength(0)
      expect(screen.getByText(blank)).toBeTruthy()
      expect(calls.some((call) => call.startsWith("POST"))).toBe(false)
      // And it takes the message as a new chat's first.
      fireEvent.keyDown(composer(), { key: "Enter" })
      await later(100)
      expect(calls).toContain("POST /v1/organizations/org_1/threads")
    })

    it("does not fall back once the choice has been made in time", async () => {
      stubApi({ threads: [thread("th_1", "Plan the launch")] })
      renderChat()
      await opensThread("th_1")
      vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
      act(() => {
        vi.advanceTimersByTime(30_000)
      })
      expect(screen.queryByText("New conversation")).toBeNull()
      expect(screen.getByText("Plan the launch")).toBeTruthy()
    })
  })

  it("says so, and opens a new chat, when the conversation it opened is gone", async () => {
    stubApi({
      threads: [thread("th_1", "Plan the launch")],
      gone: ["th_1"],
    })
    renderChat()
    expect(
      await screen.findByText("That conversation no longer exists.")
    ).toBeTruthy()
    expect(screen.getByText(blank)).toBeTruthy()
    expect(screen.getByText("New conversation")).toBeTruthy()
    // And a reload stays on the new chat, not the conversation that is gone.
    expect(rememberedChoice(scope, LEO)).toBe("new")
  })

  it("keeps a first message as not sent, and leaves the composer alone, when the conversation made for it is gone before it was sent", async () => {
    stubApi({ threads: [], gone: ["th_new"] })
    renderChat()
    expect(await screen.findByText(blank)).toBeTruthy()
    type("Plan the launch")

    expect(
      await screen.findByText("Not sent. That conversation no longer exists.")
    ).toBeTruthy()
    // Nothing was sent to a conversation that is not there; the words wait as
    // a message of their own, and the composer is still the member's.
    expect(hooks.sendMessage).not.toHaveBeenCalled()
    expect(notSentWords()).toEqual(["Plan the launch"])
    expect((composer() as HTMLTextAreaElement).value).toBe("")
    expect(screen.getByText("New conversation")).toBeTruthy()
  })

  it("opens the conversation the member chose in this session, however old", async () => {
    rememberChoice(scope, LEO, "th_1")
    stubApi({
      threads: [
        thread("th_2", "Fresh", 60_000),
        thread("th_1", "Chosen", 30 * HOUR),
      ],
    })
    renderChat()
    await opensThread("th_1")
  })

  it("ignores a chosen conversation that no longer exists and opens New", async () => {
    rememberChoice(scope, LEO, "th_gone")
    stubApi({ threads: [thread("th_1", "Fresh", 60_000)] })
    renderChat()
    expect(await screen.findByText(blank)).toBeTruthy()
  })

  it("keeps a member's choice of a new chat over a fresh conversation, over pending approvals", async () => {
    rememberChoice(scope, LEO, null)
    const calls = stubApi({ threads: [thread("th_1", "Fresh", 60_000)] })
    renderChat()

    expect(await screen.findByText(blank)).toBeTruthy()
    expect(hooks.agentOptions).toHaveLength(0)
    expect(calls.some((call) => call.includes("/get-messages"))).toBe(false)
  })

  it("stays on a new chat after a reload, when the member started one", async () => {
    stubApi({ threads: [thread("th_1", "Plan the launch")] })
    const first = renderChat()
    await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
    open("org_1~th_1")
    fireEvent.click(screen.getByRole("button", { name: "New conversation" }))
    expect(rememberedChoice(scope, LEO)).toBe("new")

    // The same tab, loaded again: the conversation they left is still fresh.
    first.unmount()
    cleanup()
    hooks.agentOptions.length = 0
    renderChat()
    expect(await screen.findByText(blank)).toBeTruthy()
    expect(hooks.agentOptions).toHaveLength(0)
  })

  it("records the latest real leave after a recent explicit New is restored on reload", async () => {
    vi.useFakeTimers({ toFake: ["Date"] })
    const now = Date.now()
    rememberChoice(scope, LEO, null, now - 4 * 60_000)
    stubApi({
      threads: [thread("pending", "Waiting")],
      pending: ["approval"],
      rememberLatest: false,
    })
    const first = renderChat()
    expect(await screen.findByText(blank)).toBeTruthy()
    expect(rememberedView(scope, LEO)?.leftAt).toBe(now)
    vi.setSystemTime(now + 3 * 60_000)
    first.unmount()
    expect(rememberedView(scope, LEO)?.leftAt).toBe(now + 3 * 60_000)
    renderChat()
    expect(await screen.findByText(blank)).toBeTruthy()
    expect(hooks.agentOptions).toHaveLength(0)
  })

  it("remembers the conversation a first message starts, so a reload stays in it", async () => {
    // It is old by the time of the reload, which only the choice can outlast.
    stubApi({ threads: [], created: thread("th_new", "", 5 * HOUR) })
    const first = renderChat()
    expect(await screen.findByText(blank)).toBeTruthy()
    type("Plan the launch")
    await opensThread("th_new")
    expect(rememberedChoice(scope, LEO)).toBe("th_new")

    first.unmount()
    cleanup()
    hooks.agentOptions.length = 0
    renderChat()
    await opensThread("th_new")
  })

  it("reopens an old conversation that still waits on the member's confirmation", async () => {
    const items: Item[] = [
      { type: "message", id: "ev_1", author: "member", text: "Email Sam" },
      {
        type: "confirmation",
        id: "ev_2",
        call_id: "call_1",
        tool_call_id: "tool_1",
        tool: "send_email",
        arguments: { to: "sam@example.com" },
        status: "pending",
      },
    ]
    const calls = stubApi({
      threads: [thread("th_1", "Email Sam", 6 * HOUR)],
      items,
      pending: ["call_1"],
    })
    hooks.chat.messages = itemsToChatMessages(items)
    renderChat()

    await opensThread("th_1")
    expect(calls).toContain(detailPath("th_1"))
    open("org_1~th_1")
    expect(
      await screen.findByRole("group", { name: "Confirm Send email" })
    ).toBeTruthy()
  })

  it("opens a new chat when the old conversation waits on nothing, having asked Go once", async () => {
    const calls = stubApi({
      threads: [thread("th_1", "Done long ago", 6 * HOUR)],
      pending: [],
    })
    renderChat()

    expect(await screen.findByText(blank)).toBeTruthy()
    expect(hooks.agentOptions).toHaveLength(0)
    expect(calls.filter((call) => call === detailPath("th_1"))).toHaveLength(1)
  })

  it("waits for the primary agent's id before it chooses, so no choice is kept under a stand-in", async () => {
    stubApi({ threads: [thread("th_1", "Plan the launch")] })
    function AgentArrives() {
      const [agent, setAgent] = useState<string | null>(null)
      return (
        <>
          <button type="button" onClick={() => setAgent(LEO)}>
            Agents loaded
          </button>
          <ChatHost agent={agent} />
        </>
      )
    }
    renderWithQueries(<AgentArrives />)
    // The thread list is here (it is a fetch away), but the chat holds off.
    await screen.findByText("Loading the conversation…")
    await act(async () => {})
    expect(hooks.agentOptions).toHaveLength(0)
    expect(screen.queryByText("Plan the launch")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Agents loaded" }))
    await opensThread("th_1")
  })

  it("does not wait for it when there is nothing to remember", async () => {
    stubApi({ threads: [thread("th_1", "Plan the launch")] })
    renderWithQueries(<ChatHost remember={false} agent={null} />)
    expect(await screen.findByText(blank)).toBeTruthy()
  })

  it("opens a new chat when Go can't say whether the old conversation waits", async () => {
    stubApi({ threads: [thread("th_1", "Old", 6 * HOUR)] })
    const fetcher = vi.mocked(fetch)
    const answer = fetcher.getMockImplementation()
    fetcher.mockImplementation(async (input, init) =>
      String(input).endsWith("/threads/th_1")
        ? json({ error: { code: "unavailable", message: "down" } }, 503)
        : answer!(input, init)
    )
    renderChat()
    // A read that fails is tried once more before the chat gives up on it.
    expect(
      await screen.findByText(blank, undefined, { timeout: 4000 })
    ).toBeTruthy()
    expect(hooks.agentOptions).toHaveLength(0)
  })

  it("remembers nothing without a scope, and still applies the rules", async () => {
    stubApi({ threads: [thread("th_1", "Old", 6 * HOUR)] })
    renderWithQueries(<ChatHost remember={false} />)
    expect(await screen.findByText(blank)).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "New conversation" }))
    expect(window.sessionStorage.length).toBe(0)
  })
})

describe("a mounted conversation", () => {
  afterEach(() => vi.useRealTimers())
  it("keeps its current chat, composer draft and unsent rows through a long tab absence", async () => {
    stubApi({ threads: [thread("th_1", "Plan the launch")] })
    const initial = {
      ...initialPrimaryChat(),
      thread: "th_1",
      unsent: [{ id: 20, text: "Waiting message", notice: "Offline." }],
    }
    renderWithQueries(<ChatHost initial={initial} />)
    await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
    open("org_1~th_1")
    fireEvent.change(composer(), { target: { value: "Half a thought" } })
    vi.useFakeTimers({ toFake: ["Date"] })
    vi.setSystemTime(Date.now() + 3 * HOUR)
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"))
      window.dispatchEvent(new Event("focus"))
    })
    expect(screen.getByText("Plan the launch")).toBeTruthy()
    expect((composer() as HTMLTextAreaElement).value).toBe("Half a thought")
    expect(notSentWords()).toEqual(["Waiting message"])
    expect(rememberedChoice(scope, LEO)).toBe("th_1")
  })
})

describe("a first message that was not sent", () => {
  const blank = "Ask anything to start a conversation."
  const down = "The agents service is down."
  /** A chat that holds these messages as not sent, as the panel would. */
  const holding = (...words: string[]): PrimaryChat => ({
    ...initialPrimaryChat(),
    unsent: words.map((text, index) => ({ id: index + 1, text, notice: down })),
  })
  const button = (name: string, row = 0) =>
    screen.getAllByRole("button", { name })[row]

  it("stays in view in whichever conversation is open, and in a new chat", async () => {
    stubApi({ threads: [thread("th_1", "Plan the launch")] })
    renderWithQueries(<ChatHost initial={holding("Lost words")} />)
    await waitFor(() =>
      expect(hooks.agentOptions.map((item) => item.name)).toContain(
        "org_1~th_1"
      )
    )
    open("org_1~th_1")
    expect(notSentWords()).toEqual(["Lost words"])
    expect(screen.getByText(`Not sent. ${down}`)).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "New conversation" }))
    expect(notSentWords()).toEqual(["Lost words"])
    // The words replace the invitation, not the composer.
    expect(screen.queryByText(blank)).toBeNull()
    expect(composer()).toBeTruthy()
  })

  it("sends a message again into a conversation of its own, and it is no longer listed", async () => {
    const calls = stubApi({ threads: [] })
    renderWithQueries(<ChatHost initial={holding("Lost words")} />)
    await screen.findByText(`Not sent. ${down}`)

    fireEvent.click(button("Retry"))
    await waitFor(() =>
      expect(calls).toContain("POST /v1/organizations/org_1/threads")
    )
    await waitFor(() =>
      expect(hooks.agentOptions.map((item) => item.name)).toContain(
        "org_1~th_new"
      )
    )
    expect(notSentWords()).toEqual([])
    open("org_1~th_new")
    expect(hooks.sendMessage).toHaveBeenCalledExactlyOnceWith({
      text: "Lost words",
    })
    expect(screen.queryByText(/Not sent/)).toBeNull()
  })

  it("goes into an empty composer to be changed, and is not offered over what the member typed", async () => {
    stubApi({ threads: [] })
    renderWithQueries(<ChatHost initial={holding("First words", "More")} />)
    await screen.findAllByText(`Not sent. ${down}`)
    expect(screen.getAllByRole("button", { name: "Edit" })).toHaveLength(2)

    // Something typed: no Edit to press over it, and the words wait.
    fireEvent.change(composer(), { target: { value: "Half a thought" } })
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull()
    expect(screen.getAllByRole("button", { name: "Retry" })).toHaveLength(2)
    expect((composer() as HTMLTextAreaElement).value).toBe("Half a thought")
    expect(notSentWords()).toEqual(["First words", "More"])

    fireEvent.change(composer(), { target: { value: "" } })
    fireEvent.click(button("Edit", 1))
    expect((composer() as HTMLTextAreaElement).value).toBe("More")
    expect(notSentWords()).toEqual(["First words"])
    // The composer holds something again: the other waits.
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull()
  })

  it("is let go on request, one message at a time", async () => {
    stubApi({ threads: [] })
    renderWithQueries(<ChatHost initial={holding("First words", "More")} />)
    await screen.findAllByText(`Not sent. ${down}`)

    fireEvent.click(button("Dismiss"))
    expect(notSentWords()).toEqual(["More"])
    fireEvent.click(button("Dismiss"))
    expect(notSentWords()).toEqual([])
    // With nothing left, the chat invites the member to start again.
    expect(await screen.findByText(blank)).toBeTruthy()
  })

  it("keeps the conversation that is loading company, without hiding that it loads", async () => {
    stubApi({ threads: [thread("th_1", "Plan the launch")] })
    const release = holdMemberList()
    renderWithQueries(<ChatHost initial={holding("Lost words")} />)
    expect(await screen.findByText("Loading the conversation…")).toBeTruthy()
    expect(notSentWords()).toEqual(["Lost words"])
    act(() => release())
    await waitFor(() =>
      expect(hooks.agentOptions.map((item) => item.name)).toContain(
        "org_1~th_1"
      )
    )
    expect(notSentWords()).toEqual(["Lost words"])
  })
})

describe("a member who takes over the page", () => {
  const theirs = { ...scope, userId: "user_b" }

  it("does not choose, show or publish a conversation from the last member's cached list, and waits for its own", async () => {
    stubApi({ threads: [thread("th_b", "Your plan")], rememberLatest: false })
    rememberChoice(theirs, LEO, "th_b")
    // The other member's list is warm in the cache the two share.
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    client.setQueryData(threadListQueryOptions("org_1", "user_1").queryKey, [
      thread("th_a", "Their plan"),
    ])
    const release = holdMemberList()
    const activity = renderHook(() => usePrimaryChatActivity(theirs))
    render(
      <QueryClientProvider client={client}>
        <ChatHost viewer="user_b" />
      </QueryClientProvider>
    )

    await act(async () => {})
    expect(screen.queryByText("Their plan")).toBeNull()
    expect(hooks.agentOptions).toHaveLength(0)
    expect(activity.result.current).toBeNull()
    expect(rememberedChoice(scope, LEO)).toBeNull()

    act(() => release())
    await waitFor(() =>
      expect(hooks.agentOptions.map((item) => item.name)).toContain(
        "org_1~th_b"
      )
    )
    expect(hooks.agentOptions.every((item) => item.name === "org_1~th_b")).toBe(
      true
    )
    await waitFor(() =>
      expect(activity.result.current).toEqual({
        threadId: "th_b",
        state: "idle",
      })
    )
  })

  it("shows and publishes no conversation, whatever its state names, until the member's own list has loaded", async () => {
    stubApi({ threads: [thread("th_b", "Your plan")] })
    const decided = primaryChatReducer(initialPrimaryChat(), {
      type: "decide",
      thread: "th_a",
    })
    const release = holdMemberList()
    const activity = renderHook(() => usePrimaryChatActivity(theirs))
    renderWithQueries(<ChatHost viewer="user_b" initial={decided} />)

    await act(async () => {})
    // The list is out: the chat is still opening, and says nothing.
    expect(hooks.agentOptions).toHaveLength(0)
    expect(activity.result.current).toBeNull()
    expect(screen.getByText("Loading the conversation…")).toBeTruthy()
    release()
  })

  it("keeps the list it makes a new conversation in under the member's own entry, and refreshes it when a run ends", async () => {
    const calls = stubApi({ threads: [] })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <ChatHost />
      </QueryClientProvider>
    )
    expect(
      await screen.findByText("Ask anything to start a conversation.")
    ).toBeTruthy()
    type("Plan the launch")
    await waitFor(() =>
      expect(hooks.agentOptions.map((item) => item.name)).toContain(
        "org_1~th_new"
      )
    )
    const own = threadListQueryOptions("org_1", "user_1").queryKey
    expect(
      client.getQueryData<Thread[]>(own)?.map((item) => item.id)
    ).toContain("th_new")
    // Nothing was written to an entry that is not this member's.
    expect(
      client.getQueryData(threadListQueryOptions("org_1").queryKey)
    ).toBeUndefined()

    // A run that ends refreshes the member's list (a prefix invalidation).
    open("org_1~th_new")
    const listReads = () =>
      calls.filter((call) => call === "GET /v1/organizations/org_1/threads")
        .length
    const before = listReads()
    act(() =>
      hooks.chatOptions?.onFinish({
        isAbort: false,
        isDisconnect: false,
        isError: false,
      })
    )
    await waitFor(() => expect(listReads()).toBeGreaterThan(before))
  })
})

describe("the primary chat's activity", () => {
  // What the rest of the app would read: the chat's thread, and whether it
  // works, waits on the member or is idle.
  function watchActivity(as = scope) {
    const { result } = renderHook(() => usePrimaryChatActivity(as))
    return () => result.current
  }

  it("is heard only in its own organization, by its own member", async () => {
    stubApi({ threads: [thread("th_1", "Hello")] })
    const own = watchActivity()
    const otherOrganization = watchActivity({
      ...scope,
      organizationId: "org_2",
    })
    const otherMember = watchActivity({ ...scope, userId: "someone-else" })
    renderChat()
    await waitFor(() =>
      expect(own()).toEqual({ threadId: "th_1", state: "idle" })
    )
    expect(otherOrganization()).toBeNull()
    expect(otherMember()).toBeNull()
  })

  it("says nothing before a conversation is chosen, then which one and what it does", async () => {
    stubApi({ threads: [thread("th_1", "Hello")] })
    const activity = watchActivity()
    expect(activity()).toBeNull()
    renderChat()
    expect(activity()).toBeNull()
    await waitFor(() =>
      expect(activity()).toEqual({ threadId: "th_1", state: "idle" })
    )
    open("org_1~th_1")

    // A run being submitted, and then streamed, is running.
    updateChat({ status: "submitted" })
    expect(activity()).toEqual({ threadId: "th_1", state: "running" })
    updateChat({ status: "streaming" })
    expect(activity()).toEqual({ threadId: "th_1", state: "running" })
    updateChat({ status: "ready" })
    expect(activity()).toEqual({ threadId: "th_1", state: "idle" })
  })

  it("says it waits while a confirmation waits on the member, and runs once they answer", async () => {
    const items: Item[] = [
      { type: "message", id: "ev_1", author: "member", text: "Email Sam" },
      {
        type: "confirmation",
        id: "ev_2",
        call_id: "call_1",
        tool_call_id: "tool_1",
        tool: "send_email",
        arguments: { to: "sam@example.com" },
        status: "pending",
      },
    ]
    stubApi({
      threads: [thread("th_1", "Email Sam")],
      items,
      pending: ["call_1"],
    })
    hooks.chat.messages = itemsToChatMessages(items)
    const activity = watchActivity()
    renderChat()
    await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
    open("org_1~th_1")
    await waitFor(() =>
      expect(activity()).toEqual({ threadId: "th_1", state: "waiting" })
    )

    const card = screen.getByRole("group", { name: "Confirm Send email" })
    fireEvent.click(within(card).getByRole("button", { name: "Approve" }))
    updateChat({ status: "streaming" })
    expect(activity()).toEqual({ threadId: "th_1", state: "running" })
  })

  it("follows the member to a new conversation, and runs from its first message", async () => {
    stubApi({ threads: [thread("th_1", "Plan the launch")] })
    const activity = watchActivity()
    renderChat()
    await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
    open("org_1~th_1")

    fireEvent.click(screen.getByRole("button", { name: "New conversation" }))
    expect(activity()).toEqual({ threadId: null, state: "idle" })

    type("Something new")
    await waitFor(() =>
      expect(activity()).toEqual({ threadId: "th_new", state: "running" })
    )
    // The first message goes once the new thread's socket opens.
    open("org_1~th_new")
    updateChat({ status: "streaming" })
    expect(activity()).toEqual({ threadId: "th_new", state: "running" })
    updateChat({ status: "ready" })
    expect(activity()).toEqual({ threadId: "th_new", state: "idle" })
  })

  it("goes idle when the member leaves a running conversation for a new one", async () => {
    stubApi({ threads: [thread("th_1", "Plan the launch")] })
    const activity = watchActivity()
    renderChat()
    await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
    open("org_1~th_1")
    updateChat({ status: "streaming" })
    expect(activity()).toEqual({ threadId: "th_1", state: "running" })

    // The member leaves for a new conversation while it still runs.
    fireEvent.click(screen.getByRole("button", { name: "New conversation" }))
    expect(activity()).toEqual({ threadId: null, state: "idle" })
  })

  it("says it is gone once the chat unmounts", async () => {
    stubApi({ threads: [thread("th_1", "Hello")] })
    const activity = watchActivity()
    const view = renderChat()
    await waitFor(() => expect(activity()).not.toBeNull())
    view.unmount()
    expect(activity()).toBeNull()
  })
})

describe("WorkspaceAgentPanel", () => {
  it("names the member's primary agent and hides the switcher with one agent", async () => {
    stubApi({ threads: [] })
    renderWithQueries(
      <WorkspaceAgentPanel
        expanded
        onExpand={vi.fn()}
        onCollapse={vi.fn()}
        organizationId="org_1"
      />
    )
    expect(
      await screen.findByRole("textbox", { name: "Message Leo" })
    ).toBeTruthy()
    expect(screen.queryByRole("group", { name: "Switch agent" })).toBeNull()
    // The panel's header and its first view are named for the agent.
    expect(screen.getByRole("tab", { name: "Leo" })).toBeTruthy()
    expect(screen.getByText("Only you")).toBeTruthy()
  })
})

describe("primary chat history actions", () => {
  it.each([403, 404])(
    "hides retained approval markers after a %i refusal without restoring canonical cache data",
    async (status) => {
      const waiting = thread("waiting", "")
      const retained: ThreadDetail = {
        ...waiting,
        items: [],
        pending_confirmations: ["approval"],
      }
      let refuse = false
      stubApi({ threads: [waiting], rememberLatest: false })
      const fetcher = vi.mocked(fetch)
      const answer = fetcher.getMockImplementation()!
      fetcher.mockImplementation(async (input, init) => {
        const url = new URL(String(input), "http://localhost")
        if (url.pathname === "/v1/organizations/org_1/threads/waiting")
          return refuse
            ? json({ error: { code: "refused", message: "no" } }, status)
            : json(retained)
        return answer(input, init)
      })
      const view = renderWithQueries(
        <ChatHost initial={{ ...initialPrimaryChat(), thread: null }} />
      )
      await screen.findByText("Ask anything to start a conversation.")
      view.client.setQueryData(
        threadKeys.detail(scope.organizationId, waiting.id),
        retained
      )
      fireEvent.click(screen.getByRole("button", { name: "History" }))
      await screen.findByRole("button", {
        name: /Untitled chat.*Needs approval/,
      })
      fireEvent.change(composer(), { target: { value: "Keep my draft" } })
      refuse = true
      const options = threadApprovalQueryOptions(
        scope.organizationId,
        waiting.id,
        scope.userId
      )
      await act(async () => {
        await view.client.invalidateQueries({ queryKey: options.queryKey })
      })
      expect(view.client.getQueryState(options.queryKey)?.status).toBe("error")
      expect(view.client.getQueryData(options.queryKey)).toEqual(retained)
      await waitFor(() =>
        expect(screen.queryByText(/Needs approval/)).toBeNull()
      )
      expect(screen.queryByRole("button", { name: /Untitled chat/ })).toBeNull()
      expect((composer() as HTMLTextAreaElement).value).toBe("Keep my draft")
    }
  )
  it("keeps older cached approval markers scoped to the current member", async () => {
    const history = Array.from({ length: 8 }, (_, i) =>
      thread(`chat_${i}`, `Chat ${i}`, (i + 1) * 60_000)
    )
    stubApi({ threads: history, rememberLatest: false })
    const view = renderWithQueries(
      <ChatHost initial={{ ...initialPrimaryChat(), thread: null }} />
    )
    const detail = (index: number): ThreadDetail => ({
      ...history[index],
      items: [],
      pending_confirmations: ["approval"],
    })
    view.client.setQueryData(
      threadApprovalQueryOptions(scope.organizationId, "chat_5", scope.userId)
        .queryKey,
      detail(5)
    )
    view.client.setQueryData(
      threadApprovalQueryOptions(scope.organizationId, "chat_6", "someone-else")
        .queryKey,
      detail(6)
    )
    view.client.setQueryData(
      threadKeys.detail(scope.organizationId, "chat_7"),
      detail(7)
    )
    await screen.findByText("Ask anything to start a conversation.")
    fireEvent.click(screen.getByRole("button", { name: "History" }))
    expect(
      await screen.findByRole("button", { name: /Chat 5.*Needs approval/ })
    ).toBeTruthy()
    expect(
      screen.queryByRole("button", { name: /Chat 6.*Needs approval/ })
    ).toBeNull()
    expect(
      screen.queryByRole("button", { name: /Chat 7.*Needs approval/ })
    ).toBeNull()
  })
  it("opens an exact history id and deletes it through the chat object while preserving draft/unsent", async () => {
    const calls = stubApi({
      threads: [thread("th_1", "Earlier", 24 * HOUR), thread("th_2", "Latest")],
      rememberLatest: false,
    })
    const initial = {
      ...initialPrimaryChat(),
      thread: null,
      unsent: [{ id: 30, text: "Still waiting", notice: "Offline." }],
    }
    renderWithQueries(<ChatHost initial={initial} />)
    fireEvent.change(composer(), { target: { value: "Keep my draft" } })
    fireEvent.click(screen.getByRole("button", { name: "History" }))
    fireEvent.click(await screen.findByRole("button", { name: /Earlier.*ago/ }))
    await waitFor(() =>
      expect(hooks.agentOptions.map((item) => item.name)).toContain(
        "org_1~th_1"
      )
    )
    fireEvent.click(screen.getByRole("button", { name: "Delete Earlier" }))
    fireEvent.click(
      screen.getByRole("button", { name: "Confirm delete Earlier" })
    )
    await waitFor(() =>
      expect(calls).toContain("DELETE /agents/thread-chat-agent/org_1~th_1")
    )
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: "Delete Earlier" })
      ).toBeNull()
    )
    expect(screen.getByText("New conversation")).toBeTruthy()
    expect((composer() as HTMLTextAreaElement).value).toBe("Keep my draft")
    expect(notSentWords()).toEqual(["Still waiting"])
  })
  it("renders a long history while fetching approval details for only its newest five chats", async () => {
    const now = Date.now()
    const history = Array.from({ length: 20 }, (_, i) => ({
      id: `chat_${i}`,
      title: `Chat ${i}`,
      origin: "chat",
      updated_at: new Date(now - i * 1000).toISOString(),
    })).reverse()
    const calls = stubApi({ threads: history, rememberLatest: false })
    renderChat()
    expect(
      await screen.findByText("Ask anything to start a conversation.")
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "History" }))
    expect(screen.getAllByRole("listitem")).toHaveLength(20)
    const details = calls.filter((call) =>
      /^GET \/v1\/organizations\/org_1\/threads\//.test(call)
    )
    expect(details).toHaveLength(5)
    expect(details).toEqual(
      expect.arrayContaining(
        Array.from(
          { length: 5 },
          (_, i) => `GET /v1/organizations/org_1/threads/chat_${i}`
        )
      )
    )
  })
  it("opens New when no member was recently present even if the server chat is fresh", async () => {
    stubApi({ threads: [thread("th_1", "Fresh")], rememberLatest: false })
    renderChat()
    expect(
      await screen.findByText("Ask anything to start a conversation.")
    ).toBeTruthy()
    expect(rememberedChoice(scope, LEO)).toBeNull()
  })
})

describe("the open conversation's ⋯ menu", () => {
  async function menuItems(id: string, title: string) {
    rememberChoice(scope, LEO, id)
    stubApi({ threads: [thread(id, title)], rememberLatest: false })
    renderChat()
    await waitFor(() =>
      expect(hooks.agentOptions.map((item) => item.name)).toContain(
        `org_1~${id}`
      )
    )
    fireEvent.click(await screen.findByRole("button", { name: "Chat options" }))
    await screen.findByRole("menuitem", { name: "Delete chat" })
    return screen.getAllByRole("menuitem").map((item) => item.textContent)
  }

  it("offers Rename on a conversation", async () => {
    expect(await menuItems("th_2", "Plan the launch")).toContain("Rename")
  })

  it("has no Rename on the Requests thread, whose title Go keeps", async () => {
    expect(await menuItems("requests-org_1", "Requests")).not.toContain(
      "Rename"
    )
  })
})
