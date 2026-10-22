// Setting up a new workspace (onboarding v3, founder's redesign of 15 Oct
// 2026; boards OnbV3s1–s5 and Onb3Model). Naming it is step 1 (routes/index
// NewWorkspace); these are steps 2 to 5, in OnboardingFrame. Every step does
// real work through the existing APIs, and every one can be skipped:
//   agent     — meet the agent: name it, turn on background access (the agent grant)
//   tools     — connect the apps work happens in
//   crew      — invite people by email
//   teamspace — open the first teamspace (a team of the same name, then it)
// The step lives in the page's address (`?setup=`), so an app's or the
// grant's sign-in page returns to the step it left.
import { useState } from "react"
import type { FormEvent, ReactNode } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check } from "lucide-react"
import { Button } from "@/components/ui/button"
import { AuthNotice } from "./auth/auth-shell"
import { AuthField } from "./auth/auth-field"
import { OnboardingFrame, StepIntro } from "./onboarding-frame"
import type { OnboardingScene, WorkspaceSketch } from "./onboarding-frame"
import { ToolkitLogo } from "./workspace-tool-logo"
import { connectRedirect } from "./workspace-tool-helpers"
import { personIdentity } from "./teamspace-format"
import { IdentityAvatar } from "./workspace-avatar"
import { ApiError } from "@/lib/api/client"
import { authorizeAgentGrant, getAgentGrant } from "@/lib/auth/settings"
import { getInvitations, sendInvitation } from "@/lib/auth/invitations"
import { connect, connectionsQueryOptions } from "@/lib/apps/client"
import { createTeam } from "@/lib/settings/client"
import { createTeamspace } from "@/lib/teamspaces/client"
import { updateAgent } from "@/lib/agents/managed-agents"
import type { ManagedAgent } from "@/lib/agents/managed-agents"
import { primaryAgent } from "@/lib/agents/policy"
import { agentKeys, agentsQueryOptions } from "@/lib/agents/queries"

/** The longest name Go keeps for an agent (managedagents' maxNameRunes). */
const MAX_AGENT_NAME = 60

export const setupSteps = ["agent", "tools", "crew", "teamspace"] as const
export type SetupStep = (typeof setupSteps)[number]

export function isSetupStep(value: unknown): value is SetupStep {
  return setupSteps.includes(value as SetupStep)
}

const stepNames: Record<SetupStep, string> = {
  agent: "Your agent",
  tools: "Tools",
  crew: "Crew",
  teamspace: "Teamspace",
}

/** Each step's scene and caption; a caption may name the member's agent. */
const stepScenes: Record<
  SetupStep,
  { scene: OnboardingScene; line: string; sub: (agent: string) => string }
