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
import { SpecialistChat } from "./specialist-chat"
import { rememberChoice, rememberedView } from "@/lib/threads/chat-choice"
import { AgentsFocus } from "./agents-mode"
import {
  ORG,
  VIEWER,
  agent,
  fakeAgentsBackend,
  primary,
  team,
} from "./agents-fake-backend"
import type { FakeAgentsBackend } from "./agents-fake-backend"
import { WorkspaceAgentPanel } from "./workspace-agent-panel"
import { WorkspaceShell } from "./workspace-shell"
import type { Session } from "@/lib/auth/client"
import type { SpecialistResult } from "@/lib/specialists/result"
import { itemsToChatMessages } from "@/lib/threads/chat-messages"
import type { ThreadChatMessage } from "@/lib/threads/chat-messages"
import type { Item, ThreadDetail } from "@/lib/threads/client"
import { agentChats } from "@/test/agent-chat"
import { roomSockets } from "@/test/room-socket"
import { contribution, teamspace } from "@/test/teamspaces"

// Each view's socket to the specialist's chat object, and the chat on it,
// stand in for the Agents SDK (src/test/room-socket.ts, agent-chat.ts);
// the Worker's side of the object and Go are the in-memory agents service.
vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
)
vi.mock(
  "@cloudflare/ai-chat/react",
  async () => (await import("@/test/agent-chat")).aiChatReact
)

const session: Session = {
  user: { id: VIEWER, email: "member@example.com", display_name: "Member" },
  organizations: [{ id: ORG, name: "Simulated Org", slug: "sim" }],
}

const scout = agent({
  id: "agt_scout",
  name: "Scout",
  description: "Finds and checks sources",
})
const atlas = agent({ id: "agt_atlas", name: "Atlas" })
const quill = agent({ id: "agt_quill", name: "Quill", status: "disabled" })
const scoutChat = `${ORG}~${scout.id}`

const result: SpecialistResult = {
  status: "completed",
  summary: "Two deals **stalled** this week.",
  tools_used: ["search_web", "read_sheet", "search_web"],
  artifacts: ["user:q3-report.pdf"],
  handoffs: [{ target: "Planner", request: "Draft the rollout" }],
}
const asked: Item = {
  type: "message",
  id: "ev_1",
  author: "member",
  text: "Review the pipeline",
}
// What the object stores and streams: the answer made readable.
const answered: Item = {
  type: "message",
  id: "ev_2",
  author: "Scout",
  text: result.summary,
  result,
}
// What Go stores: the same answer as its raw JSON work result.
const answeredInGo: Item = {
  type: "message",
  id: "ev_2",
  author: "Scout",
  text: JSON.stringify(result),
}

function transcript(...items: Item[]): ThreadChatMessage[] {
  return itemsToChatMessages(items)
}

const HOUR = 60 * 60 * 1000

/** Active a minute ago, unless told otherwise: fresh. */
function conversation(fields: Partial<ThreadDetail> = {}): ThreadDetail {
  return {
    id: "th_1",
    title: "Review the pipeline",
    updated_at: new Date(Date.now() - 60_000).toISOString(),
    items: [asked, answeredInGo],
    pending_confirmations: [],
    ...fields,
  }
}

