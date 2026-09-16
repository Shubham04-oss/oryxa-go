import {
	Blocks,
	Building2,
	CircleUserRound,
	CreditCard,
	Files,
	House,
	type LucideIcon,
	Mail,
	MessageSquareText,
	Plug,
	Settings2,
	UserRoundCog,
	UsersRound,
} from "lucide-react";

export type AppPath =
	| "/app"
	| "/app/threads"
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

export const workspaceNavigation: NavigationItem[] = [
	{ href: "/app", icon: House, label: "Home", match: "exact" },
	{ href: "/app/threads", icon: MessageSquareText, label: "Threads" },
];

export const organizationNavigation: NavigationItem[] = [
	{ href: "/app/teams", icon: UsersRound, label: "Teams" },
	{ href: "/app/files", icon: Files, label: "Files" },
	{ href: "/app/connections", icon: Plug, label: "Connections" },
];

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
		label: "Tools",
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
