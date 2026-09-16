import { describe, expect, it } from "vitest";
import { daysUntil } from "./format";

describe("daysUntil", () => {
	const now = Date.parse("2026-09-15T12:00:00Z");

	it("counts whole days, rounding up", () => {
		expect(daysUntil("2026-09-16T12:00:00Z", now)).toBe(1);
		expect(daysUntil("2026-09-16T13:00:00Z", now)).toBe(2);
	});

	it("goes negative once the moment has passed", () => {
		expect(daysUntil("2026-09-14T11:00:00Z", now)).toBe(-1);
	});
});
