import { useEffect, useId, useRef, useState } from "react"
import type { FormEvent } from "react"
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import { Check, Copy } from "lucide-react"
import { Input } from "@/components/ui/input"
import type { Organization, Role } from "@/lib/auth/client"
import {
  AppsFieldError,
  appsErrorMessage,
  clientFields,
  clientIdProblem,
  fieldProblem,
  oauthClientQueryOptions,
  oryxaIntegrationsQueryOptions,
  refusedFieldText,
  removeOAuthClient,
  revealVerificationToken,
  saveOAuthClient,
  signinManifestQueryOptions,
  toolkitName,
  webhookSecretProblem,
} from "@/lib/apps/client"
import {
  SettingsActions,
  SettingsButton,
  SettingsRow,
  SettingsSwitch,
} from "./settings-kit"
import type {
  OAuthClient,
  OrganizationWebhook,
  SigninField,
  SigninManifest,
} from "@/lib/apps/client"

/** Owners and admins manage an organization's own OAuth apps; apps refuses anyone else. */
export function isOrganizationAdmin(role?: Role): boolean {
  return role === "owner" || role === "admin"
}

/**
 * Whether Settings offers Sign-in apps: to an organization's admins, once
 * Oryxa's own sign-in is switched on for an app that signs in through an
 * OAuth app. It ships switched off, and until then there is nothing to set.
 */
export function useSigninAppsAvailable({
  organizationId,
  role,
  preview,
}: {
  organizationId?: string
  role?: Role
  preview: boolean
}): boolean {
  const eligible =
    !preview && Boolean(organizationId) && isOrganizationAdmin(role)
  const integrations = useQuery({
    ...oryxaIntegrationsQueryOptions(),
    enabled: eligible,
    retry: false,
  })
  return (
    eligible &&
    (integrations.data ?? []).some((integration) =>
      integration.kinds.some((kind) => kind.startsWith("oauth2_"))
    )
  )
}

/** One OAuth client registration, shared by the apps that sign in through it. */
type Registration = {
  client: string
  name: string
  apps: Array<{ toolkit: string; name: string }>
  /** Client-level fields, which an admin saves with the organization's own app. */
  fields: SigninField[]
}

function registrationsOf(manifests: SigninManifest[]): Registration[] {
  const byClient = new Map<string, Registration>()
  for (const manifest of manifests) {
    for (const method of manifest.methods) {
      if (!method.client) continue
      const registration = byClient.get(method.client) ?? {
        client: method.client,
        name: toolkitName(method.client),
        apps: [],
        fields: [],
      }
      if (!registration.apps.some((app) => app.toolkit === manifest.toolkit)) {
        registration.apps.push({
          toolkit: manifest.toolkit,
          name: manifest.name,
        })
      }
      if (manifest.toolkit === method.client) registration.name = manifest.name
      for (const field of clientFields(method)) {
        if (!registration.fields.some((known) => known.name === field.name)) {
          registration.fields.push(field)
        }
      }
      byClient.set(method.client, registration)
    }
  }
  return [...byClient.values()].sort((a, b) => a.name.localeCompare(b.name))
}

