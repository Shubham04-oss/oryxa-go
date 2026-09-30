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

// Run the desk, the shell, the public choice store and the real primary chat
// in the center together. Only HTTP and the authenticated chat SDK's
// transport stand in.
vi.mock(
  "agents/react",
  async () => (await import("@/test/room-socket")).agentsReact
)
vi.mock(
  "@cloudflare/ai-chat/react",
  async () => (await import("@/test/agent-chat")).aiChatReact
)
vi.mock("thinking-orbs", () => ({
  ThinkingOrb: ({ state }: { state: string }) => (
    <i aria-hidden="true" data-orb-state={state} />
  ),
}))

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

const desk = () => within(screen.getByRole("main", { name: "Today canvas" }))
const recentList = () => within(screen.getByRole("region", { name: "Recent" }))
const title = () => screen.getByRole("heading", { level: 1 }).textContent
function sendFromDesk(text: string) {
  fireEvent.change(
    desk().getByRole("textbox", { name: "Start a new conversation" }),
    { target: { value: text } }
  )
  fireEvent.click(desk().getByRole("button", { name: "Send message" }))
}
const objectName = (id: string) => `${organization.id}~${id}`
async function socket(id: string) {
  await waitFor(() =>
    expect(roomSockets.named(objectName(id)).length, id).toBeGreaterThan(0)
  )
  return roomSockets.named(objectName(id)).at(-1)!
}
const posts = (api: { calls: Array<{ method: string }> }) =>
  api.calls.filter((call) => call.method === "POST")

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

