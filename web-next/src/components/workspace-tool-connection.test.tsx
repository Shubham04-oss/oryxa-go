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
import type { ReactElement } from "react"
import type { Connection, SigninManifest } from "@/lib/apps/client"
import { WorkspaceTools } from "./workspace-tools"
import { previewToolkits } from "./workspace-tools-data"

// Tools → Connect and an Oryxa connection's sign-in, through the page that
// shows them (`workspace-tools.tsx`), against a stand-in apps.

const org = "/v1/organizations/org-1"
const secret = "ghp_s3cretValue1234"

function render(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return renderBase(
    <QueryClientProvider client={client}>{ui}</QueryClientProvider>
  )
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function apiError(
  code: string,
  message: string,
  status: number,
  field?: string
) {
  return json({ error: { code, message, field } }, status)
}

function connection(overrides: Partial<Connection> = {}): Connection {
  return {
    id: "apc_1",
    toolkit: "github",
    broker: "composio",
    status: "active",
    member_id: "user-1",
    auto_subscribe: true,
    created_at: "2026-09-28T10:00:00Z",
    updated_at: "2026-09-28T10:00:00Z",
    ...overrides,
  }
}

const tokenMethod = {
  kind: "api_token",
  fields: [
    {
      name: "token",
      label: "Token",
      secret: true,
      level: "connection",
      standard: true,
      validation: { required: true, max_length: 4096 },
    },
  ],
}

function githubManifest(methods: SigninManifest["methods"]): SigninManifest {
  return {
    toolkit: "github",
    name: "GitHub",
    version: 1,
    methods,
    redirect_uri: "http://localhost:3000/v1/apps/oauth/callback",
  }
}

type FakeRequest = { method: string; path: string; body: unknown }
type Handler = (request: FakeRequest) => Response | undefined

function fakeFetch(...handlers: Handler[]) {
  const requests: FakeRequest[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost:3000")
      const request = {
        method: init?.method ?? "GET",
        path: url.pathname,
        body:
          typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
      }
      requests.push(request)
      for (const handler of handlers) {
        const response = handler(request)
        if (response) return response
      }
      return apiError("not_found", "resource not found", 404)
    })
  )
  return requests
}

/** apps' catalog: the probe, and each featured toolkit, connectable. */
function catalog(): Handler {
  return ({ method, path }) => {
    if (method !== "GET") return undefined
    if (path === "/v1/apps/catalog")
      return json({ toolkits: [previewToolkits[0]], next_cursor: null })
    const slug = /^\/v1\/apps\/catalog\/([^/]+)$/.exec(path)?.[1]
    const toolkit = previewToolkits.find((item) => item.toolkit === slug)
    return toolkit ? json({ ...toolkit, connectable: true }) : undefined
  }
}

function connections(list: Connection[]): Handler {
  return ({ method, path }) =>
    method === "GET" && path === `${org}/apps/connections`
      ? json({ connections: list })
      : undefined
}

/** Oryxa's own sign-in, switched on for GitHub with `manifest`. */
function oryxa(manifest: SigninManifest): Handler {
  return ({ method, path }) => {
    if (method !== "GET") return undefined
    if (path === "/v1/apps/integrations")
      return json({
        integrations: [
          {
            toolkit: manifest.toolkit,
            name: manifest.name,
            kinds: manifest.methods.map((way) => way.kind),
          },
        ],
      })
    if (path === `/v1/apps/integrations/${manifest.toolkit}/manifest`)
      return json(manifest)
    return undefined
  }
}

function stubLocation(href: string) {
  const url = new URL(href)
  const assign = vi.fn()
  vi.stubGlobal("location", {
    assign,
    hash: url.hash,
    href: url.href,
    origin: url.origin,
    pathname: url.pathname,
    search: url.search,
  })
  return assign
}

