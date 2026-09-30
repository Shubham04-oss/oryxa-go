// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render as renderBase,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactElement } from "react"
import { resumeSessionAfterSignIn } from "@/lib/api/client"
import type {
  AnswerGuardPreferences,
  GuardLevel,
} from "@/lib/answer-guard/client"
import { WorkspaceAnswerCheckSettings } from "./workspace-answer-check-settings"

const organization = { id: "org-1", name: "Oryxa", slug: "oryxa" }
const guardPath = "/v1/organizations/org-1/answer-guard/preferences"
const cacheKey = ["settings", "answer-guard", "org-1"]

const ownerTitle = "Answers you start yourself"
const peerTitle = "Answers other people read"
const intro =
  "When your agent answers someone, Oryxa checks the answer first. Choose what it hides."
const floorLine =
  "Your organization requires all personal details to be hidden here."
const regulatedLine =
  "Your organization is regulated: health, HR and pay details wait for you before anyone else sees them."
const holdLine =
  "Answers that don't match what was asked are held for you first."

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function apiError(code: string, message: string, status: number) {
  return json({ error: { code, message } }, status)
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => {
    resolve = done
  })
  return { promise, resolve }
}

/** What Go tells a member who has chosen nothing, in an organization that asks for nothing. */
function settings(
  overrides: Partial<AnswerGuardPreferences> = {}
): AnswerGuardPreferences {
  return {
    owner: {
      chosen: null,
      default: "secrets",
      floor: "secrets",
      effective: "secrets",
    },
    peer: {
      chosen: null,
      default: "strict",
      floor: "secrets",
      effective: "strict",
    },
    relevance: "shadow",
    regulated: false,
    ...overrides,
  }
}

type FakeRequest = { method: string; path: string; body: unknown }

/** Go's rule, written out again here: the stricter of the floor and the choice, or the default. */
function applies(origin: {
  chosen: GuardLevel | null
  default: GuardLevel
  floor: GuardLevel
}): GuardLevel {
  return (origin.chosen ?? origin.default) === "strict" ||
    origin.floor === "strict"
    ? "strict"
    : "secrets"
}

/**
 * The agents service, keeping one member's settings: a PUT leaves a key it
 * does not name alone, and null clears it. `answer` sees every request first
 * and may take it over, with a Response or a promise of one.
 */
function backend({
  start = settings(),
  answer,
}: {
  start?: AnswerGuardPreferences
  answer?: (request: FakeRequest) => Response | Promise<Response> | undefined
} = {}) {
  const current = structuredClone(start)
  const seen: FakeRequest[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost:3000")
      const request: FakeRequest = {
        method: init?.method ?? "GET",
        path: url.pathname,
        body:
          typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
      }
      seen.push(request)
      const taken = await answer?.(request)
      if (taken) return taken
      if (request.path !== guardPath) {
        return apiError("not_found", "resource not found", 404)
      }
      if (request.method === "PUT") {
        const change = request.body as Record<string, GuardLevel | null>
        for (const origin of ["owner", "peer"] as const) {
          if (origin in change) current[origin].chosen = change[origin]
          current[origin].effective = applies(current[origin])
        }
      }
      return json(current)
    })
  )
  return {
    /** What was asked of the guard's settings, in order. */
    puts: () => seen.filter((request) => request.method === "PUT"),
    reads: () => seen.filter((request) => request.method === "GET"),
    all: () => seen,
  }
}

function newClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
}

function show(ui: ReactElement, client = newClient()) {
  return {
    client,
    ...renderBase(
      <QueryClientProvider client={client}>{ui}</QueryClientProvider>
    ),
  }
}

function showSection(client?: QueryClient) {
  return show(
    <WorkspaceAnswerCheckSettings
      organization={organization}
      preview={false}
    />,
    client
  )
}

const ownerGroup = () => screen.findByRole("radiogroup", { name: ownerTitle })
const peerGroup = () => screen.findByRole("radiogroup", { name: peerTitle })

function radio(group: HTMLElement, name: string) {
  return within(group).getByRole<HTMLInputElement>("radio", { name })
}

