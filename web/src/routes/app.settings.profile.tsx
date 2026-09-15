import { createFileRoute } from "@tanstack/react-router";
import { CircleUserRound } from "lucide-react";
import { ServicePlaceholder } from "@/components/app/service-placeholder";

export const Route = createFileRoute("/app/settings/profile")({
	component: ProfileSettingsPage,
	head: () => ({ meta: [{ title: "Account · The Weirdly ODD" }] }),
});

function ProfileSettingsPage() {
	return (
		<ServicePlaceholder
			description="Your profile and personal preferences."
			icon={CircleUserRound}
			title="Account settings"
		/>
	);
}
