// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render as renderBase,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { useState } from "react"
import type { AvatarIdentity } from "./workspace-avatar"
import type * as AvatarModule from "./workspace-avatar"
import { TeamspacesManagement } from "./teamspaces-management"
import type { Teamspace } from "@/lib/teamspaces/client"
import {
  adaWhisperer,
  apiError,
  contribution,
  deferred,
  entry,
  json,
  memberWhisperer,
  mockApi,
  orgPath,
  teamspace,
  viewerId,
  whisperer,
} from "@/test/teamspaces"

// The avatar art needs a canvas jsdom lacks; what matters here is which art
// each identity asks for (Lorelei for people, voxel for agents) and its seed.
vi.mock("./workspace-avatar", async (importOriginal) => {
  const actual = await importOriginal<typeof AvatarModule>()
  return {
    ...actual,
    IdentityAvatar: ({ identity }: { identity: AvatarIdentity }) => (
      <span
        data-testid="identity-avatar"
        data-kind={identity.kind}
        data-seed={identity.id}
      >
        {identity.name}
      </span>
    ),
  }
})

const launch = teamspace({
  updated_at: new Date(Date.now() - 5 * 60_000).toISOString(),
})
const design = teamspace({
  id: "tsp_design",
  team_id: "team_design",
  team_name: "Design",
  name: "Design reviews",
  description: "A quieter space for feedback.",
  status: "disabled",
  can_manage: false,
  created_by_user_id: "user-ada",
  whisperers: [
    adaWhisperer,
    memberWhisperer,
    whisperer("user-sam", "Sam", "agt_sam", "Orbit"),
  ],
})

const teams = [
  {
    id: "team_launch",
    name: "Launch",
    organization_id: "org_sim",
    role: "admin",
    can_manage: true,
  },
  {
    id: "team_design",
    name: "Design",
    organization_id: "org_sim",
    role: "member",
    can_manage: false,
  },
  {
    id: "team_research",
    name: "Research",
    organization_id: "org_sim",
    role: "admin",
    can_manage: true,
  },
]

function render(onOpen = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  function Harness() {
    const [selectedId, setSelectedId] = useState<string | null>(null)
    return (
      <TeamspacesManagement
        organizationId="org_sim"
        viewerId={viewerId}
        selectedId={selectedId}
        onSelect={setSelectedId}
        onOpen={onOpen}
      />
    )
  }
  return {
    ...renderBase(
      <QueryClientProvider client={client}>
        <Harness />
      </QueryClientProvider>
    ),
    client,
    onOpen,
  }
}

