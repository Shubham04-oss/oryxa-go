import { createFileRoute, Outlet, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/app/threads")({
	// Conversations live in the sidebar and in search, not on a page of their
	// own, so the bare path has nothing to show.
	beforeLoad: ({ location }) => {
		if (location.pathname.replace(/\/$/, "").endsWith("/app/threads")) {
			throw redirect({ to: "/app" });
		}
	},
	component: ThreadsLayout,
});

function ThreadsLayout() {
	return <Outlet />;
}
