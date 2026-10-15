import { useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ApiError } from "@/lib/api/client"
import { authErrorMessage } from "@/lib/auth/client"
import type { Organization } from "@/lib/auth/client"
import {
  authorizeAgentGrant,
  getAgentGrant,
  getAppPreferences,
  revokeAgentGrant,
  setAppUpdates,
} from "@/lib/auth/settings"
import { updateAgent } from "@/lib/agents/managed-agents"
import { primaryAgent } from "@/lib/agents/policy"
import { agentKeys, agentsQueryOptions } from "@/lib/agents/queries"
import { connectionsQueryOptions, toolkitName } from "@/lib/apps/client"
import { Input } from "@/components/ui/input"
import { IdentityAvatar } from "./workspace-avatar"
import { WorkspaceAnswerCheckSettings } from "./workspace-answer-check-settings"
import {
  SettingsActions,
  SettingsButton,
  SettingsGroup,
  SettingsMessage,
  SettingsNote,
  SettingsRow,
  SettingsSwitch,
} from "./settings-kit"

/** "Slack", "Slack and Notion", "Slack, Notion and Drive". */
function listWords(words: readonly string[]): string {
  if (words.length <= 1) return words.join("")
  return `${words.slice(0, -1).join(", ")} and ${words[words.length - 1]}`
}

function unavailable(error: unknown): boolean {
  // A server without the route answers 404; agents answers 503 when it runs
  // without its preferences store, which no retry will change.
  return (
    error instanceof ApiError && (error.status === 404 || error.status === 503)
  )
}

/**
 * Settings → Your agent: the member's own agent, what it may do for them
 * while they are away, and what it hides when it answers.
 *
 * How it answers (suggest or act) has no per-member setting yet, so it is not
 * offered.
 */
export function WorkspaceAgentSettings({
  organization,
  viewerName,
  preview,
}: {
  organization?: Organization
  viewerName?: string
  preview: boolean
}) {
  if (preview || !organization) {
    return (
      <SettingsNote>Sign in to a workspace to manage your agent.</SettingsNote>
    )
  }
  return (
    <AgentSettings
      key={organization.id}
      organization={organization}
      viewerName={viewerName}
    />
  )
}

