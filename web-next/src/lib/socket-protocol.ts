/**
 * The WebSocket scheme for the page's own origin, for every realtime
 * object's socket (`useAgent({ protocol: socketProtocol(), … })`).
 *
 * PartySocket guesses `ws` only for localhost, 127.0.0.1 and private LAN
 * addresses and `wss` for every other host, so a page served over plain
 * http from any other name (a `*.localhost` host, a LAN hostname, a tunnel)
 * would open `wss://` to a server that speaks no TLS and never connect. The
 * page's own scheme is always the right one.
 */
export function socketProtocol(
  page: Pick<Location, "protocol"> | undefined = typeof window === "undefined"
    ? undefined
    : window.location
): "ws" | "wss" | undefined {
  if (!page) return undefined
  return page.protocol === "https:" ? "wss" : "ws"
}
