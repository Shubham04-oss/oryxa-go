import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ApiError } from "@/lib/api/client"
import { authErrorMessage, requestPasswordReset } from "@/lib/auth/client"
import type { Organization, Role, Session } from "@/lib/auth/client"
import {
  authorizeAgentGrant,
  getAgentGrant,
  getAppPreferences,
  revokeAgentGrant,
  setAppUpdates,
} from "@/lib/auth/settings"
import { getTeam, listTeams, setTeamEmailNotices } from "@/lib/settings/client"
import type { Team } from "@/lib/settings/client"
import type { AvatarIdentity } from "./workspace-avatar"
import { IdentityAvatar } from "./workspace-avatar"

export function WorkspaceAccountSettings({
  identity,
  session,
  organization,
  role,
  preview,
  onSignOut,
  signingOut,
  signOutError,
}: {
  identity: AvatarIdentity
  session?: Session
  organization?: Organization
  role?: Role
  preview: boolean
  onSignOut: () => void
  signingOut: boolean
  signOutError: string
}) {
  const queryClient = useQueryClient()
  const [confirmRevoke, setConfirmRevoke] = useState(false)
  const reset = useMutation({
    mutationFn: () => requestPasswordReset(session!.user.email),
  })
  const preferences = useQuery({
    queryKey: ["settings", "app-preferences", organization?.id],
    queryFn: () => getAppPreferences(organization!.id),
    enabled: !preview && Boolean(organization),
    retry: false,
  })
  const updatePreferences = useMutation({
    mutationFn: (enabled: boolean) => setAppUpdates(organization!.id, enabled),
    onSuccess: (saved) =>
      queryClient.setQueryData(
        ["settings", "app-preferences", organization?.id],
        saved
      ),
  })
  const grant = useQuery({
    queryKey: ["auth", "agent-grant"],
    queryFn: getAgentGrant,
    enabled: !preview,
    retry: false,
  })
  const authorize = useMutation({
    mutationFn: () => authorizeAgentGrant(window.location.href),
    onSuccess: ({ authorization_url }) =>
      window.location.assign(authorization_url),
  })
  const revoke = useMutation({
    mutationFn: revokeAgentGrant,
    onSuccess: async () => {
      setConfirmRevoke(false)
      await queryClient.invalidateQueries({ queryKey: ["auth", "agent-grant"] })
    },
  })
  const appUpdates = updatePreferences.isPending
    ? updatePreferences.variables
    : preferences.data?.app_updates
  const grantUnavailable =
    grant.error instanceof ApiError && grant.error.status === 404
  // Agents answers 503 when it runs without its model or preferences store,
  // which no retry will change.
  const preferencesUnavailable =
    preferences.error instanceof ApiError &&
    (preferences.error.status === 404 || preferences.error.status === 503)
  const teams = useQuery({
    queryKey: ["settings", "teams", organization?.id],
    queryFn: () => listTeams(organization!.id),
    enabled: !preview && Boolean(organization),
    retry: false,
  })

  return (
    <div className="workspace-settings-account">
      <div className="workspace-settings-account-card">
        <IdentityAvatar identity={identity} />
        <div>
          <strong>{identity.name}</strong>
          <small>{session?.user.email || "Preview account"}</small>
        </div>
      </div>
      <dl>
        <div>
          <dt>Organisation</dt>
          <dd>{organization?.name || (preview ? "Oryxa" : "—")}</dd>
        </div>
        <div>
          <dt>Role</dt>
          <dd>{role || (preview ? "Owner · preview" : "Checking role")}</dd>
        </div>
      </dl>

      <section
        className="workspace-settings-section"
        aria-label="Sign-in settings"
      >
        <div className="workspace-settings-section-title">
          <h3>Sign-in</h3>
          <p>Your account and password.</p>
        </div>
        <div className="workspace-settings-row">
          <div>
            <strong>Password</strong>
            <small>We’ll email you a link to choose a new password.</small>
          </div>
          <button
            type="button"
            disabled={preview || reset.isPending || reset.isSuccess}
            onClick={() => reset.mutate()}
          >
            {reset.isPending
              ? "Sending…"
              : reset.isSuccess
                ? "Link sent"
                : "Send reset link"}
          </button>
        </div>
        {reset.isSuccess && (
          <p className="workspace-settings-notice" role="status">
            If this account has a password, a reset link is on its way to{" "}
            {session?.user.email}.
          </p>
        )}
        {reset.isError && (
          <p className="workspace-settings-error" role="alert">
            {authErrorMessage(reset.error, "Could not send a reset link.")}
          </p>
        )}
        <div className="workspace-settings-row">
          <div>
            <strong>Sign out</strong>
            <small>Leave this account on this browser.</small>
          </div>
          <button
            type="button"
            disabled={preview || signingOut}
            onClick={onSignOut}
          >
            {signingOut ? "Signing out…" : "Sign out"}
          </button>
        </div>
        {signOutError && (
          <p className="workspace-settings-error" role="alert">
            {signOutError}
          </p>
        )}
      </section>

      <section
        className="workspace-settings-section"
        aria-label="Agent permissions"
      >
        <div className="workspace-settings-section-title">
          <h3>What your agents may do</h3>
          <p>Control work that can happen while you’re away.</p>
        </div>
        <div className="workspace-settings-row">
          <div>
            <strong>Background agent access</strong>
            <small>
              {preview
                ? "Sign in to manage access."
                : grant.isPending
                  ? "Checking access…"
                  : grantUnavailable
                    ? "Not available on this server."
                    : grant.isError
                      ? "Could not check access."
                      : grant.data.granted
                        ? "Your agents can act for you while you’re away."
                        : "Your agents cannot act for you while you’re away."}
            </small>
          </div>
          {!preview &&
            !grant.isPending &&
            !grant.isError &&
            (grant.data.granted ? (
              <button type="button" onClick={() => setConfirmRevoke(true)}>
                Revoke
              </button>
            ) : (
              <button
                type="button"
                disabled={authorize.isPending}
                onClick={() => authorize.mutate()}
              >
                {authorize.isPending ? "Redirecting…" : "Turn on"}
              </button>
            ))}
          {grant.isError && !grantUnavailable && (
            <button type="button" onClick={() => void grant.refetch()}>
              Retry
            </button>
          )}
        </div>
        {confirmRevoke && (
          <div className="workspace-settings-confirm">
            <p>
              Your agents will stop acting for you while you’re away. You can
              grant access again later.
            </p>
            <button type="button" onClick={() => setConfirmRevoke(false)}>
              Cancel
            </button>
            <button
              type="button"
              disabled={revoke.isPending}
              onClick={() => revoke.mutate()}
            >
              {revoke.isPending ? "Revoking…" : "Revoke access"}
            </button>
          </div>
        )}
        {(authorize.isError || revoke.isError) && (
          <p className="workspace-settings-error" role="alert">
            {authErrorMessage(
              authorize.error || revoke.error,
              "Could not change agent access."
            )}
          </p>
        )}
        <div className="workspace-settings-row">
          <div>
            <strong>Updates from your apps</strong>
            <small>
              {preview
                ? "Sign in to manage updates."
                : preferences.isPending
                  ? "Checking your preference…"
                  : preferencesUnavailable
                    ? "Not available on this server."
                    : preferences.isError
                      ? "Could not load your preference."
                      : appUpdates
                        ? "Your agent briefs you when connected apps report something."
                        : "Off. New app updates will not be kept."}
            </small>
          </div>
          {!preview && !preferences.isPending && !preferences.isError && (
            <button
              type="button"
              role="switch"
              aria-label="Updates from your apps"
              aria-checked={Boolean(appUpdates)}
              className="workspace-settings-switch"
              disabled={updatePreferences.isPending}
              onClick={() => updatePreferences.mutate(!appUpdates)}
            >
              <span />
            </button>
          )}
          {preferences.isError && !preferencesUnavailable && (
            <button type="button" onClick={() => void preferences.refetch()}>
              Retry
            </button>
          )}
        </div>
        {updatePreferences.isError && (
          <p className="workspace-settings-error" role="alert">
            {authErrorMessage(
              updatePreferences.error,
              "Could not save your preference."
            )}
          </p>
        )}
      </section>
      <section
        className="workspace-settings-section"
        aria-label="Team call emails"
      >
        <div className="workspace-settings-section-title">
          <h3>Team call emails</h3>
          <p>Choose which teams email you when an assistant calls them.</p>
        </div>
        {preview ? (
          <p className="workspace-settings-hint">
            Sign in to manage your team notices.
          </p>
        ) : teams.isPending ? (
          <p role="status">Loading your teams…</p>
        ) : teams.isError ? (
          <p className="workspace-settings-error" role="alert">
            {teams.error instanceof ApiError && teams.error.status === 404
              ? "Team email settings are not available in this environment."
              : authErrorMessage(teams.error, "Could not load teams.")}
          </p>
        ) : teams.data.filter((team) => Boolean(team.role)).length === 0 ? (
          <p className="workspace-settings-hint">
            You are not on any teams yet.
          </p>
        ) : (
          teams.data
            .filter((team) => Boolean(team.role))
            .map((team) => (
              <TeamCallEmailRow
                key={team.id}
                team={team}
                orgId={organization!.id}
              />
            ))
        )}
      </section>
    </div>
  )
}

