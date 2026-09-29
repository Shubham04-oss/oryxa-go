import { useEffect, useRef, useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import {
  ArrowUpRight,
  Check,
  CircleAlert,
  Clock3,
  LoaderCircle,
} from "lucide-react"
import {
  appsErrorMessage,
  appsQueryKey,
  canReconnect,
  connectionReasonText,
  connectionStatusHint,
  connectionStatusLabel,
  connectionStatusTone,
  connectionsQueryOptions,
  disconnectConnection,
} from "@/lib/apps/client"
import type { Connection, Toolkit } from "@/lib/apps/client"
import { ToolkitLogo } from "./workspace-tool-logo"
import { prettyTime, sentence } from "./workspace-tool-helpers"

/** How a connection left the row: what the member did, in the words of the notice. */
export type RemovalKind = "cancelled" | "disconnected" | "removed" | "replaced"

/**
 * What a removal is called for each status (see lib/apps/README.md): a
 * pending connection is cancelled, a failed one (never connected, nothing
 * attached) is simply removed, an expired one a newer connection replaced is
 * removed as the old one, and a working or expired one is disconnected.
 */
function removalKind(connection: Connection, replaced: boolean): RemovalKind {
  if (replaced) return "replaced"
  if (connection.status === "pending") return "cancelled"
  if (connection.status === "failed") return "removed"
  return "disconnected"
}

function statusLine(connection: Connection, replaced: boolean): string {
  const reason = connection.status_reason?.trim()
  if (replaced) return "Replaced by your new connection."
  switch (connection.status) {
    case "active":
      return connection.label || "Connected to your account"
    case "pending":
      return `${connectionStatusHint("pending")} Started ${prettyTime(connection.created_at)}.`
    case "failed":
      return reason
        ? `Didn't connect: ${sentence(connectionReasonText(reason))}`
        : connectionStatusHint("failed")
    case "expired":
      return reason
        ? `Expired: ${sentence(reason)}`
        : connectionStatusHint("expired")
    default:
      return connectionStatusHint(connection.status)
  }
}

/** A second line where the status needs one. */
function statusNote(connection: Connection, replaced: boolean): string {
  if (replaced) {
    const reason = connection.status_reason?.trim()
    return `It expired${reason ? `: ${connectionReasonText(reason)}` : ""}. What it indexed stays searchable until you remove it.`
  }
  if (connection.status === "expired") {
    return "Reconnecting starts a new connection; this one stays until you remove it."
  }
  return ""
}

/**
 * One connection opened from the connected row: what it is, where it
 * stands, and what the member can do about it. The actions follow what apps
 * accepts for each status:
 *
 * - active: Disconnect, after a confirm step.
 * - pending: Cancel, which is the same DELETE; there is no cancel endpoint.
 * - failed: Reconnect, or Remove the stale row. Nothing hangs off a failed
 *   connection, so removing it asks no confirmation.
 * - expired: Reconnect, or Disconnect after a confirm step.
 * - expired and `replaced` (a newer connection to the app is active): only
 *   Remove, after the same confirm step. Reconnecting would answer 409 while
 *   the new one is active, and removing retracts what it indexed, so it is
 *   never done for the member.
 *
 * Reconnecting is a fresh `connect()`. The page removes a failed row once
 * the new connection is active (`replacedConnections`); an expired one stays.
 */
export function ConnectionSummary({
  connection,
  toolkit,
  organizationId,
  preview,
  offered = true,
  replaced,
  reconnecting,
  connectBusy,
  reconnectError,
  onReconnect,
  onRemoved,
}: {
  connection: Connection
  toolkit: Toolkit
  organizationId: string
  preview: boolean
  offered?: boolean
  /** An expired connection a newer active connection to the app replaced. */
  replaced: boolean
  /** The page is starting a new connection to this app. */
  reconnecting: boolean
  /** Some connection is being started, here or in the catalog. */
  connectBusy: boolean
  reconnectError?: string
  onReconnect: () => void
  onRemoved: (connection: Connection, kind: RemovalKind) => void
}) {
  const queryClient = useQueryClient()
  const [confirming, setConfirming] = useState(false)
  const keepButton = useRef<HTMLButtonElement>(null)
  const removeButton = useRef<HTMLButtonElement>(null)
  const wasConfirming = useRef(false)
  const status = connection.status
  const name = toolkit.name
  const kind = removalKind(connection, replaced)
  const remove = useMutation({
    mutationFn: () => disconnectConnection(organizationId, connection.id),
    onSuccess: () => {
      const list = connectionsQueryOptions(organizationId).queryKey
      queryClient.setQueryData<Connection[]>(list, (current) =>
        current?.filter((item) => item.id !== connection.id)
      )
      onRemoved(connection, kind)
      return queryClient.invalidateQueries({
        queryKey: [...appsQueryKey(organizationId), "connections"],
      })
    },
  })

  // Focus moves into the confirm step, and back to the button that opened it
  // when the member keeps the connection, so a keyboard user is never left
  // on the page body.
  useEffect(() => {
    if (confirming) keepButton.current?.focus()
    else if (wasConfirming.current) removeButton.current?.focus()
    wasConfirming.current = confirming
  }, [confirming])

  // What asks for the confirm step: disconnecting a working or expired
  // connection, or removing an expired one that has been replaced.
  const confirmVerb = replaced
    ? "Remove"
    : status === "active" || status === "expired"
      ? "Disconnect"
      : null
  const confirmQuestion = replaced
    ? `Remove the old ${name} connection? What it indexed is removed from search. Your new connection isn't affected.`
    : status === "active"
      ? `Disconnect ${name}? Your agents lose access to it, its events stop and what it indexed is removed from search.`
      : `Disconnect ${name}? Its events stop and what it indexed is removed from search.`
  const tone = preview
    ? "active"
    : replaced
      ? "neutral"
      : connectionStatusTone(status)
  const BadgeIcon =
    preview || status === "active"
      ? Check
      : status === "pending"
        ? Clock3
        : CircleAlert
  const badge = preview
    ? "Preview"
    : replaced
      ? "Replaced"
      : status === "active"
        ? "Connected"
        : connectionStatusLabel(status)
  const note = preview ? "" : statusNote(connection, replaced)
  const error = remove.isError
    ? appsErrorMessage(
        remove.error,
        kind === "cancelled"
          ? `Could not cancel connecting ${name}.`
          : kind === "disconnected"
            ? `Could not disconnect ${name}.`
            : `Could not remove ${name}.`
      )
    : reconnectError

  return (
    <div
      className="workspace-tools-expanded"
      data-status={replaced ? "replaced" : status}
    >
      <ToolkitLogo toolkit={toolkit} />
      <div>
        <strong>{name}</strong>
        {toolkit.description && <p>{toolkit.description}</p>}
        <small>
          {preview
            ? "Example connection · no account linked"
            : statusLine(connection, replaced)}
        </small>
        {note && (
          <small className="workspace-tools-expanded-note">{note}</small>
        )}
        {!preview && !offered && (
          <small className="workspace-tools-expanded-note">
            This app is not offered by your workspace. You can disconnect it, or
            ask an admin to allow it again.
          </small>
        )}
        {!preview && !confirming && (
          <div className="workspace-tools-lifecycle">
            {offered && canReconnect(status) && !replaced && (
              <button
                type="button"
                className="workspace-tools-pill"
                disabled={connectBusy || remove.isPending}
                onClick={onReconnect}
              >
                {reconnecting ? (
                  <>
                    <LoaderCircle
                      className="workspace-tools-spinner"
                      size={14}
                    />
                    Opening…
                  </>
                ) : (
                  <>
                    Reconnect <ArrowUpRight size={14} />
                  </>
                )}
              </button>
            )}
            {status === "pending" && (
              <button
                type="button"
                className="workspace-tools-pill"
                disabled={remove.isPending}
                onClick={() => remove.mutate()}
              >
                {remove.isPending ? "Cancelling…" : "Cancel connecting"}
              </button>
            )}
            {status === "failed" && (
              <button
                type="button"
                className="workspace-tools-pill is-quiet"
                disabled={remove.isPending || reconnecting}
                onClick={() => remove.mutate()}
              >
                {remove.isPending ? "Removing…" : "Remove"}
              </button>
            )}
            {confirmVerb && (
              <button
                ref={removeButton}
                type="button"
                className="workspace-tools-pill is-quiet"
                disabled={reconnecting}
                onClick={() => {
                  remove.reset()
                  setConfirming(true)
                }}
              >
                {confirmVerb}
              </button>
            )}
          </div>
        )}
        {!preview && confirming && confirmVerb && (
          <div
            className="workspace-tools-confirm"
            role="group"
            aria-label={
              replaced
                ? `Remove the old ${name} connection`
                : `Disconnect ${name}`
            }
            onKeyDown={(event) => {
              if (event.key === "Escape" && !remove.isPending) {
                setConfirming(false)
              }
            }}
          >
            <p>{confirmQuestion}</p>
            <span>
              <button
                ref={keepButton}
                type="button"
                className="workspace-tools-pill is-quiet"
                disabled={remove.isPending}
                onClick={() => setConfirming(false)}
              >
                {status === "active" ? "Keep connected" : "Keep it"}
              </button>
              <button
                type="button"
                className="workspace-tools-pill is-danger"
                disabled={remove.isPending}
                onClick={() => remove.mutate()}
              >
                {remove.isPending
                  ? replaced
                    ? "Removing…"
                    : "Disconnecting…"
                  : replaced
                    ? "Yes, remove"
                    : "Yes, disconnect"}
              </button>
            </span>
          </div>
        )}
        {error && (
          <p className="workspace-tool-inline-error" role="alert">
            {error}
          </p>
        )}
      </div>
      <span className="workspace-tools-connected-badge" data-tone={tone}>
        <BadgeIcon size={13} aria-hidden="true" /> {badge}
      </span>
    </div>
  )
}