function withQueries(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

function renderPanel(
  backend: FakeAgentsBackend,
  chatRequest?: { agentId: string }
) {
  vi.stubGlobal("fetch", vi.fn(backend.fetch))
  return withQueries(
    <WorkspaceAgentPanel
      expanded
      onExpand={vi.fn()}
      onCollapse={vi.fn()}
      organizationId={ORG}
      chatRequest={chatRequest}
    />
  )
}

function renderFocus(backend: FakeAgentsBackend, agentId = scout.id) {
  vi.stubGlobal("fetch", vi.fn(backend.fetch))
  return withQueries(
    <AgentsFocus
      target={{ kind: "specialist", agentId }}
      organizationId={ORG}
      onBack={vi.fn()}
      onOpen={vi.fn()}
    />
  )
}

function renderShell(
  backend: FakeAgentsBackend,
  section: "agents" | "multi-agents" | "specialists"
) {
  vi.stubGlobal("fetch", vi.fn(backend.fetch))
  return withQueries(
    <WorkspaceShell
      session={session}
      selectedOrganization={session.organizations[0]}
      initialMode="agents"
      initialSection={section}
    />
  )
}

/** The latest socket on a specialist's chat object. */
function chatSocket(name = scoutChat) {
  const socket = roomSockets.named(name).at(-1)
  if (!socket) throw new Error(`no socket on ${name}`)
  return socket
}

/**
 * Waits for a chat's transcript to load: the chat then asks for its socket.
 * What showed while it loaded is replaced, so query the chat after this.
 */
async function chatLoaded(sockets = 1, name = scoutChat) {
  await waitFor(() => expect(roomSockets.named(name)).toHaveLength(sockets))
}

function conversationTitle() {
  return document.querySelector(".workspace-agent-thread-title")?.textContent
}

beforeEach(() => {
  roomSockets.reset()
  agentChats.reset()
  window.sessionStorage.clear()
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
})

describe("The agent panel's quick switch", () => {
  it("switches the Agents tab between the primary agent and active specialists", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, quill, scout],
      chats: { [scout.id]: transcript(asked, answered) },
    })
    renderPanel(backend)

    const switcher = await screen.findByRole("group", { name: "Switch agent" })
    // The one in use is the named pill; the rest are avatar-only, and a
    // paused specialist is not offered.
    const leo = within(switcher).getByRole("button", {
      name: "Leo",
      pressed: true,
    })
    expect(within(leo).getByText("Leo")).toBeTruthy()
    const scoutPill = within(switcher).getByRole("button", {
      name: "Scout",
      pressed: false,
    })
    expect(within(scoutPill).queryByText("Scout")).toBeNull()
    expect(within(switcher).getAllByRole("button")).toHaveLength(2)
    expect(screen.getByRole("textbox", { name: "Message Leo" })).toBeTruthy()

    fireEvent.click(scoutPill)
    expect(
      within(switcher).getByRole("button", { name: "Scout", pressed: true })
    ).toBeTruthy()
    expect(within(switcher).getByText("Scout")).toBeTruthy()
    await chatLoaded()
    const log = screen.getByRole("log", { name: "Conversation with Scout" })
    expect(within(log).getByText("Review the pipeline")).toBeTruthy()
    const socket = chatSocket()
    expect(socket.options.agent).toBe("specialist-chat-agent")
    const composer = screen.getByRole("textbox", { name: "Message Scout" })
    expect(composer.hasAttribute("disabled")).toBe(true)
    socket.open()
    expect(composer.hasAttribute("disabled")).toBe(false)
    expect(screen.queryByRole("textbox", { name: "Message Leo" })).toBeNull()

    fireEvent.click(within(switcher).getByRole("button", { name: "Leo" }))
    expect(
      await screen.findByRole("textbox", { name: "Message Leo" })
    ).toBeTruthy()
    expect(
      screen.queryByRole("log", { name: "Conversation with Scout" })
    ).toBeNull()
  })

  it("hides the row when the primary agent is the member's only active agent", async () => {
    renderPanel(fakeAgentsBackend({ agents: [primary, quill] }))
    expect(
      await screen.findByRole("textbox", { name: "Message Leo" })
    ).toBeTruthy()
    expect(screen.queryByRole("group", { name: "Switch agent" })).toBeNull()
  })

  it("keeps a specialist deleted while in use, and says it no longer exists", async () => {
    const backend = fakeAgentsBackend({ agents: [primary, scout, atlas] })
    renderPanel(backend, { agentId: scout.id })
    await waitFor(() => expect(roomSockets.named(scoutChat)).toHaveLength(1))
    chatSocket().open()
    expect(
      screen
        .getByRole("textbox", { name: "Message Scout" })
        .hasAttribute("disabled")
    ).toBe(false)

    // Deleted in another tab: the Worker now turns the socket away, which a
    // browser learns only as a close. Go says why.
    backend.state.agents = backend.state.agents.filter(
      (entry) => entry.id !== scout.id
    )
    chatSocket().drop()
    expect(await screen.findByText("Scout no longer exists.")).toBeTruthy()
    const closed = screen.getByRole("textbox", { name: "Message Scout" })
    expect(closed.hasAttribute("disabled")).toBe(true)
    expect(closed.getAttribute("placeholder")).toBe("Scout no longer exists")
    // Still the one in use until the member picks another.
    const switcher = screen.getByRole("group", { name: "Switch agent" })
    expect(
      within(switcher).getByRole("button", { name: "Scout", pressed: true })
    ).toBeTruthy()
    // Its socket stops trying, and no other opens.
    expect(roomSockets.named(scoutChat)).toHaveLength(1)
    expect(chatSocket().options.enabled).toBe(false)

    fireEvent.click(within(switcher).getByRole("button", { name: "Atlas" }))
    expect(within(switcher).queryByRole("button", { name: "Scout" })).toBeNull()
  })
})

