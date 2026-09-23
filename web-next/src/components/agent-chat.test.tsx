// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactElement } from "react"
import { AgentChat } from "./agent-chat"
import { WorkspaceAgentPanel } from "./workspace-agent-panel"
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

const thread = (id: string, title: string): Thread => ({
  id,
  title,
  updated_at: "2026-09-23T10:00:00Z",
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

function renderChat() {
  return renderWithQueries(
    <AgentChat
      organizationId="org_1"
      agentName="Leo"
      onComposingChange={vi.fn()}
    />
  )
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
})

describe("AgentChat", () => {
  it("opens the member's most recent conversation through its Durable Object", async () => {
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
