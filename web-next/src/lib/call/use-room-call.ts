// A room's call as a page sees it, joined or not: the edge's view of it,
// kept fresh by the room's live log (a call.* event refetches it).
import { useEffect } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { ApiError } from "@/lib/api/client"
import type { LiveRoom } from "@/lib/live/client"
import { useRoomLog } from "@/lib/live/use-room-log"
import { callKey, readCall } from "./client"

/** How often the view is read again when no event says it changed. */
export const CALL_REFRESH_MS = 20_000

// The newest call.* event each room's view was refetched for, this page:
// every hook on a room shares its log, so one of them refetching is enough.
const refreshedFor = new Map<string, number>()

/** Forgets which events refetched the view; for tests. */
export function forgetCallRefreshes() {
  refreshedFor.clear()
}

export function useRoomCall(
  organizationId: string | undefined,
  room: LiveRoom | undefined,
  { enabled = true }: { enabled?: boolean } = {}
) {
  const queryClient = useQueryClient()
  const active = enabled && Boolean(organizationId && room)
  const call = useQuery({
    queryKey: callKey(organizationId ?? "", room ?? { kind: "thread", id: "" }),
    queryFn: ({ signal }) =>
      readCall(
        organizationId ?? "",
        room ?? { kind: "thread", id: "" },
        signal
      ),
    enabled: active,
    refetchInterval: CALL_REFRESH_MS,
    // A room the member isn't in yet (403) or that the edge doesn't know
    // (404) has no call to show; asking again won't change that.
    retry: (count, error) =>
      !(error instanceof ApiError && [401, 403, 404].includes(error.status)) &&
      count < 2,
  })
  // The log opens beside the view, not after it (T6): one subscription per
  // room, shared by every hook on it (T5).
  const log = useRoomLog(organizationId, room, { enabled: active })
  // Only news changes the view: a call.* event from the room's history was
  // already in the view the first read returned.
  const lastCallEvent = [...log.events]
    .reverse()
    .find((event) => event.type.startsWith("call."))?.seq
  const news =
    lastCallEvent !== undefined && lastCallEvent > log.history
      ? lastCallEvent
      : undefined

  useEffect(() => {
    if (news === undefined || !organizationId || !room) return
    const key = `${organizationId}/${room.kind}/${room.id}`
    if ((refreshedFor.get(key) ?? 0) >= news) return
    refreshedFor.set(key, news)
    void queryClient.invalidateQueries({
      queryKey: callKey(organizationId, room),
    })
    // The room's identity is in its key; the object is new on each render.
  }, [news, organizationId, room?.kind, room?.id, queryClient])

  return {
    call: call.data ?? null,
    /**
     * The edge refused or doesn't know the room, or calls aren't set up
     * there: no parley here. A network blip or a 5xx is not that (T13).
     */
    unavailable:
      call.isError &&
      call.error instanceof ApiError &&
      [403, 404, 503].includes(call.error.status),
    loading: call.isPending && active,
    log,
  }
}
