import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
	SpecialistAvatar,
	suggestedSpecialistInstructions,
} from "./specialist-identity";

describe("specialist identity", () => {
	it("keeps an avatar stable for the same specialist ID", () => {
		const before = renderToStaticMarkup(
			<SpecialistAvatar name="Revenue Analyst" seed="specialist-42" />,
		);
		const renamed = renderToStaticMarkup(
			<SpecialistAvatar name="Growth Analyst" seed="specialist-42" />,
		);
		expect(before).toContain("RA");
		expect(renamed).toContain("GA");
		expect(before.match(/data-tone="(\d)"/)?.[1]).toBe(
			renamed.match(/data-tone="(\d)"/)?.[1],
		);
	});

	it("grounds starter guidance in the chosen focus and purpose", () => {
		const instructions = suggestedSpecialistInstructions(
			"research",
			"  Verify account claims  ",
		);
		expect(instructions).toContain(
			"Your responsibility: Verify account claims",
		);
		expect(instructions).toContain("distinguish evidence from inference");
		expect(instructions).toContain("never assume a connected app");
		expect(instructions).toContain("A handoff is not completed work");
	});
});
