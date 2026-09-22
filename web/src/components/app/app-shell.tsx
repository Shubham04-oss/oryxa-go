import { useLocation } from "@tanstack/react-router";
import type { CSSProperties, ReactNode } from "react";
import { AppSidebar } from "@/components/app/app-sidebar";
import {
	WorkspaceSearch,
	WorkspaceSearchProvider,
} from "@/components/app/workspace-search";
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar";
import { useWorkspace } from "@/lib/auth/session";

export function AppShell({ children }: { children: ReactNode }) {
	const { organization } = useWorkspace();
	const pathname = useLocation({ select: (location) => location.pathname });
	const isOrganizationSettings = pathname.startsWith(
		"/app/settings/organization",
	);
	const scope = workspaceScope(pathname);

	return (
		<WorkspaceSearchProvider>
			<SidebarProvider
				className="app-shell"
				style={
					{
						"--sidebar-width": "15.5rem",
						"--sidebar-width-icon": "3.5rem",
					} as CSSProperties
				}
			>
				<AppSidebar />
				<SidebarInset className="app-main">
					<header className="app-topbar">
						<div className="app-topbar-context">
							<div>
								<small>{scope}</small>
								<span>
									{isOrganizationSettings
										? "Organization settings"
										: organization.name}
								</span>
							</div>
						</div>
						<WorkspaceSearch />
					</header>
					{children}
				</SidebarInset>
			</SidebarProvider>
		</WorkspaceSearchProvider>
	);
}

export function workspaceScope(pathname: string): string {
	if (pathname.startsWith("/app/settings")) return "Settings";
	if (
		pathname.startsWith("/app/teamspaces") ||
		pathname.startsWith("/app/teams")
	) {
		return "Collaborate";
	}
	if (
		pathname.startsWith("/app/knowledge") ||
		pathname.startsWith("/app/files") ||
		pathname.startsWith("/app/connections")
	) {
		return "Resources";
	}
	return "Solo workspace";
}
