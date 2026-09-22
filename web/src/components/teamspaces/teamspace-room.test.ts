import { describe, expect, it } from "vitest";
import type { RoomMessage } from "@/lib/rooms/client";
import { mentionRoomSeat, mergeRoomMessages } from "./teamspace-room";

describe("Teamspace room conversation", () => {
	it("merges catch-up and live replies by sequence without duplicates", () => {
		const held = [
			{ id: "one", seq: 1, body: "Hello" },
			{ id: "two-old", seq: 2, body: "Working" },
		] as RoomMessage[];
		const arrived = [
			{ id: "three", seq: 3, body: "Done" },
			{ id: "two", seq: 2, body: "Updated" },
		] as RoomMessage[];

		expect(
			mergeRoomMessages(held, arrived).map((message) => message.id),
		).toEqual(["one", "two", "three"]);
	});

	it("adds one readable mention to an existing draft", () => {
		expect(mentionRoomSeat("Can you check", "research-specialist")).toBe(
			"Can you check @research-specialist ",
		);
		expect(
			mentionRoomSeat(
				"@research-specialist please check",
				"research-specialist",
			),
		).toBe("@research-specialist please check");
	});
});
