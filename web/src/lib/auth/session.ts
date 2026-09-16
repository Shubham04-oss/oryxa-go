import {
	type QueryClient,
	queryOptions,
	useQuery,
	useSuspenseQuery,
} from "@tanstack/react-query";
import { redirect } from "@tanstack/react-router";
import { useSyncExternalStore } from "react";
import { ApiError } from "../api/client";
import {
	getOrganization,
	getSession,
	type Membership,
	type Organization,
	type Session,
} from "./client";

export const sessionQueryOptions = queryOptions({
	queryFn: getSession,
	queryKey: ["auth", "session"],
	retry: false,
	staleTime: 60_000,
});

export function organizationQueryOptions(organizationId: string) {
	return queryOptions({
		queryFn: () => getOrganization(organizationId),
		queryKey: ["auth", "organizations", organizationId],
		retry: false,
		staleTime: 60_000,
	});
}

/**
 * The signed-in member's session, for a route that needs one. A signed-out
 * visitor is sent to sign in, and back to `returnTo` afterwards.
 */
export async function ensureSession(
	queryClient: QueryClient,
	returnTo: string,
): Promise<Session> {
	try {
		return await queryClient.ensureQueryData(sessionQueryOptions);
	} catch (error) {
		if (error instanceof ApiError && error.status === 401) {
			throw redirect({ search: { redirect: returnTo }, to: "/auth/sign-in" });
		}
		throw error;
	}
}

/**
 * Refetches the session once the member has joined or created an
 * organization. `ensureSession` would otherwise keep answering from the cache.
 */
export function reloadSession(queryClient: QueryClient): Promise<void> {
	return queryClient.invalidateQueries({
		queryKey: sessionQueryOptions.queryKey,
		refetchType: "all",
	});
}

/** A path on this app to return to after signing in; anything else is dropped. */
export function safeRedirect(value: unknown): string | undefined {
	if (typeof value !== "string" || !value.startsWith("/")) {
		return undefined;
	}
	const base = "http://app.invalid";
	try {
		const url = new URL(value, base);
		return url.origin === base
			? `${url.pathname}${url.search}${url.hash}`
			: undefined;
	} catch {
		return undefined;
	}
}

/** The single-use token an emailed auth link carries. */
export function searchToken(value: unknown): string | undefined {
	return typeof value === "string" && value.trim() !== "" ? value : undefined;
}

const organizationStorageKey = "weirdly-odd:organization";
const selectionListeners = new Set<() => void>();
let selectedOrganizationId: string | null | undefined;

function readSelectedOrganization(): string | null {
	if (selectedOrganizationId === undefined) {
		try {
			selectedOrganizationId = localStorage.getItem(organizationStorageKey);
		} catch {
			selectedOrganizationId = null;
		}
	}
	return selectedOrganizationId;
}

function subscribeToSelection(listener: () => void): () => void {
	selectionListeners.add(listener);
	return () => {
		selectionListeners.delete(listener);
	};
}

/** Shows `organizationId` in the workspace, and keeps it across reloads. */
export function selectOrganization(organizationId: string): void {
	selectedOrganizationId = organizationId;
	try {
		localStorage.setItem(organizationStorageKey, organizationId);
	} catch {
		// Without storage the choice still holds until the page reloads.
	}
	for (const listener of selectionListeners) {
		listener();
	}
}

export function resolveOrganization(
	organizations: Organization[],
	selectedId: string | null,
): Organization | undefined {
	return (
		organizations.find((organization) => organization.id === selectedId) ??
		organizations[0]
	);
}

export interface SessionState extends Session {
	/** The organization the workspace shows; missing when the member has none. */
	organization: Organization | undefined;
}

/** The member's session, in routes whose `beforeLoad` ensures it. */
export function useSession(): SessionState {
	const { data } = useSuspenseQuery(sessionQueryOptions);
	const selectedId = useSyncExternalStore(
		subscribeToSelection,
		readSelectedOrganization,
		() => null,
	);
	return {
		...data,
		organization: resolveOrganization(data.organizations, selectedId),
	};
}

export interface Workspace extends Session {
	organization: Organization;
}

/** The member and the organization the workspace shows, inside `/app`. */
export function useWorkspace(): Workspace {
	const { organization, ...session } = useSession();
	if (!organization) {
		throw new Error("useWorkspace is only available inside the /app layout");
	}
	return { ...session, organization };
}

export function useMembership(organizationId: string): Membership | undefined {
	return useQuery(organizationQueryOptions(organizationId)).data?.membership;
}
