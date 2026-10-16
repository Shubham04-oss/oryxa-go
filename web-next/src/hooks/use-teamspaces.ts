import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import type { Teamspace } from "@/lib/teamspaces/client"
import { teamspacesQueryOptions, tracksUnread } from "@/lib/teamspaces/queries"

const noSpaces: Teamspace[] = []

/**
 * How often the list is read again while it carries unread counts (#389),
 * so the sidebar's counts follow rooms the member doesn't have open. A list
 * without them is read as before, on its own staleness.
 */
export const TEAMSPACES_UNREAD_POLL_MS = 60_000

/**
 * Team mode's teamspace state for the shell: the member's teamspaces in the
 * chosen organization, the one selected in Team → Teamspaces (its detail),
 * and the one open as focused work.
 */
export function useTeamspaces(organizationId: string | undefined) {
  const list = useQuery({
    ...teamspacesQueryOptions(organizationId ?? ""),
    enabled: Boolean(organizationId),
    refetchInterval: (query) =>
      query.state.data?.some(tracksUnread) ? TEAMSPACES_UNREAD_POLL_MS : false,
  })
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [openId, setOpenId] = useState<string | null>(null)
  const spaces = list.data ?? noSpaces

  return {
    spaces,
    /** Undefined until the list has loaded. */
    count: list.data?.length,
    selectedId,
    selected: selectedId
      ? spaces.find((space) => space.id === selectedId)
      : undefined,
    select: setSelectedId,
    openId,
    /** Opens a teamspace as focused work, leaving its detail. */
    open(id: string) {
      setSelectedId(null)
      setOpenId(id)
    },
    close() {
      setOpenId(null)
    },
    /** Leaves both the detail and the focused view. */
    reset() {
      setSelectedId(null)
      setOpenId(null)
    },
  }
}
