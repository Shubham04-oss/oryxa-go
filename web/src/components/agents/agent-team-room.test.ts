import { describe, expect, it } from "vitest";
import type { AgentTeam } from "@/lib/agents/client";
import { teamSeats } from "./agent-team-room";

describe("team workroom handles", () => {
	it("includes the coordinator and gives specialists stable distinct mentions", () => {
		const team = {
			coordinator_agent_id: "primary",
			coordinator_name: "Nia",
			members: [
				{
					agent_id: "agt_research_1",
					name: "Research Guide",
					role: "Verify claims",
					status: "active",
				},
				{
					agent_id: "agt_research_2",
					name: "Research Guide",
					role: "Review sources",
					status: "disabled",
				},
			],
		} as AgentTeam;
		expect(teamSeats(team)).toMatchObject([
			{ handle: "primary", active: true },
			{ handle: "research-guide", active: true },
			{ handle: "agtresearch2", active: false },
		]);
	});
});
