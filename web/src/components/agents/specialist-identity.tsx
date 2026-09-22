import {
	ChartNoAxesCombined,
	Compass,
	PenLine,
	Workflow,
	Wrench,
} from "lucide-react";
import type { ComponentType } from "react";

export type SpecialistFocus =
	| "research"
	| "operations"
	| "analysis"
	| "writing"
	| "custom";

export const specialistFocuses: {
	key: SpecialistFocus;
	label: string;
	caption: string;
	icon: ComponentType<{ "aria-hidden"?: boolean }>;
	guidance: string;
}[] = [
	{
		key: "research",
		label: "Research",
		caption: "Find and verify answers",
		icon: Compass,
		guidance:
			"Search relevant knowledge and connected sources, distinguish evidence from inference, and cite the material actually read.",
	},
	{
		key: "operations",
		label: "Operations",
		caption: "Move work forward",
		icon: Workflow,
		guidance:
			"Track the state of assigned work, use available connected-app tools for concrete steps, and report outcomes and blockers precisely.",
	},
	{
		key: "analysis",
		label: "Analysis",
		caption: "Make sense of data",
		icon: ChartNoAxesCombined,
		guidance:
			"Check source quality, show important assumptions, and make calculations or comparisons reproducible.",
	},
	{
		key: "writing",
		label: "Writing",
		caption: "Shape clear deliverables",
		icon: PenLine,
		guidance:
			"Draft for the requested audience and purpose, separate verified facts from proposed wording, and return the actual draft.",
	},
	{
		key: "custom",
		label: "Custom",
		caption: "Define your own role",
		icon: Wrench,
		guidance:
			"Stay within the stated purpose and ask for clarification when the assignment falls outside it.",
	},
];

export function suggestedSpecialistInstructions(
	focus: SpecialistFocus,
	purpose: string,
): string {
	const selected = specialistFocuses.find((item) => item.key === focus);
	return [
		`Your responsibility: ${purpose.trim()}`,
		selected?.guidance ?? "",
		"Work only within this responsibility. Use member-scoped tools when relevant; never assume a connected app, document, or permission is available without checking.",
		"Return the requested deliverable with sources, actions actually completed, and any blockers. Do not claim a tool action succeeded without its result.",
		"If another specialist or teammate is needed, propose a bounded handoff. A handoff is not completed work. Stop and ask the member when the task needs a decision or confirmation you cannot provide.",
	]
		.filter(Boolean)
		.join("\n\n");
}

function avatarTone(seed: string): number {
	let value = 0;
	for (const character of seed) {
		value = (value * 31 + (character.codePointAt(0) ?? 0)) >>> 0;
	}
	return value % 6;
}

export function SpecialistAvatar({
	name,
	seed,
	size = "default",
}: {
	name: string;
	seed: string;
	size?: "default" | "small" | "large";
}) {
	const initials =
		name
			.trim()
			.split(/\s+/)
			.slice(0, 2)
			.map((part) => part[0]?.toLocaleUpperCase() ?? "")
			.join("") || "✦";
	return (
		<span
			aria-label={`${name || "New specialist"} avatar`}
			className="specialist-avatar"
			data-size={size}
			data-tone={avatarTone(seed)}
			role="img"
		>
			<span aria-hidden="true" className="specialist-avatar-orbit" />
			<span aria-hidden="true" className="specialist-avatar-initials">
				{initials}
			</span>
		</span>
	);
}
