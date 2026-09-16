import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";

interface SettingsSectionProps {
	children: ReactNode;
	description: ReactNode;
	footer?: ReactNode;
	id: string;
	title: string;
}

export function SettingsSection({
	children,
	description,
	footer,
	id,
	title,
}: SettingsSectionProps) {
	return (
		<section className="settings-section" aria-labelledby={id}>
			<header>
				<h2 id={id}>{title}</h2>
				<p>{description}</p>
			</header>
			<Card className="settings-section-card">
				<div className="settings-section-content">{children}</div>
				{footer ? <footer>{footer}</footer> : null}
			</Card>
		</section>
	);
}

interface SettingsRowProps {
	children: ReactNode;
	description: ReactNode;
	htmlFor?: string;
	title: string;
}

export function SettingsRow({
	children,
	description,
	htmlFor,
	title,
}: SettingsRowProps) {
	return (
		<div className="settings-row">
			<div className="settings-row-copy">
				<Label htmlFor={htmlFor}>{title}</Label>
				<p>{description}</p>
			</div>
			<div className="settings-row-control">{children}</div>
		</div>
	);
}

export function SettingsPageBody({ children }: { children: ReactNode }) {
	return <div className="settings-page-body">{children}</div>;
}

interface StatusBadgeProps {
	children: ReactNode;
	icon?: LucideIcon;
	tone: "active" | "admin" | "danger" | "neutral" | "warning";
}

export function StatusBadge({ children, icon: Icon, tone }: StatusBadgeProps) {
	return (
		<Badge className="settings-status-badge" data-tone={tone} variant="ghost">
			{Icon ? (
				<Icon aria-hidden="true" />
			) : (
				<span className="settings-status-marker" aria-hidden="true" />
			)}
			{children}
		</Badge>
	);
}

const placeholderRows = ["first", "second", "third"];

/** Rows standing in for a list while it loads. */
export function SettingsListSkeleton() {
	return (
		<div aria-busy="true" className="settings-list">
			{placeholderRows.map((row) => (
				<div className="settings-list-row" key={row}>
					<Skeleton className="size-8 rounded-full" />
					<div className="settings-list-copy">
						<Skeleton className="h-3 w-40" />
						<Skeleton className="h-2.5 w-24" />
					</div>
				</div>
			))}
		</div>
	);
}

export function SettingsListEmpty({ children }: { children: ReactNode }) {
	return <p className="settings-list-empty">{children}</p>;
}
