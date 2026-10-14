// Setting up a new workspace (founder's redesign, 14 Oct 2026): after naming
// it, four steps in the sign-in layout, each with its scene. Every step does
// real work through the existing APIs, and every one can be skipped:
//   agent     — meet Krude; turn on background access (the agent grant)
//   tools     — connect the apps work happens in
//   crew      — invite people by email
//   teamspace — open the first teamspace (a team of the same name, then it)
// The step lives in the page's address (`?setup=`), so an app's or the
// grant's sign-in page returns to the step it left.
import { useState } from "react"
import type { FormEvent, ReactNode } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowRight, Check } from "lucide-react"
import { AuthNotice, AuthShell } from "./auth/auth-shell"
import type { AuthScene } from "./auth/auth-shell"
import { AuthField } from "./auth/auth-field"
import { ToolkitLogo } from "./workspace-tool-logo"
import { connectRedirect } from "./workspace-tool-helpers"
import { ApiError } from "@/lib/api/client"
import { authorizeAgentGrant, getAgentGrant } from "@/lib/auth/settings"
import { sendInvitation } from "@/lib/auth/invitations"
import { connect, connectionsQueryOptions } from "@/lib/apps/client"
import { createTeam } from "@/lib/settings/client"
import { createTeamspace } from "@/lib/teamspaces/client"
import "./onboarding.css"

export const setupSteps = ["agent", "tools", "crew", "teamspace"] as const
export type SetupStep = (typeof setupSteps)[number]

export function isSetupStep(value: unknown): value is SetupStep {
  return setupSteps.includes(value as SetupStep)
}

const stepNames: Record<SetupStep, string> = {
  agent: "Krude",
  tools: "Tools",
  crew: "Crew",
  teamspace: "Teamspace",
}

const stepScenes: Record<
  SetupStep,
  { scene: AuthScene; line: string; sub: string }
> = {
  agent: {
    scene: "nest",
    line: "An agent in the crow’s nest.",
    sub: "It keeps watch, so you don’t have to.",
  },
  tools: {
    scene: "deck",
    line: "Rig the ship.",
    sub: "Each app you connect, Krude can read.",
  },
  crew: {
    scene: "ships",
    line: "One crew. Every ask answered.",
    sub: "Your team and its agents, in one place.",
  },
  teamspace: {
    scene: "sea",
    line: "Every ask is a message in a bottle.",
    sub: "oryxa makes sure it reaches the right person.",
  },
}

/** The apps offered first, as the Tools page features them. */
const starterApps = [
  { toolkit: "slack", name: "Slack", what: "Channels and messages" },
  { toolkit: "googledrive", name: "Google Drive", what: "Docs and sheets" },
  { toolkit: "notion", name: "Notion", what: "Pages" },
  { toolkit: "github", name: "GitHub", what: "Code and issues" },
  { toolkit: "gmail", name: "Gmail", what: "Email asks" },
  { toolkit: "linear", name: "Linear", what: "Issues" },
]

/** This page at another step of setup, for a sign-in page to return to. */
function setupHref(organizationId: string, step: SetupStep): string {
  const url = new URL(window.location.href)
  url.hash = ""
  url.search = ""
  url.searchParams.set("org", organizationId)
  url.searchParams.set("setup", step)
  return url.toString()
}

/** The email addresses in what was typed: split on commas, spaces or lines. */
export function emailsIn(text: string): string[] {
  return [
    ...new Set(
      text
        .split(/[\s,;]+/)
        .map((part) => part.trim().toLowerCase())
        .filter(Boolean)
    ),
  ]
}

export function Onboarding({
  organizationId,
  organizationName,
  step,
  onStep,
  onDone,
}: {
  organizationId: string
  organizationName: string
  step: SetupStep
  onStep: (step: SetupStep) => void
  /** Setup is over: open the workspace (on the new teamspace, if one was made). */
  onDone: (teamspaceId?: string) => void
}) {
  const index = setupSteps.indexOf(step)
  const next = () =>
    index < setupSteps.length - 1 ? onStep(setupSteps[index + 1]) : onDone()
  const back = index > 0 ? () => onStep(setupSteps[index - 1]) : undefined
  const art = stepScenes[step]

  return (
    <AuthShell scene={art.scene} caption={{ line: art.line, sub: art.sub }}>
      <ol className="setup-steps" aria-label={`Step ${index + 2} of 5`}>
        <li className="is-done">Workspace</li>
        {setupSteps.map((key, at) => (
          <li
            key={key}
            className={at < index ? "is-done" : at === index ? "is-now" : ""}
            aria-current={at === index ? "step" : undefined}
          >
            {stepNames[key]}
          </li>
        ))}
      </ol>
      {step === "agent" && (
        <AgentStep organizationId={organizationId} onNext={next} />
      )}
      {step === "tools" && (
        <ToolsStep organizationId={organizationId} onNext={next} back={back} />
      )}
      {step === "crew" && (
        <CrewStep
          organizationId={organizationId}
          organizationName={organizationName}
          onNext={next}
          back={back}
        />
      )}
      {step === "teamspace" && (
        <TeamspaceStep
          organizationId={organizationId}
          onDone={onDone}
          back={back}
        />
      )}
    </AuthShell>
  )
}

