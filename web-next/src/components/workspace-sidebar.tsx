import { useId } from "react"
import type { ComponentType, ReactNode, Ref } from "react"
import {
  BookOpen,
  Inbox,
  LogOut,
  MoreHorizontal,
  PanelLeftClose,
  PanelLeftOpen,
  Puzzle,
  Settings2,
  SquarePen,
  X,
} from "lucide-react"
import { IdentityAvatar } from "./workspace-avatar"
import type { AvatarIdentity } from "./workspace-avatar"
import type { Organization, Session } from "@/lib/auth/client"
import { cn } from "@/lib/utils"

/** What the center shows besides the member's agent (the desk and its conversations). */
export type WorkspaceArea = "agent" | "requests" | "knowledge" | "tools"

type Destination = {
  key: Exclude<WorkspaceArea, "agent">
  label: string
  icon: ComponentType<{ size?: number; strokeWidth?: number }>
}

const destinations: Destination[] = [
  { key: "requests", label: "Requests", icon: Inbox },
  { key: "knowledge", label: "Knowledge", icon: BookOpen },
  { key: "tools", label: "Tools", icon: Puzzle },
]

/**
 * The workspace's one navigation (DESIGN.md §2): the workspace, New chat,
 * Requests with its Inbox count, Knowledge and Tools, the member's recent
 * conversations, and the profile menu with Settings and Sign out. On a
 * desktop it collapses to an icon rail; on a phone it is an overlay the
 * header's menu button opens, and it closes after a selection.
 */