/** The member's teamspaces and teams; `extra` answers anything else. */
function backend(
  spaces: Teamspace[] = [launch, design],
  extra: Parameters<typeof mockApi>[0] = () => undefined
) {
  const state = { spaces: [...spaces] }
  const api = mockApi(async (call) => {
    const answer = await extra(call)
    if (answer) return answer
    if (call.method === "GET" && call.path === `${orgPath}/teamspaces`) {
      return json({ teamspaces: state.spaces })
    }
    if (call.method === "GET" && call.path === `${orgPath}/teams`) {
      return json({ teams })
    }
    if (call.method === "GET" && call.path.endsWith("/entries?limit=100")) {
      return json({ entries: [] })
    }
    if (call.method === "GET" && call.path === `${orgPath}/members`) {
      return json({
        members: [
          {
            user_id: "user-ada",
            email: "ada@sim.oryxa",
            display_name: "Ada",
            role: "member",
          },
        ],
      })
    }
    return undefined
  })
  return { ...api, state }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

/** Opens a card's More menu and picks `item`. */
async function fromMenu(name: string, item: string) {
  fireEvent.click(
    await screen.findByRole("button", { name: `More for ${name}` })
  )
  fireEvent.click(await screen.findByRole("menuitem", { name: item }))
}

/** Opens a teamspace's management detail from its card. */
async function manage(name: string) {
  await fromMenu(name, "Manage details")
}

/**
 * The header's New teamspace button (the last card has one too). The open
 * pop-up hides the page behind it from assistive tech.
 */
function newTeamspaceButton() {
  return screen.getAllByRole("button", {
    name: "New teamspace",
    hidden: true,
  })[0]
}

describe("TeamspacesManagement list", () => {
  it("shows each real teamspace as a card: team, people, purpose and faces", async () => {
    backend()
    render()

    const launchCard = await screen.findByRole("article", {
      name: "Launch room",
    })
    expect(within(launchCard).getByText("Launch team · 2 people")).toBeTruthy()
    expect(
      within(launchCard).getByText(
        "Plans, owners and updates for the next release."
      )
    ).toBeTruthy()
    expect(
      within(launchCard).getByRole("img", { name: "Member, Ada" })
    ).toBeTruthy()

    // A paused card says so, and that its room is read-only.
    const designCard = screen.getByRole("article", { name: "Design reviews" })
    expect(within(designCard).getByText("Design team · paused")).toBeTruthy()
    expect(
      within(designCard).getByText(
        "Paused. Its room is read-only until a team admin resumes it."
      )
    ).toBeTruthy()
    expect(
      within(designCard).getByRole("img", { name: "Ada, Member, Sam" })
    ).toBeTruthy()

    expect(screen.getByLabelText("Teamspace summary").textContent).toBe(
      "1 active."
    )
    // The shell's header names the page; the canvas doesn't say it again.
    expect(screen.queryByRole("heading", { name: "Teamspaces" })).toBeNull()
    // Nothing from the board's example data.
    for (const example of ["Northwind", "Krude:", "Paused by", "editing"]) {
      expect(screen.queryByText(new RegExp(example))).toBeNull()
    }
  })

  it("says on a paused card who paused it and when, once Go says so", async () => {
    const pausedAt = "2026-10-16T09:12:44Z"
    const on = new Intl.DateTimeFormat(undefined, {
      day: "numeric",
      month: "short",
      year: "numeric",
    }).format(Date.parse(pausedAt))
    backend([
      { ...design, paused_by_user_id: "user-sam", paused_at: pausedAt },
      {
        ...design,
        id: "tsp_mine",
        name: "Mine",
        paused_by_user_id: viewerId,
        paused_at: pausedAt,
      },
      {
        ...design,
        id: "tsp_unknown",
        name: "Unknown",
        paused_by_user_id: "user-gone",
        paused_at: pausedAt,
      },
      // Paused before Go kept who and when.
      {
        ...design,
        id: "tsp_old",
        name: "Old",
        paused_by_user_id: null,
        paused_at: null,
      },
    ])
    render()

    const card = (name: string) => screen.findByRole("article", { name })
    expect(
      within(await card("Design reviews")).getByText(`Paused by Sam on ${on}`)
    ).toBeTruthy()
    expect(
      within(await card("Mine")).getByText(`Paused by you on ${on}`)
    ).toBeTruthy()
    expect(
      within(await card("Unknown")).getByText(`Paused on ${on}`)
    ).toBeTruthy()
    expect(within(await card("Old")).getByText("Paused")).toBeTruthy()
    expect(
      within(await card("Old")).queryByText(/Paused by|Paused on/)
    ).toBeNull()
  })

  it("shows four faces, then how many more", async () => {
    backend([
      teamspace({
        whisperers: ["Ana", "Ben", "Cy", "Dee", "Eve", "Fay"].map(
          (name, index) =>
            whisperer(`user-${index}`, name, `agt_${index}`, name)
        ),
      }),
    ])
    render()
    expect(
      await screen.findByRole("img", { name: "Ana, Ben, Cy, Dee, 2 more" })
    ).toBeTruthy()
    expect(screen.getByText("+2")).toBeTruthy()
  })

  it("carries the target and the checklist's progress as fact chips, once Go serves them", async () => {
    const year = new Date().getFullYear()
    backend([
      teamspace({
        target_date: `${year}-11-04`,
        target_label: "Launch",
        task_counts: { done: 6, total: 9 },
      }),
      design,
    ])
    render()

    const launchCard = await screen.findByRole("article", {
      name: "Launch room",
    })
    const day = new Intl.DateTimeFormat(undefined, {
      day: "numeric",
      month: "short",
    }).format(new Date(year, 10, 4))
    expect(within(launchCard).getByText(`Launch ${day}`)).toBeTruthy()
    expect(
      within(launchCard).getByLabelText("6 of 9 tasks done").textContent
    ).toBe("6/9")
    // Without a target or tasks (or from a Go without them), no chips.
    const designCard = screen.getByRole("article", { name: "Design reviews" })
    expect(within(designCard).queryByText(/\d+\/\d+/)).toBeNull()
    expect(within(designCard).queryByLabelText(/tasks done/)).toBeNull()
  })

  it("opens the teamspace when its card is clicked, not its management", async () => {
    backend()
    const { onOpen } = render()

    fireEvent.click(
      await screen.findByRole("button", { name: "Open Launch room" })
    )
    expect(onOpen).toHaveBeenCalledWith("tsp_launch")
    expect(
      screen.queryByRole("region", { name: "Teamspace details" })
    ).toBeNull()

    await fromMenu("Design reviews", "Open teamspace")
    expect(onOpen).toHaveBeenLastCalledWith("tsp_design")
  })

  it("offers pause and delete in the More menu only to who manages it", async () => {
    backend()
    render()

    fireEvent.click(
      await screen.findByRole("button", { name: "More for Launch room" })
    )
    expect(
      (await screen.findAllByRole("menuitem")).map((item) => item.textContent)
    ).toEqual([
      "Open teamspace",
      "Manage details",
      "Pause teamspace",
      "Delete teamspace",
    ])
    fireEvent.keyDown(screen.getAllByRole("menuitem")[0], { key: "Escape" })
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull())

    fireEvent.click(
      screen.getByRole("button", { name: "More for Design reviews" })
    )
    expect(
      (await screen.findAllByRole("menuitem")).map((item) => item.textContent)
    ).toEqual(["Open teamspace", "Manage details"])
  })

  it("pauses or deletes from the menu only through the detail's confirm", async () => {
    const api = backend()
    render()

    await fromMenu("Launch room", "Pause teamspace")
    expect(screen.getByRole("group", { name: "Confirm pausing" })).toBeTruthy()
    expect(api.calls.some((call) => call.method === "PATCH")).toBe(false)

    cleanup()
    const again = backend()
    render()
    await fromMenu("Launch room", "Delete teamspace")
    expect(screen.getByRole("group", { name: "Confirm deletion" })).toBeTruthy()
    expect(again.calls.some((call) => call.method === "DELETE")).toBe(false)
  })

  it("resumes a paused teamspace straight from its menu", async () => {
    const paused = { ...launch, status: "disabled" as const }
    const api = backend([paused], ({ method }) => {
      if (method !== "PATCH") return undefined
      const next = { ...launch, revision: 2 }
      api.state.spaces = [next]
      return json(next)
    })
    render()

    expect(await screen.findByText("Launch team · paused")).toBeTruthy()
    await fromMenu("Launch room", "Resume teamspace")
    expect(await screen.findByText("Launch team · 2 people")).toBeTruthy()
    expect(
      api.calls
        .filter((call) => call.method === "PATCH")
        .map((call) => [call.path, call.body])
    ).toEqual([[`${orgPath}/teamspaces/tsp_launch`, { status: "active" }]])
  })

  it("shows a live parley on its card and in the subtitle", async () => {
    const startedAt = new Date(Date.now() - 18 * 60_000).toISOString()
    const api = backend([launch, design], ({ path }) =>
      path === "/live/org_sim/rooms/teamspace/tsp_launch/call"
        ? json({
            call: {
              callId: "call_1",
              startedAt,
              startedBy: viewerId,
              participants: [],
              recording: { on: false, by: null, since: null },
              agent: { present: false, name: "Krude" },
            },
          })
        : undefined
    )
    render()

    const launchCard = await screen.findByRole("article", {
      name: "Launch room",
    })
    expect(await within(launchCard).findByText("Parley · 18 min")).toBeTruthy()
    expect(screen.getByLabelText("Teamspace summary").textContent).toBe(
      "1 active, and a parley is live in Launch room."
    )
    // A paused teamspace has no parley to ask after.
    expect(
      api.calls.some((call) => call.path.includes("/teamspace/tsp_design/"))
    ).toBe(false)
  })

  it("filters by status, with counts, and searches names, descriptions and teams", async () => {
    backend()
    render()
    await screen.findByRole("button", { name: "Open Launch room" })
    expect(
      within(screen.getByRole("group", { name: "Teamspace status filter" }))
        .getAllByRole("button")
        .map((button) => button.textContent)
    ).toEqual(["All 2", "Active 1", "Paused 1"])

    fireEvent.click(screen.getByRole("button", { name: "Paused 1" }))
    expect(
      screen.getByRole("button", { name: "Open Design reviews" })
    ).toBeTruthy()
    expect(
      screen.queryByRole("button", { name: "Open Launch room" })
    ).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Active 1" }))
    expect(
      screen.getByRole("button", { name: "Open Launch room" })
    ).toBeTruthy()
    expect(
      screen.queryByRole("button", { name: "Open Design reviews" })
    ).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "All 2" }))
    const search = screen.getByRole("searchbox", { name: "Search teamspaces" })
    fireEvent.change(search, { target: { value: "design" } })
    expect(
      screen.getByRole("button", { name: "Open Design reviews" })
    ).toBeTruthy()
    expect(
      screen.queryByRole("button", { name: "Open Launch room" })
    ).toBeNull()
    fireEvent.change(search, { target: { value: "next release" } })
    expect(
      screen.getByRole("button", { name: "Open Launch room" })
    ).toBeTruthy()
    fireEvent.change(search, { target: { value: "nothing like this" } })
    expect(screen.getByText("No teamspaces found")).toBeTruthy()
  })

  it("jumps to the search on /, but not while typing elsewhere", async () => {
    backend()
    render()
    await screen.findByRole("button", { name: "Open Launch room" })
    const search = screen.getByRole("searchbox", { name: "Search teamspaces" })

    fireEvent.keyDown(document.body, { key: "/" })
    expect(document.activeElement).toBe(search)

    search.blur()
    const elsewhere = document.createElement("textarea")
    document.body.appendChild(elsewhere)
    elsewhere.focus()
    fireEvent.keyDown(elsewhere, { key: "/" })
    expect(document.activeElement).toBe(elsewhere)
    elsewhere.remove()
  })

  it("shows loading, a retryable failure, and a server without teamspaces", async () => {
    let fail = true
    const api = mockApi(({ path }) => {
      if (path === `${orgPath}/teams`) return json({ teams: [] })
      return fail
        ? apiError(500, "internal", "internal server error")
        : json({ teamspaces: [launch] })
    })
    render()

    expect(screen.getByText("Loading teamspaces…")).toBeTruthy()
    expect(screen.getByLabelText("Teamspace summary").textContent).toBe(
      "Loading…"
    )
    expect(await screen.findByText("Couldn't load teamspaces")).toBeTruthy()
    expect(screen.queryByLabelText("Teamspace summary")).toBeNull()
    fail = false
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(
      await screen.findByRole("button", { name: "Open Launch room" })
    ).toBeTruthy()
    expect(
      api.calls.filter((call) => call.path === `${orgPath}/teamspaces`)
    ).toHaveLength(2)

    cleanup()
    mockApi(() => apiError(503, "teamspaces_unavailable", "not configured"))
    render()
    expect(
      await screen.findByText("Teamspaces aren't set up on this server")
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull()
    expect(screen.queryByLabelText("Teamspace summary")).toBeNull()
  })

  it("says what to do when there are no teamspaces", async () => {
    mockApi(({ path }) =>
      path === `${orgPath}/teams`
        ? json({ teams: [teams[1]] })
        : json({ teamspaces: [] })
    )
    render()

    expect(await screen.findByText("No teamspaces yet")).toBeTruthy()
    await waitFor(() =>
      expect(
        screen.getByText(
          "A team admin can open a teamspace for a team you're on."
        )
      ).toBeTruthy()
    )
    expect(screen.queryByRole("button", { name: "New teamspace" })).toBeNull()
  })

  it("needs a workspace before it can list anything", () => {
    const api = mockApi(() => undefined)
    renderBase(
      <QueryClientProvider client={new QueryClient()}>
        <TeamspacesManagement
          selectedId={null}
          onSelect={vi.fn()}
          onOpen={vi.fn()}
        />
      </QueryClientProvider>
    )
    expect(screen.getByText("No workspace to load")).toBeTruthy()
    expect(api.calls).toEqual([])
  })
})

