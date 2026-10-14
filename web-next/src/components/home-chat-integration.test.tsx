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
import type { ReactNode } from "react"
import { WorkspaceShell } from "./workspace-shell"
import { primary, request } from "./agents-fake-backend"
import type { Session } from "@/lib/auth/client"
import type { Item, Thread } from "@/lib/threads/client"
import {
  claimPrimaryChatRequest,
  publishPrimaryChatActivity,
  usePrimaryChatRequest,
} from "@/lib/threads/chat-choice"
import { itemsToChatMessages } from "@/lib/threads/chat-messages"
import { agentChats } from "@/test/agent-chat"
import { roomSockets } from "@/test/room-socket"
import { deferred, json, mockApi } from "@/test/teamspaces"

// Run Home, the shell, the public choice store and the real primary panel
// together. Only HTTP and the authenticated chat SDK's transport stand in.
vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
)
vi.mock(
  "@cloudflare/ai-chat/react",
  async () => (await import("@/test/agent-chat")).aiChatReact
)

const organization = { id: "org_home", name: "Home workspace", slug: "home" }
const session: Session = {
  user: {
    id: "user-member",
    email: "member@local.test",
    display_name: "Member",
  },
  organizations: [organization],
}
const thread = (id: string, title: string): Thread => ({
  id,
  title,
  origin: "chat",
  updated_at: new Date().toISOString(),
})
const recent = [
  thread("th_newer", "Newer conversation"),
  thread("th_old", "Exact older conversation"),
]

function backend({
  items = {},
  inbox = false,
}: {
  items?: Partial<Record<string, Item[]>>
  inbox?: boolean
} = {}) {
  let currentSession = session
  let lists = [...recent]
  let creates = 0
  let createGate: ReturnType<typeof deferred<Response>> | undefined
  let agentsGate: ReturnType<typeof deferred<void>> | undefined
  let refusedReads: ReturnType<typeof deferred<Response>> | undefined
  const api = mockApi(({ url, method }) => {
    const path = url.pathname
    if (path === "/v1/auth/me") return json(currentSession)
    if (path.endsWith("/get-messages")) return json([])
    const scoped = /^\/v1\/organizations\/([^/]+)\/(.+)$/.exec(path)
    if (!scoped) return undefined
    const [, org, resource] = scoped
    if (method === "POST" && resource === "threads") {
      creates += 1
      if (createGate) {
        const gate = createGate
        createGate = undefined
        return gate.promise
      }
      const created = thread(
        creates === 1 ? "th_created" : `th_created_${creates}`,
        "New Home conversation"
      )
      lists = [created, ...lists]
      return json(created, 201)
    }
    if (refusedReads && method === "GET") return refusedReads.promise
    const own =
      org === organization.id && currentSession.user.id === session.user.id
    if (resource === "threads") return json({ threads: own ? lists : [] })
    if (resource.startsWith("threads/")) {
      const id = resource.slice("threads/".length)
      const selected = lists.find((entry) => entry.id === id)
      return own && selected
        ? json({
            ...selected,
            items: items[id] ?? [],
            pending_confirmations: items[id]?.length ? ["tc_held"] : [],
          })
        : json({ error: { code: "forbidden", message: "Not available" } }, 403)
    }
    if (resource === "agents") {
      const response = () =>
        json({ agents: own ? [{ ...primary, organization_id: org }] : [] })
      return agentsGate ? agentsGate.promise.then(response) : response()
    }
    if (resource === "agent-requests/inbox")
      return json({
        requests:
          inbox && own
            ? [
                request({
                  id: "req_home",
                  status: "pending",
                  approval: "owner",
                  organization_id: org,
                }),
              ]
            : [],
      })
    if (resource === "apps/connections") return json({ connections: [] })
    if (resource === "agent-workflows") return json({ workflows: [] })
    if (resource === "corpora") return json({ corpora: [] })
    if (resource === "teamspaces") return json({ teamspaces: [] })
    if (resource === "members") return json({ members: [] })
    if (resource === "teams") return json({ teams: [] })
    return undefined
  })
  return {
    ...api,
    setSession(next: Session) {
      currentSession = next
    },
    holdCreate({ fail = false }: { fail?: boolean } = {}) {
      const gate = deferred<Response>()
      createGate = gate
      return () => {
        if (fail) {
          gate.resolve(
            json(
              { error: { code: "internal", message: "Couldn't start" } },
              500
            )
          )
          return
        }
        const created = thread("th_created", "New Home conversation")
        lists = [created, ...lists]
        gate.resolve(json(created, 201))
      }
    },
    holdAgents() {
      const gate = deferred<void>()
      agentsGate = gate
      return () => {
        agentsGate = undefined
        gate.resolve()
      }
    },
    holdRefusedReads() {
      refusedReads = deferred<Response>()
      return () => {
        refusedReads?.resolve(
          json({ error: { code: "forbidden", message: "Not available" } }, 403)
        )
        refusedReads = undefined
      }
    },
  }
}

