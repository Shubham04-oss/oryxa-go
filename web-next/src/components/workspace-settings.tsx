import { useEffect, useState } from "react"
import {
  Bell,
  Bot,
  Building2,
  CreditCard,
  KeyRound,
  Mail,
  Network,
  Puzzle,
  UserRound,
  UsersRound,
  X,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"
import type { ReactNode } from "react"
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
import { SettingsHeading } from "./settings-kit"
import { WorkspaceAgentSettings } from "./workspace-agent-settings"
import { WorkspaceNotificationSettings } from "./workspace-notification-settings"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog"
import { cn } from "@/lib/utils"
import {
  rememberedSettingsPage,
  rememberSettingsPage,
} from "@/lib/settings/page-choice"
import type { SettingsPage } from "@/lib/settings/page-choice"

export type { SettingsPage } from "@/lib/settings/page-choice"

const pageTitles: Record<SettingsPage, string> = {
  account: "Account",
  agent: "Your agent",
  notifications: "Notifications",
  organization: "Organisation",
  people: "People",
  teams: "Teams",
  billing: "Billing",
  tools: "Tool access",
  signin: "Sign-in apps",
  slack: "Slack",
  email: "Email",
}

/** The line under each page's title. */
function pageSubtitle(page: SettingsPage, organizationName?: string): string {
  switch (page) {
    case "account":
      return "You, how you sign in, and how oryxa looks."
    case "agent":
      return "What your agent may do for you, and what it hides when it answers."
    case "notifications":
      return "Where oryxa tells you about things."
    case "organization":
      return organizationName
        ? `${organizationName}'s name, address and who may join.`
        : "The workspace's name, address and who may join."
    case "people":
      return "Everyone brings their own agent."
    case "teams":
      return "Teams group people. Each team can own teamspaces and get parley emails."
    case "billing":
      return "Owners manage the plan and payment."
    case "tools":
      return "Agents read only what each tool already lets that person see."
    case "signin":
      return "Apps your people use to sign in to oryxa."
    case "slack":
      return "Ask a teammate's agent from Slack. The person asked chooses what their agent shares."
    case "email":
      return "What oryxa sends by email."
  }
}

type NavItem = { page: SettingsPage; icon: LucideIcon | typeof SlackMark }

/** Settings' own navigation: three groups, as the boards have them. */
const navGroups: readonly { label: string; items: readonly NavItem[] }[] = [
  {
    label: "You",
    items: [
      { page: "account", icon: UserRound },
      { page: "agent", icon: Bot },
      { page: "notifications", icon: Bell },
    ],
  },
  {
    label: "Workspace",
    items: [
      { page: "organization", icon: Building2 },
      { page: "people", icon: UsersRound },
      { page: "teams", icon: Network },
      { page: "billing", icon: CreditCard },
    ],
  },
  {
    label: "Connections",
    items: [
      { page: "tools", icon: Puzzle },
      // Organisation admins only, once Oryxa's own sign-in is on.
      { page: "signin", icon: KeyRound },
      // Every member links their own Slack account; owners and admins also
      // manage the organisation's connection there.
      { page: "slack", icon: SlackMark },
      { page: "email", icon: Mail },
    ],
  },
]

/**
 * Settings, a pop-up over the workspace (DESIGN.md §3; the founder keeps it a
 * dialog): "Settings · org" at its head, its own grouped navigation on a
 * tile, and the open page beside it. It opens from the user menu, and on
 * Billing or Slack when a link asked for them (`?view=billing|slack`).
 */
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
  const signinApps = useSigninAppsAvailable({
    organizationId: organization?.id,
    role: organizationRole,
    preview,
  })
  // A remembered section must still be offered to this member. Explicit
  // returns keep their page and its own permission/service explanation.
  const page =
    !initialPage && selectedPage === "signin" && !signinApps
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

  let content: ReactNode
  switch (page) {
    case "account":
      content = (
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
      )
      break
    case "agent":
      content = (
        <WorkspaceAgentSettings
          organization={organization}
          viewerName={session ? identity.name.split(/\s+/)[0] : undefined}
          preview={preview}
        />
      )
      break
    case "notifications":
      content = (
        <WorkspaceNotificationSettings
          organization={organization}
          preview={preview}
        />
      )
      break
    case "organization":
      content = (
        <WorkspaceOrganizationSettings
          organization={organization}
          role={organizationRole}
          preview={preview}
          session={session}
        />
      )
      break
    case "people":
      content = (
        <PeopleManagement
          organizationId={
            organization?.id || (preview ? "preview-organization" : undefined)
          }
          role={session ? organizationRole || "member" : "owner"}
          viewerId={session?.user.id}
          preview={preview}
        />
      )
      break
    case "teams":
      content = (
        <WorkspaceTeamsSettings
          organization={organization}
          role={organizationRole}
          preview={preview}
        />
      )
      break
    case "billing":
      content = (
        <WorkspaceBillingSettings
          organization={organization}
          preview={preview}
        />
      )
      break
    case "tools":
      content = (
        <WorkspaceToolAccessSettings
          organization={organization}
          role={organizationRole}
          preview={preview}
          onOpenTools={onOpenTools}
        />
      )
      break
    case "signin":
      content = (
        <WorkspaceSigninAppsSettings
          organization={organization}
          role={organizationRole}
          preview={preview}
        />
      )
      break
    case "slack":
      content = (
        <WorkspaceSlackSettings
          organization={organization}
          role={organizationRole}
          preview={preview}
        />
      )
      break
    case "email":
      content = (
        <WorkspaceEmailSettings
          organization={organization}
          onBilling={() => setPage("billing")}
          onAccount={() => setPage("account")}
        />
      )
      break
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className="settings-dialog" showCloseButton={false}>
        <header className="settings-dialog-head">
          <DialogTitle className="settings-dialog-title">Settings</DialogTitle>
          {organization && (
            <DialogDescription className="settings-dialog-org">
              {organization.name}
            </DialogDescription>
          )}
          <DialogClose
            render={
              <Button
                variant="ghost"
                size="icon-sm"
                className="settings-dialog-close"
                aria-label="Close settings"
              />
            }
          >
            <X size={17} strokeWidth={1.7} />
          </DialogClose>
        </header>
        <div className="settings-layout">
          <nav className="settings-nav" aria-label="Settings sections">
            {navGroups.map((group) => (
              <div className="settings-nav-group" key={group.label}>
                <span className="settings-nav-heading" aria-hidden="true">
                  {group.label}
                </span>
                {group.items
                  .filter((item) => item.page !== "signin" || signinApps)
                  .map(({ page: item, icon: Icon }) => (
                    <button
                      key={item}
                      type="button"
                      className={cn(
                        "settings-nav-item",
                        page === item && "is-active"
                      )}
                      aria-current={page === item ? "page" : undefined}
                      onClick={() => setPage(item)}
                    >
                      <Icon size={17} strokeWidth={1.7} />
                      {pageTitles[item]}
                    </button>
                  ))}
              </div>
            ))}
          </nav>
          <section
            className="settings-body"
            aria-labelledby="settings-page-title"
          >
            <div className="settings-body-inner">
              <SettingsHeading
                id="settings-page-title"
                title={pageTitles[page]}
                subtitle={pageSubtitle(page, organization?.name)}
              />
              {content}
            </div>
          </section>
        </div>
      </DialogContent>
    </Dialog>
  )
}
