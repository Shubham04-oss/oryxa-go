import { afterEach, describe, expect, it, vi } from "vitest"
import {
  canDeleteFile,
  defaultRetentionDays,
  deleteFile,
  downloadFile,
  fileContentUrl,
  filesQueryOptions,
  formatBytes,
  getFile,
  listFiles,
  maxRetentionDays,
  maxUploadBytes,
  minRetentionDays,
  retentionChoices,
  scopes,
  uploadContentType,
  uploadFile,
  uploadProblem,
} from "./client"
import type { FileViewer, StoredFile } from "./client"

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

afterEach(() => vi.unstubAllGlobals())

function file(overrides: Partial<StoredFile> = {}): StoredFile {
  return {
    id: "file_1",
    organization_id: "org_1",
    scope: "member",
    filename: "notes.txt",
    content_type: "text/plain",
    purpose: "upload",
    size_bytes: 12,
    sha256: "",
    created_by: "user-1",
    created_at: "2026-09-15T00:00:00Z",
    expires_at: "2026-10-15T00:00:00Z",
    ...overrides,
  }
}

describe("constants verified against the Go handler", () => {
  it("caps uploads at 8 MiB (domain.MaxBytes)", () => {
    expect(maxUploadBytes).toBe(8 * 1024 * 1024)
  })

  it("bounds retention between 1 and 90 days, defaulting to 30", () => {
    expect(minRetentionDays).toBe(1)
    expect(maxRetentionDays).toBe(90)
    expect(defaultRetentionDays).toBe(30)
    for (const choice of retentionChoices) {
      expect(choice).toBeGreaterThanOrEqual(minRetentionDays)
      expect(choice).toBeLessThanOrEqual(maxRetentionDays)
    }
  })

  it("lists every scope files accepts", () => {
    expect(scopes).toEqual(["member", "team", "organization"])
  })
})

describe("uploadContentType", () => {
  it("keeps the content types files stores and sends the rest as bytes", () => {
    expect(uploadContentType({ type: "image/png" })).toBe("image/png")
    expect(uploadContentType({ type: "text/plain;charset=UTF-8" })).toBe(
      "text/plain"
    )
    expect(uploadContentType({ type: "application/vnd.ms-excel" })).toBe(
      "application/octet-stream"
    )
    expect(uploadContentType({ type: "" })).toBe("application/octet-stream")
  })
})

describe("uploadProblem", () => {
  it("refuses what files would refuse before sending it", () => {
    expect(
      uploadProblem({ name: "notes.txt", size: 8 * 1024 * 1024 })
    ).toBeUndefined()
    expect(uploadProblem({ name: "big.bin", size: 8 * 1024 * 1024 + 1 })).toBe(
      "Files can be up to 8 MB."
    )
    expect(uploadProblem({ name: "a".repeat(256), size: 1 })).toBe(
      "The file name is too long."
    )
    expect(uploadProblem({ name: "notes.txt", size: 0 })).toBe(
      "The file is empty."
    )
    expect(uploadProblem({ name: "a/b.txt", size: 1 })).toBe(
      "The file name cannot contain / \\ or :."
    )
  })
})

describe("formatBytes", () => {
  it("formats sizes for reading", () => {
    expect(formatBytes(512)).toBe("512 B")
    expect(formatBytes(1536)).toBe("1.5 KB")
    expect(formatBytes(8 * 1024 * 1024)).toBe("8 MB")
    expect(formatBytes(25 * 1024 * 1024)).toBe("25 MB")
  })
})

describe("canDeleteFile", () => {
  const viewer: FileViewer = {
    isOrgAdmin: false,
    manageableTeamIds: new Set(["team_1"]),
    userId: "user-1",
  }

  it("lets a member delete their own member-scope file", () => {
    expect(canDeleteFile(file({ member_id: "user-1" }), viewer)).toBe(true)
    expect(
      canDeleteFile(file({ created_by: "user-2", member_id: "user-2" }), viewer)
    ).toBe(false)
  })

  it("falls back to created_by when member_id names someone else, matching the Go OR", () => {
    // service.go's canDelete checks MemberID == caller OR CreatedBy ==
    // caller, not one field with the other as a fallback.
    expect(
      canDeleteFile(file({ created_by: "user-1", member_id: "user-2" }), viewer)
    ).toBe(true)
  })

  it("lets a team's uploader or its manager delete a team file", () => {
    expect(
      canDeleteFile(
        file({ created_by: "user-2", scope: "team", team_id: "team_1" }),
        viewer
      )
    ).toBe(true)
    expect(
      canDeleteFile(
        file({ created_by: "user-2", scope: "team", team_id: "team_2" }),
        viewer
      )
    ).toBe(false)
    expect(
      canDeleteFile(
        file({ created_by: "user-1", scope: "team", team_id: "team_9" }),
        viewer
      )
    ).toBe(true)
  })

  it("never lets a plain member delete an organization-scope file", () => {
    expect(canDeleteFile(file({ scope: "organization" }), viewer)).toBe(false)
  })

  it("lets an organization admin delete any file", () => {
    expect(
      canDeleteFile(file({ created_by: "user-2", scope: "organization" }), {
        ...viewer,
        isOrgAdmin: true,
      })
    ).toBe(true)
  })
})