> = {
  agent: {
    scene: "nest",
    line: "An agent in the crow’s nest.",
    sub: () => "It keeps watch, so you don’t have to.",
  },
  tools: {
    scene: "deck",
    line: "Charts for every sea.",
    sub: (agent) => `${agent} reads where your work already lives.`,
  },
  crew: {
    scene: "ships",
    line: "A crew, and their agents.",
    sub: () => "Everyone keeps their own agent.",
  },
  teamspace: {
    scene: "sea",
    line: "Your first harbour.",
    sub: () => "A teamspace is where a crew works on one thing.",
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

/** Back on the left; Skip for now and the primary on the right (DESIGN.md §1). */
export function StepNav({
  back,
  backLabel = "Back",
  skip,
  primary,
}: {
  back?: () => void
  backLabel?: string
  skip?: () => void
  primary: ReactNode
}) {
  return (
    <div className="onb-nav">
      {back && (
        <Button variant="ghost" onClick={back}>
          {backLabel}
        </Button>
      )}
      <span className="onb-grow" />
      {skip && (
        <Button variant="ghost" onClick={skip}>
          Skip for now
        </Button>
      )}
      {primary}
    </div>
  )
}

/** The primary action, with its Enter hint. */
export function ContinueButton({
  children = "Continue",
  ...props
}: {
  children?: ReactNode
  onClick?: () => void
  type?: "button" | "submit"
  form?: string
  disabled?: boolean
}) {
  return (
    <Button className="onb-next" {...props}>
      {children}
      <kbd aria-hidden="true">↵</kbd>
    </Button>
  )
}

export function Onboarding({
  organizationId,
  organizationName,
  viewerName,
  step,
  onStep,
  onDone,
}: {
  organizationId: string
  organizationName: string
  /** The member's name, for the miniature's greeting. */
  viewerName?: string
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
  const [teamspaceName, setTeamspaceName] = useState("")
  const [sent, setSent] = useState<string[]>([])
  // The name being typed in the agent step, for the miniature; undefined
  // until the member types, so the miniature shows the saved name.
  const [typedAgentName, setTypedAgentName] = useState<string>()

  // What the miniature shows: the member's real setup, read where it lives.
  const grant = useQuery({
    queryKey: ["auth", "agent-grant", organizationId],
    queryFn: getAgentGrant,
    retry: false,
  })
  const connections = useQuery({
    ...connectionsQueryOptions(organizationId),
    retry: false,
  })
  const agents = useQuery({
    ...agentsQueryOptions(organizationId),
    retry: false,
  })
  const agent = agents.data ? primaryAgent(agents.data) : undefined
  const agentName =
    (step === "agent" ? typedAgentName?.trim() : undefined) ||
    agent?.name ||
    "Krude"
  const invitations = useQuery({
    queryKey: ["organizations", organizationId, "invitations", "setup"],
    queryFn: () => getInvitations(organizationId),
    retry: false,
  })
  const tools = [
    ...new Set(
      (connections.data ?? [])
        .filter((connection) => connection.status === "active")
        .map((connection) => connection.toolkit)
    ),
  ]
  const invited = [
    ...new Set([
      ...sent,
      ...(invitations.data ?? [])
        .filter((invitation) => invitation.status === "pending")
        .map((invitation) => invitation.email),
    ]),
  ]
  const first = viewerName?.includes("@")
    ? undefined
    : viewerName?.trim().split(/\s+/)[0]
  const sketch: WorkspaceSketch = {
    workspaceName: organizationName,
    firstName: first,
    agent: true,
    agentName,
    agentAway: grant.data?.granted === true,
    tools,
    invited,
    teamspace: teamspaceName,
  }
  const submitTeamspace = () =>
    (
      document.getElementById("setup-teamspace") as HTMLFormElement | null
    )?.requestSubmit()
  // Continue in the agent step saves the name first, when there is a field
  // to save it from; without one (the agent could not be read) it moves on.
  const submitAgent = () => {
    const form = document.getElementById(
      "setup-agent"
    ) as HTMLFormElement | null
    if (form) form.requestSubmit()
    else next()
  }

  return (
    <OnboardingFrame
      step={index + 2}
      stepName={stepNames[step]}
      scene={art.scene}
      caption={{ line: art.line, sub: art.sub(agentName) }}
      sketch={sketch}
      onEnter={
        step === "teamspace"
          ? submitTeamspace
          : step === "agent"
            ? submitAgent
            : next
      }
    >
      {step === "agent" && (
        <AgentStep
          organizationId={organizationId}
          agent={agent}
          agentPending={agents.isPending}
          agentName={agentName}
          onNameChange={setTypedAgentName}
          granted={grant.data?.granted === true}
          checking={grant.isPending}
          onNext={next}
        />
      )}
      {step === "tools" && (
        <ToolsStep
          organizationId={organizationId}
          active={new Set(tools)}
          onNext={next}
          back={back}
        />
      )}
      {step === "crew" && (
        <CrewStep
          organizationId={organizationId}
          organizationName={organizationName}
          invited={invited}
          onSent={(emails) =>
            setSent((current) => [...new Set([...current, ...emails])])
          }
          onNext={next}
          back={back}
        />
      )}
      {step === "teamspace" && (
        <TeamspaceStep
          organizationId={organizationId}
          onNameChange={setTeamspaceName}
          onDone={onDone}
          back={back}
        />
      )}
    </OnboardingFrame>
  )
}

function AgentStep({
  organizationId,
  agent,
  agentPending,
  agentName,
  onNameChange,
  granted,
  checking,
  onNext,
}: {
  organizationId: string
  /** The member's primary agent, once it has been read. */
  agent?: ManagedAgent
  agentPending: boolean
  /** What to call it: the name as typed, else as saved. */
  agentName: string
  onNameChange: (name: string) => void
  granted: boolean
  checking: boolean
  onNext: () => void
}) {
  const queryClient = useQueryClient()
  const [nameError, setNameError] = useState("")
  const rename = useMutation({
    mutationFn: (name: string) =>
      updateAgent(organizationId, agent!.id, { name }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: agentKeys.agentList(organizationId),
      })
      onNext()
    },
  })
  const turnOn = useMutation({
    mutationFn: () => authorizeAgentGrant(setupHref(organizationId, "agent")),
    onSuccess: ({ authorization_url }) =>
      window.location.assign(
        connectRedirect(authorization_url, window.location.origin)
      ),
  })

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const name = String(
      new FormData(event.currentTarget).get("name") || ""
    ).trim()
    if (!name || name.length > MAX_AGENT_NAME) {
      setNameError(
        name
          ? `Use ${MAX_AGENT_NAME} characters or fewer.`
          : "Give your agent a name."
      )
      return
    }
    setNameError("")
    // The name it already has needs no save.
    if (name === agent?.name) return onNext()
    rename.mutate(name)
  }

  return (
    <>
      <StepIntro
        eyebrow="Meet your agent"
        title="What should your agent be called?"
        lede="It works for you, and only you: it reads what you share, keeps watch on requests, and takes notes in parleys. Only you see your chats with it."
      />
      {rename.isError && (
        <AuthNotice>
          {rename.error instanceof ApiError && rename.error.status === 409
            ? "Another agent in this workspace already has that name. Try another."
            : "Couldn’t save that name. Try again."}
        </AuthNotice>
      )}
      {agent ? (
        <form
          className="auth-form"
          onSubmit={submit}
          noValidate
          id="setup-agent"
          // The miniature shows the name as it is typed.
          onChange={(event) =>
            onNameChange(
              String(new FormData(event.currentTarget).get("name") ?? "")
            )
          }
        >
          <AuthField
            // Read once the agent is: a field rendered before would keep its
            // empty start.
            key={agent.id}
            defaultValue={agent.name}
            error={nameError}
            label="Name"
            name="name"
            autoComplete="off"
          />
        </form>
      ) : agentPending ? (
        <p className="onb-note">Getting your agent ready…</p>
      ) : (
        <p className="onb-note">
          Your agent couldn’t be loaded. You can name it later in Settings →
          Your agent.
        </p>
      )}
      <div className="onb-option" data-on={granted || undefined}>
        <span className="onb-grow">
          <strong>Works while you’re away</strong>
          <small>
            {granted
              ? `On. ${agentName} can act for you in the background.`
              : `${agentName} can answer and follow up in the background, within what you allow.`}
          </small>
        </span>
        {granted ? (
          <span className="onb-on">
            <Check size={15} strokeWidth={2.4} /> On
          </span>
        ) : (
          <Button
            variant="outline"
            size="sm"
            disabled={checking || turnOn.isPending}
            onClick={() => turnOn.mutate()}
          >
            {turnOn.isPending ? "Opening…" : "Turn on"}
          </Button>
        )}
      </div>
      {turnOn.isError && (
        <AuthNotice>Couldn’t open that page. Try again.</AuthNotice>
      )}
      <StepNav
        skip={granted ? undefined : onNext}
        primary={
          agent ? (
            <ContinueButton
              type="submit"
              form="setup-agent"
              disabled={rename.isPending}
            >
              {rename.isPending ? "Saving…" : "Continue"}
            </ContinueButton>
          ) : (
            <ContinueButton onClick={onNext} disabled={agentPending} />
          )
        }
      />
    </>
  )
}

