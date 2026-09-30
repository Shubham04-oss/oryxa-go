// Member-safe metadata for the Home desk. These reads neither create a Slack
// authorization URL nor expose credentials; Go derives the member from auth.
import { apiRequest } from "@/lib/api/client"
import { organizationPath, withSearch } from "@/lib/agents/http"
import type { AgentLease } from "@/lib/agents/agent-requests"
import type { SlackInstallStatus } from "@/lib/apps/slack"

export type ActiveAgentLease = AgentLease & {
  role: "owner" | "asker"
  can_revoke: boolean
}

export type ActiveAgentLeasePage = {
  leases: ActiveAgentLease[]
  observed_at: string
  has_more: boolean
  next_cursor?: string
}

export function listActiveAgentLeases(
  organizationId: string,
  {
    limit = 100,
    cursor,
    signal,
  }: { limit?: number; cursor?: string; signal?: AbortSignal } = {}
) {
  return apiRequest<ActiveAgentLeasePage>(
    withSearch(organizationPath(organizationId, "agent-leases"), {
      limit,
      cursor,
    }),
    { signal }
  )
}

export type MemberSlackStatus = {
  status: SlackInstallStatus
  can_manage: boolean
}

export function getMemberSlackStatus(
  organizationId: string,
  { signal }: { signal?: AbortSignal } = {}
) {
  return apiRequest<MemberSlackStatus>(
    organizationPath(organizationId, "apps", "slack", "status"),
    { signal }
  )
}
