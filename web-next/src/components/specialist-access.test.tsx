// @vitest-environment jsdom

import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  SpecialistAccess,
  choiceOf,
  corporaChoiceOf,
  grantFrom,
  grantWithCorpora,
} from "./specialist-access"
import type { ManagedAgent } from "@/lib/agents/managed-agents"

const agent: ManagedAgent = {
  id: "agt_research",
  organization_id: "org_1",
  owner_user_id: "usr_ada",
  kind: "specialist",
  name: "Researcher",
  description: "",
  instructions: "Find things.",
  status: "active",
  revision: 3,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-14T00:00:00Z",
  model_tier: "lite",
}

function corpus(id: string, name: string) {
  return {
    id,
    organization_id: "org_1",
    slug: id,
    name,
    scope: "organization",
    document_count: 2,
    chunk_count: 8,
  }
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()

function patches() {
  return fetchMock.mock.calls
    .filter(([, init]) => init?.method === "PATCH")
    .map(
      ([, init]) => JSON.parse(String(init?.body)) as Record<string, unknown>
    )
}

beforeEach(() => {
  vi.stubGlobal("fetch", fetchMock)
  fetchMock.mockImplementation(async (url, init) => {
    if (String(url).endsWith("/corpora")) {
      return json({
        corpora: [corpus("cor_plans", "Plans"), corpus("cor_hr", "HR")],
      })
    }
    if (String(url).endsWith("/apps/connections")) {
      return json({
        connections: [
          { id: "c1", toolkit: "slack", status: "active" },
          { id: "c2", toolkit: "github", status: "active" },
          { id: "c3", toolkit: "notion", status: "expired" },
        ],
      })
    }
    if (init?.method === "PATCH") {
      return json({ ...agent, ...(JSON.parse(String(init.body)) as object) })
    }
    return json({ agents: [agent] })
  })
})

afterEach(() => {
  cleanup()
  fetchMock.mockReset()
  vi.unstubAllGlobals()
})

function renderAccess(shown: ManagedAgent = agent) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <SpecialistAccess organizationId="org_1" agent={shown} />
    </QueryClientProvider>
  )
}

describe("a specialist's grant", () => {
  it("reads a missing grant as every app, read only", () => {
    expect(choiceOf({ apps: ["*"] })).toEqual({
      every: true,
      reads: [],
      writes: [],
    })
  })

  it("saves only writes to apps it reads, and keeps its tools", () => {
    expect(
      grantFrom(
        {
          every: false,
          reads: ["slack", "github"],
          writes: ["github", "notion"],
        },
        { apps: ["*"], tools: ["SLACK_SEND"], write_tools: [] }
      )
    ).toEqual({
      apps: ["github", "slack"],
      tools: ["SLACK_SEND"],
      write_apps: ["github"],
      write_tools: [],
      corpora: ["*"],
    })
    expect(
      grantFrom({ every: true, reads: [], writes: ["slack"] }, { apps: ["*"] })
    ).toEqual({
      apps: ["*"],
      tools: [],
      write_apps: ["slack"],
      write_tools: [],
      corpora: ["*"],
    })
    expect(
      grantFrom(
        { every: true, reads: [], writes: [] },
        { apps: ["github"], corpora: ["cor_plans"] }
      ).corpora
    ).toEqual(["cor_plans"])
  })

  it("reads its corpora as all, some or none", () => {
    expect(corporaChoiceOf({ apps: ["*"] })).toEqual({ scope: "all", ids: [] })
    expect(corporaChoiceOf({ apps: ["*"], corpora: ["*"] })).toEqual({
      scope: "all",
      ids: [],
    })
    expect(corporaChoiceOf({ apps: ["*"], corpora: [] })).toEqual({
      scope: "none",
      ids: [],
    })
    expect(corporaChoiceOf({ apps: ["*"], corpora: ["cor_a"] })).toEqual({
      scope: "some",
      ids: ["cor_a"],
    })
  })

  it("saves corpora and keeps the rest of the grant", () => {
    const previous = {
      apps: ["github"],
      tools: ["GITHUB_GET_ISSUE"],
      write_apps: ["github"],
      write_tools: ["GITHUB_CREATE_ISSUE"],
      corpora: ["*"],
    }
    expect(
      grantWithCorpora({ scope: "some", ids: ["cor_b", "cor_a"] }, previous)
    ).toEqual({ ...previous, corpora: ["cor_a", "cor_b"] })
    expect(
      grantWithCorpora({ scope: "none", ids: ["cor_a"] }, previous)
    ).toEqual({ ...previous, corpora: [] })
    expect(grantWithCorpora({ scope: "all", ids: [] }, previous)).toEqual(
      previous
    )
  })
})

