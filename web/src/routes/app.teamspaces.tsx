import { createFileRoute } from "@tanstack/react-router";
import { TeamspacesPage } from "@/features/teamspaces/teamspaces-page";

interface TeamspacesSearch {
	space?: string;
}

export const Route = createFileRoute("/app/teamspaces")({
	validateSearch: (search: Record<string, unknown>): TeamspacesSearch => ({
		space:
			typeof search.space === "string" && search.space !== ""
				? search.space
				: undefined,
	}),
	component: TeamspacesRoute,
	head: () => ({ meta: [{ title: "Teamspaces · The Weirdly ODD" }] }),
});

function TeamspacesRoute() {
	const { space } = Route.useSearch();
	return <TeamspacesPage selectedId={space} />;
}
