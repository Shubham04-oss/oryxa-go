// Onboarding v3's frame (boards OnbV3s1–s5 and Onb3Model, founder's redesign
// of 15 Oct 2026): the scene on the left with a miniature of the member's
// workspace that fills in one part per step, the form on the right under
// "Step N of 5" and a five-part progress bar. The miniature shows only what
// the member has really set up (or is typing now), never example people.
import { useEffect } from "react"
import type { CSSProperties, ReactNode } from "react"
import { Skeleton } from "@/components/ui/skeleton"
import { personIdentity } from "./teamspace-format"
import { IdentityAvatar } from "./workspace-avatar"
import { ToolkitLogo } from "./workspace-tool-logo"
import { toolkitIcon } from "./workspace-tools-data"
import { toolkitName } from "@/lib/apps/client"
import "./onboarding.css"

export const SETUP_STEP_COUNT = 5

const sceneImages = {
  harbour: "/scenes/harbour.webp",
  nest: "/scenes/crows-nest.webp",
  deck: "/scenes/deck.webp",
  ships: "/scenes/ships.jpg",
  sea: "/scenes/sea.webp",
} as const

export type OnboardingScene = keyof typeof sceneImages

/** What the miniature knows of the member's workspace so far. */
export type WorkspaceSketch = {
  workspaceName: string
  firstName?: string
  /** Krude has been met (step 2 reached). */
  agent: boolean
  /** Background access is on. */
  agentAway?: boolean
  /** Active app connections. */
  tools: string[]
  /** Addresses invited, as the invitations list or this session knows them. */
  invited: string[]
  /** The first teamspace's name, made or being typed. */
  teamspace?: string
}

function appName(toolkit: string) {
  return toolkitIcon(toolkit)?.title || toolkitName(toolkit)
}

function Ghost({
  width = "100%",
  height = 6,
}: {
  width?: string
  height?: number
}) {
  return <Skeleton className="onb-ghost" style={{ width, height }} />
}

/** The miniature: navigation, the page and Krude's panel, as set up so far. */
export function WorkspaceModel({
  step,
  sketch,
}: {
  step: number
  sketch: WorkspaceSketch
}) {
  const initial = sketch.workspaceName.trim().charAt(0).toUpperCase()
  return (
    <div
      className="onb-model"
      role="img"
      aria-label="A preview of your workspace as you set it up"
    >
      <div className="onb-pane">
        <div className="onb-ws" data-fresh={step === 1 || undefined}>
          <i>{initial || "·"}</i>
          {sketch.workspaceName.trim() || <Ghost width="60%" />}
        </div>
        <div className="onb-nav-i is-on">
          <u />
          My workspace
        </div>
        <div className="onb-nav-i">
          <u />
          Requests
        </div>
        <div className="onb-nav-i">
          <u />
          Library
        </div>
        {step >= 5 && sketch.teamspace?.trim() && (
          <div className="onb-nav-i onb-gap" data-fresh>
            <u />
            {sketch.teamspace.trim()}
          </div>
        )}
        {step >= 4 && sketch.invited.length > 0 ? (
          <div className="onb-gap" data-fresh={step === 4 || undefined}>
            {sketch.invited.slice(0, 3).map((email) => (
              <div className="onb-nav-i" key={email}>
                <IdentityAvatar identity={personIdentity(email, email)} />
                {email.split("@")[0]}
              </div>
            ))}
          </div>
        ) : (
          <div className="onb-gap">
            <Ghost width="70%" />
            <Ghost width="55%" />
          </div>
        )}
      </div>
      <div className="onb-pane">
        <div className="onb-hello">
          {sketch.firstName ? `Hello, ${sketch.firstName}` : "Hello"}
        </div>
        {step >= 3 && sketch.tools.length > 0 ? (
          <div className="onb-rows" data-fresh={step === 3 || undefined}>
            {sketch.tools.slice(0, 3).map((toolkit) => (
              <div className="onb-row" key={toolkit}>
                <span className="onb-logo">
                  <ToolkitLogo toolkit={{ toolkit, name: appName(toolkit) }} />
                </span>
                <span>{appName(toolkit)} connected</span>
              </div>
            ))}
          </div>
        ) : (
          <div className="onb-rows">
            <Ghost height={14} />
            <Ghost height={14} />
            <Ghost height={14} width="80%" />
          </div>
        )}
        {step >= 5 && sketch.teamspace?.trim() && (
          <div className="onb-row" data-fresh>
            <span>
              <b>{sketch.teamspace.trim()}</b> · you and Krude
            </span>
          </div>
        )}
      </div>
      <div className="onb-pane">
        {step >= 2 && sketch.agent ? (
          <div className="onb-krude" data-fresh={step === 2 || undefined}>
            <b>
              Krude <span>· only you</span>
            </b>
            <p className="onb-bubble">
              {sketch.agentAway
                ? "I’ll keep watch while you’re away."
                : "Ask me anything. I’ll keep watch on your asks."}
            </p>
          </div>
        ) : (
          <div className="onb-rows">
            <Ghost width="60%" />
            <Ghost height={30} />
          </div>
        )}
        <span className="onb-compose" />
      </div>
    </div>
  )
}