describe("A specialist's focused view", () => {
  it("shows its chat in the conversation column, on the same object as the panel", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      chats: { [scout.id]: transcript(asked, answered) },
    })
    renderShell(backend, "specialists")
    fireEvent.click(await screen.findByRole("button", { name: "Open Scout" }))

    const column = screen.getByRole("region", { name: "Chat with Scout" })
    await chatLoaded()
    const log = within(column).getByRole("log", {
      name: "Conversation with Scout",
    })
    expect(within(log).getByText("Review the pipeline")).toBeTruthy()
    expect(within(log).getByText("stalled")).toBeTruthy()
    expect(screen.queryByText(/opens in the agent panel soon/)).toBeNull()
    const composer = within(column).getByRole("textbox", {
      name: "Message Scout",
    })
    expect(composer.hasAttribute("disabled")).toBe(true)
    chatSocket().open()
    expect(composer.hasAttribute("disabled")).toBe(false)
    expect(within(column).getByText("Ready")).toBeTruthy()

    // The agent panel on the same specialist is a second socket on the same
    // object, which sends both the same conversation.
    fireEvent.click(screen.getByRole("button", { name: "Expand agent panel" }))
    const panel = screen.getByRole("complementary", { name: "Agent panel" })
    fireEvent.click(await within(panel).findByRole("button", { name: "Scout" }))
    await chatLoaded(2)
    expect(within(panel).getByText("Review the pipeline")).toBeTruthy()
    const sockets = roomSockets.named(scoutChat)
    expect(
      sockets.every(
        (socket) => socket.options.agent === "specialist-chat-agent"
      )
    ).toBe(true)

    const followUp = transcript(asked, answered, {
      type: "message",
      id: "ev_3",
      author: "member",
      text: "And next week?",
    })
    for (const chat of agentChats.all.filter(
      (candidate) => candidate.name === scoutChat
    )) {
      chat.update({ messages: followUp })
    }
    expect(within(column).getByText("And next week?")).toBeTruthy()
    expect(within(panel).getByText("And next week?")).toBeTruthy()
  })

  it("says the specialist is working while its draft has no words, then shows its readable result", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      chats: { [scout.id]: transcript(asked) },
    })
    renderFocus(backend)
    await chatLoaded()
    const log = screen.getByRole("log", { name: "Conversation with Scout" })
    chatSocket().open()
    const chat = agentChats.named(scoutChat)

    // The object streams one progress marker per draft: a delta with no text.
    const marker: Item = { type: "delta", author: "Scout", text: "" }
    chat.update({ status: "streaming", messages: transcript(asked, marker) })
    expect(within(log).getByRole("status").textContent).toBe(
      "Scout is working…"
    )
    expect(log.querySelector('[data-from="agent"]')).toBeNull()
    expect(screen.getByText("Working")).toBeTruthy()
    expect(screen.getByRole("button", { name: "Stop response" })).toBeTruthy()
    expect(
      screen
        .getByRole("button", { name: "New conversation" })
        .hasAttribute("disabled")
    ).toBe(true)

    // The finished message takes the marker's place, readable: its summary,
    // and one quiet line for the tools, files and hand-offs of its result.
    const tools: Item[] = [
      {
        type: "tool_call",
        id: "ev_t",
        call_id: "call_1",
        author: "Scout",
        tool: "search_web",
      },
      {
        type: "tool_result",
        id: "ev_t2",
        call_id: "call_1",
        author: "Scout",
        tool: "search_web",
      },
    ]
    const file: Item = {
      type: "artifact",
      id: "ev_f",
      author: "Scout",
      artifact: "user:q3-report.pdf",
      version: 1,
    }
    chat.update({
      status: "ready",
      messages: transcript(asked, ...tools, answered, file),
    })
    expect(within(log).queryByText("Scout is working…")).toBeNull()
    const answer = log.querySelector<HTMLElement>('[data-from="agent"]')
    expect(answer).toBeTruthy()
    expect(within(answer!).getByText("stalled")).toBeTruthy()
    expect(
      within(answer!).getByText(
        "Used Search web, Read sheet · Saved q3-report.pdf · Suggests Planner: Draft the rollout"
      )
    ).toBeTruthy()
    expect(within(log).getByText("Search web")).toBeTruthy()
    // Go has no file route for a specialist's conversation: named, not linked.
    expect(within(log).getByText("q3-report.pdf").closest("a")).toBeNull()
    expect(screen.getByText("Ready")).toBeTruthy()

    // A draft cut short before any words says only that.
    chat.update({
      status: "streaming",
      messages: transcript(asked, answered, { ...asked, id: "ev_4" }, marker),
    })
    chat.update({ status: "ready" })
    expect(within(log).getByText("Interrupted")).toBeTruthy()
  })

  it("answers a specialist's confirmation in the chat request's body", async () => {
    const confirm: Item = {
      type: "confirmation",
      id: "ev_c",
      call_id: "call_9",
      tool_call_id: "tool_9",
      author: "Scout",
      tool: "send_email",
      arguments: { to: "sam@example.com" },
      status: "pending",
    }
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      chats: { [scout.id]: transcript(asked, confirm) },
      conversations: {
        [scout.id]: [
          conversation({
            items: [asked, confirm],
            pending_confirmations: ["call_9"],
          }),
        ],
      },
    })
    renderFocus(backend)
    await chatLoaded()
    const card = screen.getByRole("group", { name: "Confirm Send email" })
    const socket = chatSocket()
    socket.open()
    socket.state({ threadId: "th_1" })
    const composer = screen.getByRole("textbox", { name: "Message Scout" })
    await waitFor(() => expect(composer.hasAttribute("disabled")).toBe(true))
    expect(
      screen.getByText("Answer the confirmation above to continue.")
    ).toBeTruthy()

    fireEvent.click(within(card).getByRole("button", { name: "Approve" }))
    expect(
      agentChats.named(scoutChat).sendMessage
    ).toHaveBeenCalledExactlyOnceWith(
      { text: " ", metadata: { oryxaConfirmation: true } },
      { body: { confirmations: [{ id: "call_9", confirmed: true }] } }
    )
    expect(within(card).getByText("Approved")).toBeTruthy()
  })

  it("merges what Go stored that the transcript lacks, readable, once", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      chats: { [scout.id]: transcript(asked) },
      conversations: { [scout.id]: [conversation()] },
    })
    renderFocus(backend)
    await chatLoaded()
    const socket = chatSocket()
    socket.open()
    socket.state({ threadId: "th_1" })
    const chat = agentChats.named(scoutChat)

    await waitFor(() => expect(chat.setMessages).toHaveBeenCalledOnce())
    const merged = chat.setMessages.mock.calls[0][0]
    expect(merged).toHaveLength(2)
    // Go's raw JSON answer arrives readable, never as JSON.
    expect(merged[1].parts[0]).toEqual({ type: "data-oryxa", data: answered })
    expect(await screen.findByText("stalled")).toBeTruthy()
    await waitFor(() => expect(conversationTitle()).toBe("Review the pipeline"))
    expect(chat.setMessages).toHaveBeenCalledOnce()
  })

  it("keeps a paused specialist's chat readable, with the composer off and why", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, quill],
      chats: {
        [quill.id]: transcript(asked, {
          type: "message",
          id: "ev_2",
          author: "Quill",
          text: "Done.",
        }),
      },
    })
    renderFocus(backend, quill.id)
    const column = await screen.findByRole("region", {
      name: "Chat with Quill",
    })
    await chatLoaded(1, `${ORG}~${quill.id}`)
    expect(within(column).getByText("Done.")).toBeTruthy()
    chatSocket(`${ORG}~${quill.id}`).open()

    const composer = within(column).getByRole("textbox", {
      name: "Message Quill",
    })
    expect(composer.hasAttribute("disabled")).toBe(true)
    expect(composer.getAttribute("placeholder")).toBe("Quill is paused")
    expect(
      within(column).getByText("Quill is paused. Resume it to chat with it.")
    ).toBeTruthy()
    expect(within(column).getByText("Paused")).toBeTruthy()
  })

  it("closes the composer when Go refuses a run because the specialist was paused elsewhere", async () => {
    const backend = fakeAgentsBackend({ agents: [primary, scout] })
    renderFocus(backend)
    await chatLoaded()
    const composer = screen.getByRole("textbox", { name: "Message Scout" })
    chatSocket().open()
    fireEvent.change(composer, { target: { value: "Hello" } })
    fireEvent.keyDown(composer, { key: "Enter" })
    const chat = agentChats.named(scoutChat)
    expect(chat.sendMessage).toHaveBeenCalledExactlyOnceWith({ text: "Hello" })

    backend.state.agents = backend.state.agents.map((entry) =>
      entry.id === scout.id ? { ...entry, status: "disabled" } : entry
    )
    const refused: Item = {
      type: "run_failed",
      error: "The specialist is paused. Turn it back on to continue.",
      code: "agent_disabled",
    }
    chat.update({
      messages: transcript(
        { type: "message", id: "m_1", author: "member", text: "Hello" },
        refused
      ),
    })
    chat.data(refused)
    chat.finish()

    expect(
      await screen.findByText(
        "The specialist is paused. Turn it back on to continue."
      )
    ).toBeTruthy()
    await waitFor(() => expect(composer.hasAttribute("disabled")).toBe(true))
    expect(
      screen.getByText("Scout is paused. Resume it to chat with it.")
    ).toBeTruthy()
  })

  it("sends once more, silently, after its session expired before a run", async () => {
    const backend = fakeAgentsBackend({ agents: [primary, scout] })
    renderFocus(backend)
    await chatLoaded()
    const composer = screen.getByRole("textbox", { name: "Message Scout" })
    const socket = chatSocket()
    socket.open()
    const chat = agentChats.named(scoutChat)
    const expired: Item = {
      type: "run_failed",
      error: "Your session has ended. Sign in again to continue.",
      code: "session_expired",
    }

    // A turn another view sent is that view's to resend.
    chat.data(expired)
    chat.finish()
    expect(chat.regenerate).not.toHaveBeenCalled()

    fireEvent.change(composer, { target: { value: "Hello" } })
    fireEvent.keyDown(composer, { key: "Enter" })
    chat.data(expired)
    chat.finish()
    await waitFor(() => expect(chat.regenerate).toHaveBeenCalledOnce())
    expect(chat.regenerate).toHaveBeenCalledWith({ body: undefined })
    expect(socket.reconnects).toBe(1)
    expect(backend.calls.some((call) => call.path === "/v1/auth/me")).toBe(true)
    expect(screen.queryByRole("alert")).toBeNull()

    // Expired again: the member is told, and nothing goes a third time.
    chat.data(expired)
    chat.finish()
    expect((await screen.findByRole("alert")).textContent).toContain(
      "Your session has ended."
    )
    expect(chat.regenerate).toHaveBeenCalledOnce()
  })
})

