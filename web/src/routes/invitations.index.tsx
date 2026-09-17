import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import {
	createFileRoute,
	type ErrorComponentProps,
	Link,
	Navigate,
	useLocation,
	useNavigate,
	useRouter,
} from "@tanstack/react-router";
import { useState } from "react";
import { roleLabel } from "../components/app/identity";
import { AuthShell } from "../components/auth/auth-shell";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { ApiError } from "../lib/api/client";
import {
	acceptReceivedInvitation,
	authErrorMessage,
	type ReceivedInvitation,
	signOut,
} from "../lib/auth/client";
import {
	ensureReceivedInvitations,
	ensureSession,
	receivedInvitationsQueryOptions,
	reloadSession,
	selectOrganization,
	useSession,
} from "../lib/auth/session";
import { daysUntil, expiryText } from "../lib/format";

export const Route = createFileRoute("/invitations/")({
	// Where /app sends a member who has no organization. Like /app, it needs
	// their session, so it loads on the client.
	ssr: false,
	beforeLoad: async ({ context, location }) => {
		const session = await ensureSession(context.queryClient, location.href);
		await ensureReceivedInvitations(context.queryClient, session);
	},
	component: InvitationsPage,
	errorComponent: InvitationsUnavailable,
	head: () => ({ meta: [{ title: "Invitations · The Weirdly ODD" }] }),
	pendingComponent: InvitationsPending,
});

// Revoked, expired, or already used since the list loaded.
const closedStatuses = new Set([404, 409, 410]);

function joinFailure(error: unknown, invitation: ReceivedInvitation): string {
	if (error instanceof ApiError && error.status === 402) {
		return `${invitation.organization_name} has reached the member limit its plan allows. Ask an owner to upgrade, then try again.`;
	}
	if (error instanceof ApiError && closedStatuses.has(error.status)) {
		return `The invitation to ${invitation.organization_name} is no longer open. Ask an admin there to send a new one.`;
	}
	return authErrorMessage(error, "Could not join the organization");
}

function heading(count: number): string {
	if (count === 0) {
		return "No invitations right now";
	}
	return count === 1 ? "You have an invitation" : "You have invitations";
}

function InvitationsPage() {
	const { organizations, user } = useSession();
	const { data: invitations } = useSuspenseQuery(
		receivedInvitationsQueryOptions,
	);
	const href = useLocation({ select: (location) => location.href });
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const [joining, setJoining] = useState<string>();
	const [switching, setSwitching] = useState(false);
	const [formError, setFormError] = useState("");
	const busy = joining !== undefined || switching;

	async function join(invitation: ReceivedInvitation) {
		setFormError("");
		setJoining(invitation.id);
		try {
			const membership = await acceptReceivedInvitation(invitation.id);
			selectOrganization(membership.organization_id);
			await reloadSession(queryClient);
			await navigate({ to: "/app" });
		} catch (error) {
			setFormError(joinFailure(error, invitation));
			setJoining(undefined);
			if (error instanceof ApiError && closedStatuses.has(error.status)) {
				await queryClient.invalidateQueries({
					queryKey: receivedInvitationsQueryOptions.queryKey,
				});
			}
		}
	}

	async function switchAccount() {
		setSwitching(true);
		// Even if signing out fails, sign-in lets the member choose an account.
		await signOut().catch(() => undefined);
		await navigate({ search: { redirect: href }, to: "/auth/sign-in" });
		queryClient.clear();
	}

	const invited = invitations.length > 0;

	return (
		<AuthShell>
			<h1 className="auth-heading">{heading(invitations.length)}</h1>
			<p className="auth-subheading">
				{invited
					? `Join as ${user.email}.`
					: `Invitations sent to ${user.email} appear here.`}
			</p>

			{formError ? (
				<Alert className="auth-alert" variant="destructive">
					<AlertDescription>{formError}</AlertDescription>
				</Alert>
			) : null}

			{invited ? (
				<ul aria-label="Invitations" className="invitation-list">
					{invitations.map((invitation) => (
						<li key={invitation.id}>
							<div className="invitation-copy">
								<span>{invitation.organization_name}</span>
								<small>
									{roleLabel(invitation.role)} ·{" "}
									{expiryText(daysUntil(invitation.expires_at))}
								</small>
							</div>
							<Button
								aria-label={`Join ${invitation.organization_name}`}
								disabled={busy}
								onClick={() => join(invitation)}
								size="sm"
								type="button"
							>
								{joining === invitation.id ? "Joining…" : "Join"}
							</Button>
						</li>
					))}
				</ul>
			) : null}

			{/* With no invitation left, this is the only way on, so it leads. */}
			<Button
				asChild
				className={
					invited
						? "auth-button auth-button-secondary"
						: "auth-button auth-button-primary"
				}
				size="lg"
				variant={invited ? "outline" : "default"}
			>
				{organizations.length > 0 ? (
					<Link to="/app">Go to your workspace</Link>
				) : (
					<Link to="/onboarding">Set up a new organization</Link>
				)}
			</Button>

			<p className="auth-footer">
				Invited at another address?{" "}
				<button
					className="auth-link auth-link-button"
					disabled={busy}
					onClick={switchAccount}
					type="button"
				>
					Use another account
				</button>
			</p>
		</AuthShell>
	);
}

function InvitationsUnavailable({ error }: ErrorComponentProps) {
	const router = useRouter();
	const href = useLocation({ select: (location) => location.href });

	// A session that expired and could not be refreshed ends up here.
	if (error instanceof ApiError && error.status === 401) {
		return <Navigate search={{ redirect: href }} to="/auth/sign-in" />;
	}

	return (
		<AuthShell>
			<h1 className="auth-heading">Invitations unavailable</h1>
			<p className="auth-subheading">
				The Weirdly ODD could not be reached. Check your connection and try
				again.
			</p>
			<Button
				className="auth-button auth-button-primary"
				onClick={() => router.invalidate()}
				size="lg"
				type="button"
			>
				Try again
			</Button>
		</AuthShell>
	);
}

function InvitationsPending() {
	return <main aria-busy="true" className="auth-canvas" />;
}
