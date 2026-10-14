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
  const log = useRoomLog(organizationId, room, {
    enabled: active && call.isSuccess,
  })
  const lastCallEvent = [...log.events]
    .reverse()
    .find((event) => event.type.startsWith("call."))?.seq

  useEffect(() => {
    if (lastCallEvent === undefined || !organizationId || !room) return
    void queryClient.invalidateQueries({
      queryKey: callKey(organizationId, room),
    })
    // The room's identity is in its key; the object is new on each render.
  }, [lastCallEvent, organizationId, room?.kind, room?.id, queryClient])

  return {
    call: call.data ?? null,
    /** The edge refused or doesn't know the room: no parley here yet. */
    unavailable: call.isError,
    loading: call.isPending && active,
    log,
  }
}
