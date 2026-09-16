import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

interface EmptyStateProps {
	action?: ReactNode;
	description: string;
	icon: LucideIcon;
	title: string;
}

export function EmptyState({
	action,
	description,
	icon: Icon,
	title,
}: EmptyStateProps) {
	return (
		<div className="empty-state">
			<span aria-hidden="true">
				<Icon />
			</span>
			<strong>{title}</strong>
			<p>{description}</p>
			{action}
		</div>
	);
}