describe("Starting over", () => {
  it("clears the chat object for a new conversation, and leaves Go's copy", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      chats: { [scout.id]: transcript(asked, answered) },
      conversations: { [scout.id]: [conversation()] },
    })
    renderFocus(backend)
    await chatLoaded()
    const log = screen.getByRole("log", { name: "Conversation with Scout" })
    const socket = chatSocket()
    socket.open()
    socket.state({ threadId: "th_1" })
    // The row names the conversation from Go's copy.
    await waitFor(() => expect(conversationTitle()).toBe("Review the pipeline"))
    const chat = agentChats.named(scoutChat)
    // Go's raw answer matches the readable one the transcript holds.
    expect(chat.setMessages).not.toHaveBeenCalled()

    const startOver = screen.getByRole("button", { name: "New conversation" })
    expect(startOver.hasAttribute("disabled")).toBe(false)
    fireEvent.click(startOver)
    await waitFor(() =>
      expect(backend.writes()).toEqual([
        {
          method: "PUT",
          path: `/agents/specialist-chat-agent/${scoutChat}/conversation`,
          body: { thread_id: null },
        },
      ])
    )

    // The object sends every socket the empty chat and forgets the Go
    // conversation, so the next message starts a new one.
    chat.update({ messages: [] })
    socket.state({ threadId: null })
    expect(conversationTitle()).toBe("New conversation")
    expect(
      within(log).getByText("Ask anything to start a conversation.")
    ).toBeTruthy()
    expect(startOver.hasAttribute("disabled")).toBe(false)
    expect(backend.state.agents.map((entry) => entry.id)).toContain(scout.id)
  })

  it("says so when the chat could not be cleared", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      chats: { [scout.id]: transcript(asked, answered) },
    })
    renderFocus(backend)
    await chatLoaded()
    chatSocket().open()
    backend.failNext("PUT", /specialist-chat-agent/, {
      status: 503,
      code: "unavailable",
      message: "Your specialist's conversation is unavailable.",
    })
    fireEvent.click(screen.getByRole("button", { name: "New conversation" }))
    expect(
      await screen.findByText("Your specialist's conversation is unavailable.")
    ).toBeTruthy()
    expect(screen.getByText("Review the pipeline")).toBeTruthy()
  })
})