function shellProps(nextSession: Session | undefined) {
  return {
    session: nextSession,
    selectedOrganization: nextSession?.organizations[0],
    role: "member" as const,
    organizationRole: "member" as const,
  }
}

function renderShell() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
  const view = render(<WorkspaceShell {...shellProps(session)} />, { wrapper })
  return { ...view, client }
}

const home = () => within(screen.getByRole("main", { name: "My workspace canvas" }))
const work = () => within(home().getByRole("region", { name: "Conversations" }))
function sendHome(text: string) {
  fireEvent.change(
    home().getByRole("textbox", { name: "Start a new conversation" }),
    { target: { value: text } }
  )
  fireEvent.click(home().getByRole("button", { name: "Send message" }))
}
const objectName = (id: string) => `${organization.id}~${id}`
async function socket(id: string) {
  await waitFor(() =>
    expect(roomSockets.named(objectName(id)).length).toBeGreaterThan(0)
  )
  return roomSockets.named(objectName(id)).at(-1)!
}

beforeEach(() => {
  const requestHook = renderHook(() => usePrimaryChatRequest())
  if (requestHook.result.current)
    claimPrimaryChatRequest(requestHook.result.current.id)
  requestHook.unmount()
  publishPrimaryChatActivity(null)
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
  vi.unstubAllGlobals()
  window.sessionStorage.clear()
})

