import type {
	SpecialistDraft,
	SpecialistDraftInput,
} from "@/lib/agents/client";
import {
	type SpecialistFocus,
	suggestedSpecialistInstructions,
} from "./specialist-identity";

export function inferSpecialistFocus(name: string): SpecialistFocus {
	const normalized = name.toLowerCase();
	if (/research|investigat|intelligen|fact.?check/.test(normalized))
		return "research";
	if (/analy|finance|revenue|data|metric/.test(normalized)) return "analysis";
	if (/writ|edit|content|copy|communicat/.test(normalized)) return "writing";
	if (/operat|project|coordinat|support|sales/.test(normalized))
		return "operations";
	return "custom";
}

/** A transparent local starter when the model cannot draft a proposal. */
export function starterSpecialistDraft(
	input: SpecialistDraftInput,
): SpecialistDraft {
	const focus = (input.focus ||
		inferSpecialistFocus(input.name)) as SpecialistFocus;
	const purpose =
		input.purpose?.trim() ||
		`Own ${input.name.trim().toLowerCase()} requests for ${input.context.member_name || "the member"} at ${input.context.organization_name || "their organization"}.`;
	return {
		purpose,
		instructions: suggestedSpecialistInstructions(focus, purpose),
		cycle: [
			{
				title: "Understand",
				detail: "Ask what outcome, deadline, and constraints matter.",
			},
			{
				title: "Ground",
				detail:
					"Check available knowledge and connected sources before acting.",
			},
			{
				title: "Deliver",
				detail: "Return the work, evidence, uncertainty, and next handoff.",
			},
		],
		integrations: [
			input.context.knowledge_sources.length
				? "Review the available indexed knowledge"
				: "Index a relevant knowledge base when ready",
			input.context.connected_apps.length
				? "Use an authorized connected app when the task needs it"
				: "Connect a relevant app when ready",
		],
		first_message: `What should I work on first as your ${input.name.trim()}?`,
	};
}

/** These are the actual persisted instructions, not merely a UI-only plan. */
export function compileSpecialistInstructions(plan: SpecialistDraft): string {
	return [
		plan.instructions.trim(),
		"## Working cycle",
		...plan.cycle.map(
			(step, index) =>
				`${index + 1}. ${step.title.trim()}: ${step.detail.trim()}`,
		),
		"## First conversation",
		`Start by asking: ${plan.first_message.trim()}`,
		"Integrations listed during onboarding are suggestions, not grants. Check availability and authorization at run time. Never claim an app or source was connected, read, or changed without a successful tool result.",
	]
		.filter(Boolean)
		.join("\n\n");
}