describe("A specialist's conversation is never cleared by itself", () => {
  let visibility: DocumentVisibilityState = "visible"

  beforeEach(() => {
    visibility = "visible"
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => visibility,
    })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  const cleared = (backend: FakeAgentsBackend) =>
    backend
      .writes()
      .filter(
        (call) =>
          call.method === "DELETE" &&
          call.path === `/agents/specialist-chat-agent/${scoutChat}`
      )

  /** The chat opens, and its object says which conversation it is on. */
  async function opened(backend: FakeAgentsBackend) {
    renderFocus(backend)
    await chatLoaded()
    const socket = chatSocket()
    socket.open()
    socket.state({ threadId: "th_1" })
    await act(async () => {})
    return socket
  }

  it("leaves a conversation Go last touched hours ago as it opens: it may hold the result the member came for", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      chats: { [scout.id]: transcript(asked, answered) },
      conversations: {
        [scout.id]: [
          conversation({
            updated_at: new Date(Date.now() - 5 * HOUR).toISOString(),
          }),
        ],
      },
    })
    await opened(backend)
    expect(cleared(backend)).toHaveLength(0)
    expect(screen.getByText("stalled")).toBeTruthy()
  })

  it("leaves it when the tab comes back hours later, and after a reconnect", async () => {
    vi.useFakeTimers({ toFake: ["Date"] })
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      chats: { [scout.id]: transcript(asked, answered) },
      conversations: { [scout.id]: [conversation()] },
    })
    const socket = await opened(backend)

    // The laptop sleeps for three hours: on waking the tab is visible and
    // the window focused while the socket reconnects.
    vi.setSystemTime(Date.now() + 3 * HOUR)
    socket.drop(1006)
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"))
      window.dispatchEvent(new Event("focus"))
    })
    await act(async () => {})
    socket.open()
    await act(async () => {})
    expect(cleared(backend)).toHaveLength(0)
    expect(screen.getByText("stalled")).toBeTruthy()
  })

  it("does not read a fast browser clock as an old conversation", async () => {
    // Go says the conversation was active a minute ago...
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      chats: { [scout.id]: transcript(asked, answered) },
      conversations: { [scout.id]: [conversation()] },
    })
    // ...but this browser's clock runs two hours ahead of Go's.
    vi.useFakeTimers({ toFake: ["Date"] })
    vi.setSystemTime(Date.now() + 2 * HOUR)
    await opened(backend)
    expect(cleared(backend)).toHaveLength(0)
  })

  it("keeps a message Go never stored, such as a refused send, in the transcript", async () => {
    const retry: Item = {
      type: "message",
      id: "local_1",
      author: "member",
      text: "Please try again with the Q3 numbers",
    }
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      // The object holds the member's newest message, refused before Go
      // stored it; Go's copy ends at the answer, two hours ago.
      chats: { [scout.id]: transcript(asked, answered, retry) },
      conversations: {
        [scout.id]: [
          conversation({
            updated_at: new Date(Date.now() - 2 * HOUR).toISOString(),
          }),
        ],
      },
    })
    await opened(backend)
    expect(cleared(backend)).toHaveLength(0)
    expect(
      screen.getByText("Please try again with the Q3 numbers")
    ).toBeTruthy()
  })
})

