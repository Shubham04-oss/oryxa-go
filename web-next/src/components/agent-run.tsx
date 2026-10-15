import { useEffect, useRef, useState } from "react"
import type { ReactNode } from "react"
import { ThinkingOrb } from "thinking-orbs"
import {
  ArrowRightLeft,
  ChevronRight,
  CircleAlert,
  CircleCheck,
  CircleSlash,
  Eye,
  Hand,
  Square,
  Wrench,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import type { ItemUsage } from "@/lib/threads/client"
import type { ScreenContext } from "@/lib/agents/screen-context"
import {
  artifactFileName,
  openedLinks,
  toolKeyArgument,
  turnTimes,
  turnUsage,
} from "@/lib/threads/timeline"
import type {
  MessageEntry,
  TimelineEntry,
  ToolEntry,
  Turn,
  TurnStatus,
} from "@/lib/threads/timeline"
import {
  AppCallIcon,
  calledApp,
  settledCalls,
  toolCallLabel,
  toolOrbState,
  toolStateWords,
} from "./agent-tool-calls"
import { FileKindBadge } from "./file-kind"
import "./agent-run.css"

// The agent conversation as a working session (founder, 15 Oct 2026): each
// task the member gives is a line, and the work that answers it is one run
// card under it, with its status, its steps, what it asks the member, what it
// made and its answer. A people chat stays a messenger; this does not.

const statusWords: Record<TurnStatus, string> = {
  working: "Working",
  waiting: "Waiting for you",
  done: "Done",
  interrupted: "Interrupted",
  failed: "Failed",
}

/** A span of time in words: "8s", "1m 12s", "1h 4m". */
export function formatElapsed(ms: number): string {
  const seconds = Math.max(0, Math.round(ms / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) {
    const rest = seconds % 60
    return rest ? `${minutes}m ${rest}s` : `${minutes}m`
  }
  const hours = Math.floor(minutes / 60)
  const rest = minutes % 60
  return rest ? `${hours}h ${rest}m` : `${hours}h`
}

/** Tokens in words: "940 tokens", "12.4k tokens". */
export function formatTokens(usage: ItemUsage): string {
  const total = usage.total_tokens
  if (total < 1000) return `${total} tokens`
  const thousands = total / 1000
  return `${thousands < 10 ? thousands.toFixed(1) : Math.round(thousands)}k tokens`
}

/** When something happened, as a task line says it: "09:41", or "14 Oct, 09:41" before today. */
export function formatTaskTime(at: number, now = Date.now()): string {
  const date = new Date(at)
  const clock = date.toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
  })
  if (new Date(now).toDateString() === date.toDateString()) return clock
  const day = date.toLocaleDateString([], { day: "numeric", month: "short" })
  return `${day}, ${clock}`
}

/** The time now, renewed every second while `active`. */
function useNow(active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    setNow(Date.now())
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [active])
  return now
}

/**
 * The member's task, as the line that heads its run: left-aligned, no
 * bubble, with who and when above it.
 */
export function TaskLine({
  entry,
  at,
}: {
  entry: MessageEntry
  /** When the task was given, in ms; the line says only "You" without it. */
  at?: number
}) {
  return (
    <div className="agent-task">
      <p className="agent-task-meta">
        You{at !== undefined && <> · {formatTaskTime(at)}</>}
      </p>
      <p className="agent-task-text workspace-agent-message" data-from="member">
        {entry.text}
      </p>
    </div>
  )
}

function StatusMark({ status }: { status: TurnStatus }) {
  if (status === "working" || status === "waiting")
    return (
      <ThinkingOrb
        size={20}
        state={status === "waiting" ? "listening" : "working"}
        aria-hidden="true"
      />
    )
  const Icon =
    status === "done"
      ? CircleCheck
      : status === "failed"
        ? CircleAlert
        : CircleSlash
  return (
    <Icon
      className="agent-run-mark"
      size={16}
      strokeWidth={1.8}
      aria-hidden="true"
    />
  )
}

function StepRow({ tool }: { tool: ToolEntry }) {
  const app = calledApp(tool)
  const detail = toolKeyArgument(tool.arguments)
  const failed = tool.state === "failed" && !tool.recovered
  return (
    <li className="agent-run-step" data-state={tool.state}>
      <span className="agent-run-step-icon">
        {app ? (
          <AppCallIcon slug={app} />
        ) : tool.state === "running" ? (
          <ThinkingOrb
            size={20}
            state={toolOrbState(tool.tool)}
            aria-hidden="true"
          />
        ) : (
          <Wrench size={13} strokeWidth={1.8} aria-hidden="true" />
        )}
      </span>
      <span className="agent-run-step-text">
        <span className="agent-run-step-name">{toolCallLabel(tool)}</span>
        {detail && <span className="agent-run-step-detail">{detail}</span>}
        {failed && tool.error && (
          <span className="agent-run-step-error">{tool.error}</span>
        )}
      </span>
      <span className="agent-run-step-state">
        {tool.state === "failed" && tool.recovered
          ? "Recovered"
          : toolStateWords[tool.state]}
      </span>
    </li>
  )
}

