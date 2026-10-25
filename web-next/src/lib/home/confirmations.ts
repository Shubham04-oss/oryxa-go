// The tool confirmations waiting on the member across all their chats, in
// one read: Go keeps each chat's count as its runs end, so nothing here walks
// a chat's history. agents/primary/internal/httpserver/server.go
// pendingConfirmations
import { apiRequest } from "@/lib/api/client"
import { organizationPath } from "@/lib/agents/http"

/** A chat with tools waiting for the member to approve or decline them. */
export type PendingChat = {
  id: string
  title: string
  updated_at: string
  origin?: string
  /** How many confirmations wait in it: at least one. */
  pending_confirmations: number
}

export type PendingConfirmations = {
  /** The chats, the most recently active first. */
  threads: PendingChat[]
  /** Every confirmation waiting, across them. */
  total: number
}

/** GET /v1/organizations/{org}/pending-confirmations */
export function listPendingConfirmations(
  organizationId: string,
  { signal }: { signal?: AbortSignal } = {}
): Promise<PendingConfirmations> {
  return apiRequest<PendingConfirmations>(
    organizationPath(organizationId, "pending-confirmations"),
    { signal }
  )
}
