import { describe, expect, it } from "vitest";
import {
	availableSourceProviders,
	defaultSourceIntervalSeconds,
	effectiveKinds,
	intervalLabel,
	kindsSummary,
	runStatusLabel,
	runSummary,
	sourceStatusLabel,
	sourceStatusTone,
	sourceTargetField,
	sourceTargetLabel,
	sourceTargetPayload,
	sourceTargetProblem,
	supportsSourceProvider,
} from "./client";

describe("availableSourceProviders / supportsSourceProvider", () => {
	it("keeps only the connectable providers this build actually mounts", () => {
		expect(availableSourceProviders(["github", "slack", "files"])).toEqual([
			"github",
			"slack",
		]);
		expect(availableSourceProviders([])).toEqual([]);
	});

	it("never offers files, even if the server listed it", () => {
		expect(supportsSourceProvider(["files"], "files")).toBe(false);
	});

	it("requires both a known provider and it being mounted", () => {
		expect(supportsSourceProvider(["github"], "github")).toBe(true);
		expect(supportsSourceProvider(["slack"], "github")).toBe(false);
		expect(supportsSourceProvider(["github"], "jira")).toBe(false);
	});

	it("offers google drive and gmail once knowledge mounts them", () => {
		expect(
			availableSourceProviders(["gmail", "googledrive", "github"]),
		).toEqual(["github", "googledrive", "gmail"]);
		expect(supportsSourceProvider(["googledrive"], "googledrive")).toBe(true);
		expect(supportsSourceProvider(["gmail"], "gmail")).toBe(true);
		expect(supportsSourceProvider(["github"], "gmail")).toBe(false);
	});
});

describe("sourceTargetField", () => {
	it("labels each provider's target from what knowledge's README documents", () => {
		expect(sourceTargetField("github")).toMatchObject({
			label: "Repository",
			required: true,
		});
		expect(sourceTargetField("notion")).toMatchObject({ required: false });
		expect(sourceTargetField("slack")).toMatchObject({
			label: "Channel ID",
			required: true,
		});
		expect(sourceTargetField("googledrive")).toMatchObject({
			required: false,
		});
		expect(sourceTargetField("gmail")).toMatchObject({
			label: "Label",
			required: false,
		});
	});
});

describe("sourceTargetPayload", () => {
	it("builds a github target from owner/name", () => {
		expect(sourceTargetPayload("github", " acme/api ")).toEqual({
			repository: "acme/api",
		});
	});

	it("builds a slack target from a channel id", () => {
		expect(sourceTargetPayload("slack", "C0123456789")).toEqual({
			channel: "C0123456789",
		});
	});

	it("leaves notion's target empty for every page, or names a database", () => {
		expect(sourceTargetPayload("notion", "")).toEqual({});
		expect(sourceTargetPayload("notion", "  ")).toEqual({});
		expect(sourceTargetPayload("notion", "db-123")).toEqual({
			database: "db-123",
		});
	});

	it("leaves google drive's target empty for the whole drive, or names a folder", () => {
		expect(sourceTargetPayload("googledrive", " ")).toEqual({});
		expect(sourceTargetPayload("googledrive", " 1AbC ")).toEqual({
			folder: "1AbC",
		});
	});

	it("leaves gmail's target empty for the inbox, or names a label", () => {
		expect(sourceTargetPayload("gmail", "")).toEqual({});
		expect(sourceTargetPayload("gmail", "SENT")).toEqual({ label: "SENT" });
	});
});

describe("sourceTargetProblem", () => {
	it("requires owner/name for github", () => {
		expect(sourceTargetProblem("github", "")).toBeDefined();
		expect(sourceTargetProblem("github", "acme")).toBeDefined();
		expect(sourceTargetProblem("github", "acme/api")).toBeUndefined();
	});

	it("requires a channel id with no spaces for slack", () => {
		expect(sourceTargetProblem("slack", "")).toBeDefined();
		expect(sourceTargetProblem("slack", "general channel")).toBeDefined();
		expect(sourceTargetProblem("slack", "C0123456789")).toBeUndefined();
	});

	it("never refuses notion's target: it is optional", () => {
		expect(sourceTargetProblem("notion", "")).toBeUndefined();
		expect(sourceTargetProblem("notion", "db-123")).toBeUndefined();
	});

	it("takes google drive's and gmail's targets blank, or as an id", () => {
		expect(sourceTargetProblem("googledrive", "")).toBeUndefined();
		expect(sourceTargetProblem("googledrive", "1AbC")).toBeUndefined();
		expect(sourceTargetProblem("googledrive", "Team folder")).toBeDefined();
		expect(sourceTargetProblem("gmail", "")).toBeUndefined();
		expect(sourceTargetProblem("gmail", "Label_12")).toBeUndefined();
		expect(sourceTargetProblem("gmail", "My receipts")).toBeDefined();
	});
});

