import {
	createFileRoute,
	type ErrorComponentProps,
	Navigate,
	Outlet,
	redirect,
	useLocation,
	useRouter,
} from "@tanstack/react-router";
import { AppShell } from "@/components/app/app-shell";
import { AuthShell } from "@/components/auth/auth-shell";
import { Button } from "@/components/ui/button";
import { ApiError } from "@/lib/api/client";
import { ensureSession, useSession } from "@/lib/auth/session";

export const Route = createFileRoute("/app")({
	// Loading the session can refresh it, which rotates the browser's cookies,
	// so the workspace renders on the client.
	ssr: false,
	beforeLoad: async ({ context, location }) => {
		const session = await ensureSession(context.queryClient, location.href);
		if (session.organizations.length === 0) {
			throw redirect({ to: "/onboarding" });
		}
	},
	component: AppLayout,
	errorComponent: WorkspaceUnavailable,
	pendingComponent: WorkspacePending,
});

function AppLayout() {
	const { organization } = useSession();

	// The member can leave their last organization while the app is open.
	if (!organization) {
		return <Navigate to="/onboarding" />;
	}

	return (
		<AppShell>
			<Outlet />
		</AppShell>
	);
}

function WorkspacePending() {
	return <main aria-busy="true" className="auth-canvas" />;
}

function WorkspaceUnavailable({ error }: ErrorComponentProps) {
	const router = useRouter();
	const href = useLocation({ select: (location) => location.href });

	// A session that expired and could not be refreshed ends up here.
	if (error instanceof ApiError && error.status === 401) {
		return <Navigate search={{ redirect: href }} to="/auth/sign-in" />;
	}

	return (
		<AuthShell>
			<h1 className="auth-heading">Workspace unavailable</h1>
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