describe("creating a teamspace", () => {
  it("is offered only to someone who manages a team without one", async () => {
    mockApi(({ path }) =>
      path === `${orgPath}/teams`
        ? json({ teams: [teams[1]] })
        : json({ teamspaces: [launch] })
    )
    render()
    await screen.findByRole("button", { name: "Open Launch room" })
    expect(screen.queryByRole("button", { name: "New teamspace" })).toBeNull()
  })

  it("opens the form as a pop-up, from the header or the grid's last card", async () => {
    backend([launch, design])
    render()
    await screen.findByRole("button", { name: "Open Launch room" })

    const card = screen.getByText("Open a new teamspace").parentElement
    if (!card) throw new Error("no new-teamspace card")
    fireEvent.click(within(card).getByRole("button", { name: "New teamspace" }))
    const dialog = await screen.findByRole("dialog", { name: "New teamspace" })
    expect(within(dialog).getByRole("combobox", { name: "Team" })).toBeTruthy()
    expect(newTeamspaceButton().getAttribute("aria-expanded")).toBe("true")

    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())

    fireEvent.click(newTeamspaceButton())
    expect(
      await screen.findByRole("dialog", { name: "New teamspace" })
    ).toBeTruthy()
  })

  it("posts the fields Go takes, for a team the member manages, and opens the result", async () => {
    const created = teamspace({
      id: "tsp_research",
      team_id: "team_research",
      team_name: "Research",
      name: "Research room",
      description: "Sources and findings.",
      whisperers: [memberWhisperer],
    })
    const answer = deferred<Response>()
    const api = backend([launch, design], ({ method, path }) =>
      method === "POST" && path === `${orgPath}/teamspaces`
        ? answer.promise
        : undefined
    )
    render()
    await screen.findByRole("button", { name: "Open Launch room" })

    await screen.findByRole("button", { name: "Open Launch room" })
    fireEvent.click(newTeamspaceButton())
    await screen.findByRole("dialog", { name: "New teamspace" })
    // Launch already has its one teamspace and Design isn't the member's to
    // manage, so Research is the only choice.
    const team = screen.getByRole("combobox", { name: "Team" })
    expect(
      within(team)
        .getAllByRole("option")
        .map((option) => option.textContent)
    ).toEqual(["Research"])

    fireEvent.click(screen.getByRole("button", { name: "Create teamspace" }))
    expect(screen.getByText("Give the teamspace a name.")).toBeTruthy()
    expect(api.calls.some((call) => call.method === "POST")).toBe(false)

    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
      target: { value: "  Research    room " },
    })
    fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
      target: { value: " Sources and findings. " },
    })
    fireEvent.click(screen.getByRole("button", { name: "Create teamspace" }))

    expect(
      screen.getByRole("button", { name: "Creating…" }).hasAttribute("disabled")
    ).toBe(true)
    await waitFor(() =>
      expect(
        api.calls.filter(
          (call) =>
            call.method === "POST" && call.path === `${orgPath}/teamspaces`
        )
      ).toEqual([
        {
          method: "POST",
          path: `${orgPath}/teamspaces`,
          body: {
            team_id: "team_research",
            name: "Research room",
            description: "Sources and findings.",
          },
        },
      ])
    )

    api.state.spaces.push(created)
    answer.resolve(json(created, 201))
    expect(
      await screen.findByRole("button", { name: "Open teamspace" })
    ).toBeTruthy()
    expect(
      within(
        screen.getByRole("region", { name: "Teamspace details" })
      ).getByText("Research")
    ).toBeTruthy()
  })

  it("shows Go's refusal and adds nothing", async () => {
    backend([launch], ({ method }) =>
      method === "POST"
        ? apiError(409, "teamspace_conflict", "teamspace already exists")
        : undefined
    )
    render()
    await screen.findByRole("button", { name: "Open Launch room" })
    fireEvent.click(newTeamspaceButton())
    await screen.findByRole("dialog", { name: "New teamspace" })
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
      target: { value: "Launch room" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Create teamspace" }))

    expect(
      await screen.findByText(
        "That team already has a teamspace, or another teamspace uses this name."
      )
    ).toBeTruthy()
    expect(screen.getAllByRole("article", { hidden: true })).toHaveLength(1)
  })

  it("explains when every team the member manages already has one", async () => {
    backend([
      launch,
      design,
      teamspace({ id: "tsp_r", team_id: "team_research", name: "R" }),
    ])
    render()
    await screen.findByRole("button", { name: "Open Launch room" })
    fireEvent.click(newTeamspaceButton())
    await screen.findByRole("dialog", { name: "New teamspace" })
    expect(
      screen.getByText(
        "Every team you manage already has a teamspace. A team has one."
      )
    ).toBeTruthy()
    expect(
      screen
        .getByRole("button", { name: "Create teamspace" })
        .hasAttribute("disabled")
    ).toBe(true)
  })
})

