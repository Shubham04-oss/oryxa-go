// @vitest-environment jsdom

import { describe, expect, it } from "vitest"
import { renderHook } from "@testing-library/react"
import { useStableLayout } from "./requests-view-layout"
import type { RequestGroup } from "./requests-view-layout"
import { slackRequest } from "./request-card-fixtures"
import type { AgentRequest } from "@/lib/agents/agent-requests"

const request = (id: string, status: AgentRequest["status"] = "pending") =>
  slackRequest({ id, status })

/** What waits on the member is pending. */
const groupOf = (item: AgentRequest): RequestGroup =>
  item.status === "pending" ? "waiting" : "earlier"

function layoutOf(initial: AgentRequest[]) {
  const hook = renderHook(
    ({ requests }) => useStableLayout(requests, groupOf),
    { initialProps: { requests: initial } }
  )
  const ids = () => ({
    waiting: hook.result.current.waiting.map((item) => item.id),
    earlier: hook.result.current.earlier.map((item) => item.id),
  })
  return { ...hook, ids }
}

describe("where a request sits while the list is open", () => {
  it("starts in the order Go gave, waiting ones apart from the rest", () => {
    const { ids } = layoutOf([
      request("a"),
      request("b", "completed"),
      request("c"),
      request("d", "approved"),
    ])

    expect(ids()).toEqual({ waiting: ["a", "c"], earlier: ["b", "d"] })
  })

  it("leaves a request where it was once it stops waiting, whatever order Go lists it in", () => {
    const { ids, rerender } = layoutOf([
      request("a"),
      request("b"),
      request("c", "completed"),
    ])

    // b is answered: Go lists it first now, among those that do not wait.
    rerender({
      requests: [
        request("b", "approved"),
        request("a"),
        request("c", "completed"),
      ],
    })

    expect(ids()).toEqual({ waiting: ["a", "b"], earlier: ["c"] })
  })

  it("puts requests that arrive first, the newest of them on top", () => {
    const { ids, rerender } = layoutOf([
      request("a"),
      request("c", "completed"),
    ])

    rerender({
      requests: [
        request("n2"),
        request("n1", "completed"),
        request("a"),
        request("c", "completed"),
      ],
    })

    expect(ids()).toEqual({ waiting: ["n2", "a"], earlier: ["n1", "c"] })
  })

  it("moves a request that starts waiting to the top of those waiting", () => {
    const { ids, rerender } = layoutOf([
      request("a"),
      request("b"),
      request("c", "approved"),
    ])

    rerender({
      requests: [request("a"), request("b"), request("c", "pending")],
    })

    expect(ids()).toEqual({ waiting: ["c", "a", "b"], earlier: [] })
  })

  it("does not lose a request that leaves and comes back", () => {
    const { ids, rerender } = layoutOf([request("a"), request("b")])

    rerender({ requests: [request("b")] })
    expect(ids()).toEqual({ waiting: ["b"], earlier: [] })
    rerender({ requests: [request("a"), request("b")] })

    expect(ids()).toEqual({ waiting: ["a", "b"], earlier: [] })
  })

  it("begins again from Go's order when the list is left and opened afresh", () => {
    const first = layoutOf([request("a"), request("b", "approved")])
    first.rerender({
      requests: [request("b", "approved"), request("a", "approved")],
    })
    first.unmount()

    const again = layoutOf([request("b", "approved"), request("a", "approved")])

    expect(again.ids()).toEqual({ waiting: [], earlier: ["b", "a"] })
  })
})
