// A stand-in for the Agents SDK's `useAgentChat`, for the chat components'
// tests: each mount gets a chat the test drives as its Durable Object would
// (what the socket shows, the items a turn streams, the turn's end), beside
// the socket from src/test/room-socket.ts. Use it with
// `vi.mock("@cloudflare/ai-chat/react", async () => (await import("@/test/agent-chat")).aiChatReact)`.
import { act } from "@testing-library/react"
import { useEffect, useReducer, useRef } from "react"
import { vi } from "vitest"
import type { ThreadChatMessage } from "@/lib/threads/chat-messages"
import type { Item } from "@/lib/threads/client"

type FinishResult = {
  isAbort: boolean
  isDisconnect: boolean
  isError: boolean
}

type Options = {
  /** The socket from `useAgent`: the stand-in's, which knows its object's name. */
  agent: { options?: { name?: string } }
  messages?: ThreadChatMessage[]
  getInitialMessages?: unknown
  onData?: (part: { type: "data-oryxa"; data: Item }) => void
  onError?: (error: Error) => void
  onFinish?: (result: FinishResult) => void
}

export type FakeChatStatus = "ready" | "submitted" | "streaming" | "error"

export class FakeAgentChat {
  options: Options
  messages: ThreadChatMessage[]
  status: FakeChatStatus = "ready"
  isServerStreaming = false
  rerender?: () => void
  readonly sendMessage = vi.fn(
    async (_message?: unknown, _options?: unknown) => undefined
  )
  readonly regenerate = vi.fn(async (_options?: unknown) => undefined)
  readonly stop = vi.fn(async () => undefined)
  /** As the SDK's: the page's transcript, which it also sends the object. */
  readonly setMessages = vi.fn((messages: ThreadChatMessage[]) => {
    this.messages = messages
    this.rerender?.()
  })
  readonly clearHistory = vi.fn(() => {
    this.messages = []
    this.rerender?.()
  })

  constructor(options: Options) {
    this.options = options
    this.messages = options.messages ?? []
  }

  /** The object's name, from the socket the chat was given. */
  get name(): string {
    return this.options.agent.options?.name ?? ""
  }

  /** What the object sends this socket: the transcript, or a turn's progress. */
  update(
    next: Partial<
      Pick<FakeAgentChat, "messages" | "status" | "isServerStreaming">
    >
  ) {
    act(() => {
      Object.assign(this, next)
      this.rerender?.()
    })
  }

  /** An item streams in a turn this socket sees. */
  data(item: Item) {
    act(() => this.options.onData?.({ type: "data-oryxa", data: item }))
  }

  /** This page's own turn ends. */
  finish(result: Partial<FinishResult> = {}) {
    act(() =>
      this.options.onFinish?.({
        isAbort: false,
        isDisconnect: false,
        isError: false,
        ...result,
      })
    )
  }
}

export const agentChats = {
  all: [] as FakeAgentChat[],
  /** The latest chat on the object with this name. */
  named(name: string): FakeAgentChat {
    const chat = this.all.filter((candidate) => candidate.name === name).at(-1)
    if (!chat) throw new Error(`no chat on ${name}`)
    return chat
  },
  reset() {
    this.all.length = 0
  },
}

function useAgentChat(options: Options) {
  const held = useRef<FakeAgentChat>(undefined)
  if (!held.current) {
    held.current = new FakeAgentChat(options)
    agentChats.all.push(held.current)
  }
  const chat = held.current
  chat.options = options
  const [, rerender] = useReducer((count: number) => count + 1, 0)
  useEffect(() => {
    chat.rerender = rerender
    return () => {
      chat.rerender = undefined
    }
  }, [chat])
  return {
    messages: chat.messages,
    status: chat.status,
    isServerStreaming: chat.isServerStreaming,
    sendMessage: chat.sendMessage,
    regenerate: chat.regenerate,
    stop: chat.stop,
    setMessages: chat.setMessages,
    clearHistory: chat.clearHistory,
  }
}

export const aiChatReact = { useAgentChat }
