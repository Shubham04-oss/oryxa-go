import { useEffect, useRef, useState } from "react"
import type { FormEvent } from "react"
import { Link, useNavigate } from "@tanstack/react-router"
import { useQueryClient } from "@tanstack/react-query"
import { ArrowRight } from "lucide-react"
import { ApiError } from "@/lib/api/client"
import {
  authErrorMessage,
  getAuthConfig,
  googleSignIn,
  requestPasswordReset,
  resendVerification,
  resetPassword,
  signIn,
  signUp,
  verifyEmail,
} from "@/lib/auth/client"
import {
  emailSchema,
  fieldErrors,
  resetPasswordSchema,
  signInSchema,
  signUpSchema,
} from "@/lib/auth/schemas"
import { AuthField } from "./auth-field"
import { AuthNotice, AuthShell } from "./auth-shell"

function useGoogleEnabled() {
  const [enabled, setEnabled] = useState(false)
  useEffect(() => {
    let active = true
    getAuthConfig()
      .then((config) => {
        if (active)
          setEnabled(config.identity_providers?.includes("Google") ?? false)
      })
      .catch(() => undefined)
    return () => {
      active = false
    }
  }, [])
  return enabled
}

function GoogleOption({ returnTo }: { returnTo: string }) {
  const enabled = useGoogleEnabled()
  return (
    <div className="auth-google-row">
      <button
        className="auth-google"
        type="button"
        aria-label={
          enabled ? "Continue with Google" : "Google sign-in unavailable"
        }
        title={
          enabled
            ? "Continue with Google"
            : "Google sign-in is not available yet"
        }
        disabled={!enabled}
        onClick={() => googleSignIn(returnTo)}
      >
        <svg viewBox="0 0 40 40" aria-hidden="true" focusable="false">
          <path
            d="M29.6 20.2273C29.6 19.5182 29.5364 18.8364 29.4182 18.1818H20V22.05H25.3818C25.15 23.3 24.4455 24.3591 23.3864 25.0682V27.5773H26.6182C28.5091 25.8364 29.6 23.2727 29.6 20.2273Z"
            fill="#4285F4"
          />
          <path
            d="M20 30C22.7 30 24.9636 29.1045 26.6181 27.5773L23.3863 25.0682C22.4909 25.6682 21.3454 26.0227 20 26.0227C17.3954 26.0227 15.1909 24.2636 14.4045 21.9H11.0636V24.4909C12.7091 27.7591 16.0909 30 20 30Z"
            fill="#34A853"
          />
          <path
            d="M14.4045 21.9C14.2045 21.3 14.0909 20.6591 14.0909 20C14.0909 19.3409 14.2045 18.7 14.4045 18.1V15.5091H11.0636C10.3864 16.8591 10 18.3864 10 20C10 21.6136 10.3864 23.1409 11.0636 24.4909L14.4045 21.9Z"
            fill="#FBBC04"
          />
          <path
            d="M20 13.9773C21.4681 13.9773 22.7863 14.4818 23.8227 15.4727L26.6909 12.6045C24.9591 10.9909 22.6954 10 20 10C16.0909 10 12.7091 12.2409 11.0636 15.5091L14.4045 18.1C15.1909 15.7364 17.3954 13.9773 20 13.9773Z"
            fill="#E94235"
          />
        </svg>
      </button>
    </div>
  )
}

function AuthSubmit({
  children,
  pending,
}: {
  children: string
  pending: boolean
}) {
  return (
    <button className="auth-submit" disabled={pending} type="submit">
      {children}
      <ArrowRight size={17} aria-hidden="true" />
    </button>
  )
}

export function SignInPage({ redirect }: { redirect?: string }) {
  const destination = redirect || "/"
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [formError, setFormError] = useState("")
  const [submitting, setSubmitting] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setFormError("")
    const data = new FormData(event.currentTarget)
    const result = signInSchema.safeParse({
      email: data.get("email"),
      password: data.get("password"),
    })
    if (!result.success) {
      setErrors(fieldErrors(result.error.issues))
      return
    }
    setErrors({})
    setSubmitting(true)
    try {
      await signIn(result.data)
      queryClient.clear()
      await navigate({ href: destination })
    } catch (error) {
      setFormError(
        error instanceof ApiError && error.status === 401
          ? "That email and password do not match."
          : authErrorMessage(
              error,
              "Sign-in is unavailable. Try again shortly."
            )
      )
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <AuthShell>
      <h1>Welcome back.</h1>
      <p className="auth-subtitle">
        Sign in to pick up where your work left off.
      </p>
      <GoogleOption returnTo={destination} />
      {formError && <AuthNotice>{formError}</AuthNotice>}
      <form className="auth-form" onSubmit={submit} noValidate>
        <AuthField
          autoComplete="email"
          error={errors.email}
          label="Email"
          name="email"
          placeholder="you@company.com"
          type="email"
        />
        <AuthField
          autoComplete="current-password"
          error={errors.password}
          label="Password"
          name="password"
          type="password"
        />
        <Link
          className="auth-inline-link auth-forgot"
          to="/auth/forgot-password"
        >
          Forgot password?
        </Link>
        <AuthSubmit pending={submitting}>
          {submitting ? "Signing in…" : "Sign in"}
        </AuthSubmit>
      </form>
      <p className="auth-footer">
        New to Oryxa?{" "}
        <Link to="/auth/sign-up" search={{ redirect }}>
          Create an account
        </Link>
      </p>
    </AuthShell>
  )
}

