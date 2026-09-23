import { createFileRoute } from "@tanstack/react-router"
import { SignInPage } from "@/components/auth/auth-pages"
import { safeRedirect } from "@/lib/auth/schemas"

export const Route = createFileRoute("/auth/sign-in")({
  validateSearch: (search: Record<string, unknown>) => ({
    redirect: safeRedirect(search.redirect),
  }),
  component: () => <SignInPage redirect={Route.useSearch().redirect} />,
  head: () => ({ meta: [{ title: "Sign in · Oryxa" }] }),
})
