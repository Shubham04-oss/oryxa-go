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
import type { ComponentProps, ReactElement } from "react"
import {
  ORG,
  VIEWER,
  agent,
  fakeAgentsBackend,
  primary,
} from "./agents-fake-backend"
import type { DmPeople } from "./direct-message-people"
import { WorkspaceAgentPanel } from "./workspace-agent-panel"
import type { Inbox } from "@/lib/dms"
import {
  claimPrimaryChatRequest,
  rememberedChoice,
  requestPrimaryChat,
  usePrimaryChatRequest,
} from "@/lib/threads/chat-choice"
import { itemsToChatMessages } from "@/lib/threads/chat-messages"
import type { Item, Thread, ThreadDetail } from "@/lib/threads/client"
import { agentChats } from "@/test/agent-chat"
import { roomSockets } from "@/test/room-socket"

// The panel holds what each agent's chat keeps while the member looks at
// another, and answers the rest of the app's requests to open a
// conversation. Its sockets and chats are stand-ins (src/test); Go is the
// in-memory agents service, with the member's primary threads added here.
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
const scout = agent({ id: "agt_scout", name: "Scout" })
const inbox: Inbox = { conversations: [], unreadTotal: 0, status: "live" }
const people: DmPeople = {
  status: "ready",
  people: [],
  find: () => undefined,
  nameOf: () => undefined,
  peer: () => undefined,
  refetch: vi.fn(),
}

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
  /** What each thread holds in Go. */
  items?: Record<string, Item[]>
  pending?: Record<string, string[]>
  created?: Thread
}

/**
 * Go for the primary chat's routes (its threads), and the agents service for
 * everything else. `holdCreate` keeps the next thread from being made until
 * it is released.
 */
function stubGo(seed: Seed = {}) {
  const backend = fakeAgentsBackend({ agents: [primary, scout] })
  let threads = seed.threads ?? []
  const created = seed.created ?? thread("th_new", "")
  const calls: string[] = []
  let gate: Promise<void> | undefined
  let agentsGate: Promise<void> | undefined
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
          threads = [created, ...threads]
          return json(created, 201)
        }
        return json({ threads })
      }
      const detail = new RegExp(`^/v1/organizations/${ORG}/threads/([^/]+)$`)
      const found = detail.exec(path)
      if (found) {
        const id = decodeURIComponent(found[1])
        const listed = threads.find((item) => item.id === id)
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
    rerender: (next: ReactElement) => view.rerender(wrap(next)),
  }
}

/** The panel as the shell mounts it, with a member (so it knows whose choices to keep) unless told otherwise. */
function panel(
  props: Partial<ComponentProps<typeof WorkspaceAgentPanel>> = {}
) {
  return (
    <WorkspaceAgentPanel
      expanded
      onExpand={vi.fn()}
      onCollapse={vi.fn()}
      organizationId={ORG}
      directMessages={{ viewerId: VIEWER, inbox, people }}
      {...props}
    />
  )
}

const leoBox = () =>
  screen.getByRole<HTMLTextAreaElement>("textbox", { name: "Message Leo" })
const scoutBox = () =>
  screen.getByRole<HTMLTextAreaElement>("textbox", { name: "Message Scout" })
const blank = "Ask anything to start a conversation."

function chatName(threadId: string) {
  return `${ORG}~${threadId}`
}

/** The latest socket on a chat object, once one has been asked for. */
async function socketOn(name: string) {
  await waitFor(() => expect(roomSockets.named(name).length).toBeGreaterThan(0))
  return roomSockets.named(name).at(-1)!
}

function switcher() {
  return screen.getByRole("group", { name: "Switch agent" })
}

function type(box: HTMLTextAreaElement, text: string) {
  fireEvent.change(box, { target: { value: text } })
}

beforeEach(() => {
  // A request an earlier test left unclaimed must not reach this one.
  const { result, unmount } = renderHook(() => usePrimaryChatRequest())
  if (result.current) claimPrimaryChatRequest(result.current.id)
  unmount()
  roomSockets.reset()
  agentChats.reset()
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }))
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.sessionStorage.clear()
})

