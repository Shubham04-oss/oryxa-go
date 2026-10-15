// The first run of My workspace (board OnbFirstRunV3, founder's redesign of
// 15 Oct 2026): the same centred page, right after setup, with a Getting
// started tile in place of While you were away. Each step is ticked from a
// real read, never assumed; a read that fails or is not served leaves its
// step out, and the count says how many are known.
import { useQuery } from "@tanstack/react-query"
import { Check } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { getInvitations, getMembers } from "@/lib/auth/invitations"
import { organizationKeys } from "@/lib/auth/queries"
import { teamspacesQueryOptions } from "@/lib/teamspaces/queries"

export type StartStep = {
  key: "grant" | "tools" | "crew" | "teamspace" | "ask"
  title: string
  /** What doing it takes, or that it is done. */
  detail: string
  done: boolean
  open?: () => void
}

/** The five steps, as far as the reads can tell; `undefined` means unknown. */
export function startSteps(
  known: {
    granted?: boolean
    tools?: boolean
    crew?: boolean
    teamspace?: boolean
    asked?: boolean
  },
  open: Partial<Record<StartStep["key"], () => void>> = {}
): StartStep[] {
  const steps: Array<
    Omit<StartStep, "done" | "detail"> & {
      done?: boolean
      todo: string
    }
  > = [
    {
      key: "grant",
      title: "Let Krude work while you’re away",
      todo: "Turn on background access",
      done: known.granted,
    },
    {
      key: "tools",
      title: "Connect your tools",
      todo: "Slack, Drive, Notion and more",
      done: known.tools,
    },
    {
      key: "crew",
      title: "Invite your crew",
      todo: "Everyone gets their own agent",
      done: known.crew,
    },
    {
      key: "teamspace",
      title: "Open a teamspace",
      todo: "A room for a team and its agents",
      done: known.teamspace,
    },
    {
      key: "ask",
      title: "Ask Krude something",
      todo: "Use the box above",
      done: known.asked,
    },
  ]
  return steps
    .filter((step) => step.done !== undefined)
    .map(({ todo, done, ...step }) => ({
      ...step,
      done: Boolean(done),
      detail: done ? "Done" : todo,
      open: done ? undefined : open[step.key],
    }))
}

/** The crew step: someone else is a member, or an invitation is out. */
export function useCrewStep(
  organizationId: string,
  viewerId: string,
  enabled: boolean
): boolean | undefined {
  // The members read shares the key usePeopleNames scopes to the viewer.
  const members = useQuery({
    queryKey: [...organizationKeys.members(organizationId), viewerId],
    queryFn: () => getMembers(organizationId),
    staleTime: 60_000,
    enabled,
    retry: false,
  })
  const invitations = useQuery({
    queryKey: ["organizations", organizationId, "invitations", viewerId],
    queryFn: () => getInvitations(organizationId),
    enabled,
    retry: false,
  })
  if (members.data && members.data.length > 1) return true
  if (invitations.data?.some((invitation) => invitation.status === "pending"))
    return true
  // Only members who may list invitations can tell "none sent yet".
  if (members.isSuccess && invitations.isSuccess) return false
  return undefined
}

export function useTeamspaceStep(
  organizationId: string,
  enabled: boolean
): boolean | undefined {
  const list = useQuery({
    ...teamspacesQueryOptions(organizationId),
    enabled,
  })
  return list.isSuccess ? list.data.length > 0 : undefined
}

export function HomeGettingStarted({
  steps,
  onHide,
}: {
  steps: StartStep[]
  onHide?: () => void
}) {
  const done = steps.filter((step) => step.done).length
  const left = steps.length - done
  return (
    <section className="home-column" aria-labelledby="home-start">
      <header className="home-column-head">
        <h2 id="home-start">Getting started</h2>
        {steps.length > 0 && (
          <Badge
            className="home-count"
            variant="secondary"
            aria-label={`${done} of ${steps.length} done`}
          >
            {done} of {steps.length}
          </Badge>
        )}
        {onHide && (
          <Button variant="ghost" size="sm" onClick={onHide}>
            {left === 0 ? "Done" : "Hide"}
          </Button>
        )}
      </header>
      {steps.length === 0 ? (
        <p className="home-quiet" role="status">
          Checking your setup…
        </p>
      ) : (
        <ul className="home-list" aria-label="Getting started">
          {steps.map((step) => (
            <li key={step.key}>
              <div className="home-item" data-done={step.done || undefined}>
                <span className="home-item-mark">
                  <span className="home-check" aria-hidden="true">
                    {step.done && <Check />}
                  </span>
                </span>
                <span className="home-item-copy">
                  <span>{step.title}</span>
                  <small>{step.detail}</small>
                </span>
                {step.open && (
                  <Button
                    className="home-item-action"
                    variant="outline"
                    size="sm"
                    aria-label={step.title}
                    onClick={step.open}
                  >
                    Start
                  </Button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
      {steps.length > 0 && left === 0 && (
        <p className="home-quiet">You’re set up. Krude takes it from here.</p>
      )}
    </section>
  )
}