/** The names of the radios in a group that are checked. */
function checkedIn(group: HTMLElement): string[] {
  return within(group)
    .getAllByRole("radio")
    .filter((option) => (option as HTMLInputElement).checked)
    .map((option) => option.getAttribute("aria-labelledby") ?? "")
    .map((id) => document.getElementById(id)?.textContent ?? "")
}

/** Waits until the settings query holds an answer, then lets the page render it. */
async function answered(client: QueryClient) {
  await waitFor(() => expect(client.getQueryData(cacheKey)).toBeDefined())
  await waitFor(() => expect(client.isFetching()).toBe(0))
}

beforeEach(resumeSessionAfterSignIn)

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("What your agents share", () => {
  it("shows both rows as labelled radio groups, with what each option hides", async () => {
    backend()
    showSection()

    const section = await screen.findByRole("region", {
      name: "What your agents share",
    })
    expect(
      within(section).getByRole("heading", {
        level: 3,
        name: "What your agents share",
      })
    ).toBeTruthy()
    expect(within(section).getByText(intro)).toBeTruthy()
    expect(screen.getAllByRole("radiogroup")).toHaveLength(2)

    for (const group of [await ownerGroup(), await peerGroup()]) {
      expect(
        within(group).getByText(
          "Passwords, keys, and card, bank and ID numbers."
        )
      ).toBeTruthy()
      expect(
        within(group).getByText("Also emails, phone numbers and addresses.")
      ).toBeTruthy()
      expect(within(group).getAllByRole("radio")).toHaveLength(3)
      expect(radio(group, "Secrets only")).toBeTruthy()
      expect(radio(group, "All personal details")).toBeTruthy()
    }
  })

  it("keeps the two rows' radios apart, and each row's together, so the arrow keys move within a row", async () => {
    backend()
    showSection()
    const owner = await ownerGroup()
    const peer = await peerGroup()

    const names = (group: HTMLElement) =>
      within(group)
        .getAllByRole("radio")
        .map((option) => option.getAttribute("name"))
    expect(new Set(names(owner)).size).toBe(1)
    expect(new Set(names(peer)).size).toBe(1)
    expect(names(owner)[0]).not.toBe(names(peer)[0])
    expect(names(owner)[0]).toBeTruthy()
  })

  it("shows the row's choice, or Default when it has none", async () => {
    backend({
      start: settings({
        owner: {
          chosen: "strict",
          default: "secrets",
          floor: "secrets",
          effective: "strict",
        },
      }),
    })
    showSection()

    expect(checkedIn(await ownerGroup())).toEqual(["All personal details"])
    expect(checkedIn(await peerGroup())).toEqual([
      "Default (all personal details)",
    ])
  })

  it("shows a choice that is the same as the default as the choice, not as Default", async () => {
    backend({
      start: settings({
        owner: {
          chosen: "secrets",
          default: "secrets",
          floor: "secrets",
          effective: "secrets",
        },
      }),
    })
    showSection()

    expect(checkedIn(await ownerGroup())).toEqual(["Secrets only"])
  })

  it("names what Default means in each row, from what Go says the default is", async () => {
    backend()
    showSection()

    expect(radio(await ownerGroup(), "Default (secrets only)")).toBeTruthy()
    expect(
      radio(await peerGroup(), "Default (all personal details)")
    ).toBeTruthy()
    cleanup()

    backend({
      start: settings({
        owner: {
          chosen: null,
          default: "strict",
          floor: "secrets",
          effective: "strict",
        },
        peer: {
          chosen: null,
          default: "secrets",
          floor: "secrets",
          effective: "secrets",
        },
      }),
    })
    showSection()

    expect(
      radio(await ownerGroup(), "Default (all personal details)")
    ).toBeTruthy()
    expect(radio(await peerGroup(), "Default (secrets only)")).toBeTruthy()
  })

  describe("when the organization asks for more", () => {
    const strictFloor = {
      chosen: null,
      default: "strict",
      floor: "strict",
      effective: "strict",
    } as const

    it("disables the looser option in that row, and says why once", async () => {
      backend({ start: settings({ peer: strictFloor }) })
      showSection()
      const peer = await peerGroup()
      const owner = await ownerGroup()

      expect(radio(peer, "Secrets only").disabled).toBe(true)
      expect(radio(peer, "All personal details").disabled).toBe(false)
      expect(radio(peer, "Default (all personal details)").disabled).toBe(false)
      expect(screen.getAllByText(floorLine)).toHaveLength(1)
      expect(within(peer).queryByText(floorLine)).toBeNull()
      // The row is described by the line, so a screen reader says it too.
      const described = peer.getAttribute("aria-describedby")
      expect(document.getElementById(described ?? "")?.textContent).toBe(
        floorLine
      )
      // The other row asks for nothing more.
      expect(
        within(owner)
          .getAllByRole("radio")
          .some((option) => (option as HTMLInputElement).disabled)
      ).toBe(false)
      expect(owner.getAttribute("aria-describedby")).toBeNull()
    })

    it("says it in each row it applies to", async () => {
      backend({
        start: settings({
          owner: { ...strictFloor, default: "secrets" },
          peer: strictFloor,
        }),
      })
      showSection()
      await ownerGroup()

      expect(screen.getAllByText(floorLine)).toHaveLength(2)
      expect(
        (await ownerGroup()).contains(screen.getAllByText(floorLine)[0])
      ).toBe(false)
    })

    it("keeps a choice made below it shown, and lets the member move off it", async () => {
      const service = backend({
        start: settings({
          peer: {
            chosen: "secrets",
            default: "strict",
            floor: "strict",
            effective: "strict",
          },
        }),
      })
      showSection()
      const peer = await peerGroup()

      expect(checkedIn(peer)).toEqual(["Secrets only"])
      expect(radio(peer, "Secrets only").disabled).toBe(true)
      expect(screen.getByText(floorLine)).toBeTruthy()

      fireEvent.click(radio(peer, "Secrets only"))
      expect(service.puts()).toHaveLength(0)

      fireEvent.click(radio(peer, "All personal details"))
      await waitFor(() => expect(service.puts()).toHaveLength(1))
      expect(service.puts()[0].body).toEqual({ peer: "strict" })
      await waitFor(() =>
        expect(checkedIn(peer)).toEqual(["All personal details"])
      )
    })

    it("asks nothing of the member, and shows no line, when the minimum is the least", async () => {
      backend()
      showSection()
      const owner = await ownerGroup()
      const peer = await peerGroup()

      for (const group of [owner, peer]) {
        expect(
          within(group)
            .getAllByRole("radio")
            .some((option) => (option as HTMLInputElement).disabled)
        ).toBe(false)
      }
      expect(screen.queryByText(floorLine)).toBeNull()
    })
  })

  describe("choosing", () => {
    it("saves a pick as only that key, and shows what Go answers", async () => {
      const service = backend()
      showSection()
      const owner = await ownerGroup()
      const peer = await peerGroup()

      fireEvent.click(radio(owner, "All personal details"))

      await waitFor(() => expect(service.puts()).toHaveLength(1))
      expect(service.puts()[0]).toEqual({
        method: "PUT",
        path: guardPath,
        body: { owner: "strict" },
      })
      await waitFor(() => expect(owner.getAttribute("aria-busy")).toBe("false"))
      expect(checkedIn(owner)).toEqual(["All personal details"])
      expect(checkedIn(peer)).toEqual(["Default (all personal details)"])
      expect(screen.queryByRole("alert")).toBeNull()
    })

    it("saves the other row on its own", async () => {
      const service = backend()
      showSection()
      const peer = await peerGroup()

      fireEvent.click(radio(peer, "Secrets only"))

      await waitFor(() => expect(service.puts()).toHaveLength(1))
      expect(service.puts()[0].body).toEqual({ peer: "secrets" })
      await waitFor(() => expect(checkedIn(peer)).toEqual(["Secrets only"]))
      expect(checkedIn(await ownerGroup())).toEqual(["Default (secrets only)"])
    })

    it("clears a choice with null when Default is picked", async () => {
      const service = backend({
        start: settings({
          owner: {
            chosen: "strict",
            default: "secrets",
            floor: "secrets",
            effective: "strict",
          },
        }),
      })
      showSection()
      const owner = await ownerGroup()

      fireEvent.click(radio(owner, "Default (secrets only)"))

      await waitFor(() => expect(service.puts()).toHaveLength(1))
      expect(service.puts()[0].body).toEqual({ owner: null })
      await waitFor(() =>
        expect(checkedIn(owner)).toEqual(["Default (secrets only)"])
      )
    })

    it("shows the pick at once, before Go has answered, and keeps it after", async () => {
      const gate = deferred<Response>()
      const service = backend({
        answer: (request) =>
          request.method === "PUT" ? gate.promise : undefined,
      })
      showSection()
      const owner = await ownerGroup()

      fireEvent.click(radio(owner, "All personal details"))

      expect(checkedIn(owner)).toEqual(["All personal details"])
      await waitFor(() => expect(service.puts()).toHaveLength(1))
      expect(checkedIn(owner)).toEqual(["All personal details"])
      expect(owner.getAttribute("aria-busy")).toBe("true")
      // The radios stay enabled, so the keyboard's focus is not dropped.
      expect(
        within(owner)
          .getAllByRole("radio")
          .some((option) => (option as HTMLInputElement).disabled)
      ).toBe(false)

      gate.resolve(
        json(
          settings({
            owner: {
              chosen: "strict",
              default: "secrets",
              floor: "secrets",
              effective: "strict",
            },
          })
        )
      )

      await waitFor(() => expect(owner.getAttribute("aria-busy")).toBe("false"))
      expect(checkedIn(owner)).toEqual(["All personal details"])
    })

    it("sends one save at a time: a pick made while another is on its way is dropped", async () => {
      const gate = deferred<Response>()
      const service = backend({
        answer: (request) =>
          request.method === "PUT" ? gate.promise : undefined,
      })
      showSection()
      const owner = await ownerGroup()
      const peer = await peerGroup()

      fireEvent.click(radio(owner, "All personal details"))
      fireEvent.click(radio(owner, "Secrets only"))
      fireEvent.click(radio(peer, "Secrets only"))
      await waitFor(() => expect(service.puts()).toHaveLength(1))

      gate.resolve(
        json(
          settings({
            owner: {
              chosen: "strict",
              default: "secrets",
              floor: "secrets",
              effective: "strict",
            },
          })
        )
      )
      await waitFor(() => expect(owner.getAttribute("aria-busy")).toBe("false"))

      expect(service.puts()).toHaveLength(1)
      expect(service.puts()[0].body).toEqual({ owner: "strict" })
      expect(checkedIn(owner)).toEqual(["All personal details"])
      expect(checkedIn(peer)).toEqual(["Default (all personal details)"])
    })

    it("takes the next pick once the last has been saved", async () => {
      const service = backend()
      showSection()
      const owner = await ownerGroup()

      fireEvent.click(radio(owner, "All personal details"))
      await waitFor(() => expect(owner.getAttribute("aria-busy")).toBe("false"))
      fireEvent.click(radio(owner, "Secrets only"))

      await waitFor(() => expect(service.puts()).toHaveLength(2))
      expect(service.puts().map((request) => request.body)).toEqual([
        { owner: "strict" },
        { owner: "secrets" },
      ])
      await waitFor(() => expect(checkedIn(owner)).toEqual(["Secrets only"]))
    })
  })

  describe("when saving fails", () => {
    it("puts the pick back and says so under that row alone", async () => {
      backend({
        answer: (request) =>
          request.method === "PUT"
            ? apiError("internal", "internal server error", 500)
            : undefined,
      })
      showSection()
      const owner = await ownerGroup()
      const peer = await peerGroup()

      fireEvent.click(radio(owner, "All personal details"))

      const alert = await screen.findByRole("alert")
      expect(alert.textContent).toBe("Could not save your choice.")
      expect(alert.parentElement?.contains(owner)).toBe(true)
      expect(alert.parentElement?.contains(peer)).toBe(false)
      expect(checkedIn(owner)).toEqual(["Default (secrets only)"])
      expect(checkedIn(peer)).toEqual(["Default (all personal details)"])
      expect(owner.getAttribute("aria-busy")).toBe("false")
    })

    it("says what Go says when it refuses the pick", async () => {
      backend({
        answer: (request) =>
          request.method === "PUT"
            ? apiError(
                "invalid_request",
                'peer must be "secrets", "strict" or null',
                400
              )
            : undefined,
      })
      showSection()
      const peer = await peerGroup()

      fireEvent.click(radio(peer, "Secrets only"))

      expect((await screen.findByRole("alert")).textContent).toBe(
        'peer must be "secrets", "strict" or null'
      )
      expect(checkedIn(peer)).toEqual(["Default (all personal details)"])
    })

    it("puts the pick back when the answer to it cannot be read", async () => {
      backend({
        answer: (request) =>
          request.method === "PUT"
            ? new Response("{oops", {
                status: 200,
                headers: { "Content-Type": "application/json" },
              })
            : undefined,
      })
      showSection()
      const owner = await ownerGroup()

      fireEvent.click(radio(owner, "All personal details"))

      expect((await screen.findByRole("alert")).textContent).toBe(
        "Could not save your choice."
      )
      expect(checkedIn(owner)).toEqual(["Default (secrets only)"])
    })

    it("can be tried again, and clears the message when it works", async () => {
      let refuse = true
      const service = backend({
        answer: (request) =>
          request.method === "PUT" && refuse
            ? apiError("internal", "internal server error", 500)
            : undefined,
      })
      showSection()
      const owner = await ownerGroup()

      fireEvent.click(radio(owner, "All personal details"))
      await screen.findByRole("alert")

      refuse = false
      fireEvent.click(radio(owner, "All personal details"))

      await waitFor(() => expect(screen.queryByRole("alert")).toBeNull())
      expect(service.puts()).toHaveLength(2)
      await waitFor(() =>
        expect(checkedIn(owner)).toEqual(["All personal details"])
      )
    })

    it("asks the session check to look at a session that has ended, as the workspace already sends a 401 to sign-in", async () => {
      backend({
        answer: (request) =>
          request.path === "/v1/auth/refresh"
            ? new Response(null, { status: 401 })
            : request.method === "PUT"
              ? apiError("unauthorized", "Your session has ended.", 401)
              : undefined,
      })
      const client = newClient()
      client.setQueryData(["auth", "session"], {
        user: { id: "user-1", email: "a@example.com" },
        organizations: [organization],
      })
      showSection(client)
      const owner = await ownerGroup()
      expect(client.getQueryState(["auth", "session"])?.isInvalidated).toBe(
        false
      )

      fireEvent.click(radio(owner, "All personal details"))

      expect((await screen.findByRole("alert")).textContent).toBe(
        "Your session has ended. Sign in again to continue."
      )
      expect(client.getQueryState(["auth", "session"])?.isInvalidated).toBe(
        true
      )
      expect(checkedIn(owner)).toEqual(["Default (secrets only)"])
    })

    it("goes away if the settings turn out not to be served after all", async () => {
      backend({
        answer: (request) =>
          request.method === "PUT"
            ? apiError("not_found", "resource not found", 404)
            : undefined,
      })
      const { container } = showSection()
      const owner = await ownerGroup()

      fireEvent.click(radio(owner, "All personal details"))

      await waitFor(() => expect(container.firstChild).toBeNull())
      expect(screen.queryByRole("alert")).toBeNull()
    })
  })

  describe("when the server does not serve the settings", () => {
    it("shows nothing at all, and no error, for a 404", async () => {
      const service = backend({
        answer: () => apiError("not_found", "resource not found", 404),
      })
      const { container, client } = showSection()

      await answered(client)

      expect(service.reads()).toHaveLength(1)
      expect(container.firstChild).toBeNull()
      expect(screen.queryByRole("alert")).toBeNull()
      expect(screen.queryByText("What your agents share")).toBeNull()
    })

    it("shows nothing for a server that keeps no settings", async () => {
      backend({
        answer: () =>
          apiError(
            "answer_guard_unavailable",
            "The answer guard's settings are not kept on this server.",
            503
          ),
      })
      const { container, client } = showSection()

      await answered(client)

      expect(container.firstChild).toBeNull()
      expect(screen.queryByRole("alert")).toBeNull()
    })

    it("shows nothing while it is still asking, so a section that is not served never flashes", async () => {
      const gate = deferred<Response>()
      const service = backend({ answer: () => gate.promise })
      const { container } = showSection()

      await waitFor(() => expect(service.reads()).toHaveLength(1))

      expect(container.firstChild).toBeNull()
      gate.resolve(json(settings()))
      expect(
        await screen.findByRole("radiogroup", { name: ownerTitle })
      ).toBeTruthy()
    })

    it("asks nothing, and shows nothing, in the layout preview or without an organization", () => {
      const service = backend()

      const preview = show(
        <WorkspaceAnswerCheckSettings organization={organization} preview />
      )
      expect(preview.container.firstChild).toBeNull()
      cleanup()
      const none = show(<WorkspaceAnswerCheckSettings preview={false} />)
      expect(none.container.firstChild).toBeNull()

      expect(service.all()).toHaveLength(0)
    })
  })

  describe("when the settings cannot be loaded", () => {
    it("says so, with Retry, and shows the rows once it works", async () => {
      let failing = true
      backend({
        answer: () =>
          failing
            ? apiError("internal", "internal server error", 500)
            : undefined,
      })
      showSection()

      const alert = await screen.findByRole("alert")
      expect(alert.textContent).toContain("Could not load these settings.")
      expect(
        screen.getByRole("heading", { name: "What your agents share" })
      ).toBeTruthy()
      expect(screen.queryAllByRole("radiogroup")).toHaveLength(0)

      failing = false
      fireEvent.click(within(alert).getByRole("button", { name: "Retry" }))

      expect(await ownerGroup()).toBeTruthy()
      expect(screen.queryByRole("alert")).toBeNull()
    })

    it("shows an error, not a broken page, for a value it does not know", async () => {
      const consoleError = vi
        .spyOn(console, "error")
        .mockImplementation(() => {})
      backend({
        answer: (request) =>
          request.method === "GET"
            ? json({ ...settings(), relevance: "maybe" })
            : undefined,
      })
      showSection()

      const alert = await screen.findByRole("alert")

      expect(alert.textContent).toContain("Could not load these settings.")
      expect(screen.queryAllByRole("radiogroup")).toHaveLength(0)
      expect(consoleError).not.toHaveBeenCalled()
    })

    it("shows an error for a level it does not know, and for an answer that is not JSON", async () => {
      backend({
        answer: () =>
          json({
            ...settings(),
            owner: { ...settings().owner, effective: "lenient" },
          }),
      })
      showSection()
      expect((await screen.findByRole("alert")).textContent).toContain(
        "Could not load these settings."
      )
      cleanup()

      backend({
        answer: () =>
          new Response("<html></html>", {
            status: 200,
            headers: { "Content-Type": "text/html" },
          }),
      })
      showSection()
      expect((await screen.findByRole("alert")).textContent).toContain(
        "Could not load these settings."
      )
    })

    it("asks the session check to look at a session that has ended", async () => {
      backend({
        answer: (request) =>
          request.path === "/v1/auth/refresh"
            ? new Response(null, { status: 401 })
            : apiError("unauthorized", "Your session has ended.", 401),
      })
      const client = newClient()
      client.setQueryData(["auth", "session"], {
        user: { id: "user-1", email: "a@example.com" },
        organizations: [organization],
      })

      showSection(client)

      expect((await screen.findByRole("alert")).textContent).toContain(
        "Your session has ended."
      )
      await waitFor(() =>
        expect(client.getQueryState(["auth", "session"])?.isInvalidated).toBe(
          true
        )
      )
    })
  })

  describe("the lines under the rows", () => {
    it.each([
      [true, "hold"],
      [true, "shadow"],
      [true, "off"],
      [false, "hold"],
      [false, "shadow"],
      [false, "off"],
    ] as const)(
      "says each line only when it applies: regulated %s, relevance %s",
      async (regulated, relevance) => {
        backend({ start: settings({ regulated, relevance }) })
        showSection()
        await ownerGroup()

        expect(screen.queryByText(regulatedLine) !== null).toBe(regulated)
        expect(screen.queryByText(holdLine) !== null).toBe(relevance === "hold")
      }
    )
  })
})
