import { createFileRoute } from "@tanstack/react-router";
import { MessageSquareText } from "lucide-react";
import { ServicePlaceholder } from "@/components/app/service-placeholder";

export const Route = createFileRoute("/app/threads")({
	component: ThreadsPage,
	head: () => ({ meta: [{ title: "Threads · The Weirdly ODD" }] }),
});

function ThreadsPage() {
	return (
		<ServicePlaceholder
			description="Every conversation, in one place."
			icon={MessageSquareText}
			title="Threads"
		/>
	);
}