describe("The chat seam", () => {
  it("opens the agent panel on the Agents tab with the specialist a team's Chat button names", async () => {
    const crew = team({ id: "atm_crew", name: "Pipeline crew" }, [
      { agent: scout, role: "Research" },
    ])
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      teams: [crew],
    })
    const { container } = renderShell(backend, "multi-agents")
    fireEvent.click(
      await screen.findByRole("button", { name: "Open Pipeline crew" })
    )
    const shell = container.querySelector(".workspace-shell")
    expect(shell?.getAttribute("data-right")).toBe("collapsed")

    fireEvent.click(screen.getByRole("button", { name: "Members" }))
    fireEvent.click(screen.getByRole("button", { name: "Chat with Scout" }))
    expect(shell?.getAttribute("data-right")).toBe("expanded")
    const panel = screen.getByRole("complementary", { name: "Agent panel" })
    const agentsTab = within(panel).getByRole("tab", { name: "Agents" })
    expect(agentsTab.getAttribute("aria-selected")).toBe("true")
    expect(document.activeElement).toBe(agentsTab)
    expect(
      await within(panel).findByRole("button", { name: "Scout", pressed: true })
    ).toBeTruthy()
    expect(
      await within(panel).findByRole("textbox", { name: "Message Scout" })
    ).toBeTruthy()
    expect(roomSockets.named(scoutChat)).toHaveLength(1)

    // From another of the panel's tabs, the Agents tab comes forward again.
    fireEvent.click(within(panel).getByRole("tab", { name: "DMs" }))
    fireEvent.click(screen.getByRole("button", { name: "Chat with Scout" }))
    expect(
      within(panel)
        .getByRole("tab", { name: "Agents" })
        .getAttribute("aria-selected")
    ).toBe("true")
  })
})