function StepNav({
  back,
  skip,
  primary,
}: {
  back?: () => void
  skip?: () => void
  primary: ReactNode
}) {
  return (
    <div className="setup-nav">
      {back && (
        <button type="button" className="setup-back" onClick={back}>
          Back
        </button>
      )}
      <span className="setup-grow" />
      {skip && (
        <button type="button" className="setup-skip" onClick={skip}>
          Skip
        </button>
      )}
      {primary}
    </div>
  )
}

function AgentStep({
  organizationId,
  onNext,
}: {
  organizationId: string
  onNext: () => void
}) {
  const grant = useQuery({
    queryKey: ["auth", "agent-grant", organizationId],
    queryFn: getAgentGrant,
    retry: false,
  })
  const turnOn = useMutation({
    mutationFn: () => authorizeAgentGrant(setupHref(organizationId, "agent")),
    onSuccess: ({ authorization_url }) =>
      window.location.assign(
        connectRedirect(authorization_url, window.location.origin)
      ),
  })
  const granted = grant.data?.granted === true

  return (
    <>
      <h1>Meet Krude, your agent.</h1>
      <p className="auth-subtitle">
        Krude works for you: it reads what you share, keeps watch on requests,
        and takes notes in parleys. Only you see your chats with it.
      </p>
      <div className={granted ? "setup-option is-on" : "setup-option"}>
        <span className="setup-grow">
          <strong>Works while you’re away</strong>
          <small>
            {granted
              ? "On. Krude can act for you in the background."
              : "Krude can answer and follow up in the background, within what you allow."}
          </small>
        </span>
        {granted ? (
          <span className="setup-on">
            <Check size={15} strokeWidth={2.4} /> On
          </span>
        ) : (
          <button
            type="button"
            className="setup-small"
            disabled={grant.isPending || turnOn.isPending}
            onClick={() => turnOn.mutate()}
          >
            {turnOn.isPending ? "Opening…" : "Turn on"}
          </button>
        )}
      </div>
      {turnOn.isError && (
        <AuthNotice>Couldn’t open that page. Try again.</AuthNotice>
      )}
      <StepNav
        skip={granted ? undefined : onNext}
        primary={
          <button
            className="auth-submit setup-primary"
            type="button"
            onClick={onNext}
          >
            Continue <ArrowRight size={17} />
          </button>
        }
      />
    </>
  )
}

function ToolsStep({
  organizationId,
  onNext,
  back,
}: {
  organizationId: string
  onNext: () => void
  back?: () => void
}) {
  const queryClient = useQueryClient()
  const connections = useQuery({
    ...connectionsQueryOptions(organizationId),
    retry: false,
  })
  const active = new Set(
    (connections.data ?? [])
      .filter((connection) => connection.status === "active")
      .map((connection) => connection.toolkit)
  )
  const [problem, setProblem] = useState("")
  const connectApp = useMutation({
    mutationFn: async (toolkit: string) => {
      const started = await connect(organizationId, {
        toolkit,
        returnTo: setupHref(organizationId, "tools"),
      })
      window.location.assign(
        connectRedirect(started.redirect_url, window.location.origin)
      )
    },
    onError: async (error) => {
      // apps keeps one pending-or-active connection per app and member.
      if (error instanceof ApiError && error.code === "conflict") {
        await queryClient.invalidateQueries({
          queryKey: connectionsQueryOptions(organizationId).queryKey,
        })
        return
      }
      setProblem("Couldn’t start connecting that app. Try again.")
    },
  })

  return (
    <>
      <h1>Connect where work happens.</h1>
      <p className="auth-subtitle">
        Krude and your team’s agents read only what these apps already let you
        see. You can add more from Tools later.
      </p>
      {problem && <AuthNotice>{problem}</AuthNotice>}
      <ul className="setup-apps" aria-label="Apps to connect">
        {starterApps.map((app) => {
          const on = active.has(app.toolkit)
          return (
            <li key={app.toolkit} className={on ? "is-on" : undefined}>
              <span className="setup-app-logo">
                <ToolkitLogo toolkit={app} />
              </span>
              <span className="setup-grow">
                <strong>{app.name}</strong>
                <small>{on ? "Connected" : app.what}</small>
              </span>
              {on ? (
                <Check
                  size={16}
                  strokeWidth={2.4}
                  aria-label={`${app.name} connected`}
                />
              ) : (
                <button
                  type="button"
                  className="setup-small"
                  aria-label={`Connect ${app.name}`}
                  disabled={connectApp.isPending}
                  onClick={() => {
                    setProblem("")
                    connectApp.mutate(app.toolkit)
                  }}
                >
                  {connectApp.isPending && connectApp.variables === app.toolkit
                    ? "Opening…"
                    : "Connect"}
                </button>
              )}
            </li>
          )
        })}
      </ul>
      <StepNav
        back={back}
        skip={active.size === 0 ? onNext : undefined}
        primary={
          <button
            className="auth-submit setup-primary"
            type="button"
            onClick={onNext}
          >
            Continue <ArrowRight size={17} />
          </button>
        }
      />
    </>
  )
}

