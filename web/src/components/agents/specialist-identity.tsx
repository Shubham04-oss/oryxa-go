import Avatar from "boring-avatars";
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

/**
 * The colours an agent's face is drawn from: the green the product already
 * spends on a thing working, the amber it warns with, and three warm
 * neutrals either side of them. Nothing here is pale enough to wash an agent
 * out at 28px, and nothing is louder than the product's own palette.
 */
export const AVATAR_COLOURS = [
	"#2f3b33",
	"#3f7d52",
	"#9a6a1e",
	"#7d5b4f",
	"#b08968",
];

/**
 * An agent's face, generated from `seed` rather than stored as an asset.
 *
 * The seed is the agent's id, never its name, so a face survives renaming —
 * it is how you recognise a specialist across a roster without reading. The
 * shape stays a rounded square, which is what separates an agent from a
 * person (a circle) at a glance.
 */
export function SpecialistAvatar({
	name,
	seed,
	size = "default",
}: {
	name: string;
	seed: string;
	size?: "default" | "small" | "large";
}) {
	return (
		<span
			aria-label={`${name || "New specialist"} avatar`}
			className="specialist-avatar"
			data-size={size}
			role="img"
		>
			{/* CSS sizes the box; the svg fills it, so one face works at every size. */}
			<Avatar colors={AVATAR_COLOURS} name={seed} square variant="marble" />
		</span>
	);
}