function listNames(names: string[]): string {
  if (names.length < 2) return names.join("")
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`
}

/**
 * Settings → Sign-in apps: an organization's own OAuth app for each client
 * registration Oryxa's own sign-in uses (GitHub, Google, Notion, Slack),
 * for its admins. Each card shows what to copy into the app (the redirect
 * URI and any webhook URL), takes the client ID, a write-only secret shown
 * only by its last four characters, the client-level fields and any webhook
 * signing secret, reveals Notion's verification token once, switches off
 * Oryxa's own app, and removes the organization's.
 */
export function WorkspaceSigninAppsSettings({
  organization,
  role,
  preview,
}: {
  organization?: Organization
  role?: Role
  preview: boolean
}) {
  const admin = isOrganizationAdmin(role)
  const enabled = !preview && Boolean(organization) && admin
  const integrations = useQuery({
    ...oryxaIntegrationsQueryOptions(),
    enabled,
  })
  const manifests = useQueries({
    queries: (integrations.data ?? []).map((integration) => ({
      ...signinManifestQueryOptions(integration.toolkit),
      enabled,
    })),
  })
  const manifestFailure = manifests.find((read) => read.isError)
  const manifestsPending = manifests.some((read) => read.isPending)
  const registrations = registrationsOf(
    manifests.flatMap((read) => (read.data ? [read.data] : []))
  )

  return (
    <div className="workspace-settings-account">
      <section className="workspace-settings-section" aria-label="Sign-in apps">
        <div className="workspace-settings-section-title">
          <h3>Your organisation's own apps</h3>
          <p>
            Members sign in to these apps through Oryxa's own app unless your
            organisation brings its own. Secrets are write-only: once saved,
            only their last four characters show.
          </p>
        </div>
        {preview ? (
          <p className="workspace-settings-hint">
            Layout preview · Sign in to manage sign-in apps.
          </p>
        ) : !organization ? (
          <p className="workspace-settings-hint">
            Select an organisation first.
          </p>
        ) : !admin ? (
          <p className="workspace-settings-hint">
            Organisation owners and admins manage sign-in apps.
          </p>
        ) : integrations.isPending || manifestsPending ? (
          <p className="workspace-settings-hint" role="status">
            Loading sign-in apps…
          </p>
        ) : integrations.isError || manifestFailure ? (
          <p className="workspace-settings-error" role="alert">
            {appsErrorMessage(
              integrations.error ?? manifestFailure?.error,
              "Sign-in apps could not be loaded."
            )}{" "}
            <SettingsButton
              type="button"
              onClick={() => {
                if (integrations.isError) void integrations.refetch()
                for (const read of manifests) {
                  if (read.isError) void read.refetch()
                }
              }}
            >
              Retry
            </SettingsButton>
          </p>
        ) : !registrations.length ? (
          <p className="workspace-settings-hint">
            Oryxa's own sign-in isn't switched on for an app with an OAuth
            sign-in here yet.
          </p>
        ) : null}
      </section>
      {enabled &&
        organization &&
        registrations.map((registration) => (
          <OAuthClientCard
            key={registration.client}
            organizationId={organization.id}
            registration={registration}
          />
        ))}
    </div>
  )
}

function clientStateText(view: OAuthClient, name: string): string {
  if (view.client_id) {
    return `Members sign in through your organisation's own ${name} app.`
  }
  if (view.refuse_default) {
    return `Oryxa's ${name} app is off and there is no app of your own, so members can't sign in to ${name}.`
  }
  if (view.oryxa_default) return `Members sign in through Oryxa's ${name} app.`
  return `Oryxa has no ${name} app here. Add your own for members to sign in.`
}

/** A saved client's identity: once it changes, the form starts again from it. */
function clientVersion(view: OAuthClient): string {
  return [
    view.saved,
    view.client_id ?? "",
    view.refuse_default,
    view.updated_at ?? "",
  ].join("|")
}

function OAuthClientCard({
  organizationId,
  registration,
}: {
  organizationId: string
  registration: Registration
}) {
  const view = useQuery(
    oauthClientQueryOptions(organizationId, registration.client)
  )
  const [status, setStatus] = useState("")
  const label = `${registration.name} sign-in app`
  const usedBy = `Used by ${listNames(registration.apps.map((app) => app.name))}.`

  if (!view.data) {
    return (
      <section className="workspace-settings-section" aria-label={label}>
        <div className="workspace-settings-section-title">
          <h3>{registration.name}</h3>
          <p>{usedBy}</p>
        </div>
        {view.isError ? (
          <p className="workspace-settings-error" role="alert">
            {appsErrorMessage(
              view.error,
              `${registration.name}'s sign-in app could not be loaded.`
            )}{" "}
            <SettingsButton type="button" onClick={() => void view.refetch()}>
              Retry
            </SettingsButton>
          </p>
        ) : (
          <p className="workspace-settings-hint" role="status">
            Loading…
          </p>
        )}
      </section>
    )
  }
  return (
    <OAuthClientForm
      organizationId={organizationId}
      registration={registration}
      view={view.data}
      label={label}
      usedBy={usedBy}
      status={status}
      onStatus={setStatus}
      onRefresh={() => void view.refetch()}
    />
  )
}

