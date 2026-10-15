import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { MoreHorizontal, Search } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { ApiError } from "@/lib/api/client"
import {
  appsQueryKey,
  connect,
  connectionsQueryOptions,
  disconnectConnection,
  listCatalog,
  listConnectionTargets,
  toolkitName,
} from "@/lib/apps/client"
import type { Connection } from "@/lib/apps/client"
import type { Organization, Role } from "@/lib/auth/client"
import { authErrorMessage } from "@/lib/auth/client"
import { listOrganizationConnections } from "@/lib/settings/client"
import type { Connection as OrganizationConnection } from "@/lib/settings/client"
import { ToolkitLogo } from "./workspace-tool-logo"
import { connectRedirect, connectReturnTo } from "./workspace-tool-helpers"
import {
  SettingsActions,
  SettingsButton,
  SettingsGroup,
  SettingsMessage,
  SettingsNote,
  SettingsRow,
} from "./settings-kit"

const targetWords: Record<string, [string, string]> = {
  channel: ["channel", "channels"],
  repository: ["repo", "repos"],
  database: ["database", "databases"],
  page: ["page", "pages"],
  file: ["file", "files"],
  folder: ["folder", "folders"],
}

/** "14 channels", from what the connection can reach. */
function reachLine(targets: readonly { kind: string }[]): string | undefined {
  if (targets.length === 0) return undefined
  const kind = targets[0].kind
  const [one, many] = targetWords[kind] ?? ["item", "items"]
  return `${targets.length} ${targets.length === 1 ? one : many}`
}

function Logo({ toolkit, logo }: { toolkit: string; logo?: string }) {
  return <ToolkitLogo toolkit={{ toolkit, name: toolkitName(toolkit), logo }} />
}

/**
 * Settings → Tool access: the member's own connected tools (who can use them
 * and what each reaches), adding one from the catalog, and, for owners and
 * admins, who in the organisation has connected what.
 *
 * Choosing who may use a connection has no route yet: a member's connection
 * is used only for them, and the page says so instead of offering a choice.
 */
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
  if (preview || !organization) {
    return (
      <SettingsGroup label="Connected">
        <SettingsNote>
          Layout preview. Sign in to see and connect your tools.
        </SettingsNote>
      </SettingsGroup>
    )
  }
  return (
    <ToolAccess
      key={organization.id}
      organizationId={organization.id}
      canViewOrganization={role === "owner" || role === "admin"}
      onOpenTools={onOpenTools}
    />
  )
}

function ToolAccess({
  organizationId,
  canViewOrganization,
  onOpenTools,
}: {
  organizationId: string
  canViewOrganization: boolean
  onOpenTools: () => void
}) {
  const mine = useQuery({
    ...connectionsQueryOptions(organizationId),
    retry: false,
  })
  const own = (mine.data ?? []).filter(
    (connection) => connection.status !== "removed"
  )
  return (
    <>
      <SettingsGroup label={`Connected${mine.data ? ` · ${own.length}` : ""}`}>
        {mine.isPending ? (
          <SettingsNote>Loading your tools…</SettingsNote>
        ) : mine.isError ? (
          <SettingsMessage tone="error">
            {mine.error instanceof ApiError && mine.error.status === 404
              ? "Connected apps are not available in this environment."
              : authErrorMessage(mine.error, "Could not load your tools.")}
          </SettingsMessage>
        ) : own.length === 0 ? (
          <SettingsNote>You haven't connected a tool yet.</SettingsNote>
        ) : (
          own.map((connection) => (
            <OwnConnectionRow
              key={connection.id}
              organizationId={organizationId}
              connection={connection}
            />
          ))
        )}
      </SettingsGroup>

      <AddTool organizationId={organizationId} onOpenTools={onOpenTools} />

      <OrganizationConnections
        organizationId={organizationId}
        canView={canViewOrganization}
      />
    </>
  )
}

function OwnConnectionRow({
  organizationId,
  connection,
}: {
  organizationId: string
  connection: Connection
}) {
  const queryClient = useQueryClient()
  const name = toolkitName(connection.toolkit)
  const [confirm, setConfirm] = useState(false)
  const active = connection.status === "active"
  const targets = useQuery({
    queryKey: [
      ...appsQueryKey(organizationId),
      "connections",
      connection.id,
      "targets",
    ],
    queryFn: () => listConnectionTargets(organizationId, connection.id),
    enabled: active,
    retry: false,
    staleTime: 5 * 60_000,
  })
  const disconnect = useMutation({
    mutationFn: () => disconnectConnection(organizationId, connection.id),
    onSuccess: async () => {
      setConfirm(false)
      await queryClient.invalidateQueries({
        queryKey: appsQueryKey(organizationId),
      })
    },
  })
  const reach = targets.data ? reachLine(targets.data) : undefined
  const state = active
    ? undefined
    : connection.status === "pending"
      ? "Connecting"
      : connection.status === "expired"
        ? "Needs reconnecting"
        : "Not working"
  return (
    <>
      <SettingsRow
        media={<Logo toolkit={connection.toolkit} />}
        title={connection.label ? `${name} · ${connection.label}` : name}
        description={["Only you", reach, state].filter(Boolean).join(" · ")}
      >
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={`More for ${name}`}
              />
            }
          >
            <MoreHorizontal />
          </DropdownMenuTrigger>
          <DropdownMenuContent className="settings-popup" align="end">
            <DropdownMenuItem
              variant="destructive"
              onClick={() => setConfirm(true)}
            >
              Disconnect
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SettingsRow>
      {confirm && (
        <SettingsActions
          start={`Disconnect ${name}? Your agent stops using it.`}
        >
          <SettingsButton size="default" onClick={() => setConfirm(false)}>
            Cancel
          </SettingsButton>
          <SettingsButton
            size="default"
            tone="danger"
            disabled={disconnect.isPending}
            onClick={() => disconnect.mutate()}
          >
            {disconnect.isPending ? "Disconnecting…" : "Disconnect"}
          </SettingsButton>
        </SettingsActions>
      )}
      {disconnect.isError && (
        <SettingsMessage tone="error">
          {authErrorMessage(disconnect.error, `Could not disconnect ${name}.`)}
        </SettingsMessage>
      )}
    </>
  )
}

