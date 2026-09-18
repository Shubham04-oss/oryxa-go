import { afterEach, describe, expect, it, vi } from "vitest";
import {
	type Corpus,
	externalSourceLink,
	getCorpus,
	isFromConnectedApps,
	listCorpora,
	listDocuments,
	scopeLabel,
} from "./client";

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
		status,
	});
}

afterEach(() => {
	vi.unstubAllGlobals();
});

function corpus(overrides: Partial<Corpus> = {}): Corpus {
	return {
		chunk_count: 0,
		created_at: "2026-09-15T00:00:00Z",
		document_count: 0,
		id: "corpus_1",
		name: "My notes",
		organization_id: "org_1",
		scope: "member",
		slug: "my-notes",
		updated_at: "2026-09-15T00:00:00Z",
		...overrides,
	};
}

describe("listCorpora", () => {
	it("gets the organization's collections", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValue(json(200, { corpora: [corpus()] }));
		vi.stubGlobal("fetch", fetchMock);

		await expect(listCorpora("org_1")).resolves.toEqual([corpus()]);
		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org_1/corpora",
			expect.objectContaining({ method: "GET" }),
		);
	});
});

describe("getCorpus", () => {
	it("gets one collection by id", async () => {
		const fetchMock = vi.fn().mockResolvedValue(json(200, corpus()));
		vi.stubGlobal("fetch", fetchMock);

		await getCorpus("org_1", "corpus_1");
		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org_1/corpora/corpus_1",
			expect.objectContaining({ method: "GET" }),
		);
	});
});

describe("listDocuments", () => {
	it("lists with no query string when given no cursor or limit", async () => {
		const fetchMock = vi.fn().mockResolvedValue(json(200, { documents: [] }));
		vi.stubGlobal("fetch", fetchMock);

		await listDocuments("org_1", "corpus_1");
		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org_1/corpora/corpus_1/documents",
			expect.objectContaining({ method: "GET" }),
		);
	});

	it("passes the cursor and limit through as query parameters", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValue(json(200, { documents: [], next_cursor: "next" }));
		vi.stubGlobal("fetch", fetchMock);

		await listDocuments("org_1", "corpus_1", { cursor: "abc", limit: 50 });
		expect(fetchMock).toHaveBeenCalledWith(
			"/v1/organizations/org_1/corpora/corpus_1/documents?cursor=abc&limit=50",
			expect.objectContaining({ method: "GET" }),
		);
	});
});

describe("scopeLabel", () => {
	it("labels every scope in the member's own words", () => {
		expect(scopeLabel("member")).toBe("Just you");
		expect(scopeLabel("team")).toBe("Team");
		expect(scopeLabel("organization")).toBe("Organization");
	});
});

describe("isFromConnectedApps", () => {
	it("recognizes a collection knowledge built from connected apps", () => {
		expect(isFromConnectedApps({ metadata: { origin: "knowledge" } })).toBe(
			true,
		);
	});

	it("is false for a collection with unrelated or no metadata", () => {
		expect(isFromConnectedApps({ metadata: { origin: "manual" } })).toBe(false);
		expect(isFromConnectedApps({})).toBe(false);
	});
});

describe("externalSourceLink", () => {
	it("shows an external http(s) source as a link", () => {
		expect(
			externalSourceLink({ kind: "external", ref: "https://example.com/doc" }),
		).toBe("https://example.com/doc");
	});

	it("hides a non-external source", () => {
		expect(externalSourceLink({ kind: "file", ref: "file_1" })).toBeUndefined();
		expect(externalSourceLink({ kind: "inline" })).toBeUndefined();
	});

	it("hides an external source with no ref or a non-http(s) scheme", () => {
		expect(externalSourceLink({ kind: "external" })).toBeUndefined();
		expect(
			externalSourceLink({ kind: "external", ref: "javascript:alert(1)" }),
		).toBeUndefined();
		expect(
			externalSourceLink({ kind: "external", ref: "not a url" }),
		).toBeUndefined();
	});
});
