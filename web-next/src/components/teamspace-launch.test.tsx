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
import type { AvatarIdentity } from "./workspace-avatar"
import type * as AvatarModule from "./workspace-avatar"
import { TeamspaceLaunchCard } from "./teamspace-launch"
import type { Teamspace, TeamspaceTask } from "@/lib/teamspaces/client"
import { teamspaceKeys } from "@/lib/teamspaces/queries"
import {
  apiError,
  json,
  mockApi,
  orgPath,
  teamspace,
  viewerId,
} from "@/test/teamspaces"

vi.mock("./workspace-avatar", async (importOriginal) => {
  const actual = await importOriginal<typeof AvatarModule>()
  return {
    ...actual,
    IdentityAvatar: ({ identity }: { identity: AvatarIdentity }) => (
      <span data-testid="identity-avatar" data-seed={identity.id}>
        {identity.name}
      </span>
    ),
  }
})

const spacePath = `${orgPath}/teamspaces/tsp_launch`
const tasksPath = `${spacePath}/tasks`
const teamMembersPath = `${orgPath}/teams/team_launch/members`

/** `YYYY-MM-DD`, `days` from today on this machine's calendar. */
function dayFromToday(days: number) {
  const date = new Date()
  date.setDate(date.getDate() + days)
  const pad = (value: number) => String(value).padStart(2, "0")
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

function task(overrides: Partial<TeamspaceTask> = {}): TeamspaceTask {
  return {
    id: "tst_1",
    teamspace_id: "tsp_launch",
    organization_id: "org_sim",
    title: "Legal sign-off",
    owner_user_id: null,
    done: false,
    position: 0,
    due_date: null,
    created_by_user_id: "user-ada",
    created_at: "2026-10-10T09:00:00Z",
    updated_at: "2026-10-10T09:00:00Z",
    done_at: null,
    ...overrides,
  }
}

const served = {
  target_date: dayFromToday(21),
  target_label: "Launch",
  task_counts: { done: 1, total: 2 },
} satisfies Partial<Teamspace>

function backend(
  tasks: TeamspaceTask[],
  extra: Parameters<typeof mockApi>[0] = () => undefined
) {
  const state = { tasks: [...tasks] }
  const api = mockApi(async (call) => {
    const answer = await extra(call)
    if (answer) return answer
    const path = call.url.pathname
    if (path === tasksPath && call.method === "GET") {
      return json({ tasks: state.tasks })
    }
    if (path === teamMembersPath && call.method === "GET") {
      return json({
        members: [
          { user_id: viewerId, email: "member@example.test", role: "admin" },
          {
            user_id: "user-ada",
            email: "ada@example.test",
            display_name: "Ada",
            role: "member",
          },
          {
            user_id: "user-kenji",
            email: "kenji@example.test",
            display_name: "Kenji",
            role: "member",
          },
        ],
      })
    }
    if (path === spacePath) return json(teamspace(served))
    if (path === `${orgPath}/teamspaces`) return json({ teamspaces: [] })
    return undefined
  })
  return { ...api, state }
}

/** Opens the first task's title field from its ⋯ menu. */
async function openTitleEdit() {
  fireEvent.click(
    await screen.findByRole("button", { name: "More for Legal sign-off" })
  )
  fireEvent.click(await screen.findByRole("menuitem", { name: "Edit title" }))
  return screen.findByRole("textbox", { name: "Task title" })
}

function render(space: Teamspace) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return {
    ...renderBase(
      <QueryClientProvider client={client}>
        <TeamspaceLaunchCard
          organizationId="org_sim"
          space={space}
          viewerId={viewerId}
        />
      </QueryClientProvider>
    ),
    client,
  }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("TeamspaceLaunchCard", () => {
  it("shows nothing, and reads nothing, from a Go without targets or tasks", async () => {
    const { calls } = backend([])
    const { container } = render(teamspace())
    await new Promise((settle) => setTimeout(settle, 0))
    expect(container.textContent).toBe("")
    expect(calls).toEqual([])
  })

  it("shows the target, how much is done and the checklist", async () => {
    backend([
      task({ done: true, title: "Billing retry tests" }),
      task({
        id: "tst_2",
        owner_user_id: "user-kenji",
        due_date: dayFromToday(3),
      }),
    ])
    render(teamspace(served))

    const card = await screen.findByRole("region", { name: "Launch" })
    expect(within(card).getByRole("heading", { name: "Launch" })).toBeTruthy()
    expect(within(card).getByText("in 21 days")).toBeTruthy()
    expect(within(card).getByText("1 of 2 done")).toBeTruthy()
    const done = await within(card).findByRole("checkbox", {
      name: "Billing retry tests",
    })
    expect(done.getAttribute("aria-checked")).toBe("true")
    expect(
      within(card)
        .getByRole("checkbox", { name: "Legal sign-off" })
        .getAttribute("aria-checked")
    ).toBe("false")
    // Its owner by name, from the team's members.
    expect(await within(card).findByText(/^Due .+ · Kenji$/)).toBeTruthy()
  })

  it("ticks a task, then reads the checklist and the counts again", async () => {
    const { calls, state } = backend([task()], (call) => {
      if (
        call.method === "PATCH" &&
        call.url.pathname === `${tasksPath}/tst_1`
      ) {
        state.tasks = [task({ done: true })]
        return json(state.tasks[0])
      }
      return undefined
    })
    const { client } = render(teamspace(served))
    const detailKey = teamspaceKeys.teamspace("org_sim", "tsp_launch")
    client.setQueryData(detailKey, teamspace(served))

    fireEvent.click(
      await screen.findByRole("checkbox", { name: "Legal sign-off" })
    )
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PATCH",
        path: `${tasksPath}/tst_1`,
        body: { done: true },
      })
    )
    await waitFor(() =>
      expect(
        calls.filter((call) => call.path === tasksPath).length
      ).toBeGreaterThan(1)
    )
    // The teamspace is read again for its task_counts.
    expect(client.getQueryState(detailKey)?.isInvalidated).toBe(true)
  })

  it("adds a task with an owner and a due date", async () => {
    const due = dayFromToday(5)
    const { calls } = backend([], (call) => {
      if (call.method === "POST" && call.url.pathname === tasksPath) {
        return json(
          task({ id: "tst_9", title: "Close pen-test findings" }),
          201
        )
      }
      return undefined
    })
    render(teamspace(served))

    expect(
      await screen.findByText("Nothing on the checklist yet.")
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Add a task" }))
    const form = screen.getByRole("form", { name: "Add a task" })
    fireEvent.change(within(form).getByLabelText("Task"), {
      target: { value: "  Close   pen-test findings " },
    })
    await waitFor(() =>
      expect(within(form).getByRole("option", { name: "Kenji" })).toBeTruthy()
    )
    fireEvent.change(within(form).getByLabelText("Owner"), {
      target: { value: "user-kenji" },
    })
    fireEvent.change(within(form).getByLabelText("Due"), {
      target: { value: due },
    })
    fireEvent.click(within(form).getByRole("button", { name: "Add task" }))

    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "POST",
        path: tasksPath,
        body: {
          title: "Close pen-test findings",
          owner_user_id: "user-kenji",
          due_date: due,
        },
      })
    )
  })

  it("edits a title inline from the row's menu", async () => {
    const { calls } = backend([task()], (call) =>
      call.method === "PATCH"
        ? json(task({ title: "Legal sign-off (EU)" }))
        : undefined
    )
    render(teamspace(served))

    fireEvent.click(
      await screen.findByRole("button", { name: "More for Legal sign-off" })
    )
    fireEvent.click(await screen.findByRole("menuitem", { name: "Edit title" }))
    const input = await screen.findByRole("textbox", { name: "Task title" })
    fireEvent.change(input, { target: { value: "Legal sign-off (EU)" } })
    fireEvent.keyDown(input, { key: "Enter" })

    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PATCH",
        path: `${tasksPath}/tst_1`,
        body: { title: "Legal sign-off (EU)" },
      })
    )
  })

  it("sends nothing when Escape cancels an edit and the field then blurs", async () => {
    const { calls } = backend([task()])
    render(teamspace(served))

    const input = await openTitleEdit()
    fireEvent.change(input, { target: { value: "Legal sign-off (EU)" } })
    fireEvent.keyDown(input, { key: "Escape" })
    fireEvent.blur(input)

    expect(
      await screen.findByRole("checkbox", { name: "Legal sign-off" })
    ).toBeTruthy()
    await new Promise((settle) => setTimeout(settle, 0))
    expect(calls.filter((call) => call.method === "PATCH")).toEqual([])
  })

  it("saves a title once when Enter is followed by a blur", async () => {
    const { calls } = backend([task()], (call) =>
      call.method === "PATCH"
        ? json(task({ title: "Legal sign-off (EU)" }))
        : undefined
    )
    render(teamspace(served))

    const input = await openTitleEdit()
    fireEvent.change(input, { target: { value: "Legal sign-off (EU)" } })
    fireEvent.keyDown(input, { key: "Enter" })
    fireEvent.blur(input)

    await waitFor(() =>
      expect(calls.filter((call) => call.method === "PATCH")).toEqual([
        {
          method: "PATCH",
          path: `${tasksPath}/tst_1`,
          body: { title: "Legal sign-off (EU)" },
        },
      ])
    )
    await new Promise((settle) => setTimeout(settle, 0))
    expect(calls.filter((call) => call.method === "PATCH")).toHaveLength(1)
  })

  it("reassigns a task from the row's menu", async () => {
    const { calls } = backend([task()], (call) =>
      call.method === "PATCH"
        ? json(task({ owner_user_id: "user-ada" }))
        : undefined
    )
    render(teamspace(served))

    fireEvent.click(
      await screen.findByRole("button", { name: "More for Legal sign-off" })
    )
    fireEvent.click(await screen.findByRole("menuitem", { name: "Assign to" }))
    fireEvent.click(await screen.findByRole("menuitemradio", { name: "Ada" }))

    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PATCH",
        path: `${tasksPath}/tst_1`,
        body: { owner_user_id: "user-ada" },
      })
    )
  })

  it("offers Delete to whoever created the task or manages the team", async () => {
    const { calls } = backend(
      [
        task({ id: "tst_mine", title: "Mine", created_by_user_id: viewerId }),
        task(),
      ],
      (call) =>
        call.method === "DELETE"
          ? new Response(null, { status: 204 })
          : undefined
    )
    render(teamspace({ ...served, can_manage: false }))

    fireEvent.click(
      await screen.findByRole("button", { name: "More for Legal sign-off" })
    )
    await screen.findByRole("menuitem", { name: "Edit title" })
    expect(screen.queryByRole("menuitem", { name: "Delete task" })).toBeNull()
    fireEvent.keyDown(document.activeElement ?? document.body, {
      key: "Escape",
    })

    fireEvent.click(
      await screen.findByRole("button", { name: "More for Mine" })
    )
    fireEvent.click(
      await screen.findByRole("menuitem", { name: "Delete task" })
    )
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "DELETE",
        path: `${tasksPath}/tst_mine`,
        body: undefined,
      })
    )
  })

  it("is read-only while the teamspace is paused", async () => {
    backend([task()])
    render(teamspace({ ...served, status: "disabled" }))

    const box = await screen.findByRole("checkbox", { name: "Legal sign-off" })
    expect(
      box.hasAttribute("disabled") ||
        box.getAttribute("aria-disabled") === "true"
    ).toBe(true)
    expect(screen.queryByRole("button", { name: "Add a task" })).toBeNull()
    expect(screen.queryByRole("button", { name: /^More for/ })).toBeNull()
    expect(screen.queryByRole("button", { name: /target date/ })).toBeNull()
  })

  it("says the checklist went read-only when Go refuses a write 409", async () => {
    backend([task()], (call) =>
      call.method === "PATCH"
        ? apiError(409, "teamspace_disabled", "teamspace is paused")
        : undefined
    )
    render(teamspace(served))

    fireEvent.click(
      await screen.findByRole("checkbox", { name: "Legal sign-off" })
    )
    expect(
      await screen.findByText(
        "This teamspace was paused, so its checklist is read-only now."
      )
    ).toBeTruthy()
  })

  it("lets a manager set and clear the target", async () => {
    const date = dayFromToday(30)
    const { calls } = backend([], (call) =>
      call.method === "PATCH" && call.url.pathname === spacePath
        ? json(teamspace({ ...served, ...(call.body as object) }))
        : undefined
    )
    render(teamspace({ ...served, target_date: null, target_label: null }))

    // Nothing set yet: the card is the checklist, with a way to set a date.
    expect(
      await screen.findByRole("heading", { name: "Checklist" })
    ).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Set a target date" }))
    fireEvent.change(await screen.findByLabelText("What it is"), {
      target: { value: "Public launch" },
    })
    fireEvent.change(screen.getByLabelText("Date"), {
      target: { value: date },
    })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PATCH",
        path: spacePath,
        body: { target_date: date, target_label: "Public launch" },
      })
    )

    cleanup()
    render(teamspace(served))
    fireEvent.click(
      await screen.findByRole("button", { name: "Change the target date" })
    )
    fireEvent.click(
      await screen.findByRole("button", { name: "Clear the target" })
    )
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PATCH",
        path: spacePath,
        body: { target_date: null, target_label: null },
      })
    )
  })

  it("says the target went read-only, and reads the teamspace, when Go refuses it 409", async () => {
    const { calls } = backend([], (call) =>
      call.method === "PATCH" && call.url.pathname === spacePath
        ? apiError(409, "teamspace_disabled", "teamspace is paused")
        : undefined
    )
    const { client } = render(teamspace(served))
    const invalidate = vi.spyOn(client, "invalidateQueries")

    fireEvent.click(
      await screen.findByRole("button", { name: "Change the target date" })
    )
    fireEvent.change(await screen.findByLabelText("What it is"), {
      target: { value: "Public launch" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    expect(
      await screen.findByText(
        "This teamspace was paused, so its target can't change now."
      )
    ).toBeTruthy()
    expect(
      screen.queryByText("Couldn't save the target. Try again.")
    ).toBeNull()
    expect(screen.queryByLabelText("What it is")).toBeNull()
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: teamspaceKeys.teamspace("org_sim", "tsp_launch"),
      exact: true,
    })
    expect(calls.some((call) => call.method === "PATCH")).toBe(true)
  })

  it("offers the target only to whoever manages the team", async () => {
    backend([])
    render(teamspace({ ...served, can_manage: false }))
    await screen.findByRole("heading", { name: "Launch" })
    expect(screen.queryByRole("button", { name: /target date/ })).toBeNull()
  })

  it("leaves the checklist out for a manager who isn't on the team", async () => {
    const { calls } = backend([])
    render(teamspace({ ...served, is_member: false }))
    await screen.findByRole("heading", { name: "Launch" })
    expect(screen.queryByRole("button", { name: "Add a task" })).toBeNull()
    expect(calls.some((call) => call.path === tasksPath)).toBe(false)
  })
})
