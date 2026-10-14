import type { CSSProperties, ReactNode } from "react"

/**
 * The painted scenes from oryxa's site and brand kit (gtm/brand), one per
 * moment of signing in. Words sit on a scene in white, as the brand writes
 * them (DESIGN.md §4).
 */
export type AuthScene = "ships" | "harbour" | "deck" | "nest" | "sea" | "gate"

const scenes: Record<AuthScene, { image: string; line: string; sub: string }> =
  {
    ships: {
      image: "/scenes/ships.jpg",
      line: "One crew. Every ask answered.",
      sub: "Your team and its agents, in one place.",
    },
    nest: {
      image: "/scenes/crows-nest.webp",
      line: "See every ask from the crow’s nest.",
      sub: "Requests, rooms and parleys, with an agent on watch.",
    },
    deck: {
      image: "/scenes/deck.webp",
      line: "Back on deck in a minute.",
      sub: "Then sign in with your new password.",
    },
    sea: {
      image: "/scenes/sea.webp",
      line: "Every ask is a message in a bottle.",
      sub: "oryxa makes sure it reaches the right person.",
    },
    gate: {
      image: "/scenes/gate.webp",
      line: "This one didn’t get through.",
      sub: "A new link takes a few seconds.",
    },
    harbour: {
      image: "/scenes/harbour.webp",
      line: "Welcome to the harbour.",
      sub: "Your crew is already here.",
    },
  }

export function AuthShell({
  children,
  scene = "ships",
  caption,
}: {
  children: ReactNode
  scene?: AuthScene
  /** The scene's words, when the page has its own (creating a workspace). */
  caption?: { line: string; sub: string }
}) {
  const art = { ...scenes[scene], ...caption }
  return (
    <main className="auth-page" data-scene={scene}>
      <div
        className="auth-scene"
        style={{ "--auth-scene": `url("${art.image}")` } as CSSProperties}
        aria-hidden="true"
      >
        <p className="auth-scene-line">
          <strong>{art.line}</strong>
          <span>{art.sub}</span>
        </p>
      </div>
      <div className="auth-side">
        <span className="workspace-wordmark auth-wordmark" aria-label="oryxa">
          oryxa
          <i aria-hidden="true" />
        </span>
        <section className="auth-card" aria-label="Account access">
          {children}
        </section>
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
