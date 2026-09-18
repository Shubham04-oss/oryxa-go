import { describe, expect, it } from "vitest";
import { daysUntil, expiryText, formatDate, relativeTime } from "./format";

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

describe("relativeTime", () => {
	const now = Date.parse("2026-09-15T12:00:00Z");

	it("says how recent a moment was, by the minute and by the hour", () => {
		expect(relativeTime("2026-09-15T11:59:31Z", now)).toBe("Just now");
		expect(relativeTime("2026-09-15T11:57:00Z", now)).toBe("3 min");
		expect(relativeTime("2026-09-15T09:00:00Z", now)).toBe("3 hr");
	});

	it("says yesterday for a moment a day or more back, under two", () => {
		expect(relativeTime("2026-09-14T06:00:00Z", now)).toBe("Yesterday");
	});

	it("names the weekday for the rest of the past week", () => {
		const value = "2026-09-10T12:00:00Z";
		expect(relativeTime(value, now)).toBe(
			new Intl.DateTimeFormat(undefined, { weekday: "long" }).format(
				new Date(value),
			),
		);
	});

	it("falls back to the date once it is a week old or more", () => {
		const value = "2026-09-01T12:00:00Z";
		expect(relativeTime(value, now)).toBe(formatDate(value));
	});
});