describe("what the agents' chats keep across a switch", () => {
  it("keeps each agent's unsent message while the member visits the other", async () => {
    stubGo({ threads: [thread("th_1", "Plan the launch")] })
    withQueries(panel())
    ;(await socketOn(chatName("th_1"))).open()
    await screen.findByRole("group", { name: "Switch agent" })
    type(leoBox(), "Half a thought")

    fireEvent.click(within(switcher()).getByRole("button", { name: "Scout" }))
    await screen.findByRole("textbox", { name: "Message Scout" })
    expect(screen.queryByRole("textbox", { name: "Message Leo" })).toBeNull()
    await waitFor(() =>
      expect(roomSockets.named(chatName(scout.id)).length).toBeGreaterThan(0)
    )
    roomSockets.named(chatName(scout.id)).at(-1)!.open()
    type(scoutBox(), "A note for Scout")

    fireEvent.click(within(switcher()).getByRole("button", { name: "Leo" }))
    await screen.findByRole("textbox", { name: "Message Leo" })
    expect(leoBox().value).toBe("Half a thought")

    fireEvent.click(within(switcher()).getByRole("button", { name: "Scout" }))
    await screen.findByRole("textbox", { name: "Message Scout" })
    expect(scoutBox().value).toBe("A note for Scout")
  })

  it("keeps the primary chat's conversation, so leaving it does not decide it again", async () => {
    // No member, so nothing is remembered in the browser: the panel alone
    // holds where the chat was.
    stubGo({ threads: [thread("th_1", "Plan the launch")] })
    withQueries(panel({ directMessages: undefined }))
    ;(await socketOn(chatName("th_1"))).open()
    await screen.findByRole("group", { name: "Switch agent" })

    const sockets = roomSockets.named(chatName("th_1")).length

    // A new conversation, though a fresh one exists.
    fireEvent.click(screen.getByRole("button", { name: "New conversation" }))
    expect(screen.getByText(blank)).toBeTruthy()

    fireEvent.click(within(switcher()).getByRole("button", { name: "Scout" }))
    await screen.findByRole("textbox", { name: "Message Scout" })
    fireEvent.click(within(switcher()).getByRole("button", { name: "Leo" }))

    // Still the new conversation, not the fresh one the rules would pick.
    expect(await screen.findByText(blank)).toBeTruthy()
    expect(leoBox().hasAttribute("disabled")).toBe(false)
    expect(roomSockets.named(chatName("th_1"))).toHaveLength(sockets)
  })

  it("puts a first message back in the composer when the member left before its thread was made", async () => {
    const go = stubGo({ threads: [] })
    withQueries(panel())
    await screen.findByText(blank)
    await screen.findByRole("group", { name: "Switch agent" })
    const release = go.holdCreate()

    type(leoBox(), "Plan the launch")
    fireEvent.keyDown(leoBox(), { key: "Enter" })
    await waitFor(() =>
      expect(go.calls).toContain(`POST /v1/organizations/${ORG}/threads`)
    )
    fireEvent.click(within(switcher()).getByRole("button", { name: "Scout" }))
    await screen.findByRole("textbox", { name: "Message Scout" })
    act(() => release())

    fireEvent.click(within(switcher()).getByRole("button", { name: "Leo" }))
    // Back on the new conversation with the words they sent, and nothing
    // was sent to a chat nobody was looking at.
    expect(await screen.findByText(blank)).toBeTruthy()
    await waitFor(() => expect(leoBox().value).toBe("Plan the launch"))
    expect(roomSockets.named(chatName("th_new"))).toHaveLength(0)
  })
})

