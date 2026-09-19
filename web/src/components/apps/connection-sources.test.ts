// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { createElement } from "react";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { type Source, setSourceKinds } from "@/lib/knowledge/client";
import { KindsDialog } from "./connection-sources";

const kinds = [
	{
		default: true,
		hint: "Mail with people",
		label: "Conversations",
		name: "conversation",
	},
	{
		default: true,
		hint: "Tickets you opened",
		label: "Support cases",
		name: "support_case",
	},
	{
		default: false,
		hint: "Registrations and venues",
		label: "Event updates",
		name: "event_update",
	},
];

vi.mock("@/lib/knowledge/client", async (importActual) => ({
	...(await importActual<typeof import("@/lib/knowledge/client")>()),
	kindsQueryOptions: () => ({
		queryFn: async () => kinds,
		queryKey: ["knowledge", "kinds"],
		staleTime: Number.POSITIVE_INFINITY,
	}),
	setSourceKinds: vi.fn(async () => ({})),
}));

beforeAll(() => {
	// Radix sizes its checkboxes with a ResizeObserver, which jsdom lacks.
	vi.stubGlobal(
		"ResizeObserver",
		class {
			disconnect() {}
			observe() {}
			unobserve() {}
		},
	);
});

afterEach(() => {
	cleanup();
	vi.mocked(setSourceKinds).mockClear();
});

const source = { id: "src_1", provider: "gmail" } as Source;

function open(onSaved = vi.fn()) {
	render(
		createElement(
			QueryClientProvider,
			{ client: new QueryClient() },
			createElement(KindsDialog, {
				onSaved,
				organizationId: "org_1",
				source,
				trigger: createElement("button", { type: "button" }, "Kinds"),
			}),
		),
	);
	fireEvent.click(screen.getByRole("button", { name: "Kinds" }));
	return onSaved;
}

describe("KindsDialog", () => {
	it("starts from the default kinds and saves a choice in the order kinds are shown", async () => {
		const onSaved = open();
		const updates = await screen.findByRole("checkbox", {
			name: /Event updates/,
		});
		expect(
			screen
				.getByRole("checkbox", { name: /Conversations/ })
				.getAttribute("aria-checked"),
		).toBe("true");
		expect(updates.getAttribute("aria-checked")).toBe("false");

		fireEvent.click(updates);
		fireEvent.click(screen.getByRole("checkbox", { name: /Support cases/ }));
		fireEvent.click(screen.getByRole("button", { name: "Save" }));

		await waitFor(() =>
			expect(setSourceKinds).toHaveBeenCalledWith("org_1", "src_1", [
				"conversation",
				"event_update",
			]),
		);
		await waitFor(() => expect(onSaved).toHaveBeenCalled());
	});

	it("returns the source to the default kinds", async () => {
		open();
		fireEvent.click(
			await screen.findByRole("button", { name: "Use defaults" }),
		);
		await waitFor(() =>
			expect(setSourceKinds).toHaveBeenCalledWith("org_1", "src_1", null),
		);
	});

	it("will not save a choice of nothing", async () => {
		open();
		for (const name of [/Conversations/, /Support cases/]) {
			fireEvent.click(await screen.findByRole("checkbox", { name }));
		}
		expect(
			screen.getByRole("button", { name: "Save" }).hasAttribute("disabled"),
		).toBe(true);
		expect(screen.getByText(/Choose at least one kind/)).toBeTruthy();
	});
});
