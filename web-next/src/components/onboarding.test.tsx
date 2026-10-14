// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
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
  it("names the step it is on, with the workspace done", () => {
    stubApi({ "GET /v1/agent-grant": () => [200, { granted: false }] })
    renderStep("agent")
    const steps = screen.getByRole("list", { name: "Step 2 of 5" })
    expect(steps.textContent).toContain("Workspace")
    expect(screen.getByText("Krude").getAttribute("aria-current")).toBe("step")
  })

  it("shows background access as on when the grant exists, and moves on", async () => {
    stubApi({ "GET /v1/agent-grant": () => [200, { granted: true }] })
    const { onStep } = renderStep("agent")
    expect(await screen.findByText(/On\. Krude can act for you/)).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Skip" })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: /Continue/ }))
    expect(onStep).toHaveBeenCalledWith("tools")
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
    expect(calls).toEqual([])

    fireEvent.change(field, {
      target: {
        value: "raya@northstar.io, Priya@northstar.io raya@northstar.io",
      },
    })
    fireEvent.click(screen.getByRole("button", { name: "Send invitations" }))
    const invited = await screen.findByRole("list", { name: "Invited" })
    expect(invited.textContent).toContain("raya@northstar.io")
    expect(invited.textContent).toContain("priya@northstar.io")
    expect(calls.map((call) => call.body)).toEqual([
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
    fireEvent.click(screen.getByRole("button", { name: /Open teamspace/ }))
    await waitFor(() => expect(onDone).toHaveBeenCalledWith("ts_1"))
    expect(calls.map((call) => [call.url, call.body])).toEqual([
      [`${base}/teams`, { name: "EU launch" }],
      [`${base}/teamspaces`, { team_id: "team_1", name: "EU launch" }],
    ])
  })

  it("lets the last step be skipped straight into the workspace", () => {
    stubApi({})
    const { onDone } = renderStep("teamspace")
    fireEvent.click(screen.getByRole("button", { name: "Skip" }))
    expect(onDone).toHaveBeenCalledWith()
  })
})
