// @vitest-environment jsdom

import { act, render } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WordRotate } from "./word-rotate";

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

describe("WordRotate", () => {
	it("shows one word at a time and advances in order", () => {
		vi.stubGlobal(
			"matchMedia",
			vi.fn(() => ({
				addEventListener: vi.fn(),
				matches: false,
				removeEventListener: vi.fn(),
			})),
		);
		vi.useFakeTimers();
		const { container } = render(
			createElement(WordRotate, {
				duration: 100,
				words: ["Search knowledge", "Read document"],
			}),
		);
		const activeWord = () =>
			container.querySelector('[data-slot="word-rotate-active"]')?.textContent;

		expect(activeWord()).toBe("Search knowledge");
		act(() => vi.advanceTimersByTime(100));
		expect(activeWord()).toBe("Read document");
	});
});