describe("the desk and the primary chat in the center", () => {
  it("makes one conversation of a desk message, shows it in the center and sends once, then opens the exact older thread from Recent without a POST", async () => {
    const api = backend()
    const release = api.holdCreate()
    const view = renderShell()
    await desk().findByRole("region", { name: "Your agent" })
    await recentList().findByRole("button", {
      name: /^Exact older conversation/,
    })
    sendFromDesk("  Fresh request from the desk  ")
    await waitFor(() => expect(posts(api)).toHaveLength(1))

    // The center is the conversation being made, the message in it: never a
    // side panel, and nothing claims it is sent.
    expect(title()).toBe("New conversation")
    expect(
      screen.getByRole("log").querySelector('[data-from="member"]')?.textContent
    ).toBe("Fresh request from the desk")
    const box = screen.getByRole("textbox", { name: "Message Leo" })
    expect(box.hasAttribute("disabled")).toBe(true)
    expect(screen.getByText("Connecting…")).toBeTruthy()
    expect(view.container.querySelector(".workspace-right")).toBeNull()

    await act(async () => release())
    const created = await socket("th_created")
    const chat = agentChats.named(objectName("th_created"))
    expect(chat.sendMessage).not.toHaveBeenCalled()
    created.open()
    expect(chat.sendMessage).toHaveBeenCalledExactlyOnceWith(
      { text: "Fresh request from the desk" },
      {
        body: {
          screen_context: expect.objectContaining({ view: "home/overview" }),
        },
      }
    )
    created.open()
    view.rerender(<WorkspaceShell {...shellProps(session)} />)
    expect(chat.sendMessage).toHaveBeenCalledOnce()

    // Recent waits for the first message to settle in its conversation.
    const older = recentList().getByRole<HTMLButtonElement>("button", {
      name: /^Exact older conversation/,
    })
    expect(older.disabled).toBe(true)
    await waitFor(() => expect(older.disabled).toBe(false))
    fireEvent.click(older)
    ;(await socket("th_old")).open()
    await waitFor(() => expect(title()).toBe("Exact older conversation"))
    expect(
      agentChats.named(objectName("th_old")).sendMessage
    ).not.toHaveBeenCalled()
    expect(posts(api)).toHaveLength(1)

    fireEvent.click(screen.getByRole("button", { name: "New chat" }))
    expect(
      await desk().findByRole("region", { name: "Your agent" })
    ).toBeTruthy()
    expect(posts(api)).toHaveLength(1)
  })

  it("sends what the member types next into the conversation their desk message made", async () => {
    const api = backend()
    renderShell()
    await desk().findByRole("region", { name: "Your agent" })
    sendFromDesk("First question")
    ;(await socket("th_created")).open()
    const chat = agentChats.named(objectName("th_created"))
    await waitFor(() => expect(chat.sendMessage).toHaveBeenCalledOnce())

    const box = await screen.findByRole("textbox", { name: "Message Leo" })
    await waitFor(() => expect(box.hasAttribute("disabled")).toBe(false))
    fireEvent.change(box, { target: { value: "A follow-up" } })
    fireEvent.keyDown(box, { key: "Enter" })
    expect(chat.sendMessage).toHaveBeenLastCalledWith(
      { text: "A follow-up" },
      {
        body: {
          screen_context: expect.objectContaining({ view: "agents/chat" }),
        },
      }
    )
    expect(posts(api)).toHaveLength(1)
  })

  it("counts the waiting conversation once beside Inbox requests on the desk, and opens its actual chat in the center", async () => {
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
    // As the app opens, the conversation waiting on the member opens.
    ;(await socket("th_old")).open()
    agentChats
      .named(objectName("th_old"))
      .update({ messages: itemsToChatMessages([held]) })
    await waitFor(() => expect(title()).toBe("Exact older conversation"))
    expect(
      await screen.findByRole("group", { name: /Confirm Gmail send email/ })
    ).toBeTruthy()
    expect(
      recentList().getByRole("button", {
        name: /^Exact older conversation.*Needs approval/,
      })
    ).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "New chat" }))
    await desk().findByText("1 tool confirmation · Open chat to decide")
    const needs = within(desk().getByRole("region", { name: "Needs you" }))
    expect(needs.getAllByRole("article")).toHaveLength(1)
    expect(needs.getByText(/Quarterly numbers/)).toBeTruthy()
    expect(
      within(
        needs.getByRole("list", { name: "Tool confirmations" })
      ).getAllByRole("listitem")
    ).toHaveLength(1)
    expect(desk().queryByText("Nothing needs you right now.")).toBeNull()

    fireEvent.click(
      needs.getByRole("button", { name: /Exact older conversation/ })
    )
    await waitFor(() => expect(title()).toBe("Exact older conversation"))
    expect(screen.getByRole("region", { name: "Agent chat" })).toBeTruthy()
    expect(
      agentChats.named(objectName("th_old")).sendMessage
    ).not.toHaveBeenCalled()
  })

  it("keeps a desk message whose conversation couldn't be made on the desk, as not sent, and sends it again into a conversation of its own", async () => {
    const api = backend()
    const release = api.holdCreate({ fail: true })
    renderShell()
    await desk().findByRole("region", { name: "Your agent" })
    sendFromDesk("Failed first request")
    await waitFor(() => expect(posts(api)).toHaveLength(1))
    await act(async () => release())

    // Back on the desk: the words wait as not sent, the composer empty.
    const unsent = await desk().findByRole("log", {
      name: "Messages not sent",
    })
    expect(within(unsent).getByText("Failed first request")).toBeTruthy()
    expect(within(unsent).getByText(/Not sent\. Couldn't start/)).toBeTruthy()
    expect(
      desk().getByRole<HTMLTextAreaElement>("textbox", {
        name: "Start a new conversation",
      }).value
    ).toBe("")

    fireEvent.click(within(unsent).getByRole("button", { name: "Retry" }))
    ;(await socket("th_created_2")).open()
    expect(
      agentChats.named(objectName("th_created_2")).sendMessage
    ).toHaveBeenCalledExactlyOnceWith(
      { text: "Failed first request" },
      {
        body: {
          screen_context: expect.objectContaining({ view: "home/overview" }),
        },
      }
    )
    expect(posts(api)).toHaveLength(2)
    expect(
      screen.queryByText("Failed first request", { selector: "[data-status]" })
    ).toBeNull()
  })

  it("keeps one draft for the desk and the conversations, as the chat's one composer did", async () => {
    backend()
    renderShell()
    fireEvent.click(
      await recentList().findByRole("button", {
        name: /^Exact older conversation/,
      })
    )
    ;(await socket("th_old")).open()
    fireEvent.change(
      await screen.findByRole("textbox", { name: "Message Leo" }),
      { target: { value: "Half a thought" } }
    )
    fireEvent.click(screen.getByRole("button", { name: "New chat" }))
    expect(
      desk().getByRole<HTMLTextAreaElement>("textbox", {
        name: "Start a new conversation",
      }).value
    ).toBe("Half a thought")
  })

  it.each([
    "viewer pending",
    "viewer refused",
    "organization",
    "logout",
  ] as const)(
    "clears the open chat's activity and draft when %s changes",
    async (change) => {
      const api = backend()
      const view = renderShell()
      fireEvent.click(
        await recentList().findByRole("button", {
          name: /^Exact older conversation/,
        })
      )
      ;(await socket("th_old")).open()
      const box = await screen.findByRole<HTMLTextAreaElement>("textbox", {
        name: "Message Leo",
      })
      fireEvent.change(box, {
        target: { value: "Member's private draft" },
      })
      agentChats.named(objectName("th_old")).update({ status: "streaming" })
      await recentList().findByText("Working")
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
      expect(screen.queryByText("Working")).toBeNull()
      expect(screen.queryByText("Exact older conversation")).toBeNull()
      expect(screen.queryByDisplayValue("Member's private draft")).toBeNull()
      await act(async () => {})
      if (change === "viewer refused") await act(async () => refuse())
      expect(roomSockets.named(objectName("th_old"))).toHaveLength(before)
      expect(roomSockets.named(objectName("th_newer"))).toHaveLength(
        newerBefore
      )
      await act(async () => refuse())
      expect(screen.queryByText("Working")).toBeNull()
      expect(screen.queryByText("Exact older conversation")).toBeNull()
      expect(screen.queryByDisplayValue("Member's private draft")).toBeNull()
      expect(roomSockets.named(objectName("th_old"))).toHaveLength(before)
      expect(roomSockets.named(objectName("th_newer"))).toHaveLength(
        newerBefore
      )
    }
  )
})
