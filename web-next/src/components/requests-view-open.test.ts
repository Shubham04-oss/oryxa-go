// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, renderHook } from "@testing-library/react"
import {
  OPEN_REQUESTS_EVENT,
  openRequest,
  openRequests,
  requestsLink,
  requestsLinkOf,
  useRequestsEntry,
} from "./requests-view-open"

afterEach(() => {
  cleanup()
  window.history.replaceState({}, "", "/")
})

describe("opening Requests from anywhere", () => {
  it("tells the page which request to open, or that none is named", () => {
    const opened = vi.fn()
    renderHook(() => useRequestsEntry(opened))

    openRequest("irq_1")
    openRequests()

    expect(opened.mock.calls).toEqual([["irq_1"], [undefined]])
  })

  it("stops when the page that listens is gone", () => {
    const opened = vi.fn()
    const { unmount } = renderHook(() => useRequestsEntry(opened))
    unmount()

    openRequest("irq_1")

    expect(opened).not.toHaveBeenCalled()
  })

  it("calls the newest callback, not the one it started with", () => {
    const first = vi.fn()
    const second = vi.fn()
    const { rerender } = renderHook(({ onOpen }) => useRequestsEntry(onOpen), {
      initialProps: { onOpen: first },
    })
    rerender({ onOpen: second })

    openRequest("irq_1")

    expect(first).not.toHaveBeenCalled()
    expect(second).toHaveBeenCalledWith("irq_1")
  })

  it("dispatches an event any listener may hear, with only the request's id", () => {
    const heard = vi.fn()
    window.addEventListener(OPEN_REQUESTS_EVENT, heard)

    openRequest("irq_1")

    window.removeEventListener(OPEN_REQUESTS_EVENT, heard)
    expect(heard).toHaveBeenCalledTimes(1)
    expect((heard.mock.calls[0][0] as CustomEvent).detail).toEqual({
      requestId: "irq_1",
    })
  })
})

describe("a link that opens Requests", () => {
  it("opens the request an address names, once, as the page opens", () => {
    window.history.replaceState(
      {},
      "",
      "/?org=org-1&view=requests&request=irq_1"
    )
    const opened = vi.fn()

    renderHook(() => useRequestsEntry(opened))

    expect(opened).toHaveBeenCalledTimes(1)
    expect(opened).toHaveBeenCalledWith("irq_1")
  })

  it("opens Requests without a request when the address names none", () => {
    window.history.replaceState({}, "", "/?view=requests")
    const opened = vi.fn()

    renderHook(() => useRequestsEntry(opened))

    expect(opened).toHaveBeenCalledWith(undefined)
  })

  it("leaves every other address alone", () => {
    window.history.replaceState({}, "", "/?view=billing&request=irq_1")
    const opened = vi.fn()

    renderHook(() => useRequestsEntry(opened))

    expect(opened).not.toHaveBeenCalled()
  })

  it("builds the address it reads", () => {
    expect(requestsLink()).toBe("/?view=requests")
    expect(requestsLink({ organizationId: "org 1", requestId: "irq_1" })).toBe(
      "/?org=org+1&view=requests&request=irq_1"
    )
    expect(
      requestsLinkOf(
        new URL(
          requestsLink({ organizationId: "org-1", requestId: "irq_1" }),
          "https://app.oryxa.in"
        ).search
      )
    ).toEqual({ requestId: "irq_1" })
    expect(requestsLinkOf("?view=requests")).toEqual({})
    expect(requestsLinkOf("?view=tools")).toBeUndefined()
    expect(requestsLinkOf("")).toBeUndefined()
  })
})
