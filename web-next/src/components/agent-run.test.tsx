// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { AgentTimeline } from "./agent-timeline"
import {
  LookingAt,
  formatElapsed,
  formatTokens,
  screenContextLabel,
} from "./agent-run"
import type { ConfirmationAnswer, Item } from "@/lib/threads/client"
import { buildTimeline } from "@/lib/threads/timeline"

vi.mock("thinking-orbs", () => ({
  ThinkingOrb: ({ state }: { state: string }) => (
    <canvas data-orb-state={state} aria-hidden="true" />
  ),
}))

afterEach(cleanup)

function session(
  items: Item[],
  {
    running = false,
    ended = !running,
    actionable = [],
    onStop,
    onAnswer = vi.fn<(answers: ConfirmationAnswer[]) => void>(),
  }: {
    running?: boolean
    ended?: boolean
    actionable?: string[]
    onStop?: () => void
    onAnswer?: (answers: ConfirmationAnswer[]) => void
  } = {}
) {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <div role="log" data-variant="session">
        <AgentTimeline
          variant="session"
          entries={buildTimeline(items)}
          items={items}
          running={running}
          ended={ended}
          onStop={onStop}
          agentName="Leo"
          actionable={new Set(actionable)}
          submitted={{}}
          answering={false}
          onAnswer={onAnswer}
          organizationId="org_1"
          threadId="th_1"
        />
      </div>
    </QueryClientProvider>
  )
}

const task: Item = {
  type: "message",
  id: "ev_1",
  author: "member",
  text: "Check the reseller terms",
  at: "2026-10-15T09:41:00Z",
}

