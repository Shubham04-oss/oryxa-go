import { useEffect, useState } from "react"
import {
  ArrowLeft,
  Building2,
  CreditCard,
  KeyRound,
  Mail,
  Network,
  Puzzle,
  Settings2,
  UserRound,
  UsersRound,
  X,
} from "lucide-react"
import type { Organization, Role, Session } from "@/lib/auth/client"
import type { AvatarIdentity } from "./workspace-avatar"
import { PeopleManagement } from "./people-management"
import { WorkspaceAccountSettings } from "./workspace-account-settings"
import { WorkspaceOrganizationSettings } from "./workspace-organization-settings"
import { WorkspaceTeamsSettings } from "./workspace-teams-settings"
import { WorkspaceToolAccessSettings } from "./workspace-tool-access-settings"
import {
  WorkspaceSigninAppsSettings,
  useSigninAppsAvailable,
} from "./workspace-signin-apps-settings"
import { WorkspaceEmailSettings } from "./workspace-email-settings"
import { SlackMark, WorkspaceSlackSettings } from "./slack-settings"
import { WorkspaceBillingSettings } from "./workspace-billing-settings"
import { cn } from "@/lib/utils"
import {
  rememberedSettingsPage,
  rememberSettingsPage,
} from "@/lib/settings/page-choice"
import type { SettingsPage } from "@/lib/settings/page-choice"

export type { SettingsPage } from "@/lib/settings/page-choice"

const pageTitles: Record<SettingsPage, string> = {
  account: "Account",
  organization: "Organisation",
  people: "People & invitations",
  teams: "Teams",
  tools: "Tool access",
  signin: "Sign-in apps",
  slack: "Slack",
  email: "Email",
  billing: "Billing",
}

