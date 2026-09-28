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
import { AppsFieldError } from "@/lib/apps/client"
import type { SigninMethod } from "@/lib/apps/client"
import { CredentialDialog } from "./workspace-tool-credential-dialog"

afterEach(cleanup)

const secret = "ghp_s3cretValue1234"

/** A manifest method as apps answers it: standard fields first, then the manifest's own. */
const zendeskToken: SigninMethod = {
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
    {
      name: "subdomain",
      label: "Zendesk subdomain",
      help: "The part before .zendesk.com.",
      secret: false,
      level: "connection",
      validation: { required: true, pattern: "[a-z0-9][a-z0-9-]{0,62}" },
    },
    {
      name: "region",
      label: "Region",
      secret: false,
      level: "connection",
      validation: { required: true, enum: ["us", "eu"] },
    },
    {
      name: "audience",
      label: "API audience",
      secret: false,
      level: "client",
      validation: { required: true, format: "https_url" },
    },
    {
      name: "instance_url",
      label: "Instance",
      secret: false,
      level: "discovered",
      validation: { required: false, format: "https_url" },
    },
  ],
}

const githubMethods: SigninMethod[] = [
  { kind: "oauth2_code", client: "github", scopes: ["repo"], fields: [] },
  {
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
  },
]

function showDialog(
  props: Partial<Parameters<typeof CredentialDialog>[0]> = {}
) {
  const onSubmit = vi.fn(
    props.onSubmit ?? (async (): Promise<void> => undefined)
  )
  const onClose = vi.fn()
  render(
    <CredentialDialog
      mode="connect"
      appName="Zendesk"
      methods={[zendeskToken]}
      {...props}
      onSubmit={onSubmit}
      onClose={onClose}
    />
  )
  return { onSubmit, onClose }
}

function tokenInput() {
  return screen.getByLabelText<HTMLInputElement>(/^Token/)
}

