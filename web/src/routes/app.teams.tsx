import { createFileRoute } from "@tanstack/react-router";
import { UsersRound } from "lucide-react";
import { ServicePlaceholder } from "@/components/app/service-placeholder";

export const Route = createFileRoute("/app/teams")({
	component: TeamsPage,
	head: () => ({ meta: [{ title: "Teams · The Weirdly ODD" }] }),
});

function TeamsPage() {
	return (
		<ServicePlaceholder
			description="People and access, organized around the work."
			icon={UsersRound}
			title="Teams"
		/>
	);
}
