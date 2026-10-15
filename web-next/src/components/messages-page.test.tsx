// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { DmPeople, DmPerson } from "./direct-message-people"
import { MessagesPage, unreadDirectMessages } from "./messages-page"
import type { Conversation, Inbox } from "@/lib/dms"
import { OpenRequestProvider } from "@/lib/dms/request-navigation"
import { json, mockApi } from "@/test/teamspaces"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const viewerId = "user-member"

function person(
  id: string,
  name: string,
  extra: Partial<DmPerson> = {}
): DmPerson {
  return {
    id,
    name,
    email: `${id}@sim.oryxa`,
    role: "member",
    spaces: [],
    agents: [],
    ...extra,
  }
}

function peopleOf(list: DmPerson[]): DmPeople {
  const byId = new Map(list.map((entry) => [entry.id, entry]))
  return {
    status: "ready",
    people: list,
    find: (id) => byId.get(id),
    nameOf: (id) => byId.get(id)?.name ?? "Former member",
    peer: (id) => {
      const known = byId.get(id)
      return known
        ? { id, name: known.name, short: known.name.split(" ")[0] }
        : { id, name: "Former member", short: "this person" }
    },
    refetch: vi.fn(),
  }
}

function conversation(
  peerId: string,
  unread: number,
  body: string,
  kind: Conversation["kind"] = "dm"
): Conversation {
  return {
    kind,
    name: `${kind}:${peerId}`,
    peerId,
    unread,
    updatedAt: new Date().toISOString(),
    lastMessage: {
      authorId: peerId,
      body,
      createdAt: new Date().toISOString(),
    },
  }
}

const priya = person("user-priya", "Priya Shah", {
  role: "admin",
  spaces: ["EU launch", "Sales"],
  agents: [{ id: "agent-nia", name: "Nia", teamspaceId: "space-eu" }],
})
const rohan = person("user-rohan", "Rohan Mehta")
const kenji = person("user-kenji", "Kenji Sato")
const people = peopleOf([priya, rohan, kenji])

const inbox: Inbox = {
  status: "live",
  unreadTotal: 6,
  conversations: [
    conversation(priya.id, 2, "Can you check the risk section?"),
    conversation(rohan.id, 1, "Retry fix is merged"),
    // A side conversation stays in the agent panel, with its teamspace.
    conversation(kenji.id, 3, "About the launch", "side"),
  ],
}

function renderPage(onOpen = vi.fn(), openRequest = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <OpenRequestProvider value={openRequest}>
        <MessagesPage
          organizationId="org-1"
          viewerId={viewerId}
          inbox={inbox}
          people={people}
          peer={{ id: priya.id, name: priya.name, short: "Priya" }}
          onOpen={onOpen}
        >
          <p>The conversation</p>
        </MessagesPage>
      </OpenRequestProvider>
    </QueryClientProvider>
  )
  return onOpen
}