describe("CredentialDialog", () => {
  it("renders a way in from its manifest: the standard fields and the member's own, never an admin's or a discovered one", () => {
    showDialog()

    const dialog = screen.getByRole("dialog", { name: "Connect Zendesk" })
    expect(
      within(dialog).getByLabelText<HTMLInputElement>("Token *")
    ).toBeTruthy()
    const subdomain = within(dialog).getByLabelText<HTMLInputElement>(
      "Zendesk subdomain *"
    )
    expect(subdomain.getAttribute("type")).toBe("text")
    expect(within(dialog).getByText("The part before .zendesk.com.").id).toBe(
      subdomain.getAttribute("aria-describedby")
    )
    const region = within(dialog).getByLabelText<HTMLInputElement>("Region *")
    expect(region.tagName).toBe("SELECT")
    expect(
      within(region)
        .getAllByRole("option")
        .map((option) => option.textContent)
    ).toEqual(["Choose…", "us", "eu"])
    expect(within(dialog).queryByLabelText(/API audience/)).toBeNull()
    expect(within(dialog).queryByLabelText(/Instance/)).toBeNull()
    // One way in: nothing to choose between.
    expect(within(dialog).queryByRole("radio")).toBeNull()
  })

  it("masks a secret, never fills it in, hands it on once and wipes it after", async () => {
    let resolveSubmit = () => {}
    const { onSubmit } = showDialog({
      onSubmit: () =>
        new Promise<void>((resolve) => {
          resolveSubmit = resolve
        }),
    })
    const token = tokenInput()
    expect(token.type).toBe("password")
    expect(token.value).toBe("")
    expect(token.getAttribute("autocomplete")).toBe("off")

    fireEvent.change(token, { target: { value: `  ${secret}  ` } })
    fireEvent.change(
      screen.getByLabelText<HTMLInputElement>("Zendesk subdomain *"),
      {
        target: { value: "acme" },
      }
    )
    fireEvent.change(screen.getByLabelText<HTMLInputElement>("Region *"), {
      target: { value: "eu" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Connect" }))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    expect(onSubmit).toHaveBeenCalledWith("api_token", {
      region: "eu",
      subdomain: "acme",
      token: secret,
    })
    expect(
      screen.getByRole("button", { name: /Checking with Zendesk/ })
    ).toBeTruthy()
    resolveSubmit()
    await waitFor(() => expect(tokenInput().value).toBe(""))
    expect(tokenInput().disabled).toBe(false)
    // A non-secret field keeps what was typed; nothing shows the secret.
    expect(
      screen.getByLabelText<HTMLInputElement>("Zendesk subdomain *").value
    ).toBe("acme")
    expect(document.body.innerHTML).not.toContain(secret)
  })

  it("checks each field's rules before sending, under the field and without its value", async () => {
    const { onSubmit } = showDialog()
    fireEvent.change(
      screen.getByLabelText<HTMLInputElement>("Zendesk subdomain *"),
      {
        target: { value: "Not A Subdomain" },
      }
    )
    fireEvent.click(screen.getByRole("button", { name: "Connect" }))

    const token = tokenInput()
    expect(await screen.findByText("Token is required.")).toBeTruthy()
    expect(token.getAttribute("aria-invalid")).toBe("true")
    expect(token.getAttribute("aria-describedby")).toBe(
      screen.getByText("Token is required.").id
    )
    expect(document.activeElement).toBe(token)
    expect(
      screen.getByText("Zendesk subdomain isn't in the form this app expects.")
    ).toBeTruthy()
    expect(screen.getByText("Region is required.")).toBeTruthy()
    expect(document.body.textContent).not.toContain("Not A Subdomain")
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("puts apps' invalid_credentials under the field it names, and wipes the secret", async () => {
    showDialog({
      appName: "GitHub",
      methods: [githubMethods[1]],
      onSubmit: async () => {
        throw new AppsFieldError(
          "token was refused by the app",
          400,
          "invalid_credentials",
          "token"
        )
      },
    })
    fireEvent.change(tokenInput(), { target: { value: secret } })
    fireEvent.click(screen.getByRole("button", { name: "Connect" }))

    const problem = await screen.findByText("Token was refused by the app.")
    const token = tokenInput()
    expect(token.getAttribute("aria-describedby")).toBe(problem.id)
    expect(token.getAttribute("aria-invalid")).toBe("true")
    expect(token.value).toBe("")
    await waitFor(() => expect(document.activeElement).toBe(token))
    expect(screen.queryByRole("alert")).toBeNull()
    expect(document.body.innerHTML).not.toContain(secret)
  })

  it("says the app refused it when apps names no field, and anything else in words", async () => {
    let answer: Error = new AppsFieldError(
      "the credential was refused by the app",
      400,
      "invalid_credentials"
    )
    showDialog({
      appName: "GitHub",
      methods: [githubMethods[1]],
      onSubmit: async () => {
        throw answer
      },
    })
    fireEvent.change(tokenInput(), { target: { value: secret } })
    fireEvent.click(screen.getByRole("button", { name: "Connect" }))
    expect((await screen.findByRole("alert")).textContent).toBe(
      "GitHub didn't accept this token. Check it and try again."
    )

    answer = new AppsFieldError("too many requests; retry", 429, "rate_limited")
    fireEvent.change(tokenInput(), { target: { value: secret } })
    fireEvent.click(screen.getByRole("button", { name: "Connect" }))
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toBe(
        "Too many requests. Try again in a minute."
      )
    )
  })

  it("offers each way in when there is more than one, and continues to the app for OAuth", async () => {
    const { onSubmit } = showDialog({
      appName: "GitHub",
      methods: githubMethods,
    })

    const oauth = screen.getByRole("radio", { name: "Sign in with GitHub" })
    expect((oauth as HTMLInputElement).checked).toBe(true)
    expect(screen.queryByLabelText(/^Token/)).toBeNull()
    expect(
      screen.getByText(
        "You'll continue to GitHub to approve access, then come back here."
      )
    ).toBeTruthy()

    fireEvent.click(screen.getByRole("radio", { name: "API token" }))
    fireEvent.change(tokenInput(), { target: { value: secret } })
    fireEvent.click(screen.getByRole("button", { name: "Connect" }))
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith("api_token", { token: secret })
    )

    fireEvent.click(screen.getByRole("radio", { name: "Sign in with GitHub" }))
    fireEvent.click(screen.getByRole("button", { name: /Continue to GitHub/ }))
    await waitFor(() =>
      expect(onSubmit).toHaveBeenLastCalledWith("oauth2_code", {})
    )
  })

  it("replaces a key with the connection's own kind alone", async () => {
    const { onSubmit, onClose } = showDialog({
      mode: "replace",
      appName: "GitHub",
      methods: [githubMethods[1]],
    })
    expect(
      screen.getByRole("dialog", { name: "Replace your GitHub key" })
    ).toBeTruthy()
    expect(screen.queryByRole("radio")).toBeNull()
    fireEvent.change(tokenInput(), { target: { value: secret } })
    fireEvent.click(screen.getByRole("button", { name: "Replace key" }))
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith("api_token", { token: secret })
    )
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(onClose).toHaveBeenCalled()
  })

  it("says when the app's sign-in is still loading or could not be read", () => {
    const onRetry = vi.fn()
    const { rerender } = render(
      <CredentialDialog
        mode="replace"
        appName="GitHub"
        methods={[]}
        loading
        onSubmit={async () => undefined}
        onClose={() => {}}
      />
    )
    expect(screen.getByRole("status").textContent).toBe(
      "Loading GitHub's sign-in…"
    )
    rerender(
      <CredentialDialog
        mode="replace"
        appName="GitHub"
        methods={[]}
        loadError="GitHub's sign-in could not be loaded."
        onRetry={onRetry}
        onSubmit={async () => undefined}
        onClose={() => {}}
      />
    )
    expect(screen.getByRole("alert").textContent).toBe(
      "GitHub's sign-in could not be loaded."
    )
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(onRetry).toHaveBeenCalled()
  })
})
