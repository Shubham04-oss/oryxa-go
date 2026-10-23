import { useEffect, useState } from "react"
import type { FormEvent } from "react"
import { Link } from "@tanstack/react-router"
import { useMutation, useQuery } from "@tanstack/react-query"
import { ArrowRight } from "lucide-react"
import { ApiError } from "@/lib/api/client"
import { getSession } from "@/lib/auth/client"
import type { CliDeviceRequest } from "@/lib/auth/cli-device"
import { decideCliDevice, lookupCliDevice } from "@/lib/auth/cli-device"
import { AuthField } from "./auth-field"
import { AuthNotice, AuthShell } from "./auth-shell"

function deviceErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 404)
      return "This code is not valid or has expired. Run oryxa-cli login --device again for a new one."
    if (error.status === 429)
      return "Too many attempts. Wait a minute, then try again."
  }
  return "Could not reach Oryxa. Try again shortly."
}

function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime())
    ? value
    : date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
}

/**
 * Where a member approves a CLI's device sign-in (oryxa-cli login --device).
 * It shows which device asks and what it gets before anything is granted,
 * so a code someone else sent stands out.
 */
export function CliDevicePage({ code: initialCode }: { code?: string }) {
  const [code, setCode] = useState(initialCode?.trim() || "")
  const [decided, setDecided] = useState<"approved" | "denied">()
  const session = useQuery({
    queryKey: ["auth", "session"],
    queryFn: getSession,
    retry: false,
  })
  const request = useQuery({
    queryKey: ["auth", "cli-device", code],
    queryFn: () => lookupCliDevice(code),
    enabled: Boolean(code) && Boolean(session.data) && !decided,
    retry: false,
    staleTime: Infinity,
  })
  const decide = useMutation({
    mutationFn: (decision: "approve" | "deny") =>
      decideCliDevice(code, decision),
    onSuccess: (result) => setDecided(result.status),
  })

  useEffect(() => {
    if (!(session.error instanceof ApiError) || session.error.status !== 401)
      return
    const destination = `${window.location.pathname}${window.location.search}`
    window.location.replace(
      `/auth/sign-in?redirect=${encodeURIComponent(destination)}`
    )
  }, [session.error])

  function submitCode(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const value = new FormData(event.currentTarget).get("user_code")
    if (typeof value === "string" && value.trim()) setCode(value.trim())
  }

  const email = session.data?.user.email
  return (
    <AuthShell
      scene="harbour"
      caption={{
        line: "Signing in from another machine.",
        sub: "Approve it here only if you started it.",
      }}
    >
      <h1>Sign in the Oryxa CLI.</h1>
      {decided === "approved" && (
        <AuthNotice tone="success">
          Approved. Return to your terminal; the CLI signs in within a few
          seconds.
        </AuthNotice>
      )}
      {decided === "denied" && (
        <AuthNotice tone="success">
          Denied. That CLI gets nothing. If you didn’t start this sign-in, tell
          your workspace administrator.
        </AuthNotice>
      )}
      {!decided && session.data && !code && (
        <>
          <p className="auth-subtitle">
            Enter the code your terminal shows after{" "}
            <code>oryxa-cli login --device</code>.
          </p>
          <form className="auth-form" onSubmit={submitCode}>
            <AuthField
              autoComplete="one-time-code"
              label="Code"
              name="user_code"
              placeholder="XXXX-XXXX"
            />
            <button className="auth-submit" type="submit">
              Continue
              <ArrowRight size={17} aria-hidden="true" />
            </button>
          </form>
        </>
      )}
      {!decided && request.isPending && code && session.data && (
        <p className="auth-subtitle" role="status">
          Looking up this code…
        </p>
      )}
      {!decided && request.error && (
        <>
          <AuthNotice>{deviceErrorMessage(request.error)}</AuthNotice>
          <button
            className="auth-submit"
            type="button"
            onClick={() => setCode("")}
          >
            Enter another code
          </button>
        </>
      )}
      {!decided && request.data && (
        <DeviceRequest
          device={request.data}
          email={email}
          pending={decide.isPending}
          error={decide.error ? deviceErrorMessage(decide.error) : ""}
          onDecide={(decision) => decide.mutate(decision)}
        />
      )}
      {session.isPending && (
        <p className="auth-subtitle" role="status">
          Checking your account…
        </p>
      )}
      {session.error &&
        (!(session.error instanceof ApiError) ||
          session.error.status !== 401) && (
          <AuthNotice>
            We couldn’t check your account. Try again shortly.
          </AuthNotice>
        )}
      <p className="auth-footer">
        <Link
          to="/"
          search={{ org: undefined, view: undefined, dm: undefined }}
        >
          Back to workspace
        </Link>
      </p>
    </AuthShell>
  )
}

function DeviceRequest({
  device,
  email,
  pending,
  error,
  onDecide,
}: {
  device: CliDeviceRequest
  email?: string
  pending: boolean
  error: string
  onDecide: (decision: "approve" | "deny") => void
}) {
  return (
    <>
      <p className="auth-subtitle">
        A command line is asking to sign in{email ? ` as ${email}` : ""}.
        Approve only if you started it yourself, just now. If someone sent you
        this code, deny it.
      </p>
      <dl className="auth-device-details">
        <div>
          <dt>Code</dt>
          <dd>{device.user_code}</dd>
        </div>
        <div>
          <dt>Device</dt>
          <dd>{device.client_name}</dd>
        </div>
        <div>
          <dt>From address</dt>
          <dd>{device.client_address}</dd>
        </div>
        <div>
          <dt>Asked at</dt>
          <dd>
            {formatTime(device.requested_at)}, expires{" "}
            {formatTime(device.expires_at)}
          </dd>
        </div>
        <div>
          <dt>It gets</dt>
          <dd>{device.grants}</dd>
        </div>
      </dl>
      {error && <AuthNotice>{error}</AuthNotice>}
      <button
        className="auth-submit"
        type="button"
        disabled={pending}
        onClick={() => onDecide("approve")}
      >
        {pending ? "Saving…" : "Approve this CLI"}
        <ArrowRight size={17} aria-hidden="true" />
      </button>
      <button
        className="auth-submit auth-device-deny"
        type="button"
        disabled={pending}
        onClick={() => onDecide("deny")}
      >
        Deny
      </button>
    </>
  )
}
