import { describe, expect, it } from "vitest";
import {
	canDeleteFile,
	formatBytes,
	type StoredFile,
	uploadContentType,
	uploadProblem,
} from "./client";

function file(overrides: Partial<StoredFile>): StoredFile {
	return {
		content_type: "text/plain",
		created_at: "2026-09-15T00:00:00Z",
		created_by: "user-1",
		expires_at: "2026-10-15T00:00:00Z",
		filename: "notes.txt",
		id: "file_1",
		organization_id: "org_1",
		purpose: "upload",
		scope: "member",
		sha256: "",
		size_bytes: 12,
		...overrides,
	};
}

describe("uploads", () => {
	it("keeps the content types files stores and sends the rest as bytes", () => {
		expect(uploadContentType({ type: "image/png" })).toBe("image/png");
		expect(uploadContentType({ type: "text/plain;charset=UTF-8" })).toBe(
			"text/plain",
		);
		expect(uploadContentType({ type: "application/vnd.ms-excel" })).toBe(
			"application/octet-stream",
		);
		expect(uploadContentType({ type: "" })).toBe("application/octet-stream");
	});

	it("refuses what files would refuse before sending it", () => {
		expect(
			uploadProblem({ name: "notes.txt", size: 8 * 1024 * 1024 }),
		).toBeUndefined();
		expect(uploadProblem({ name: "big.bin", size: 8 * 1024 * 1024 + 1 })).toBe(
			"Files can be up to 8 MB.",
		);
		expect(uploadProblem({ name: "a".repeat(256), size: 1 })).toBe(
			"The file name is too long.",
		);
	});

	it("formats sizes for reading", () => {
		expect(formatBytes(512)).toBe("512 B");
		expect(formatBytes(1536)).toBe("1.5 KB");
		expect(formatBytes(8 * 1024 * 1024)).toBe("8 MB");
		expect(formatBytes(25 * 1024 * 1024)).toBe("25 MB");
	});
});

describe("canDeleteFile", () => {
	const viewer = {
		isOrgAdmin: false,
		manageableTeamIds: new Set(["team_1"]),
		userId: "user-1",
	};

	it("lets people delete their own files and their teams' files", () => {
		expect(canDeleteFile(file({}), viewer)).toBe(true);
		expect(
			canDeleteFile(
				file({ created_by: "user-2", member_id: "user-2" }),
				viewer,
			),
		).toBe(false);
		expect(
			canDeleteFile(
				file({ created_by: "user-2", scope: "team", team_id: "team_1" }),
				viewer,
			),
		).toBe(true);
		expect(
			canDeleteFile(
				file({ created_by: "user-2", scope: "team", team_id: "team_2" }),
				viewer,
			),
		).toBe(false);
		expect(canDeleteFile(file({ scope: "organization" }), viewer)).toBe(false);
	});

	it("lets organization admins delete any file", () => {
		expect(
			canDeleteFile(file({ created_by: "user-2", scope: "organization" }), {
				...viewer,
				isOrgAdmin: true,
			}),
		).toBe(true);
	});
});
