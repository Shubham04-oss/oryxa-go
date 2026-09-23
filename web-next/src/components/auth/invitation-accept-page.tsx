import { useEffect, useState } from "react"
import { Link, useNavigate } from "@tanstack/react-router"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowRight } from "lucide-react"
import { ApiError } from "@/lib/api/client"
import { getSession } from "@/lib/auth/client"
import { acceptInvitationToken } from "@/lib/auth/invitations"
import { AuthNotice, AuthShell } from "./auth-shell"

export function InvitationAcceptPage({ token }: { token?: string }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [errorMessage, setErrorMessage] = useState("")
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
      if (error instanceof ApiError) {
        if (error.status === 403) {
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

  return (
    <AuthShell>
      <h1>Join your team.</h1>
      <p className="auth-subtitle">
        {token
          ? "Your invitation is ready. Accept it to enter the workspace."
          : "This invitation link is incomplete."}
      </p>
      {errorMessage && <AuthNotice>{errorMessage}</AuthNotice>}
      {token && !session.error && session.data && (
        <button
          className="auth-submit"
          type="button"
          disabled={accept.isPending}
          onClick={() => {
            setErrorMessage("")
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
          search={{ org: undefined, view: undefined, preview: undefined }}
        >
          Back to workspace
        </Link>
      </p>
    </AuthShell>
  )
}
