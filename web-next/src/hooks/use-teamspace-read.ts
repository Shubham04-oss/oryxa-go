// Marking a teamspace's room read (#389). Reading the room never moves the
// member's read cursor on its own: the page says, with `markTeamspaceRead`,
// the highest seq the member has actually had on screen. This hook does that
// for the room's conversation, forward only, once the newest message has sat
// on screen in a visible tab for a moment, and clears the teamspace's unread
// count in the cache at once.
import { useEffect, useRef, useState } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { markTeamspaceRead } from "@/lib/teamspaces/client"
import type { Teamspace } from "@/lib/teamspaces/client"
import {
  markReadInCache,
  teamspaceKeys,
  tracksUnread,
} from "@/lib/teamspaces/queries"

/** How long the newest seen message waits before it is marked read. */
export const MARK_READ_DELAY_MS = 1000

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
 * and only ever moves forward. Nothing is sent for a teamspace without read
 * cursors (a server before #389) or one the member is not on.
 */
export function useMarkTeamspaceRead(
  space: Teamspace | undefined,
  seenSeq: number | undefined
): void {
  const queryClient = useQueryClient()
  const visible = useDocumentVisible()
  // The furthest seq sent for each teamspace, answered or not: a refused one
  // is not sent again, only a later one.
  const sent = useRef(new Map<string, number>())
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
    const timer = window.setTimeout(() => {
      if (!documentVisible()) return
      sent.current.set(teamspaceId, seenSeq)
      markReadInCache(queryClient, organizationId, teamspaceId, seenSeq)
      markTeamspaceRead(organizationId, teamspaceId, seenSeq).then(
        (through) =>
          markReadInCache(queryClient, organizationId, teamspaceId, through),
        () => {
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
    }, MARK_READ_DELAY_MS)
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
  ])
}
