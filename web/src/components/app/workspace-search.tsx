import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { MessageSquareText, Search } from "lucide-react";
import {
	createContext,
	type ReactNode,
	useCallback,
	useContext,
	useEffect,
	useMemo,
	useState,
} from "react";
import {
	accountSettingsNavigation,
	collaborationNavigation,
	type NavigationItem,
	organizationAdminNavigation,
	organizationOperationsNavigation,
	resourceNavigation,
	settingsNavigation,
	soloNavigation,
} from "@/components/app/navigation";
import { Button } from "@/components/ui/button";
import {
	Command,
	CommandEmpty,
	CommandGroup,
	CommandInput,
	CommandItem,
	CommandList,
} from "@/components/ui/command";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
} from "@/components/ui/dialog";
import { useWorkspace } from "@/lib/auth/session";
import { relativeTime } from "@/lib/format";
import { threadsQueryOptions, threadTitle } from "@/lib/threads/client";

interface DestinationGroup {
	items: NavigationItem[];
	label: string;
}

const destinationGroups: DestinationGroup[] = [
	{ items: soloNavigation, label: "Solo workspace" },
	{ items: collaborationNavigation, label: "Collaborate" },
	{ items: resourceNavigation, label: "Resources" },
	{
		items: [
			...organizationAdminNavigation,
			...organizationOperationsNavigation,
		],
		label: "Organization settings",
	},
	{
		items: settingsNavigation.filter(
			(item) => item.href === accountSettingsNavigation.href,
		),
		label: "Account",
	},
];

/**
 * What the palette was opened to do. "conversations" narrows it to threads —
 * the sidebar shows a few and sends you here for the rest, so arriving on a
 * list of pages instead would be the wrong answer to "see all".
 */
type SearchScope = "conversations" | "everything";

interface WorkspaceSearchControl {
	open: (scope?: SearchScope) => void;
}

const WorkspaceSearchContext = createContext<WorkspaceSearchControl | null>(
	null,
);

/** Opens the palette from anywhere in the workspace, optionally scoped. */
export function useWorkspaceSearch(): WorkspaceSearchControl {
	const control = useContext(WorkspaceSearchContext);
	if (!control) {
		throw new Error(
			"useWorkspaceSearch is only available inside the workspace shell",
		);
	}
	return control;
}

interface SearchState {
	open: boolean;
	scope: SearchScope;
}

export function WorkspaceSearchProvider({ children }: { children: ReactNode }) {
	const [state, setState] = useState<SearchState>({
		open: false,
		scope: "everything",
	});

	const open = useCallback((scope: SearchScope = "everything") => {
		setState({ open: true, scope });
	}, []);

	useEffect(() => {
		function shortcut(event: KeyboardEvent) {
			if (event.key.toLowerCase() === "k" && (event.metaKey || event.ctrlKey)) {
				event.preventDefault();
				open();
			}
		}
		window.addEventListener("keydown", shortcut);
		return () => window.removeEventListener("keydown", shortcut);
	}, [open]);

	const control = useMemo(() => ({ open }), [open]);

	return (
		<WorkspaceSearchContext.Provider value={control}>
			<SearchStateContext.Provider value={{ setState, state }}>
				{children}
			</SearchStateContext.Provider>
		</WorkspaceSearchContext.Provider>
	);
}

// The dialog lives in the topbar while the control is used from the sidebar,
// so the two halves meet here rather than through props.
const SearchStateContext = createContext<{
	setState: (state: SearchState) => void;
	state: SearchState;
} | null>(null);

export function WorkspaceSearch() {
	const shared = useContext(SearchStateContext);
	if (!shared) {
		throw new Error(
			"WorkspaceSearch is only available inside WorkspaceSearchProvider",
		);
	}
	const { setState, state } = shared;
	const { organization } = useWorkspace();
	const navigate = useNavigate();
	const threads = useQuery({
		...threadsQueryOptions(organization.id),
		enabled: state.open,
	});
	const conversationsOnly = state.scope === "conversations";

	function go(run: () => void) {
		setState({ open: false, scope: "everything" });
		run();
	}

	return (
		<Dialog
			onOpenChange={(open) => setState({ open, scope: state.scope })}
			open={state.open}
		>
			<DialogTrigger asChild>
				<Button className="app-search" size="sm" type="button" variant="ghost">
					<Search aria-hidden="true" />
					<span>Search</span>
					<kbd>⌘K</kbd>
				</Button>
			</DialogTrigger>
			<DialogContent
				className="workspace-search-dialog"
				showCloseButton={false}
			>
				<DialogHeader className="sr-only">
					<DialogTitle>
						{conversationsOnly ? "Find a conversation" : "Search the workspace"}
					</DialogTitle>
					<DialogDescription>
						{conversationsOnly
							? "Open any conversation with your primary agent."
							: "Open a personal, collaborative, resource, or settings page."}
					</DialogDescription>
				</DialogHeader>
				<Command>
					<CommandInput
						autoFocus
						placeholder={
							conversationsOnly
								? "Search conversations…"
								: "Search conversations, pages and settings…"
						}
					/>
					<CommandList>
						<CommandEmpty>
							{conversationsOnly
								? "No matching conversation."
								: "No matching page."}
						</CommandEmpty>
						<CommandGroup heading="Conversations">
							{threads.data?.map((thread) => (
								<CommandItem
									key={thread.id}
									onSelect={() =>
										go(() =>
											navigate({
												params: { threadId: thread.id },
												to: "/app/threads/$threadId",
											}),
										)
									}
									value={`${threadTitle(thread)} conversation ${thread.id}`}
								>
									<MessageSquareText aria-hidden="true" />
									<span>{threadTitle(thread)}</span>
									<small className="workspace-search-meta">
										{relativeTime(thread.updated_at)}
									</small>
								</CommandItem>
							))}
						</CommandGroup>
						{conversationsOnly
							? null
							: destinationGroups.map((group) => (
									<CommandGroup heading={group.label} key={group.label}>
										{group.items.map((item) => {
											const Icon = item.icon;
											return (
												<CommandItem
													key={item.href}
													onSelect={() => go(() => navigate({ to: item.href }))}
													value={`${item.label} ${group.label}`}
												>
													<Icon aria-hidden="true" />
													<span>{item.label}</span>
												</CommandItem>
											);
										})}
									</CommandGroup>
								))}
					</CommandList>
				</Command>
			</DialogContent>
		</Dialog>
	);
}
