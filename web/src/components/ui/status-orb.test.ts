import { describe, expect, it } from "vitest";
import { orbDots } from "./status-orb";

describe("orbDots", () => {
	const dots = orbDots();

	it("draws enough dots to read as texture, few enough to stay a shape", () => {
		expect(dots.length).toBeGreaterThan(12);
		expect(dots.length).toBeLessThan(40);
	});

	it("keeps every dot inside the 24-unit field", () => {
		for (const dot of dots) {
			expect(dot.x - dot.r).toBeGreaterThanOrEqual(0);
			expect(dot.y - dot.r).toBeGreaterThanOrEqual(0);
			expect(dot.x + dot.r).toBeLessThanOrEqual(24);
			expect(dot.y + dot.r).toBeLessThanOrEqual(24);
		}
	});

	it("gives every dot a distinct key", () => {
		expect(new Set(dots.map((dot) => dot.id)).size).toBe(dots.length);
	});

	it("lights the orb from one side, so it reads as a ball", () => {
		const opacities = dots.map((dot) => dot.o);
		expect(Math.min(...opacities)).toBeLessThan(Math.max(...opacities));
		// Every dot stays visible: the shading gives volume, not holes.
		expect(Math.min(...opacities)).toBeGreaterThan(0.3);
		const upperLeft = dots.find((dot) => dot.x < 12 && dot.y < 12);
		const lowerRight = dots.find((dot) => dot.x > 12 && dot.y > 12);
		expect(upperLeft?.o).toBeGreaterThan(lowerRight?.o ?? 1);
	});
});
