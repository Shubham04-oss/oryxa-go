// Marking a teamspace's room read (#389). Reading the room never moves the
// member's read cursor on its own: the page says, with `markTeamspaceRead`,
// the highest seq the member has actually had on screen. This hook does that
// for the room's conversation, forward only, once the newest message has sat
// on screen in a visible tab for a moment, and clears the teamspace's unread
// count in the cache at once.
import { useEffect, useRef, useState } from "react"
import { useQueryClient } from "@tanstack/react-query"
import type { Query } from "@tanstack/react-query"
import { markTeamspaceRead } from "@/lib/teamspaces/client"
import type { Teamspace } from "@/lib/teamspaces/client"
import {
  markReadInCache,
  teamspaceKeys,
  tracksUnread,
} from "@/lib/teamspaces/queries"

/** How long the newest seen message waits before it is marked read. */
export const MARK_READ_DELAY_MS = 1000

/** The first wait before a refused mark is tried again; it doubles each time. */
export const MARK_READ_RETRY_MS = 5000

/** The longest wait between tries of a mark that keeps failing. */
const MARK_READ_RETRY_MAX_MS = 60_000

function retryDelay(failures: number): number {
  if (failures === 0) return MARK_READ_DELAY_MS
  return Math.min(
    MARK_READ_RETRY_MS * 2 ** (failures - 1),
    MARK_READ_RETRY_MAX_MS
  )
}

function documentVisible(): boolean {
  return (
    typeof document === "undefined" || document.visibilityState === "visible"
  )
}

/** Whether this tab is the one on screen, following its changes. */
function useDocumentVisible(): boolean {
  const [visible, setVisible] = useState(documentVisible)
  useEffect(() => {
    const onChange = () => setVisible(documentVisible())
    document.addEventListener("visibilitychange", onChange)
    return () => document.removeEventListener("visibilitychange", onChange)
  }, [])
  return visible
}

/**
 * Marks `space`'s room read through `seenSeq`, the highest seq on the
 * member's screen right now (undefined while nothing is, such as when they
 * have scrolled back or the room is behind another tab). It waits
 * `MARK_READ_DELAY_MS` after the last change and for the tab to be visible,
 * and only ever moves forward. A mark that fails is tried again, after
 * `MARK_READ_RETRY_MS`, doubling while it keeps failing. Nothing is sent for a teamspace without read
 * cursors (a server before #389) or one the member is not on.
 */
export function useMarkTeamspaceRead(
  space: Teamspace | undefined,
  seenSeq: number | undefined
): void {
  const queryClient = useQueryClient()
  const visible = useDocumentVisible()
  // The furthest seq sent for each teamspace and not refused: it is not sent
  // again, only a later one.
  const sent = useRef(new Map<string, number>())
  // How many marks in a row failed for each teamspace, which spaces the next
  // try out; `retry` changes after each failure to schedule it.
  const failures = useRef(new Map<string, number>())
  const [retry, setRetry] = useState(0)
  const organizationId = space?.organization_id
  const teamspaceId = space?.id
  const tracked = Boolean(space && tracksUnread(space))
  const readThrough = space?.last_read_seq ?? 0
  const unread = space?.unread_count ?? 0

  useEffect(() => {
    if (!tracked || !visible || !organizationId || !teamspaceId) return
    if (seenSeq === undefined || seenSeq <= 0) return
    const already = sent.current.get(teamspaceId) ?? 0
    if (seenSeq <= already) return
    // Already read that far, and nothing counted unread: nothing to say.
    if (seenSeq <= readThrough && unread === 0) return
    const delay = retryDelay(failures.current.get(teamspaceId) ?? 0)
    const timer = window.setTimeout(() => {
      if (!documentVisible()) return
      sent.current.set(teamspaceId, seenSeq)
      // A list or teamspace read already in flight would land after the
      // optimistic clear and bring the old count back: drop it, and the next
      // read sees the moved cursor. A first load, with nothing to show yet,
      // is left to finish.
      const loaded = (query: Query) => query.state.data !== undefined
      void queryClient.cancelQueries({
        queryKey: teamspaceKeys.list(organizationId),
        predicate: loaded,
      })
      void queryClient.cancelQueries({
        queryKey: teamspaceKeys.teamspace(organizationId, teamspaceId),
        exact: true,
        predicate: loaded,
      })
      markReadInCache(queryClient, organizationId, teamspaceId, seenSeq)
      markTeamspaceRead(organizationId, teamspaceId, seenSeq).then(
        (through) => {
          failures.current.delete(teamspaceId)
          markReadInCache(queryClient, organizationId, teamspaceId, through)
        },
        () => {
          // Not marked: let this seq be sent again, after a wait.
          if (sent.current.get(teamspaceId) === seenSeq) {
            sent.current.delete(teamspaceId)
          }
          failures.current.set(
            teamspaceId,
            (failures.current.get(teamspaceId) ?? 0) + 1
          )
          setRetry((n) => n + 1)
          // The optimistic count may be wrong now: the server says again.
          void queryClient.invalidateQueries({
            queryKey: teamspaceKeys.list(organizationId),
          })
          void queryClient.invalidateQueries({
            queryKey: teamspaceKeys.teamspace(organizationId, teamspaceId),
            exact: true,
          })
        }
      )
    }, delay)
    return () => window.clearTimeout(timer)
  }, [
    queryClient,
    tracked,
    visible,
    organizationId,
    teamspaceId,
    seenSeq,
    readThrough,
    unread,
    retry,
  ])
}
