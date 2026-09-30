// A piece the request views share: an empty, unavailable or failed state,
// said plainly.
import type { ReactNode } from "react"

/** An empty, unavailable or failed state, said plainly. */
export function AgentsNotice({
  title,
  children,
  action,
  alert = false,
}: {
  title: string
  children?: ReactNode
  action?: ReactNode
  alert?: boolean
}) {
  return (
    <div className="workspace-agents-notice" role={alert ? "alert" : undefined}>
      <strong>{title}</strong>
      {children && <p>{children}</p>}
      {action}
    </div>
  )
}
