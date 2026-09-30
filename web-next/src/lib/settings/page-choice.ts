export type SettingsPage =
  | "account"
  | "organization"
  | "people"
  | "teams"
  | "tools"
  | "signin"
  | "slack"
  | "email"
  | "billing"

export type SettingsScope = { organizationId: string; userId: string }

const pages: readonly SettingsPage[] = [
  "account",
  "organization",
  "people",
  "teams",
  "tools",
  "signin",
  "slack",
  "email",
  "billing",
]

function choiceKey(scope: SettingsScope): string {
  return `oryxa.settings.${JSON.stringify([scope.organizationId, scope.userId])}`
}

/** The last section this member opened in this workspace, across browser visits. */
export function rememberedSettingsPage(
  scope: SettingsScope | null
): SettingsPage | null {
  if (!scope) return null
  try {
    const value = window.localStorage.getItem(choiceKey(scope))
    return pages.find((page) => page === value) ?? null
  } catch {
    return null
  }
}

export function rememberSettingsPage(
  scope: SettingsScope | null,
  page: SettingsPage
): void {
  if (!scope) return
  try {
    window.localStorage.setItem(choiceKey(scope), page)
  } catch {
    // Settings remains usable when browser storage is disabled or full.
  }
}
