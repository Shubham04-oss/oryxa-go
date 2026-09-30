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
import { useState } from "react"
import type { ReactElement } from "react"
import { AgentChat } from "./agent-chat"
import { WorkspaceAgentPanel } from "./workspace-agent-panel"
import {
  rememberChoice,
  rememberedChoice,
  usePrimaryChatActivity,
} from "@/lib/threads/chat-choice"
import { itemsToChatMessages } from "@/lib/threads/chat-messages"
import type { Item, Thread } from "@/lib/threads/client"

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
} = {}) {
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
      if (path.endsWith("/get-messages")) return json([])
      const detail = /^\/v1\/organizations\/org_1\/threads\/([^/]+)$/.exec(path)
      if (detail) {
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

function renderWithQueries(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

const scope = { organizationId: "org_1", userId: "user_1" }
/** The primary agent, whose id keys the choices the chat remembers. */
const LEO = "agt_leo"

/**
 * The chat as the panel mounts it, holding its conversation and draft.
 * `agent` is the primary agent's id, or null while the agents list is out.
 */
function ChatHost({
  remember = true,
  agent = LEO,
}: {
  remember?: boolean
  agent?: string | null
}) {
  const [threadId, setThreadId] = useState<string | null | undefined>()
  const [draft, setDraft] = useState("")
  return (
    <AgentChat
      organizationId="org_1"
      scope={remember ? scope : undefined}
      agentId={agent ?? undefined}
      agentName="Leo"
      threadId={threadId}
      setThreadId={setThreadId}
      draft={draft}
      onDraftChange={setDraft}
      onComposingChange={vi.fn()}
    />
  )
}

function renderChat() {
  return renderWithQueries(<ChatHost />)
}

const composer = () => screen.getByRole("textbox", { name: "Message Leo" })

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

    // Waits for the socket before it takes a message.
    expect(composer().hasAttribute("disabled")).toBe(true)
    expect(screen.getByText("Connecting…")).toBeTruthy()
    open("org_1~th_2")
    expect(composer().hasAttribute("disabled")).toBe(false)
    expect(screen.queryByText("Connecting…")).toBeNull()

    type("  Draft the brief  ")
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
    expect(composer().hasAttribute("disabled")).toBe(true)
    // A dropped socket refreshes the session its reconnect will carry.
    await waitFor(() => expect(calls).toContain("GET /v1/auth/me"))
    open("org_1~th_1")
    expect(screen.queryByText("Reconnecting…")).toBeNull()
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
    const row = () => document.querySelector(".workspace-agent-tools")
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
    const row = () => document.querySelector(".workspace-agent-tools")
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
      const rows = document.querySelectorAll(".workspace-agent-tools")
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
    ).toBe(true)

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

  it("ignores a chosen conversation that no longer exists", async () => {
    rememberChoice(scope, LEO, "th_gone")
    stubApi({ threads: [thread("th_1", "Fresh", 60_000)] })
    renderChat()
    await opensThread("th_1")
  })

  it("keeps a member's choice of a new chat over a fresh conversation, and asks Go about nothing", async () => {
    rememberChoice(scope, LEO, null)
    const calls = stubApi({ threads: [thread("th_1", "Fresh", 60_000)] })
    renderChat()

    expect(await screen.findByText(blank)).toBeTruthy()
    expect(hooks.agentOptions).toHaveLength(0)
    expect(calls.some((call) => call.includes("/threads/th_1"))).toBe(false)
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
    await opensThread("th_1")
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

describe("a conversation left alone", () => {
  let visibility: DocumentVisibilityState = "visible"

  beforeEach(() => {
    // Only the clock moves: the chat's own timers and promises run as usual.
    vi.useFakeTimers({ toFake: ["Date"] })
    visibility = "visible"
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => visibility,
    })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  /** Time passes; then the tab shows again, or the window is focused. */
  function comeBack(
    after: number,
    event: "visibilitychange" | "focus" = "visibilitychange"
  ) {
    vi.setSystemTime(Date.now() + after)
    act(() => {
      if (event === "focus") window.dispatchEvent(new Event("focus"))
      else document.dispatchEvent(new Event("visibilitychange"))
    })
  }

  /** A new chat is titled so; a conversation shows its own title. */
  const onNewChat = () => screen.queryByText("New conversation") !== null

  async function openThread() {
    const calls = stubApi({ threads: [thread("th_1", "Plan the launch")] })
    renderChat()
    await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
    open("org_1~th_1")
    return calls
  }

  it("gives way to a new chat when the tab shows again after an hour, and deletes nothing", async () => {
    const calls = await openThread()
    fireEvent.change(composer(), { target: { value: "Half a thought" } })

    comeBack(HOUR)
    expect(onNewChat()).toBe(true)
    expect(rememberedChoice(scope, LEO)).toBe("new")
    // What they were typing goes with them, and the old conversation stays.
    expect((composer() as HTMLTextAreaElement).value).toBe("Half a thought")
    expect(calls.filter((call) => call.startsWith("DELETE"))).toEqual([])
    expect(calls.filter((call) => call.startsWith("POST"))).toEqual([])
  })

  it("does the same when the window is focused again", async () => {
    await openThread()
    comeBack(3 * HOUR, "focus")
    expect(onNewChat()).toBe(true)
    expect(rememberedChoice(scope, LEO)).toBe("new")
  })

  it("leaves a conversation alone that was active within the hour", async () => {
    await openThread()
    comeBack(HOUR - 60_000)
    expect(onNewChat()).toBe(false)
    expect(rememberedChoice(scope, LEO)).toBeNull()
  })

  it("counts what the member sends, and what the agent says, as activity", async () => {
    await openThread()
    vi.setSystemTime(Date.now() + 50 * 60_000)
    type("Still here")
    comeBack(50 * 60_000)
    expect(onNewChat()).toBe(false)

    // Half an hour on, the agent answers.
    vi.setSystemTime(Date.now() + 30 * 60_000)
    updateChat({
      messages: itemsToChatMessages([
        { type: "message", id: "ev_2", author: "Leo", text: "Hi there" },
      ]),
    })
    comeBack(45 * 60_000)
    expect(onNewChat()).toBe(false)

    comeBack(30 * 60_000)
    expect(onNewChat()).toBe(true)
  })

  it("does not take a reconnect that hands over the same conversation again for news", async () => {
    await openThread()
    comeBack(50 * 60_000)
    // The socket reconnected in the background: the same messages, again.
    updateChat({ messages: [...hooks.chat.messages] })
    comeBack(20 * 60_000)
    expect(onNewChat()).toBe(true)
  })

  it("does not leave a run that is streaming, and starts counting when it ends", async () => {
    await openThread()
    updateChat({ status: "streaming" })
    comeBack(3 * HOUR)
    expect(onNewChat()).toBe(false)

    updateChat({ status: "ready" })
    comeBack(30 * 60_000)
    expect(onNewChat()).toBe(false)
    comeBack(HOUR)
    expect(onNewChat()).toBe(true)
  })

  it("does not leave a confirmation that waits on the member", async () => {
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
    renderChat()
    await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
    open("org_1~th_1")
    await screen.findByRole("group", { name: "Confirm Send email" })

    comeBack(6 * HOUR)
    expect(onNewChat()).toBe(false)
    expect(
      screen.getByRole("group", { name: "Confirm Send email" })
    ).toBeTruthy()
  })

  it("waits for the tab to be visible", async () => {
    await openThread()
    visibility = "hidden"
    comeBack(3 * HOUR)
    expect(onNewChat()).toBe(false)

    visibility = "visible"
    comeBack(0, "focus")
    expect(onNewChat()).toBe(true)
  })

  it("never takes a conversation from the member the moment they chose it", async () => {
    rememberChoice(scope, LEO, "th_1")
    stubApi({ threads: [thread("th_1", "Chosen", 30 * HOUR)] })
    renderChat()
    await waitFor(() => expect(hooks.agentOptions.length).toBeGreaterThan(0))
    open("org_1~th_1")

    comeBack(0)
    comeBack(0, "focus")
    expect(onNewChat()).toBe(false)
    expect(rememberedChoice(scope, LEO)).toBe("th_1")
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
    expect(
      within(screen.getByRole("tab", { name: "Agents" })).getByLabelText("Leo")
    ).toBeTruthy()
  })
})
