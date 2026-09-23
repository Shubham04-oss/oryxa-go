import { useQuery } from "@tanstack/react-query"
import { Puzzle } from "lucide-react"
import { ApiError } from "@/lib/api/client"
import type { Organization, Role } from "@/lib/auth/client"
import { authErrorMessage } from "@/lib/auth/client"
import { listOrganizationConnections } from "@/lib/settings/client"
import type { Connection } from "@/lib/settings/client"
import { toolkitIcon } from "./workspace-tools-data"

function label(toolkit: string) {
  return toolkit
    .replace(/[-_]/g, " ")
    .replace(/^./, (letter) => letter.toUpperCase())
}

export function WorkspaceToolAccessSettings({
  organization,
  role,
  preview,
  onOpenTools,
}: {
  organization?: Organization
  role?: Role
  preview: boolean
  onOpenTools: () => void
}) {
  const canView = role === "owner" || role === "admin"
  const connections = useQuery({
    queryKey: ["settings", "tool-access", organization?.id],
    queryFn: () => listOrganizationConnections(organization!.id),
    enabled: !preview && Boolean(organization) && canView,
    retry: false,
  })
  const groups = (connections.data || []).reduce<Map<string, Connection[]>>(
    (current, connection) => {
      if (connection.status !== "removed") {
        const bucket = current.get(connection.toolkit)
        if (bucket) bucket.push(connection)
        else current.set(connection.toolkit, [connection])
      }
      return current
    },
    new Map()
  )

  return (
    <div className="workspace-settings-account">
      <section className="workspace-settings-section" aria-label="Tool access">
        <div className="workspace-settings-section-title">
          <h3>Connected apps</h3>
          <p>
            Members connect their own apps. Agents use a connection only as its
            owner.
          </p>
        </div>
        {preview ? (
          <>
            <p className="workspace-settings-hint">
              Layout preview · Sign in to see real connections.
            </p>
            {["github", "notion"].map((toolkit) => {
              const icon = toolkitIcon(toolkit)
              return (
                <div className="workspace-settings-tool-row" key={toolkit}>
                  <span className="workspace-settings-tool-icon">
                    {icon ? (
                      <svg viewBox="0 0 24 24" fill={`#${icon.hex}`}>
                        <path d={icon.path} />
                      </svg>
                    ) : (
                      <Puzzle size={18} />
                    )}
                  </span>
                  <div>
                    <strong>{label(toolkit)}</strong>
                    <small>Example only · no account linked</small>
                  </div>
                </div>
              )
            })}
          </>
        ) : !organization ? (
          <p className="workspace-settings-hint">
            Select an organisation first.
          </p>
        ) : !canView ? (
          <p className="workspace-settings-hint">
            Organisation owners and admins can see who has connected what. You
            can still manage your own tools.
          </p>
        ) : (
          <>
            {connections.isPending && <p role="status">Loading connections…</p>}
            {connections.isError && (
              <p className="workspace-settings-error" role="alert">
                {connections.error instanceof ApiError &&
                connections.error.status === 404
                  ? "Connected apps are not available in this environment."
                  : authErrorMessage(
                      connections.error,
                      "Could not load connections."
                    )}{" "}
                <button
                  type="button"
                  onClick={() => void connections.refetch()}
                >
                  Retry
                </button>
              </p>
            )}
            {connections.data && groups.size === 0 && (
              <p className="workspace-settings-hint">No apps connected yet.</p>
            )}
            {Array.from(groups.entries()).map(([toolkit, items]) => {
              const icon = toolkitIcon(toolkit)
              const owners = new Set(items.map((item) => item.member_id))
              const active = items.filter(
                (item) => item.status === "active"
              ).length
              return (
                <div className="workspace-settings-tool-row" key={toolkit}>
                  <span className="workspace-settings-tool-icon">
                    {icon ? (
                      <svg viewBox="0 0 24 24" fill={`#${icon.hex}`}>
                        <path d={icon.path} />
                      </svg>
                    ) : (
                      <Puzzle size={18} />
                    )}
                  </span>
                  <div>
                    <strong>{label(toolkit)}</strong>
                    <small>
                      {owners.size} {owners.size === 1 ? "member" : "members"}{" "}
                      connected · {active} active
                    </small>
                  </div>
                </div>
              )
            })}
          </>
        )}
        <button
          className="workspace-settings-action"
          type="button"
          onClick={onOpenTools}
        >
          Open my tools →
        </button>
      </section>
    </div>
  )
}
