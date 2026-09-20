import type { Teamspace } from "./client";

/** Whether the current member has an attributable agent that may publish now. */
export function canPublishSharedWork(
	space: Teamspace,
	authorCount: number,
): boolean {
	return (
		space.is_member &&
		space.status === "active" &&
		space.organization_agent.status === "active" &&
		authorCount > 0
	);
}

/**
 * Shared artifact addresses are untrusted data. Only HTTPS resources and
 * first-party absolute paths become links; everything else stays plain text.
 */
export function safeArtifactHref(uri: string): string | undefined {
	if (uri.startsWith("/") && !uri.startsWith("//")) return uri;
	try {
		const parsed = new URL(uri);
		return parsed.protocol === "https:" ? parsed.toString() : undefined;
	} catch {
		return undefined;
	}
}
