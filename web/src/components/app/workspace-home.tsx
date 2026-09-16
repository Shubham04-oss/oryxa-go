import { Link } from "@tanstack/react-router";
import { ArrowUpRight, Blocks, Clock3, ListTodo, ScanText } from "lucide-react";
import { useRef, useState } from "react";
import { ThreadComposer } from "@/components/app/thread-composer";
import { WorkspaceSection } from "@/components/app/workspace-section";
import { useWorkspace } from "@/lib/auth/session";

const recentThreads = [
	{ title: "Plan the launch checklist", updated: "3 min" },
	{ title: "Review billing edge cases", updated: "Yesterday" },
	{ title: "Shape the connections rollout", updated: "Monday" },
];

const starters = [
	{
		description: "Turn an idea into clear next steps",
		icon: ListTodo,
		prompt: "Help me plan a project from the first step.",
		title: "Plan something",
		tone: "warm",
	},
	{
		description: "Bring in a file, repository, or decision",
		icon: ScanText,
		prompt: "Help me review something and surface what matters.",
		title: "Review something",
		tone: "rose",
	},
	{
		description: "Work across the apps already connected",
		icon: Blocks,
		prompt: "Help me work with one of my connected tools.",
		title: "Use a tool",
		tone: "sage",
	},
] as const;

export function WorkspaceHome() {
	const { organization } = useWorkspace();
	const composerRef = useRef<HTMLTextAreaElement>(null);
	const [draft, setDraft] = useState("");

	function applyStarter(prompt: string) {
		setDraft(prompt);
		requestAnimationFrame(() => composerRef.current?.focus());
	}

	return (
		<section className="workspace-launchpad">
			<header className="workspace-launchpad-header">
				<span>{organization.name}</span>
				<h1>What are we working on?</h1>
			</header>

			<ThreadComposer
				inputRef={composerRef}
				onChange={setDraft}
				value={draft}
			/>

			<div className="workspace-home-panels">
				<WorkspaceSection title="Start here">
					<div className="workspace-naked-list">
						{starters.map((starter) => {
							const Icon = starter.icon;
							return (
								<button
									className="workspace-starter-row"
									data-tone={starter.tone}
									key={starter.title}
									onClick={() => applyStarter(starter.prompt)}
									type="button"
								>
									<Icon aria-hidden="true" />
									<span>
										<strong>{starter.title}</strong>
										<small>{starter.description}</small>
									</span>
									<ArrowUpRight aria-hidden="true" />
								</button>
							);
						})}
					</div>
				</WorkspaceSection>

				<WorkspaceSection
					action={<Link to="/app/threads">View all</Link>}
					title="Recent"
				>
					<div className="workspace-naked-list">
						{recentThreads.map((thread) => (
							<Link
								className="workspace-recent-row"
								key={thread.title}
								to="/app/threads"
							>
								<span>{thread.title}</span>
								<small>
									<Clock3 />
									{thread.updated}
								</small>
							</Link>
						))}
					</div>
				</WorkspaceSection>
			</div>
		</section>
	);
}
