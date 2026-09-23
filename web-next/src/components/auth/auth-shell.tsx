import type { ReactNode } from "react"
import { AuthPixelArt } from "./auth-pixel-art"

export function AuthShell({ children }: { children: ReactNode }) {
  return (
    <main className="auth-page">
      <AuthPixelArt />
      <div className="auth-layout">
        <div className="auth-frame">
          <section className="auth-card" aria-label="Account access">
            {children}
          </section>
        </div>
      </div>
    </main>
  )
}

export function AuthNotice({
  children,
  tone = "error",
}: {
  children: ReactNode
  tone?: "error" | "success"
}) {
  return (
    <div
      className={`auth-notice auth-notice-${tone}`}
      role={tone === "error" ? "alert" : "status"}
    >
      {children}
    </div>
  )
}
