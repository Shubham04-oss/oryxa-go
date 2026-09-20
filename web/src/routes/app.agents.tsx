import { createFileRoute, useNavigate } from "@tanstack/react-router";
import {
	AgentSystemPage,
	type AgentTab,
	agentTabs,
	type RequestBox,
} from "@/features/agents/agent-system-page";

interface AgentsSearch {
	box: RequestBox;
	tab: AgentTab;
}

function validTab(value: unknown): AgentTab {
	return typeof value === "string" && agentTabs.includes(value as AgentTab)
		? (value as AgentTab)
		: "overview";
}

export const Route = createFileRoute("/app/agents")({
	validateSearch: (search: Record<string, unknown>): AgentsSearch => ({
		box: search.box === "sent" ? "sent" : "inbox",
		tab: validTab(search.tab),
	}),
	component: AgentsRoute,
	head: () => ({ meta: [{ title: "Agent system · The Weirdly ODD" }] }),
});

function AgentsRoute() {
	const search = Route.useSearch();
	const navigate = useNavigate({ from: Route.fullPath });

	return (
		<AgentSystemPage
			box={search.box}
			onSearchChange={(next) => navigate({ search: next })}
			tab={search.tab}
		/>
	);
}
