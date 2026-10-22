// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { Onboarding, emailsIn } from "./onboarding"
import type { SetupStep } from "./onboarding"

const ORG = "org_1"
const base = `/v1/organizations/${ORG}`

type Call = { url: string; method: string; body?: unknown }

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

/** Answers by "METHOD url"; anything unknown is a 404. Records every call. */
function stubApi(
  routes: Partial<Record<string, (body: unknown) => [number, unknown]>>
): Call[] {
  const calls: Call[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input).split("?")[0]
      const method = init?.method ?? (init?.body ? "POST" : "GET")
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      calls.push({ url, method, body })
      const route = routes[`${method} ${url}`]
      if (!route) return jsonResponse({ error: "not_found" }, 404)
      const [status, payload] = route(body)
      return jsonResponse(payload, status)
    })
  )
  return calls
}

function renderStep(step: SetupStep) {
  const onStep = vi.fn()
  const onDone = vi.fn()
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <Onboarding
        organizationId={ORG}
        organizationName="Northstar"
        step={step}
        onStep={onStep}
        onDone={onDone}
      />
    </QueryClientProvider>
  )
  return { onStep, onDone }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("emailsIn", () => {
  it("splits on commas, spaces and lines, lowercases and drops repeats", () => {
    expect(emailsIn(" Raya@x.io, priya@x.io\nraya@x.io  kenji@x.io;")).toEqual([
      "raya@x.io",
      "priya@x.io",
      "kenji@x.io",
    ])
  })
})

