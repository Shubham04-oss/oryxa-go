// One-time secrets on a request card. A secret is never in Slack, in an event,
// in the ledger or in a model's context: the owner of a request approved with
// access pastes it in themselves, the member who asked opens it once, and the
// owner can take it back first.
//
// What a secret holds is kept out of everything that outlives the screen. The
// owner's field is uncontrolled, read from its ref at submit and cleared once
// sent, so the value is never React state. The asker's value is React state of
// this component alone, until they press Done or leave. Neither goes through
// TanStack Query (a mutation's variables and data stay in its cache after it
// settles), and nothing here logs.
import { useEffect, useId, useRef, useState } from "react"
import type { ClipboardEvent, FormEvent } from "react"
import { Check, Copy, KeyRound } from "lucide-react"
import { agentsErrorMessage } from "./agents-format"
import { clockTime } from "./direct-message-format"
import { whenPhrase } from "./request-card-format"
import {
  SECRET_MAX_TTL_SECONDS,
  isSecretGone,
  openAgentRequestSecret,
  revokeAgentRequestSecret,
  shareAgentRequestSecret,
} from "@/lib/agents/agent-requests"
import type { AgentRequest } from "@/lib/agents/agent-requests"

/** What a secret may hold: Go's bound, in characters as it counts them. */
const MAX_SECRET_CHARACTERS = 16_000

/** How long a shared secret waits to be opened: the choices, in seconds. */
const lifetimes: ReadonlyArray<{ seconds: number; label: string }> = [
  { seconds: 600, label: "10 minutes" },
  { seconds: 1800, label: "30 minutes" },
  { seconds: SECRET_MAX_TTL_SECONDS, label: "1 hour" },
]

const copy = {
  secret_gone:
    "There is no secret to open. It was already opened, taken back or has expired, or it was never shared.",
  request_expired: "This request has expired, so nothing more can be shared.",
  request_closed: "This request has been closed.",
  invalid_state: "A secret can only be shared once the request is approved.",
  invalid_request:
    "A secret can only be shared when the request was approved with access.",
  unavailable_here: "One-time secrets aren't kept on this server.",
  secrets_unavailable: "One-time secrets aren't kept on this server.",
  forbidden: "Only the person this request is addressed to can share a secret.",
} as const

/**
 * The owner sharing a secret with the member who asked, for a request they
 * approved with access: pasted here, sent to Oryxa, never answered back. The
 * asker opens it once; the owner can take it back before they do or it
 * expires. It waits at most an hour, and Go may make it less.
 */