function ToolsStep({
  organizationId,
  active,
  onNext,
  back,
}: {
  organizationId: string
  active: Set<string>
  onNext: () => void
  back?: () => void
}) {
  const queryClient = useQueryClient()
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
      <StepIntro
        eyebrow="Connect your tools"
        title="Where does your work live?"
        lede="Krude reads only what you connect, and only for you. Two is plenty to start; add the rest from Tools any time."
      />
      {problem && <AuthNotice>{problem}</AuthNotice>}
      <ul className="onb-apps" aria-label="Apps to connect">
        {starterApps.map((app) => {
          const on = active.has(app.toolkit)
          return (
            <li key={app.toolkit} data-on={on || undefined}>
              <span className="onb-app-logo">
                <ToolkitLogo toolkit={app} />
              </span>
              <span className="onb-grow">
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
                <Button
                  variant="outline"
                  size="sm"
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
                </Button>
              )}
            </li>
          )
        })}
      </ul>
      <StepNav
        back={back}
        skip={active.size === 0 ? onNext : undefined}
        primary={<ContinueButton onClick={onNext} />}
      />
    </>
  )
}

function CrewStep({
  organizationId,
  organizationName,
  invited,
  onSent,
  onNext,
  back,
}: {
  organizationId: string
  organizationName: string
  invited: string[]
  onSent: (emails: string[]) => void
  onNext: () => void
  back?: () => void
}) {
  const [emailError, setEmailError] = useState("")
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
      onSent(results.filter((r) => r.ok).map((r) => r.email))
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
      <StepIntro
        eyebrow="Invite your crew"
        title="Who do you work with most?"
        lede={`Invite people to ${organizationName}. Each person gets their own Krude, and agents only see what their person shares. Invitations expire after seven days.`}
      />
      {failed.length > 0 && (
        <AuthNotice>
          Couldn’t invite {failed.join(", ")}. They may already be here.
        </AuthNotice>
      )}
      <form className="onb-invite" onSubmit={submit} noValidate>
        <AuthField
          error={emailError}
          label="Email addresses"
          name="emails"
          placeholder="raya@company.com, priya@company.com"
          autoComplete="off"
        />
        <Button variant="outline" type="submit" disabled={invite.isPending}>
          {invite.isPending ? "Sending…" : "Send invitations"}
        </Button>
      </form>
      {invited.length > 0 && (
        <ul className="onb-people" aria-label="Invited">
          {invited.map((email) => (
            <li key={email}>
              <IdentityAvatar identity={personIdentity(email, email)} />
              <span className="onb-grow">{email}</span>
              <small>Invited · Member</small>
            </li>
          ))}
        </ul>
      )}
      <StepNav
        back={back}
        skip={invited.length === 0 ? onNext : undefined}
        primary={<ContinueButton onClick={onNext} />}
      />
    </>
  )
}

