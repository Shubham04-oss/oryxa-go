// @vitest-environment jsdom
import { act, renderHook } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  FRESH_CONVERSATION_MS,
  chooseThread,
  claimPrimaryChatRequest,
  openingThread,
  publishPrimaryChatActivity,
  rememberChoice,
  rememberedChoice,
  requestPrimaryChat,
  sameChatScope,
  usePrimaryChatActivity,
  usePrimaryChatRequest,
} from "./chat-choice"

const now = Date.parse("2026-09-30T10:00:00Z")
const ago = (ms: number) => new Date(now - ms).toISOString()
const scope = { organizationId: "org-1", userId: "user-1" }

describe("chooseThread", () => {
  it("reopens the latest conversation while it's fresh", () => {
    const threads = [
      { id: "t2", title: "Plan", updated_at: ago(5 * 60_000), origin: "chat" },
    ]
    expect(chooseThread(threads, null, now)).toBe("t2")
  })

  it("opens a new chat once the latest conversation is an hour old", () => {
    const threads = [
      {
        id: "t2",
        title: "Plan",
        updated_at: ago(FRESH_CONVERSATION_MS),
        origin: "chat",
      },
    ]
    expect(chooseThread(threads, null, now)).toBeNull()
  })

  it("opens a new chat when there's no conversation or no readable time", () => {
    expect(chooseThread([], null, now)).toBeNull()
    expect(
      chooseThread(
        [{ id: "t1", title: "Plan", updated_at: "", origin: "chat" }],
        null,
        now
      )
    ).toBeNull()
  })

  it("follows the member's choice over freshness", () => {
    const threads = [
      { id: "t2", title: "Plan", updated_at: ago(60_000), origin: "chat" },
      {
        id: "t1",
        title: "Plan",
        updated_at: ago(3 * FRESH_CONVERSATION_MS),
        origin: "chat",
      },
    ]
    expect(chooseThread(threads, "new", now)).toBeNull()
    expect(chooseThread(threads, "t1", now)).toBe("t1")
  })

  it("ignores a chosen conversation that no longer exists", () => {
    const threads = [
      { id: "t2", title: "Plan", updated_at: ago(60_000), origin: "chat" },
    ]
    expect(chooseThread(threads, "gone", now)).toBe("t2")
  })

  it("never picks a conversation that isn't a chat", () => {
    const threads = [
      { id: "w1", title: "Plan", updated_at: ago(60_000), origin: "wakeup" },
      { id: "t1", title: "Plan", updated_at: ago(2 * 60_000), origin: "chat" },
    ]
    expect(chooseThread(threads, null, now)).toBe("t1")
  })

  it("skips a conversation with no title, which no message ever went into", () => {
    const threads = [
      { id: "t3", title: "", updated_at: ago(60_000), origin: "chat" },
      { id: "t2", title: "  ", updated_at: ago(2 * 60_000), origin: "chat" },
      { id: "t1", title: "Plan", updated_at: ago(3 * 60_000), origin: "chat" },
    ]
    expect(chooseThread(threads, null, now)).toBe("t1")
    // Nothing but abandoned starts is no conversation at all.
    expect(chooseThread(threads.slice(0, 2), null, now)).toBeNull()
    // One the member chose is theirs to open, title or not.
    expect(chooseThread(threads, "t3", now)).toBe("t3")
  })

  it("reads a thread with no origin as a chat, as Go does", () => {
    const threads = [
      { id: "t1", title: "Plan", updated_at: ago(60_000), origin: "" },
    ]
    expect(chooseThread(threads, null, now)).toBe("t1")
  })
})