describe("files API contracts", () => {
  it("lists with the scope and team_id query, URL-encoding both the org and the filter", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ files: [] }))
    vi.stubGlobal("fetch", fetchMock)

    await expect(
      listFiles("org / one", { scope: "team", teamId: "team / one" })
    ).resolves.toEqual([])

    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org%20%2F%20one/files?scope=team&team_id=team+%2F+one",
      expect.objectContaining({ method: "GET", credentials: "include" })
    )
  })

  it("lists with no query string when the filter is empty", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ files: [] }))
    vi.stubGlobal("fetch", fetchMock)

    await listFiles("org_1")

    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/files",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("gets one file by id, URL-encoded", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(file()))
    vi.stubGlobal("fetch", fetchMock)

    await getFile("org_1", "file / one")

    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/files/file%20%2F%20one",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("uploads multipart with exactly the fields the Go handler reads", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) =>
      jsonResponse(file({ id: "file_new" }))
    )
    vi.stubGlobal("fetch", fetchMock)

    await uploadFile("org_1", {
      file: new File(["hello"], "notes.txt", { type: "text/plain" }),
      retentionDays: 30,
      scope: "team",
      teamId: "team_1",
    })

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe("/v1/organizations/org_1/files")
    expect(init.method).toBe("POST")
    expect(init.headers).toBeUndefined()
    const form = init.body as FormData
    expect(form).toBeInstanceOf(FormData)
    expect((form.get("file") as File).name).toBe("notes.txt")
    expect(form.get("filename")).toBe("notes.txt")
    expect(form.get("content_type")).toBe("text/plain")
    expect(form.get("purpose")).toBe("upload")
    expect(form.get("scope")).toBe("team")
    expect(form.get("retention_days")).toBe("30")
    expect(form.get("team_id")).toBe("team_1")
    expect([...form.keys()].sort()).toEqual(
      [
        "content_type",
        "file",
        "filename",
        "purpose",
        "retention_days",
        "scope",
        "team_id",
      ].sort()
    )
  })

  it("leaves team_id out of the upload for a member-scope file", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) =>
      jsonResponse(file())
    )
    vi.stubGlobal("fetch", fetchMock)

    await uploadFile("org_1", {
      file: new File(["hello"], "notes.txt", { type: "text/plain" }),
      retentionDays: 7,
      scope: "member",
    })

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    const form = init.body as FormData
    expect(form.has("team_id")).toBe(false)
  })

  it("deletes by id, URL-encoded", async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal("fetch", fetchMock)

    await deleteFile("org_1", "file / one")

    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/files/file%20%2F%20one",
      expect.objectContaining({ method: "DELETE" })
    )
  })

  it("builds the content URL without fetching it", () => {
    const fetchMock = vi.fn()
    vi.stubGlobal("fetch", fetchMock)
    expect(fileContentUrl("org_1", "file_1")).toBe(
      "/v1/organizations/org_1/files/file_1/content"
    )
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("downloadFile reads the file first, then returns the content URL", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(file({ id: "file_1" })))
    vi.stubGlobal("fetch", fetchMock)

    await expect(downloadFile("org_1", "file_1")).resolves.toBe(
      "/v1/organizations/org_1/files/file_1/content"
    )
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/files/file_1",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("downloadFile surfaces a failed metadata read instead of returning a URL", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ error: "resource not found" }, 404)
    )
    vi.stubGlobal("fetch", fetchMock)

    await expect(downloadFile("org_1", "gone")).rejects.toMatchObject({
      status: 404,
    })
  })
})

describe("filesQueryOptions", () => {
  it("keys on the organization and filter, and loads through listFiles", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ files: [file()] }))
    vi.stubGlobal("fetch", fetchMock)

    const options = filesQueryOptions("org_1", {
      scope: "team",
      teamId: "team_1",
    })
    expect(options.queryKey).toEqual(["files", "org_1", "team", "team_1"])
    await expect(options.queryFn?.({} as never)).resolves.toEqual([file()])
  })

  it("keys an empty filter as all files with no team", () => {
    const options = filesQueryOptions("org_1")
    expect(options.queryKey).toEqual(["files", "org_1", "all", ""])
  })
})
