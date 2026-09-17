import { describe, expect, it } from "vitest";
import { daysUntil, expiryText } from "./format";

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

describe("expiryText", () => {
	it("says how long is left", () => {
		expect(expiryText(6)).toBe("Expires in 6 days");
		expect(expiryText(1)).toBe("Expires tomorrow");
		expect(expiryText(0)).toBe("Expires today");
		expect(expiryText(-1)).toBe("Expires today");
	});
});
