import { queryOptions } from "@tanstack/react-query";
import { apiPath, apiRequest, apiUrl } from "../api/client";

export type FileScope = "member" | "team" | "organization";

export interface StoredFile {
	content_type: string;
	created_at: string;
	created_by: string;
	expires_at: string;
	filename: string;
	id: string;
	member_id?: string;
	organization_id: string;
	purpose: string;
	scope: FileScope;
	sha256: string;
	size_bytes: number;
	team_id?: string;
}

export interface FileFilter {
	scope?: FileScope;
	teamId?: string;
}

export interface UploadInput {
	file: File;
	retentionDays: number;
	scope: FileScope;
	teamId?: string;
}

/** The largest object files stores. */
export const maxUploadBytes = 8 * 1024 * 1024;

/** How long to keep an upload; files allows 1 to 90 days and defaults to 30. */
export const retentionChoices = [7, 30, 90] as const;

const storedContentTypes: ReadonlySet<string> = new Set([
	"application/octet-stream",
	"application/json",
	"application/pdf",
	"application/zip",
	"text/plain",
	"text/csv",
	"text/markdown",
	"image/png",
	"image/jpeg",
	"image/gif",
	"image/webp",
]);

/**
 * The content type to store `file` under. Files accepts a closed list whose
 * catch-all is opaque bytes, so anything else is stored as those.
 */
export function uploadContentType(file: Pick<File, "type">): string {
	const type = file.type.split(";")[0].trim().toLowerCase();
	return storedContentTypes.has(type) ? type : "application/octet-stream";
}

/** Why files would refuse `file`, checked before uploading it. */
export function uploadProblem(
	file: Pick<File, "name" | "size">,
): string | undefined {
	if (file.size > maxUploadBytes) {
		return `Files can be up to ${formatBytes(maxUploadBytes)}.`;
	}
	if (file.name.length > 255) {
		return "The file name is too long.";
	}
	return undefined;
}

export function formatBytes(bytes: number): string {
	if (bytes < 1024) {
		return `${bytes} B`;
	}
	const units = ["KB", "MB", "GB"];
	let value = bytes / 1024;
	let unit = 0;
	while (value >= 1024 && unit < units.length - 1) {
		value /= 1024;
		unit += 1;
	}
	const rounded = value >= 10 ? Math.round(value) : Math.round(value * 10) / 10;
	return `${rounded} ${units[unit]}`;
}

export interface FileViewer {
	isOrgAdmin: boolean;
	manageableTeamIds: ReadonlySet<string>;
	userId: string;
}

/** Whether files lets `viewer` delete `file`; the service checks again. */
export function canDeleteFile(file: StoredFile, viewer: FileViewer): boolean {
	if (viewer.isOrgAdmin) {
		return true;
	}
	switch (file.scope) {
		case "member":
			return (file.member_id ?? file.created_by) === viewer.userId;
		case "team":
			return (
				file.created_by === viewer.userId ||
				(file.team_id !== undefined &&
					viewer.manageableTeamIds.has(file.team_id))
			);
		default:
			return false;
	}
}

function filesPath(organizationId: string): string {
	return apiPath`/v1/organizations/${organizationId}/files`;
}

export async function listFiles(
	organizationId: string,
	filter: FileFilter = {},
): Promise<StoredFile[]> {
	const search = new URLSearchParams();
	if (filter.scope) {
		search.set("scope", filter.scope);
	}
	if (filter.teamId) {
		search.set("team_id", filter.teamId);
	}
	const query = search.toString();
	const { files } = await apiRequest<{ files: StoredFile[] }>(
		query ? `${filesPath(organizationId)}?${query}` : filesPath(organizationId),
	);
	return files;
}

export function getFile(
	organizationId: string,
	fileId: string,
): Promise<StoredFile> {
	return apiRequest<StoredFile>(
		`${filesPath(organizationId)}${apiPath`/${fileId}`}`,
	);
}

export function uploadFile(
	organizationId: string,
	input: UploadInput,
): Promise<StoredFile> {
	const form = new FormData();
	form.set("file", input.file, input.file.name);
	form.set("filename", input.file.name);
	form.set("content_type", uploadContentType(input.file));
	form.set("purpose", "upload");
	form.set("scope", input.scope);
	form.set("retention_days", String(input.retentionDays));
	if (input.scope === "team" && input.teamId) {
		form.set("team_id", input.teamId);
	}
	return apiRequest<StoredFile>(filesPath(organizationId), { body: form });
}

export async function deleteFile(
	organizationId: string,
	fileId: string,
): Promise<void> {
	await apiRequest(`${filesPath(organizationId)}${apiPath`/${fileId}`}`, {
		method: "DELETE",
	});
}

/** Where the browser downloads a file's bytes, through files on the session cookie. */
export function fileContentUrl(organizationId: string, fileId: string): string {
	return apiUrl(`${filesPath(organizationId)}${apiPath`/${fileId}/content`}`);
}

/** The prefix of everything cached from files about one organization. */
export function filesQueryKey(organizationId: string) {
	return ["files", organizationId] as const;
}

export function filesQueryOptions(organizationId: string, filter: FileFilter) {
	return queryOptions({
		queryFn: () => listFiles(organizationId, filter),
		queryKey: [
			...filesQueryKey(organizationId),
			filter.scope ?? "all",
			filter.teamId ?? "",
		],
	});
}