export function ShareSecret({
  organizationId,
  request,
  askerName,
  now,
}: {
  organizationId: string
  request: AgentRequest
  /** The asker's first name, as a sentence says it. */
  askerName: string
  now: number
}) {
  const [open, setOpen] = useState(false)
  const [pending, setPending] = useState(false)
  const [seconds, setSeconds] = useState<number>(SECRET_MAX_TTL_SECONDS)
  const [shared, setShared] = useState<{ expiresAt: string }>()
  const [takenBack, setTakenBack] = useState(false)
  const [problem, setProblem] = useState("")
  // The value lives in this field alone, read at submit and cleared once sent.
  const field = useRef<HTMLInputElement>(null)
  const fieldId = useId()
  const lifetimeId = useId()
  const hintId = useId()
  const viewed = request.secret_viewed_at

  useEffect(() => {
    if (open) field.current?.focus()
  }, [open])

  function refuseLines(event: ClipboardEvent<HTMLInputElement>) {
    // A single-line field keeps no newline: a key pasted across lines would
    // be joined into a value that is not the key, so it is refused.
    if (/[\r\n]/.test(event.clipboardData.getData("text"))) {
      event.preventDefault()
      setProblem(
        "That has more than one line, and this field keeps one. Share where a long secret, such as a private key, lives instead."
      )
    }
  }

  async function share(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending) return
    const value = field.current?.value ?? ""
    if (value.trim() === "") {
      setProblem("Paste the secret first.")
      return
    }
    if ([...value].length > MAX_SECRET_CHARACTERS) {
      setProblem("That is too long: a secret holds at most 16,000 characters.")
      return
    }
    setPending(true)
    setProblem("")
    setTakenBack(false)
    try {
      const result = await shareAgentRequestSecret(organizationId, request.id, {
        value,
        ttlSeconds: seconds,
      })
      if (field.current) field.current.value = ""
      setShared({ expiresAt: result.expires_at })
      setOpen(false)
    } catch (error) {
      setProblem(
        agentsErrorMessage(error, "Couldn't share it. Try again.", copy)
      )
    } finally {
      setPending(false)
    }
  }

  async function takeBack() {
    if (pending) return
    setPending(true)
    setProblem("")
    try {
      await revokeAgentRequestSecret(organizationId, request.id)
      setShared(undefined)
      setTakenBack(true)
    } catch (error) {
      setProblem(
        agentsErrorMessage(error, "Couldn't take it back. Try again.", copy)
      )
    } finally {
      setPending(false)
    }
  }

  const expires = shared ? Date.parse(shared.expiresAt) : undefined
  const waiting = expires !== undefined && expires > now

  return (
    <div className="workspace-request-secret" data-kind="share">
      <p className="workspace-request-secret-head">
        <KeyRound size={13} strokeWidth={1.8} aria-hidden="true" />
        <span>Need to send a credential?</span>
      </p>
      <p className="workspace-request-secret-note">
        Share it here, never in Slack: you paste it in yourself, it goes
        straight to Oryxa without passing an agent, and {askerName} opens it
        once, in Oryxa.
      </p>
      {waiting && shared ? (
        <p className="workspace-request-secret-state" role="status">
          Shared. {askerName} can open it once, until{" "}
          {clockTime(shared.expiresAt)}.
        </p>
      ) : (
        takenBack && (
          <p className="workspace-request-secret-state" role="status">
            Taken back. {askerName} can't open it.
          </p>
        )
      )}
      {viewed && (
        <p className="workspace-request-secret-state" role="status">
          {askerName} opened it{whenPhrase(viewed, now)}. It can't be opened
          again.
        </p>
      )}
      {open ? (
        <form className="workspace-request-secret-form" onSubmit={share}>
          <input
            id={fieldId}
            ref={field}
            type="password"
            name="secret"
            aria-label={`The secret to share with ${askerName}`}
            autoComplete="off"
            spellCheck={false}
            autoCapitalize="off"
            autoCorrect="off"
            data-1p-ignore="true"
            data-lpignore="true"
            placeholder={`The secret for ${askerName}`}
            disabled={pending}
            aria-describedby={hintId}
            onPaste={refuseLines}
          />
          <div className="workspace-request-secret-lifetime">
            <label htmlFor={lifetimeId}>Can be opened for</label>
            <select
              id={lifetimeId}
              value={seconds}
              disabled={pending}
              onChange={(event) => setSeconds(Number(event.target.value))}
            >
              {lifetimes.map((choice) => (
                <option key={choice.seconds} value={choice.seconds}>
                  {choice.label}
                </option>
              ))}
            </select>
          </div>
          <p className="workspace-request-secret-hint" id={hintId}>
            One line, at most 16,000 characters. Your organization may make it
            expire sooner.
          </p>
          <div className="workspace-request-actions">
            <button
              className="workspace-agent-pill"
              type="button"
              disabled={pending}
              onClick={() => {
                if (field.current) field.current.value = ""
                setProblem("")
                setOpen(false)
              }}
            >
              Cancel
            </button>
            <button
              className="workspace-agent-pill"
              data-primary="true"
              type="submit"
              disabled={pending}
            >
              {pending ? "Sharing…" : `Share with ${askerName}`}
            </button>
          </div>
        </form>
      ) : (
        <div className="workspace-request-actions" data-align="start">
          <button
            className="workspace-agent-pill"
            type="button"
            disabled={pending}
            onClick={() => {
              setProblem("")
              setTakenBack(false)
              setOpen(true)
            }}
          >
            {waiting ? "Share another" : "Share a secret"}
          </button>
          <button
            className="workspace-agent-text-button"
            type="button"
            disabled={pending}
            onClick={() => void takeBack()}
          >
            Take back a shared secret
          </button>
        </div>
      )}
      {problem && (
        <p className="workspace-agent-error" role="alert">
          {problem}
        </p>
      )}
    </div>
  )
}

type Opening =
  | { step: "idle" }
  | { step: "confirming" }
  | { step: "opening" }
  | { step: "open"; value: string }
  | { step: "done" }
  | { step: "gone" }

/**
 * The member who asked opening, once, the secret their owner shared. It can be
 * opened one time: Go deletes it as it answers, so the button says so first,
 * and what it shows is held here alone until Done, or until the card goes.
 */