function CrewStep({
  organizationId,
  organizationName,
  onNext,
  back,
}: {
  organizationId: string
  organizationName: string
  onNext: () => void
  back?: () => void
}) {
  const [emailError, setEmailError] = useState("")
  const [sent, setSent] = useState<string[]>([])
  const [failed, setFailed] = useState<string[]>([])
  const invite = useMutation({
    mutationFn: async (emails: string[]) => {
      const results = await Promise.allSettled(
        emails.map((email) => sendInvitation(organizationId, email, "member"))
      )
      return emails.map((email, at) => ({
        email,
        ok: results[at].status === "fulfilled",
      }))
    },
    onSuccess: (results) => {
      setSent((current) => [
        ...current,
        ...results.filter((r) => r.ok).map((r) => r.email),
      ])
      setFailed(results.filter((r) => !r.ok).map((r) => r.email))
    },
  })

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const emails = emailsIn(String(new FormData(form).get("emails") || ""))
    const invalid = emails.filter(
      (email) => !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email)
    )
    if (emails.length === 0) {
      setEmailError("Add at least one email address.")
      return
    }
    if (invalid.length > 0) {
      setEmailError(`Check ${invalid.join(", ")}.`)
      return
    }
    setEmailError("")
    invite.mutate(emails, { onSuccess: () => form.reset() })
  }

  return (
    <>
      <h1>Bring your crew aboard.</h1>
      <p className="auth-subtitle">
        Invite people to {organizationName}. Everyone gets their own agent.
        Invitations expire after seven days.
      </p>
      {failed.length > 0 && (
        <AuthNotice>
          Couldn’t invite {failed.join(", ")}. They may already be here.
        </AuthNotice>
      )}
      {sent.length > 0 && (
        <ul className="setup-sent" aria-label="Invited">
          {sent.map((email) => (
            <li key={email}>
              <Check size={14} strokeWidth={2.4} /> {email}
            </li>
          ))}
        </ul>
      )}
      <form className="auth-form" onSubmit={submit} noValidate>
        <AuthField
          error={emailError}
          label="Email addresses"
          name="emails"
          placeholder="raya@company.com, priya@company.com"
          autoComplete="off"
        />
        <button
          className="auth-submit setup-secondary"
          type="submit"
          disabled={invite.isPending}
        >
          {invite.isPending ? "Sending…" : "Send invitations"}
        </button>
      </form>
      <StepNav
        back={back}
        skip={sent.length === 0 ? onNext : undefined}
        primary={
          <button
            className="auth-submit setup-primary"
            type="button"
            onClick={onNext}
          >
            Continue <ArrowRight size={17} />
          </button>
        }
      />
    </>
  )
}

function TeamspaceStep({
  organizationId,
  onDone,
  back,
}: {
  organizationId: string
  onDone: (teamspaceId?: string) => void
  back?: () => void
}) {
  const [nameError, setNameError] = useState("")
  const create = useMutation({
    mutationFn: async (name: string) => {
      const team = await createTeam(organizationId, name)
      return createTeamspace(organizationId, { team_id: team.id, name })
    },
    onSuccess: (teamspace) => onDone(teamspace.id),
  })

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const name = String(
      new FormData(event.currentTarget).get("name") || ""
    ).trim()
    if (!name || name.length > 100) {
      setNameError(name ? "Use 100 characters or fewer." : "Give it a name.")
      return
    }
    setNameError("")
    create.mutate(name)
  }

  return (
    <>
      <h1>Open your first teamspace.</h1>
      <p className="auth-subtitle">
        A teamspace is a room, its files and its parleys, shared by people and
        their agents.
      </p>
      {create.isError && (
        <AuthNotice>
          {create.error instanceof ApiError && create.error.status === 409
            ? "That name is taken. Try another."
            : "Couldn’t open the teamspace. Try again."}
        </AuthNotice>
      )}
      <form
        className="auth-form"
        onSubmit={submit}
        noValidate
        id="setup-teamspace"
      >
        <AuthField
          error={nameError}
          label="Name"
          name="name"
          placeholder="EU launch"
          autoComplete="off"
        />
      </form>
      <StepNav
        back={back}
        skip={() => onDone()}
        primary={
          <button
            className="auth-submit setup-primary"
            type="submit"
            form="setup-teamspace"
            disabled={create.isPending}
          >
            {create.isPending ? "Opening…" : "Open teamspace"}{" "}
            <ArrowRight size={17} />
          </button>
        }
      />
    </>
  )
}
