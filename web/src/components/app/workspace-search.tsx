import { useNavigate } from "@tanstack/react-router";
import { Search } from "lucide-react";
import { useEffect, useState } from "react";
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

export function WorkspaceSearch() {
	const [open, setOpen] = useState(false);
	const navigate = useNavigate();

	useEffect(() => {
		function openSearch(event: KeyboardEvent) {
			if (event.key.toLowerCase() === "k" && (event.metaKey || event.ctrlKey)) {
				event.preventDefault();
				setOpen(true);
			}
		}
		window.addEventListener("keydown", openSearch);
		return () => window.removeEventListener("keydown", openSearch);
	}, []);

	return (
		<Dialog onOpenChange={setOpen} open={open}>
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
					<DialogTitle>Search the workspace</DialogTitle>
					<DialogDescription>
						Open a personal, collaborative, resource, or settings page.
					</DialogDescription>
				</DialogHeader>
				<Command>
					<CommandInput autoFocus placeholder="Search pages and settings…" />
					<CommandList>
						<CommandEmpty>No matching page.</CommandEmpty>
						{destinationGroups.map((group) => (
							<CommandGroup heading={group.label} key={group.label}>
								{group.items.map((item) => {
									const Icon = item.icon;
									return (
										<CommandItem
											key={item.href}
											onSelect={() => {
												setOpen(false);
												navigate({ to: item.href });
											}}
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
