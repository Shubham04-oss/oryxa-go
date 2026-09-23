import { createFileRoute } from "@tanstack/react-router"
import { ForgotPasswordPage } from "@/components/auth/auth-pages"

export const Route = createFileRoute("/auth/forgot-password")({
  component: ForgotPasswordPage,
  head: () => ({ meta: [{ title: "Reset password · Oryxa" }] }),
})
