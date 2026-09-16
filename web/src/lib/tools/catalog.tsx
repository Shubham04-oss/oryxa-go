import {
	Blocks,
	Boxes,
	CalendarDays,
	CircleUser,
	Cloud,
	FileText,
	Frame,
	GitFork,
	Grid3x3,
	Headset,
	LifeBuoy,
	ListChecks,
	MessageSquare,
	SquareKanban,
	Table,
	Workflow,
} from "lucide-react";
import type {
	CredentialType,
	CredentialTypeDescription,
	ProviderDescription,
} from "@/lib/connections/client";

/**
 * How a tool is introduced to people. Connections owns what a provider takes;
 * only its name and one line about it live here, so a provider added to the
 * service still appears, under a name made from its key.
 */
const toolCopy: Record<string, { description: string; name: string }> = {
	airtable: { description: "Bases and records", name: "Airtable" },
	asana: { description: "Tasks and projects", name: "Asana" },
	atlassian: { description: "Jira and Confluence", name: "Atlassian" },
	box: { description: "Files and folders", name: "Box" },
	clickup: { description: "Tasks and docs", name: "ClickUp" },
	dropbox: { description: "Files and folders", name: "Dropbox" },
	figma: { description: "Design files and comments", name: "Figma" },
	github: { description: "Repositories and pull requests", name: "GitHub" },
	google: { description: "Mail, Drive and calendars", name: "Google" },
	hubspot: { description: "Contacts and deals", name: "HubSpot" },
	intercom: { description: "Conversations and contacts", name: "Intercom" },
	linear: { description: "Issues and projects", name: "Linear" },
	microsoft: {
		description: "Mail, files, calendar and Teams",
		name: "Microsoft",
	},
	notion: { description: "Pages and databases", name: "Notion" },
	salesforce: { description: "Accounts and opportunities", name: "Salesforce" },
	slack: { description: "Channels and messages", name: "Slack" },
	zendesk: { description: "Tickets and customers", name: "Zendesk" },
};

export interface ToolOption {
	/** How an organization can enable it, in the order to offer them. */
	credentialTypes: CredentialTypeDescription[];
	description: string;
	name: string;
	provider: string;
}

/** A provider's name, made from its key when the catalogue has no copy for it. */
export function toolName(provider: string): string {
	return toolCopy[provider]?.name ?? titleCase(provider);
}

function titleCase(provider: string): string {
	return provider
		.split(/[_-]/)
		.filter(Boolean)
		.map((word) => word.charAt(0).toUpperCase() + word.slice(1))
		.join(" ");
}

/** A provider as the tools page shows it: what it is, and how to enable it. */
export function describeTool(description: ProviderDescription): ToolOption {
	return {
		credentialTypes: description.credential_types,
		description: toolCopy[description.provider]?.description ?? "",
		name: toolName(description.provider),
		provider: description.provider,
	};
}

export function describeTools(
	descriptions: ProviderDescription[],
): ToolOption[] {
	return descriptions.map(describeTool);
}

export const credentialTypeNames: Record<CredentialType, string> = {
	api_key: "API key",
	basic: "Username and password",
	bearer: "Personal access token",
	installation: "GitHub App",
	oauth2: "OAuth app",
	oauth2_client_credentials: "App-only access",
};

export function ToolGlyph({ provider }: { provider: string }) {
	switch (provider) {
		case "airtable":
			return <Table />;
		case "asana":
			return <SquareKanban />;
		case "atlassian":
			return <Workflow />;
		case "box":
			return <Boxes />;
		case "clickup":
			return <ListChecks />;
		case "dropbox":
			return <Cloud />;
		case "figma":
			return <Frame />;
		case "github":
			return <GitFork />;
		case "google":
			return <CalendarDays />;
		case "hubspot":
			return <CircleUser />;
		case "intercom":
			return <Headset />;
		case "linear":
			return <ListChecks />;
		case "microsoft":
			return <Grid3x3 />;
		case "notion":
			return <FileText />;
		case "salesforce":
			return <Cloud />;
		case "slack":
			return <MessageSquare />;
		case "zendesk":
			return <LifeBuoy />;
		default:
			return <Blocks />;
	}
}