function TeamCallEmailRow({ team, orgId }: { team: Team; orgId: string }) {
  const queryClient = useQueryClient()
  const key = ["settings", "teams", orgId, team.id, "detail"]
  const detail = useQuery({
    queryKey: key,
    queryFn: () => getTeam(orgId, team.id),
    retry: false,
  })
  const save = useMutation({
    mutationFn: (enabled: boolean) =>
      setTeamEmailNotices(orgId, team.id, enabled),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: key }),
  })
  const checked = save.isPending
    ? save.variables
    : detail.data?.membership?.notify_email
  return (
    <div className="workspace-settings-row">
      <div>
        <strong>{team.name}</strong>
        <small>
          {detail.isError
            ? "Could not load this team’s preference."
            : checked
              ? "Emails you when this team is called."
              : "No team call emails."}
        </small>
      </div>
      <button
        type="button"
        role="switch"
        aria-label={`Team call emails for ${team.name}`}
        aria-checked={Boolean(checked)}
        className="workspace-settings-switch"
        disabled={detail.isPending || detail.isError || save.isPending}
        onClick={() => save.mutate(!checked)}
      >
        <span />
      </button>
      {save.isError && (
        <p className="workspace-settings-error" role="alert">
          {authErrorMessage(save.error, "Could not save team emails.")}
        </p>
      )}
    </div>
  )
}
