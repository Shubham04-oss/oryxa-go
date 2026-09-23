import { createFileRoute } from "@tanstack/react-router"
import { useEffect, useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { WorkspaceShell } from "@/components/workspace-shell"
import { AuthField } from "@/components/auth/auth-field"
import { AuthNotice, AuthShell } from "@/components/auth/auth-shell"
import { ApiError } from "@/lib/api/client"
import {
  createOrganization,
  getOrganizationRole,
  getSession,
  signOut,
} from "@/lib/auth/client"
import type { Session } from "@/lib/auth/client"
import {
  acceptReceivedInvitation,
  getReceivedInvitations,
} from "@/lib/auth/invitations"

export const Route = createFileRoute("/")({
  validateSearch: (search: Record<string, unknown>) => ({
    org: typeof search.org === "string" ? search.org : undefined,
    view: typeof search.view === "string" ? search.view : undefined,
    preview: typeof search.preview === "string" ? search.preview : undefined,
  }),
  component: Home,
})

function Home() {
  const { org, preview } = Route.useSearch()
  const showPeoplePreview = import.meta.env.DEV && preview === "people"
  const session = useQuery({
    queryKey: ["auth", "session"],
    queryFn: getSession,
    retry: false,
    staleTime: 60_000,
    enabled: !showPeoplePreview,
  })
  const selectedOrganization =
    session.data?.organizations.find(
      (organization) => organization.id === org
    ) || session.data?.organizations[0]
  const organizationId = selectedOrganization?.id
  const role = useQuery({
    queryKey: ["auth", "role", organizationId],
    queryFn: () => getOrganizationRole(organizationId!),
    enabled: Boolean(organizationId) && !showPeoplePreview,
    retry: false,
  })

  useEffect(() => {
    if (showPeoplePreview) return
    if (!(session.error instanceof ApiError) || session.error.status !== 401)
      return
    const destination = `${window.location.pathname}${window.location.search}${window.location.hash}`
    window.location.replace(
      `/auth/sign-in?redirect=${encodeURIComponent(destination)}`
    )
  }, [session.error, showPeoplePreview])

  if (showPeoplePreview) {
    return <WorkspaceShell preview initialMode="team" initialSection="people" />
  }

  if (
    session.isPending ||
    (session.error instanceof ApiError && session.error.status === 401)
  ) {
    return (
      <div className="workspace-auth-loading" role="status">
        Opening your workspace…
      </div>
    )
  }

  if (session.isLoadingError) {
    if (import.meta.env.DEV) return <WorkspaceShell preview />
    return (
      <main className="workspace-auth-error">
        <h1>We couldn’t reach your workspace.</h1>
        <p>Check your connection and try again.</p>
        <button type="button" onClick={() => void session.refetch()}>
          Try again
        </button>
      </main>
    )
  }

  if (session.data.organizations.length === 0) {
    return <NewWorkspace session={session.data} />
  }

  const workspaceRole =
    role.data === "owner" || role.data === "admin" ? "admin" : "member"
  return (
    <WorkspaceShell
      session={session.data}
      role={workspaceRole}
      organizationRole={role.data}
      selectedOrganization={selectedOrganization}
    />
  )
}

function NewWorkspace({ session }: { session: Session }) {
  const queryClient = useQueryClient()
  const [nameError, setNameError] = useState("")
  const [formError, setFormError] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const invitations = useQuery({
    queryKey: ["auth", "received-invitations"],
    queryFn: getReceivedInvitations,
    retry: false,
  })
  const accept = useMutation({
    mutationFn: acceptReceivedInvitation,
    onSuccess: async (membership) => {
      await queryClient.invalidateQueries({ queryKey: ["auth", "session"] })
      window.location.assign(
        `/?org=${encodeURIComponent(membership.organization_id)}`
      )
    },
    onError: () =>
      setFormError(
        "Could not join that workspace. Ask for a fresh invitation."
      ),
  })

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const name = String(
      new FormData(event.currentTarget).get("organization") || ""
    ).trim()
    if (!name || new TextEncoder().encode(name).length > 120) {
      setNameError(name ? "Use a shorter name." : "Give your workspace a name.")
      return
    }
    setNameError("")
    setFormError("")
    setSubmitting(true)
    try {
      const organization = await createOrganization(name)
      queryClient.setQueryData<Session>(["auth", "session"], {
        ...session,
        organizations: [organization],
      })
    } catch (error) {
      setFormError(
        error instanceof ApiError && error.status === 400
          ? error.message
          : "Could not create your workspace. Try again shortly."
      )
      setSubmitting(false)
    }
  }

  async function leave() {
    try {
      await signOut()
      queryClient.clear()
      window.location.replace("/auth/sign-in")
    } catch {
      setFormError("Could not sign out. Try again shortly.")
    }
  }

  return (
    <AuthShell>
      <h1>Make room for your work.</h1>
      <p className="auth-subtitle">
        Name your workspace to bring your people and agents together.
      </p>
      {formError && <AuthNotice>{formError}</AuthNotice>}
      {Boolean(invitations.data?.length) && (
        <div className="auth-received-invitations">
          <p className="auth-received-heading">You have an invitation</p>
          {invitations.data?.map((invitation) => (
            <div className="auth-received-row" key={invitation.id}>
              <span>
                <strong>{invitation.organization_name}</strong>
                <small>Join as {invitation.role}</small>
              </span>
              <button
                type="button"
                disabled={accept.isPending}
                onClick={() => accept.mutate(invitation.id)}
              >
                {accept.isPending && accept.variables === invitation.id
                  ? "Joining…"
                  : "Join"}
              </button>
            </div>
          ))}
          <p className="auth-received-separator">or create a new workspace</p>
        </div>
      )}
      <form className="auth-form" onSubmit={submit} noValidate>
        <AuthField
          autoComplete="organization"
          error={nameError}
          label="Workspace name"
          name="organization"
          placeholder="Your team or company"
        />
        <button className="auth-submit" type="submit" disabled={submitting}>
          {submitting ? "Creating…" : "Create workspace"}
        </button>
      </form>
      <p className="auth-footer">
        <button
          className="auth-inline-link"
          type="button"
          onClick={() => void leave()}
        >
          Sign out
        </button>
      </p>
    </AuthShell>
  )
}
