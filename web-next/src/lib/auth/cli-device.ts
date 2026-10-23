import { apiRequest } from "@/lib/api/client"

/** A CLI's pending device sign-in, as auth shows it before a member decides. */
export type CliDeviceRequest = {
  user_code: string
  client_name: string
  client_address: string
  requested_at: string
  expires_at: string
  /** What approving hands over, in auth's words. */
  grants: string
}

export function lookupCliDevice(userCode: string): Promise<CliDeviceRequest> {
  return apiRequest("/v1/auth/cli/device/lookup", {
    body: { user_code: userCode },
  })
}

export function decideCliDevice(
  userCode: string,
  decision: "approve" | "deny"
): Promise<{ status: "approved" | "denied" }> {
  return apiRequest(`/v1/auth/cli/device/${decision}`, {
    body: { user_code: userCode },
  })
}