type Problems = Record<string, string>

const secretFieldName = (name: string) => `field.${name}`
const webhookSecretName = (toolkit: string) => `webhook.${toolkit}`

function OAuthClientForm({
  organizationId,
  registration,
  view,
  label,
  usedBy,
  status,
  onStatus,
  onRefresh,
}: {
  organizationId: string
  registration: Registration
  view: OAuthClient
  label: string
  usedBy: string
  status: string
  onStatus: (status: string) => void
  onRefresh: () => void
}) {
  const queryClient = useQueryClient()
  const queryKey = oauthClientQueryOptions(
    organizationId,
    registration.client
  ).queryKey
  const name = registration.name
  const plainFields = registration.fields.filter((field) => !field.secret)
  const secretFields = registration.fields.filter((field) => field.secret)
  const savedValues = () =>
    Object.fromEntries(
      plainFields.map((field) => [field.name, view.fields[field.name] ?? ""])
    )
  const [clientId, setClientId] = useState(view.client_id ?? "")
  const [values, setValues] = useState<Record<string, string>>(savedValues)
  const [refuse, setRefuse] = useState(view.refuse_default)
  // A save or a removal changes what apps holds: the form follows it, and
  // stays mounted so focus stays where the admin left it.
  const version = clientVersion(view)
  const [syncedVersion, setSyncedVersion] = useState(version)
  if (version !== syncedVersion) {
    setSyncedVersion(version)
    setClientId(view.client_id ?? "")
    setValues(savedValues())
    setRefuse(view.refuse_default)
  }
  // Whether any secret input holds something: a flag, never what it holds.
  const [secretTyped, setSecretTyped] = useState(false)
  const [problems, setProblems] = useState<Problems>({})
  const [formError, setFormError] = useState("")
  const [pending, setPending] = useState(false)
  const [confirmRemove, setConfirmRemove] = useState(false)
  const formRef = useRef<HTMLFormElement>(null)
  // A field apps refused is focused once its input is enabled again.
  const refocus = useRef<string | null>(null)
  useEffect(() => {
    if (pending || !refocus.current || !formRef.current) return
    const input = formRef.current.elements.namedItem(refocus.current)
    refocus.current = null
    if (input instanceof HTMLElement) input.focus()
  }, [pending])
  const remove = useMutation({
    mutationFn: () => removeOAuthClient(organizationId, registration.client),
    onSuccess: async () => {
      setConfirmRemove(false)
      onStatus(`Removed. Members sign in through Oryxa's ${name} app again.`)
      await queryClient.invalidateQueries({ queryKey })
    },
  })
  const id = clientId.trim()
  const newClient = id !== "" && id !== (view.client_id ?? "")
  const changed =
    id !== (view.client_id ?? "") ||
    refuse !== view.refuse_default ||
    secretTyped ||
    plainFields.some(
      (field) =>
        (values[field.name] ?? "").trim() !== (view.fields[field.name] ?? "")
    )
  const busy = pending || remove.isPending

  const labelOf = (key: string): string => {
    if (key === "client_id") return "Client ID"
    if (key === "client_secret") return "Client secret"
    if (key.startsWith("webhook.")) {
      return `${toolkitName(key.slice("webhook.".length))} signing secret`
    }
    const field = registration.fields.find(
      (candidate) => candidate.name === key.replace(/^field\./, "")
    )
    return field?.label ?? key
  }

  /** The input apps' `field` names, keyed as this form keys its problems. */
  const problemKey = (field: string): string | undefined => {
    if (field === "client_id" || field === "client_secret") return field
    if (field.startsWith("webhook_secrets.")) {
      const toolkit = field.slice("webhook_secrets.".length)
      return view.webhooks.some((webhook) => webhook.toolkit === toolkit)
        ? webhookSecretName(toolkit)
        : undefined
    }
    if (plainFields.some((candidate) => candidate.name === field)) return field
    if (secretFields.some((candidate) => candidate.name === field)) {
      return secretFieldName(field)
    }
    return undefined
  }

  function noteSecrets(form: HTMLFormElement | null) {
    if (!form) return
    setSecretTyped(
      [
        ...form.querySelectorAll<HTMLInputElement>('input[type="password"]'),
      ].some((input) => input.value !== "")
    )
  }

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy) return
    const form = event.currentTarget
    const data = new FormData(form)
    const read = (field: string) => {
      const value = data.get(field)
      return typeof value === "string" ? value.trim() : ""
    }
    // What was typed into the secret inputs lives in these locals alone,
    // from here to the request.
    const secret = read("client_secret")
    const typedFields: Record<string, string> = {}
    for (const field of secretFields) {
      const value = read(secretFieldName(field.name))
      if (value) typedFields[field.name] = value
    }
    const webhookSecrets: Record<string, string> = {}
    for (const webhook of view.webhooks) {
      if (!webhook.secret_required) continue
      const value = read(webhookSecretName(webhook.toolkit))
      if (value) webhookSecrets[webhook.toolkit] = value
    }

    const found: Problems = {}
    if (id) {
      const idProblem = clientIdProblem(id)
      if (idProblem) found.client_id = idProblem
      if (secret) {
        const problem = fieldProblem(
          { label: "Client secret", validation: { required: true } },
          secret
        )
        if (problem) found.client_secret = problem
      } else if (newClient) {
        found.client_secret = view.client_id
          ? "Client secret is required with a new client ID."
          : "Client secret is required."
      }
      for (const field of plainFields) {
        const problem = fieldProblem(field, values[field.name] ?? "")
        if (problem) found[field.name] = problem
      }
      for (const field of secretFields) {
        const typed = typedFields[field.name]
        const problem = typed
          ? fieldProblem(field, typed)
          : field.validation.required &&
              (newClient || !(field.name in view.secret_fields))
            ? `${field.label} is required.`
            : undefined
        if (problem) found[secretFieldName(field.name)] = problem
      }
      for (const [toolkit, value] of Object.entries(webhookSecrets)) {
        const problem = webhookSecretProblem(
          labelOf(webhookSecretName(toolkit)),
          value
        )
        if (problem) found[webhookSecretName(toolkit)] = problem
      }
    } else if (
      secret ||
      Object.keys(typedFields).length ||
      Object.keys(webhookSecrets).length ||
      plainFields.some((field) => (values[field.name] ?? "").trim())
    ) {
      found.client_id = "Enter the client ID these belong to."
    } else if (!refuse) {
      found.client_id = view.saved
        ? `Enter a client ID, or remove your app below to use Oryxa's ${name} app again.`
        : "Enter a client ID."
    }
    const first = Object.keys(found)[0]
    if (first) {
      setProblems(found)
      setFormError("")
      const input = form.elements.namedItem(first)
      if (input instanceof HTMLElement) input.focus()
      return
    }

    // Fields apps keeps: for the same client, what changed (one left out
    // keeps its value, an empty one clears it); for a new one, all of them.
    const fields: Record<string, string> = {}
    if (id) {
      for (const field of plainFields) {
        const value = (values[field.name] ?? "").trim()
        if (
          newClient ? value !== "" : value !== (view.fields[field.name] ?? "")
        ) {
          fields[field.name] = value
        }
      }
      Object.assign(fields, typedFields)
    }
    setProblems({})
    setFormError("")
    onStatus("")
    setPending(true)
    try {
      const saved = await saveOAuthClient(organizationId, registration.client, {
        clientId: id,
        clientSecret: secret || undefined,
        fields: id ? fields : undefined,
        refuseDefault: refuse,
        webhookSecrets: id ? webhookSecrets : undefined,
      })
      onStatus("Saved.")
      queryClient.setQueryData(queryKey, saved)
    } catch (error) {
      const refused = error instanceof AppsFieldError ? error : undefined
      const blamed = refused?.field
      const key = blamed ? problemKey(blamed) : undefined
      if (refused && blamed && key) {
        setProblems({
          [key]: refusedFieldText(refused.message, blamed, labelOf(key)),
        })
        refocus.current = key
      } else {
        const message = appsErrorMessage(
          error,
          `Could not save the ${name} app. Try again.`
        )
        setFormError(message.charAt(0).toUpperCase() + message.slice(1))
      }
    } finally {
      for (const input of form.querySelectorAll<HTMLInputElement>(
        'input[type="password"]'
      )) {
        input.value = ""
      }
      setSecretTyped(false)
      setPending(false)
    }
  }

  const inputId = (key: string) =>
    `signin-app-${registration.client}-${key.replace(/[^a-z0-9_-]/gi, "-")}`
  const problemFor = (key: string) =>
    problems[key] ? (
      <p className="workspace-settings-error" id={`${inputId(key)}-problem`}>
        {problems[key]}
      </p>
    ) : null
  const describedBy = (key: string, hint?: string) =>
    [problems[key] ? `${inputId(key)}-problem` : "", hint ?? ""]
      .filter(Boolean)
      .join(" ") || undefined
  const secretHintId = `${inputId("client_secret")}-hint`

  return (
    <form
      ref={formRef}
      className="workspace-settings-form"
      aria-label={label}
      noValidate
      onSubmit={(event) => void save(event)}
      onChange={(event) => {
        if (
          event.target instanceof HTMLInputElement &&
          event.target.type === "password"
        ) {
          noteSecrets(event.currentTarget)
        }
      }}
    >
      <div className="workspace-settings-section-title">
        <h3>{name}</h3>
        <p>
          {usedBy} {clientStateText(view, name)}
        </p>
      </div>

      {view.redirect_uri && (
        <CopyField
          label="Redirect URI"
          value={view.redirect_uri}
          hint={`Add it to your ${name} app as its redirect or callback URL.`}
        />
      )}

      <label htmlFor={inputId("client_id")}>Client ID</label>
      <Input
        id={inputId("client_id")}
        name="client_id"
        value={clientId}
        autoComplete="off"
        spellCheck={false}
        disabled={busy}
        aria-invalid={Boolean(problems.client_id)}
        aria-describedby={describedBy("client_id")}
        onChange={(event) => {
          setClientId(event.target.value)
          onStatus("")
        }}
      />
      {problemFor("client_id")}

      <label htmlFor={inputId("client_secret")}>Client secret</label>
      <Input
        id={inputId("client_secret")}
        name="client_secret"
        type="password"
        autoComplete="off"
        spellCheck={false}
        disabled={busy}
        aria-invalid={Boolean(problems.client_secret)}
        aria-describedby={describedBy("client_secret", secretHintId)}
      />
      {problemFor("client_secret")}
      <p className="workspace-settings-hint" id={secretHintId}>
        {newClient
          ? "A new client ID needs its secret."
          : view.secret_hint
            ? `Saved, ending in ${view.secret_hint}. Leave it blank to keep it.`
            : view.secret_set
              ? "Saved. Leave it blank to keep it."
              : "Not saved yet."}
      </p>

      {plainFields.map((field) => (
        <div className="contents" key={field.name}>
          <label htmlFor={inputId(field.name)}>
            {field.label}
            {field.validation.required ? "" : " (optional)"}
          </label>
          <Input
            id={inputId(field.name)}
            name={field.name}
            value={values[field.name] ?? ""}
            autoComplete="off"
            spellCheck={false}
            disabled={busy}
            aria-invalid={Boolean(problems[field.name])}
            aria-describedby={describedBy(
              field.name,
              field.help ? `${inputId(field.name)}-help` : undefined
            )}
            onChange={(event) => {
              const next = event.target.value
              setValues((current) => ({ ...current, [field.name]: next }))
              onStatus("")
            }}
          />
          {problemFor(field.name)}
          {field.help && (
            <p
              className="workspace-settings-hint"
              id={`${inputId(field.name)}-help`}
            >
              {field.help}
            </p>
          )}
        </div>
      ))}

      {secretFields.map((field) => {
        const key = secretFieldName(field.name)
        const hint = view.secret_fields[field.name]
        const hintId = `${inputId(key)}-hint`
        return (
          <div className="contents" key={field.name}>
            <label htmlFor={inputId(key)}>
              {field.label}
              {field.validation.required ? "" : " (optional)"}
            </label>
            <Input
              id={inputId(key)}
              name={key}
              type="password"
              autoComplete="off"
              spellCheck={false}
              disabled={busy}
              aria-invalid={Boolean(problems[key])}
              aria-describedby={describedBy(key, hintId)}
            />
            {problemFor(key)}
            <p className="workspace-settings-hint" id={hintId}>
              {field.name in view.secret_fields
                ? hint
                  ? `Saved, ending in ${hint}. Leave it blank to keep it.`
                  : "Saved. Leave it blank to keep it."
                : field.help || "Not saved yet."}
            </p>
          </div>
        )
      })}

      {view.webhooks.map((webhook) => (
        <WebhookSettings
          key={webhook.toolkit}
          organizationId={organizationId}
          webhook={webhook}
          appName={
            registration.apps.find((app) => app.toolkit === webhook.toolkit)
              ?.name ?? toolkitName(webhook.toolkit)
          }
          inputId={inputId(webhookSecretName(webhook.toolkit))}
          problem={problems[webhookSecretName(webhook.toolkit)]}
          disabled={busy}
          onRefresh={onRefresh}
        />
      ))}

      {/* Refusing an app Oryxa doesn't have changes nothing, so the switch
          shows only where Oryxa has one, or where it is already on. */}
      {(view.oryxa_default || view.refuse_default) && (
        <div className="workspace-settings-row mt-2 border-t border-[var(--tone-383838)]">
          <div>
            <strong>Don't use Oryxa's app</strong>
            <small>
              {view.oryxa_default
                ? `Members sign in only through your organisation's own ${name} app.`
                : `Oryxa has no ${name} app here, so this changes nothing.`}
            </small>
          </div>
          <SettingsSwitch
            label={`Don't use Oryxa's ${name} app`}
            checked={refuse}
            disabled={busy}
            onCheckedChange={(next) => {
              setRefuse(next)
              onStatus("")
            }}
          />
        </div>
      )}

      {formError && (
        <p className="workspace-settings-error" role="alert">
          {formError}
        </p>
      )}
      {remove.isError && (
        <p className="workspace-settings-error" role="alert">
          {appsErrorMessage(remove.error, `Could not remove the ${name} app.`)}
        </p>
      )}
      <SettingsActions start={<span role="status">{status}</span>}>
        <SettingsButton
          type="submit"
          size="default"
          tone="primary"
          disabled={busy || !changed}
        >
          {pending ? "Saving…" : "Save"}
        </SettingsButton>
      </SettingsActions>

      {/* Removing never sits beside Save: it is the form's own danger row. */}
      {view.saved && (
        <div className="settings-danger-row">
          <SettingsRow
            tone="danger"
            title={`Remove your ${name} app`}
            description="Members' new sign-ins go through Oryxa's app again."
          >
            {!confirmRemove && (
              <SettingsButton
                disabled={busy}
                onClick={() => {
                  remove.reset()
                  setConfirmRemove(true)
                }}
              >
                Remove
              </SettingsButton>
            )}
          </SettingsRow>
          {confirmRemove && (
            <SettingsActions
              start={`Remove your organisation's ${name} app? Members' new sign-ins go through Oryxa's app again. Connections made through yours keep working until they're disconnected.`}
            >
              <SettingsButton
                size="default"
                disabled={remove.isPending}
                onClick={() => setConfirmRemove(false)}
              >
                Cancel
              </SettingsButton>
              <SettingsButton
                size="default"
                tone="danger"
                disabled={remove.isPending}
                onClick={() => remove.mutate()}
              >
                {remove.isPending ? "Removing…" : "Remove app"}
              </SettingsButton>
            </SettingsActions>
          )}
        </div>
      )}
    </form>
  )
}

