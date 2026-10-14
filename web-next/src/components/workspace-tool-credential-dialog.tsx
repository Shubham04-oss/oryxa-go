import { useEffect, useId, useRef, useState } from "react"
import type { FormEvent } from "react"
import { ArrowUpRight, LoaderCircle } from "lucide-react"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  AppsFieldError,
  appsErrorMessage,
  fieldProblem,
  memberFields,
  refusedFieldText,
  signinMethodLabel,
} from "@/lib/apps/client"
import type { SigninField, SigninMethod } from "@/lib/apps/client"

export type CredentialDialogMode = "connect" | "replace"

type Problems = Record<string, string>

/**
 * What the member typed for `fields`, read from the form at the moment it is
 * sent. The values live in this object alone, which the caller hands on and
 * drops: none of them is ever put in React state.
 */
function readValues(
  form: HTMLFormElement,
  fields: SigninField[]
): Record<string, string> {
  const data = new FormData(form)
  const values: Record<string, string> = {}
  for (const field of fields) {
    const value = data.get(field.name)
    const text = typeof value === "string" ? value.trim() : ""
    if (text) values[field.name] = text
  }
  return values
}

/** Empties every secret input, so what was typed doesn't outlive the request. */
function wipeSecrets(form: HTMLFormElement, fields: SigninField[]) {
  for (const field of fields) {
    if (!field.secret) continue
    const input = form.elements.namedItem(field.name)
    if (input instanceof HTMLInputElement) input.value = ""
  }
}

function focusField(form: HTMLFormElement, name: string) {
  const input = form.elements.namedItem(name)
  if (input instanceof HTMLElement) input.focus()
}

/**
 * The form a way in to an app asks for, rendered from its manifest: the
 * kind's standard fields (a token; a username and password; a client ID and
 * secret) and the manifest's own connection-level fields, each with its
 * label, help and rules. Secret fields are masked and write-only: never
 * filled in, never kept in state, and emptied once the request settles,
 * whatever apps answered.
 *
 * To connect, it offers each of the app's ways in when there is more than
 * one; an OAuth way in with nothing to type continues to the app. To replace
 * a key, it offers the connection's own kind alone.
 */
