import { createRoot } from "react-dom/client"
import { CredentialDialog } from "@/components/workspace-tool-credential-dialog"
import { connectOryxa } from "./client"
import type { OryxaConnectResult, SigninManifest } from "./client"

/**
 * Asks the member how to sign in to an app (and for its key, when that is
 * the way in) in the credential dialog, then starts the connection through
 * Oryxa's own sign-in. It resolves with what apps answered, or undefined
 * once the member closes the dialog.
 *
 * `connect()` calls this because the page that starts a connection, Tools
 * (`components/workspace-tools.tsx`), renders no dialog of its own, so this
 * mounts one in a root of its own. A page that renders `CredentialDialog`
 * itself needs none of this.
 */
export function promptConnect(options: {
  organizationId: string
  manifest: SigninManifest
  returnTo: string
  autoSubscribe?: boolean
}): Promise<OryxaConnectResult | undefined> {
  const { manifest } = options
  const opener =
    document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null
  return new Promise((resolve) => {
    const host = document.createElement("div")
    document.body.appendChild(host)
    const root = createRoot(host)
    let settled = false
    const finish = (result?: OryxaConnectResult) => {
      if (settled) return
      settled = true
      resolve(result)
      window.setTimeout(() => {
        root.unmount()
        host.remove()
        // Closed: the member goes back to what opened the dialog, which its
        // page may still hold disabled for a moment while it hears why.
        if (!result) returnFocus(opener)
      })
    }
    root.render(
      <CredentialDialog
        mode="connect"
        appName={manifest.name}
        methods={manifest.methods}
        onSubmit={async (kind, values) => {
          const started = await connectOryxa(options.organizationId, {
            autoSubscribe: options.autoSubscribe,
            fields: values,
            kind,
            returnTo: options.returnTo,
            toolkit: manifest.toolkit,
          })
          finish(started)
        }}
        onClose={() => finish(undefined)}
      />
    )
  })
}

/** Focuses `element` once it is enabled again, trying for about half a second. */
function returnFocus(element: HTMLElement | null, tries = 10) {
  if (!element?.isConnected) return
  if (element.matches(":disabled") && tries > 0) {
    window.setTimeout(() => returnFocus(element, tries - 1), 50)
    return
  }
  element.focus()
}