export function WorkspaceSettings({
  onClose,
  identity,
  session,
  selectedOrganization,
  organizationRole,
  preview,
  onSignOut,
  signingOut,
  signOutError,
  onOpenTools,
  initialPage,
}: {
  onClose: () => void
  identity: AvatarIdentity
  session?: Session
  selectedOrganization?: Organization
  organizationRole?: Role
  preview: boolean
  onSignOut: () => void
  signingOut: boolean
  signOutError: string
  onOpenTools: () => void
  initialPage?: SettingsPage
}) {
  const organization = selectedOrganization || session?.organizations[0]
  const scope =
    !preview && session && organization
      ? { organizationId: organization.id, userId: session.user.id }
      : null
  const [selectedPage, setPage] = useState<SettingsPage>(
    () => initialPage ?? rememberedSettingsPage(scope) ?? "account"
  )
  // Organization admins only, and only once Oryxa's own sign-in is on.
  const signinApps = useSigninAppsAvailable({
    organizationId: organization?.id,
    role: organizationRole,
    preview,
  })
  const admin = organizationRole === "owner" || organizationRole === "admin"
  // A remembered section must still be offered to this member. Explicit
  // returns keep their page and its own permission/service explanation.
  const page =
    !initialPage &&
    ((selectedPage === "slack" && !admin) ||
      (selectedPage === "signin" && !signinApps))
      ? "account"
      : selectedPage
  const organizationId = scope?.organizationId
  const userId = scope?.userId
  useEffect(() => {
    // Do not replace a remembered Sign-in section while availability loads.
    if (selectedPage === "signin" && !signinApps) return
    rememberSettingsPage(
      organizationId && userId ? { organizationId, userId } : null,
      selectedPage
    )
  }, [organizationId, userId, selectedPage, signinApps])

  return (
    <div
      className="workspace-settings-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div
        className="workspace-settings-dialog"
        role="dialog"
        aria-modal="true"
        aria-label="Settings"
      >
        <aside
          className="workspace-settings-sidebar"
          aria-label="Settings navigation"
        >
          <div className="workspace-settings-sidebar-head">
            <span className="workspace-settings-icon">
              <Settings2 size={19} strokeWidth={1.7} />
            </span>
            <strong>Settings</strong>
          </div>
          <nav aria-label="Settings sections">
            <button
              type="button"
              className={cn(page === "account" && "is-active")}
              onClick={() => setPage("account")}
            >
              <UserRound size={18} strokeWidth={1.7} /> Account
            </button>
            <button
              type="button"
              className={cn(page === "organization" && "is-active")}
              onClick={() => setPage("organization")}
            >
              <Building2 size={18} strokeWidth={1.7} /> Organisation
            </button>
            <button
              type="button"
              className={cn(page === "people" && "is-active")}
              onClick={() => setPage("people")}
            >
              <UsersRound size={18} strokeWidth={1.7} /> People & invitations
            </button>
            <button
              type="button"
              className={cn(page === "teams" && "is-active")}
              onClick={() => setPage("teams")}
            >
              <Network size={18} strokeWidth={1.7} /> Teams
            </button>
            <button
              type="button"
              className={cn(page === "tools" && "is-active")}
              onClick={() => setPage("tools")}
            >
              <Puzzle size={18} strokeWidth={1.7} /> Tool access
            </button>
            {signinApps && (
              <button
                type="button"
                className={cn(page === "signin" && "is-active")}
                onClick={() => setPage("signin")}
              >
                <KeyRound size={18} strokeWidth={1.7} /> Sign-in apps
              </button>
            )}
            {/* Owners and admins manage the organisation's Slack connection. */}
            {(organizationRole === "owner" || organizationRole === "admin") && (
              <button
                type="button"
                className={cn(page === "slack" && "is-active")}
                onClick={() => setPage("slack")}
              >
                <SlackMark size={18} /> Slack
              </button>
            )}
            <button
              type="button"
              className={cn(page === "email" && "is-active")}
              onClick={() => setPage("email")}
            >
              <Mail size={18} strokeWidth={1.7} /> Email
            </button>
            <button
              type="button"
              className={cn(page === "billing" && "is-active")}
              onClick={() => setPage("billing")}
            >
              <CreditCard size={18} strokeWidth={1.7} /> Billing
            </button>
          </nav>
          <button
            className="workspace-settings-back"
            type="button"
            onClick={onClose}
          >
            <ArrowLeft size={17} strokeWidth={1.7} /> Back to workspace
          </button>
        </aside>
        <div className="workspace-settings-content">
          <header className="workspace-settings-header">
            <div>
              <h2>{pageTitles[page]}</h2>
            </div>
            <button
              type="button"
              aria-label="Close settings"
              autoFocus
              onClick={onClose}
            >
              <X size={18} strokeWidth={1.7} />
            </button>
          </header>
          <div className="workspace-settings-scroll">
            {page === "people" ? (
              <PeopleManagement
                organizationId={
                  organization?.id ||
                  (preview ? "preview-organization" : undefined)
                }
                role={session ? organizationRole || "member" : "owner"}
                viewerId={session?.user.id}
                preview={preview}
              />
            ) : page === "organization" ? (
              <WorkspaceOrganizationSettings
                organization={organization}
                role={organizationRole}
                preview={preview}
                session={session}
              />
            ) : page === "account" ? (
              <WorkspaceAccountSettings
                identity={identity}
                session={session}
                organization={organization}
                role={organizationRole}
                preview={preview}
                onSignOut={onSignOut}
                signingOut={signingOut}
                signOutError={signOutError}
              />
            ) : page === "teams" ? (
              <WorkspaceTeamsSettings
                organization={organization}
                role={organizationRole}
                preview={preview}
              />
            ) : page === "tools" ? (
              <WorkspaceToolAccessSettings
                organization={organization}
                role={organizationRole}
                preview={preview}
                onOpenTools={onOpenTools}
              />
            ) : page === "signin" ? (
              <WorkspaceSigninAppsSettings
                organization={organization}
                role={organizationRole}
                preview={preview}
              />
            ) : page === "slack" ? (
              <WorkspaceSlackSettings
                organization={organization}
                role={organizationRole}
                preview={preview}
              />
            ) : page === "email" ? (
              <WorkspaceEmailSettings
                organization={organization}
                onBilling={() => setPage("billing")}
                onAccount={() => setPage("account")}
              />
            ) : (
              <WorkspaceBillingSettings
                organization={organization}
                preview={preview}
              />
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