describe("a first message on its way", () => {
  it("goes back to the composer when the member leaves before the new thread's socket takes it", async () => {
    const go = stubGo({ threads: [] })
    withQueries(panel())
    await screen.findByText(blank)
    await screen.findByRole("group", { name: "Switch agent" })

    type(leoBox(), "Plan the launch")
    fireEvent.keyDown(leoBox(), { key: "Enter" })
    // The thread exists and its chat is waiting for the socket.
    await socketOn(chatName("th_new"))
    expect(go.calls).toContain(`POST /v1/organizations/${ORG}/threads`)

    fireEvent.click(within(switcher()).getByRole("button", { name: "Scout" }))
    await screen.findByRole("textbox", { name: "Message Scout" })
    fireEvent.click(within(switcher()).getByRole("button", { name: "Leo" }))

    await waitFor(() => expect(leoBox().value).toBe("Plan the launch"))
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).not.toHaveBeenCalled()
  })

  it("is not sent into the conversation a request moved the chat to meanwhile", async () => {
    const go = stubGo({ threads: [thread("th_b", "Somewhere else", 3 * HOUR)] })
    withQueries(panel())
    await screen.findByText(blank)
    await screen.findByRole("group", { name: "Switch agent" })
    const release = go.holdCreate()

    type(leoBox(), "Plan the launch")
    fireEvent.keyDown(leoBox(), { key: "Enter" })
    await waitFor(() =>
      expect(go.calls).toContain(`POST /v1/organizations/${ORG}/threads`)
    )
    // Home opens another conversation while the new thread is being made.
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_b" }))
    const socket = await socketOn(chatName("th_b"))
    act(() => release())
    socket.open()

    // The message went nowhere, and is back in the composer; the chat stays
    // on the conversation it was asked to open.
    await waitFor(() => expect(leoBox().value).toBe("Plan the launch"))
    expect(
      agentChats.named(chatName("th_b")).sendMessage
    ).not.toHaveBeenCalled()
    expect(roomSockets.named(chatName("th_new"))).toHaveLength(0)
    expect(rememberedChoice(scope, primary.id)).toBe("th_b")
  })
})

