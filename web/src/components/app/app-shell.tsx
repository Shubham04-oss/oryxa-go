import { useLocation } from "@tanstack/react-router";
import { Search } from "lucide-react";
import type { CSSProperties, ReactNode } from "react";
import { AppSidebar } from "@/components/app/app-sidebar";
import { Button } from "@/components/ui/button";
import {
	SidebarInset,
	SidebarProvider,
	SidebarTrigger,
} from "@/components/ui/sidebar";

export function AppShell({ children }: { children: ReactNode }) {
	const pathname = useLocation({ select: (location) => location.pathname });
	const isOrganizationSettings = pathname.startsWith(
		"/app/settings/organization",
	);

	return (
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
						<SidebarTrigger aria-label="Toggle navigation" />
						<span>
							{isOrganizationSettings ? "Organization settings" : "Odd Works"}
						</span>
					</div>
					<Button className="app-search" size="sm" variant="ghost">
						<Search />
						<span>Search</span>
						<kbd>⌘ K</kbd>
					</Button>
				</header>
				{children}
			</SidebarInset>
		</SidebarProvider>
	);
}