async function openCard(name: string) {
  fireEvent.click(
    await screen.findByRole("button", { name: `${name} details` })
  )
  return screen.getByRole("button", { name: `Connect ${name}` })
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("Tools → Connect", () => {
  it("signs in to a toolkit Oryxa's sign-in lists through Oryxa, straight to the app", async () => {
    const assign = stubLocation("http://localhost:3000/?view=tools")
    const requests = fakeFetch(
      catalog(),
      connections([]),
      oryxa(
        githubManifest([{ kind: "oauth2_code", client: "github", fields: [] }])
      ),
      ({ method, path }) =>
        method === "POST" && path === `${org}/apps/connections`
          ? json(
              {
                connection: connection({ broker: "oryxa", status: "pending" }),
                redirect_url:
                  "https://github.com/login/oauth/authorize?client_id=Iv1&state=s",
              },
              201
            )
          : undefined
    )
    render(<WorkspaceTools organizationId="org-1" />)

    fireEvent.click(await openCard("GitHub"))
    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith(
        "https://github.com/login/oauth/authorize?client_id=Iv1&state=s"
      )
    )
    expect(requests.find((request) => request.method === "POST")?.body).toEqual(
      {
        broker: "oryxa",
        kind: "oauth2_code",
        return_to: "http://localhost:3000/?view=tools&org=org-1",
        toolkit: "github",
      }
    )
    expect(screen.queryByRole("dialog")).toBeNull()
  })

  it("leaves every other toolkit on Composio, with the request it always sent", async () => {
    const assign = stubLocation("http://localhost:3000/?view=tools")
    const requests = fakeFetch(
      catalog(),
      connections([]),
      oryxa(
        githubManifest([{ kind: "oauth2_code", client: "github", fields: [] }])
      ),
      ({ method, path }) =>
        method === "POST" && path === `${org}/apps/connections`
          ? json(
              {
                connection: connection({ toolkit: "gmail", status: "pending" }),
                redirect_url: "https://connect.composio.dev/link/lk_1",
              },
              201
            )
          : undefined
    )
    render(<WorkspaceTools organizationId="org-1" />)

    fireEvent.click(await openCard("Gmail"))
    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith(
        "https://connect.composio.dev/link/lk_1"
      )
    )
    expect(requests.find((request) => request.method === "POST")?.body).toEqual(
      {
        return_to: "http://localhost:3000/?view=tools&org=org-1",
        toolkit: "gmail",
      }
    )
  })

  it("asks for a key in the credential dialog, connects at once, and returns as a callback would", async () => {
    const assign = stubLocation("http://localhost:3000/?view=tools")
    const requests = fakeFetch(
      catalog(),
      connections([]),
      oryxa(
        githubManifest([
          { kind: "oauth2_code", client: "github", fields: [] },
          tokenMethod,
        ])
      ),
      ({ method, path }) =>
        method === "POST" && path === `${org}/apps/connections`
          ? json(
              {
                connection: connection({
                  id: "apc_key",
                  broker: "oryxa",
                  credential_kind: "api_token",
                  label: "octocat",
                }),
              },
              201
            )
          : undefined
    )
    render(<WorkspaceTools organizationId="org-1" />)

    fireEvent.click(await openCard("GitHub"))
    const dialog = await screen.findByRole("dialog", { name: "Connect GitHub" })
    fireEvent.click(within(dialog).getByRole("radio", { name: "API token" }))
    const token = within(dialog).getByLabelText<HTMLInputElement>("Token *")
    expect(token.type).toBe("password")
    fireEvent.change(token, { target: { value: secret } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Connect" }))

    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith(
        "http://localhost:3000/?view=tools&org=org-1&app_connection=apc_key&status=active"
      )
    )
    expect(requests.find((request) => request.method === "POST")?.body).toEqual(
      {
        broker: "oryxa",
        fields: { token: secret },
        kind: "api_token",
        return_to: "http://localhost:3000/?view=tools&org=org-1",
        toolkit: "github",
      }
    )
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
    expect(document.body.innerHTML).not.toContain(secret)
  })

  it("keeps the dialog open with the refusal under the field, and says so when it is closed", async () => {
    const assign = stubLocation("http://localhost:3000/?view=tools")
    const requests = fakeFetch(
      catalog(),
      connections([]),
      oryxa(githubManifest([tokenMethod])),
      ({ method, path }) =>
        method === "POST" && path === `${org}/apps/connections`
          ? apiError(
              "invalid_credentials",
              "token must be at most 4096 characters",
              400,
              "token"
            )
          : undefined
    )
    render(<WorkspaceTools organizationId="org-1" />)

    const connectButton = await openCard("GitHub")
    connectButton.focus()
    fireEvent.click(connectButton)
    const dialog = await screen.findByRole("dialog", { name: "Connect GitHub" })
    const token = within(dialog).getByLabelText<HTMLInputElement>("Token *")
    fireEvent.change(token, { target: { value: secret } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Connect" }))

    const problem = await within(dialog).findByText(
      "Token must be at most 4096 characters."
    )
    expect(token.getAttribute("aria-describedby")).toBe(problem.id)
    expect(token.value).toBe("")
    expect(
      requests.filter((request) => request.method === "POST")
    ).toHaveLength(1)

    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }))
    expect(
      await screen.findByText(
        "You closed GitHub's sign-in, so it isn't connected."
      )
    ).toBeTruthy()
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
    expect(assign).not.toHaveBeenCalled()
    const again = screen.getByRole("button", { name: "Connect GitHub" })
    expect(again.hasAttribute("disabled")).toBe(false)
    // Back on what opened the dialog.
    await waitFor(() => expect(document.activeElement).toBe(again))
  })
})

