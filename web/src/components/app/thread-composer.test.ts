// @vitest-environment jsdom

import { fireEvent, render } from "@testing-library/react";
import { createElement, useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { ThreadComposer } from "./thread-composer";

describe("ThreadComposer", () => {
	it("submits trimmed input with Enter and keeps Shift+Enter for a new line", () => {
		const onSubmit = vi.fn();

		function Harness() {
			const [value, setValue] = useState("");
			return createElement(ThreadComposer, {
				onChange: setValue,
				onSubmit,
				value,
			});
		}

		const view = render(createElement(Harness));
		const input = view.getByRole("textbox", {
			name: "Message The Weirdly ODD",
		});

		fireEvent.change(input, { target: { value: "  Plan the launch  " } });
		fireEvent.keyDown(input, { key: "Enter", shiftKey: true });
		expect(onSubmit).not.toHaveBeenCalled();

		fireEvent.keyDown(input, { key: "Enter" });
		expect(onSubmit).toHaveBeenCalledWith("Plan the launch");
	});

	it("does not render controls that have no action", () => {
		const view = render(
			createElement(ThreadComposer, {
				onChange: vi.fn(),
				value: "",
			}),
		);

		expect(view.queryByRole("button", { name: "Add files" })).toBeNull();
		expect(view.queryByRole("button", { name: "Choose tools" })).toBeNull();
	});
});
