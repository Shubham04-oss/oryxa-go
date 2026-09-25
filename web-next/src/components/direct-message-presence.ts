// Who is online, as far as this page knows. A DM says whether the other
// person is online in it: they have it open in a visible tab (REALTIME.md,
// "Presence"). Only an open DM view has that socket, so each view reports its
// peer here and the rest of the page (the header, Team's recent
// conversations, the DMs list) shows the dot for whoever an open view knows
// is online. Nobody is ever shown offline: an unknown person has no dot.
import {
  createContext,
  useContext,
  useEffect,
  useId,
  useSyncExternalStore,
} from "react"

export type PresenceStore = {
  subscribe: (listener: () => void) => () => void
  /** Whether an open view knows `peerId` is online. */
  online: (peerId: string) => boolean
  /** A view's word on its peer; no peer takes the view's word back. */
  report: (viewId: string, peerId: string | undefined, online: boolean) => void
}

export function createPresenceStore(): PresenceStore {
  const views = new Map<string, { peerId: string; online: boolean }>()
  const listeners = new Set<() => void>()
  const changed = () => {
    for (const listener of [...listeners]) listener()
  }
  return {
    subscribe(listener) {
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
      }
    },
    online(peerId) {
      for (const view of views.values()) {
        if (view.peerId === peerId && view.online) return true
      }
      return false
    },
    report(viewId, peerId, online) {
      const before = views.get(viewId)
      if (!peerId) {
        if (before && views.delete(viewId)) changed()
        return
      }
      if (before?.peerId === peerId && before.online === online) return
      views.set(viewId, { peerId, online })
      changed()
    },
  }
}

/**
 * The page's store, which every view reports to and every list reads: what
 * one page knows. A provider can scope another (a test that wants its own).
 */
export const PresenceContext = createContext<PresenceStore>(
  createPresenceStore()
)

/** Whether an open DM view on the page knows `peerId` is online. */
export function usePeerOnline(peerId: string | undefined): boolean {
  const store = useContext(PresenceContext)
  return useSyncExternalStore(
    store.subscribe,
    () => Boolean(peerId && store.online(peerId)),
    () => false
  )
}

/** Reports what a DM view knows of its peer for as long as it is mounted. */
export function useReportPresence(peerId: string, online: boolean): void {
  const store = useContext(PresenceContext)
  const viewId = useId()
  useEffect(() => {
    store.report(viewId, peerId, online)
  }, [store, viewId, peerId, online])
  useEffect(
    () => () => {
      store.report(viewId, undefined, false)
    },
    [store, viewId]
  )
}
