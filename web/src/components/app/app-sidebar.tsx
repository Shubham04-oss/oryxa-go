import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useLocation, useNavigate } from "@tanstack/react-router";
import {
	ChevronLeft,
	ChevronRight,
	ChevronsUpDown,
	LogOut,
	MessageSquareText,
	Plus,
} from "lucide-react";
import { Fragment, type ReactNode, useState } from "react";
import { displayName, initials, roleLabel } from "@/components/app/identity";
import {
	type AppPath,
	collaborationNavigation,
	type NavigationItem,
	organizationAdminNavigation,
	organizationOperationsNavigation,
	resourceNavigation,
	settingsNavigation,
	soloNavigation,
} from "@/components/app/navigation";
import { useWorkspaceSearch } from "@/components/app/workspace-search";
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
	SidebarMenuAction,
	SidebarMenuButton,
	SidebarMenuItem,
	SidebarMenuSub,
	SidebarMenuSubButton,
	SidebarMenuSubItem,
	SidebarRail,
	SidebarTrigger,
	useSidebar,
} from "@/components/ui/sidebar";
import { signOut } from "@/lib/auth/client";
import {
	selectOrganization,
	useMembership,
	useWorkspace,
} from "@/lib/auth/session";
import {
	createThread,
	recentThreads,
	SIDEBAR_THREAD_LIMIT,
	threadsQueryKey,
	threadsQueryOptions,
	threadTitle,
} from "@/lib/threads/client";

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
				<div className="sidebar-brand-row">
					<Link className="product-wordmark" to="/app">
						<span aria-hidden="true">O</span>
						<strong>The Weirdly ODD</strong>
					</Link>
					<SidebarTrigger
						aria-label="Toggle navigation"
						className="sidebar-collapse"
						size="icon-xs"
					/>
				</div>

				<SidebarMenu>
					<SidebarMenuItem>
						<OrganizationSwitcher />
					</SidebarMenuItem>
				</SidebarMenu>
			</SidebarHeader>

			<SidebarContent>
				<NavigationGroup
					childrenAfter="/app"
					items={soloNavigation}
					label="Solo workspace"
					pathname={pathname}
				>
					<ConversationsMenu pathname={pathname} />
				</NavigationGroup>
				<NavigationGroup
					items={collaborationNavigation}
					label="Collaborate"
					pathname={pathname}
				/>
				<NavigationGroup
					items={resourceNavigation}
					label="Resources"
					pathname={pathname}
				/>
			</SidebarContent>

			<SidebarFooter className="product-sidebar-footer">
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
				{settingsNavigation.map((item) => {
					const Icon = item.icon;
					return (
						<DropdownMenuItem
							key={item.href}
							onSelect={() => navigate({ to: item.href })}
						>
							<Icon />
							{item.label}
						</DropdownMenuItem>
					);
				})}
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
				<div className="sidebar-brand-row">
					<SidebarMenu>
						<SidebarMenuItem>
							<SidebarMenuButton
								asChild
								className="settings-sidebar-scope"
								size="lg"
								tooltip="Back to workspace"
							>
								<Link to="/app">
									<ChevronLeft className="settings-scope-back" />
									<span aria-hidden="true" className="settings-scope-mark">
										{initials(organization.name)}
									</span>
									<span className="settings-scope-copy">
										<strong>Organization settings</strong>
										<small>{organization.name}</small>
									</span>
								</Link>
							</SidebarMenuButton>
						</SidebarMenuItem>
					</SidebarMenu>
					<SidebarTrigger
						aria-label="Toggle navigation"
						className="sidebar-collapse"
						size="icon-xs"
					/>
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
				<AccountMenu />
			</SidebarFooter>
		</>
	);
}

/**
 * Conversations, as a section of the sidebar rather than a page of its own.
 *
 * It shows the few you were most recently in and hands the rest to search:
 * past a handful, a list you scroll is slower than a name you type. The
 * section stays open once you open it, and opens itself whenever you are
 * reading a conversation, so the one you are in is always on screen.
 */
function ConversationsMenu({ pathname }: { pathname: string }) {
	const { organization } = useWorkspace();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const search = useWorkspaceSearch();
	const readingId = pathname.match(/^\/app\/threads\/([^/]+)/)?.[1];
	const reading = readingId !== undefined;
	const [opened, setOpened] = useState(false);
	const expanded = opened || reading;
	const threads = useQuery(threadsQueryOptions(organization.id));
	const recent = recentThreads(threads.data, SIDEBAR_THREAD_LIMIT, readingId);

	const start = useMutation({
		mutationFn: () => createThread(organization.id),
		onSuccess: async (thread) => {
			await queryClient.invalidateQueries({
				queryKey: threadsQueryKey(organization.id),
			});
			setOpened(true);
			await navigate({
				params: { threadId: thread.id },
				to: "/app/threads/$threadId",
			});
		},
	});

	return (
		<SidebarMenuItem>
			<SidebarMenuButton
				aria-expanded={expanded}
				isActive={reading}
				onClick={() => setOpened(!expanded)}
				tooltip="Conversations"
			>
				<MessageSquareText />
				<span>Conversations</span>
				<ChevronRight className="sidebar-disclosure" data-open={expanded} />
			</SidebarMenuButton>
			<SidebarMenuAction
				aria-label="New conversation"
				disabled={start.isPending}
				onClick={() => start.mutate()}
			>
				<Plus />
			</SidebarMenuAction>
			{expanded ? (
				<SidebarMenuSub>
					{recent.map((thread) => (
						<SidebarMenuSubItem key={thread.id}>
							<SidebarMenuSubButton asChild isActive={thread.id === readingId}>
								<Link
									params={{ threadId: thread.id }}
									to="/app/threads/$threadId"
								>
									<span>{threadTitle(thread)}</span>
								</Link>
							</SidebarMenuSubButton>
						</SidebarMenuSubItem>
					))}
					{recent.length === 0 ? (
						<SidebarMenuSubItem>
							<p className="sidebar-sub-empty">
								{threads.isPending ? "Loading…" : "Nothing yet."}
							</p>
						</SidebarMenuSubItem>
					) : null}
					<SidebarMenuSubItem>
						<button
							className="sidebar-see-all"
							onClick={() => search.open("conversations")}
							type="button"
						>
							See all
							<kbd>⌘K</kbd>
						</button>
					</SidebarMenuSubItem>
				</SidebarMenuSub>
			) : null}
		</SidebarMenuItem>
	);
}

interface NavigationGroupProps {
	/** A section with children of its own, such as Conversations. */
	children?: ReactNode;
	/** Which link `children` follows; without it they go last. */
	childrenAfter?: AppPath;
	items: NavigationItem[];
	label: string;
	pathname: string;
}

function NavigationGroup({
	children,
	childrenAfter,
	items,
	label,
	pathname,
}: NavigationGroupProps) {
	return (
		<SidebarGroup>
			<SidebarGroupLabel>{label}</SidebarGroupLabel>
			<SidebarGroupContent>
				<SidebarMenu>
					{items.map((item) => (
						<Fragment key={item.href}>
							<NavigationMenuItem item={item} pathname={pathname} />
							{item.href === childrenAfter ? children : null}
						</Fragment>
					))}
					{childrenAfter ? null : children}
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
