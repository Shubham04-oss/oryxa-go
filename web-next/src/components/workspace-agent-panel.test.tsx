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
import { StrictMode } from "react"
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
  MAX_WAITING_MESSAGES,
  claimPrimaryChatRequest,
  rememberedChoice,
  requestPrimaryChat,
  usePrimaryChatActivity,
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
  /** Each member's own conversations, by user id, for a page more than one member uses in turn. */
  lists?: Record<string, Thread[]>
  /** What each thread holds in Go. */
  items?: Record<string, Item[]>
  pending?: Record<string, string[]>
  /** The first thread made; later ones are `th_new_2`, `th_new_3` and so on. */
  created?: Thread
}

/** A call that starts a conversation. */
const START = `POST /v1/organizations/${ORG}/threads`

/**
 * Go for the primary chat's routes (its threads), and the agents service for
 * everything else. `holdCreate` keeps the next thread from being made until
 * it is released; `failNextCreate` makes the next start fail.
 */
function stubGo(seed: Seed = {}) {
  const backend = fakeAgentsBackend({ agents: [primary, scout] })
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
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }))
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
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

  it("sends a first message into its own conversation though the member went to another agent and back before it was made, and leaves every draft alone", async () => {
    const go = stubGo({ threads: [] })
    withQueries(panel())
    await screen.findByText(blank)
    await screen.findByRole("group", { name: "Switch agent" })
    const release = go.holdCreate()

    type(leoBox(), "Plan the launch")
    fireEvent.keyDown(leoBox(), { key: "Enter" })
    await waitFor(() => expect(go.calls).toContain(START))
    fireEvent.click(within(switcher()).getByRole("button", { name: "Scout" }))
    await screen.findByRole("textbox", { name: "Message Scout" })
    type(scoutBox(), "For Scout")

    // Back with Leo while the conversation is still being made: the message
    // is still on its way, in view, and the composer does not take a second
    // one over it.
    fireEvent.click(within(switcher()).getByRole("button", { name: "Leo" }))
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
    fireEvent.click(within(switcher()).getByRole("button", { name: "Scout" }))
    await screen.findByRole("textbox", { name: "Message Scout" })
    expect(scoutBox().value).toBe("For Scout")
  })
})

