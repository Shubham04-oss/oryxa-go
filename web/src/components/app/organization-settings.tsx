import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Label } from "@/components/ui/label";

interface SettingsSectionProps {
	children: ReactNode;
	description: string;
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
	description: string;
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
	tone: "active" | "admin" | "neutral";
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
