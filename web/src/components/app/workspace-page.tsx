import type { ReactNode } from "react";

interface WorkspacePageProps {
	actions?: ReactNode;
	children?: ReactNode;
	description?: string;
	title: string;
}

export function WorkspacePage({
	actions,
	children,
	description,
	title,
}: WorkspacePageProps) {
	return (
		<section className="workspace-page">
			<header className="workspace-page-header">
				<div>
					<h1>{title}</h1>
					{description ? <p>{description}</p> : null}
				</div>
				{actions ? (
					<div className="workspace-page-actions">{actions}</div>
				) : null}
			</header>
			{children}
		</section>
	);
}
