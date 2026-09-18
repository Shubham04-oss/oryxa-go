import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/app/knowledge")({
	component: KnowledgeLayout,
});

function KnowledgeLayout() {
	return <Outlet />;
}
