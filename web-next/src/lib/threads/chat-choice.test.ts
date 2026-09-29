// @vitest-environment jsdom
import { act, renderHook } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"
import {
  FRESH_CONVERSATION_MS,
  chooseThread,
  publishPrimaryChatActivity,
  rememberChoice,
  rememberedChoice,
  requestPrimaryChat,
  usePrimaryChatActivity,
  usePrimaryChatRequest,
} from "./chat-choice"

const now = Date.parse("2026-09-30T10:00:00Z")
const ago = (ms: number) => new Date(now - ms).toISOString()
const scope = { organizationId: "org-1", userId: "user-1" }

describe("chooseThread", () => {
  it("reopens the latest conversation while it's fresh", () => {
    const threads = [{ id: "t2", updated_at: ago(5 * 60_000), origin: "chat" }]
    expect(chooseThread(threads, null, now)).toBe("t2")
  })

  it("opens a new chat once the latest conversation is an hour old", () => {
    const threads = [
      { id: "t2", updated_at: ago(FRESH_CONVERSATION_MS), origin: "chat" },
    ]
    expect(chooseThread(threads, null, now)).toBeNull()
  })

  it("opens a new chat when there's no conversation or no readable time", () => {
    expect(chooseThread([], null, now)).toBeNull()
    expect(
      chooseThread([{ id: "t1", updated_at: "", origin: "chat" }], null, now)
    ).toBeNull()
  })

  it("follows the member's choice over freshness", () => {
    const threads = [
      { id: "t2", updated_at: ago(60_000), origin: "chat" },
      { id: "t1", updated_at: ago(3 * FRESH_CONVERSATION_MS), origin: "chat" },
    ]
    expect(chooseThread(threads, "new", now)).toBeNull()
    expect(chooseThread(threads, "t1", now)).toBe("t1")
  })

  it("ignores a chosen conversation that no longer exists", () => {
    const threads = [{ id: "t2", updated_at: ago(60_000), origin: "chat" }]
    expect(chooseThread(threads, "gone", now)).toBe("t2")
  })

  it("never picks a conversation that isn't a chat", () => {
    const threads = [
      { id: "w1", updated_at: ago(60_000), origin: "wakeup" },
      { id: "t1", updated_at: ago(2 * 60_000), origin: "chat" },
    ]
    expect(chooseThread(threads, null, now)).toBe("t1")
  })

  it("reads a thread with no origin as a chat, as Go does", () => {
    const threads = [{ id: "t1", updated_at: ago(60_000), origin: "" }]
    expect(chooseThread(threads, null, now)).toBe("t1")
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

describe("primary chat activity", () => {
  afterEach(() => act(() => publishPrimaryChatActivity(null)))

  it("reads null until the chat publishes, follows it, and reads null again once it is gone", () => {
    const { result } = renderHook(() => usePrimaryChatActivity())
    expect(result.current).toBeNull()

    act(() => publishPrimaryChatActivity({ threadId: "t1", state: "running" }))
    expect(result.current).toEqual({ threadId: "t1", state: "running" })
    act(() => publishPrimaryChatActivity({ threadId: "t1", state: "waiting" }))
    expect(result.current).toEqual({ threadId: "t1", state: "waiting" })
    // A new, empty chat has no thread.
    act(() => publishPrimaryChatActivity({ threadId: null, state: "idle" }))
    expect(result.current).toEqual({ threadId: null, state: "idle" })

    act(() => publishPrimaryChatActivity(null))
    expect(result.current).toBeNull()
  })

  it("wakes no one when the chat says what it already said", () => {
    let renders = 0
    const { result } = renderHook(() => {
      renders += 1
      return usePrimaryChatActivity()
    })
    act(() => publishPrimaryChatActivity({ threadId: "t1", state: "idle" }))
    const published = result.current
    const seen = renders

    act(() => publishPrimaryChatActivity({ threadId: "t1", state: "idle" }))
    act(() => publishPrimaryChatActivity({ threadId: "t1", state: "idle" }))
    expect(renders).toBe(seen)
    // Nothing changed, so it is the very object it was.
    expect(result.current).toBe(published)

    act(() => publishPrimaryChatActivity({ threadId: "t2", state: "idle" }))
    expect(renders).toBe(seen + 1)
    act(() => publishPrimaryChatActivity({ threadId: "t2", state: "running" }))
    expect(renders).toBe(seen + 2)

    // Being gone is said once too.
    act(() => publishPrimaryChatActivity(null))
    act(() => publishPrimaryChatActivity(null))
    expect(renders).toBe(seen + 3)
  })

  it("keeps its own copy of what was published", () => {
    const { result } = renderHook(() => usePrimaryChatActivity())
    const activity = { threadId: "t1", state: "idle" as const }
    act(() => publishPrimaryChatActivity(activity))
    activity.threadId = "t9"
    expect(result.current).toEqual({ threadId: "t1", state: "idle" })
  })

  it("tells a listener that arrives late what the chat last said", () => {
    act(() => publishPrimaryChatActivity({ threadId: "t1", state: "running" }))
    const { result } = renderHook(() => usePrimaryChatActivity())
    expect(result.current).toEqual({ threadId: "t1", state: "running" })
  })
})
