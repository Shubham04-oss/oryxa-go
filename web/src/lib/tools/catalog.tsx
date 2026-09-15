import {
	Blocks,
	CalendarDays,
	GitFork,
	ListChecks,
	MessageSquare,
} from "lucide-react";

export type ToolProvider =
	| "github"
	| "google"
	| "slack"
	| "linear"
	| "microsoft";

export interface ToolOption {
	description: string;
	name: string;
	provider: ToolProvider;
}

export const toolOptions: ToolOption[] = [
	{
		provider: "github",
		name: "GitHub",
		description: "Repositories and pull requests",
	},
	{
		provider: "google",
		name: "Google",
		description: "Mail, Drive and calendars",
	},
	{
		provider: "slack",
		name: "Slack",
		description: "Channels and messages",
	},
	{
		provider: "linear",
		name: "Linear",
		description: "Issues and projects",
	},
	{
		provider: "microsoft",
		name: "Microsoft",
		description: "Mail, files, calendar and Teams",
	},
];

export function ToolGlyph({ provider }: { provider: ToolProvider }) {
	switch (provider) {
		case "github":
			return <GitFork />;
		case "google":
			return <CalendarDays />;
		case "slack":
			return <MessageSquare />;
		case "linear":
			return <ListChecks />;
		case "microsoft":
			return <Blocks />;
	}
}
