// @vitest-environment jsdom
import { act, cleanup, renderHook } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  FRESH_CONVERSATION_MS,
  MAX_WAITING_MESSAGES,
  PRIMARY_DRAFT,
  chooseThread,
  claimPrimaryChatRequest,
  firstMessage,
  initialPrimaryChat,
  lastChosenChat,
  openingThread,
  primaryOpeningThread,
  rememberLastChosenChat,
  useLastChosenChat,
  primaryChatReducer,
  publishPrimaryChatActivity,
  rememberChoice,
  rememberedChoice,
  rememberedView,
  requestPrimaryChat,
  sameChatScope,
  shownThread,
  usePrimaryChatActivity,
  usePrimaryChatRequest,
  useRememberedChoice,
} from "./chat-choice"
import type {
  PrimaryChat,
  PrimaryChatAction,
  PrimaryChatTarget,
} from "./chat-choice"

const now = Date.parse("2026-09-30T10:00:00Z")
const ago = (ms: number) => new Date(now - ms).toISOString()
const scope = { organizationId: "org-1", userId: "user-1" }

afterEach(() => {
  cleanup()
  window.sessionStorage.clear()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe("opening a conversation", () => {
  const threads = [
    { id: "newest", title: "Latest", updated_at: ago(1000), origin: "chat" },
    { id: "untitled", title: "", updated_at: ago(2000), origin: "chat" },
    {
      id: "old",
      title: "Earlier",
      updated_at: ago(24 * 60 * 60_000),
      origin: "chat",
    },
  ]
  it("does not infer member presence from a recently updated conversation", () => {
    expect(chooseThread(threads, null, now)).toBeNull()
    expect(chooseThread(threads, "old", now)).toBeNull()
  })
  it("reopens a recent history choice only strictly within five minutes", () => {
    expect(
      chooseThread(threads, "old", now, now - FRESH_CONVERSATION_MS + 1)
    ).toBe("old")
    expect(
      chooseThread(threads, "old", now, now - FRESH_CONVERSATION_MS)
    ).toBeNull()
    expect(chooseThread(threads, "old", now, now + 1)).toBeNull()
    expect(chooseThread(threads, "gone", now, now - 1)).toBeNull()
  })
  it("keeps a recent conversation or explicit New over a pending approval", async () => {
    const waiting = vi.fn(async () => true)
    expect(
      await openingThread({
        threads,
        choice: "old",
        leftAt: now - 1,
        now,
        waiting,
      })
    ).toBe("old")
    expect(
      await openingThread({
        threads,
        choice: "new",
        leftAt: now - 1,
        now,
        waiting,
      })
    ).toBeNull()
    expect(waiting).not.toHaveBeenCalled()
  })
  it("checks recent real chats and picks the newest pending, including untitled chats", async () => {
    const waiting = vi.fn(async (id: string) => id !== "newest")
    expect(
      await openingThread({
        threads: [
          threads[2],
          { id: "wakeup", title: "Apps", updated_at: ago(0), origin: "wakeup" },
          threads[0],
          threads[1],
        ],
        choice: "old",
        leftAt: now - FRESH_CONVERSATION_MS,
        now,
        waiting,
      })
    ).toBe("untitled")
    expect(waiting.mock.calls.map(([id]) => id)).toEqual([
      "newest",
      "untitled",
      "old",
    ])
  })
  it("checks at most the newest five chat conversations in a long history", async () => {
    const history = Array.from({ length: 20 }, (_, i) => ({
      id: `chat_${i}`,
      title: `Chat ${i}`,
      updated_at: ago(i * 1000),
      origin: "chat",
    })).reverse()
    const waiting = vi.fn(async (id: string) => id === "chat_5")
    expect(
      await openingThread({
        threads: [
          {
            id: "background",
            title: "Wake",
            origin: "wakeup",
            updated_at: ago(0),
          },
          ...history,
        ],
        choice: null,
        now,
        waiting,
      })
    ).toBeNull()
    expect(waiting.mock.calls.map(([id]) => id)).toEqual([
      "chat_0",
      "chat_1",
      "chat_2",
      "chat_3",
      "chat_4",
    ])
  })
  it("opens New when every check fails or nothing waits, including a stale New choice", async () => {
    expect(
      await openingThread({
        threads,
        choice: "new",
        leftAt: now - FRESH_CONVERSATION_MS,
        now,
        waiting: async () => false,
      })
    ).toBeNull()
    expect(
      await openingThread({
        threads,
        choice: null,
        now,
        waiting: async () => {
          throw new Error("offline")
        },
      })
    ).toBeNull()
  })
})

describe("the primary chat's opening rule", () => {
  afterEach(() => window.localStorage.clear())
  const threads = [
    { id: "newest", title: "Latest", updated_at: ago(1000), origin: "chat" },
    { id: "old", title: "Old", updated_at: ago(30 * 24 * 3600_000) },
    { id: "wake", title: "Apps", updated_at: ago(0), origin: "wakeup" },
  ]
  it("opens the chat the member last chose, however old, else New; never one for a pending confirmation (C1)", () => {
    expect(primaryOpeningThread(threads, "old")).toEqual({
      thread: "old",
      restored: true,
    })
    expect(primaryOpeningThread(threads, "new")).toEqual({
      thread: null,
      restored: true,
    })
    // Gone, never chosen, or not a chat: New, as the rules' own decision.
    for (const last of ["gone", null, "wake"])
      expect(primaryOpeningThread(threads, last)).toEqual({
        thread: null,
        restored: false,
      })
  })
  it("keeps the last choice across tabs, per organization, member and agent", () => {
    const hook = renderHook(
      ({ thread, chosen }) => useLastChosenChat(thread, scope, "agent", chosen),
      {
        initialProps: {
          thread: null as string | null | undefined,
          chosen: false,
        },
      }
    )
    // A decision the rules made is not the member's choice.
    expect(lastChosenChat(scope, "agent")).toBeNull()
    hook.rerender({ thread: "th_1", chosen: true })
    expect(lastChosenChat(scope, "agent")).toBe("th_1")
    hook.rerender({ thread: null, chosen: true })
    expect(lastChosenChat(scope, "agent")).toBe("new")
    expect(window.localStorage.length).toBe(1)
    rememberLastChosenChat(scope, "other", "th_2")
    expect(lastChosenChat(scope, "agent")).toBe("new")
    expect(
      lastChosenChat({ organizationId: "org-2", userId: "user-1" }, "agent")
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

/** Claims every request still waiting, so no test leaves one for the next. */
function drainRequests() {
  const { result, unmount } = renderHook(() => usePrimaryChatRequest())
  while (result.current) {
    const { id } = result.current
    act(() => {
      claimPrimaryChatRequest(id)
    })
  }
  unmount()
}

describe("requestPrimaryChat", () => {
  afterEach(drainRequests)

  it("hands each request to the chat with a growing id", () => {
    const { result } = renderHook(() => usePrimaryChatRequest())
    act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "t1" }))
    const first = result.current
    expect(first).toMatchObject({ kind: "thread", threadId: "t1", scope })
    act(() => claimPrimaryChatRequest(first!.id))
    act(() => requestPrimaryChat(scope, { kind: "new", message: "Hello" }))
    expect(result.current).toMatchObject({ kind: "new", message: "Hello" })
    expect(result.current!.id).toBeGreaterThan(first!.id)
  })

  it("keeps every request until it is claimed, and hands them over oldest first, each once", () => {
    const { result } = renderHook(() => usePrimaryChatRequest())
    act(() => {
      requestPrimaryChat(scope, { kind: "new", message: "First" })
      requestPrimaryChat(scope, { kind: "thread", threadId: "t1" })
      requestPrimaryChat(scope, { kind: "new", message: "Third" })
    })
    // The later requests did not replace the first.
    const handed: unknown[] = []
    while (result.current) {
      const request = result.current
      handed.push(request.kind === "new" ? request.message : request.threadId)
      const claims: boolean[] = []
      act(() => {
        claims.push(claimPrimaryChatRequest(request.id))
        // Claimed, it is gone: not claimed again.
        claims.push(claimPrimaryChatRequest(request.id))
      })
      expect(claims).toEqual([true, false])
    }
    expect(handed).toEqual(["First", "t1", "Third"])
  })

  it("does not hand a request over again once it is claimed, however often the chat reads", () => {
    act(() => requestPrimaryChat(scope, { kind: "new", message: "Once" }))
    const first = renderHook(() => usePrimaryChatRequest())
    const id = first.result.current!.id
    act(() => {
      claimPrimaryChatRequest(id)
    })
    expect(first.result.current).toBeNull()
    // A chat that mounts afterwards finds nothing to act on.
    const later = renderHook(() => usePrimaryChatRequest())
    expect(later.result.current).toBeNull()
  })

  it("hands over one member's requests without waiting behind another's", () => {
    const other = { organizationId: "org-1", userId: "user-2" }
    act(() => {
      requestPrimaryChat(other, { kind: "new", message: "Theirs" })
      requestPrimaryChat(scope, { kind: "new", message: "Mine" })
    })
    const mine = renderHook(() => usePrimaryChatRequest(scope))
    expect(mine.result.current).toMatchObject({ message: "Mine", scope })
    const theirs = renderHook(() => usePrimaryChatRequest(other))
    expect(theirs.result.current).toMatchObject({ message: "Theirs" })
    // Without a scope, whoever came first.
    const anyone = renderHook(() => usePrimaryChatRequest())
    expect(anyone.result.current).toMatchObject({ message: "Theirs" })

    act(() => {
      claimPrimaryChatRequest(mine.result.current!.id)
    })
    expect(mine.result.current).toBeNull()
    expect(theirs.result.current).toMatchObject({ message: "Theirs" })
  })

  describe("when more messages wait than the chat may hold", () => {
    const say = (message: string, from = scope) =>
      act(() => requestPrimaryChat(from, { kind: "new", message }))
    const waiting = () => {
      const seen: NonNullable<ReturnType<typeof usePrimaryChatRequest>>[] = []
      const { result } = renderHook(() => usePrimaryChatRequest())
      while (result.current) {
        const request = result.current
        seen.push(request)
        act(() => {
          claimPrimaryChatRequest(request.id)
        })
      }
      return seen
    }

    it("refuses the one past the limit, and pushes no older message out to make room", () => {
      for (let count = 1; count <= MAX_WAITING_MESSAGES; count += 1) {
        say(`Message ${count}`)
      }
      say("One too many")
      const requests = waiting()
      expect(requests).toHaveLength(MAX_WAITING_MESSAGES + 1)
      const kept = requests.slice(0, MAX_WAITING_MESSAGES)
      // Every message that was in is still in, in order, and none is marked.
      expect(
        kept.map((request) => "message" in request && request.message)
      ).toEqual(
        Array.from(
          { length: MAX_WAITING_MESSAGES },
          (_, index) => `Message ${index + 1}`
        )
      )
      expect(kept.some((request) => request.refused)).toBe(false)
      // The one after them says so, and still has its words.
      expect(requests.at(-1)).toMatchObject({
        message: "One too many",
        refused: true,
      })
    })

    it("makes room again as the chat takes messages", () => {
      for (let count = 1; count <= MAX_WAITING_MESSAGES; count += 1) {
        say(`Message ${count}`)
      }
      const { result } = renderHook(() => usePrimaryChatRequest())
      act(() => {
        claimPrimaryChatRequest(result.current!.id)
      })
      say("Room for this one")
      expect(waiting().at(-1)).toMatchObject({ message: "Room for this one" })
      expect(waiting()).toEqual([])
    })

    it("counts only messages, and only this member's", () => {
      const other = { organizationId: "org-1", userId: "user-2" }
      for (let count = 1; count <= MAX_WAITING_MESSAGES; count += 1) {
        say(`Theirs ${count}`, other)
      }
      // Another member's messages take none of this member's room, and a
      // request that only opens a conversation is never refused.
      say("Mine")
      act(() => requestPrimaryChat(scope, { kind: "thread", threadId: "t1" }))
      act(() => requestPrimaryChat(scope, { kind: "new", message: "   " }))
      const mine = waiting().filter((request) => request.scope === scope)
      expect(mine).toHaveLength(3)
      expect(mine.some((request) => request.refused)).toBe(false)
    })
  })
})

describe("claiming a request", () => {
  afterEach(drainRequests)

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

describe("the primary chat's state", () => {
  const run = (actions: PrimaryChatAction[], from = initialPrimaryChat()) =>
    actions.reduce(primaryChatReducer, from)
  const ask = (id: number, target: PrimaryChatTarget): PrimaryChatAction => ({
    type: "request",
    request: { ...target, id, scope },
  })
  /** A first message's conversation is made, its chat takes the message, and the move on follows. */
  const delivered = (id: number, threadId: string): PrimaryChatAction[] => [
    { type: "making", id },
    { type: "made", id, threadId },
    { type: "taken", id },
    { type: "sent", id },
  ]
  const states = (chat: PrimaryChat) => ({
    thread: chat.thread,
    shown: shownThread(chat),
    waiting: chat.tasks.map((task) => task.id),
  })

  it("opens what a request names at once, and a blank chat for one with no message", () => {
    const opened = run([ask(1, { kind: "thread", threadId: "t1" })])
    expect(states(opened)).toEqual({ thread: "t1", shown: "t1", waiting: [] })
    expect(opened.touched).toBe(1)

    const blank = run([ask(2, { kind: "new", message: "   " })], opened)
    expect(states(blank)).toEqual({ thread: null, shown: null, waiting: [] })
    expect(blank.touched).toBe(2)
  })

  it("makes a request's message a conversation of its own, and stays in it once it is sent", () => {
    const asked = run([ask(1, { kind: "new", message: "  Hello  " })])
    // Nothing is decided yet, and the chat is a blank one meanwhile.
    expect(states(asked)).toEqual({
      thread: undefined,
      shown: null,
      waiting: [1],
    })
    expect(firstMessage(asked)).toMatchObject({ id: 1, text: "Hello" })

    const made = run(
      [
        { type: "making", id: 1 },
        { type: "made", id: 1, threadId: "t_a" },
      ],
      asked
    )
    expect(shownThread(made)).toBe("t_a")
    expect(firstMessage(made)).toMatchObject({ making: true, threadId: "t_a" })

    // Taken by its conversation's chat, it is still on its way for a moment.
    const taken = run([{ type: "taken", id: 1 }], made)
    expect(firstMessage(taken)?.taken).toBe(true)
    expect(shownThread(taken)).toBe("t_a")

    const sent = run([{ type: "sent", id: 1 }], taken)
    expect(states(sent)).toEqual({ thread: "t_a", shown: "t_a", waiting: [] })
  })

  it("puts a second message behind the first: each its own conversation, and the chat ends on the last", () => {
    const both = run([
      ask(1, { kind: "new", message: "First" }),
      ask(2, { kind: "new", message: "Second" }),
    ])
    expect(states(both).waiting).toEqual([1, 2])
    expect(firstMessage(both)?.text).toBe("First")

    // The first one's conversation arrives late: it is still the first's.
    const first = run(
      [
        { type: "making", id: 1 },
        { type: "made", id: 1, threadId: "t_a" },
      ],
      both
    )
    expect(shownThread(first)).toBe("t_a")
    expect(states(first).waiting).toEqual([1, 2])

    // Only when it is taken and settled does the second have its turn.
    const taken = run([{ type: "taken", id: 1 }], first)
    expect(firstMessage(taken)?.id).toBe(1)
    const second = run([{ type: "sent", id: 1 }], taken)
    expect(firstMessage(second)).toMatchObject({ id: 2, text: "Second" })
    expect(states(second)).toMatchObject({ thread: "t_a", shown: null })

    const done = run([...delivered(2, "t_b")], second)
    expect(states(done)).toEqual({ thread: "t_b", shown: "t_b", waiting: [] })
  })

  it("holds a request to open a conversation behind a message on its way, then opens it", () => {
    const chat = run([
      ask(1, { kind: "new", message: "First" }),
      ask(2, { kind: "thread", threadId: "t_x" }),
    ])
    expect(states(chat)).toEqual({
      thread: undefined,
      shown: null,
      waiting: [1, 2],
    })
    const after = run(delivered(1, "t_a"), chat)
    // The first is delivered, then the later request has its say.
    expect(states(after)).toEqual({ thread: "t_x", shown: "t_x", waiting: [] })
  })

  it("never touches the draft for a request, and takes it from the composer for a send", () => {
    const typed = run([{ type: "draft", key: PRIMARY_DRAFT, text: "Half" }])
    const requested = run(
      [ask(1, { kind: "new", message: "From Home" }), ...delivered(1, "t_a")],
      typed
    )
    expect(requested.drafts[PRIMARY_DRAFT]).toBe("Half")

    const sent = run(
      [
        { type: "draft", key: "agt_scout", text: "For Scout" },
        { type: "submit", text: "  Plan it  " },
      ],
      requested
    )
    expect(sent.drafts).toEqual({ [PRIMARY_DRAFT]: "", agt_scout: "For Scout" })
    expect(firstMessage(sent)?.text).toBe("Plan it")
    // An empty send is nothing.
    expect(run([{ type: "submit", text: "  " }], typed)).toBe(typed)
  })

  it("keeps a message no conversation could be made for as not sent, and never touches the composer", () => {
    const failed = (draft: string) =>
      run(
        [
          { type: "draft", key: PRIMARY_DRAFT, text: draft },
          ask(1, { kind: "new", message: "From Home" }),
          { type: "making", id: 1 },
          { type: "failed", id: 1, notice: "Couldn't start a conversation." },
        ],
        run([{ type: "decide", thread: "t_old" }])
      )

    for (const draft of ["", "Half a thought"]) {
      const chat = failed(draft)
      expect(chat.drafts[PRIMARY_DRAFT]).toBe(draft)
      expect(chat.unsent).toEqual([
        {
          id: 1,
          text: "From Home",
          screen: undefined,
          notice: "Couldn't start a conversation.",
        },
      ])
      // A blank chat, not the conversation it was on.
      expect(states(chat)).toEqual({ thread: null, shown: null, waiting: [] })
    }
  })

  it("goes on to what waited behind a message that failed", () => {
    const chat = run([
      ask(1, { kind: "new", message: "First" }),
      ask(2, { kind: "new", message: "Second" }),
      { type: "failed", id: 1, notice: "Couldn't start a conversation." },
    ])
    expect(firstMessage(chat)).toMatchObject({ id: 2, text: "Second" })
    expect(chat.unsent.map((message) => message.text)).toEqual(["First"])
    expect(chat.drafts[PRIMARY_DRAFT]).toBeUndefined()
  })

  describe("a message that was not sent", () => {
    const failedFirst = () =>
      run([
        ask(1, { kind: "new", message: "First" }),
        { type: "failed", id: 1, notice: "Couldn't start a conversation." },
      ])

    it("stays through every later request, send, move and step, until the member acts on it", () => {
      const later = run(
        [
          ask(2, { kind: "new", message: "Second" }),
          ...delivered(2, "t_b"),
          ask(3, { kind: "thread", threadId: "t_x" }),
          { type: "move", thread: null, notice: "Gone." },
          { type: "submit", text: "Third" },
          { type: "making", id: -1 },
          { type: "failed", id: -1, notice: "Down again." },
          { type: "draft", key: PRIMARY_DRAFT, text: "Typing" },
        ],
        failedFirst()
      )
      // Oldest first: the first message is still there, and so is the third.
      expect(later.unsent.map((message) => message.text)).toEqual([
        "First",
        "Third",
      ])
      expect(later.unsent.map((message) => message.notice)).toEqual([
        "Couldn't start a conversation.",
        "Down again.",
      ])
    })

    it("is sent again as a send of its own, last in line, from the screen it was first sent from", () => {
      const screen = { view: "home/overview", visible_items: [] }
      const chat = run([
        {
          type: "request",
          request: { kind: "new", message: "First", id: 1, scope },
          screen,
        },
        { type: "failed", id: 1, notice: "Down." },
        ask(2, { kind: "new", message: "Second" }),
        { type: "retry", id: 1 },
      ])
      expect(chat.unsent).toEqual([])
      expect(
        chat.tasks.map((task) => task.kind === "send" && task.text)
      ).toEqual(["Second", "First"])
      // Not the id it had: a step still coming for the failed one lands nowhere.
      const retried = chat.tasks[1]
      expect(retried.id).toBeLessThan(0)
      expect(retried).toMatchObject({ kind: "send", screen })
      expect(
        primaryChatReducer(chat, { type: "made", id: 1, threadId: "t_late" })
      ).toBe(chat)
      expect(chat.touched).toBe(3)

      // Second has its turn, then the retried message its own conversation.
      const done = run(
        [...delivered(2, "t_b"), ...delivered(retried.id, "t_c")],
        chat
      )
      expect(states(done)).toEqual({ thread: "t_c", shown: "t_c", waiting: [] })
    })

    it("is also what a message the chat had no room for becomes: kept, and nothing sent", () => {
      const refused = (message: string, id: number): PrimaryChatAction => ({
        type: "refused",
        request: { kind: "new", message, id, scope, refused: true },
        notice: "Too many messages were waiting.",
      })
      const chat = run([
        ask(1, { kind: "new", message: "First" }),
        refused("  Extra  ", 2),
      ])
      expect(chat.unsent).toEqual([
        {
          id: 2,
          text: "Extra",
          screen: undefined,
          notice: "Too many messages were waiting.",
        },
      ])
      // Nothing is waiting to be sent for it, and it counts as the member's doing.
      expect(chat.tasks.map((task) => task.id)).toEqual([1])
      expect(chat.touched).toBe(2)
      // Without words there is nothing to keep.
      expect(primaryChatReducer(chat, refused("   ", 3))).toBe(chat)

      // And like any other, it can be sent again.
      const again = run([{ type: "retry", id: 2 }], chat)
      expect(
        again.tasks.map((task) => task.kind === "send" && task.text)
      ).toEqual(["First", "Extra"])
    })

    it("can fail again, and is then one message again, not two", () => {
      const again = run(
        [
          { type: "retry", id: 1 },
          { type: "making", id: -1 },
          { type: "failed", id: -1, notice: "Still down." },
        ],
        failedFirst()
      )
      expect(again.unsent).toEqual([
        { id: -1, text: "First", screen: undefined, notice: "Still down." },
      ])
    })

    it("goes into the composer to be changed only while the composer is empty", () => {
      const empty = run([{ type: "edit", id: 1 }], failedFirst())
      expect(empty.drafts[PRIMARY_DRAFT]).toBe("First")
      expect(empty.unsent).toEqual([])

      // Whitespace is nothing to protect.
      const blank = run(
        [
          { type: "draft", key: PRIMARY_DRAFT, text: "  " },
          { type: "edit", id: 1 },
        ],
        failedFirst()
      )
      expect(blank.drafts[PRIMARY_DRAFT]).toBe("First")

      const busy = run(
        [{ type: "draft", key: PRIMARY_DRAFT, text: "Half a thought" }],
        failedFirst()
      )
      expect(primaryChatReducer(busy, { type: "edit", id: 1 })).toBe(busy)
    })

    it("can be let go, and a second click or a stale id changes nothing", () => {
      const chat = failedFirst()
      const gone = run([{ type: "dismiss", id: 1 }], chat)
      expect(gone.unsent).toEqual([])
      for (const stale of [
        { type: "dismiss", id: 1 },
        { type: "retry", id: 1 },
        { type: "edit", id: 1 },
      ] satisfies PrimaryChatAction[]) {
        expect(primaryChatReducer(gone, stale)).toBe(gone)
      }
      expect(primaryChatReducer(chat, { type: "dismiss", id: 9 })).toBe(chat)
    })
  })

  it("ignores a step for a message that is gone", () => {
    const chat = run([ask(1, { kind: "new", message: "First" })])
    for (const stale of [
      { type: "making", id: 9 },
      { type: "made", id: 9, threadId: "t_z" },
      { type: "taken", id: 9 },
      { type: "sent", id: 9 },
      { type: "failed", id: 9, notice: "x" },
    ] satisfies PrimaryChatAction[]) {
      expect(primaryChatReducer(chat, stale)).toBe(chat)
    }
    // Nor is a message sent before its conversation took it, or twice.
    expect(primaryChatReducer(chat, { type: "sent", id: 1 })).toBe(chat)
    const made = run([{ type: "made", id: 1, threadId: "t_a" }], chat)
    expect(
      primaryChatReducer(made, { type: "made", id: 1, threadId: "t_b" })
    ).toBe(made)
    expect(primaryChatReducer(chat, { type: "taken", id: 1 })).toBe(chat)
  })

  it("lets the rules decide only a chat that is undecided and idle", () => {
    const decided = run([{ type: "decide", thread: "t1" }])
    expect(decided.thread).toBe("t1")
    expect(primaryChatReducer(decided, { type: "decide", thread: "t2" })).toBe(
      decided
    )
    const busy = run([ask(1, { kind: "new", message: "First" })])
    expect(primaryChatReducer(busy, { type: "decide", thread: "t2" })).toBe(
      busy
    )
  })

  it("moves the chat for the member, but not away from a message that has not been taken", () => {
    const on = run([{ type: "decide", thread: "t1" }])
    const away = primaryChatReducer(on, {
      type: "move",
      thread: null,
      notice: "That conversation no longer exists.",
    })
    expect(states(away)).toEqual({ thread: null, shown: null, waiting: [] })
    expect(away.notice).toBe("That conversation no longer exists.")

    const waiting = run(
      [
        ask(1, { kind: "new", message: "First" }),
        { type: "made", id: 1, threadId: "t_a" },
      ],
      on
    )
    expect(primaryChatReducer(waiting, { type: "move", thread: null })).toBe(
      waiting
    )
    // Once its conversation has it, the move is the member's again.
    const taken = run([{ type: "taken", id: 1 }], waiting)
    expect(
      states(primaryChatReducer(taken, { type: "move", thread: null }))
    ).toEqual({ thread: null, shown: null, waiting: [] })
  })

  it("forgets everything for another member, and keeps counting so no old step lands on a new send", () => {
    const first = run([
      { type: "draft", key: PRIMARY_DRAFT, text: "Mine" },
      { type: "submit", text: "Hello" },
    ])
    const id = firstMessage(first)!.id
    const failed = run(
      [
        ask(7, { kind: "new", message: "Not sent" }),
        { type: "failed", id: 7, notice: "Down." },
      ],
      first
    )
    expect(failed.unsent).toHaveLength(1)
    const reset = primaryChatReducer(failed, { type: "reset" })
    expect(reset.thread).toBeUndefined()
    expect(reset.tasks).toEqual([])
    expect(reset.drafts).toEqual({})
    expect(reset.unsent).toEqual([])
    expect(reset.touched).toBe(0)

    const next = run([{ type: "submit", text: "Hi" }], reset)
    expect(firstMessage(next)!.id).not.toBe(id)
    // A step for the old send finds nothing to change.
    expect(
      primaryChatReducer(next, { type: "made", id, threadId: "t_old" })
    ).toBe(next)
  })

  it("takes the screen a message was sent from along with it", () => {
    const screen = { view: "home/overview", visible_items: [] }
    const chat = run([
      {
        type: "request",
        request: { kind: "new", message: "Hi", id: 5, scope },
        screen,
      },
    ])
    expect(firstMessage(chat)?.screen).toBe(screen)
  })
})

describe("what the primary chat remembers", () => {
  afterEach(() => window.sessionStorage.clear())
  const run = (actions: PrimaryChatAction[], from = initialPrimaryChat()) =>
    actions.reduce(primaryChatReducer, from)
  const ask = (id: number, target: PrimaryChatTarget): PrimaryChatAction => ({
    type: "request",
    request: { ...target, id, scope },
  })

  it("counts only a choice, not the rules' decision", () => {
    expect(run([{ type: "decide", thread: "t1" }]).chosen).toBe(false)
    expect(run([ask(1, { kind: "thread", threadId: "t1" })]).chosen).toBe(true)
    expect(run([ask(1, { kind: "new" })]).chosen).toBe(true)
    expect(
      run([
        { type: "decide", thread: "t1" },
        { type: "move", thread: null },
      ]).chosen
    ).toBe(true)
    // A message whose conversation was made and sent is one too.
    const sent = run([
      ask(1, { kind: "new", message: "Hi" }),
      { type: "made", id: 1, threadId: "t_a" },
      { type: "taken", id: 1 },
      { type: "sent", id: 1 },
    ])
    expect(sent.chosen).toBe(true)
  })

  it("keeps what the chat shows once it is a choice, in the order things happened", () => {
    const { rerender } = renderHook(
      ({ chat }) => useRememberedChoice(chat, scope, "agt_leo"),
      { initialProps: { chat: initialPrimaryChat() } }
    )
    const remembered = () => rememberedChoice(scope, "agt_leo")
    expect(remembered()).toBeNull()

    // Automatic opens are last-viewed conversations too.
    const decided = run([{ type: "decide", thread: "t1" }])
    rerender({ chat: decided })
    expect(remembered()).toBe("t1")

    // A message asked for from elsewhere: a blank chat, then its conversation.
    const asked = run([ask(1, { kind: "new", message: "Hi" })], decided)
    rerender({ chat: asked })
    expect(remembered()).toBe("new")
    // A request that waits behind it does not change what is remembered
    // until it has its turn.
    const waiting = run([ask(2, { kind: "thread", threadId: "t_x" })], asked)
    rerender({ chat: waiting })
    expect(remembered()).toBe("new")
    const made = run([{ type: "made", id: 1, threadId: "t_a" }], waiting)
    rerender({ chat: made })
    expect(remembered()).toBe("t_a")
    const done = run(
      [
        { type: "taken", id: 1 },
        { type: "sent", id: 1 },
      ],
      made
    )
    rerender({ chat: done })
    expect(remembered()).toBe("t_x")
  })

  it("keeps nothing without a member and an agent", () => {
    const chat = run([ask(1, { kind: "thread", threadId: "t1" })])
    renderHook(() => useRememberedChoice(chat, undefined, "agt_leo"))
    renderHook(() => useRememberedChoice(chat, scope, undefined))
    expect(window.sessionStorage.length).toBe(0)
  })
})

describe("last viewed timestamps", () => {
  afterEach(() => {
    window.sessionStorage.clear()
    vi.useRealTimers()
  })
  it("records a real leave and never extends a hidden panel's grace", () => {
    vi.useFakeTimers({ toFake: ["Date"] })
    vi.setSystemTime(now)
    const chat = { ...initialPrimaryChat(), thread: "old" }
    const hook = renderHook(
      ({ visible }) => useRememberedChoice(chat, scope, "agent", visible),
      { initialProps: { visible: true } }
    )
    expect(rememberedView(scope, "agent")?.leftAt).toBe(now)
    vi.setSystemTime(now + 1000)
    hook.rerender({ visible: false })
    expect(rememberedView(scope, "agent")?.leftAt).toBe(now + 1000)
    vi.setSystemTime(now + FRESH_CONVERSATION_MS + 1000)
    act(() => {
      document.dispatchEvent(new Event("pointerdown"))
      window.dispatchEvent(new Event("pagehide"))
    })
    expect(rememberedView(scope, "agent")?.leftAt).toBe(now + 1000)
  })
  it("throttles activity saves to once per 15 seconds but saves a page leave immediately", () => {
    vi.useFakeTimers({ toFake: ["Date"] })
    vi.setSystemTime(now)
    const writes = vi.spyOn(
      Object.getPrototypeOf(window.sessionStorage),
      "setItem"
    )
    const hook = renderHook(() =>
      useRememberedChoice(
        { ...initialPrimaryChat(), thread: "old" },
        scope,
        "agent"
      )
    )
    expect(writes).toHaveBeenCalledTimes(1)
    for (let second = 1; second < 15; second++) {
      vi.setSystemTime(now + second * 1000)
      act(() => {
        document.dispatchEvent(new Event("pointerdown"))
        document.dispatchEvent(new Event("keydown"))
        for (let wheel = 0; wheel < 10; wheel++)
          document.dispatchEvent(new Event("wheel"))
      })
    }
    expect(writes).toHaveBeenCalledTimes(1)
    vi.setSystemTime(now + 15_000)
    act(() => document.dispatchEvent(new Event("wheel")))
    expect(writes).toHaveBeenCalledTimes(2)
    vi.setSystemTime(now + 15_001)
    act(() => document.dispatchEvent(new Event("keydown")))
    expect(writes).toHaveBeenCalledTimes(2)
    act(() => window.dispatchEvent(new Event("pagehide")))
    expect(writes).toHaveBeenCalledTimes(3)
    expect(rememberedView(scope, "agent")?.leftAt).toBe(now + 15_001)
    hook.unmount()
    expect(writes).toHaveBeenCalledTimes(3)
    writes.mockRestore()
  })
  it("treats a legacy unbounded choice as stale", () => {
    window.sessionStorage.setItem("oryxa.chat.org-1.user-1.agent", "old")
    expect(rememberedView(scope, "agent")).toBeNull()
  })
})

describe("automatic blank views", () => {
  afterEach(() => {
    window.sessionStorage.clear()
    vi.useRealTimers()
  })
  it("does not keep automatic New over an approval that arrives before reload", async () => {
    const blank = { ...initialPrimaryChat(), thread: null }
    const hook = renderHook(() => useRememberedChoice(blank, scope, "agent"))
    expect(rememberedView(scope, "agent")).toBeNull()
    hook.unmount()
    const stored = rememberedView(scope, "agent")
    expect(
      await openingThread({
        threads: [{ id: "pending", title: "", updated_at: ago(1000) }],
        choice: stored?.choice ?? null,
        leftAt: stored?.leftAt,
        now,
        waiting: async () => true,
      })
    ).toBe("pending")
  })
  it("does not refresh a hidden tab's timestamp on late pagehide or cleanup", () => {
    vi.useFakeTimers({ toFake: ["Date"] })
    vi.setSystemTime(now)
    let visibility: DocumentVisibilityState = "visible"
    const original = Object.getOwnPropertyDescriptor(
      document,
      "visibilityState"
    )
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => visibility,
    })
    const hook = renderHook(() =>
      useRememberedChoice(
        { ...initialPrimaryChat(), thread: "old" },
        scope,
        "agent"
      )
    )
    vi.setSystemTime(now + 1000)
    visibility = "hidden"
    act(() => document.dispatchEvent(new Event("visibilitychange")))
    vi.setSystemTime(now + 10 * FRESH_CONVERSATION_MS)
    act(() => window.dispatchEvent(new Event("pagehide")))
    hook.unmount()
    expect(rememberedView(scope, "agent")?.leftAt).toBe(now + 1000)
    if (original) Object.defineProperty(document, "visibilityState", original)
    else
      delete (document as unknown as { visibilityState?: string })
        .visibilityState
  })
})