export function SignUpPage({ redirect }: { redirect?: string }) {
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [formError, setFormError] = useState("")
  const [submittedEmail, setSubmittedEmail] = useState("")
  const [submitting, setSubmitting] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setFormError("")
    const data = new FormData(event.currentTarget)
    const result = signUpSchema.safeParse({
      displayName: data.get("displayName"),
      email: data.get("email"),
      password: data.get("password"),
    })
    if (!result.success) {
      setErrors(fieldErrors(result.error.issues))
      return
    }
    setErrors({})
    setSubmitting(true)
    try {
      await signUp(result.data)
      setSubmittedEmail(result.data.email)
    } catch (error) {
      setFormError(
        authErrorMessage(
          error,
          "Account creation is unavailable. Try again shortly."
        )
      )
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <AuthShell>
      <h1>Create your space.</h1>
      <p className="auth-subtitle">
        A place for your team and agents to work together.
      </p>
      {submittedEmail ? (
        <AuthNotice tone="success">
          Check {submittedEmail} for a verification link, then sign in.
        </AuthNotice>
      ) : (
        <>
          <GoogleOption returnTo={redirect || "/"} />
          {formError && <AuthNotice>{formError}</AuthNotice>}
          <form className="auth-form" onSubmit={submit} noValidate>
            <AuthField
              autoComplete="name"
              error={errors.displayName}
              label="Your name"
              name="displayName"
              placeholder="What should we call you?"
            />
            <AuthField
              autoComplete="email"
              error={errors.email}
              label="Work email"
              name="email"
              placeholder="you@company.com"
              type="email"
            />
            <AuthField
              autoComplete="new-password"
              error={errors.password}
              label="Password"
              name="password"
              type="password"
            />
            <p className="auth-password-hint">
              12+ characters, with upper and lowercase letters, a number, and a
              symbol.
            </p>
            <AuthSubmit pending={submitting}>
              {submitting ? "Creating account…" : "Create account"}
            </AuthSubmit>
          </form>
        </>
      )}
      <p className="auth-footer">
        Already have an account?{" "}
        <Link to="/auth/sign-in" search={{ redirect }}>
          Sign in
        </Link>
      </p>
    </AuthShell>
  )
}

export function ForgotPasswordPage() {
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [formError, setFormError] = useState("")
  const [requestedFor, setRequestedFor] = useState("")
  const [submitting, setSubmitting] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setFormError("")
    const result = emailSchema.safeParse({
      email: new FormData(event.currentTarget).get("email"),
    })
    if (!result.success) {
      setErrors(fieldErrors(result.error.issues))
      return
    }
    setErrors({})
    setSubmitting(true)
    try {
      await requestPasswordReset(result.data.email)
      setRequestedFor(result.data.email)
    } catch (error) {
      setFormError(
        authErrorMessage(
          error,
          "Could not send a reset link. Try again shortly."
        )
      )
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <AuthShell>
      <h1>Reset your password.</h1>
      <p className="auth-subtitle">
        We’ll send you a link to choose a new one.
      </p>
      {requestedFor ? (
        <AuthNotice tone="success">
          If {requestedFor} has an account, a reset link is on its way.
        </AuthNotice>
      ) : (
        <>
          {formError && <AuthNotice>{formError}</AuthNotice>}
          <form className="auth-form" onSubmit={submit} noValidate>
            <AuthField
              autoComplete="email"
              error={errors.email}
              label="Email"
              name="email"
              placeholder="you@company.com"
              type="email"
            />
            <AuthSubmit pending={submitting}>
              {submitting ? "Sending…" : "Send reset link"}
            </AuthSubmit>
          </form>
        </>
      )}
      <p className="auth-footer">
        <Link to="/auth/sign-in" search={{ redirect: undefined }}>
          Back to sign in
        </Link>
      </p>
    </AuthShell>
  )
}