describe("An Oryxa connection's sign-in", () => {
  it("says how it signs in, offers none of Composio's panes, and replaces its key through the same dialog", async () => {
    let attempts = 0
    const requests = fakeFetch(
      catalog(),
      connections([
        connection({
          broker: "oryxa",
          credential_kind: "api_token",
          label: "octocat",
        }),
      ]),
      oryxa(
        githubManifest([
          { kind: "oauth2_code", client: "github", fields: [] },
          tokenMethod,
        ])
      ),
      ({ method, path }) => {
        if (
          method !== "PUT" ||
          path !== `${org}/apps/connections/apc_1/credentials`
        )
          return undefined
        attempts += 1
        return attempts === 1
          ? apiError(
              "invalid_credentials",
              "token was refused by the app",
              400,
              "token"
            )
          : json({
              connection: connection({
                broker: "oryxa",
                credential_kind: "api_token",
                label: "octocat-2",
              }),
            })
      }
    )
    render(<WorkspaceTools organizationId="org-1" />)

    fireEvent.click(
      await screen.findByRole("button", { name: "Expand GitHub connection" })
    )
    expect(
      screen.getByText(
        "Connected with an API token through Oryxa's own sign-in."
      )
    ).toBeTruthy()
    expect(screen.queryByRole("tab", { name: "Events" })).toBeNull()
    expect(
      screen.getByText(
        "Events, indexing and runs aren't available yet for apps connected through Oryxa's own sign-in."
      )
    ).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "Replace key" }))
    const dialog = await screen.findByRole("dialog", {
      name: "Replace your GitHub key",
    })
    const token =
      await within(dialog).findByLabelText<HTMLInputElement>("Token *")
    fireEvent.change(token, { target: { value: secret } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Replace key" }))
    expect(
      await within(dialog).findByText("Token was refused by the app.")
    ).toBeTruthy()
    expect(token.value).toBe("")

    fireEvent.change(token, { target: { value: `${secret}-new` } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Replace key" }))
    expect(
      await screen.findByText("Key replaced. GitHub accepted the new one.")
    ).toBeTruthy()
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
    expect(
      requests
        .filter((request) => request.method === "PUT")
        .map((request) => request.body)
    ).toEqual([
      { fields: { token: secret }, kind: "api_token" },
      { fields: { token: `${secret}-new` }, kind: "api_token" },
    ])
    expect(document.body.innerHTML).not.toContain(secret)
  })

  it("offers no key to replace for an OAuth sign-in", async () => {
    fakeFetch(
      catalog(),
      connections([
        connection({ broker: "oryxa", credential_kind: "oauth2_code" }),
      ])
    )
    render(<WorkspaceTools organizationId="org-1" />)

    fireEvent.click(
      await screen.findByRole("button", { name: "Expand GitHub connection" })
    )
    expect(
      screen.getByText("Signed in with GitHub through Oryxa's own sign-in.")
    ).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Replace key" })).toBeNull()
  })
})
