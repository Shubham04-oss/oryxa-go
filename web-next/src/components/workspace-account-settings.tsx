import { useEffect, useState } from "react"
import { useMutation } from "@tanstack/react-query"
import { useTheme } from "next-themes"
import { authErrorMessage, requestPasswordReset } from "@/lib/auth/client"
import type { Organization, Role, Session } from "@/lib/auth/client"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import type { AvatarIdentity } from "./workspace-avatar"
import { IdentityAvatar } from "./workspace-avatar"
import {
  SettingsButton,
  SettingsGroup,
  SettingsMessage,
  SettingsRow,
} from "./settings-kit"

const roleNames: Record<Role, string> = {
  owner: "Owner",
  admin: "Admin",
  member: "Member",
}

/**
 * Settings → Account: who the member is, how they sign in, and how oryxa
 * looks. What their agent may do moved to Your agent, and team parley emails
 * to Notifications.
 *
 * Editing the profile waits for a profile route (PATCH /v1/auth/me), and the
 * sign-in providers a member has linked for a route that lists them; until
 * then neither is offered.
 */
export function WorkspaceAccountSettings({
  identity,
  session,
  organization,
  role,
  preview,
  onSignOut,
  signingOut,
  signOutError,
}: {
  identity: AvatarIdentity
  session?: Session
  organization?: Organization
  role?: Role
  preview: boolean
  onSignOut: () => void
  signingOut: boolean
  signOutError: string
}) {
  const reset = useMutation({
    mutationFn: () => requestPasswordReset(session!.user.email),
  })
  const organizationName = organization?.name || (preview ? "Oryxa" : "")
  const roleName = role
    ? roleNames[role]
    : preview
      ? "Owner · preview"
      : undefined
  const about = [
    session?.user.email || "Preview account",
    roleName && organizationName
      ? `${roleName} of ${organizationName}`
      : roleName,
  ]
    .filter(Boolean)
    .join(" · ")

  return (
    <>
      <SettingsGroup label="Profile">
        <SettingsRow
          media={<IdentityAvatar identity={identity} />}
          title={identity.name}
          description={about}
        />
      </SettingsGroup>

      <SettingsGroup label="Sign-in">
        <SettingsRow
          title="Password"
          description="We'll email you a link to choose a new password."
        >
          <SettingsButton
            disabled={preview || reset.isPending || reset.isSuccess}
            onClick={() => reset.mutate()}
          >
            {reset.isPending
              ? "Sending…"
              : reset.isSuccess
                ? "Link sent"
                : "Send reset link"}
          </SettingsButton>
        </SettingsRow>
        {reset.isSuccess && (
          <SettingsMessage tone="notice">
            If this account has a password, a reset link is on its way to{" "}
            {session?.user.email}.
          </SettingsMessage>
        )}
        {reset.isError && (
          <SettingsMessage tone="error">
            {authErrorMessage(reset.error, "Could not send a reset link.")}
          </SettingsMessage>
        )}
        <SettingsRow
          title="Sign out"
          description="Leave this account on this browser."
        >
          <SettingsButton disabled={preview || signingOut} onClick={onSignOut}>
            {signingOut ? "Signing out…" : "Sign out"}
          </SettingsButton>
        </SettingsRow>
        {signOutError && (
          <SettingsMessage tone="error">{signOutError}</SettingsMessage>
        )}
      </SettingsGroup>

      <SettingsGroup label="Appearance">
        <SettingsRow
          title="Theme"
          description="Light, dark, or follow your computer."
        >
          <ThemePicker />
        </SettingsRow>
      </SettingsGroup>
    </>
  )
}

type Theme = "light" | "dark" | "system"

const themes: readonly { value: Theme; label: string }[] = [
  { value: "light", label: "Light" },
  { value: "dark", label: "Dark" },
  { value: "system", label: "System" },
]

function isTheme(value: string | undefined): value is Theme {
  return value === "light" || value === "dark" || value === "system"
}

/** Light, Dark or System, as swatches; next-themes keeps it in this browser. */
function ThemePicker() {
  const { theme, setTheme } = useTheme()
  // The server render cannot know the browser's choice: show System until mounted.
  const [mounted, setMounted] = useState(false)
  useEffect(() => setMounted(true), [])
  const current: Theme = mounted && isTheme(theme) ? theme : "system"
  return (
    <RadioGroup
      aria-label="Theme"
      className="settings-theme"
      value={current}
      onValueChange={(next) => {
        if (isTheme(next as string)) setTheme(next as Theme)
      }}
    >
      {themes.map(({ value, label }) => (
        <label key={value} className="settings-theme-option">
          <span className="settings-theme-swatch" data-swatch={value} />
          <span className="settings-theme-label">
            <RadioGroupItem value={value} />
            {label}
          </span>
        </label>
      ))}
    </RadioGroup>
  )
}
