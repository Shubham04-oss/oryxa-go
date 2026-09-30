// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import type { Mock } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import {
  AgentTimeline,
  groupToolActivity,
  toolGroupState,
} from "./agent-timeline"
import type { ConfirmationAnswer, Item } from "@/lib/threads/client"
import { buildTimeline } from "@/lib/threads/timeline"
import type { ToolEntry } from "@/lib/threads/timeline"

vi.mock("thinking-orbs", () => ({
  ThinkingOrb: ({ state, paused }: { state: string; paused?: boolean }) => (
    <canvas
      data-orb-state={state}
      data-orb-paused={paused ? "true" : "false"}
      aria-hidden="true"
    />
  ),
}))

afterEach(cleanup)

const agent = "Leo"

function timeline(
  items: Item[],
  {
    running = false,
    ended,
    live = [],
    actionable = [],
    submitted = {},
    onAnswer = vi.fn<(answers: ConfirmationAnswer[]) => void>(),
  }: {
    running?: boolean
    ended?: boolean
    live?: Item[]
    actionable?: string[]
    submitted?: Record<string, boolean>
    onAnswer?: Mock<(answers: ConfirmationAnswer[]) => void>
  } = {}
) {
  const result = render(
    <div role="log">
      <AgentTimeline
        entries={buildTimeline(items, live)}
        running={running}
        ended={ended}
        agentName={agent}
        actionable={new Set(actionable)}
        submitted={submitted}
        answering={false}
        onAnswer={onAnswer}
        organizationId="org_1"
        threadId="th_1"
      />
    </div>
  )
  return { ...result, onAnswer, log: screen.getByRole("log") }
}

const confirmation = (callId: string, tool: string): Item => ({
  type: "confirmation",
  id: `ev_${callId}`,
  call_id: callId,
  tool_call_id: `tool_${callId}`,
  tool,
  arguments: { to: "sam@example.com" },
  status: "pending",
})

