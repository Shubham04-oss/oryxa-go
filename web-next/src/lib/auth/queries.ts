// Query keys for what the auth service answers about an organization. The
// members list's key is the one Settings → People and Teams already read, so
// every reader shares one copy.
export const organizationKeys = {
  members: (organizationId: string) =>
    ["organizations", organizationId, "members"] as const,
}