describe("the agent session", () => {
  it("shows the task as a line and the work as one run card under it", () => {
    session([
      task,
      { type: "run_started", run_id: "r1", at: "2026-10-15T09:41:00Z" },
      {
        type: "tool_call",
        id: "ev_2",
        call_id: "c1",
        tool: "knowledge_search",
        arguments: { query: "reseller agreement" },
      },
      {
        type: "tool_result",
        id: "ev_3",
        call_id: "c1",
        tool: "knowledge_search",
      },
      {
        type: "message",
        id: "ev_4",
        author: "Leo",
        text: "Clause **7.2** keeps data in Frankfurt.",
      },
      {
        type: "run_completed",
        run_id: "r1",
        at: "2026-10-15T09:41:14Z",
        usage: { input_tokens: 1100, output_tokens: 140, total_tokens: 1240 },
      },
    ])
    const task_ = screen.getByText("Check the reseller terms")
    expect(task_.closest(".agent-task")).toBeTruthy()
    expect(task_.closest(".agent-run")).toBeNull()

    const card = screen.getByRole("article", { name: "Run: Done" })
    expect(card.querySelector(".agent-run-status")?.textContent).toBe("Done")
    expect(within(card).getByText("14s · 1.2k tokens")).toBeTruthy()
    // The steps are folded once the run is over, and open on demand.
    const toggle = within(card).getByRole("button", { name: /1 step/ })
    expect(toggle.getAttribute("aria-expanded")).toBe("false")
    fireEvent.click(toggle)
    expect(toggle.getAttribute("aria-expanded")).toBe("true")
    expect(within(card).getByText("Knowledge search")).toBeTruthy()
    expect(within(card).getByText("reseller agreement")).toBeTruthy()
    // The answer is the card's last part.
    expect(within(card).getByText("7.2").tagName).toBe("STRONG")
  })

  it("works with the steps open, and offers Stop, while the run streams", () => {
    const onStop = vi.fn()
    session(
      [
        task,
        {
          type: "tool_call",
          id: "ev_2",
          call_id: "c1",
          tool: "web_search",
          arguments: { query: "VAT" },
        },
      ],
      { running: true, onStop }
    )
    const card = screen.getByRole("article", { name: "Run: Working" })
    expect(
      within(card)
        .getByRole("button", { name: /1 step/ })
        .getAttribute("aria-expanded")
    ).toBe("true")
    expect(within(card).getByText("Running")).toBeTruthy()
    fireEvent.click(within(card).getByRole("button", { name: "Stop" }))
    expect(onStop).toHaveBeenCalledOnce()
  })

  it("waits for the member with the approval inside the card", () => {
    const onAnswer = vi.fn<(answers: ConfirmationAnswer[]) => void>()
    session(
      [
        task,
        {
          type: "tool_call",
          id: "ev_2",
          call_id: "t1",
          tool: "send_email",
          arguments: { to: "sam@example.com" },
        },
        {
          type: "confirmation",
          id: "ev_3",
          call_id: "k1",
          tool_call_id: "t1",
          tool: "send_email",
          arguments: { to: "sam@example.com" },
          status: "pending",
        },
      ],
      { actionable: ["k1"], onAnswer }
    )
    const card = screen.getByRole("article", { name: "Run: Waiting for you" })
    fireEvent.click(within(card).getByRole("button", { name: "Approve" }))
    expect(onAnswer).toHaveBeenCalledWith([{ id: "k1", confirmed: true }])
  })

  it("shows a failed step's error, and a run that failed", () => {
    session([
      task,
      { type: "tool_call", id: "ev_2", call_id: "c1", tool: "read_file" },
      {
        type: "tool_result",
        id: "ev_3",
        call_id: "c1",
        tool: "read_file",
        failed: true,
        error: "No such file",
      },
      { type: "run_failed", run_id: "r1", error: "The model is unavailable." },
    ])
    const card = screen.getByRole("article", { name: "Run: Failed" })
    expect(within(card).getByText(/1 step · 1 failed/)).toBeTruthy()
    expect(within(card).getByText("No such file")).toBeTruthy()
    expect(within(card).getByText("The model is unavailable.")).toBeTruthy()
  })

  it("lists a file the run made as an output with Open and Download", () => {
    session([
      task,
      { type: "artifact", id: "ev_2", artifact: "plan.docx", version: 2 },
      { type: "message", id: "ev_3", author: "Leo", text: "Saved." },
    ])
    const card = screen.getByRole("article", { name: "Run: Done" })
    expect(within(card).getByText("plan.docx")).toBeTruthy()
    expect(within(card).getByText("Version 2 · Made in this chat")).toBeTruthy()
    expect(
      within(card).getByRole("link", { name: "Open plan.docx" })
    ).toBeTruthy()
    expect(
      within(card)
        .getByRole("link", { name: "Download plan.docx" })
        .getAttribute("download")
    ).toBe("plan.docx")
  })

  it("reads a task that got no answer as interrupted once its run is over", () => {
    session([task], { running: false, ended: true })
    expect(
      screen.getByRole("article", { name: "Run: Interrupted" })
    ).toBeTruthy()
  })
})

describe("formatting", () => {
  it("says how long and how much", () => {
    expect(formatElapsed(8_200)).toBe("8s")
    expect(formatElapsed(72_000)).toBe("1m 12s")
    expect(formatElapsed(3_840_000)).toBe("1h 4m")
    expect(
      formatTokens({ input_tokens: 0, output_tokens: 0, total_tokens: 940 })
    ).toBe("940 tokens")
    expect(
      formatTokens({ input_tokens: 0, output_tokens: 0, total_tokens: 12_400 })
    ).toBe("12k tokens")
  })
})

describe("LookingAt", () => {
  it("names what the page selects, or the page", () => {
    expect(
      screenContextLabel({
        view: "agents/specialist",
        selection: { kind: "specialist", id: "a1", name: "Pricing" },
        visible_items: [],
      })
    ).toBe("Pricing")
    expect(
      screenContextLabel({ view: "home/overview", visible_items: [] })
    ).toBe("My workspace")
    expect(screenContextLabel({ view: "team/people", visible_items: [] })).toBe(
      "People"
    )
  })

  it("shows the chip from the page's context", () => {
    render(
      <LookingAt
        getScreenContext={() => ({
          view: "team/teamspace",
          selection: { kind: "teamspace", id: "t1", name: "EU launch" },
          visible_items: [],
        })}
      />
    )
    expect(screen.getByText("Looking at EU launch")).toBeTruthy()
  })
})
