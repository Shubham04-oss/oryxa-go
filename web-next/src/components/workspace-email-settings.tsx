import { useQuery } from "@tanstack/react-query"
import { connectionsQueryOptions } from "@/lib/apps/client"
import type { Organization } from "@/lib/auth/client"
import { ToolkitLogo } from "./workspace-tool-logo"
import {
  SettingsButton,
  SettingsGroup,
  SettingsNote,
  SettingsRow,
  SettingsStatus,
  SettingsValue,
} from "./settings-kit"

/**
 * Settings → Email. Forwarding asks to oryxa by email, who may send them and
 * the address replies come from have no route yet: one honest line says so.
 * Gmail's connection is real, read from the member's own connections, and
 * masking lives with Your agent rather than in a second switch here. What
 * oryxa always sends stays listed.
 */
export function WorkspaceEmailSettings({
  organization,
  preview = false,
  onBilling,
  onAgent,
  onOpenTools,
}: {
  organization?: Organization
  preview?: boolean
  onBilling: () => void
  onAgent: () => void
  onOpenTools: () => void
}) {
  const gmail = useQuery({
    ...connectionsQueryOptions(organization?.id ?? "", { toolkit: "gmail" }),
    enabled: !preview && Boolean(organization),
    retry: false,
  })
  const connected = gmail.data?.some(
    (connection) => connection.status === "active"
  )

  return (
    <>
      <SettingsGroup label="Your address">
        <SettingsRow
          title="Forward asks here"
          description="Forwarding mail to oryxa so it lands in Requests is coming soon."
        />
        <SettingsRow
          media={
            <ToolkitLogo
              toolkit={{ toolkit: "gmail", name: "Gmail", logo: undefined }}
            />
          }
          title="Gmail"
          description={
            preview || !organization
              ? "Sign in to see your Gmail connection."
              : gmail.isPending
                ? "Checking your connection…"
                : gmail.isError
                  ? "Could not check your Gmail connection."
                  : connected
                    ? "Your agent reads your mail through your own Gmail connection."
                    : "Connect Gmail so your agent can read your mail."
          }
        >
          {connected ? (
            <SettingsStatus>Connected</SettingsStatus>
          ) : (
            !preview &&
            organization &&
            !gmail.isPending && (
              <SettingsButton onClick={onOpenTools}>Connect</SettingsButton>
            )
          )}
        </SettingsRow>
      </SettingsGroup>

      <SettingsGroup label="Replies">
        <SettingsRow
          title="Mask sensitive details"
          description="Answers follow what your agent hides when it answers."
        >
          <SettingsButton onClick={onAgent}>Your agent</SettingsButton>
        </SettingsRow>
      </SettingsGroup>

      <SettingsGroup label="Always sent">
        <SettingsRow
          title="Account access"
          description="Verification, password resets and invitations each use a one-time link."
        >
          <SettingsValue>Always on</SettingsValue>
        </SettingsRow>
        <SettingsRow
          title="Membership changes"
          description="When a person's role changes or they are removed, we tell that person."
        >
          <SettingsValue>To that member</SettingsValue>
        </SettingsRow>
        <SettingsRow
          title="Billing"
          description="Payment and renewal notices go to the billing contact, not every admin."
        >
          <SettingsButton onClick={onBilling}>Open billing</SettingsButton>
        </SettingsRow>
      </SettingsGroup>
      <SettingsNote>
        Parley emails are chosen team by team in Notifications.
      </SettingsNote>
    </>
  )
}
