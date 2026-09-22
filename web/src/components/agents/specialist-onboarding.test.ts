import { describe, expect, it } from "vitest";
import {
	compileSpecialistInstructions,
	inferSpecialistFocus,
	starterSpecialistDraft,
} from "./specialist-onboarding";

describe("specialist onboarding", () => {
	it("infers a useful focus from the name", () => {
		expect(inferSpecialistFocus("Revenue Analyst")).toBe("analysis");
		expect(inferSpecialistFocus("Researcher")).toBe("research");
		expect(inferSpecialistFocus("My custom role")).toBe("custom");
	});

	it("persists the proposed cycle and first conversation in the preamble", () => {
		const draft = starterSpecialistDraft({
			name: "Researcher",
			context: {
				member_name: "Shubham",
				organization_name: "Odd Works",
				knowledge_sources: [],
				connected_apps: [],
			},
		});
		const instructions = compileSpecialistInstructions(draft);
		expect(instructions).toContain("## Working cycle");
		expect(instructions).toContain("## First conversation");
		expect(instructions).toContain("not grants");
	});
});