export function ResetPasswordPage({ token }: { token?: string }) {
  const [state, setState] = useState<"form" | "done" | "expired" | "invalid">(
    token ? "form" : "invalid"
  )
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [formError, setFormError] = useState("")
  const [submitting, setSubmitting] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!token) return
    setFormError("")
    const result = resetPasswordSchema.safeParse({
      password: new FormData(event.currentTarget).get("password"),
    })
    if (!result.success) {
      setErrors(fieldErrors(result.error.issues))
      return
    }
    setErrors({})
    setSubmitting(true)
    try {
      await resetPassword(token, result.data.password)
      setState("done")
    } catch (error) {
      if (error instanceof ApiError && error.status === 410) setState("expired")
      else if (
        error instanceof ApiError &&
        [401, 404, 409].includes(error.status)
      )
        setState("invalid")
      else
        setFormError(
          authErrorMessage(error, "Could not update your password. Try again.")
        )
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <AuthShell>
      <h1>
        {state === "done"
          ? "Password updated."
          : state === "form"
            ? "Choose a new password."
            : "This link isn’t available."}
      </h1>
      <p className="auth-subtitle">
        {state === "done"
          ? "Sign in with your new password. Other sessions have been signed out."
          : state === "expired"
            ? "Reset links expire after 30 minutes. Request a fresh one."
            : state === "invalid"
              ? "This link was already used or is incomplete."
              : "Use at least 12 characters, including upper and lowercase letters, a number, and a symbol."}
      </p>
      {state === "form" ? (
        <>
          {formError && <AuthNotice>{formError}</AuthNotice>}
          <form className="auth-form" onSubmit={submit} noValidate>
            <AuthField
              autoComplete="new-password"
              error={errors.password}
              label="New password"
              name="password"
              type="password"
            />
            <AuthSubmit pending={submitting}>
              {submitting ? "Updating…" : "Update password"}
            </AuthSubmit>
          </form>
        </>
      ) : (
        <Link
          className="auth-submit auth-link-button"
          to={state === "done" ? "/auth/sign-in" : "/auth/forgot-password"}
        >
          {state === "done" ? "Sign in" : "Send a new link"}
          <ArrowRight size={17} />
        </Link>
      )}
    </AuthShell>
  )
}

type Verification =
  "verifying" | "verified" | "expired" | "invalid" | "unavailable"

export function VerifyEmailPage({ token }: { token?: string }) {
  const [state, setState] = useState<Verification>(
    token ? "verifying" : "invalid"
  )
  const [attempt, setAttempt] = useState(0)
  const redeemedAttempt = useRef(-1)
  const outcome = useRef<Promise<Verification>>(undefined)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [formError, setFormError] = useState("")
  const [sentTo, setSentTo] = useState("")
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    if (!token) return
    let active = true
    if (redeemedAttempt.current !== attempt) {
      redeemedAttempt.current = attempt
      setState("verifying")
      outcome.current = verifyEmail(token).then(
        (): Verification => "verified",
        (error: unknown): Verification =>
          error instanceof ApiError && error.status === 410
            ? "expired"
            : error instanceof ApiError && error.status < 500
              ? "invalid"
              : "unavailable"
      )
    }
    outcome.current?.then((result) => {
      if (active) setState(result)
    })
    return () => {
      active = false
    }
  }, [attempt, token])

  async function resend(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setFormError("")
    const result = emailSchema.safeParse({
      email: new FormData(event.currentTarget).get("email"),
    })
    if (!result.success) {
      setErrors(fieldErrors(result.error.issues))
      return
    }
    setErrors({})
    setSubmitting(true)
    try {
      await resendVerification(result.data.email)
      setSentTo(result.data.email)
    } catch (error) {
      setFormError(
        authErrorMessage(error, "Could not send a new link. Try again shortly.")
      )
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <AuthShell>
      <h1>
        {state === "verified"
          ? "Email verified."
          : state === "verifying"
            ? "Verifying your email."
            : state === "unavailable"
              ? "Couldn’t verify yet."
              : "This link isn’t available."}
      </h1>
      <p className="auth-subtitle">
        {state === "verified"
          ? "Your account is ready. Sign in to get started."
          : state === "verifying"
            ? "This only takes a moment."
            : state === "unavailable"
              ? "The service could not be reached. Please try again."
              : state === "expired"
                ? "Verification links expire after 24 hours."
                : "This link was already used or is incomplete."}
      </p>
      {state === "verified" && (
        <Link
          className="auth-submit auth-link-button"
          to="/auth/sign-in"
          search={{ redirect: undefined }}
        >
          Sign in <ArrowRight size={17} />
        </Link>
      )}
      {state === "unavailable" && (
        <button
          className="auth-submit"
          type="button"
          onClick={() => setAttempt((current) => current + 1)}
        >
          Try again <ArrowRight size={17} />
        </button>
      )}
      {(state === "expired" || state === "invalid") &&
        (sentTo ? (
          <AuthNotice tone="success">
            Check {sentTo} for a new verification link.
          </AuthNotice>
        ) : (
          <>
            {formError && <AuthNotice>{formError}</AuthNotice>}
            <form className="auth-form" onSubmit={resend} noValidate>
              <AuthField
                autoComplete="email"
                error={errors.email}
                label="Email"
                name="email"
                placeholder="you@company.com"
                type="email"
              />
              <AuthSubmit pending={submitting}>
                {submitting ? "Sending…" : "Send a new link"}
              </AuthSubmit>
            </form>
          </>
        ))}
      {state !== "verified" && (
        <p className="auth-footer">
          <Link to="/auth/sign-in" search={{ redirect: undefined }}>
            Back to sign in
          </Link>
        </p>
      )}
    </AuthShell>
  )
}
