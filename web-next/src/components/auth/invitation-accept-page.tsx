import { useEffect, useState } from "react"
import { Link, useNavigate } from "@tanstack/react-router"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowRight } from "lucide-react"
import { ApiError } from "@/lib/api/client"
import { getSession, signOut } from "@/lib/auth/client"
import { acceptInvitationToken } from "@/lib/auth/invitations"
import { AuthNotice, AuthShell } from "./auth-shell"

export function InvitationAcceptPage({ token }: { token?: string }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [errorMessage, setErrorMessage] = useState("")
  const [wrongAccount, setWrongAccount] = useState(false)
  const [signingOut, setSigningOut] = useState(false)
  const [signOutError, setSignOutError] = useState("")
  const session = useQuery({
    queryKey: ["auth", "session"],
    queryFn: getSession,
    enabled: Boolean(token),
    retry: false,
  })
  const accept = useMutation({
    mutationFn: () => acceptInvitationToken(token!),
    onSuccess: async (membership) => {
      await queryClient.invalidateQueries({ queryKey: ["auth", "session"] })
      await navigate({
        href: `/?org=${encodeURIComponent(membership.organization_id)}`,
      })
    },
    onError: (error) => {
      setWrongAccount(false)
      if (error instanceof ApiError) {
        if (error.status === 403) {
          setWrongAccount(true)
          setErrorMessage(
            "Sign in with the email address this invitation was sent to."
          )
          return
        }
        if (error.status === 402) {
          setErrorMessage(
            "This workspace has reached its member limit. Ask an owner to raise it."
          )
          return
        }
        if ([404, 409, 410].includes(error.status)) {
          setErrorMessage(
            "This invitation is no longer available. Ask your workspace admin for a new one."
          )
          return
        }
      }
      setErrorMessage("Could not accept this invitation. Try again shortly.")
    },
  })

  useEffect(() => {
    if (!(session.error instanceof ApiError) || session.error.status !== 401)
      return
    const destination = `${window.location.pathname}${window.location.search}`
    window.location.replace(
      `/auth/sign-in?redirect=${encodeURIComponent(destination)}`
    )
  }, [session.error])

  // The invitation was sent to a different address than whoever is signed
  // in right now. Signing out and sending them to sign-in with this page as
  // the redirect brings them straight back here once they are that person.
  async function signOutAndSwitch() {
    setSigningOut(true)
    setSignOutError("")
    try {
      await signOut()
      const destination = `${window.location.pathname}${window.location.search}`
      window.location.replace(
        `/auth/sign-in?redirect=${encodeURIComponent(destination)}`
      )
    } catch {
      setSignOutError("Could not sign out. Try again.")
      setSigningOut(false)
    }
  }

  return (
    <AuthShell>
      <h1>Join your team.</h1>
      <p className="auth-subtitle">
        {token
          ? "Your invitation is ready. Accept it to enter the workspace."
          : "This invitation link is incomplete."}
      </p>
      {errorMessage && <AuthNotice>{errorMessage}</AuthNotice>}
      {wrongAccount && (
        <>
          <button
            className="auth-submit"
            type="button"
            disabled={signingOut}
            onClick={() => void signOutAndSwitch()}
          >
            {signingOut ? "Signing out…" : "Sign out and use another account"}
            <ArrowRight size={17} aria-hidden="true" />
          </button>
          {signOutError && <AuthNotice>{signOutError}</AuthNotice>}
        </>
      )}
      {token && !session.error && session.data && (
        <button
          className="auth-submit"
          type="button"
          disabled={accept.isPending}
          onClick={() => {
            setErrorMessage("")
            setWrongAccount(false)
            accept.mutate()
          }}
        >
          {accept.isPending ? "Joining…" : "Accept invitation"}
          <ArrowRight size={17} aria-hidden="true" />
        </button>
      )}
      {session.isPending && token && (
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
