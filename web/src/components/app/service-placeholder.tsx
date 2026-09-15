import type { LucideIcon } from "lucide-react";
import { WorkspacePage } from "@/components/app/workspace-page";
import { Card } from "@/components/ui/card";

interface ServicePlaceholderProps {
	description: string;
	icon: LucideIcon;
	title: string;
}

export function ServicePlaceholder({
	description,
	icon: Icon,
	title,
}: ServicePlaceholderProps) {
	return (
		<WorkspacePage description={description} title={title}>
			<Card className="service-placeholder">
				<span aria-hidden="true">
					<Icon />
				</span>
				<strong>{title}</strong>
			</Card>
		</WorkspacePage>
	);
}