export function CredentialDialog({
  mode,
  appName,
  methods,
  loading = false,
  loadError,
  onRetry,
  onSubmit,
  onClose,
}: {
  mode: CredentialDialogMode
  appName: string
  /** The ways in to offer, the default first. */
  methods: SigninMethod[]
  /** The app's manifest is still being read. */
  loading?: boolean
  /** Why the manifest could not be read. */
  loadError?: string
  onRetry?: () => void
  /**
   * Sends the values. It resolves once the member is done (the owner then
   * closes the dialog or leaves the page) and throws what apps answered
   * otherwise. The values must go straight to the request: not into state,
   * a query cache or a mutation's variables.
   */
  onSubmit: (kind: string, values: Record<string, string>) => Promise<void>
  onClose: () => void
}) {
  const [chosenKind, setChosenKind] = useState("")
  const [pending, setPending] = useState(false)
  const [problems, setProblems] = useState<Problems>({})
  const [formError, setFormError] = useState("")
  const form = useRef<HTMLFormElement>(null)
  // A field apps refused is focused once its input is enabled again.
  const refocus = useRef<string | null>(null)
  useEffect(() => {
    if (pending || !refocus.current || !form.current) return
    focusField(form.current, refocus.current)
    refocus.current = null
  }, [pending])
  const method: SigninMethod | undefined =
    methods.find((candidate) => candidate.kind === chosenKind) ?? methods.at(0)
  const fields = method ? memberFields(method) : []
  const continues = method?.kind === "oauth2_code"
  const replacing = mode === "replace"
  const title = replacing ? `Replace your ${appName} key` : `Connect ${appName}`

  function choose(kind: string) {
    setChosenKind(kind)
    setProblems({})
    setFormError("")
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!method || pending) return
    const target = event.currentTarget
    const values = readValues(target, fields)
    const found: Problems = {}
    for (const field of fields) {
      const problem = fieldProblem(field, values[field.name] ?? "")
      if (problem) found[field.name] = problem
    }
    const first = fields.find((field) => found[field.name])
    if (first) {
      setProblems(found)
      setFormError("")
      focusField(target, first.name)
      return
    }
    setProblems({})
    setFormError("")
    setPending(true)
    try {
      await onSubmit(method.kind, values)
    } catch (error) {
      const refused = error instanceof AppsFieldError ? error : undefined
      const blamed = refused?.field
        ? fields.find((field) => field.name === refused.field)
        : undefined
      if (refused && blamed) {
        setProblems({
          [blamed.name]: refusedFieldText(
            refused.message,
            blamed.name,
            blamed.label
          ),
        })
        refocus.current = blamed.name
      } else if (refused?.code === "invalid_credentials") {
        // apps names no field when the app itself refused what was typed.
        setFormError(
          fields.length === 1
            ? `${appName} didn't accept this ${fields[0].label.toLowerCase()}. Check it and try again.`
            : `${appName} didn't accept these details. Check them and try again.`
        )
      } else {
        setFormError(
          appsErrorMessage(
            error,
            replacing
              ? `Could not replace the ${appName} key. Try again.`
              : `Could not connect ${appName}. Try again.`
          )
        )
      }
    } finally {
      wipeSecrets(target, fields)
      setPending(false)
    }
  }

  return (
    <Dialog
      open
      disablePointerDismissal={pending}
      onOpenChange={(open) => {
        if (!open && !pending) onClose()
      }}
    >
      <DialogContent
        className="gap-4 rounded-[15px] border border-[var(--tone-383838)] bg-[var(--tone-252525)] p-5 text-[var(--tone-eeeeee)] shadow-2xl ring-0 sm:max-w-md"
        showCloseButton={!pending}
      >
        <div className="grid gap-1.5 pr-6">
          <DialogTitle className="text-[15px] font-semibold text-[var(--tone-f3f3f3)]">
            {title}
          </DialogTitle>
          <DialogDescription className="text-xs leading-normal text-[var(--text-muted)]">
            {replacing
              ? `The new key is checked with ${appName} before it replaces the old one, which keeps working until then. A saved key can't be read back.`
              : `Your agents use this to act in ${appName} as you. A key is checked with ${appName} before it's saved, and can't be read back.`}
          </DialogDescription>
        </div>
        {loading ? (
          <p className="m-0 text-xs text-[var(--text-muted)]" role="status">
            Loading {appName}'s sign-in…
          </p>
        ) : loadError || !method ? (
          <div className="grid gap-3">
            <p className="m-0 text-xs text-[var(--tone-f0aaaa)]" role="alert">
              {loadError || `${appName}'s sign-in could not be loaded.`}
            </p>
            <div className="flex justify-end gap-2">
              <button
                type="button"
                className="workspace-tools-pill is-quiet"
                onClick={onClose}
              >
                Close
              </button>
              {onRetry && (
                <button
                  type="button"
                  className="workspace-tools-pill"
                  onClick={onRetry}
                >
                  Try again
                </button>
              )}
            </div>
          </div>
        ) : (
          <form
            ref={form}
            className="grid gap-4"
            noValidate
            aria-label={title}
            onSubmit={(event) => void submit(event)}
          >
            {!replacing && methods.length > 1 && (
              <fieldset className="grid gap-2" disabled={pending}>
                <legend className="mb-2 text-xs text-[var(--tone-dddddd)]">
                  How to sign in
                </legend>
                {methods.map((candidate) => (
                  <label
                    key={candidate.kind}
                    className="flex cursor-pointer items-center gap-2.5 rounded-lg border border-[var(--tone-3d3d3d)] px-3 py-2 text-xs text-[var(--tone-dddddd)] has-[:checked]:border-[var(--tone-8a8a8a)] has-[:checked]:bg-[var(--tone-2f2f2f)] has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-[var(--tone-8a8a8a)]"
                  >
                    <input
                      type="radio"
                      name="signin-method"
                      value={candidate.kind}
                      checked={candidate.kind === method.kind}
                      onChange={() => choose(candidate.kind)}
                      className="accent-[var(--tone-b5b5b5)]"
                    />
                    {signinMethodLabel(candidate.kind, appName)}
                  </label>
                ))}
              </fieldset>
            )}
            {/* Keyed by the way in, so switching starts its fields afresh. */}
            <div className="grid gap-3.5" key={method.kind}>
              {continues && fields.length === 0 && (
                <p className="m-0 text-xs leading-normal text-[var(--tone-bbbbbb)]">
                  You'll continue to {appName} to approve access, then come back
                  here.
                </p>
              )}
              {fields.map((field) => (
                <CredentialField
                  key={field.name}
                  field={field}
                  problem={problems[field.name]}
                  disabled={pending}
                />
              ))}
            </div>
            {formError && (
              <p
                className="m-0 text-xs leading-normal text-[var(--tone-f0aaaa)]"
                role="alert"
              >
                {formError}
              </p>
            )}
            <div className="flex flex-wrap justify-end gap-2">
              <button
                type="button"
                className="workspace-tools-pill is-quiet"
                disabled={pending}
                onClick={onClose}
              >
                Cancel
              </button>
              <button
                type="submit"
                className="workspace-tools-pill"
                disabled={pending}
              >
                {pending ? (
                  <>
                    <LoaderCircle
                      className="workspace-tools-spinner"
                      size={14}
                    />
                    {continues ? "Opening…" : `Checking with ${appName}…`}
                  </>
                ) : continues ? (
                  <>
                    Continue to {appName} <ArrowUpRight size={14} />
                  </>
                ) : replacing ? (
                  "Replace key"
                ) : (
                  "Connect"
                )}
              </button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

/**
 * One field. Nothing is ever filled in: a secret is typed fresh each time,
 * and the rest start empty because apps never answers them back either.
 */
function CredentialField({
  field,
  problem,
  disabled,
}: {
  field: SigninField
  problem?: string
  disabled: boolean
}) {
  const id = useId()
  const helpId = `${id}-help`
  const problemId = `${id}-problem`
  const describedBy =
    [field.help ? helpId : "", problem ? problemId : ""]
      .filter(Boolean)
      .join(" ") || undefined
  const choices = field.validation.enum ?? []
  const control =
    "min-h-9 w-full rounded-lg border border-[var(--tone-4c4c4c)] bg-[var(--tone-202020)] px-2.5 text-base text-[var(--tone-eeeeee)] outline-none focus:border-[var(--tone-aaaaaa)] disabled:opacity-50 aria-[invalid=true]:border-[var(--tone-cc7777)] md:text-[13px]"
  return (
    <div className="grid gap-1.5">
      <label htmlFor={id} className="text-xs text-[var(--tone-dddddd)]">
        {field.label}
        {field.validation.required ? " *" : " (optional)"}
      </label>
      {choices.length ? (
        <select
          id={id}
          name={field.name}
          defaultValue=""
          disabled={disabled}
          aria-invalid={Boolean(problem)}
          aria-describedby={describedBy}
          className={control}
        >
          <option value="" disabled={field.validation.required}>
            Choose…
          </option>
          {choices.map((choice) => (
            <option key={choice} value={choice}>
              {choice}
            </option>
          ))}
        </select>
      ) : (
        <input
          id={id}
          name={field.name}
          type={field.secret ? "password" : "text"}
          autoComplete="off"
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          disabled={disabled}
          aria-invalid={Boolean(problem)}
          aria-describedby={describedBy}
          className={control}
        />
      )}
      {field.help && (
        <small
          id={helpId}
          className="text-[11px] leading-snug text-[var(--tone-929292)]"
        >
          {field.help}
        </small>
      )}
      {problem && (
        <small
          id={problemId}
          className="text-[11px] leading-snug text-[var(--tone-f0aaaa)]"
        >
          {problem}
        </small>
      )}
    </div>
  )
}