describe("Messages", () => {
  it("counts unread DMs only, leaving side conversations to the panel", () => {
    expect(unreadDirectMessages(inbox)).toBe(3)
  })

  it("lists the DMs beside the open one, which is current", () => {
    const onOpen = renderPage()
    expect(screen.getByRole("heading", { name: /^Messages/ }).textContent).toBe(
      "Messages · 3 unread"
    )
    const list = screen.getByRole("list", { name: "Direct messages" })
    const rows = within(list).getAllByRole("button")
    expect(rows.map((row) => row.getAttribute("aria-label"))).toEqual([
      "Open conversation with Priya Shah, 2 unread",
      "Open conversation with Rohan Mehta, 1 unread",
    ])
    expect(rows[0].getAttribute("aria-current")).toBe("page")
    expect(rows[1].getAttribute("aria-current")).toBeNull()
    fireEvent.click(rows[1])
    expect(onOpen).toHaveBeenCalledWith(rohan.id)
    expect(screen.getByText("The conversation")).toBeTruthy()
  })

  it("finds a person, and starts a conversation with someone new", () => {
    const onOpen = renderPage()
    const search = screen.getByRole("searchbox", { name: "Find a person" })
    fireEvent.change(search, { target: { value: "ken" } })
    expect(screen.queryByRole("list", { name: "Direct messages" })).toBeNull()
    const others = screen.getByRole("list", { name: "People" })
    fireEvent.click(
      within(others).getByRole("button", { name: "Message Kenji Sato" })
    )
    expect(onOpen).toHaveBeenCalledWith(kenji.id)

    fireEvent.change(search, { target: { value: "nobody" } })
    expect(screen.getByText("No one matches your search.")).toBeTruthy()
  })

  it("New message moves to the search", () => {
    renderPage()
    fireEvent.click(screen.getByRole("button", { name: "New message" }))
    expect(document.activeElement).toBe(
      screen.getByRole("searchbox", { name: "Find a person" })
    )
  })

  it("says who the person is: role, their agent and shared teamspaces", () => {
    renderPage()
    const card = screen.getByRole("complementary", { name: "About Priya Shah" })
    expect(
      within(card).getByRole("heading", { name: "Priya Shah" })
    ).toBeTruthy()
    expect(within(card).getByText("Admin")).toBeTruthy()
    const agent = within(card).getByRole("region", { name: "Priya's agent" })
    expect(within(agent).getByText("Nia")).toBeTruthy()
    expect(within(agent).getByRole("button", { name: "Ask Nia" })).toBeTruthy()
    const spaces = within(card).getByRole("region", {
      name: "Shared teamspaces",
    })
    expect(within(spaces).getByText("EU launch")).toBeTruthy()
    expect(within(spaces).getByText("Sales")).toBeTruthy()
    // Nothing the API can't say: no local time, profile or shared files.
    expect(within(card).queryByText(/Shared files/)).toBeNull()
    expect(within(card).queryByRole("button", { name: "Profile" })).toBeNull()
  })

  it("asks their agent: files a request the owner approves, and opens it in Requests", async () => {
    const { calls } = mockApi(({ method, url }) =>
      method === "POST" &&
      url.pathname === "/v1/organizations/org-1/agent-requests"
        ? json({ id: "irq_9" }, 201)
        : undefined
    )
    const openRequest = vi.fn()
    renderPage(vi.fn(), openRequest)
    fireEvent.click(screen.getByRole("button", { name: "Ask Nia" }))
    const dialog = await screen.findByRole("dialog", { name: "Ask Nia" })
    expect(
      within(dialog).getByText(
        /Priya approves your question before Nia answers/
      )
    ).toBeTruthy()
    const send = within(dialog).getByRole("button", { name: "Send to Nia" })
    expect(send.hasAttribute("disabled")).toBe(true)
    fireEvent.change(
      within(dialog).getByRole("textbox", { name: "Your question" }),
      {
        target: { value: "Is VAT included?" },
      }
    )
    fireEvent.click(send)
    expect(await within(dialog).findByText(/^Sent\./)).toBeTruthy()
    expect(calls).toEqual([
      {
        method: "POST",
        path: "/v1/organizations/org-1/agent-requests",
        body: {
          target_agent_id: "agent-nia",
          purpose: "A teammate asked Nia directly in Oryxa.",
          task: "Is VAT included?",
        },
      },
    ])
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Open in Requests" })
    )
    expect(openRequest).toHaveBeenCalledWith("irq_9")
  })

  it("says so when the request is refused, keeping the question", async () => {
    mockApi(() => json({ error: { code: "forbidden", message: "no" } }, 403))
    renderPage()
    fireEvent.click(screen.getByRole("button", { name: "Ask Nia" }))
    const dialog = await screen.findByRole("dialog", { name: "Ask Nia" })
    const field = within(dialog).getByRole("textbox", { name: "Your question" })
    fireEvent.change(field, { target: { value: "Is VAT included?" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Send to Nia" }))
    expect(
      await within(dialog).findByText("You can't ask Priya's agent from here.")
    ).toBeTruthy()
    expect((field as HTMLTextAreaElement).value).toBe("Is VAT included?")
  })
})