/**
 * The run's tool calls, one line each, under a summary that opens them. They
 * show open while the run works or waits, and folded once it is over, until
 * the member opens or folds them.
 */
function RunSteps({
  tools,
  status,
  links,
}: {
  tools: readonly ToolEntry[]
  status: TurnStatus
  links: readonly URL[]
}) {
  const [chosen, setChosen] = useState<boolean | undefined>(undefined)
  const open = chosen ?? (status === "working" || status === "waiting")
  const failed = tools.filter(
    (tool) => tool.state === "failed" && !tool.recovered
  ).length
  const apps = [
    ...new Set(
      tools.map(calledApp).filter((slug): slug is string => Boolean(slug))
    ),
  ]
  const summary = [
    tools.length === 1 ? "1 step" : `${tools.length} steps`,
    failed > 0 ? `${failed} failed` : undefined,
  ]
    .filter(Boolean)
    .join(" · ")
  return (
    <Collapsible
      className="agent-run-steps"
      open={open}
      onOpenChange={setChosen}
    >
      <CollapsibleTrigger
        render={
          <Button
            variant="ghost"
            size="sm"
            className="agent-run-steps-toggle"
          />
        }
      >
        <ChevronRight
          className="agent-run-chevron"
          size={14}
          strokeWidth={1.8}
          aria-hidden="true"
        />
        <span>{summary}</span>
        {apps.length > 0 && (
          <span className="workspace-agent-tools-apps" aria-label="Apps used">
            {apps.slice(0, 4).map((slug) => (
              <AppCallIcon key={slug} slug={slug} />
            ))}
          </span>
        )}
      </CollapsibleTrigger>
      <CollapsibleContent keepMounted>
        <ol className="agent-run-step-list" aria-label="Steps">
          {tools.map((tool) => (
            <StepRow key={tool.key} tool={tool} />
          ))}
        </ol>
        {links.length > 0 && (
          <p className="agent-run-opened">
            <span>Opened</span>
            {links.map((url) => (
              <a
                key={url.href}
                href={url.href}
                target="_blank"
                rel="noopener noreferrer"
                title={url.href}
              >
                {url.hostname.replace(/^www\./, "")}
              </a>
            ))}
          </p>
        )}
      </CollapsibleContent>
    </Collapsible>
  )
}

/**
 * The work that answered one task, as a card: a status and how long it took
 * (and what it used, where the run said), Stop while it runs, its steps, the
 * approvals it waits for, what it made, and its answer.
 */
