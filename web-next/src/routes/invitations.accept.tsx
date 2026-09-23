import { createFileRoute } from "@tanstack/react-router"
import { InvitationAcceptPage } from "@/components/auth/invitation-accept-page"
import { searchToken } from "@/lib/auth/schemas"

export const Route = createFileRoute("/invitations/accept")({
  validateSearch: (search: Record<string, unknown>) => ({
    token: searchToken(search.token),
  }),
  component: () => <InvitationAcceptPage token={Route.useSearch().token} />,
  head: () => ({ meta: [{ title: "Accept invitation · Oryxa" }] }),
})
