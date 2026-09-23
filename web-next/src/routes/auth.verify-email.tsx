import { createFileRoute } from "@tanstack/react-router"
import { VerifyEmailPage } from "@/components/auth/auth-pages"
import { searchToken } from "@/lib/auth/schemas"

export const Route = createFileRoute("/auth/verify-email")({
  validateSearch: (search: Record<string, unknown>) => ({
    token: searchToken(search.token),
  }),
  component: () => <VerifyEmailPage token={Route.useSearch().token} />,
  head: () => ({ meta: [{ title: "Verify email · Oryxa" }] }),
})