export function WorkspaceSidebar({
  collapsed,
  mobile,
  onToggle,
  toggleRef,
  session,
  selectedOrganization,
  area,
  onNewChat,
  newChatDisabled,
  onNavigate,
  requestsWaiting,
  recent,
  identity,
  roleLabel,
  profileMenuOpen,
  onProfileMenuOpenChange,
  onOpenSettings,
  onSignOut,
  signingOut,
  signOutError,
}: {
  /** The desktop rail: icons only. A phone's overlay is never collapsed. */
  collapsed: boolean
  mobile: boolean
  /** Collapses or expands the rail, or closes the phone's overlay. */
  onToggle: () => void
  toggleRef?: Ref<HTMLButtonElement>
  session?: Session
  selectedOrganization?: Organization
  /** The screen in the center; the member's agent (desk or conversation) is no item here. */
  area: WorkspaceArea
  onNewChat: () => void
  newChatDisabled?: boolean
  onNavigate: (area: Exclude<WorkspaceArea, "agent">) => void
  /** Inbox requests waiting on the member, once known. */
  requestsWaiting?: number
  /** The Recent list, shown while the sidebar is expanded. */
  recent?: ReactNode
  identity: AvatarIdentity
  roleLabel: string
  profileMenuOpen: boolean
  onProfileMenuOpenChange: (open: boolean) => void
  onOpenSettings: () => void
  onSignOut?: () => void
  signingOut: boolean
  signOutError: string
}) {
  const expanded = mobile || !collapsed
  const recentHeading = useId()
  const toggleLabel = mobile
    ? "Close navigation"
    : collapsed
      ? "Expand sidebar"
      : "Collapse sidebar"
  const ToggleIcon = mobile ? X : collapsed ? PanelLeftOpen : PanelLeftClose

  return (
    <aside
      id="workspace-navigation"
      className="workspace-sidebar"
      aria-label="Workspace navigation"
    >
      <div className="workspace-sidebar-top">
        {expanded && (
          <WorkspaceName
            session={session}
            selectedOrganization={selectedOrganization}
          />
        )}
        <button
          className="workspace-icon-button workspace-sidebar-toggle"
          type="button"
          ref={toggleRef}
          aria-label={toggleLabel}
          title={toggleLabel}
          aria-controls="workspace-navigation"
          aria-expanded={mobile ? true : !collapsed}
          onClick={onToggle}
        >
          <ToggleIcon size={17} strokeWidth={1.7} />
        </button>
      </div>

      <button
        className="workspace-new-chat"
        type="button"
        aria-label={expanded ? undefined : "New chat"}
        title={expanded ? "Start a new conversation" : "New chat"}
        disabled={newChatDisabled}
        onClick={onNewChat}
      >
        <SquarePen size={16} strokeWidth={1.7} aria-hidden="true" />
        {expanded && <span>New chat</span>}
      </button>

      <nav className="workspace-navigation" aria-label="Main">
        {destinations.map((item) => {
          const Icon = item.icon
          const current = area === item.key
          const count =
            item.key === "requests" && requestsWaiting ? requestsWaiting : 0
          const name =
            count > 0 ? `${item.label}, ${count} waiting` : item.label
          return (
            <button
              className={cn("workspace-nav-item", current && "is-active")}
              key={item.key}
              type="button"
              title={expanded ? undefined : name}
              aria-label={count > 0 || !expanded ? name : undefined}
              aria-current={current ? "page" : undefined}
              onClick={() => onNavigate(item.key)}
            >
              <Icon size={17} strokeWidth={1.7} />
              {expanded && (
                <span className="workspace-nav-label">{item.label}</span>
              )}
              {count > 0 && (
                <span className="workspace-nav-count" aria-hidden="true">
                  {count > 99 ? "99+" : count}
                </span>
              )}
            </button>
          )
        })}
      </nav>

      {expanded && recent && (
        <section className="workspace-recent" aria-labelledby={recentHeading}>
          <h2 className="workspace-recent-heading" id={recentHeading}>
            Recent
          </h2>
          {recent}
        </section>
      )}

      <div className="workspace-profile-area">
        {profileMenuOpen && (
          <div
            className="workspace-profile-menu"
            role="menu"
            aria-label="User menu"
          >
            <button type="button" role="menuitem" onClick={onOpenSettings}>
              <Settings2 size={17} strokeWidth={1.7} /> Settings
            </button>
            {onSignOut && (
              <button
                type="button"
                role="menuitem"
                disabled={signingOut}
                onClick={onSignOut}
              >
                <LogOut size={17} strokeWidth={1.7} />{" "}
                {signingOut ? "Signing out…" : "Sign out"}
              </button>
            )}
          </div>
        )}
        <button
          className="workspace-profile"
          type="button"
          aria-label="Open user menu"
          aria-haspopup="menu"
          aria-expanded={profileMenuOpen}
          onClick={() => onProfileMenuOpenChange(!profileMenuOpen)}
        >
          <IdentityAvatar identity={identity} />
          {expanded && (
            <>
              <span className="workspace-profile-copy">
                <strong>{identity.name}</strong>
                <small>{roleLabel}</small>
              </span>
              <MoreHorizontal size={17} strokeWidth={1.7} />
            </>
          )}
        </button>
      </div>
      {signOutError && (
        <p className="workspace-profile-error" role="alert">
          {signOutError}
        </p>
      )}
    </aside>
  )
}

/**
 * The workspace the member is in, at the top of the sidebar: a switcher when
 * they belong to more than one (choosing one opens it), its name otherwise.
 */
function WorkspaceName({
  session,
  selectedOrganization,
}: {
  session?: Session
  selectedOrganization?: Organization
}) {
  const organizations = session?.organizations ?? []
  const current = selectedOrganization ?? organizations.at(0)
  if (organizations.length > 1 && current) {
    return (
      <label className="workspace-organization-switcher">
        <span className="sr-only">Workspace</span>
        <select
          value={current.id}
          onChange={(event) =>
            window.location.assign(
              `/?org=${encodeURIComponent(event.target.value)}`
            )
          }
        >
          {organizations.map((organization) => (
            <option value={organization.id} key={organization.id}>
              {organization.name}
            </option>
          ))}
        </select>
      </label>
    )
  }
  return (
    <span className="workspace-name" title={current?.name}>
      {current?.name ?? "Oryxa"}
    </span>
  )
}