describe("a first message on its way", () => {
  it("still goes into its own conversation when the member leaves before its socket takes it", async () => {
    const go = stubGo({ threads: [] })
    withQueries(panel())
    await screen.findByText(blank)
    await screen.findByRole("group", { name: "Switch agent" })

    type(leoBox(), "Plan the launch")
    fireEvent.keyDown(leoBox(), { key: "Enter" })
    // The thread exists and its chat is waiting for the socket.
    await socketOn(chatName("th_new"))
    expect(go.calls).toContain(START)

    fireEvent.click(within(switcher()).getByRole("button", { name: "Scout" }))
    await screen.findByRole("textbox", { name: "Message Scout" })
    fireEvent.click(within(switcher()).getByRole("button", { name: "Leo" }))

    // Back on the same conversation, its message still waiting in it.
    await waitFor(() =>
      expect(roomSockets.named(chatName("th_new")).length).toBeGreaterThan(1)
    )
    roomSockets.named(chatName("th_new")).at(-1)!.open()
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).toHaveBeenCalledExactlyOnceWith({ text: "Plan the launch" })
    expect(leoBox().value).toBe("")
    expect(go.calls.filter((call) => call === START)).toHaveLength(1)
  })

  it("holds a request behind it, then opens what the request asked for", async () => {
    stubGo({ threads: [thread("th_b", "Somewhere else", 3 * HOUR)] })
    withQueries(panel())
    await screen.findByText(blank)
    await screen.findByRole("group", { name: "Switch agent" })

    type(leoBox(), "Plan the launch")
    fireEvent.keyDown(leoBox(), { key: "Enter" })
    const socket = await socketOn(chatName("th_new"))

    // Home opens another conversation while the message waits for it.
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
    expect(rememberedChoice(scope, primary.id)).toBe("th_b")
  })

  it("keeps the member from starting another conversation over it, until its conversation has it", async () => {
    stubGo({ threads: [] })
    withQueries(panel())
    await screen.findByText(blank)
    await screen.findByRole("group", { name: "Switch agent" })
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

  it("gets nothing of the last member's chat: not their conversation, draft, agents or files", async () => {
    const go = stubGo({
      lists: {
        [VIEWER]: [thread("th_a", "Their plan")],
        [other]: [thread("th_b", "Your plan")],
      },
    })
    const reported = vi.fn()
    const view = withQueries(panel({ onPrimaryThreadChange: reported }))
    ;(await socketOn(chatName("th_a"))).open()
    await screen.findByRole("group", { name: "Switch agent" })
    type(leoBox(), "Their draft")
    expect(reported).toHaveBeenLastCalledWith("th_a")
    const sockets = roomSockets.all.length
    const seen = renderHook(() => usePrimaryChatActivity(otherScope))

    // Another tab signed in as someone else, and this one read the session
    // again: same page, same cache, same organization.
    go.as(other)
    const releaseList = go.holdList()
    const releaseAgents = go.holdAgents()
    view.rerender(
      panel({
        onPrimaryThreadChange: reported,
        directMessages: { viewerId: other, inbox, people },
      })
    )
    await act(async () => {})
    // Not their agents (Scout is theirs), their conversation or their files,
    // and nothing for this member to hear of theirs.
    expect(screen.queryByRole("group", { name: "Switch agent" })).toBeNull()
    expect(screen.queryByText("Their plan")).toBeNull()
    expect(reported).toHaveBeenLastCalledWith(null)
    expect(seen.result.current).toBeNull()
    expect(roomSockets.all).toHaveLength(sockets)
    expect(screen.queryByDisplayValue("Their draft")).toBeNull()

    // Their own list decides, once it has loaded.
    act(() => {
      releaseAgents()
      releaseList()
    })
    ;(await socketOn(chatName("th_b"))).open()
    expect(roomSockets.named(chatName("th_a"))).toHaveLength(
      roomSockets.all.filter(
        (socket) => socket.options.name === chatName("th_a")
      ).length
    )
    expect(roomSockets.all.length).toBe(sockets + 1)
    expect(leoBox().value).toBe("")
    expect(reported).toHaveBeenLastCalledWith("th_b")
    await waitFor(() =>
      expect(seen.result.current).toEqual({ threadId: "th_b", state: "idle" })
    )
    // The last member's remembered choice stays theirs.
    expect(rememberedChoice(otherScope, primary.id)).toBeNull()
  })

  it("does not carry a message the last member could not send", async () => {
    const go = stubGo({ threads: [] })
    go.failNextCreate()
    const view = withQueries(panel())
    await screen.findByText(blank)
    await screen.findByRole("group", { name: "Switch agent" })
    act(() =>
      requestPrimaryChat(scope, { kind: "new", message: "Their private words" })
    )
    expect(await screen.findByText(down)).toBeTruthy()

    go.as(other)
    view.rerender(panel({ directMessages: { viewerId: other, inbox, people } }))
    expect(screen.queryByText("Their private words")).toBeNull()
    expect(screen.queryByText(down)).toBeNull()
    await screen.findByRole("textbox", { name: "Message Leo" })
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
  })

  it("does not carry the specialist they had open, or their direct messages' drafts", async () => {
    const go = stubGo({ threads: [thread("th_a", "Their plan")] })
    const view = withQueries(panel())
    ;(await socketOn(chatName("th_a"))).open()
    fireEvent.click(await screen.findByRole("button", { name: "Scout" }))
    await screen.findByRole("textbox", { name: "Message Scout" })

    go.as(other)
    view.rerender(panel({ directMessages: { viewerId: other, inbox, people } }))
    // Back to their own primary agent, not the last member's specialist.
    expect(screen.queryByRole("textbox", { name: "Message Scout" })).toBeNull()
    expect(
      await screen.findByRole("textbox", { name: "Message Leo" })
    ).toBeTruthy()
  })

  it("keeps a member's chat through a moment the session couldn't be read", async () => {
    stubGo({ threads: [thread("th_a", "Their plan")] })
    const view = withQueries(panel())
    ;(await socketOn(chatName("th_a"))).open()
    await screen.findByRole("group", { name: "Switch agent" })
    type(leoBox(), "Still here")

    // The session could not be read for a moment, then it was, unchanged.
    view.rerender(
      panel({ organizationId: undefined, directMessages: undefined })
    )
    view.rerender(panel())
    expect(leoBox().value).toBe("Still here")
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

  it("leaves what the member had typed alone when a request brings a message", async () => {
    stubGo({ threads: [] })
    withQueries(panel())
    await screen.findByText(blank)
    await screen.findByRole("group", { name: "Switch agent" })
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
    withQueries(panel())
    await screen.findByRole("group", { name: "Switch agent" })
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
    withQueries(panel())
    act(() =>
      requestPrimaryChat(scope, { kind: "new", message: "First question" })
    )
    act(() =>
      requestPrimaryChat(scope, { kind: "new", message: "Second question" })
    )
    await act(async () => {})
    // The panel takes none until it knows the agent: nothing is made yet.
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
    withQueries(panel())
    await socketOn(chatName("th_1"))
    expect(roomSockets.named(chatName("th_2"))).toHaveLength(0)
    expect(rememberedChoice(scope, primary.id)).toBe("th_1")
  })

  it("refuses the message after the last it has room for, and keeps its words as not sent", async () => {
    const go = stubGo({ threads: [] })
    const releaseAgents = go.holdAgents()
    // The first conversation is never made here, so what follows only waits.
    go.holdCreate()
    withQueries(panel())
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
    withQueries(panel())
    await screen.findByText(blank)
    await screen.findByRole("group", { name: "Switch agent" })

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
    withQueries(panel())
    await screen.findByText(blank)
    await screen.findByRole("group", { name: "Switch agent" })
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
    withQueries(panel())
    ;(await socketOn(chatName("th_old"))).open()
    await screen.findByRole("group", { name: "Switch agent" })
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
    await waitFor(() =>
      expect(
        document.querySelector(".workspace-agent-thread-title")?.textContent
      ).toBe("Old plan")
    )
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
    withQueries(panel())
    ;(await socketOn(chatName("th_1"))).open()
    const title = () =>
      document.querySelector(".workspace-agent-thread-title")?.textContent
    const comeBack = (after: number) => {
      vi.setSystemTime(Date.now() + after)
      visibility = "visible"
      act(() => {
        document.dispatchEvent(new Event("visibilitychange"))
      })
    }
    await waitFor(() => expect(title()).toBe("Plan the launch"))

    // Nearly an hour on, Home asks for the conversation that is open.
    vi.setSystemTime(Date.now() + 50 * 60_000)
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_1" }))
    comeBack(50 * 60_000)
    expect(title()).toBe("Plan the launch")

    // An hour after the request, it is left as any other.
    comeBack(HOUR)
    expect(title()).toBe("New conversation")
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

  it("sends a message a request brought before the panel mounted, once, into one conversation", async () => {
    const go = stubGo({ threads: [thread("th_1", "Old", 5 * HOUR)] })
    act(() =>
      requestPrimaryChat(scope, { kind: "new", message: "What changed today?" })
    )
    strict(panel())
    ;(await socketOn(chatName("th_new"))).open()
    await act(async () => {})
    expect(go.calls.filter((call) => call === START)).toHaveLength(1)
    expect(
      agentChats.named(chatName("th_new")).sendMessage
    ).toHaveBeenCalledOnce()
  })

  it("opens a conversation a request named before the panel mounted, once", async () => {
    stubGo({
      threads: [thread("th_2", "Newer"), thread("th_1", "Older", 3 * HOUR)],
    })
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "th_1" }))
    strict(panel())
    await socketOn(chatName("th_1"))
    await act(async () => {})
    expect(roomSockets.named(chatName("th_2"))).toHaveLength(0)
    expect(rememberedChoice(scope, primary.id)).toBe("th_1")
  })

  it("sends a typed first message once", async () => {
    const go = stubGo({ threads: [] })
    strict(panel())
    await screen.findByText(blank)
    await screen.findByRole("group", { name: "Switch agent" })
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
