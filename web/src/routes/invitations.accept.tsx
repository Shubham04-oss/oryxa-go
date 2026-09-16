import { useQueryClient } from "@tanstack/react-query";
import {
	createFileRoute,
	Link,
	useLocation,
	useNavigate,
} from "@tanstack/react-router";
import { useState } from "react";
import { AuthShell } from "../components/auth/auth-shell";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { ApiError } from "../lib/api/client";
import {
	acceptInvitation,
	authErrorMessage,
	signOut,
} from "../lib/auth/client";
import {
	ensureSession,
	reloadSession,
	searchToken,
	selectOrganization,
	useSession,
} from "../lib/auth/session";

interface AcceptInvitationSearch {
	token?: string;
}

export const Route = createFileRoute("/invitations/accept")({
	validateSearch: (
		search: Record<string, unknown>,
	): AcceptInvitationSearch => ({
		token: searchToken(search.token),
	}),
	// Accepting joins the signed-in member, so the page needs their session;
	// like /app, it loads on the client.
	ssr: false,
	beforeLoad: async ({ context, location }) => {
		await ensureSession(context.queryClient, location.href);
	},
	component: AcceptInvitationPage,
	head: () => ({ meta: [{ title: "Join organization · The Weirdly ODD" }] }),
	pendingComponent: InvitationPending,
});

type Problem = "expired" | "full" | "invalid" | "someone-else";

const problems: Record<Problem, { description: string; title: string }> = {
	expired: {
		description: "Ask an organization admin to send you a new one.",
		title: "Invitation expired",
	},
	full: {
		description:
			"The organization has reached the member limit its plan allows. Ask an owner to upgrade, then open the link again.",
		title: "Organization is full",
	},
	invalid: {
		description: "It was already used or revoked, or the link is incomplete.",
		title: "Invitation not valid",
	},
	"someone-else": {
		description:
			"Sign in with the email address the invitation was sent to, then open the link again.",
		title: "Invitation is for someone else",
	},
};

function invitationProblem(error: unknown): Problem | undefined {
	if (!(error instanceof ApiError)) {
		return undefined;
	}
	switch (error.status) {
		case 402:
			return "full";
		case 403:
			return "someone-else";
		case 404:
		case 409:
			return "invalid";
		case 410:
			return "expired";
		default:
			return undefined;
	}
}

function AcceptInvitationPage() {
	const { token } = Route.useSearch();
	const { user } = useSession();
	const href = useLocation({ select: (location) => location.href });
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const [problem, setProblem] = useState<Problem | undefined>(
		token ? undefined : "invalid",
	);
	const [formError, setFormError] = useState("");
	const [pending, setPending] = useState(false);

	async function accept() {
		if (!token) return;
		setFormError("");
		setPending(true);
		try {
			const membership = await acceptInvitation(token);
			selectOrganization(membership.organization_id);
			await reloadSession(queryClient);
			await navigate({ to: "/app" });
		} catch (error) {
			const found = invitationProblem(error);
			if (found) {
				setProblem(found);
			} else {
				setFormError(
					authErrorMessage(error, "Could not accept the invitation"),
				);
			}
			setPending(false);
		}
	}

	async function switchAccount() {
		setPending(true);
		// Even if signing out fails, sign-in lets the member choose an account.
		await signOut().catch(() => undefined);
		await navigate({ search: { redirect: href }, to: "/auth/sign-in" });
		queryClient.clear();
	}

	if (problem) {
		return (
			<AuthShell>
				<h1 className="auth-heading">{problems[problem].title}</h1>
				<p className="auth-subheading">{problems[problem].description}</p>
				{problem === "someone-else" ? (
					<Button
						className="auth-button auth-button-primary"
						disabled={pending}
						onClick={switchAccount}
						size="lg"
						type="button"
					>
						Use another account
					</Button>
				) : (
					<Button asChild className="auth-button auth-button-primary" size="lg">
						<Link to="/app">Go to your workspace</Link>
					</Button>
				)}
			</AuthShell>
		);
	}

	return (
		<AuthShell>
			<h1 className="auth-heading">You have an invitation</h1>
			<p className="auth-subheading">
				Accept it to join the organization as {user.email}.
			</p>

			{formError ? (
				<Alert className="auth-alert" variant="destructive">
					<AlertDescription>{formError}</AlertDescription>
				</Alert>
			) : null}

			<Button
				className="auth-button auth-button-primary"
				disabled={pending}
				onClick={accept}
				size="lg"
				type="button"
			>
				{pending ? "Joining…" : "Accept invitation"}
			</Button>

			<p className="auth-footer">
				Not {user.email}?{" "}
				<button
					className="auth-link auth-link-button"
					disabled={pending}
					onClick={switchAccount}
					type="button"
				>
					Use another account
				</button>
			</p>
		</AuthShell>
	);
}

function InvitationPending() {
	return <main aria-busy="true" className="auth-canvas" />;
}
