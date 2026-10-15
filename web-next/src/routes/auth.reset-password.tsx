import { createFileRoute } from "@tanstack/react-router"
import { ResetPasswordPage } from "@/components/auth/auth-pages"
import { searchToken } from "@/lib/auth/schemas"

export const Route = createFileRoute("/auth/reset-password")({
  validateSearch: (search: Record<string, unknown>) => ({
    token: searchToken(search.token),
  }),
  component: ResetPasswordRoute,
  head: () => ({ meta: [{ title: "Choose a new password · Oryxa" }] }),
})

function ResetPasswordRoute() {
  const { token } = Route.useSearch()
  return <ResetPasswordPage token={token} />
}