/**
 * What an app that delivers to an endpoint of the organization's own needs:
 * the URL to paste into the app, its signing secret (Slack), or its
 * verification token revealed once (Notion).
 */
function WebhookSettings({
  organizationId,
  webhook,
  appName,
  inputId,
  problem,
  disabled,
  onRefresh,
}: {
  organizationId: string
  webhook: OrganizationWebhook
  appName: string
  inputId: string
  problem?: string
  disabled: boolean
  onRefresh: () => void
}) {
  const secretLabel = `${appName} signing secret`
  const hintId = `${inputId}-hint`
  const problemId = `${inputId}-problem`
  return (
    <div className="mt-2 grid gap-1.5 border-t border-[var(--tone-383838)] pt-3">
      <strong className="text-[13px] font-semibold text-[var(--tone-eeeeee)]">
        {appName} events
      </strong>
      {webhook.url ? (
        <CopyField
          label={`${appName} webhook URL`}
          value={webhook.url}
          hint={`Paste it into your ${appName} app's event settings.`}
        />
      ) : (
        <p className="workspace-settings-hint">
          {webhook.secret_required
            ? `Save your ${appName} app's signing secret to get the URL to paste into ${appName}.`
            : `Save the client ID to get the URL to paste into ${appName}.`}
        </p>
      )}
      {webhook.secret_required && (
        <>
          <label htmlFor={inputId}>{secretLabel}</label>
          <Input
            id={inputId}
            name={webhookSecretName(webhook.toolkit)}
            type="password"
            autoComplete="off"
            spellCheck={false}
            disabled={disabled}
            aria-invalid={Boolean(problem)}
            aria-describedby={
              [problem ? problemId : "", hintId].filter(Boolean).join(" ") ||
              undefined
            }
          />
          {problem && (
            <p className="workspace-settings-error" id={problemId}>
              {problem}
            </p>
          )}
          <p className="workspace-settings-hint" id={hintId}>
            {webhook.secret_set
              ? "Saved. A new one replaces it; the old one keeps working for 24 hours."
              : `The secret your ${appName} app signs its events with. Not saved yet.`}
          </p>
        </>
      )}
      {webhook.verification_token && (
        <VerificationToken
          organizationId={organizationId}
          toolkit={webhook.toolkit}
          appName={appName}
          state={webhook.verification_token}
          onRefresh={onRefresh}
        />
      )}
    </div>
  )
}

