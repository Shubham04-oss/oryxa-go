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
import type { Role, Session } from "@/lib/auth/client"
import type {
  OAuthClient,
  OryxaIntegration,
  SigninManifest,
} from "@/lib/apps/client"
import { WorkspaceSettings } from "./workspace-settings"
import { WorkspaceSigninAppsSettings } from "./workspace-signin-apps-settings"

const organization = { id: "org-1", name: "Oryxa", slug: "oryxa" }
const org = "/v1/organizations/org-1"
const redirectUri = "https://app.oryxa.in/v1/apps/oauth/callback"
const secret = "client-s3cret-value-9876"

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

const oauth = (client: string) => ({ kind: "oauth2_code", client, fields: [] })

const manifests: Record<string, SigninManifest> = {
  github: {
    toolkit: "github",
    name: "GitHub",
    version: 1,
    redirect_uri: redirectUri,
    methods: [
      {
        kind: "oauth2_code",
        client: "github",
        fields: [
          {
            name: "enterprise_host",
            label: "Enterprise host",
            help: "Only for GitHub Enterprise Server.",
            secret: false,
            level: "client",
            validation: { required: false, format: "hostname" },
          },
        ],
      },
      {
        kind: "api_token",
        fields: [
          {
            name: "token",
            label: "Token",
            secret: true,
            level: "connection",
            standard: true,
            validation: { required: true },
          },
        ],
      },
    ],
  },
  gmail: {
    toolkit: "gmail",
    name: "Gmail",
    version: 1,
    redirect_uri: redirectUri,
    methods: [oauth("google")],
  },
  googledrive: {
    toolkit: "googledrive",
    name: "Google Drive",
    version: 1,
    redirect_uri: redirectUri,
    methods: [oauth("google")],
  },
  notion: {
    toolkit: "notion",
    name: "Notion",
    version: 1,
    redirect_uri: redirectUri,
    methods: [oauth("notion")],
    webhook: {
      scope: "deployment",
      organization_endpoint: true,
      organization_secret: false,
      verification_token: true,
    },
  },
  slack: {
    toolkit: "slack",
    name: "Slack",
    version: 1,
    redirect_uri: redirectUri,
    methods: [oauth("slack")],
    webhook: {
      scope: "deployment",
      organization_endpoint: true,
      organization_secret: true,
      verification_token: false,
    },
  },
}

function view(
  overrides: Partial<OAuthClient> & { client: string }
): OAuthClient {
  return {
    saved: false,
    secret_set: false,
    fields: {},
    secret_fields: {},
    refuse_default: false,
    oryxa_default: true,
    redirect_uri: redirectUri,
    toolkits: [overrides.client],
    webhooks: [],
    ...overrides,
  }
}

type FakeRequest = { method: string; path: string; body: unknown }

/**
 * apps, with Oryxa's own sign-in switched on for `toolkits`, and each
 * organization OAuth client in `clients`. `answer` sees every request first.
 */
function backend({
  toolkits = Object.keys(manifests),
  clients = {},
  answer,
}: {
  toolkits?: string[]
  clients?: Record<string, OAuthClient>
  answer?: (request: FakeRequest) => Response | undefined
} = {}) {
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
      const answered = answer?.(request)
      if (answered) return answered
      if (request.path === "/v1/apps/integrations") {
        const integrations: OryxaIntegration[] = toolkits.map((toolkit) => ({
          toolkit,
          name: manifests[toolkit].name,
          kinds: manifests[toolkit].methods.map((method) => method.kind),
        }))
        return json({ integrations })
      }
      const manifest = /^\/v1\/apps\/integrations\/([^/]+)\/manifest$/.exec(
        request.path
      )?.[1]
      if (manifest) return json(manifests[manifest])
      const client = new RegExp(`^${org}/apps/oauth-clients/([^/]+)$`).exec(
        request.path
      )?.[1]
      if (client && request.method === "GET") {
        const toolkitsOf = toolkits.filter((toolkit) =>
          manifests[toolkit].methods.some((method) => method.client === client)
        )
        return json(clients[client] ?? view({ client, toolkits: toolkitsOf }))
      }
      return apiError("not_found", "resource not found", 404)
    })
  )
  return requests
}

