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
            <footer className="auth-footer">
              <nav className="auth-legal-links" aria-label="Legal">
                <a
                  href="https://oryxa.in/legal/privacy"
                  target="_blank"
                  rel="noopener noreferrer"
                  aria-label="Privacy Policy (opens in a new tab)"
                >
                  Privacy Policy
                </a>
                <a
                  href="https://oryxa.in/legal/terms"
                  target="_blank"
                  rel="noopener noreferrer"
                  aria-label="Terms of Service (opens in a new tab)"
                >
                  Terms of Service
                </a>
              </nav>
            </footer>
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