/**
 * The frame every step shares. `onEnter` is Continue's action: Enter
 * anywhere outside a field, a button or a link runs it.
 */
export function OnboardingFrame({
  step,
  stepName,
  scene,
  caption,
  sketch,
  onEnter,
  children,
}: {
  step: number
  stepName: string
  scene: OnboardingScene
  caption: { line: string; sub: string }
  sketch: WorkspaceSketch
  onEnter?: () => void
  children: ReactNode
}) {
  useEffect(() => {
    if (!onEnter) return
    function onKey(event: KeyboardEvent) {
      if (event.key !== "Enter" || event.defaultPrevented) return
      if (event.isComposing || event.shiftKey || event.metaKey) return
      const target = event.target as HTMLElement | null
      if (target?.closest("input, textarea, select, button, a, [role=button]"))
        return
      event.preventDefault()
      onEnter?.()
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [onEnter])

  return (
    <main className="auth-page onb-page">
      <div
        className="auth-scene onb-scene"
        style={
          { "--auth-scene": `url("${sceneImages[scene]}")` } as CSSProperties
        }
      >
        <div className="onb-stage">
          <WorkspaceModel step={step} sketch={sketch} />
          <p className="onb-model-cap">Your workspace, as you set it up</p>
        </div>
        <p className="auth-scene-line" aria-hidden="true">
          <strong>{caption.line}</strong>
          <span>{caption.sub}</span>
        </p>
      </div>
      <div className="auth-side onb-side">
        <div className="onb-top">
          <span className="workspace-wordmark auth-wordmark" aria-label="oryxa">
            oryxa
            <i aria-hidden="true" />
          </span>
          <span className="onb-grow" />
          <span className="onb-count">
            Step {step} of {SETUP_STEP_COUNT} · {stepName}
          </span>
        </div>
        <ol
          className="onb-bar"
          aria-label={`Step ${step} of ${SETUP_STEP_COUNT}`}
        >
          {Array.from({ length: SETUP_STEP_COUNT }, (_, at) => (
            <li
              key={at}
              data-on={at < step || undefined}
              aria-current={at === step - 1 ? "step" : undefined}
            >
              <span className="sr-only">
                Step {at + 1}
                {at < step - 1 ? ", done" : ""}
              </span>
            </li>
          ))}
        </ol>
        <section
          className="onb-form"
          aria-label={`Onboarding step ${step} of ${SETUP_STEP_COUNT}`}
        >
          {children}
        </section>
        <footer className="auth-footer onb-foot">
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

/** A step's heading: the eyebrow, the question and one line on why. */
export function StepIntro({
  eyebrow,
  title,
  lede,
}: {
  eyebrow: string
  title: string
  lede: ReactNode
}) {
  return (
    <div className="onb-intro">
      <p className="onb-eyebrow">{eyebrow}</p>
      <h1>{title}</h1>
      <p className="onb-lede">{lede}</p>
    </div>
  )
}