export function OpenSecret({
  organizationId,
  request,
  ownerName,
  now,
}: {
  organizationId: string
  request: AgentRequest
  /** The owner's first name, as a sentence says it. */
  ownerName: string
  now: number
}) {
  const [opening, setOpening] = useState<Opening>({ step: "idle" })
  const [problem, setProblem] = useState("")
  const [copied, setCopied] = useState(false)
  const shown = useRef<HTMLInputElement>(null)
  const valueId = useId()
  const viewed = request.secret_viewed_at

  useEffect(() => {
    if (opening.step === "open") shown.current?.focus()
  }, [opening.step])

  // A copied flag that clears itself, and a timer that does not outlive the card.
  useEffect(() => {
    if (!copied) return
    const timer = window.setTimeout(() => setCopied(false), 2000)
    return () => window.clearTimeout(timer)
  }, [copied])

  async function open() {
    setProblem("")
    setOpening({ step: "opening" })
    try {
      const secret = await openAgentRequestSecret(organizationId, request.id)
      setOpening({ step: "open", value: secret.value })
    } catch (error) {
      if (isSecretGone(error)) {
        setOpening({ step: "gone" })
        return
      }
      setOpening({ step: "idle" })
      setProblem(
        agentsErrorMessage(error, "Couldn't open it. Try again.", copy)
      )
    }
  }

  async function copyValue(value: string) {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
    } catch {
      // No clipboard here (plain http, a denied permission): it is selected
      // for them to copy.
      shown.current?.select()
    }
  }

  if (opening.step === "open") {
    return (
      <div className="workspace-request-secret" data-kind="open">
        <p className="workspace-request-secret-head">
          <KeyRound size={13} strokeWidth={1.8} aria-hidden="true" />
          <span>The secret from {ownerName}</span>
        </p>
        <div className="workspace-request-secret-value">
          <label htmlFor={valueId} className="sr-only">
            The secret from {ownerName}
          </label>
          <input
            id={valueId}
            ref={shown}
            readOnly
            spellCheck={false}
            value={opening.value}
            onFocus={(event) => event.currentTarget.select()}
          />
          <button
            className="workspace-agent-pill"
            type="button"
            aria-label="Copy the secret"
            onClick={() => void copyValue(opening.value)}
          >
            {copied ? (
              <Check size={13} strokeWidth={1.8} aria-hidden="true" />
            ) : (
              <Copy size={13} strokeWidth={1.8} aria-hidden="true" />
            )}
            {copied ? "Copied" : "Copy"}
          </button>
        </div>
        <p className="workspace-request-secret-note">
          This was the one time. Put it somewhere safe now: once you press Done
          it is gone, and {ownerName} would have to share it again.
        </p>
        <div className="workspace-request-actions" data-align="start">
          <button
            className="workspace-agent-pill"
            data-primary="true"
            type="button"
            onClick={() => setOpening({ step: "done" })}
          >
            Done
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="workspace-request-secret" data-kind="open">
      <p className="workspace-request-secret-head">
        <KeyRound size={13} strokeWidth={1.8} aria-hidden="true" />
        <span>Did {ownerName} share a secret with you?</span>
      </p>
      <p className="workspace-request-secret-note">
        A secret can be opened once, here, and is deleted as you open it.
      </p>
      {opening.step === "gone" && (
        <p className="workspace-request-secret-state" role="status">
          {copy.secret_gone}
        </p>
      )}
      {opening.step === "done" && (
        <p className="workspace-request-secret-state" role="status">
          You opened it. It can't be opened again.
        </p>
      )}
      {opening.step !== "gone" && opening.step !== "done" && viewed && (
        <p className="workspace-request-secret-state" role="status">
          You opened a secret{whenPhrase(viewed, now)}. It can't be opened
          again.
        </p>
      )}
      {opening.step === "confirming" ? (
        <div className="workspace-request-confirm">
          <p>
            Open it now? It is shown once, and gone when you close it. Have
            somewhere ready to put it.
          </p>
          <div className="workspace-request-actions" data-align="start">
            <button
              className="workspace-agent-pill"
              type="button"
              onClick={() => setOpening({ step: "idle" })}
            >
              Not now
            </button>
            <button
              className="workspace-agent-pill"
              data-primary="true"
              type="button"
              onClick={() => void open()}
            >
              Open it once
            </button>
          </div>
        </div>
      ) : (
        <div className="workspace-request-actions" data-align="start">
          <button
            className="workspace-agent-pill"
            type="button"
            disabled={opening.step === "opening"}
            onClick={() => {
              setProblem("")
              setOpening({ step: "confirming" })
            }}
          >
            {opening.step === "opening" ? "Opening…" : "Open the secret"}
          </button>
        </div>
      )}
      {problem && (
        <p className="workspace-agent-error" role="alert">
          {problem}
        </p>
      )}
    </div>
  )
}
