// The welcome (founder's redesign, 15 Oct 2026, "Welcome" on the pop-ups
// board): the one pop-up with the wordmark on its scene, the crow's nest,
// and three first steps ticked from what the member has really done. It
// shows once per member and workspace, and only while a step is left.
import { useEffect, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Check } from "lucide-react"
import { Popup, PopupFoot, PopupHead } from "./popup"
import { Button } from "@/components/ui/button"
import { connectionsQueryOptions } from "@/lib/apps/client"
import { membersQueryOptions } from "@/lib/auth/queries"
import { threadListQueryOptions } from "@/lib/threads/client"

/** How long after joining a workspace the member is welcomed. */
export const WELCOME_WITHIN_MS = 14 * 24 * 60 * 60 * 1000

/** Where the welcome remembers it was seen, in this browser. */
export function welcomeKey(organizationId: string, viewerId: string) {
  return `oryxa:welcome:${organizationId}:${viewerId}`
}

function seen(key: string): boolean {
  try {
    return window.localStorage.getItem(key) !== null
  } catch {
    // No storage (a private window, blocked site data): never nag.
    return true
  }
}

function remember(key: string) {
  try {
    window.localStorage.setItem(key, new Date().toISOString())
  } catch {
    // Nothing to keep it in; it shows again next time.
  }
}

type Step = {
  key: "apps" | "team" | "ask"
  label: string
  action: string
  done: boolean
  go: () => void
}

export function WelcomeDialog({
  organizationId,
  viewerId,
  viewerName,
  onConnectApps,
  onInviteTeam,
  onAskAgent,
}: {
  organizationId: string
  viewerId: string
  viewerName?: string
  onConnectApps: () => void
  onInviteTeam: () => void
  onAskAgent: () => void
}) {
  const key = welcomeKey(organizationId, viewerId)
  const [already] = useState(() => seen(key))
  const [open, setOpen] = useState(false)
  const connections = useQuery({
    ...connectionsQueryOptions(organizationId),
    enabled: !already,
    retry: false,
  })
  const members = useQuery({
    ...membersQueryOptions(organizationId),
    enabled: !already,
    retry: false,
  })
  const threads = useQuery({
    ...threadListQueryOptions(organizationId, viewerId),
    enabled: !already,
    retry: false,
  })
  const settled =
    !connections.isPending && !members.isPending && !threads.isPending

  const steps: Step[] = [
    {
      key: "apps",
      label: "Connect your apps",
      action: "Connect your apps",
      done: (connections.data ?? []).some(
        (connection) => connection.status === "active"
      ),
      go: onConnectApps,
    },
    {
      key: "team",
      label: "Invite your team",
      action: "Invite your team",
      done: (members.data?.length ?? 0) > 1,
      go: onInviteTeam,
    },
    {
      key: "ask",
      label: "Ask your agent your first question",
      action: "Ask your agent",
      done: (threads.data?.length ?? 0) > 0,
      go: onAskAgent,
    },
  ]
  const done = steps.filter((step) => step.done).length
  const next = steps.find((step) => !step.done)

  // A welcome is for someone new here: they joined this workspace lately.
  const joinedAt = members.data?.find(
    (member) => member.user_id === viewerId
  )?.joined_at
  const newHere =
    joinedAt !== undefined &&
    Date.now() - Date.parse(joinedAt) < WELCOME_WITHIN_MS

  // Once, when what's done is known and something is left.
  const ready = !already && settled && newHere && next !== undefined
  useEffect(() => {
    if (ready) setOpen(true)
  }, [ready])

  if (already) return null
  const close = () => {
    remember(key)
    setOpen(false)
  }
  const firstName = viewerName?.split(" ")[0]

  return (
    <Popup
      open={open}
      onOpenChange={(opened) => {
        if (!opened) close()
      }}
      scene="crows-nest"
      wordmark
      wide
    >
      <PopupHead
        title={firstName ? `Welcome aboard, ${firstName}` : "Welcome aboard"}
      >
        Your agent works here with you. It reads only what you let it, and asks
        before it acts for you.
      </PopupHead>
      <ol className="popup-steps" aria-label="First steps">
        {steps.map((step, index) => (
          <li key={step.key} data-done={step.done ? "true" : undefined}>
            <span aria-hidden="true">
              {step.done ? <Check size={11} strokeWidth={3} /> : index + 1}
            </span>
            {step.label}
            {step.done && <span className="sr-only">, done</span>}
          </li>
        ))}
      </ol>
      <PopupFoot note={`${done} of ${steps.length} done`}>
        <Button variant="outline" onClick={close}>
          Skip for now
        </Button>
        {next && (
          <Button
            onClick={() => {
              close()
              next.go()
            }}
          >
            {next.action}
          </Button>
        )}
      </PopupFoot>
    </Popup>
  )
}
