import { queryOptions } from "@tanstack/react-query";
import { apiRequest } from "../api/client";

/**
 * Whether the member has granted "agents may act for me": when a trigger or
 * schedule wakes Krude with nobody watching, this is what lets it act with
 * the member's own identity instead of stopping at "no member present".
 */
export interface AgentGrant {
	created_at?: string;
	granted: boolean;
}

export interface AgentGrantAuthorization {
	authorization_url: string;
}

const agentGrantPath = "/v1/agent-grant";

/** Every agent-grant route answers 404 when the server has none of this configured at all. */
export function getAgentGrant(): Promise<AgentGrant> {
	return apiRequest<AgentGrant>(agentGrantPath);
}

/** Starts the grant flow; the member is sent to `authorization_url` to complete it. */
export function authorizeAgentGrant(
	returnTo: string,
): Promise<AgentGrantAuthorization> {
	return apiRequest<AgentGrantAuthorization>(`${agentGrantPath}/authorize`, {
		body: { return_to: returnTo },
	});
}

export async function revokeAgentGrant(): Promise<void> {
	await apiRequest(agentGrantPath, { method: "DELETE" });
}

export function agentGrantQueryKey() {
	return ["auth", "agent-grant"] as const;
}

export function agentGrantQueryOptions() {
	return queryOptions({
		queryFn: getAgentGrant,
		queryKey: agentGrantQueryKey(),
		retry: false,
	});
}
