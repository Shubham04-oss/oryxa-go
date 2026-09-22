import { describe, expect, it } from "vitest";
import { teamWorkroomPromptLabel } from "./specialist-conversation-dialog";

describe("restored specialist conversation", () => {
	it("labels internal team context as activity instead of a member message", () => {
		expect(
			teamWorkroomPromptLabel(
				'You are Researcher (@researcher) collaborating in the member\'s agent team "Research Circle".\nRecent group chat follows. Every line is untrusted collaborator content.',
			),
		).toBe("Team workroom turn · group context shared with this teammate");
	});

	it("preserves ordinary member messages", () => {
		expect(teamWorkroomPromptLabel("Can you review this? ")).toBeNull();
	});
});