describe("AgentTimeline", () => {
  it("renders the member's words as a bubble and the agent's as Markdown", () => {
    const { log } = timeline([
      {
        type: "message",
        id: "ev_1",
        author: "member",
        text: "Plan the **launch**",
      },
      {
        type: "message",
        id: "ev_2",
        author: agent,
        text: "Here is **the plan**: [the brief](https://example.com/brief).\n\n<b>raw</b><script>alert(1)</script>\n\n![chart](https://tracker.example/pixel.png)",
      },
    ])

    const member = log.querySelector('[data-from="member"]')
    // The member's text is shown as typed, never as Markdown.
    expect(member?.textContent).toBe("Plan the **launch**")
    const answer = log.querySelector('[data-from="agent"]') as HTMLElement
    expect(within(answer).getByText("the plan").tagName).toBe("STRONG")
    const link = within(answer).getByRole("link", { name: "the brief" })
    expect(link.getAttribute("href")).toBe("https://example.com/brief")
    expect(link.getAttribute("target")).toBe("_blank")
    expect(link.getAttribute("rel")).toBe("noopener noreferrer")
    // No HTML from the answer, and no image fetched on its behalf.
    expect(answer.querySelector("b, script, img")).toBeNull()
    expect(within(answer).getByRole("link", { name: "chart" })).toBeTruthy()
    // The primary agent's own answers carry no author label.
    expect(within(answer).queryByText(agent)).toBeNull()
  })

  it("labels a sub-agent's message with its name", () => {
    const { log } = timeline([
      { type: "message", id: "ev_1", author: "researcher", text: "Found it." },
    ])
    expect(within(log).getByText("researcher")).toBeTruthy()
  })

  it("streams a draft, and marks one a stopped run left behind as interrupted", () => {
    const draft: Item[] = [
      { type: "run_started", run_id: "run_1" },
      { type: "delta", author: agent, text: "Work" },
      { type: "delta", author: agent, text: "ing on it" },
    ]
    const streaming = timeline(draft, { running: true })
    const bubble = streaming.log.querySelector('[data-from="agent"]')
    expect(bubble?.getAttribute("data-state")).toBe("streaming")
    expect(bubble?.textContent).toContain("Working on it")
    expect(within(streaming.log).queryByText("Interrupted")).toBeNull()
    cleanup()

    // The same transcript once nothing runs: no end marker ever came.
    const stopped = timeline(draft, { running: false })
    expect(
      stopped.log
        .querySelector('[data-from="agent"]')
        ?.getAttribute("data-state")
    ).toBe("interrupted")
    expect(within(stopped.log).getByText("Interrupted")).toBeTruthy()
    cleanup()

    // A run that failed mid-answer ends its draft too.
    const failed = timeline(
      [
        ...draft,
        {
          type: "run_failed",
          run_id: "run_1",
          error: "The run was cancelled.",
        },
      ],
      { running: false }
    )
    expect(within(failed.log).getByText("Interrupted")).toBeTruthy()
    expect(within(failed.log).getByText("The run was cancelled.")).toBeTruthy()
  })

  it("shows a turn's calls in one inline row across agent messages", () => {
    const items: Item[] = [
      { type: "tool_call", id: "ev_1", call_id: "c1", tool: "web_search" },
      { type: "tool_result", id: "ev_2", call_id: "c1", tool: "web_search" },
      { type: "tool_call", id: "ev_3", call_id: "c2", tool: "read_file" },
      { type: "message", id: "ev_4", author: agent, text: "Reading." },
      { type: "tool_call", id: "ev_5", call_id: "c3", tool: "send_email" },
      {
        type: "tool_result",
        id: "ev_6",
        call_id: "c3",
        tool: "send_email",
        failed: true,
        error: "mailbox full",
      },
    ]
    expect(
      groupToolActivity(buildTimeline(items)).map((entry) => entry.kind)
    ).toEqual(["tools", "message"])
    const { container } = timeline(items, { running: true })
    const rows = container.querySelectorAll(".workspace-agent-tools")
    expect(rows).toHaveLength(1)
    expect(rows[0].textContent).toContain("3 tool calls")
    expect(rows[0].textContent).toContain("Running")
    expect(rows[0].getAttribute("data-state")).toBe("running")
    expect(
      rows[0].querySelector("canvas")?.getAttribute("data-orb-state")
    ).toBe("searching")
    expect(rows[0].querySelector("summary, ol, button, svg")).toBeNull()
    expect(
      rows[0]
        .querySelector(".workspace-agent-tools-status")
        ?.getAttribute("title")
    ).toBe("mailbox full")
  })

  it.each([1, 2])(
    "shows %i recovered errors as a muted note while a matching retry leaves the group Done",
    (count) => {
      const items: Item[] = []
      for (let index = 0; index <= count; index += 1) {
        items.push(
          {
            type: "tool_call",
            call_id: `c${index}`,
            tool: "knowledge_search",
            author: agent,
            arguments: { query: "launch" },
          },
          {
            type: "tool_result",
            call_id: `c${index}`,
            tool: "knowledge_search",
            failed: index < count,
            error: index < count ? "search timed out" : undefined,
          }
        )
      }
      const entries = buildTimeline(items)
      expect(
        entries.filter(
          (entry) => entry.kind === "tool" && entry.state === "failed"
        )
      ).toHaveLength(count)
      const { container, log } = timeline(items, { ended: true })
      const row = container.querySelector(".workspace-agent-tools")
      expect(row?.getAttribute("data-state")).toBe("done")
      expect(
        row?.querySelector(".workspace-agent-tools-status")?.textContent
      ).toBe("Done")
      expect(within(log).queryByText("Failed")).toBeNull()
      const note = within(log).getByText(
        count === 1 ? "Recovered tool error" : "2 recovered tool errors"
      )
      expect(note.className).toBe("workspace-agent-tools-recovered")
      expect(note.getAttribute("title")).toContain("search timed out")
      expect(row?.getAttribute("aria-label")).toContain("Recovered tool error")
      expect(row?.querySelector("summary, ol, button")).toBeNull()
    }
  )

  it.each(["history", "live", "reconciled"] as const)(
    "shows a proven retry in a twelve-call %s group as Done with its recovered note",
    (source) => {
      const items: Item[] = Array.from({ length: 12 }, (_, index) => [
        {
          type: "tool_call" as const,
          id: `call_${index}`,
          call_id: `c${index}`,
          author: agent,
          tool: "knowledge_search",
          arguments: { query: index < 2 ? "launch" : `task ${index}` },
        },
        {
          type: "tool_result" as const,
          id: `result_${index}`,
          call_id: `c${index}`,
          tool: "knowledge_search",
          failed: index === 0,
          error: index === 0 ? "search timed out" : undefined,
        },
      ]).flat()
      const history =
        source === "live"
          ? []
          : source === "reconciled"
            ? items.slice(0, 2)
            : items
      const live = source === "history" ? [] : items
      const { container, log } = timeline(history, { live, ended: true })
      const rows = container.querySelectorAll(".workspace-agent-tools")
      expect(rows).toHaveLength(1)
      expect(rows[0].getAttribute("data-state")).toBe("done")
      expect(rows[0].textContent).toContain("12 tool calls")
      expect(
        within(log).getByText("Recovered tool error").getAttribute("title")
      ).toBe("search timed out")
      expect(within(log).queryByText("Failed")).toBeNull()
    }
  )

  it("keeps an unrecovered failure red beside a recovered note and a good answer", () => {
    const items: Item[] = [
      {
        type: "tool_call",
        call_id: "c1",
        tool: "knowledge_search",
        author: agent,
        arguments: { query: "launch" },
      },
      {
        type: "tool_result",
        call_id: "c1",
        failed: true,
        error: "search timed out",
      },
      {
        type: "tool_call",
        call_id: "c2",
        tool: "knowledge_search",
        author: agent,
        arguments: { query: "launch" },
      },
      { type: "tool_result", call_id: "c2" },
      {
        type: "tool_call",
        call_id: "c3",
        tool: "knowledge_search",
        author: agent,
        arguments: { query: "another task" },
      },
      {
        type: "tool_result",
        call_id: "c3",
        failed: true,
        error: "access refused",
      },
      { type: "message", author: agent, text: "The launch is 14 October." },
    ]
    const { container, log } = timeline(items, { ended: true })
    const row = container.querySelector(".workspace-agent-tools")
    expect(row?.getAttribute("data-state")).toBe("failed")
    expect(within(log).getByText("Recovered tool error")).toBeTruthy()
    expect(
      row?.querySelector(".workspace-agent-tools-status")?.textContent
    ).toBe("Failed")
    expect(
      row?.querySelector(".workspace-agent-tools-status")?.getAttribute("title")
    ).toBe("access refused")
    expect(within(log).getByText("The launch is 14 October.")).toBeTruthy()
  })

  it.each(["history", "live", "reconciled"] as const)(
    "keeps twelve successful calls Done in %s and preserves an actual failure beside a good answer",
    (source) => {
      const calls: Item[] = Array.from({ length: 12 }, (_, index) => [
        {
          type: "tool_call" as const,
          id: `call_${index}`,
          call_id: `c${index}`,
          tool: "knowledge_search",
        },
        {
          type: "tool_result" as const,
          id: `result_${index}`,
          call_id: `c${index}`,
          tool: "knowledge_search",
        },
      ]).flat()
      const answer: Item = {
        type: "message",
        id: "answer",
        author: agent,
        text: "The launch is 14 October.",
      }
      for (const failed of [false, true]) {
        const items: Item[] = [
          ...calls.slice(0, -1),
          {
            ...calls[calls.length - 1],
            failed,
            error: failed ? "access refused" : undefined,
          },
          answer,
        ]
        const history = source === "live" ? [] : items
        const live = source === "history" ? [] : items
        const { container, log } = timeline(history, { live, ended: true })
        const rows = container.querySelectorAll(".workspace-agent-tools")
        expect(rows).toHaveLength(1)
        expect(rows[0].textContent).toContain("12 tool calls")
        expect(rows[0].getAttribute("data-state")).toBe(
          failed ? "failed" : "done"
        )
        expect(
          rows[0].querySelector(".workspace-agent-tools-status")?.textContent
        ).toBe(failed ? "Failed" : "Done")
        expect(within(log).getAllByText(answer.text!)).toHaveLength(1)
        if (failed) {
          expect(
            rows[0]
              .querySelector(".workspace-agent-tools-status")
              ?.getAttribute("title")
          ).toBe("access refused")
        } else {
          expect(within(log).queryByText("Failed")).toBeNull()
        }
        cleanup()
      }
    }
  )

  it("reads a call a stopped run left behind as interrupted, with no moving orb", () => {
    // Stopped mid-call: the run's end marker never came.
    const stopped: Item[] = [
      { type: "message", id: "ev_1", author: "member", text: "Look it up" },
      { type: "run_started", run_id: "run_1" },
      { type: "tool_call", id: "ev_2", call_id: "c1", tool: "web_search" },
    ]
    const streaming = timeline(stopped, { running: true })
    const live = streaming.container.querySelector(".workspace-agent-tools")
    expect(live?.getAttribute("data-state")).toBe("running")
    expect(live?.textContent).toContain("Running")
    expect(live?.querySelector("canvas")?.getAttribute("data-orb-paused")).toBe(
      "false"
    )
    cleanup()

    const after = timeline(stopped, { running: false })
    const row = after.container.querySelector(".workspace-agent-tools")
    expect(row?.getAttribute("data-state")).toBe("interrupted")
    expect(
      row?.querySelector(".workspace-agent-tools-status")?.textContent
    ).toBe("Interrupted")
    expect(row?.textContent).not.toContain("Running")
    expect(row?.querySelector("canvas")?.getAttribute("data-orb-paused")).toBe(
      "true"
    )
    expect(row?.getAttribute("aria-label")).toBe("Web search · Interrupted")
    // Still one quiet row that never expands into a list.
    expect(
      after.container.querySelectorAll(".workspace-agent-tools")
    ).toHaveLength(1)
    expect(row?.querySelector("summary, ol, button")).toBeNull()
  })

  it("leaves a call and a draft as they were while the run may only be out of sight", () => {
    const items: Item[] = [
      { type: "run_started", run_id: "run_1" },
      { type: "tool_call", id: "ev_1", call_id: "c1", tool: "web_search" },
      { type: "delta", author: agent, text: "Looking" },
    ]
    // Nothing streams here (the socket dropped, or a resume is on its way),
    // but nothing says the run is over.
    const alive = timeline(items, { running: false, ended: false })
    const row = alive.container.querySelector(".workspace-agent-tools")
    expect(row?.getAttribute("data-state")).toBe("running")
    const draft = alive.log.querySelector('[data-from="agent"]')
    expect(draft?.getAttribute("data-state")).toBe("streaming")
    expect(within(alive.log).queryByText("Interrupted")).toBeNull()
    cleanup()

    // Told the run is over, the same rows read interrupted.
    const over = timeline(items, { running: false, ended: true })
    expect(
      over.container
        .querySelector(".workspace-agent-tools")
        ?.getAttribute("data-state")
    ).toBe("interrupted")
    expect(within(over.log).getAllByText("Interrupted").length).toBeGreaterThan(
      0
    )
  })

  it("reads a call whose run ended without its result as interrupted, even while another run streams", () => {
    const { container } = timeline(
      [
        { type: "run_started", run_id: "run_1" },
        { type: "tool_call", id: "ev_1", call_id: "c1", tool: "web_search" },
        {
          type: "run_failed",
          run_id: "run_1",
          error: "The run was cancelled.",
        },
        { type: "run_started", run_id: "run_2" },
      ],
      { running: true }
    )
    const row = container.querySelector(".workspace-agent-tools")
    expect(row?.getAttribute("data-state")).toBe("interrupted")
    expect(row?.querySelector("canvas")?.getAttribute("data-orb-paused")).toBe(
      "true"
    )
    // A failed run is one muted error line under it.
    expect(container.querySelector(".workspace-agent-error")?.textContent).toBe(
      "The run was cancelled."
    )
  })

  it("keeps a stopped run's call interrupted while the next run streams", () => {
    const { container } = timeline(
      [
        { type: "message", id: "ev_1", author: "member", text: "Look it up" },
        { type: "run_started", run_id: "run_1" },
        { type: "tool_call", id: "ev_2", call_id: "c1", tool: "web_search" },
        // The member stopped it, and asked something else.
        { type: "message", id: "ev_3", author: "member", text: "Never mind" },
        { type: "run_started", run_id: "run_2" },
        { type: "tool_call", id: "ev_4", call_id: "c2", tool: "read_file" },
      ],
      { running: true }
    )
    const rows = container.querySelectorAll(".workspace-agent-tools")
    expect(rows).toHaveLength(2)
    expect(rows[0].getAttribute("data-state")).toBe("interrupted")
    expect(
      rows[0].querySelector("canvas")?.getAttribute("data-orb-paused")
    ).toBe("true")
    expect(rows[1].getAttribute("data-state")).toBe("running")
    expect(
      rows[1].querySelector("canvas")?.getAttribute("data-orb-paused")
    ).toBe("false")
  })

  it("folds finished and interrupted calls into one row that says the run was cut short", () => {
    const { container } = timeline(
      [
        { type: "tool_call", id: "ev_1", call_id: "c1", tool: "web_search" },
        { type: "tool_result", id: "ev_2", call_id: "c1", tool: "web_search" },
        { type: "tool_call", id: "ev_3", call_id: "c2", tool: "read_file" },
      ],
      { running: false }
    )
    const rows = container.querySelectorAll(".workspace-agent-tools")
    expect(rows).toHaveLength(1)
    expect(rows[0].textContent).toContain("2 tool calls")
    expect(rows[0].getAttribute("data-state")).toBe("interrupted")
    expect(rows[0].getAttribute("aria-label")).toBe(
      "Web search · Done; Read file · Interrupted"
    )
  })

  it("leaves a call that waits on the member as it was when nothing streams", () => {
    const { container } = timeline(
      [
        { type: "tool_call", id: "ev_1", call_id: "c1", tool: "send_email" },
        {
          type: "confirmation",
          id: "ev_2",
          call_id: "confirm_1",
          tool_call_id: "c1",
          tool: "send_email",
          status: "pending",
        },
      ],
      { running: false, actionable: ["confirm_1"] }
    )
    const row = container.querySelector(".workspace-agent-tools")
    expect(row?.getAttribute("data-state")).toBe("waiting_approval")
    expect(row?.textContent).toContain("Waiting for you")
  })

  it("uses compact app marks in the same row and starts a new row next turn", () => {
    const { container } = timeline([
      { type: "message", id: "ev_1", author: "member", text: "Check my apps" },
      {
        type: "tool_call",
        id: "ev_2",
        call_id: "c1",
        tool: "apps_search_tools",
        arguments: { toolkit: "gmail" },
      },
      {
        type: "tool_result",
        id: "ev_3",
        call_id: "c1",
        tool: "apps_search_tools",
      },
      {
        type: "tool_call",
        id: "ev_4",
        call_id: "c2",
        tool: "apps_run_read_tool",
        arguments: { tool: "GOOGLE_DRIVE_LIST_FILES", arguments: {} },
      },
      {
        type: "tool_result",
        id: "ev_5",
        call_id: "c2",
        tool: "apps_run_read_tool",
      },
      { type: "message", id: "ev_6", author: "member", text: "Now send it" },
      {
        type: "tool_call",
        id: "ev_7",
        call_id: "c3",
        tool: "apps_run_write_tool",
        arguments: { tool: "SLACK_POST_MESSAGE", arguments: {} },
      },
      {
        type: "tool_result",
        id: "ev_8",
        call_id: "c3",
        tool: "apps_run_write_tool",
        failed: true,
        error: "no access",
      },
    ])
    const rows = container.querySelectorAll(".workspace-agent-tools")
    expect(rows).toHaveLength(2)
    expect(rows[0].textContent).toContain("2 tool calls")
    expect(
      within(rows[0] as HTMLElement).getByRole("img", { name: "Gmail" })
    ).toBeTruthy()
    expect(
      within(rows[0] as HTMLElement).getByRole("img", { name: "Google Drive" })
    ).toBeTruthy()
    expect(
      rows[0].querySelector("canvas")?.getAttribute("data-orb-paused")
    ).toBe("true")
    expect(rows[1].textContent).toContain("Slack · Make change")
    expect(
      within(rows[1] as HTMLElement).getByRole("img", { name: "Slack" })
        .textContent
    ).toBe("S")
    expect(rows[1].textContent).toContain("Failed")
    expect(
      rows[1]
        .querySelector(".workspace-agent-tools-status")
        ?.getAttribute("title")
    ).toBe("no access")
  })

  it("does not invent an app icon when a generic apps call has no app", () => {
    const { container } = timeline([
      {
        type: "tool_call",
        id: "ev_1",
        call_id: "c1",
        tool: "apps_connections",
      },
    ])
    expect(container.querySelector(".workspace-agent-tool-app")).toBeNull()
  })

  it("offers a copy action for fenced code without flattening the answer", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    })
    const { log } = timeline([
      {
        type: "message",
        id: "ev_1",
        author: agent,
        text: "## Example\n\n```ts\nconst answer = 42\n```",
      },
    ])
    expect(within(log).getByRole("heading", { name: "Example" }).tagName).toBe(
      "H2"
    )
    fireEvent.click(within(log).getByRole("button", { name: "Copy code" }))
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith("const answer = 42\n")
    )
  })

  it("wraps prose in text fences while preserving code formatting", () => {
    const { log } = timeline([
      {
        type: "message",
        id: "ev_1",
        author: agent,
        text: "```text\nA long operating instruction for the specialist.\n```\n\n```ts\nconst ok = true\n```",
      },
    ])
    const blocks = log.querySelectorAll(".workspace-agent-code-block pre")
    expect(blocks).toHaveLength(2)
    expect(blocks[0].classList.contains("workspace-agent-code-wrap")).toBe(true)
    expect(blocks[1].classList.contains("workspace-agent-code-wrap")).toBe(
      false
    )
  })

  it("shows the files runs kept: images as thumbnails, other files as links", () => {
    const { log } = timeline([
      { type: "artifact", id: "ev_1", artifact: "chart.png", version: 2 },
      { type: "artifact", id: "ev_2", artifact: "user:report.pdf", version: 1 },
    ])
    const image = within(log).getByRole("img", { name: "chart.png" })
    expect(image.getAttribute("src")).toBe(
      "/v1/organizations/org_1/threads/th_1/artifacts/chart.png?version=2"
    )
    const file = within(log).getByRole("link", { name: "report.pdf" })
    expect(file.getAttribute("href")).toBe(
      "/v1/organizations/org_1/threads/th_1/artifacts/user%3Areport.pdf?version=1"
    )
    expect(file.getAttribute("download")).toBe("report.pdf")
  })

  it("shows a failed run as a quiet error line, and a batch from apps as a notice", () => {
    const { log } = timeline([
      {
        type: "run_failed",
        run_id: "run_1",
        error: "Your primary agent isn't available right now.",
        code: "agents_unavailable",
      },
      { type: "message", id: "ev_1", author: "apps", text: "1\n2\n3\n4\n5\n6" },
    ])
    expect(
      within(log).getByText("Your primary agent isn't available right now.")
        .className
    ).toBe("workspace-agent-error")
    expect(within(log).getByText("From your apps")).toBeTruthy()
    expect(within(log).queryByText(/6/)).toBeNull()
    fireEvent.click(within(log).getByRole("button", { name: "Show more" }))
    expect(within(log).getByText(/6/)).toBeTruthy()
  })

  it("never shows the Durable Object's request to refresh the session", () => {
    const { log } = timeline([
      { type: "message", id: "ev_1", author: "member", text: "Hi" },
      {
        type: "run_failed",
        error: "Your session has ended.",
        code: "session_expired",
      },
    ])
    expect(within(log).queryByText("Your session has ended.")).toBeNull()
  })
})

