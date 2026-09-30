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
  }
}

function withQueries(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
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