describe("Onboarding", () => {
  it("names the step it is on, with a five-part bar and the workspace done", () => {
    stubApi({ "GET /v1/agent-grant": () => [200, { granted: false }] })
    renderStep("agent")
    expect(screen.getByText("Step 2 of 5 · Your agent")).toBeTruthy()
    const bar = screen.getByRole("list", { name: "Step 2 of 5" })
    const parts = within(bar).getAllByRole("listitem")
    expect(parts).toHaveLength(5)
    expect(parts[1].getAttribute("aria-current")).toBe("step")
    expect(parts[0].textContent).toBe("Step 1, done")
    // Step 2 is the first after naming the workspace: no Back.
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull()
  })

  it("builds the miniature from what is really set up", async () => {
    stubApi({
      [`GET ${base}/apps/connections`]: () => [
        200,
        { connections: [{ id: "c1", toolkit: "slack", status: "active" }] },
      ],
      [`GET ${base}/invitations`]: () => [
        200,
        {
          invitations: [
            { id: "i1", email: "raya@northstar.io", status: "pending" },
            { id: "i2", email: "old@northstar.io", status: "revoked" },
          ],
        },
      ],
    })
    renderStep("crew")
    const model = screen.getByRole("img", {
      name: "A preview of your workspace as you set it up",
    })
    expect(model.textContent).toContain("Northstar")
    expect(await within(model).findByText("Slack connected")).toBeTruthy()
    expect(await within(model).findByText("raya")).toBeTruthy()
    expect(model.textContent).not.toContain("old")
    expect(
      within(screen.getByRole("list", { name: "Invited" })).getAllByRole(
        "listitem"
      )
    ).toHaveLength(1)
  })

  it("continues on Enter outside a field, and goes back with Back", () => {
    stubApi({})
    const { onStep } = renderStep("tools")
    fireEvent.keyDown(document.body, { key: "Enter" })
    expect(onStep).toHaveBeenLastCalledWith("crew")
    fireEvent.click(screen.getByRole("button", { name: "Back" }))
    expect(onStep).toHaveBeenLastCalledWith("agent")
  })

  it("shows background access as on when the grant exists, and moves on", async () => {
    stubApi({ "GET /v1/agent-grant": () => [200, { granted: true }] })
    const { onStep } = renderStep("agent")
    expect(await screen.findByText(/On\. Krude can act for you/)).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Skip for now" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
    expect(onStep).toHaveBeenCalledWith("tools")
  })

  describe("naming the agent", () => {
    const primary = {
      id: "agt_1",
      organization_id: ORG,
      owner_user_id: "user_1",
      kind: "primary",
      name: "Nia",
      description: "Your primary Oryxa agent.",
      instructions: "",
      status: "active",
      revision: 1,
      created_at: "2026-10-22T00:00:00Z",
      updated_at: "2026-10-22T00:00:00Z",
    }
    const agentRoutes = (
      patch: (body: unknown) => [number, unknown] = (body) => [
        200,
        { ...primary, ...(body as object), revision: 2 },
      ]
    ) => ({
      "GET /v1/agent-grant": (): [number, unknown] => [200, { granted: false }],
      [`GET ${base}/agents`]: (): [number, unknown] => [
        200,
        { agents: [primary] },
      ],
      [`PATCH ${base}/agents/agt_1`]: patch,
    })
    const patches = (calls: Call[]) =>
      calls.filter((call) => call.method === "PATCH").map((call) => call.body)

    it("starts from the name the agent has, and keeps it without a save", async () => {
      const calls = stubApi(agentRoutes())
      const { onStep } = renderStep("agent")
      const field = await screen.findByLabelText<HTMLInputElement>("Name")
      expect(field.value).toBe("Nia")
      fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
      await waitFor(() => expect(onStep).toHaveBeenCalledWith("tools"))
      expect(patches(calls)).toEqual([])
    })

    it("saves the name typed, trimmed, then moves on", async () => {
      const calls = stubApi(agentRoutes())
      const { onStep } = renderStep("agent")
      const field = await screen.findByLabelText("Name")
      fireEvent.change(field, { target: { value: "  Wren " } })
      fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
      await waitFor(() => expect(onStep).toHaveBeenCalledWith("tools"))
      expect(patches(calls)).toEqual([{ name: "Wren" }])
    })

    it("shows the name as it is typed, in the step and the miniature", async () => {
      stubApi(agentRoutes())
      renderStep("agent")
      const field = await screen.findByLabelText("Name")
      fireEvent.change(field, { target: { value: "Wren" } })
      const model = screen.getByRole("img", {
        name: "A preview of your workspace as you set it up",
      })
      expect(await within(model).findByText("Wren")).toBeTruthy()
      expect(
        screen.getByText(/Wren can answer and follow up in the background/)
      ).toBeTruthy()
    })

    it("asks for a name rather than saving none, or one too long", async () => {
      const calls = stubApi(agentRoutes())
      const { onStep } = renderStep("agent")
      const field = await screen.findByLabelText("Name")
      fireEvent.change(field, { target: { value: "   " } })
      fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
      expect(await screen.findByText("Give your agent a name.")).toBeTruthy()
      fireEvent.change(field, { target: { value: "x".repeat(61) } })
      fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
      expect(
        await screen.findByText("Use 60 characters or fewer.")
      ).toBeTruthy()
      expect(patches(calls)).toEqual([])
      expect(onStep).not.toHaveBeenCalled()
    })

    it("says when another agent has the name, and stays on the step", async () => {
      stubApi(
        agentRoutes(() => [
          409,
          { error: "name_conflict", message: "agent name is already in use" },
        ])
      )
      const { onStep } = renderStep("agent")
      const field = await screen.findByLabelText("Name")
      fireEvent.change(field, { target: { value: "Maya" } })
      fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
      expect(
        await screen.findByText(
          "Another agent in this workspace already has that name. Try another."
        )
      ).toBeTruthy()
      expect(onStep).not.toHaveBeenCalled()
    })

    it("saves on Enter in the field too", async () => {
      const calls = stubApi(agentRoutes())
      const { onStep } = renderStep("agent")
      const field = await screen.findByLabelText("Name")
      fireEvent.change(field, { target: { value: "Wren" } })
      fireEvent.submit(field.closest("form")!)
      await waitFor(() => expect(onStep).toHaveBeenCalledWith("tools"))
      expect(patches(calls)).toEqual([{ name: "Wren" }])
    })

    it("moves on without a field when the agent can't be read", async () => {
      stubApi({ "GET /v1/agent-grant": () => [200, { granted: false }] })
      const { onStep } = renderStep("agent")
      expect(
        await screen.findByText(/You can name it later in Settings/)
      ).toBeTruthy()
      expect(screen.queryByLabelText("Name")).toBeNull()
      fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
      expect(onStep).toHaveBeenCalledWith("tools")
    })
  })

  it("marks an app connected only when its connection is active", async () => {
    stubApi({
      [`GET ${base}/apps/connections`]: () => [
        200,
        {
          connections: [
            { id: "c1", toolkit: "slack", status: "active" },
            { id: "c2", toolkit: "github", status: "pending" },
          ],
        },
      ],
    })
    renderStep("tools")
    expect(await screen.findByLabelText("Slack connected")).toBeTruthy()
    expect(screen.getByRole("button", { name: "Connect GitHub" })).toBeTruthy()
  })

  it("invites each address once, says who was invited, and checks addresses first", async () => {
    const calls = stubApi({
      [`POST ${base}/invitations`]: (body) => [
        201,
        { id: "inv", ...(body as object) },
      ],
    })
    renderStep("crew")
    const field = screen.getByLabelText("Email addresses")
    fireEvent.change(field, { target: { value: "not-an-email" } })
    fireEvent.click(screen.getByRole("button", { name: "Send invitations" }))
    expect(screen.getByText("Check not-an-email.")).toBeTruthy()
    // The miniature reads; nothing is sent.
    expect(calls.filter((call) => call.method !== "GET")).toEqual([])

    fireEvent.change(field, {
      target: {
        value: "raya@northstar.io, Priya@northstar.io raya@northstar.io",
      },
    })
    fireEvent.click(screen.getByRole("button", { name: "Send invitations" }))
    const invited = await screen.findByRole("list", { name: "Invited" })
    expect(invited.textContent).toContain("raya@northstar.io")
    expect(invited.textContent).toContain("priya@northstar.io")
    expect(
      calls.filter((call) => call.method === "POST").map((call) => call.body)
    ).toEqual([
      { email: "raya@northstar.io", role: "member" },
      { email: "priya@northstar.io", role: "member" },
    ])
  })

  it("opens the first teamspace through a team of the same name", async () => {
    const calls = stubApi({
      [`POST ${base}/teams`]: () => [
        201,
        { id: "team_1", name: "EU launch", organization_id: ORG },
      ],
      [`POST ${base}/teamspaces`]: () => [
        201,
        { id: "ts_1", team_id: "team_1", name: "EU launch" },
      ],
    })
    const { onDone } = renderStep("teamspace")
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: " EU launch " },
    })
    fireEvent.click(screen.getByRole("button", { name: /Open my workspace/ }))
    await waitFor(() => expect(onDone).toHaveBeenCalledWith("ts_1"))
    expect(
      calls
        .filter((call) => call.method === "POST")
        .map((call) => [call.url, call.body])
    ).toEqual([
      [`${base}/teams`, { name: "EU launch" }],
      [`${base}/teamspaces`, { team_id: "team_1", name: "EU launch" }],
    ])
  })

  it("lets the last step be skipped straight into the workspace", () => {
    stubApi({})
    const { onDone } = renderStep("teamspace")
    fireEvent.click(screen.getByRole("button", { name: "Skip for now" }))
    expect(onDone).toHaveBeenCalledWith()
  })
})
