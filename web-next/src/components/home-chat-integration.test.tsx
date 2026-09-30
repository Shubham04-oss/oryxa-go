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
    if (resource === "agents")
      return json({ agents: own ? [{ ...primary, organization_id: org }] : [] })
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
    holdCreate() {
      const gate = deferred<Response>()
      createGate = gate
      return () => {
        const created = thread("th_created", "New Home conversation")
        lists = [created, ...lists]
        gate.resolve(json(created, 201))
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

const home = () => within(screen.getByRole("main", { name: "Today canvas" }))
const work = () => within(home().getByRole("region", { name: "Your agent" }))
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
    expect(home().getByText("Starting conversation")).toBeTruthy()
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

  it("shows the selected chat's held tool confirmation without adding it to the real Inbox count", async () => {
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
    await home().findByText("Needs confirmation")
    expect(
      home().getByRole("heading", { name: "Waiting on you · 1" })
    ).toBeTruthy()
    expect(work().getAllByRole("listitem")).toHaveLength(2)
    expect(home().queryByText(/Nothing waiting/)).toBeNull()
    expect(
      work().getAllByRole("button", { name: /Exact older conversation/ })
    ).toHaveLength(1)
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