describe("Deleting a specialist", () => {
  async function confirmDelete() {
    const panel = within(
      await screen.findByRole("region", { name: "Scout work panel" })
    )
    fireEvent.click(panel.getByRole("button", { name: "Delete…" }))
    fireEvent.click(panel.getByRole("button", { name: "Delete specialist" }))
  }

  it("clears its chat object first, then deletes it in Go", async () => {
    const onBack = vi.fn()
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      chats: { [scout.id]: transcript(asked, answered) },
    })
    vi.stubGlobal("fetch", vi.fn(backend.fetch))
    withQueries(
      <AgentsFocus
        target={{ kind: "specialist", agentId: scout.id }}
        organizationId={ORG}
        onBack={onBack}
        onOpen={vi.fn()}
      />
    )
    await confirmDelete()
    await waitFor(() => expect(onBack).toHaveBeenCalledOnce())
    expect(backend.writes()).toEqual([
      {
        method: "DELETE",
        path: `/agents/specialist-chat-agent/${scoutChat}`,
        body: undefined,
      },
      {
        method: "DELETE",
        path: `/v1/organizations/${ORG}/agents/${scout.id}`,
        body: undefined,
      },
    ])
  })

  it("keeps the specialist when its chat could not be cleared", async () => {
    const backend = fakeAgentsBackend({ agents: [primary, scout] })
    backend.failNext("DELETE", /specialist-chat-agent/, {
      status: 503,
      code: "unavailable",
      message: "Your specialist's conversation is unavailable.",
    })
    renderFocus(backend)
    await confirmDelete()
    expect(
      await screen.findByText("Could not delete it. Try again.")
    ).toBeTruthy()
    expect(backend.writes().map((call) => call.path)).toEqual([
      `/agents/specialist-chat-agent/${scoutChat}`,
    ])
    expect(backend.state.agents.map((entry) => entry.id)).toContain(scout.id)
  })

  it("says the chat started over when Go refuses once it was cleared", async () => {
    const backend = fakeAgentsBackend({ agents: [primary, scout] })
    backend.failNext("DELETE", /\/agents\/agt_scout$/, {
      status: 409,
      code: "agent_in_use",
      message:
        "agent belongs to an agent team: remove the specialist from its teams and teamspaces first",
    })
    renderFocus(backend)
    await confirmDelete()
    expect(
      await screen.findByText(
        "Scout is on a team or in a teamspace. Take it off first. Its chat has started over."
      )
    ).toBeTruthy()
  })

  it("won't clear anything for a specialist a teamspace still has", async () => {
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      teamspaces: [
        teamspace({
          contributions: [
            contribution({ agent_id: scout.id, owner_user_id: VIEWER }),
          ],
        }),
      ],
    })
    renderFocus(backend)
    const panel = within(
      await screen.findByRole("region", { name: "Scout work panel" })
    )
    fireEvent.click(panel.getByRole("button", { name: "Delete…" }))
    const confirm = await screen.findByRole("group", { name: "Delete Scout?" })
    expect(
      await within(confirm).findByText(
        "It's in Launch room. Take it out of that teamspace first."
      )
    ).toBeTruthy()
    expect(
      within(confirm)
        .getByRole("button", { name: "Delete specialist" })
        .hasAttribute("disabled")
    ).toBe(true)
    expect(backend.writes()).toEqual([])
  })
})