function AgentSettings({
  organization,
  viewerName,
}: {
  organization: Organization
  viewerName?: string
}) {
  const queryClient = useQueryClient()
  const organizationId = organization.id
  const agents = useQuery({
    ...agentsQueryOptions(organizationId),
    retry: false,
  })
  const agent = agents.data ? primaryAgent(agents.data) : undefined
  const connections = useQuery({
    ...connectionsQueryOptions(organizationId),
    retry: false,
  })
  const reads = [
    ...new Set(
      (connections.data ?? [])
        .filter((connection) => connection.status === "active")
        .map((connection) => toolkitName(connection.toolkit))
    ),
  ]

  const [renaming, setRenaming] = useState(false)
  const [name, setName] = useState("")
  const rename = useMutation({
    mutationFn: (next: string) =>
      updateAgent(organizationId, agent!.id, { name: next }),
    onSuccess: async () => {
      setRenaming(false)
      await queryClient.invalidateQueries({
        queryKey: agentKeys.agentList(organizationId),
      })
    },
  })
  function submitRename(event: FormEvent) {
    event.preventDefault()
    const next = name.trim()
    if (!next || next === agent?.name) return setRenaming(false)
    rename.mutate(next)
  }

  const grant = useQuery({
    queryKey: ["auth", "agent-grant"],
    queryFn: getAgentGrant,
    retry: false,
  })
  const authorize = useMutation({
    mutationFn: () => authorizeAgentGrant(window.location.href),
    onSuccess: ({ authorization_url }) =>
      window.location.assign(authorization_url),
  })
  const [confirmRevoke, setConfirmRevoke] = useState(false)
  const revoke = useMutation({
    mutationFn: revokeAgentGrant,
    onSuccess: async () => {
      setConfirmRevoke(false)
      await queryClient.invalidateQueries({ queryKey: ["auth", "agent-grant"] })
    },
  })

  const preferencesKey = ["settings", "app-preferences", organizationId]
  const preferences = useQuery({
    queryKey: preferencesKey,
    queryFn: () => getAppPreferences(organizationId),
    retry: false,
  })
  const updatePreferences = useMutation({
    mutationFn: (enabled: boolean) => setAppUpdates(organizationId, enabled),
    onSuccess: (saved) => queryClient.setQueryData(preferencesKey, saved),
  })
  const appUpdates = updatePreferences.isPending
    ? updatePreferences.variables
    : preferences.data?.app_updates

  const agentName = agent?.name ?? "Your agent"
  const granted = grant.data?.granted === true

  return (
    <>
      <SettingsGroup label={agent?.name ?? "Your agent"}>
        {agents.isPending ? (
          <SettingsNote>Loading your agent…</SettingsNote>
        ) : agents.isError || !agent ? (
          <SettingsMessage tone="error">
            {agents.isError
              ? authErrorMessage(agents.error, "Could not load your agent.")
              : "You don't have an agent in this workspace yet."}
          </SettingsMessage>
        ) : renaming ? (
          <form onSubmit={submitRename} aria-label={`Rename ${agent.name}`}>
            <SettingsRow
              media={
                <IdentityAvatar
                  identity={{
                    id: agent.id,
                    name: agent.name,
                    initials: agent.name.charAt(0).toUpperCase(),
                    kind: "agent",
                  }}
                />
              }
              title="Name"
              description="1 to 60 characters, unique in this workspace."
            >
              <Input
                className="settings-input"
                aria-label="Agent name"
                value={name}
                maxLength={60}
                autoFocus
                onChange={(event) => setName(event.target.value)}
              />
            </SettingsRow>
            {rename.isError && (
              <SettingsMessage tone="error">
                {authErrorMessage(rename.error, "Could not rename your agent.")}
              </SettingsMessage>
            )}
            <SettingsActions>
              <SettingsButton
                size="default"
                onClick={() => {
                  rename.reset()
                  setRenaming(false)
                }}
              >
                Cancel
              </SettingsButton>
              <SettingsButton
                type="submit"
                size="default"
                tone="primary"
                disabled={rename.isPending || !name.trim()}
              >
                {rename.isPending ? "Saving…" : "Save"}
              </SettingsButton>
            </SettingsActions>
          </form>
        ) : (
          <SettingsRow
            media={
              <IdentityAvatar
                identity={{
                  id: agent.id,
                  name: agent.name,
                  initials: agent.name.charAt(0).toUpperCase(),
                  kind: "agent",
                }}
              />
            }
            title={agent.name}
            description={[
              viewerName ? `${viewerName}'s agent` : "Your agent",
              reads.length > 0 ? `reads ${listWords(reads)}` : undefined,
            ]
              .filter(Boolean)
              .join(" · ")}
          >
            <SettingsButton
              onClick={() => {
                setName(agent.name)
                setRenaming(true)
              }}
            >
              Rename
            </SettingsButton>
          </SettingsRow>
        )}

        <SettingsRow
          title="Works while you're away"
          description={
            grant.isPending
              ? "Checking access…"
              : unavailable(grant.error)
                ? "Not available on this server."
                : grant.isError
                  ? "Could not check access."
                  : granted
                    ? `${agentName} can act for you in the background, within what you allow here.`
                    : `${agentName} acts only while you're here. Turning this on asks you to sign in again.`
          }
        >
          {grant.isError && !unavailable(grant.error) ? (
            <SettingsButton onClick={() => void grant.refetch()}>
              Retry
            </SettingsButton>
          ) : (
            !grant.isPending &&
            !grant.isError && (
              <SettingsSwitch
                label="Works while you're away"
                checked={granted || authorize.isPending}
                disabled={authorize.isPending || revoke.isPending}
                onCheckedChange={(next) => {
                  if (next) authorize.mutate()
                  else setConfirmRevoke(true)
                }}
              />
            )
          )}
        </SettingsRow>
        {authorize.isError && (
          <SettingsMessage tone="error">
            {authErrorMessage(authorize.error, "Could not turn on access.")}
          </SettingsMessage>
        )}

        <SettingsRow
          title="Keeps updates from your apps"
          description={
            preferences.isPending
              ? "Checking your preference…"
              : unavailable(preferences.error)
                ? "Not available on this server."
                : preferences.isError
                  ? "Could not load your preference."
                  : appUpdates
                    ? `New items from your apps are kept for ${agentName} to brief you.`
                    : "Off. New app updates are not kept."
          }
        >
          {preferences.isError && !unavailable(preferences.error) ? (
            <SettingsButton onClick={() => void preferences.refetch()}>
              Retry
            </SettingsButton>
          ) : (
            !preferences.isPending &&
            !preferences.isError && (
              <SettingsSwitch
                label="Keeps updates from your apps"
                checked={Boolean(appUpdates)}
                disabled={updatePreferences.isPending}
                onCheckedChange={(next) => updatePreferences.mutate(next)}
              />
            )
          )}
        </SettingsRow>
        {updatePreferences.isError && (
          <SettingsMessage tone="error">
            {authErrorMessage(
              updatePreferences.error,
              "Could not save your preference."
            )}
          </SettingsMessage>
        )}
      </SettingsGroup>

      <WorkspaceAnswerCheckSettings
        organization={organization}
        preview={false}
      />

      {granted && (
        <SettingsGroup label="Danger zone">
          <SettingsRow
            tone="danger"
            title="Revoke background access"
            description={`${agentName} stops all work it is doing for you now.`}
          >
            {!confirmRevoke && (
              <SettingsButton onClick={() => setConfirmRevoke(true)}>
                Revoke
              </SettingsButton>
            )}
          </SettingsRow>
          {confirmRevoke && (
            <SettingsActions
              start={`${agentName} will stop acting for you while you're away. You can turn it on again later.`}
            >
              <SettingsButton
                size="default"
                onClick={() => setConfirmRevoke(false)}
              >
                Cancel
              </SettingsButton>
              <SettingsButton
                size="default"
                tone="danger"
                disabled={revoke.isPending}
                onClick={() => revoke.mutate()}
              >
                {revoke.isPending ? "Revoking…" : "Revoke access"}
              </SettingsButton>
            </SettingsActions>
          )}
          {revoke.isError && (
            <SettingsMessage tone="error">
              {authErrorMessage(revoke.error, "Could not revoke access.")}
            </SettingsMessage>
          )}
        </SettingsGroup>
      )}
    </>
  )
}