function AddTool({
  organizationId,
  onOpenTools,
}: {
  organizationId: string
  onOpenTools: () => void
}) {
  const [search, setSearch] = useState("")
  const query = search.trim()
  const catalog = useQuery({
    queryKey: ["apps", "catalog", "settings", query],
    queryFn: () => listCatalog({ search: query || undefined, limit: 6 }),
    retry: false,
    staleTime: 60_000,
  })
  const start = useMutation({
    mutationFn: async (toolkit: string) => {
      const started = await connect(organizationId, {
        toolkit,
        returnTo: connectReturnTo(window.location.href, organizationId),
      })
      window.location.assign(
        connectRedirect(started.redirect_url, window.location.origin)
      )
    },
  })
  const toolkits = (catalog.data?.toolkits ?? []).slice(0, 6)
  return (
    <SettingsGroup label="Add a tool">
      <div className="settings-search">
        <Search aria-hidden="true" />
        <Input
          className="settings-input"
          type="search"
          aria-label="Search tools"
          placeholder="Search tools"
          value={search}
          onChange={(event) => setSearch(event.target.value)}
        />
      </div>
      {catalog.isPending ? (
        <SettingsNote>Loading the catalog…</SettingsNote>
      ) : catalog.isError ? (
        <SettingsMessage tone="error">
          {authErrorMessage(catalog.error, "Could not load the catalog.")}
        </SettingsMessage>
      ) : toolkits.length === 0 ? (
        <SettingsNote>No tool matches “{query}”.</SettingsNote>
      ) : (
        <ul className="settings-catalog">
          {toolkits.map((toolkit) => (
            <li key={toolkit.toolkit}>
              <Logo toolkit={toolkit.toolkit} logo={toolkit.logo} />
              <span>
                <strong>{toolkit.name}</strong>
                <small>{toolkit.categories[0]?.name ?? "Tool"}</small>
              </span>
              <SettingsButton
                aria-label={`Connect ${toolkit.name}`}
                disabled={!toolkit.connectable || start.isPending}
                onClick={() => start.mutate(toolkit.toolkit)}
              >
                {start.isPending && start.variables === toolkit.toolkit
                  ? "Opening…"
                  : "Connect"}
              </SettingsButton>
            </li>
          ))}
        </ul>
      )}
      {start.isError && (
        <SettingsMessage tone="error">
          {authErrorMessage(start.error, "Could not start connecting.")}
        </SettingsMessage>
      )}
      <SettingsActions>
        <SettingsButton size="default" onClick={onOpenTools}>
          All tools
        </SettingsButton>
      </SettingsActions>
    </SettingsGroup>
  )
}

function OrganizationConnections({
  organizationId,
  canView,
}: {
  organizationId: string
  canView: boolean
}) {
  const connections = useQuery({
    queryKey: ["settings", "tool-access", organizationId],
    queryFn: () => listOrganizationConnections(organizationId),
    enabled: canView,
    retry: false,
  })
  if (!canView) {
    return (
      <SettingsGroup label="Across the organisation">
        <SettingsNote>
          Organisation owners and admins can see who has connected what.
        </SettingsNote>
      </SettingsGroup>
    )
  }
  const groups = (connections.data ?? []).reduce<
    Map<string, OrganizationConnection[]>
  >((current, connection) => {
    if (connection.status !== "removed") {
      const bucket = current.get(connection.toolkit)
      if (bucket) bucket.push(connection)
      else current.set(connection.toolkit, [connection])
    }
    return current
  }, new Map())
  return (
    <SettingsGroup label="Across the organisation">
      {connections.isPending && (
        <SettingsNote>Loading connections…</SettingsNote>
      )}
      {connections.isError && (
        <SettingsMessage tone="error">
          {connections.error instanceof ApiError &&
          connections.error.status === 404
            ? "Connected apps are not available in this environment."
            : authErrorMessage(
                connections.error,
                "Could not load connections."
              )}{" "}
          <SettingsButton onClick={() => void connections.refetch()}>
            Retry
          </SettingsButton>
        </SettingsMessage>
      )}
      {connections.data && groups.size === 0 && (
        <SettingsNote>No apps connected yet.</SettingsNote>
      )}
      {Array.from(groups.entries()).map(([toolkit, items]) => {
        const owners = new Set(items.map((item) => item.member_id))
        const active = items.filter((item) => item.status === "active").length
        return (
          <SettingsRow
            key={toolkit}
            media={<Logo toolkit={toolkit} />}
            title={toolkitName(toolkit)}
            description={`${owners.size} ${owners.size === 1 ? "member" : "members"} connected · ${active} active`}
          />
        )
      })}
    </SettingsGroup>
  )
}