describe("sourceTargetLabel", () => {
	it("reads github's repository", () => {
		expect(sourceTargetLabel("github", { repository: "acme/api" })).toBe(
			"acme/api",
		);
	});

	it("reads slack's channel with a leading hash", () => {
		expect(sourceTargetLabel("slack", { channel: "C0123456789" })).toBe(
			"#C0123456789",
		);
	});

	it("reads notion's database, or names every page when there is none", () => {
		expect(sourceTargetLabel("notion", { database: "db-123" })).toBe(
			"Database db-123",
		);
		expect(sourceTargetLabel("notion", {})).toBe("All pages");
	});

	it("reads google drive's folder, or names the whole drive", () => {
		expect(sourceTargetLabel("googledrive", {})).toBe("My Drive");
		expect(sourceTargetLabel("googledrive", { folder: "1AbC" })).toBe(
			"Folder 1AbC",
		);
	});

	it("reads gmail's label, the inbox when there is none", () => {
		expect(sourceTargetLabel("gmail", {})).toBe("Inbox");
		expect(sourceTargetLabel("gmail", { label: "SENT" })).toBe("Sent");
		expect(sourceTargetLabel("gmail", { label: "Label_12" })).toBe(
			"Label Label_12",
		);
	});
});

describe("intervalLabel", () => {
	it("names the presets", () => {
		expect(intervalLabel(defaultSourceIntervalSeconds)).toBe("Every hour");
		expect(intervalLabel(300)).toBe("Every 5 minutes");
		expect(intervalLabel(604_800)).toBe("Every week");
	});

	it("describes an interval outside the presets", () => {
		expect(intervalLabel(90)).toBe("Every 90 seconds");
		expect(intervalLabel(120)).toBe("Every 2 minutes");
		expect(intervalLabel(7200)).toBe("Every 2 hours");
		expect(intervalLabel(172_800)).toBe("Every 2 days");
	});
});

describe("status labels", () => {
	it("names a source's status", () => {
		expect(sourceStatusLabel("active")).toBe("Active");
		expect(sourceStatusLabel("paused")).toBe("Needs reconnecting");
		expect(sourceStatusTone("paused")).toBe("warning");
	});

	it("names a run's status", () => {
		expect(runStatusLabel("running")).toBe("Running");
		expect(runStatusLabel("completed")).toBe("Completed");
		expect(runStatusLabel("failed")).toBe("Failed");
	});
});

describe("runSummary", () => {
	it("names what a run skipped as noise, when it skipped any", () => {
		expect(
			runSummary({
				documents_excluded: 212,
				documents_published: 40,
				documents_removed: 3,
				documents_seen: 255,
			}),
		).toBe("40 published · 212 skipped as noise · 3 removed of 255 seen");
	});

	it("reads as before for a run that skipped nothing, or predates skipping", () => {
		const run = {
			documents_published: 2,
			documents_removed: 0,
			documents_seen: 2,
		};
		expect(runSummary(run)).toBe("2 published · 0 removed of 2 seen");
		expect(runSummary({ ...run, documents_excluded: 0 })).toBe(
			"2 published · 0 removed of 2 seen",
		);
	});
});

describe("effectiveKinds / kindsSummary", () => {
	const kinds = [
		{ default: true, hint: "", label: "Conversations", name: "conversation" },
		{ default: true, hint: "", label: "Support cases", name: "support_case" },
		{ default: false, hint: "", label: "Event updates", name: "event_update" },
		{ default: false, hint: "", label: "Promotions", name: "promotion" },
	];

	it("is the default kinds until a member chooses", () => {
		expect(effectiveKinds(kinds, undefined)).toEqual([
			"conversation",
			"support_case",
		]);
		expect(kindsSummary(kinds, undefined)).toBe("Default kinds");
	});

	it("is the member's choice once they make one", () => {
		expect(effectiveKinds(kinds, ["conversation", "event_update"])).toEqual([
			"conversation",
			"event_update",
		]);
	});

	it("names what a choice adds to and takes from the default", () => {
		expect(kindsSummary(kinds, ["conversation", "event_update"])).toBe(
			"+ Event updates, − Support cases",
		);
		expect(kindsSummary(kinds, ["conversation", "support_case"])).toBe(
			"Default kinds",
		);
	});
});
