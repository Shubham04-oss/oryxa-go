// Pieces the Agents-mode views share: status pills, notices, an agent's face
// and the focused view's header.
import type { ReactNode } from "react"
import { ArrowLeft } from "lucide-react"
import { IdentityAvatar } from "./workspace-avatar"
import { agentIdentity, statusLabel } from "./agents-format"
import { cn } from "@/lib/utils"

export function StatusPill({ status }: { status: "active" | "disabled" }) {
  return (
    <span className="workspace-agents-status" data-status={status}>
      {statusLabel(status)}
    </span>
  )
}

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

/**
 * An agent's voxel avatar beside its visible name. Hidden from assistive
 * technology there, so the name is not read out twice.
 */
export function AgentFace({
  agent,
  className,
}: {
  agent: { id: string; name: string }
  className?: string
}) {
  return (
    <span className={cn("workspace-agents-face", className)} aria-hidden="true">
      <IdentityAvatar identity={agentIdentity(agent)} />
    </span>
  )
}

/** The focused view's compact header: back, title and kind. */
export function FocusHeader({
  title,
  kind,
  onBack,
}: {
  title: string
  kind: string
  onBack: () => void
}) {
  return (
    <header className="workspace-focus-header">
      <button
        className="workspace-icon-button"
        type="button"
        aria-label="Back to workspace"
        onClick={onBack}
      >
        <ArrowLeft size={17} strokeWidth={1.7} />
      </button>
      <div>
        <h1>{title}</h1>
        <p>{kind}</p>
      </div>
    </header>
  )
}
