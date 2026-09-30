// @vitest-environment jsdom
import { act, renderHook } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  FRESH_CONVERSATION_MS,
  PRIMARY_DRAFT,
  chooseThread,
  claimPrimaryChatRequest,
  firstMessage,
  initialPrimaryChat,
  openingThread,
  primaryChatReducer,
  publishPrimaryChatActivity,
  rememberChoice,
  rememberedChoice,
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

  it("puts a message no conversation could be made for back in an empty composer, and never over one", () => {
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

    const empty = failed("")
    expect(empty.drafts[PRIMARY_DRAFT]).toBe("From Home")
    expect(empty.unsent).toBeNull()
    // A blank chat, not the conversation it was on.
    expect(states(empty)).toEqual({ thread: null, shown: null, waiting: [] })
    expect(empty.notice).toBe("Couldn't start a conversation.")

    const busy = failed("Half a thought")
    expect(busy.drafts[PRIMARY_DRAFT]).toBe("Half a thought")
    expect(busy.unsent).toBe("From Home")
  })

  it("goes on to what waited behind a message that failed", () => {
    const chat = run([
      ask(1, { kind: "new", message: "First" }),
      ask(2, { kind: "new", message: "Second" }),
      { type: "failed", id: 1, notice: "Couldn't start a conversation." },
    ])
    expect(firstMessage(chat)).toMatchObject({ id: 2, text: "Second" })
    expect(chat.drafts[PRIMARY_DRAFT]).toBe("First")
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
    const reset = primaryChatReducer(first, { type: "reset" })
    expect(reset.thread).toBeUndefined()
    expect(reset.tasks).toEqual([])
    expect(reset.drafts).toEqual({})
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

    // The rules' decision is not the member's.
    const decided = run([{ type: "decide", thread: "t1" }])
    rerender({ chat: decided })
    expect(remembered()).toBeNull()

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
