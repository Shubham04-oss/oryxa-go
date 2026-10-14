// Opening Agents → Requests from anywhere, on a request's card when one is
// named. A DM, a note in a conversation, an Agent overview tile and a link
// each want "open this request" without threading a callback through the
// panel and the shell, so this is one call any of them makes, and one listener
// the shell keeps (`useRequestsEntry`). Small on purpose: it imports nothing
// of the view, so whoever calls it does not pull the view in.
import { useEffect, useRef } from "react"

/** The event `openRequests` dispatches on the window. */
export const OPEN_REQUESTS_EVENT = "oryxa:open-requests"

export type OpenRequestsDetail = { requestId?: string }

/**
 * Opens Agents → Requests, on `requestId`'s card when one is named: the
 * request appears first, above the lists, and takes focus. Does nothing where
 * there is no page to open it in (a server render).
 */
export function openRequests(requestId?: string): void {
  if (typeof window === "undefined") return
  window.dispatchEvent(
    new CustomEvent<OpenRequestsDetail>(OPEN_REQUESTS_EVENT, {
      detail: { requestId },
    })
  )
}

/** `openRequests` for one request: what a note that names a request calls. */
export function openRequest(requestId: string): void {
  openRequests(requestId)
}

/**
 * The address that opens Requests, on a request's card when one is named:
 * `/?org=…&view=requests&request=…`. For a link that is followed, where a call
 * to `openRequest` cannot reach the page (an email, a notice).
 */
export function requestsLink({
  organizationId,
  requestId,
}: { organizationId?: string; requestId?: string } = {}): string {
  const search = new URLSearchParams()
  if (organizationId) search.set("org", organizationId)
  search.set("view", "requests")
  if (requestId) search.set("request", requestId)
  return `/?${search}`
}

/** What a page's address asks Requests to open: nothing if it asks for none. */
export function requestsLinkOf(
  search: string
): { requestId?: string } | undefined {
  const params = new URLSearchParams(search)
  if (params.get("view") !== "requests") return undefined
  const requestId = params.get("request")
  return requestId ? { requestId } : {}
}

/**
 * Calls `onOpen` for the request a page's address names, once as the page
 * opens, and for every `openRequests` after. The shell keeps the one of these.
 */
export function useRequestsEntry(onOpen: (requestId?: string) => void): void {
  const latest = useRef(onOpen)
  useEffect(() => {
    latest.current = onOpen
  })

  useEffect(() => {
    const link = requestsLinkOf(window.location.search)
    if (link) latest.current(link.requestId)
  }, [])

  useEffect(() => {
    function opened(event: Event) {
      const { detail } = event as CustomEvent<OpenRequestsDetail | undefined>
      latest.current(detail?.requestId)
    }
    window.addEventListener(OPEN_REQUESTS_EVENT, opened)
    return () => window.removeEventListener(OPEN_REQUESTS_EVENT, opened)
  }, [])
}
