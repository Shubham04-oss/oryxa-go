import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/app/settings/organization")({
	component: OrganizationSettingsLayout,
});

function OrganizationSettingsLayout() {
	return <Outlet />;
}