function TeamspaceStep({
  organizationId,
  onNameChange,
  onDone,
  back,
}: {
  organizationId: string
  onNameChange: (name: string) => void
  onDone: (teamspaceId?: string) => void
  back?: () => void
}) {
  const [nameError, setNameError] = useState("")
  const create = useMutation({
    mutationFn: async (teamspace: string) => {
      const team = await createTeam(organizationId, teamspace)
      return createTeamspace(organizationId, {
        team_id: team.id,
        name: teamspace,
      })
    },
    onSuccess: (teamspace) => onDone(teamspace.id),
  })

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const trimmed = String(
      new FormData(event.currentTarget).get("name") || ""
    ).trim()
    if (!trimmed || trimmed.length > 100) {
      setNameError(trimmed ? "Use 100 characters or fewer." : "Give it a name.")
      return
    }
    setNameError("")
    create.mutate(trimmed)
  }

  return (
    <>
      <StepIntro
        eyebrow="Your first teamspace"
        title="What is the crew working on?"
        lede="A teamspace holds one piece of work: its room, its files and its parleys, shared by people and their agents."
      />
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
        // The miniature shows the name as it is typed.
        onChange={(event) =>
          onNameChange(
            String(new FormData(event.currentTarget).get("name") ?? "")
          )
        }
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
          <ContinueButton
            type="submit"
            form="setup-teamspace"
            disabled={create.isPending}
          >
            {create.isPending ? "Opening…" : "Open my workspace"}
          </ContinueButton>
        }
      />
    </>
  )
}