export function RunCard({
  turn,
  status,
  ended,
  onStop,
  renderEntry,
}: {
  turn: Turn
  status: TurnStatus
  /** Whether the run is definitely over (see `AgentTimeline`). */
  ended: boolean
  /** Stops the run; offered only while it works. */
  onStop?: () => void
  /** Renders a block of the run that is not a tool call. */
  renderEntry: (entry: Exclude<TimelineEntry, ToolEntry>) => ReactNode
}) {
  const active = status === "working" || status === "waiting"
  const now = useNow(active)
  const { start, end } = turnTimes(turn, { finished: !active })
  const elapsed =
    start === undefined
      ? undefined
      : active
        ? now - start
        : end !== undefined
          ? end - start
          : undefined
  const usage = turnUsage(turn)
  const tools = settledCalls(
    turn.entries.filter((entry): entry is ToolEntry => entry.kind === "tool"),
    ended
  )
  const of = (kinds: readonly TimelineEntry["kind"][]) =>
    turn.entries.filter(
      (entry): entry is Exclude<TimelineEntry, ToolEntry> =>
        entry.kind !== "tool" && kinds.includes(entry.kind)
    )
  const approvals = of(["confirmation"])
  const outputs = of(["artifact", "request"])
  const answers = of(["message"])
  const failures = of(["run_failed"])
  // A specialist's work result says more than its run did: whether it needs
  // the member or is blocked, the files it saved (by name; Go serves no
  // specialist files yet), and the hand-offs it suggests.
  const result = answers
    .flatMap((entry) =>
      entry.kind === "message" && entry.result ? [entry.result] : []
    )
    .at(-1)
  const label =
    status === "done" && result?.status === "needs_input"
      ? "Needs your input"
      : status === "done" && result?.status === "blocked"
        ? "Blocked"
        : statusWords[status]
  const kept = new Set(
    outputs.flatMap((entry) =>
      entry.kind === "artifact" ? [artifactFileName(entry.name)] : []
    )
  )
  const savedFiles = [
    ...new Set((result?.artifacts ?? []).map(artifactFileName)),
  ].filter((name) => name && !kept.has(name))
  const handoffs = result?.handoffs ?? []
  const meta = [
    elapsed !== undefined && elapsed >= 1000
      ? formatElapsed(elapsed)
      : undefined,
    usage && !active ? formatTokens(usage) : undefined,
  ].filter(Boolean)

  return (
    <article
      className="agent-run"
      data-status={status}
      aria-label={`Run: ${label}`}
      data-result={result?.status}
      aria-busy={status === "working" || undefined}
    >
      <header className="agent-run-head">
        <StatusMark status={status} />
        <Badge className="agent-run-status" variant="outline">
          {status === "waiting" && (
            <Hand data-icon="inline-start" aria-hidden="true" />
          )}
          {label}
        </Badge>
        {meta.length > 0 && (
          <span className="agent-run-meta">{meta.join(" · ")}</span>
        )}
        {status === "working" && onStop && (
          <Button
            className="agent-run-stop"
            variant="ghost"
            size="sm"
            onClick={onStop}
          >
            <Square data-icon="inline-start" fill="currentColor" />
            Stop
          </Button>
        )}
      </header>
      {tools.length > 0 && (
        <RunSteps tools={tools} status={status} links={openedLinks(tools)} />
      )}
      {approvals.length > 0 && (
        <div className="agent-run-section">{approvals.map(renderEntry)}</div>
      )}
      {(outputs.length > 0 || savedFiles.length > 0 || handoffs.length > 0) && (
        <div className="agent-run-section agent-run-outputs">
          {outputs.map(renderEntry)}
          {savedFiles.map((name) => (
            <div className="agent-run-file" key={`saved:${name}`}>
              <div className="agent-run-file-row">
                <FileKindBadge name={name} />
                <span className="agent-run-file-text">
                  <strong>{name}</strong>
                  <small>Saved by this run · can't be opened here yet</small>
                </span>
              </div>
            </div>
          ))}
          {handoffs.map((handoff, index) => (
            <p className="agent-run-handoff" key={`handoff:${index}`}>
              <ArrowRightLeft size={13} strokeWidth={1.8} aria-hidden="true" />
              <span>
                Suggests <strong>{handoff.target}</strong>: {handoff.request}
              </span>
            </p>
          ))}
        </div>
      )}
      {answers.length > 0 && (
        <div className="agent-run-answer">{answers.map(renderEntry)}</div>
      )}
      {failures.map(renderEntry)}
    </article>
  )
}

/** The page a screen context names, in words: what it selects, or the view. */
export function screenContextLabel(
  context: ScreenContext | undefined
): string | undefined {
  if (!context) return undefined
  if (context.selection?.name) return context.selection.name
  const views: Record<string, string> = {
    "home/overview": "My workspace",
    "agents/overview": "Agent overview",
    "agents/file": "this file",
    "team/teamspace": "this teamspace",
  }
  if (views[context.view]) return views[context.view]
  const page = context.view.split("/")[1]
  if (!page) return undefined
  const words = page.replace(/-/g, " ")
  return words.charAt(0).toUpperCase() + words.slice(1)
}

/**
 * What the agent will be told the member is looking at when they send: the
 * page's own description, read as the message would read it. It is renewed
 * while the composer is in view, and says nothing when the page names
 * nothing.
 */
export function LookingAt({
  getScreenContext,
}: {
  getScreenContext: () => ScreenContext
}) {
  const [label, setLabel] = useState<string>()
  const read = useRef(getScreenContext)
  read.current = getScreenContext
  useEffect(() => {
    const update = () => {
      try {
        setLabel(screenContextLabel(read.current()))
      } catch {
        setLabel(undefined)
      }
    }
    update()
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") update()
    }, 2000)
    return () => clearInterval(timer)
  }, [])
  if (!label) return null
  return (
    <span
      className="agent-looking-at"
      title="Sent with your message, so your agent knows what you mean"
    >
      <Eye size={12} strokeWidth={1.8} aria-hidden="true" />
      <span>Looking at {label}</span>
    </span>
  )
}
