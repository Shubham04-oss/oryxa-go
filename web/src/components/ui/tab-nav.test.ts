import { describe, expect, it } from "vitest";
import { nextTabIndex } from "./tab-nav";

describe("nextTabIndex", () => {
	it("walks forward and back", () => {
		expect(nextTabIndex(0, "ArrowRight", 4)).toBe(1);
		expect(nextTabIndex(2, "ArrowLeft", 4)).toBe(1);
	});

	it("treats the vertical arrows the same way", () => {
		expect(nextTabIndex(0, "ArrowDown", 4)).toBe(1);
		expect(nextTabIndex(1, "ArrowUp", 4)).toBe(0);
	});

	it("wraps at both ends", () => {
		expect(nextTabIndex(3, "ArrowRight", 4)).toBe(0);
		expect(nextTabIndex(0, "ArrowLeft", 4)).toBe(3);
	});

	it("jumps to the first and last", () => {
		expect(nextTabIndex(2, "Home", 4)).toBe(0);
		expect(nextTabIndex(1, "End", 4)).toBe(3);
	});

	it("stays put for a key that is not navigation", () => {
		expect(nextTabIndex(2, "a", 4)).toBe(2);
		expect(nextTabIndex(2, "Enter", 4)).toBe(2);
		expect(nextTabIndex(2, " ", 4)).toBe(2);
	});

	it("survives an empty row", () => {
		expect(nextTabIndex(0, "ArrowRight", 0)).toBe(0);
		expect(nextTabIndex(-1, "Home", 0)).toBe(-1);
	});
});
