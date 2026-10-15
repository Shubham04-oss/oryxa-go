import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ApiError } from "@/lib/api/client"
import { authErrorMessage } from "@/lib/auth/client"
import type { Organization } from "@/lib/auth/client"
import { getTeam, listTeams, setTeamEmailNotices } from "@/lib/settings/client"
import type { Team } from "@/lib/settings/client"
import {
  SettingsGroup,
  SettingsMessage,
  SettingsNote,
  SettingsRow,
  SettingsSwitch,
} from "./settings-kit"

/**
 * Settings → Notifications. Where each kind of notice goes (in app, email,
 * Slack) and quiet hours wait for a notification-preferences route, so they
 * are one honest line; the team parley emails, which Go serves, are here.
 */
export function WorkspaceNotificationSettings({
  organization,
  preview,
}: {
  organization?: Organization
  preview: boolean
}) {
  const teams = useQuery({
    queryKey: ["settings", "teams", organization?.id],
    queryFn: () => listTeams(organization!.id),
    enabled: !preview && Boolean(organization),
    retry: false,
  })
  const mine = teams.data?.filter((team) => Boolean(team.role)) ?? []

  return (
    <>
      <SettingsGroup label="What and where">
        <SettingsNote>
          Choosing where you hear about asks, parleys and mentions is coming
          soon. Until then, oryxa tells you in the app.
        </SettingsNote>
      </SettingsGroup>

      <SettingsGroup label="Parley emails by team">
        {preview || !organization ? (
          <SettingsNote>Sign in to manage your team emails.</SettingsNote>
        ) : teams.isPending ? (
          <SettingsNote>Loading your teams…</SettingsNote>
        ) : teams.isError ? (
          <SettingsMessage tone="error">
            {teams.error instanceof ApiError && teams.error.status === 404
              ? "Team emails are not available in this environment."
              : authErrorMessage(teams.error, "Could not load teams.")}
          </SettingsMessage>
        ) : mine.length === 0 ? (
          <SettingsNote>You are not on any teams yet.</SettingsNote>
        ) : (
          mine.map((team) => (
            <TeamParleyEmailRow
              key={team.id}
              team={team}
              organizationId={organization.id}
            />
          ))
        )}
      </SettingsGroup>
    </>
  )
}

function TeamParleyEmailRow({
  team,
  organizationId,
}: {
  team: Team
  organizationId: string
}) {
  const queryClient = useQueryClient()
  const key = ["settings", "teams", organizationId, team.id, "detail"]
  const detail = useQuery({
    queryKey: key,
    queryFn: () => getTeam(organizationId, team.id),
    retry: false,
  })
  const save = useMutation({
    mutationFn: (enabled: boolean) =>
      setTeamEmailNotices(organizationId, team.id, enabled),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: key }),
  })
  const checked = save.isPending
    ? save.variables
    : detail.data?.membership?.notify_email
  return (
    <>
      <SettingsRow
        title={team.name}
        description={
          detail.isError
            ? "Could not load this team's preference."
            : "Email when a parley starts without you"
        }
      >
        <SettingsSwitch
          label={`Parley emails for ${team.name}`}
          checked={Boolean(checked)}
          disabled={detail.isPending || detail.isError || save.isPending}
          onCheckedChange={(next) => save.mutate(next)}
        />
      </SettingsRow>
      {save.isError && (
        <SettingsMessage tone="error">
          {authErrorMessage(save.error, "Could not save team emails.")}
        </SettingsMessage>
      )}
    </>
  )
}
