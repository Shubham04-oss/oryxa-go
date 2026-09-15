import { createFileRoute } from "@tanstack/react-router";
import { Plug } from "lucide-react";
import { ServicePlaceholder } from "@/components/app/service-placeholder";

export const Route = createFileRoute("/app/connections")({
	component: ConnectionsPage,
	head: () => ({ meta: [{ title: "Connections · The Weirdly ODD" }] }),
});

function ConnectionsPage() {
	return (
		<ServicePlaceholder
			description="Tools and accounts available to this organization."
			icon={Plug}
			title="Connections"
		/>
	);
}
