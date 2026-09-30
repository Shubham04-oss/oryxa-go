// Where each request sits in a list while the list is on screen. The lists are
// re-read every ten seconds and after each choice, and Go lists the latest
// activity first, so a request the member has just answered would jump from
// among those waiting on them to the top of the rest, from under their
// pointer. Its card would be moved (losing focus) or, in another parent, made
// again (losing what it holds: a secret half typed, a note, the lease its
// answer gave). So a request keeps the group and the place it first had, for as
// long as the list stays open; a request that is new goes first, and one that
// starts waiting on the member later (its answer was held) moves to the top of
// those waiting. Leaving the list and coming back starts it afresh, in Go's
// order.
import { useRef } from "react"
import type { AgentRequest } from "@/lib/agents/agent-requests"

export type RequestGroup = "waiting" | "earlier"

export function useStableLayout(
  requests: readonly AgentRequest[],
  groupOf: (request: AgentRequest) => RequestGroup
): { waiting: AgentRequest[]; earlier: AgentRequest[] } {
  const seen = useRef(new Map<string, { group: RequestGroup; rank: number }>())
  // The lowest rank given so far: whatever is new goes above it.
  const top = useRef(0)
  const places = seen.current

  if (places.size === 0) {
    top.current = 0
    requests.forEach((request, index) => {
      places.set(request.id, { group: groupOf(request), rank: index })
    })
  } else {
    // Newest first: the first of several new ones gets the lowest rank.
    for (const request of requests
      .filter((item) => !places.has(item.id))
      .reverse()) {
      top.current -= 1
      places.set(request.id, { group: groupOf(request), rank: top.current })
    }
    for (const request of requests) {
      const place = places.get(request.id)
      if (place?.group === "earlier" && groupOf(request) === "waiting") {
        top.current -= 1
        places.set(request.id, { group: "waiting", rank: top.current })
      }
    }
  }

  const ranked = (group: RequestGroup) =>
    requests
      .filter((request) => places.get(request.id)?.group === group)
      .sort(
        (a, b) => (places.get(a.id)?.rank ?? 0) - (places.get(b.id)?.rank ?? 0)
      )
  return { waiting: ranked("waiting"), earlier: ranked("earlier") }
}
