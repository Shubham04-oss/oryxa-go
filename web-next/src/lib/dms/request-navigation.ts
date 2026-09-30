// How a request's line in a direct message opens the request. The line's
// "Open" belongs to the Requests view (a screen of its own, built beside the
// DM), which this lane cannot import: the shell that holds both wires them
// once, by wrapping what shows a DM in `OpenRequestProvider` with the function
// that opens the Requests view at a request. Every view of a DM (the main
// canvas, the right panel's DMs tab) reads it from there, so nothing is
// threaded through the panel. REALTIME.md, "Request notes on the page", says
// what the shell wires.
import { createContext, useContext } from "react"

/** Opens the Requests view at the request with this id. */
export type OpenRequest = (requestId: string) => void

const OpenRequestContext = createContext<OpenRequest | undefined>(undefined)

/**
 * Wrap what shows a DM in this, with the function that opens the Requests
 * view at a request: `<OpenRequestProvider value={openRequest}>`. Without it,
 * a DM's request line has no Open action, since it would open nothing.
 */
export const OpenRequestProvider = OpenRequestContext.Provider

/** The shell's way to open a request, or nothing when it wired none. */
export function useOpenRequest(): OpenRequest | undefined {
  return useContext(OpenRequestContext)
}