describe("a request to open a conversation", () => {
  it("opens the conversation it names on the primary agent, and remembers the choice", async () => {
    stubGo({
      threads: [thread("th_2", "Newer"), thread("th_1", "Older", 3 * HOUR)],
    })
    withQueries(panel())
    ;(await socketOn(chatName("th_2"))).open()
    await screen.findByRole("group", { name: "Switch agent" })
    // The member is with Scout when the request comes.
    fireEvent.click(within(switcher()).getByRole("button", { name: "Scout" }))
    await screen.findByRole("textbox", { name: "Message Scout" })

    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_1" }))

    await socketOn(chatName("th_1"))
    await screen.findByRole("textbox", { name: "Message Leo" })
    expect(
      within(switcher()).getByRole("button", { name: "Leo", pressed: true })
    ).toBeTruthy()
    expect(
      document.querySelector(".workspace-agent-thread-title")?.textContent
    ).toBe("Older")
    expect(rememberedChoice(scope, primary.id)).toBe("th_1")
  })

  it("brings the Agents tab forward", async () => {
    stubGo({ threads: [thread("th_1", "Plan the launch")] })
    withQueries(panel())
    ;(await socketOn(chatName("th_1"))).open()
    fireEvent.click(screen.getByRole("tab", { name: "DMs" }))
    expect(
      screen.getByRole("tab", { name: "DMs" }).getAttribute("aria-selected")
    ).toBe("true")

    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_1" }))
    expect(
      screen.getByRole("tab", { name: "Agents" }).getAttribute("aria-selected")
    ).toBe("true")
  })

  it("applies a request made before the panel was ever opened, once it is", async () => {
    stubGo({
      threads: [thread("th_2", "Newer"), thread("th_1", "Older", 3 * HOUR)],
    })
    const view = withQueries(panel({ expanded: false }))
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_1" }))
    // The chat is not there yet: the panel holds what was asked for.
    expect(roomSockets.all).toHaveLength(0)

    view.rerender(panel({ expanded: true }))
    await socketOn(chatName("th_1"))
    // It never opened the fresh conversation first.
    expect(roomSockets.named(chatName("th_2"))).toHaveLength(0)
    expect(rememberedChoice(scope, primary.id)).toBe("th_1")
  })

  it("applies a request made before the panel first mounted", async () => {
    stubGo({
      threads: [thread("th_2", "Newer"), thread("th_1", "Older", 3 * HOUR)],
    })
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_1" }))
    withQueries(panel())
    await socketOn(chatName("th_1"))
    expect(roomSockets.named(chatName("th_2"))).toHaveLength(0)
  })

  it("acts on a request once: nothing re-applies it when the panel renders or mounts again", async () => {
    stubGo({ threads: [thread("th_1", "Older", 3 * HOUR)] })
    const view = withQueries(panel())
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_1" }))
    ;(await socketOn(chatName("th_1"))).open()

    // The member goes to a new conversation instead.
    fireEvent.click(screen.getByRole("button", { name: "New conversation" }))
    expect(screen.getByText(blank)).toBeTruthy()
    view.rerender(panel({ expanded: true, primaryWork: false }))
    expect(screen.getByText(blank)).toBeTruthy()

    // A panel mounted afresh (the shell may be) does not open it again.
    view.unmount()
    roomSockets.reset()
    withQueries(panel())
    expect(await screen.findByText(blank)).toBeTruthy()
    expect(roomSockets.all).toHaveLength(0)
  })

  it("starts a new conversation with the message a request brings, once, as the composer would", async () => {
    const go = stubGo({ threads: [thread("th_1", "Plan the launch")] })
    withQueries(panel())
    ;(await socketOn(chatName("th_1"))).open()

    act(() =>
      requestPrimaryChat(scope, {
        kind: "new",
        message: "  What changed today?  ",
      })
    )
    await waitFor(() =>
      expect(go.calls).toContain(`POST /v1/organizations/${ORG}/threads`)
    )
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

  it("waits for the agents list, so what it remembers is kept under the agent's id", async () => {
    const go = stubGo({
      threads: [thread("th_2", "Newer"), thread("th_1", "Older", 3 * HOUR)],
    })
    const release = go.holdAgents()
    withQueries(panel())
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
    withQueries(panel())
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
    withQueries(panel())
    ;(await socketOn(chatName("th_2"))).open()

    act(() =>
      requestPrimaryChat(
        { organizationId: ORG, userId: "someone-else" },
        { kind: "thread", threadId: "th_1" }
      )
    )
    await act(async () => {})
    expect(roomSockets.named(chatName("th_1"))).toHaveLength(0)
    expect(rememberedChoice(scope, primary.id)).toBeNull()
  })
})

describe("a file the agent saves", () => {
  const saved: Item[] = [
    { type: "message", id: "ev_1", author: "member", text: "Write it up" },
    { type: "artifact", id: "ev_f", artifact: "report.pdf", version: 2 },
  ]

  /** A run in the primary chat, on the panel as first rendered, with the file's callback watched. */
  async function inARun(
    props: Partial<ComponentProps<typeof WorkspaceAgentPanel>> = {}
  ) {
    stubGo({ threads: [thread("th_1", "Write it up")] })
    const onFile = vi.fn()
    const view = withQueries(panel({ onPrimaryFileCreated: onFile, ...props }))
    ;(await socketOn(chatName("th_1"))).open()
    const chat = agentChats.named(chatName("th_1"))
    return {
      view,
      onFile,
      /** The run writes the file and ends. */
      finish() {
        chat.update({ status: "streaming" })
        chat.update({ messages: itemsToChatMessages(saved) })
        chat.update({ status: "ready" })
      },
    }
  }

  it("opens in Agent overview when the chat is in front of the member", async () => {
    const { onFile, finish } = await inARun()
    finish()
    expect(onFile).toHaveBeenCalledExactlyOnceWith({
      threadId: "th_1",
      name: "report.pdf",
      version: 2,
    })
  })

  it("leaves only the chip in the chat while the panel is collapsed, and does not open it on expanding", async () => {
    const { view, onFile, finish } = await inARun()
    view.rerender(panel({ expanded: false, onPrimaryFileCreated: onFile }))

    finish()
    expect(onFile).not.toHaveBeenCalled()
    // The chat is still there, hidden, with the file in it.
    expect(screen.getByText("report.pdf")).toBeTruthy()

    view.rerender(panel({ expanded: true, onPrimaryFileCreated: onFile }))
    expect(onFile).not.toHaveBeenCalled()
    expect(screen.getByRole("link", { name: "report.pdf" })).toBeTruthy()
  })

  it("leaves only the chip when the panel is on another tab", async () => {
    const { onFile, finish } = await inARun()
    fireEvent.click(screen.getByRole("tab", { name: "DMs" }))
    finish()
    expect(onFile).not.toHaveBeenCalled()
    expect(screen.getByText("report.pdf")).toBeTruthy()
  })

  it("opens in Agent overview from the focused overview's conversation column, whatever the panel does", async () => {
    const { onFile, finish } = await inARun({
      expanded: false,
      primaryWork: true,
    })
    finish()
    expect(onFile).toHaveBeenCalledOnce()
  })

  it("leaves only the chip while the member is with a specialist", async () => {
    const { onFile, finish } = await inARun()
    fireEvent.click(within(switcher()).getByRole("button", { name: "Scout" }))
    await screen.findByRole("textbox", { name: "Message Scout" })
    finish()
    expect(onFile).not.toHaveBeenCalled()
  })
})
