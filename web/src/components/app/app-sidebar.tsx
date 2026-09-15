import { useQueryClient } from "@tanstack/react-query";
import { Link, useLocation, useNavigate } from "@tanstack/react-router";
import {
	ArrowLeft,
	ChevronsUpDown,
	CircleUserRound,
	LogOut,
	Plus,
	ShieldCheck,
} from "lucide-react";
import { displayName, initials, roleLabel } from "@/components/app/identity";
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
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuRadioGroup,
	DropdownMenuRadioItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
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
	useSidebar,
} from "@/components/ui/sidebar";
import { type Role, signOut } from "@/lib/auth/client";
import {
	selectOrganization,
	useMembership,
	useWorkspace,
} from "@/lib/auth/session";

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
						<OrganizationSwitcher />
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
				<AccountMenu />
			</SidebarFooter>
		</>
	);
}

function OrganizationSwitcher() {
	const { organization, organizations } = useWorkspace();
	const { isMobile } = useSidebar();
	const navigate = useNavigate();

	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<SidebarMenuButton
					className="organization-switcher"
					size="lg"
					tooltip={organization.name}
				>
					<span className="organization-mark" aria-hidden="true">
						{initials(organization.name)}
					</span>
					<span className="organization-copy">
						<strong>{organization.name}</strong>
						<small>Organization</small>
					</span>
					<ChevronsUpDown className="organization-switcher-icon" />
				</SidebarMenuButton>
			</DropdownMenuTrigger>
			<DropdownMenuContent
				className="organization-menu"
				side={isMobile ? "bottom" : "right"}
			>
				<DropdownMenuLabel>Organizations</DropdownMenuLabel>
				<DropdownMenuRadioGroup
					onValueChange={selectOrganization}
					value={organization.id}
				>
					{organizations.map((candidate) => (
						<DropdownMenuRadioItem
							className="organization-menu-item"
							key={candidate.id}
							value={candidate.id}
						>
							<span className="organization-menu-mark" aria-hidden="true">
								{initials(candidate.name)}
							</span>
							<span className="organization-menu-name">{candidate.name}</span>
						</DropdownMenuRadioItem>
					))}
				</DropdownMenuRadioGroup>
				<DropdownMenuSeparator />
				<DropdownMenuItem onSelect={() => navigate({ to: "/onboarding" })}>
					<Plus />
					Create organization
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

function AccountMenu() {
	const { organization, user } = useWorkspace();
	const membership = useMembership(organization.id);
	const { isMobile } = useSidebar();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const name = displayName(user);

	async function leave() {
		// Even if signing out fails, sign-in lets the member choose an account.
		await signOut().catch(() => undefined);
		await navigate({ to: "/auth/sign-in" });
		queryClient.clear();
	}

	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<button
					className="sidebar-account sidebar-account-button"
					type="button"
				>
					<span aria-hidden="true">{initials(name)}</span>
					<span className="sidebar-account-copy">
						<strong>{name}</strong>
						<small>
							{membership ? roleLabel(membership.role) : organization.name}
						</small>
					</span>
					<ChevronsUpDown className="sidebar-account-icon" />
				</button>
			</DropdownMenuTrigger>
			<DropdownMenuContent
				align="end"
				className="account-menu"
				side={isMobile ? "top" : "right"}
			>
				<DropdownMenuLabel>{user.email}</DropdownMenuLabel>
				<DropdownMenuSeparator />
				<DropdownMenuItem onSelect={leave}>
					<LogOut />
					Sign out
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

function OrganizationSettingsSidebar({ pathname }: { pathname: string }) {
	const { organization } = useWorkspace();

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
					<span aria-hidden="true">{initials(organization.name)}</span>
					<div>
						<strong>Organization settings</strong>
						<small>{organization.name}</small>
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
				<OrganizationAccess />
			</SidebarFooter>
		</>
	);
}

const accessSummaries: Record<Role, string> = {
	admin: "Manages members and teams",
	member: "Organization member",
	owner: "Full organization access",
};

function OrganizationAccess() {
	const { organization } = useWorkspace();
	const membership = useMembership(organization.id);
	const manages = membership?.role === "owner" || membership?.role === "admin";
	const Icon = manages ? ShieldCheck : CircleUserRound;

	return (
		<div
			className="sidebar-account settings-admin-account"
			data-manages={manages}
		>
			<span aria-hidden="true">
				<Icon />
			</span>
			<div>
				<strong>
					{membership ? roleLabel(membership.role) : "Organization access"}
				</strong>
				<small>
					{membership ? accessSummaries[membership.role] : organization.name}
				</small>
			</div>
		</div>
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