describe("Home's public primary-chat integration", () => {
  it("creates one fresh conversation and sends once through its actual panel socket, then reopens the exact older thread without a POST", async () => {
    const api = backend()
    const release = api.holdCreate()
    const view = renderShell()
    await home().findByRole("button", { name: /Exact older conversation/ })
    sendHome("  Fresh request from Home  ")
    await waitFor(() =>
      expect(api.calls.filter((call) => call.method === "POST")).toHaveLength(1)
    )
    expect(await home().findByText("Starting conversation")).toBeTruthy()
    expect(work().getAllByRole("listitem")).toHaveLength(3)
    expect(
      work()
        .getByText("Starting conversation")
        .closest("li")
        ?.querySelector("button")
    ).toBeNull()
    expect(
      view.container
        .querySelector(".workspace-shell")
        ?.getAttribute("data-right")
    ).toBe("expanded")
    await act(async () => release())
    const created = await socket("th_created")
    const chat = agentChats.named(objectName("th_created"))
    expect(chat.sendMessage).not.toHaveBeenCalled()
    created.open()
    expect(chat.sendMessage).toHaveBeenCalledExactlyOnceWith(
      { text: "Fresh request from Home" },
      {
        body: {
          screen_context: expect.objectContaining({
            view: "home/overview",
          }),
        },
      }
    )
    created.open()
    view.rerender(<WorkspaceShell {...shellProps(session)} />)
    expect(chat.sendMessage).toHaveBeenCalledOnce()

    fireEvent.click(
      work().getByRole("button", { name: /Exact older conversation/ })
    )
    ;(await socket("th_old")).open()
    await waitFor(() =>
      expect(
        view.container.querySelector(".workspace-agent-thread-title")
          ?.textContent
      ).toBe("Exact older conversation")
    )
    expect(
      agentChats.named(objectName("th_old")).sendMessage
    ).not.toHaveBeenCalled()
    expect(api.calls.filter((call) => call.method === "POST")).toHaveLength(1)
    fireEvent.click(work().getByRole("button", { name: "New chat" }))
    expect(
      await screen.findByText("Ask anything to start a conversation.")
    ).toBeTruthy()
    expect(api.calls.filter((call) => call.method === "POST")).toHaveLength(1)
  })

  it("counts the selected confirmation conversation once alongside Inbox requests and opens its actual chat", async () => {
    const held: Item = {
      type: "confirmation",
      id: "ev_held",
      call_id: "tc_held",
      tool: "gmail_send_email",
      tool_call_id: "tool_held",
      arguments: {},
      status: "pending",
      hint: "Send this email?",
    }
    backend({ items: { th_old: [held] }, inbox: true })
    renderShell()
    fireEvent.click(
      await home().findByRole("button", { name: /Exact older conversation/ })
    )
    ;(await socket("th_old")).open()
    agentChats
      .named(objectName("th_old"))
      .update({ messages: itemsToChatMessages([held]) })
    await home().findByText("1 tool confirmation")
    const needs = within(home().getByRole("list", { name: "Needs you" }))
    // One teammate request and one waiting conversation, each once.
    expect(needs.getAllByRole("listitem")).toHaveLength(2)
    expect(needs.getAllByRole("button", { name: /^Decide on / })).toHaveLength(
      1
    )
    expect(needs.getByText(/Share the pipeline total\./)).toBeTruthy()
    const waiting = needs.getAllByText(/waiting in Exact older conversation/)
    expect(waiting).toHaveLength(1)
    expect(work().getAllByRole("listitem")).toHaveLength(2)
    expect(home().queryByText("Nothing needs you right now.")).toBeNull()
    fireEvent.click(work().getByRole("button", { name: "New chat" }))
    await screen.findByText("Ask anything to start a conversation.")
    fireEvent.click(
      within(waiting[0].closest("li")!).getByRole("button", { name: "Open" })
    )
    await waitFor(() =>
      expect(
        document.querySelector(".workspace-agent-thread-title")?.textContent
      ).toBe("Exact older conversation")
    )
    expect(
      agentChats.named(objectName("th_old")).sendMessage
    ).not.toHaveBeenCalled()
  })

  it("preserves a second Home message sent while a first new conversation is still being created", async () => {
    const api = backend()
    const release = api.holdCreate()
    renderShell()
    await home().findByRole("button", { name: /Exact older conversation/ })
    sendHome("First Home request")
    await waitFor(() =>
      expect(api.calls.filter((call) => call.method === "POST")).toHaveLength(1)
    )
    await home().findByText("Starting conversation")
    sendHome("Second Home request")
    await act(async () => release())
    await waitFor(() =>
      expect(
        roomSockets.all.filter((candidate) =>
          candidate.options.name?.startsWith(objectName("th_created"))
        ).length
      ).toBeGreaterThan(0)
    )
    for (const candidate of roomSockets.all)
      if (candidate.options.name?.startsWith(objectName("th_created")))
        candidate.open()
    // The consumer may keep or deliver the newer message; it must not
    // silently discard it when the older create response arrives.
    await waitFor(() => {
      const sent = agentChats.all.flatMap((chat) =>
        chat.sendMessage.mock.calls.map(
          ([message]) => (message as { text?: string } | undefined)?.text
        )
      )
      const drafts = screen
        .getAllByRole<HTMLTextAreaElement>("textbox")
        .map((box) => box.value)
      const pending = Array.from(
        document.querySelectorAll('[role="log"] [data-from="member"]')
      ).map((message) => message.textContent)
      expect([...sent, ...drafts, ...pending]).toContain("Second Home request")
    })
    // Every Home submission starts a new conversation: retaining/delivering
    // B cannot be implemented by adding it to A's pending thread.
    const firstThreadSent = agentChats.all
      .filter((chat) => chat.name === objectName("th_created"))
      .flatMap((chat) =>
        chat.sendMessage.mock.calls.map(
          ([message]) => (message as { text?: string } | undefined)?.text
        )
      )
    expect(firstThreadSent).not.toContain("Second Home request")
    const deliveredB = agentChats.all.filter((chat) =>
      chat.sendMessage.mock.calls.some(
        ([message]) =>
          (message as { text?: string } | undefined)?.text ===
          "Second Home request"
      )
    )
    for (const chat of deliveredB)
      expect(chat.name).toBe(objectName("th_created_2"))
  })

  it("delivers both Home submissions once into distinct new conversations after the member's agents finish loading", async () => {
    const api = backend()
    const release = api.holdAgents()
    renderShell()
    await home().findByRole("button", { name: /Exact older conversation/ })
    sendHome("First held-agents Home request")
    sendHome("Second held-agents Home request")
    expect(api.calls.filter((call) => call.method === "POST")).toHaveLength(0)

    await act(async () => release())
    ;(await socket("th_created")).open()
    const firstChat = agentChats.named(objectName("th_created"))
    expect(firstChat.sendMessage).toHaveBeenCalledExactlyOnceWith(
      { text: "First held-agents Home request" },
      {
        body: {
          screen_context: expect.objectContaining({ view: "home/overview" }),
        },
      }
    )
    ;(await socket("th_created_2")).open()
    const secondChat = agentChats.named(objectName("th_created_2"))
    expect(secondChat.sendMessage).toHaveBeenCalledExactlyOnceWith(
      { text: "Second held-agents Home request" },
      {
        body: {
          screen_context: expect.objectContaining({ view: "home/overview" }),
        },
      }
    )
    expect(api.calls.filter((call) => call.method === "POST")).toHaveLength(2)
    const sent = agentChats.all.flatMap((chat) =>
      chat.sendMessage.mock.calls.map(
        ([message]) => (message as { text?: string } | undefined)?.text
      )
    )
    expect(
      sent.filter((text) => text === "First held-agents Home request")
    ).toHaveLength(1)
    expect(
      sent.filter((text) => text === "Second held-agents Home request")
    ).toHaveLength(1)
  })

  it("keeps a failed first Home message recoverable beside an occupied primary draft after the next Home message sends", async () => {
    const api = backend()
    const release = api.holdCreate({ fail: true })
    renderShell()
    fireEvent.click(
      await home().findByRole("button", { name: /Exact older conversation/ })
    )
    ;(await socket("th_old")).open()
    fireEvent.change(
      await screen.findByRole("textbox", { name: "Message Leo" }),
      { target: { value: "Half a thought" } }
    )
    sendHome("Failed first Home request")
    await waitFor(() =>
      expect(api.calls.filter((call) => call.method === "POST")).toHaveLength(1)
    )
    sendHome("Following Home request")
    await act(async () => release())
    ;(await socket("th_created_2")).open()
    expect(
      agentChats.named(objectName("th_created_2")).sendMessage
    ).toHaveBeenCalledExactlyOnceWith(
      { text: "Following Home request" },
      {
        body: {
          screen_context: expect.objectContaining({ view: "home/overview" }),
        },
      }
    )
    expect(screen.getByDisplayValue("Half a thought")).toBeTruthy()
    // A failed submission must remain visible and recoverable independently
    // of the next request; it cannot replace the member's existing draft.
    expect(await screen.findByText("Failed first Home request")).toBeTruthy()
    expect(api.calls.filter((call) => call.method === "POST")).toHaveLength(2)
    expect(
      agentChats.all.flatMap((chat) =>
        chat.sendMessage.mock.calls.map(
          ([message]) => (message as { text?: string } | undefined)?.text
        )
      )
    ).not.toContain("Failed first Home request")
  })

  it.each([
    "viewer pending",
    "viewer refused",
    "organization",
    "logout",
  ] as const)(
    "clears selected chat activity and panel draft when %s changes",
    async (change) => {
      const api = backend()
      const view = renderShell()
      fireEvent.click(
        await home().findByRole("button", { name: /Exact older conversation/ })
      )
      ;(await socket("th_old")).open()
      const box = await screen.findByRole<HTMLTextAreaElement>("textbox", {
        name: "Message Leo",
      })
      fireEvent.change(box, {
        target: { value: "Member's private panel draft" },
      })
      agentChats.named(objectName("th_old")).update({ status: "streaming" })
      await home().findByText("Working")
      const before = roomSockets.named(objectName("th_old")).length
      const newerBefore = roomSockets.named(objectName("th_newer")).length
      const next =
        change === "logout"
          ? undefined
          : {
              ...session,
              ...(change.startsWith("viewer")
                ? { user: { ...session.user, id: "user-other" } }
                : {
                    organizations: [
                      {
                        id: "org_other",
                        name: "Other workspace",
                        slug: "other",
                      },
                    ],
                  }),
            }
      if (next) api.setSession(next)
      const refuse = api.holdRefusedReads()
      view.rerender(<WorkspaceShell {...shellProps(next)} />)
      expect(home().queryByText("Working")).toBeNull()
      expect(home().queryByText("Exact older conversation")).toBeNull()
      expect(
        screen.queryByDisplayValue("Member's private panel draft")
      ).toBeNull()
      await act(async () => {})
      if (change === "viewer refused") await act(async () => refuse())
      expect(roomSockets.named(objectName("th_old"))).toHaveLength(before)
      expect(roomSockets.named(objectName("th_newer"))).toHaveLength(
        newerBefore
      )
      expect(
        view.container.querySelector(".workspace-agent-thread-title")
          ?.textContent
      ).not.toBe("Newer conversation")
      await act(async () => refuse())
      expect(home().queryByText("Working")).toBeNull()
      expect(home().queryByText("Exact older conversation")).toBeNull()
      expect(
        screen.queryByDisplayValue("Member's private panel draft")
      ).toBeNull()
      expect(roomSockets.named(objectName("th_old"))).toHaveLength(before)
      expect(roomSockets.named(objectName("th_newer"))).toHaveLength(
        newerBefore
      )
    }
  )
})
