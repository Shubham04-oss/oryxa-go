import { createFileRoute } from "@tanstack/react-router";
import { Files } from "lucide-react";
import { ServicePlaceholder } from "@/components/app/service-placeholder";

export const Route = createFileRoute("/app/files")({
	component: FilesPage,
	head: () => ({ meta: [{ title: "Files · The Weirdly ODD" }] }),
});

function FilesPage() {
	return (
		<ServicePlaceholder
			description="Shared context and artifacts for your work."
			icon={Files}
			title="Files"
		/>
	);
}