describe("openingThread", () => {
  const old = (id: string, origin = "chat") => ({
    id,
    title: "Plan",
    updated_at: ago(3 * FRESH_CONVERSATION_MS),
    origin,
  })

  it("asks nothing when the member's choice or freshness decides", async () => {
    const waiting = vi.fn(async () => true)
    const fresh = [
      { id: "t2", title: "Plan", updated_at: ago(60_000), origin: "chat" },
    ]
    expect(
      await openingThread({ threads: fresh, choice: null, now, waiting })
    ).toBe("t2")
    expect(
      await openingThread({ threads: [old("t1")], choice: "t1", now, waiting })
    ).toBe("t1")
    expect(
      await openingThread({ threads: fresh, choice: "new", now, waiting })
    ).toBeNull()
    expect(
      await openingThread({ threads: [], choice: null, now, waiting })
    ).toBeNull()
    expect(waiting).not.toHaveBeenCalled()
  })

  it("keeps an old latest conversation open only while it waits on the member", async () => {
    const threads = [old("t2"), old("t1")]
    const waits = vi.fn(async (id: string) => id === "t2")
    expect(
      await openingThread({ threads, choice: null, now, waiting: waits })
    ).toBe("t2")
    expect(waits).toHaveBeenCalledExactlyOnceWith("t2")

    // Only the latest is asked about, not the older ones.
    const nothing = vi.fn(async () => false)
    expect(
      await openingThread({ threads, choice: null, now, waiting: nothing })
    ).toBeNull()
    expect(nothing).toHaveBeenCalledExactlyOnceWith("t2")
  })

  it("asks about the latest titled chat, not an abandoned start", async () => {
    const waiting = vi.fn(async () => true)
    const threads = [{ ...old("t2"), title: "" }, old("t1")]
    expect(await openingThread({ threads, choice: null, now, waiting })).toBe(
      "t1"
    )
    expect(waiting).toHaveBeenCalledExactlyOnceWith("t1")
  })

  it("asks about the latest chat, not an update from the member's apps", async () => {
    const waiting = vi.fn(async () => true)
    const threads = [old("w1", "wakeup"), old("t1")]
    expect(await openingThread({ threads, choice: null, now, waiting })).toBe(
      "t1"
    )
    expect(waiting).toHaveBeenCalledExactlyOnceWith("t1")
    expect(
      await openingThread({
        threads: [old("w1", "wakeup")],
        choice: null,
        now,
        waiting,
      })
    ).toBeNull()
    expect(waiting).toHaveBeenCalledOnce()
  })

  it("falls back to the latest when the chosen conversation is gone", async () => {
    const waiting = vi.fn(async () => true)
    expect(
      await openingThread({
        threads: [old("t1")],
        choice: "gone",
        now,
        waiting,
      })
    ).toBe("t1")
  })

  it("opens a new chat when the check itself fails", async () => {
    const waiting = vi.fn(async () => {
      throw new Error("offline")
    })
    expect(
      await openingThread({ threads: [old("t1")], choice: null, now, waiting })
    ).toBeNull()
  })
})

describe("remembered choice", () => {
  afterEach(() => window.sessionStorage.clear())

  it("keeps a choice per organization, member and agent", () => {
    rememberChoice(scope, "agent-a", "t1")
    rememberChoice(scope, "agent-b", null)
    expect(rememberedChoice(scope, "agent-a")).toBe("t1")
    expect(rememberedChoice(scope, "agent-b")).toBe("new")
    expect(
      rememberedChoice({ organizationId: "org-2", userId: "user-1" }, "agent-a")
    ).toBeNull()
  })
})

describe("requestPrimaryChat", () => {
  it("hands each request to the chat with a growing id", () => {
    const { result } = renderHook(() => usePrimaryChatRequest())
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "t1" }))
    const first = result.current
    expect(first).toMatchObject({ kind: "thread", threadId: "t1", scope })
    act(() => requestPrimaryChat(scope, { kind: "new", message: "Hello" }))
    expect(result.current).toMatchObject({ kind: "new", message: "Hello" })
    expect(result.current!.id).toBeGreaterThan(first!.id)
  })
})

describe("claiming a request", () => {
  it("lets a request be acted on once, however often it is asked", () => {
    const { result } = renderHook(() => usePrimaryChatRequest())
    act(() => requestPrimaryChat(scope, { kind: "new", message: "Hello" }))
    const request = result.current!
    expect(claimPrimaryChatRequest(request.id)).toBe(true)
    expect(claimPrimaryChatRequest(request.id)).toBe(false)
    expect(claimPrimaryChatRequest(request.id - 1)).toBe(false)

    // The next request is a new one.
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "t1" }))
    expect(claimPrimaryChatRequest(result.current!.id)).toBe(true)
  })

  it("keeps a request made before anyone was listening", () => {
    act(() => requestPrimaryChat(scope, { kind: "new" }))
    // A panel that mounts now reads it, and can claim it.
    const { result } = renderHook(() => usePrimaryChatRequest())
    expect(result.current).toMatchObject({ kind: "new" })
    expect(claimPrimaryChatRequest(result.current!.id)).toBe(true)
  })

  it("knows a member's scope from another's", () => {
    expect(sameChatScope(scope, { ...scope })).toBe(true)
    expect(sameChatScope(scope, { ...scope, userId: "user-2" })).toBe(false)
    expect(sameChatScope(scope, { ...scope, organizationId: "org-2" })).toBe(
      false
    )
  })
})

