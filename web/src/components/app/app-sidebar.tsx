import { Link, useLocation } from "@tanstack/react-router";
import { ArrowLeft, ChevronsUpDown, Plus, ShieldCheck } from "lucide-react";
import {
	accountSettingsNavigation,
	type NavigationItem,
	organizationAdminNavigation,
	organizationNavigation,
	organizationOperationsNavigation,
	settingsNavigation,
	workspaceNavigation,
} from "@/components/app/navigation";
import {
	Sidebar,
	SidebarContent,
	SidebarFooter,
	SidebarGroup,
	SidebarGroupContent,
	SidebarGroupLabel,
	SidebarHeader,
	SidebarMenu,
	SidebarMenuButton,
	SidebarMenuItem,
	SidebarRail,
} from "@/components/ui/sidebar";

export function AppSidebar() {
	const pathname = useLocation({ select: (location) => location.pathname });
	const isOrganizationSettings = pathname.startsWith(
		"/app/settings/organization",
	);

	return (
		<Sidebar className="product-sidebar" collapsible="icon">
			{isOrganizationSettings ? (
				<OrganizationSettingsSidebar pathname={pathname} />
			) : (
				<WorkspaceSidebar pathname={pathname} />
			)}
			<SidebarRail />
		</Sidebar>
	);
}

function WorkspaceSidebar({ pathname }: { pathname: string }) {
	return (
		<>
			<SidebarHeader className="product-sidebar-header">
				<Link className="product-wordmark" to="/app">
					<span aria-hidden="true">O</span>
					<strong>The Weirdly ODD</strong>
				</Link>

				<SidebarMenu>
					<SidebarMenuItem>
						<SidebarMenuButton
							className="organization-switcher"
							size="lg"
							tooltip="Odd Works"
						>
							<span className="organization-mark" aria-hidden="true">
								OW
							</span>
							<span className="organization-copy">
								<strong>Odd Works</strong>
								<small>Organization</small>
							</span>
							<ChevronsUpDown className="organization-switcher-icon" />
						</SidebarMenuButton>
					</SidebarMenuItem>
				</SidebarMenu>

				<SidebarMenu>
					<SidebarMenuItem>
						<SidebarMenuButton
							asChild
							className="app-new-thread"
							tooltip="New thread"
						>
							<Link to="/app">
								<Plus />
								<span>New thread</span>
								<kbd>N</kbd>
							</Link>
						</SidebarMenuButton>
					</SidebarMenuItem>
				</SidebarMenu>
			</SidebarHeader>

			<SidebarContent>
				<NavigationGroup
					items={workspaceNavigation}
					label="Workspace"
					pathname={pathname}
				/>
				<NavigationGroup
					items={organizationNavigation}
					label="Organization"
					pathname={pathname}
				/>
			</SidebarContent>

			<SidebarFooter className="product-sidebar-footer">
				<SidebarMenu>
					{settingsNavigation.map((item) => (
						<NavigationMenuItem
							item={item}
							key={item.href}
							pathname={pathname}
						/>
					))}
				</SidebarMenu>
				<div className="sidebar-account">
					<span aria-hidden="true">Y</span>
					<div>
						<strong>You</strong>
						<small>Owner</small>
					</div>
				</div>
			</SidebarFooter>
		</>
	);
}

function OrganizationSettingsSidebar({ pathname }: { pathname: string }) {
	return (
		<>
			<SidebarHeader className="settings-sidebar-header">
				<SidebarMenu>
					<SidebarMenuItem>
						<SidebarMenuButton
							asChild
							className="settings-back-button"
							tooltip="Back to workspace"
						>
							<Link to="/app">
								<ArrowLeft />
								<span>Back to workspace</span>
							</Link>
						</SidebarMenuButton>
					</SidebarMenuItem>
				</SidebarMenu>

				<div className="settings-sidebar-scope">
					<span aria-hidden="true">OW</span>
					<div>
						<strong>Organization settings</strong>
						<small>Odd Works</small>
					</div>
				</div>
			</SidebarHeader>

			<SidebarContent>
				<NavigationGroup
					items={organizationAdminNavigation}
					label="Organization"
					pathname={pathname}
				/>
				<NavigationGroup
					items={organizationOperationsNavigation}
					label="Operations"
					pathname={pathname}
				/>
			</SidebarContent>

			<SidebarFooter className="product-sidebar-footer settings-sidebar-footer">
				<SidebarMenu>
					<NavigationMenuItem
						item={accountSettingsNavigation}
						pathname={pathname}
					/>
				</SidebarMenu>
				<div className="sidebar-account settings-admin-account">
					<span aria-hidden="true">
						<ShieldCheck />
					</span>
					<div>
						<strong>Head admin</strong>
						<small>Full organization access</small>
					</div>
				</div>
			</SidebarFooter>
		</>
	);
}

interface NavigationGroupProps {
	items: NavigationItem[];
	label: string;
	pathname: string;
}

function NavigationGroup({ items, label, pathname }: NavigationGroupProps) {
	return (
		<SidebarGroup>
			<SidebarGroupLabel>{label}</SidebarGroupLabel>
			<SidebarGroupContent>
				<SidebarMenu>
					{items.map((item) => (
						<NavigationMenuItem
							item={item}
							key={item.href}
							pathname={pathname}
						/>
					))}
				</SidebarMenu>
			</SidebarGroupContent>
		</SidebarGroup>
	);
}

interface NavigationMenuItemProps {
	item: NavigationItem;
	pathname: string;
}

function NavigationMenuItem({ item, pathname }: NavigationMenuItemProps) {
	const Icon = item.icon;
	const normalizedPathname = pathname.endsWith("/")
		? pathname.slice(0, -1)
		: pathname;
	const isActive =
		item.match === "exact"
			? normalizedPathname === item.href
			: normalizedPathname.startsWith(item.href);

	return (
		<SidebarMenuItem>
			<SidebarMenuButton asChild isActive={isActive} tooltip={item.label}>
				<Link to={item.href}>
					<Icon />
					<span>{item.label}</span>
				</Link>
			</SidebarMenuButton>
		</SidebarMenuItem>
	);
}
