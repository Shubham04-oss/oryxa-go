import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/app/threads")({
	component: ThreadsLayout,
});

function ThreadsLayout() {
	return <Outlet />;
}