describe("toolGroupState", () => {
  const call = (state: ToolEntry["state"]): ToolEntry => ({
    kind: "tool",
    key: `tool:${state}`,
    callId: state,
    tool: "web_search",
    state,
  })

  it("shows what still needs attention first, an interrupted call before an old failure", () => {
    expect(toolGroupState([call("done"), call("interrupted")])).toBe(
      "interrupted"
    )
    expect(toolGroupState([call("failed"), call("interrupted")])).toBe(
      "interrupted"
    )
    expect(toolGroupState([call("interrupted"), call("running")])).toBe(
      "running"
    )
    expect(
      toolGroupState([call("interrupted"), call("waiting_approval")])
    ).toBe("waiting_approval")
    expect(toolGroupState([call("done"), call("failed")])).toBe("failed")
  })

  it.each(["waiting_approval", "running", "interrupted"] as const)(
    "keeps %s ahead of a recovered error",
    (state) => {
      expect(
        toolGroupState([
          { ...call("failed"), recovered: true },
          call("done"),
          call(state),
        ])
      ).toBe(state)
    }
  )
})

describe("confirmations", () => {
  it("answers a single confirmation at once", () => {
    const { log, onAnswer } = timeline([confirmation("call_1", "send_email")], {
      actionable: ["call_1"],
    })
    const card = within(log).getByRole("group", { name: "Confirm Send email" })
    expect(within(card).getByText(/It only runs if you approve/)).toBeTruthy()
    fireEvent.click(within(card).getByRole("button", { name: "Approve" }))
    expect(onAnswer).toHaveBeenCalledExactlyOnceWith([
      { id: "call_1", confirmed: true },
    ])
  })

  it("collects every waiting answer and sends them together", () => {
    const { log, onAnswer } = timeline(
      [
        confirmation("call_1", "send_email"),
        confirmation("call_2", "delete_file"),
      ],
      { actionable: ["call_1", "call_2"] }
    )
    const send = within(log).getByRole("button", { name: "Send answers" })
    expect(send.hasAttribute("disabled")).toBe(true)

    const first = within(log).getByRole("group", { name: "Confirm Send email" })
    const second = within(log).getByRole("group", {
      name: "Confirm Delete file",
    })
    fireEvent.click(within(first).getByRole("button", { name: "Approve" }))
    expect(onAnswer).not.toHaveBeenCalled()
    expect(send.hasAttribute("disabled")).toBe(true)
    fireEvent.click(within(second).getByRole("button", { name: "Decline" }))
    expect(
      within(second)
        .getByRole("button", { name: "Decline" })
        .getAttribute("aria-pressed")
    ).toBe("true")

    fireEvent.click(send)
    expect(onAnswer).toHaveBeenCalledExactlyOnceWith([
      { id: "call_1", confirmed: true },
      { id: "call_2", confirmed: false },
    ])
  })

  it("shows how a confirmation was answered instead of asking again", () => {
    const { log } = timeline(
      [
        confirmation("call_1", "send_email"),
        {
          type: "confirmation",
          call_id: "call_2",
          tool: "read_file",
          status: "declined",
        },
      ],
      { actionable: ["call_1"], submitted: { call_1: true } }
    )
    expect(within(log).queryByRole("button", { name: "Approve" })).toBeNull()
    expect(within(log).getByText("Approved")).toBeTruthy()
    expect(within(log).getByText("Declined")).toBeTruthy()
  })
})
