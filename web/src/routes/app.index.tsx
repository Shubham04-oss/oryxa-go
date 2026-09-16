import { createFileRoute } from "@tanstack/react-router";
import { WorkspaceHome } from "@/components/app/workspace-home";

export const Route = createFileRoute("/app/")({
	component: WorkspaceHome,
	head: () => ({ meta: [{ title: "Home · The Weirdly ODD" }] }),
});
