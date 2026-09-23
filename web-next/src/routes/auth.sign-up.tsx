import { createFileRoute } from "@tanstack/react-router"
import { SignUpPage } from "@/components/auth/auth-pages"
import { safeRedirect } from "@/lib/auth/schemas"

export const Route = createFileRoute("/auth/sign-up")({
  validateSearch: (search: Record<string, unknown>) => ({
    redirect: safeRedirect(search.redirect),
  }),
  component: () => <SignUpPage redirect={Route.useSearch().redirect} />,
  head: () => ({ meta: [{ title: "Create account · Oryxa" }] }),
})