describe("TeamspaceDetail", () => {
  it("shows real members, agents, creator and status, and opens an active teamspace", async () => {
    backend([
      teamspace({
        contributions: [
          contribution(),
          contribution({
            agent_id: "agt_notes",
            name: "Notes",
            role: "Keeps minutes",
            status: "disabled",
          }),
        ],
      }),
    ])
    const { onOpen } = render()
    await manage("Launch room")

    const members = screen.getByRole("region", { name: "Teamspace members" })
    const people = within(members).getAllByTestId("identity-avatar")
    expect(
      people.map((item) => [
        item.textContent,
        item.dataset.kind,
        item.dataset.seed,
      ])
    ).toEqual([
      ["Member", "user", viewerId],
      ["Ada", "user", "user-ada"],
    ])
    expect(within(members).getByText("You · Created it")).toBeTruthy()

    const agents = screen.getByRole("region", { name: "Teamspace agents" })
    expect(
      within(agents)
        .getAllByTestId("identity-avatar")
        .map((item) => [item.textContent, item.dataset.kind, item.dataset.seed])
    ).toEqual([
      ["Leo", "agent", "agt_leo"],
      ["Juno", "agent", "agt_juno"],
      ["Pricing", "agent", "agt_pricing"],
      ["Notes", "agent", "agt_notes"],
    ])
    expect(within(agents).getByText("Your agent")).toBeTruthy()
    expect(within(agents).getByText("Ada's agent")).toBeTruthy()
    expect(within(agents).getByText("Checks the launch pricing")).toBeTruthy()
    expect(within(agents).getByText("Specialist · asks its owner")).toBeTruthy()
    expect(within(agents).getByText("Paused specialist")).toBeTruthy()

    const details = screen.getByRole("region", { name: "Teamspace details" })
    expect(within(details).getByText("Active")).toBeTruthy()
    expect(within(details).getByText("You")).toBeTruthy()
    expect(within(details).getByText("Member · manages it")).toBeTruthy()
    expect(await within(details).findByText("None yet")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Open teamspace" }))
    expect(onOpen).toHaveBeenCalledWith("tsp_launch")
  })

  it("names a creator who is not on the team from the organization's members", async () => {
    backend([
      teamspace({
        created_by_user_id: "user-ada",
        whisperers: [memberWhisperer],
      }),
    ])
    render()
    await manage("Launch room")
    const details = screen.getByRole("region", { name: "Teamspace details" })
    expect(await within(details).findByText("Ada")).toBeTruthy()
  })

  it("shows when shared work was last published", async () => {
    backend([launch], ({ path }) =>
      path.endsWith("/entries?limit=100")
        ? json({
            entries: [
              entry({
                created_at: new Date(Date.now() - 2 * 3_600_000).toISOString(),
              }),
            ],
          })
        : undefined
    )
    render()
    await manage("Launch room")
    expect(await screen.findByText("2 hr ago")).toBeTruthy()
  })

  it("lets a team admin pause after confirming, then resume, through real mutations", async () => {
    const paused = { ...launch, status: "disabled" as const, revision: 2 }
    const api = backend([launch], ({ method, body }) => {
      if (method !== "PATCH") return undefined
      const next =
        (body as { status: string }).status === "disabled"
          ? paused
          : { ...launch, revision: 3 }
      api.state.spaces = [next]
      return json(next)
    })
    render()
    await manage("Launch room")

    fireEvent.click(screen.getByRole("button", { name: "Pause teamspace" }))
    const confirm = screen.getByRole("group", { name: "Confirm pausing" })
    expect(api.calls.some((call) => call.method === "PATCH")).toBe(false)
    fireEvent.click(within(confirm).getByRole("button", { name: "Cancel" }))
    expect(screen.queryByRole("group", { name: "Confirm pausing" })).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Pause teamspace" }))
    fireEvent.click(
      within(screen.getByRole("group", { name: "Confirm pausing" })).getByRole(
        "button",
        { name: "Pause teamspace" }
      )
    )
    expect(
      await screen.findByRole("button", { name: "Resume teamspace" })
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Open teamspace" })).toBeNull()
    expect(
      screen.getByText(
        "Paused: its room is read-only. Resume it to post again."
      )
    ).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Resume teamspace" }))
    expect(
      await screen.findByRole("button", { name: "Open teamspace" })
    ).toBeTruthy()
    expect(
      api.calls
        .filter((call) => call.method === "PATCH")
        .map((call) => [call.path, call.body])
    ).toEqual([
      [`${orgPath}/teamspaces/tsp_launch`, { status: "disabled" }],
      [`${orgPath}/teamspaces/tsp_launch`, { status: "active" }],
    ])
  })

  it("keeps the status when Go refuses the change", async () => {
    backend([launch], ({ method }) =>
      method === "PATCH"
        ? apiError(
            403,
            "forbidden",
            "teamspace is not available to this member"
          )
        : undefined
    )
    render()
    await manage("Launch room")
    fireEvent.click(screen.getByRole("button", { name: "Pause teamspace" }))
    fireEvent.click(
      within(screen.getByRole("group", { name: "Confirm pausing" })).getByRole(
        "button",
        { name: "Pause teamspace" }
      )
    )

    expect(
      await screen.findByText("Only a team admin can change this teamspace.")
    ).toBeTruthy()
    expect(
      within(
        screen.getByRole("region", { name: "Teamspace details" })
      ).getByText("Active")
    ).toBeTruthy()
  })

  it("deletes only after confirming, then returns to a list without it", async () => {
    const api = backend([launch, design], ({ method }) => {
      if (method !== "DELETE") return undefined
      api.state.spaces = [design]
      return new Response(null, { status: 204 })
    })
    render()
    await manage("Launch room")

    fireEvent.click(screen.getByRole("button", { name: "Delete teamspace" }))
    const confirm = screen.getByRole("group", { name: "Confirm deletion" })
    expect(within(confirm).getByText(/This can't be undone\./)).toBeTruthy()
    expect(api.calls.some((call) => call.method === "DELETE")).toBe(false)
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Delete teamspace" })
    )

    expect(
      await screen.findByRole("button", { name: "Open Design reviews" })
    ).toBeTruthy()
    expect(
      screen.queryByRole("button", { name: "Open Launch room" })
    ).toBeNull()
    expect(api.calls.filter((call) => call.method === "DELETE")).toEqual([
      {
        method: "DELETE",
        path: `${orgPath}/teamspaces/tsp_launch`,
        body: undefined,
      },
    ])
  })

  it("hides pause, resume and delete from someone who doesn't manage it", async () => {
    backend([{ ...launch, can_manage: false }, design])
    render()

    await manage("Launch room")
    expect(screen.getByRole("button", { name: "Open teamspace" })).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Pause teamspace" })).toBeNull()
    expect(
      screen.queryByRole("button", { name: "Delete teamspace" })
    ).toBeNull()
    expect(
      within(
        screen.getByRole("region", { name: "Teamspace details" })
      ).getByText("Member")
    ).toBeTruthy()
  })

  it("tells a member a paused teamspace waits for its admins", async () => {
    backend()
    render()
    await manage("Design reviews")
    expect(screen.queryByRole("button", { name: "Open teamspace" })).toBeNull()
    expect(
      screen.queryByRole("button", { name: "Resume teamspace" })
    ).toBeNull()
    expect(
      screen.getByText(
        "Paused by a team admin: its room is read-only until they resume it."
      )
    ).toBeTruthy()
  })

  it("does not read the ledger for someone who manages the team without being on it", async () => {
    const api = backend([{ ...launch, is_member: false }])
    render()
    await manage("Launch room")
    expect(
      within(
        screen.getByRole("region", { name: "Teamspace details" })
      ).getByText("Manages it")
    ).toBeTruthy()
    expect(screen.queryByText("Latest shared work")).toBeNull()
    expect(api.calls.some((call) => call.path.includes("/entries"))).toBe(false)
  })
})
