import { queryOptions } from "@tanstack/react-query"
import { apiRequest, apiUrl } from "@/lib/api/client"

export type FileScope = "member" | "organization" | "team"

/** Every scope files accepts, in the order a picker should offer them. */
export const scopes: readonly FileScope[] = ["member", "team", "organization"]

export type StoredFile = {
  id: string
  organization_id: string
  scope: FileScope
  team_id?: string
  member_id?: string
  filename: string
  content_type: string
  purpose: string
  size_bytes: number
  sha256: string
  created_by: string
  created_at: string
  expires_at: string
}

export type FileFilter = {
  scope?: FileScope
  teamId?: string
}

export type UploadInput = {
  file: File
  retentionDays: number
  scope: FileScope
  teamId?: string
}

/** The largest upload files accepts (`domain.MaxBytes`, 8 MiB). */
export const maxUploadBytes = 8 * 1024 * 1024

/**
 * The full range files accepts for `retention_days` (`domain.MinRetentionDays`
 * to `domain.MaxRetentionDays`). `0`, or leaving the field out, asks for the
 * purpose's own default (30 days for a plain upload).
 */
export const minRetentionDays = 1
export const maxRetentionDays = 90
export const defaultRetentionDays = 30

/** The choices a retention picker offers; any value in the min/max range works. */
export const retentionChoices = [7, 30, 90] as const

/**
 * The content types files stores as themselves (`domain.ContentType`'s
 * allowlist); everything else is refused with 400, so `uploadContentType`
 * below falls back to opaque bytes rather than sending one that would fail.
 */
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
])

/**
 * The content type to store `file` under. Files accepts a closed list whose
 * catch-all is opaque bytes, so anything else — including a blank type,
 * which browsers hand back for files with no recognized extension — is
 * stored as those rather than rejected.
 */
export function uploadContentType(file: Pick<File, "type">): string {
  const type = file.type.split(";")[0]?.trim().toLowerCase() ?? ""
  return storedContentTypes.has(type) ? type : "application/octet-stream"
}

/**
 * Why files would refuse `file`, checked before spending an upload on it.
 * Mirrors `domain.Size` and `domain.Filename`; the service checks again, so
 * this only saves a round trip and gives the reason sooner.
 */
export function uploadProblem(
  file: Pick<File, "name" | "size">
): string | undefined {
  if (file.size <= 0) {
    return "The file is empty."
  }
  if (file.size > maxUploadBytes) {
    return `Files can be up to ${formatBytes(maxUploadBytes)}.`
  }
  if (file.name.length > 255) {
    return "The file name is too long."
  }
  if (/[/\\:]/.test(file.name)) {
    return "The file name cannot contain / \\ or :."
  }
  return undefined
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) {
    return `${bytes} B`
  }
  const units = ["KB", "MB", "GB"]
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  const rounded = value >= 10 ? Math.round(value) : Math.round(value * 10) / 10
  return `${rounded} ${units[unit]}`
}

export type FileViewer = {
  isOrgAdmin: boolean
  manageableTeamIds: ReadonlySet<string>
  userId: string
}

/**
 * Whether files lets `viewer` delete `file`; the service checks again
 * (`Service.canDelete` in `files/internal/application/service.go`), in this
 * order: an organization admin may delete anything; a member scope's own
 * file may be deleted by whoever it names as its member or its uploader (the
 * two are the same today, since a member file is always placed under its own
 * uploader, but the service checks both, so this does too); a team scope's
 * file may be deleted by its uploader or anyone who manages the team;
 * an organization-scope file may only be deleted by an admin.
 */
export function canDeleteFile(file: StoredFile, viewer: FileViewer): boolean {
  if (viewer.isOrgAdmin) {
    return true
  }
  switch (file.scope) {
    case "member":
      return (
        file.member_id === viewer.userId || file.created_by === viewer.userId
      )
    case "team":
      return (
        file.created_by === viewer.userId ||
        (file.team_id !== undefined &&
          viewer.manageableTeamIds.has(file.team_id))
      )
    default:
      return false
  }
}

function filesPath(organizationId: string): string {
  return `/v1/organizations/${encodeURIComponent(organizationId)}/files`
}

function filePath(organizationId: string, fileId: string): string {
  return `${filesPath(organizationId)}/${encodeURIComponent(fileId)}`
}

export async function listFiles(
  organizationId: string,
  filter: FileFilter = {}
): Promise<StoredFile[]> {
  const search = new URLSearchParams()
  if (filter.scope) {
    search.set("scope", filter.scope)
  }
  if (filter.teamId) {
    search.set("team_id", filter.teamId)
  }
  const query = search.toString()
  const { files } = await apiRequest<{ files: StoredFile[] }>(
    query ? `${filesPath(organizationId)}?${query}` : filesPath(organizationId)
  )
  return files
}

export function getFile(
  organizationId: string,
  fileId: string
): Promise<StoredFile> {
  return apiRequest<StoredFile>(filePath(organizationId, fileId))
}

/**
 * Uploads `input.file`, multipart, with exactly the fields
 * `files/internal/httpserver/server.go`'s `readMultipart` reads: `file`,
 * `filename`, `content_type`, `purpose`, `scope`, `retention_days`, and
 * `team_id` when the scope is `team`. `purpose` is always `upload` — the
 * only purpose a member ever creates directly; `pipeline.artifact` and
 * `billing.attachment` are written by other services.
 */
export function uploadFile(
  organizationId: string,
  input: UploadInput
): Promise<StoredFile> {
  const form = new FormData()
  form.set("file", input.file, input.file.name)
  form.set("filename", input.file.name)
  form.set("content_type", uploadContentType(input.file))
  form.set("purpose", "upload")
  form.set("scope", input.scope)
  form.set("retention_days", String(input.retentionDays))
  if (input.scope === "team" && input.teamId) {
    form.set("team_id", input.teamId)
  }
  return apiRequest<StoredFile>(filesPath(organizationId), { body: form })
}

export async function deleteFile(
  organizationId: string,
  fileId: string
): Promise<void> {
  await apiRequest(filePath(organizationId, fileId), { method: "DELETE" })
}

/** Where the browser downloads a file's bytes, through files on the session cookie. */
export function fileContentUrl(organizationId: string, fileId: string): string {
  return apiUrl(`${filePath(organizationId, fileId)}/content`)
}

/**
 * Gets `fileId` ready to download: the download itself is a plain
 * navigation to `fileContentUrl`, which rides the ambient session cookie and
 * cannot refresh an expired one, so a request that can (`getFile`, through
 * `apiRequest`) goes first, exactly as `web/`'s `app.files.tsx` did inline.
 * Returns the URL to navigate to once that check has passed.
 *
 * files also exposes `POST .../files/{id}/download-url`, a presigned link
 * straight to blob storage that needs no cookie at all — see this package's
 * README/the task report for why this function does not use it instead.
 */
export async function downloadFile(
  organizationId: string,
  fileId: string
): Promise<string> {
  await getFile(organizationId, fileId)
  return fileContentUrl(organizationId, fileId)
}

/** The prefix of everything cached from files about one organization. */
export function filesQueryKey(organizationId: string) {
  return ["files", organizationId] as const
}

export function filesQueryOptions(
  organizationId: string,
  filter: FileFilter = {}
) {
  return queryOptions({
    queryKey: [
      ...filesQueryKey(organizationId),
      filter.scope ?? "all",
      filter.teamId ?? "",
    ] as const,
    queryFn: () => listFiles(organizationId, filter),
  })
}
