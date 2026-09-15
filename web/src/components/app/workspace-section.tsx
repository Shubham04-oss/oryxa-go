import type { ReactNode } from "react";

interface WorkspaceSectionProps {
	action?: ReactNode;
	children: ReactNode;
	title: string;
}

export function WorkspaceSection({
	action,
	children,
	title,
}: WorkspaceSectionProps) {
	return (
		<section className="workspace-naked-section">
			<header>
				<h2>{title}</h2>
				{action}
			</header>
			{children}
		</section>
	);
}