describe("saving a grant", () => {
  it("keeps the grant fields neither card edits", () => {
    const previous = {
      apps: ["gmail"],
      tools: [],
      write_apps: [],
      write_tools: [],
      corpora: ["cor_a"],
      files: ["team_eng"],
      knowledge: [],
    }
    const apps = grantFrom(
      { every: false, reads: ["gmail", "github"], writes: [] },
      previous
    )
    expect(apps.files).toEqual(["team_eng"])
    expect(apps.knowledge).toEqual([])
    const corpora = grantWithCorpora({ scope: "all", ids: [] }, previous)
    expect(corpora.files).toEqual(["team_eng"])
    expect(corpora.knowledge).toEqual([])
  })
})

describe("SpecialistAccess", () => {
  it("moves the specialist to the main model", async () => {
    renderAccess()
    const tiers = screen.getByRole("radiogroup", { name: "Researcher's model" })
    expect(
      within(tiers)
        .getByRole("radio", { name: /Lite/ })
        .getAttribute("aria-checked")
    ).toBe("true")
    fireEvent.click(within(tiers).getByRole("radio", { name: /Main/ }))
    await waitFor(() => expect(patches()).toEqual([{ model_tier: "main" }]))
  })

  it("lets it change things in one app while it reads every app", async () => {
    renderAccess()
    const apps = await screen.findByRole("list", { name: "Apps" })
    // Only active connections are offered.
    expect(within(apps).queryByText("Notion")).toBeNull()
    const reads = within(apps).getByRole("checkbox", {
      name: "Researcher may use Github",
    })
    expect((reads as HTMLInputElement).checked).toBe(true)
    expect((reads as HTMLInputElement).disabled).toBe(true)

    fireEvent.click(
      within(apps).getByRole("switch", {
        name: "Researcher may change things in Github",
      })
    )
    fireEvent.click(screen.getByRole("button", { name: "Save apps" }))
    await waitFor(() =>
      expect(patches()).toEqual([
        {
          grant: {
            apps: ["*"],
            tools: [],
            write_apps: ["github"],
            write_tools: [],
            corpora: ["*"],
          },
        },
      ])
    )
  })

  it("narrows it to chosen apps, and taking an app away takes its changes too", async () => {
    renderAccess({ ...agent, grant: { apps: ["*"], write_apps: ["slack"] } })
    await screen.findByRole("list", { name: "Apps" })
    fireEvent.click(
      screen.getByRole("radio", { name: /Only the apps you choose/ })
    )
    fireEvent.click(
      screen.getByRole("checkbox", { name: "Researcher may use Slack" })
    )
    expect(
      screen
        .getByRole("switch", {
          name: "Researcher may change things in Slack",
        })
        .hasAttribute("disabled")
    ).toBe(true)
    fireEvent.click(screen.getByRole("button", { name: "Save apps" }))
    await waitFor(() =>
      expect(patches()).toEqual([
        {
          grant: {
            apps: ["github"],
            tools: [],
            write_apps: [],
            write_tools: [],
            corpora: ["*"],
          },
        },
      ])
    )
  })

  it("says why the server refused a grant", async () => {
    fetchMock.mockImplementation(async (url, init) => {
      if (String(url).endsWith("/apps/connections")) {
        return json({
          connections: [{ id: "c1", toolkit: "slack", status: "active" }],
        })
      }
      if (init?.method === "PATCH") {
        return json(
          {
            error: {
              code: "invalid_request",
              message: "a grant's apps hold at most 200 entries",
            },
          },
          400
        )
      }
      return json({ agents: [agent] })
    })
    renderAccess()
    await screen.findByRole("list", { name: "Apps" })
    fireEvent.click(
      screen.getByRole("switch", {
        name: "Researcher may change things in Slack",
      })
    )
    fireEvent.click(screen.getByRole("button", { name: "Save apps" }))
    expect((await screen.findByRole("alert")).textContent).toBeTruthy()
  })

  describe("its collections", () => {
    const narrowed: ManagedAgent = {
      ...agent,
      grant: {
        apps: ["github"],
        tools: ["GITHUB_GET_ISSUE"],
        write_apps: ["github"],
        write_tools: [],
        corpora: ["cor_plans", "cor_gone"],
      },
    }
    const kept = {
      apps: ["github"],
      tools: ["GITHUB_GET_ISSUE"],
      write_apps: ["github"],
      write_tools: [],
    }
    const scopes = () =>
      screen.getByRole("radiogroup", {
        name: "Which collections it may search",
      })

    it("shows the current choice, with a collection it can't see any more", async () => {
      renderAccess(narrowed)
      const chosen = within(scopes()).getByRole("radio", { name: /Only these/ })
      expect((chosen as HTMLInputElement).checked).toBe(true)
      const list = await screen.findByRole("list", { name: "Collections" })
      const checked = (name: string) => {
        const box = within(list).getByRole("checkbox", {
          name: `Researcher may search ${name}`,
        })
        return (box as HTMLInputElement).checked
      }
      expect(checked("Plans")).toBe(true)
      expect(checked("HR")).toBe(false)
      expect(checked("cor_gone")).toBe(true)
      expect(within(list).getByText("No longer available")).toBeTruthy()
    })

    it("reads a grant without corpora as all collections", () => {
      renderAccess()
      const chosen = within(scopes()).getByRole("radio", {
        name: /All collections/,
      })
      expect((chosen as HTMLInputElement).checked).toBe(true)
      expect(screen.queryByRole("list", { name: "Collections" })).toBeNull()
    })

    it("saves all collections", async () => {
      renderAccess(narrowed)
      fireEvent.click(
        within(scopes()).getByRole("radio", { name: /All collections/ })
      )
      fireEvent.click(screen.getByRole("button", { name: "Save collections" }))
      await waitFor(() =>
        expect(patches()).toEqual([{ grant: { ...kept, corpora: ["*"] } }])
      )
    })

    it("saves the collections picked", async () => {
      renderAccess(narrowed)
      const list = await screen.findByRole("list", { name: "Collections" })
      fireEvent.click(
        within(list).getByRole("checkbox", { name: "Researcher may search HR" })
      )
      fireEvent.click(
        within(list).getByRole("checkbox", {
          name: "Researcher may search cor_gone",
        })
      )
      fireEvent.click(screen.getByRole("button", { name: "Save collections" }))
      await waitFor(() =>
        expect(patches()).toEqual([
          { grant: { ...kept, corpora: ["cor_hr", "cor_plans"] } },
        ])
      )
    })

    it("starts a pick from every collection it can see", async () => {
      renderAccess()
      const only = within(scopes()).getByRole("radio", { name: /Only these/ })
      await waitFor(() =>
        expect((only as HTMLInputElement).disabled).toBe(false)
      )
      fireEvent.click(only)
      await screen.findByRole("list", { name: "Collections" })
      fireEvent.click(
        screen.getByRole("checkbox", { name: "Researcher may search Plans" })
      )
      fireEvent.click(screen.getByRole("button", { name: "Save collections" }))
      await waitFor(() =>
        expect(patches()).toEqual([
          {
            grant: {
              apps: ["*"],
              tools: [],
              write_apps: [],
              write_tools: [],
              corpora: ["cor_hr"],
            },
          },
        ])
      )
    })

    it("saves none", async () => {
      renderAccess(narrowed)
      fireEvent.click(within(scopes()).getByRole("radio", { name: /None/ }))
      fireEvent.click(screen.getByRole("button", { name: "Save collections" }))
      await waitFor(() =>
        expect(patches()).toEqual([{ grant: { ...kept, corpora: [] } }])
      )
    })

    it("keeps its corpora when its apps are saved", async () => {
      renderAccess(narrowed)
      await screen.findByRole("list", { name: "Apps" })
      fireEvent.click(
        screen.getByRole("checkbox", { name: "Researcher may use Slack" })
      )
      fireEvent.click(screen.getByRole("button", { name: "Save apps" }))
      await waitFor(() =>
        expect(patches()).toEqual([
          {
            grant: {
              ...kept,
              apps: ["github", "slack"],
              corpora: ["cor_plans", "cor_gone"],
            },
          },
        ])
      )
    })
  })
})
