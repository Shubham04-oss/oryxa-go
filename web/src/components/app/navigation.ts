import {
	Blocks,
	Bot,
	Building2,
	CircleUserRound,
	CreditCard,
	Database,
	Files,
	LayoutDashboard,
	type LucideIcon,
	Mail,
	Network,
	Plug,
	Settings2,
	UserRoundCog,
	UsersRound,
} from "lucide-react";

export type AppPath =
	| "/app"
	| "/app/knowledge"
	| "/app/agents"
	| "/app/teamspaces"
	| "/app/teams"
	| "/app/files"
	| "/app/connections"
	| "/app/settings/organization"
	| "/app/settings/organization/members"
	| "/app/settings/organization/teams"
	| "/app/settings/organization/tools"
	| "/app/settings/organization/email"
	| "/app/settings/organization/billing"
	| "/app/settings/profile";

export interface NavigationItem {
	href: AppPath;
	icon: LucideIcon;
	label: string;
	match?: "exact" | "prefix";
}

export const soloNavigation: NavigationItem[] = [
	{ href: "/app", icon: LayoutDashboard, label: "Overview", match: "exact" },
	{ href: "/app/agents", icon: Bot, label: "Agent system" },
];

export const collaborationNavigation: NavigationItem[] = [
	{ href: "/app/teamspaces", icon: Network, label: "Teamspaces" },
	{ href: "/app/teams", icon: UsersRound, label: "People & teams" },
];

export const resourceNavigation: NavigationItem[] = [
	{ href: "/app/knowledge", icon: Database, label: "Knowledge bases" },
	{ href: "/app/files", icon: Files, label: "Files" },
	{ href: "/app/connections", icon: Plug, label: "Apps & tools" },
];

// Compatibility aliases keep the navigation contract stable for extensions
// while the visible shell moves to explicit work modes.
export const workspaceNavigation = soloNavigation;
export const organizationNavigation = collaborationNavigation;

export const accountSettingsNavigation: NavigationItem = {
	href: "/app/settings/profile",
	icon: CircleUserRound,
	label: "Account settings",
};

export const settingsNavigation: NavigationItem[] = [
	{
		href: "/app/settings/organization",
		icon: Settings2,
		label: "Organization settings",
	},
	accountSettingsNavigation,
];

export const organizationAdminNavigation: NavigationItem[] = [
	{
		href: "/app/settings/organization",
		icon: Building2,
		label: "Organization",
		match: "exact",
	},
	{
		href: "/app/settings/organization/members",
		icon: UserRoundCog,
		label: "Members",
	},
	{
		href: "/app/settings/organization/teams",
		icon: UsersRound,
		label: "Teams",
	},
];

export const organizationOperationsNavigation: NavigationItem[] = [
	{
		href: "/app/settings/organization/tools",
		icon: Blocks,
		label: "Tool access",
	},
	{
		href: "/app/settings/organization/email",
		icon: Mail,
		label: "Email",
	},
	{
		href: "/app/settings/organization/billing",
		icon: CreditCard,
		label: "Billing",
	},
];
