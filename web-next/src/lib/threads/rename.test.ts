import { afterEach, describe, expect, it, vi } from "vitest"
import { QueryClient } from "@tanstack/react-query"
import { ApiError } from "@/lib/api/client"
import { renameSpecialistThread } from "@/lib/specialists/client"
import {
  MAX_TITLE_LENGTH,
  canRenameThread,
  normalizeThreadTitle,
  renameThread,
  threadKeys,
  threadTitleProblem,
  withRenamedThread,
} from "./client"
import type { Thread, ThreadDetail } from "./client"
import { renameCachedThread, renameFailureText } from "./rename"

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const thread = (id: string, title: string, updated_at: string): Thread => ({
  id,
  title,
  updated_at,
})

describe("a chat's title", () => {
  it("is trimmed and its whitespace collapsed, as Go stores it", () => {
    expect(normalizeThreadTitle("  Launch \n  plan\t ")).toBe("Launch plan")
  })

  it("holds 1 to 80 characters, counted as Go counts them", () => {
    expect(threadTitleProblem("   ")).toBe("Give the chat a name.")
    expect(threadTitleProblem("é".repeat(MAX_TITLE_LENGTH))).toBeUndefined()
    expect(threadTitleProblem("😀".repeat(MAX_TITLE_LENGTH))).toBeUndefined()
    expect(threadTitleProblem("a".repeat(MAX_TITLE_LENGTH + 1))).toBe(
      "Keep the name to 80 characters."
    )
  })

  it("can't be changed on the Requests thread", () => {
    expect(canRenameThread("requests-org_1")).toBe(false)
    expect(canRenameThread("th_1")).toBe(true)
  })
})

describe("rename requests", () => {
  it("PATCHes a thread's normalized title", async () => {
    const renamed = thread("th_1", "Launch plan", "2026-10-16T10:00:00Z")
    const fetchMock = vi.fn(async () => json(renamed))
    vi.stubGlobal("fetch", fetchMock)

    await expect(
      renameThread("org_1", "th_1", "  Launch   plan ")
    ).resolves.toEqual(renamed)
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/threads/th_1",
      expect.objectContaining({
        method: "PATCH",
        body: JSON.stringify({ title: "Launch plan" }),
      })
    )
  })

  it("PATCHes a specialist's conversation through its agent", async () => {
    const fetchMock = vi.fn(async () =>
      json(thread("th_9", "Vendor list", "2026-10-16T10:00:00Z"))
    )
    vi.stubGlobal("fetch", fetchMock)

    await renameSpecialistThread("org_1", "ag_1", "th_9", "Vendor list")
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/organizations/org_1/agents/ag_1/threads/th_9",
      expect.objectContaining({
        method: "PATCH",
        body: JSON.stringify({ title: "Vendor list" }),
      })
    )
  })
})

describe("withRenamedThread", () => {
  it("replaces the thread and re-sorts newest first", () => {
    const list = [
      thread("th_1", "One", "2026-10-16T09:00:00Z"),
      thread("th_2", "Two", "2026-10-15T09:00:00Z"),
    ]
    expect(
      withRenamedThread(list, {
        id: "th_2",
        title: "Renamed",
        updated_at: "2026-10-16T10:00:00Z",
      })?.map((item) => [item.id, item.title])
    ).toEqual([
      ["th_2", "Renamed"],
      ["th_1", "One"],
    ])
    expect(withRenamedThread(undefined, { id: "th_1" })).toBeUndefined()
  })
})

describe("renameCachedThread", () => {
  const listKey = threadKeys.list("org_1")
  const detailKey = threadKeys.detail("org_1", "th_2")

  function cache() {
    const client = new QueryClient({
      defaultOptions: { queries: { staleTime: Infinity, retry: false } },
    })
    client.setQueryData(
      [...listKey, "u_1"],
      [
        thread("th_1", "One", "2026-10-16T09:00:00Z"),
        thread("th_2", "Two", "2026-10-15T09:00:00Z"),
      ]
    )
    const detail: ThreadDetail = {
      ...thread("th_2", "Two", "2026-10-15T09:00:00Z"),
      items: [],
      pending_confirmations: [],
    }
    client.setQueryData(detailKey, detail)
    return client
  }

  it("shows the new title at once, then Go's copy", async () => {
    vi.useFakeTimers({ toFake: ["Date"] })
    vi.setSystemTime(new Date("2026-10-16T10:30:00Z"))
    const client = cache()
    let answer: (thread: Thread) => void = () => undefined
    const pending = renameCachedThread({
      queryClient: client,
      listKey,
      detailKey,
      threadId: "th_2",
      title: " Vendor  plan ",
      rename: (title) => {
        expect(title).toBe("Vendor plan")
        return new Promise<Thread>((resolve) => (answer = resolve))
      },
    })
    await vi.waitFor(() =>
      expect(
        client.getQueryData<Thread[]>([...listKey, "u_1"])?.[0].title
      ).toBe("Vendor plan")
    )
    expect(client.getQueryData<ThreadDetail>(detailKey)?.title).toBe(
      "Vendor plan"
    )
    answer({
      ...thread("th_2", "Vendor plan", "2026-10-16T11:00:00Z"),
      artifact_count: 2,
    })
    await pending
    const list = client.getQueryData<Thread[]>([...listKey, "u_1"])
    expect(list?.[0]).toMatchObject({
      id: "th_2",
      updated_at: "2026-10-16T11:00:00Z",
      artifact_count: 2,
    })
  })

  it("puts the old title back when Go refuses", async () => {
    const client = cache()
    const refusal = new ApiError("A run is answering", 409, "run_in_progress")
    await expect(
      renameCachedThread({
        queryClient: client,
        listKey,
        detailKey,
        threadId: "th_2",
        title: "Vendor plan",
        rename: () => Promise.reject(refusal),
      })
    ).rejects.toBe(refusal)
    expect(
      client.getQueryData<Thread[]>([...listKey, "u_1"])?.map((t) => t.title)
    ).toEqual(["One", "Two"])
    expect(client.getQueryData<ThreadDetail>(detailKey)?.title).toBe("Two")
  })
})

describe("renameFailureText", () => {
  it("says the agent is answering on 409", () => {
    expect(
      renameFailureText(new ApiError("busy", 409, "run_in_progress"), "Krude")
    ).toBe("Krude is answering — try again in a moment")
  })

  it("says renaming isn't there yet before Go serves it", () => {
    expect(renameFailureText(new ApiError("", 405))).toBe(
      "Renaming isn't available yet."
    )
    expect(renameFailureText(new Error("network"))).toBe(
      "Couldn't rename this chat. Try again."
    )
  })
})