/**
 * The token an app sends its endpoint once (Notion's), shown to an admin
 * once, behind a confirm step. It is held here alone, and only until the
 * admin is done with it; apps answers 409 to any later reveal.
 */
function VerificationToken({
  organizationId,
  toolkit,
  appName,
  state,
  onRefresh,
}: {
  organizationId: string
  toolkit: string
  appName: string
  state: string
  onRefresh: () => void
}) {
  const [confirming, setConfirming] = useState(false)
  const [token, setToken] = useState("")
  const [pending, setPending] = useState(false)
  const [problem, setProblem] = useState("")

  // Kept out of TanStack Query's mutation state, which outlives the screen.
  async function reveal() {
    setPending(true)
    setProblem("")
    try {
      setToken(await revealVerificationToken(organizationId, toolkit))
      setConfirming(false)
    } catch (error) {
      setConfirming(false)
      setProblem(
        error instanceof AppsFieldError && error.status === 409
          ? `${appName}'s verification token was already revealed. It's shown only once.`
          : appsErrorMessage(
              error,
              `Could not reveal ${appName}'s verification token.`
            )
      )
    } finally {
      setPending(false)
      onRefresh()
    }
  }

  if (token) {
    return (
      <div className="grid gap-1.5">
        <CopyField
          label={`${appName} verification token`}
          value={token}
          hint={`Shown once. Paste it into ${appName} now: it can't be shown again.`}
        />
        <SettingsButton type="button" onClick={() => setToken("")}>
          Done
        </SettingsButton>
      </div>
    )
  }
  return (
    <div className="grid gap-1.5">
      {problem && (
        <p className="workspace-settings-error" role="alert">
          {problem}
        </p>
      )}
      {state === "awaited" ? (
        <>
          <p className="workspace-settings-hint">
            Waiting for {appName} to send its verification token. Once the URL
            is in your {appName} app, {appName} sends it here.
          </p>
          <SettingsButton type="button" onClick={onRefresh}>
            Check again
          </SettingsButton>
        </>
      ) : state === "ready" ? (
        confirming ? (
          <SettingsActions
            start={`${appName}'s verification token is shown only once. Have your ${appName} app's webhook settings open to paste it straight in.`}
          >
            <SettingsButton
              size="default"
              disabled={pending}
              onClick={() => setConfirming(false)}
            >
              Cancel
            </SettingsButton>
            <SettingsButton
              size="default"
              tone="primary"
              disabled={pending}
              onClick={() => void reveal()}
            >
              {pending ? "Revealing…" : "Reveal once"}
            </SettingsButton>
          </SettingsActions>
        ) : (
          <>
            <p className="workspace-settings-hint">
              {appName} sent its verification token.
            </p>
            <SettingsButton
              type="button"
              onClick={() => {
                setProblem("")
                setConfirming(true)
              }}
            >
              Reveal token
            </SettingsButton>
          </>
        )
      ) : (
        !problem && (
          <p className="workspace-settings-hint">
            {appName}'s verification token was revealed. It's shown only once.
          </p>
        )
      )}
    </div>
  )
}

/** A value an admin copies into their app, with a Copy button. */
function CopyField({
  label,
  value,
  hint,
}: {
  label: string
  value: string
  hint?: string
}) {
  const id = useId()
  const input = useRef<HTMLInputElement>(null)
  const [copied, setCopied] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 2000)
    } catch {
      // No clipboard here (plain http, a denied permission): the value is
      // selected for the admin to copy.
      input.current?.select()
    }
  }

  return (
    <div className="grid gap-1.5">
      <label htmlFor={id} className="mt-0!">
        {label}
      </label>
      <div className="flex items-center gap-2">
        <Input
          id={id}
          ref={input}
          readOnly
          value={value}
          className="font-mono md:text-xs"
          aria-describedby={hint ? `${id}-hint` : undefined}
          onFocus={(event) => event.currentTarget.select()}
        />
        <SettingsButton
          type="button"
          aria-label={`Copy ${label}`}
          onClick={() => void copy()}
        >
          {copied ? <Check size={14} /> : <Copy size={14} />}
          {copied ? "Copied" : "Copy"}
        </SettingsButton>
      </div>
      {hint && (
        <p className="workspace-settings-hint mt-0!" id={`${id}-hint`}>
          {hint}
        </p>
      )}
    </div>
  )
}