function showSettings(role: Role) {
  const session: Session = {
    user: { id: "user-1", email: "admin@oryxa.test" },
    organizations: [organization],
  }
  return render(
    <WorkspaceSettings
      identity={{ id: "user-1", name: "Avery", initials: "A", kind: "user" }}
      session={session}
      selectedOrganization={organization}
      organizationRole={role}
      preview={false}
      onSignOut={() => {}}
      signingOut={false}
      signOutError=""
      onOpenTools={() => {}}
      initialPage="email"
    />
  )
}

function showPage(role: Role = "admin") {
  return render(
    <WorkspaceSigninAppsSettings
      organization={organization}
      role={role}
      preview={false}
    />
  )
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("Settings → Sign-in apps", () => {
  it("is offered to an organization's admins alone, and only once Oryxa's own sign-in is on", async () => {
    let requests = backend()
    showSettings("member")
    expect(
      screen.getByRole("heading", { name: "Email", level: 2 })
    ).toBeTruthy()
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(screen.queryByRole("button", { name: "Sign-in apps" })).toBeNull()
    // A member's settings never even ask.
    expect(
      requests.some((request) => request.path === "/v1/apps/integrations")
    ).toBe(false)
    cleanup()

    requests = backend({ toolkits: [] })
    showSettings("owner")
    await waitFor(() =>
      expect(
        requests.some((request) => request.path === "/v1/apps/integrations")
      ).toBe(true)
    )
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(screen.queryByRole("button", { name: "Sign-in apps" })).toBeNull()
    cleanup()

    backend()
    showSettings("admin")
    fireEvent.click(await screen.findByRole("button", { name: "Sign-in apps" }))
    expect(
      screen.getByRole("heading", { name: "Sign-in apps", level: 2 })
    ).toBeTruthy()
    expect(
      await screen.findByRole("form", { name: "GitHub sign-in app" })
    ).toBeTruthy()
  })

  it("tells a member who reaches the page it is for admins, and reads nothing", async () => {
    const requests = backend()
    showPage("member")
    expect(
      screen.getByText("Organisation owners and admins manage sign-in apps.")
    ).toBeTruthy()
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(requests).toHaveLength(0)
  })

  it("shows one card per OAuth client, with what to copy into the app and only a hint of the secret", async () => {
    const writeText = vi.fn(async () => undefined)
    vi.stubGlobal("navigator", { clipboard: { writeText } })
    backend({
      clients: {
        github: view({
          client: "github",
          saved: true,
          client_id: "Iv1.abc",
          secret_set: true,
          secret_hint: "9876",
          fields: { enterprise_host: "github.acme.test" },
        }),
        google: view({
          client: "google",
          oryxa_default: false,
          toolkits: ["gmail", "googledrive"],
        }),
      },
    })
    showPage()

    const github = await screen.findByRole("form", {
      name: "GitHub sign-in app",
    })
    expect(
      (await screen.findAllByRole("form")).map((form) =>
        form.getAttribute("aria-label")
      )
    ).toEqual([
      "GitHub sign-in app",
      "Google sign-in app",
      "Notion sign-in app",
      "Slack sign-in app",
    ])
    expect(
      within(github).getByText(
        "Used by GitHub. Members sign in through your organisation's own GitHub app."
      )
    ).toBeTruthy()
    expect(
      within(github).getByLabelText<HTMLInputElement>("Client ID").value
    ).toBe("Iv1.abc")
    const secretInput =
      within(github).getByLabelText<HTMLInputElement>("Client secret")
    expect(secretInput.type).toBe("password")
    expect(secretInput.value).toBe("")
    expect(
      within(github).getByText(
        "Saved, ending in 9876. Leave it blank to keep it."
      )
    ).toBeTruthy()
    expect(
      within(github).getByLabelText<HTMLInputElement>(
        "Enterprise host (optional)"
      ).value
    ).toBe("github.acme.test")
    expect(
      within(github).getByLabelText<HTMLInputElement>("Redirect URI").value
    ).toBe(redirectUri)
    fireEvent.click(
      within(github).getByRole("button", { name: "Copy Redirect URI" })
    )
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(redirectUri))
    expect(within(github).getByText("Copied")).toBeTruthy()
    expect(
      within(github)
        .getByRole("switch", { name: "Don't use Oryxa's GitHub app" })
        .getAttribute("aria-checked")
    ).toBe("false")

    const google = screen.getByRole("form", { name: "Google sign-in app" })
    expect(
      within(google).getByText(
        "Used by Gmail and Google Drive. Oryxa has no Google app here. Add your own for members to sign in."
      )
    ).toBeTruthy()
    expect(within(google).queryByRole("button", { name: "Remove" })).toBeNull()
    // Refusing an app Oryxa doesn't have would change nothing.
    expect(within(google).queryByRole("switch")).toBeNull()
  })

  it("saves a new secret and the switch write-only, wipes the secret, and puts a refusal under its field", async () => {
    let saves = 0
    const requests = backend({
      clients: {
        github: view({
          client: "github",
          saved: true,
          client_id: "Iv1.abc",
          secret_set: true,
          secret_hint: "9876",
        }),
      },
      answer: ({ method, path }) => {
        if (method !== "PUT" || path !== `${org}/apps/oauth-clients/github`)
          return undefined
        saves += 1
        return saves === 1
          ? json(
              view({
                client: "github",
                saved: true,
                client_id: "Iv1.abc",
                secret_set: true,
                secret_hint: "9876",
                refuse_default: true,
                updated_at: "2026-09-28T12:00:00Z",
              })
            )
          : apiError(
              "invalid_credentials",
              "client_secret must be one line of text",
              400,
              "client_secret"
            )
      },
    })
    showPage()
    const github = await screen.findByRole("form", {
      name: "GitHub sign-in app",
    })
    const save = within(github).getByRole("button", { name: "Save" })
    expect(save.hasAttribute("disabled")).toBe(true)

    fireEvent.change(
      within(github).getByLabelText<HTMLInputElement>("Client secret"),
      {
        target: { value: secret },
      }
    )
    fireEvent.click(
      within(github).getByRole("switch", {
        name: "Don't use Oryxa's GitHub app",
      })
    )
    fireEvent.click(within(github).getByRole("button", { name: "Save" }))

    expect(await within(github).findByText("Saved.")).toBeTruthy()
    expect(requests.find((request) => request.method === "PUT")?.body).toEqual({
      client_id: "Iv1.abc",
      client_secret: secret,
      refuse_default: true,
    })
    const saved = screen.getByRole("form", { name: "GitHub sign-in app" })
    expect(
      within(saved).getByLabelText<HTMLInputElement>("Client secret").value
    ).toBe("")
    expect(
      within(saved)
        .getByRole("switch", { name: "Don't use Oryxa's GitHub app" })
        .getAttribute("aria-checked")
    ).toBe("true")
    expect(document.body.innerHTML).not.toContain(secret)

    const secretInput =
      within(saved).getByLabelText<HTMLInputElement>("Client secret")
    fireEvent.change(secretInput, { target: { value: secret } })
    fireEvent.click(within(saved).getByRole("button", { name: "Save" }))
    const problem = await within(saved).findByText(
      "Client secret must be one line of text."
    )
    expect(secretInput.getAttribute("aria-describedby")).toContain(problem.id)
    expect(secretInput.value).toBe("")
    await waitFor(() => expect(document.activeElement).toBe(secretInput))
  })

  it("asks for a client's secret with a new client ID, and checks fields before saving", async () => {
    const requests = backend()
    showPage()
    const github = await screen.findByRole("form", {
      name: "GitHub sign-in app",
    })
    fireEvent.change(
      within(github).getByLabelText<HTMLInputElement>("Client ID"),
      {
        target: { value: "Iv1.new" },
      }
    )
    fireEvent.change(
      within(github).getByLabelText<HTMLInputElement>(
        "Enterprise host (optional)"
      ),
      {
        target: { value: "localhost" },
      }
    )
    fireEvent.click(within(github).getByRole("button", { name: "Save" }))

    expect(
      await within(github).findByText("Client secret is required.")
    ).toBeTruthy()
    expect(
      within(github).getByText("Enterprise host must be a hostname.")
    ).toBeTruthy()
    expect(requests.some((request) => request.method === "PUT")).toBe(false)
  })

  it("takes Slack's signing secret for its events and shows the URL to paste back", async () => {
    const signing = "slack-signing-secret-0123456789"
    const requests = backend({
      clients: {
        slack: view({
          client: "slack",
          saved: true,
          client_id: "123.456",
          secret_set: true,
          webhooks: [
            { toolkit: "slack", secret_required: true, secret_set: false },
          ],
        }),
      },
      answer: ({ method, path }) =>
        method === "PUT" && path === `${org}/apps/oauth-clients/slack`
          ? json(
              view({
                client: "slack",
                saved: true,
                client_id: "123.456",
                secret_set: true,
                updated_at: "2026-09-28T12:00:00Z",
                webhooks: [
                  {
                    toolkit: "slack",
                    url: "https://api.oryxa.in/v1/apps/hooks/ep_1",
                    secret_required: true,
                    secret_set: true,
                  },
                ],
              })
            )
          : undefined,
    })
    showPage()
    const slack = await screen.findByRole("form", { name: "Slack sign-in app" })
    expect(
      within(slack).getByText(
        "Save your Slack app's signing secret to get the URL to paste into Slack."
      )
    ).toBeTruthy()
    const input = within(slack).getByLabelText<HTMLInputElement>(
      "Slack signing secret"
    )
    expect(input.type).toBe("password")

    fireEvent.change(input, { target: { value: "too-short" } })
    fireEvent.click(within(slack).getByRole("button", { name: "Save" }))
    expect(
      await within(slack).findByText(
        "Slack signing secret must be 16 to 1024 characters."
      )
    ).toBeTruthy()
    expect(requests.some((request) => request.method === "PUT")).toBe(false)

    fireEvent.change(input, { target: { value: signing } })
    fireEvent.click(within(slack).getByRole("button", { name: "Save" }))
    const saved = await screen.findByRole("form", { name: "Slack sign-in app" })
    await waitFor(() =>
      expect(
        within(saved).getByLabelText<HTMLInputElement>("Slack webhook URL")
          .value
      ).toBe("https://api.oryxa.in/v1/apps/hooks/ep_1")
    )
    expect(requests.find((request) => request.method === "PUT")?.body).toEqual({
      client_id: "123.456",
      refuse_default: false,
      webhook_secrets: { slack: signing },
    })
    expect(document.body.innerHTML).not.toContain(signing)
  })

  it("reveals Notion's verification token once, behind a confirm step", async () => {
    const token = "notion-verification-token-1"
    let state: "ready" | "revealed" = "ready"
    const notion = () =>
      view({
        client: "notion",
        saved: true,
        client_id: "notion-client",
        secret_set: true,
        webhooks: [
          {
            toolkit: "notion",
            url: "https://api.oryxa.in/v1/apps/hooks/ep_2",
            secret_required: false,
            secret_set: true,
            verification_token: state,
          },
        ],
      })
    const requests = backend({
      answer: ({ method, path }) => {
        if (path === `${org}/apps/oauth-clients/notion` && method === "GET")
          return json(notion())
        if (
          path === `${org}/apps/webhooks/notion/verification-token` &&
          method === "POST"
        ) {
          if (state === "revealed")
            return apiError("conflict", "the token was already revealed", 409)
          state = "revealed"
          return json({ toolkit: "notion", verification_token: token })
        }
        return undefined
      },
    })
    showPage()
    const form = await screen.findByRole("form", { name: "Notion sign-in app" })
    expect(
      within(form).getByText("Notion sent its verification token.")
    ).toBeTruthy()

    fireEvent.click(within(form).getByRole("button", { name: "Reveal token" }))
    expect(
      within(form).getByText(
        "Notion's verification token is shown only once. Have your Notion app's webhook settings open to paste it straight in."
      )
    ).toBeTruthy()
    expect(requests.some((request) => request.method === "POST")).toBe(false)
    fireEvent.click(within(form).getByRole("button", { name: "Reveal once" }))

    const shown = await within(form).findByLabelText<HTMLInputElement>(
      "Notion verification token"
    )
    expect(shown.value).toBe(token)
    expect(
      within(form).getByText(
        "Shown once. Paste it into Notion now: it can't be shown again."
      )
    ).toBeTruthy()
    fireEvent.click(within(form).getByRole("button", { name: "Done" }))
    expect(
      within(form).queryByLabelText("Notion verification token")
    ).toBeNull()
    expect(
      await within(form).findByText(
        "Notion's verification token was revealed. It's shown only once."
      )
    ).toBeTruthy()
    expect(document.body.innerHTML).not.toContain(token)
    expect(
      requests.filter(
        (request) =>
          request.method === "POST" &&
          request.path === `${org}/apps/webhooks/notion/verification-token`
      )
    ).toHaveLength(1)
  })

  it("says so when apps has already revealed the token (409)", async () => {
    backend({
      clients: {
        notion: view({
          client: "notion",
          saved: true,
          client_id: "notion-client",
          webhooks: [
            {
              toolkit: "notion",
              url: "https://api.oryxa.in/v1/apps/hooks/ep_2",
              secret_required: false,
              secret_set: true,
              verification_token: "ready",
            },
          ],
        }),
      },
      answer: ({ method, path }) =>
        method === "POST" &&
        path === `${org}/apps/webhooks/notion/verification-token`
          ? apiError("conflict", "the token was already revealed", 409)
          : undefined,
    })
    showPage()
    const form = await screen.findByRole("form", { name: "Notion sign-in app" })
    fireEvent.click(within(form).getByRole("button", { name: "Reveal token" }))
    fireEvent.click(within(form).getByRole("button", { name: "Reveal once" }))
    expect((await within(form).findByRole("alert")).textContent).toBe(
      "Notion's verification token was already revealed. It's shown only once."
    )
    expect(
      within(form).queryByLabelText("Notion verification token")
    ).toBeNull()
  })

  it("removes an organization's own app only after confirming", async () => {
    let removed = false
    const requests = backend({
      answer: ({ method, path }) => {
        if (path !== `${org}/apps/oauth-clients/github`) return undefined
        if (method === "DELETE") {
          removed = true
          return new Response(null, { status: 204 })
        }
        return json(
          removed
            ? view({ client: "github" })
            : view({
                client: "github",
                saved: true,
                client_id: "Iv1.abc",
                secret_set: true,
              })
        )
      },
    })
    showPage()
    const github = await screen.findByRole("form", {
      name: "GitHub sign-in app",
    })
    fireEvent.click(within(github).getByRole("button", { name: "Remove" }))
    expect(
      within(github).getByText(
        "Remove your organisation's GitHub app? Members' new sign-ins go through Oryxa's app again. Connections made through yours keep working until they're disconnected."
      )
    ).toBeTruthy()
    expect(requests.some((request) => request.method === "DELETE")).toBe(false)
    fireEvent.click(within(github).getByRole("button", { name: "Remove app" }))

    expect(
      await screen.findByText(
        "Removed. Members sign in through Oryxa's GitHub app again."
      )
    ).toBeTruthy()
    const after = screen.getByRole("form", { name: "GitHub sign-in app" })
    expect(
      within(after).getByLabelText<HTMLInputElement>("Client ID").value
    ).toBe("")
    expect(within(after).queryByRole("button", { name: "Remove" })).toBeNull()
  })
})