describe("specialist history and the default conversation", () => {
  const chatScope = { organizationId: ORG, userId: VIEWER }
  function renderOwn(backend: FakeAgentsBackend) {
    vi.stubGlobal("fetch", vi.fn(backend.fetch))
    return withQueries(
      <SpecialistChat
        organizationId={ORG}
        specialist={scout}
        scope={chatScope}
        layout="focus"
      />
    )
  }
  it("restores a recent history pick ahead of a different pending approval and keeps the draft", async () => {
    rememberChoice(chatScope, scout.id, "old")
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      conversations: {
        [scout.id]: [
          conversation({ id: "pending", pending_confirmations: ["approval"] }),
          conversation({
            id: "old",
            title: "Earlier work",
            updated_at: new Date(Date.now() - HOUR).toISOString(),
          }),
        ],
      },
    })
    renderOwn(backend)
    await chatLoaded()
    chatSocket().open()
    chatSocket().state({ threadId: "pending" })
    await waitFor(() =>
      expect(backend.writes()).toContainEqual({
        method: "PUT",
        path: `/agents/specialist-chat-agent/${scoutChat}/conversation`,
        body: { thread_id: "old" },
      })
    )
    chatSocket().state({ threadId: "old" })
    const composer = screen.getByRole("textbox", { name: "Message Scout" })
    await waitFor(() => expect(composer.hasAttribute("disabled")).toBe(false))
    fireEvent.change(composer, { target: { value: "Keep the draft" } })
    fireEvent.click(screen.getByRole("button", { name: "History" }))
    expect(await screen.findByText(/Needs approval/)).toBeTruthy()
    fireEvent.click(
      screen.getByRole("button", {
        name: /Review the pipeline.*Needs approval/,
      })
    )
    await waitFor(() =>
      expect(backend.writes()).toContainEqual({
        method: "PUT",
        path: `/agents/specialist-chat-agent/${scoutChat}/conversation`,
        body: { thread_id: "pending" },
      })
    )
    chatSocket().state({ threadId: "pending" })
    expect((composer as HTMLTextAreaElement).value).toBe("Keep the draft")
  })
  it("records a resumed explicit New at the member's latest real leave", async () => {
    vi.useFakeTimers({ toFake: ["Date"] })
    const now = Date.now()
    rememberChoice(chatScope, scout.id, null, now - 4 * 60_000)
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      conversations: {
        [scout.id]: [
          conversation({ id: "pending", pending_confirmations: ["approval"] }),
        ],
      },
    })
    const view = renderOwn(backend)
    await chatLoaded()
    chatSocket().open()
    chatSocket().state({ threadId: null })
    await waitFor(() =>
      expect(backend.writes()).toContainEqual({
        method: "PUT",
        path: `/agents/specialist-chat-agent/${scoutChat}/conversation`,
        body: { thread_id: null },
      })
    )
    await waitFor(() =>
      expect(rememberedView(chatScope, scout.id)?.leftAt).toBe(now)
    )
    vi.setSystemTime(now + 3 * 60_000)
    view.unmount()
    expect(rememberedView(chatScope, scout.id)?.leftAt).toBe(now + 3 * 60_000)
  })
  it("ignores a history pick at the five-minute boundary and opens the newest pending conversation", async () => {
    rememberChoice(chatScope, scout.id, "old", Date.now() - 5 * 60_000)
    const backend = fakeAgentsBackend({
      agents: [primary, scout],
      conversations: {
        [scout.id]: [
          conversation({ id: "pending", pending_confirmations: ["approval"] }),
          conversation({
            id: "old",
            updated_at: new Date(Date.now() - HOUR).toISOString(),
          }),
        ],
      },
    })
    renderOwn(backend)
    await chatLoaded()
    chatSocket().open()
    chatSocket().state({ threadId: "old" })
    await waitFor(() =>
      expect(backend.writes()).toContainEqual({
        method: "PUT",
        path: `/agents/specialist-chat-agent/${scoutChat}/conversation`,
        body: { thread_id: "pending" },
      })
    )
  })
})