describe("primary chat activity", () => {
  const other = { organizationId: "org-2", userId: "user-1" }
  afterEach(() => act(() => publishPrimaryChatActivity(null)))

  it("reads null until the chat publishes, follows it, and reads null again once it is gone", () => {
    const { result } = renderHook(() => usePrimaryChatActivity(scope))
    expect(result.current).toBeNull()

    act(() =>
      publishPrimaryChatActivity({ threadId: "t1", state: "running" }, scope)
    )
    expect(result.current).toEqual({ threadId: "t1", state: "running" })
    act(() =>
      publishPrimaryChatActivity({ threadId: "t1", state: "waiting" }, scope)
    )
    expect(result.current).toEqual({ threadId: "t1", state: "waiting" })
    // A new, empty chat has no thread.
    act(() =>
      publishPrimaryChatActivity({ threadId: null, state: "idle" }, scope)
    )
    expect(result.current).toEqual({ threadId: null, state: "idle" })

    act(() => publishPrimaryChatActivity(null))
    expect(result.current).toBeNull()
  })

  it("is heard only by the organization and member it belongs to", () => {
    const own = renderHook(() => usePrimaryChatActivity(scope))
    const elsewhere = renderHook(() => usePrimaryChatActivity(other))
    act(() =>
      publishPrimaryChatActivity({ threadId: "t1", state: "running" }, scope)
    )
    expect(own.result.current).toEqual({ threadId: "t1", state: "running" })
    expect(elsewhere.result.current).toBeNull()

    // The same activity from another scope moves to that scope's readers.
    act(() =>
      publishPrimaryChatActivity({ threadId: "t1", state: "running" }, other)
    )
    expect(own.result.current).toBeNull()
    expect(elsewhere.result.current).toEqual({
      threadId: "t1",
      state: "running",
    })
  })

  it("is heard by no one when the chat has no scope", () => {
    const { result } = renderHook(() => usePrimaryChatActivity(scope))
    act(() => publishPrimaryChatActivity({ threadId: "t1", state: "idle" }))
    expect(result.current).toBeNull()
  })

  it("wakes no one when the chat says what it already said", () => {
    let renders = 0
    const { result } = renderHook(() => {
      renders += 1
      return usePrimaryChatActivity(scope)
    })
    act(() =>
      publishPrimaryChatActivity({ threadId: "t1", state: "idle" }, scope)
    )
    const published = result.current
    const seen = renders

    act(() =>
      publishPrimaryChatActivity({ threadId: "t1", state: "idle" }, scope)
    )
    act(() =>
      publishPrimaryChatActivity({ threadId: "t1", state: "idle" }, scope)
    )
    expect(renders).toBe(seen)
    // Nothing changed, so it is the very object it was.
    expect(result.current).toBe(published)

    act(() =>
      publishPrimaryChatActivity({ threadId: "t2", state: "idle" }, scope)
    )
    expect(renders).toBe(seen + 1)
    act(() =>
      publishPrimaryChatActivity({ threadId: "t2", state: "running" }, scope)
    )
    expect(renders).toBe(seen + 2)

    // Being gone is said once too.
    act(() => publishPrimaryChatActivity(null))
    act(() => publishPrimaryChatActivity(null))
    expect(renders).toBe(seen + 3)
  })

  it("keeps its own copy of what was published", () => {
    const { result } = renderHook(() => usePrimaryChatActivity(scope))
    const activity = { threadId: "t1", state: "idle" as const }
    const owner = { ...scope }
    act(() => publishPrimaryChatActivity(activity, owner))
    activity.threadId = "t9"
    owner.userId = "someone-else"
    expect(result.current).toEqual({ threadId: "t1", state: "idle" })
  })

  it("tells a listener that arrives late what the chat last said", () => {
    act(() =>
      publishPrimaryChatActivity({ threadId: "t1", state: "running" }, scope)
    )
    const { result } = renderHook(() => usePrimaryChatActivity(scope))
    expect(result.current).toEqual({ threadId: "t1", state: "running" })
  })
})
