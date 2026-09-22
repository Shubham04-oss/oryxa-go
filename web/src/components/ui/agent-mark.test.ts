import { describe, expect, it } from "vitest";
import { dotsOf, SHAPES, shapeFor } from "./agent-mark";

describe("an agent's mark", () => {
	it("gives the same agent the same form forever", () => {
		expect(shapeFor("agt_ledger")).toBe(shapeFor("agt_ledger"));
		// The id is what is hashed, so renaming the agent cannot move it.
		expect(shapeFor("agt_ledger")).not.toBe(shapeFor("agt_maya"));
	});

	it("spreads real ids across the forms rather than favouring one", () => {
		const used = new Set(
			Array.from({ length: 200 }, (_, i) => shapeFor(`agt_${i}xKq${i * 7}`)),
		);
		expect(used.size).toBe(SHAPES.length);
	});

	it("keeps every dot inside the field it is drawn in", () => {
		for (const shape of SHAPES) {
			const dots = dotsOf(shape);
			expect(dots.length).toBeGreaterThan(40);
			for (const dot of dots) {
				expect(dot.x).toBeGreaterThanOrEqual(0);
				expect(dot.x).toBeLessThanOrEqual(40);
				expect(dot.y).toBeGreaterThanOrEqual(0);
				expect(dot.y).toBeLessThanOrEqual(40);
				expect(dot.o).toBeGreaterThan(0);
			}
		}
	});

	it("draws each form once and hands back the same field", () => {
		expect(dotsOf("hexagon")).toBe(dotsOf("hexagon"));
	});
});
