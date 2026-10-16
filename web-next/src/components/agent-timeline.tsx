import {
  Fragment,
  createContext,
  isValidElement,
  useContext,
  useRef,
  useState,
} from "react"
import type { MouseEvent, ReactNode } from "react"
import Markdown from "react-markdown"
import type { Components } from "react-markdown"
import remarkGfm from "remark-gfm"
import { ThinkingOrb } from "thinking-orbs"
import type { OrbState } from "thinking-orbs"
import { Download, FileText, Inbox, ShieldQuestion } from "lucide-react"
import { useQuery } from "@tanstack/react-query"
import { ApiError } from "@/lib/api/client"
import { agentRequestQueryOptions } from "@/lib/agents/queries"
import { isOwnerWritten } from "@/lib/agents/agent-requests"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import {
  isRequestReadRefused,
  requestCardAnswer,
  requestCardPollInterval,
  requestCardState,
} from "@/lib/threads/requests"
import type {
  SpecialistResult,
  SpecialistResultStatus,
} from "@/lib/specialists/result"
import { artifactContentUrl, fetchArtifactLink } from "@/lib/threads/client"
import type { ConfirmationAnswer, Item } from "@/lib/threads/client"
import {
  artifactFileName,
  confirmationHint,
  groupTurns,
  humanizeToolName,
  isImageArtifact,
  runFailureWords,
  turnStatus,
  turnTimes,
} from "@/lib/threads/timeline"
import { cn } from "cn"
import { Button, buttonVariants } from "@/components/ui/button"
import { RunCard, TaskLine } from "./agent-run"
import { FileKindBadge } from "./file-kind"
import { firstName } from "./people-format"
import {
  AppCallIcon,
  calledApp,
  settledCalls,
  toolCallLabel,
  toolOrbState,
  toolStateWords,
} from "./agent-tool-calls"
import type {
  ArtifactEntry,
  ConfirmationEntry,
  MessageEntry,
  RequestEntry,
  TimelineEntry,
  ToolEntry,
  ToolState,
} from "@/lib/threads/timeline"
import "./agent-request-card.css"

/** One agent turn's tool calls, shown as one quiet row. */
export type ToolGroupEntry = { kind: "tools"; key: string; tools: ToolEntry[] }

export type DisplayEntry = Exclude<TimelineEntry, ToolEntry> | ToolGroupEntry

/**
 * Folds an agent turn's tool calls into one entry, even if an agent message,
 * confirmation or file lands between calls. The next member turn begins a
 * fresh row; the intervening entries keep their original place.
 */
export function groupToolActivity(
  entries: readonly TimelineEntry[]
): DisplayEntry[] {
  const grouped: DisplayEntry[] = []
  let group: ToolGroupEntry | undefined
  for (const entry of entries) {
    if (entry.kind === "tool") {
      if (group) group.tools.push(entry)
      else {
        group = { kind: "tools", key: `tools:${entry.key}`, tools: [entry] }
        grouped.push(group)
      }
      continue
    }
    if (
      entry.kind === "run_failed" ||
      (entry.kind === "message" && entry.role !== "agent")
    )
      group = undefined
    grouped.push(entry)
  }
  return grouped
}

/** The state a group of calls shows: whatever still needs attention first. */
export function toolGroupState(tools: readonly ToolEntry[]): ToolState {
  for (const state of [
    "waiting_approval",
    "running",
    "interrupted",
    "failed",
  ] as const) {
    if (
      tools.some(
        (tool) =>
          tool.state === state && !(state === "failed" && tool.recovered)
      )
    )
      return state
  }
  return tools.every((tool) => tool.state === "declined") ? "declined" : "done"
}

function toolGroupLabel(tools: ToolEntry[]): string {
  if (tools.length > 1) return `${tools.length} tool calls`
  return toolCallLabel(tools[0])
}

function ToolGroup({
  tools: calls,
  ended,
}: {
  tools: ToolEntry[]
  /** Whether the run is definitely over (see `AgentTimeline`). */
  ended: boolean
}) {
  const tools = settledCalls(calls, ended)
  const state = toolGroupState(tools)
  const recovered = tools.filter(
    (tool) => tool.state === "failed" && tool.recovered
  )
  const recoveredLabel =
    recovered.length === 1
      ? "Recovered tool error"
      : `${recovered.length} recovered tool errors`
  const active = tools.find((tool) => tool.state === "running") || tools[0]
  const apps = [
    ...new Set(
      tools.map(calledApp).filter((slug): slug is string => Boolean(slug))
    ),
  ]
  const errors = tools.flatMap((tool) =>
    tool.error && !tool.recovered ? [tool.error] : []
  )
  const details = tools
    .map(
      (tool) =>
        `${toolGroupLabel([tool])} · ${tool.recovered ? "Recovered tool error" : toolStateWords[tool.state]}`
    )
    .join("\n")
  return (
    <div className="workspace-agent-tool-group">
      <div
        className="workspace-agent-tools"
        data-state={state}
        role="group"
        aria-label={details.replaceAll("\n", "; ")}
        title={details}
      >
        <ThinkingOrb
          size={20}
          state={
            state === "waiting_approval"
              ? "listening"
              : toolOrbState(active.tool)
          }
          paused={state !== "running" && state !== "waiting_approval"}
          aria-hidden="true"
        />
        <span className="workspace-agent-tools-names">
          {toolGroupLabel(tools)}
        </span>
        {apps.length > 0 && (
          <span className="workspace-agent-tools-apps" aria-label="Apps used">
            {apps.slice(0, 3).map((slug) => (
              <AppCallIcon key={slug} slug={slug} />
            ))}
            {apps.length > 3 && (
              <span className="workspace-agent-tools-more">
                +{apps.length - 3}
              </span>
            )}
          </span>
        )}
        <span
          className="workspace-agent-tools-status"
          role={errors.length > 0 ? "status" : undefined}
          title={errors.length > 0 ? errors.join("\n") : undefined}
          aria-label={
            errors.length > 0
              ? `${toolStateWords[state]}: ${errors.join("; ")}`
              : undefined
          }
        >
          {toolStateWords[state]}
        </span>
      </div>
      {recovered.length > 0 && (
        <p
          className="workspace-agent-tools-recovered"
          role="status"
          title={recovered
            .map((tool) => tool.error)
            .filter(Boolean)
            .join("\n")}
        >
          {recoveredLabel}
        </p>
      )}
    </div>
  )
}

export function AgentActivity({
  name,
  label,
  state = "working",
}: {
  name?: string
  label?: string
  state?: OrbState
}) {
  return (
    <p className="workspace-agent-working" role="status">
      <ThinkingOrb size={20} state={state} aria-hidden="true" />
      <span>{label || (name ? `${name} is working…` : "Working…")}</span>
    </p>
  )
}

function CodeBlock({ children }: { children?: ReactNode }) {
  const code = useRef<HTMLPreElement>(null)
  const [copied, setCopied] = useState(false)
  const language = isValidElement<{ className?: string }>(children)
    ? children.props.className?.match(/(?:^|\s)language-([\w-]+)/)?.[1]
    : undefined
  const wrap = !language || /^(text|plaintext|markdown|md)$/.test(language)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(code.current?.textContent || "")
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1800)
    } catch {
      setCopied(false)
    }
  }
  return (
    <div className="workspace-agent-code-block">
      <div className="workspace-agent-code-bar">
        <span>{language || "Text"}</span>
        <button type="button" onClick={copy} aria-label="Copy code">
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      <pre
        ref={code}
        className={wrap ? "workspace-agent-code-wrap" : undefined}
      >
        {children}
      </pre>
    </div>
  )
}

// Links leave the conversation for a new tab and say nothing about where
// they came from. An image a model writes is offered as a link rather than
// loaded, so an answer cannot fetch from any server on its own.
const markdownComponents: Components = {
  pre: ({ children }) => <CodeBlock>{children}</CodeBlock>,
  a: ({ children, href }) => (
    <a href={href} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  ),
  img: ({ alt, src }) =>
    typeof src === "string" && src ? (
      <a href={src} target="_blank" rel="noopener noreferrer">
        {alt || src}
      </a>
    ) : null,
}
const remarkPlugins = [remarkGfm]

/**
 * An agent's answer, in Markdown. It renders as React elements, never as
 * HTML: HTML in an answer is left out, and a link with an unsafe protocol
 * loses its target, so nothing a model writes runs in the page.
 */
export function AgentMarkdown({ text }: { text: string }) {
  return (
    <div className="workspace-agent-markdown">
      <Markdown components={markdownComponents} remarkPlugins={remarkPlugins}>
        {text}
      </Markdown>
    </div>
  )
}

const resultStatusWords: Record<SpecialistResultStatus, string | undefined> = {
  completed: undefined,
  needs_input: "Needs your input",
  blocked: "Blocked",
}

/**
 * A specialist's work result under its answer, in one quiet line: whether
 * it needs the member or is blocked, the tools it used, the files it saved
 * and the hand-offs it suggests (suggestions only: nothing ran). The
 * answer itself is the result's summary, shown above it.
 */
function WorkResult({
  result,
  session = false,
}: {
  result: SpecialistResult
  /** In a run card, which shows the status, files and hand-offs itself. */
  session?: boolean
}) {
  const tools = [
    ...new Set(result.tools_used.map(humanizeToolName).filter(Boolean)),
  ]
  const files = [
    ...new Set(result.artifacts.map(artifactFileName).filter(Boolean)),
  ]
  const words = (
    session
      ? [tools.length > 0 ? `Used ${tools.join(", ")}` : undefined]
      : [
          resultStatusWords[result.status],
          tools.length > 0 ? `Used ${tools.join(", ")}` : undefined,
          files.length > 0 ? `Saved ${files.join(", ")}` : undefined,
          ...result.handoffs.map(
            (handoff) => `Suggests ${handoff.target}: ${handoff.request}`
          ),
        ]
  ).filter((part): part is string => Boolean(part))
  if (words.length === 0) return null
  return (
    <p className="workspace-agent-result" data-status={result.status}>
      {words.join(" · ")}
    </p>
  )
}

function MessageBlock({
  entry,
  ended,
  agentName,
  session = false,
}: {
  entry: MessageEntry
  /** Whether the run is definitely over (see `AgentTimeline`). */
  ended: boolean
  agentName?: string
  /** Inside a run card (see `WorkResult`). */
  session?: boolean
}) {
  if (entry.role === "member") {
    return (
      <p className="workspace-agent-message" data-from="member">
        {entry.text}
      </p>
    )
  }
  if (entry.role === "apps") return <AppsUpdate entry={entry} />
  // A draft is still being written until its run has definitely ended; one
  // left behind by a stopped or broken run reads as interrupted.
  const streaming = entry.state === "streaming" && !ended
  const interrupted =
    entry.state === "interrupted" || (entry.state === "streaming" && ended)
  // A draft with no words yet is the specialist chat's progress marker: the
  // conversation says the agent is working instead of showing an empty
  // bubble, and one cut short says only that.
  if (!entry.text && !entry.error) {
    if (!interrupted) return null
    return (
      <div
        className="workspace-agent-message"
        data-from="agent"
        data-state="interrupted"
      >
        <p className="workspace-agent-note">Interrupted</p>
      </div>
    )
  }
  // Sub-agents the primary agent hands the turn to write under their own
  // names; the primary agent's own answers need no label in its own tab.
  const labelled = Boolean(entry.author) && entry.author !== agentName
  return (
    <div
      className="workspace-agent-message"
      data-from="agent"
      data-state={
        streaming ? "streaming" : interrupted ? "interrupted" : "complete"
      }
      aria-busy={streaming || undefined}
    >
      {labelled && <p className="workspace-agent-author">{entry.author}</p>}
      <AgentMarkdown text={entry.text} />
      {entry.result && <WorkResult result={entry.result} session={session} />}
      {entry.error && <p className="workspace-agent-note">{entry.error}</p>}
      {interrupted && <p className="workspace-agent-note">Interrupted</p>}
    </div>
  )
}

const appsPreviewLines = 4

/** A batch of events from the member's connected apps that woke the agent: a notice, collapsed when long. */
function AppsUpdate({ entry }: { entry: MessageEntry }) {
  const [expanded, setExpanded] = useState(false)
  const lines = entry.text.split("\n")
  const long = lines.length > appsPreviewLines
  return (
    <div className="workspace-agent-apps">
      <p className="workspace-agent-apps-label">
        <Inbox size={13} strokeWidth={1.8} aria-hidden="true" />
        From your apps
      </p>
      <p className="workspace-agent-apps-text">
        {expanded || !long
          ? entry.text
          : lines.slice(0, appsPreviewLines).join("\n")}
      </p>
      {long && (
        <button
          className="workspace-agent-text-button"
          type="button"
          aria-expanded={expanded}
          onClick={() => setExpanded((open) => !open)}
        >
          {expanded ? "Show less" : "Show more"}
        </button>
      )}
    </div>
  )
}

/** A confirmation the member answers: what would run, why it asks, and Decline and Approve. */
export function ConfirmationCard({
  entry,
  actionable,
  submitted,
  multiple,
  choice,
  disabled,
  onChoose,
  onAnswer,
}: {
  entry: ConfirmationEntry
  actionable: boolean
  submitted?: boolean
  multiple: boolean
  choice?: boolean
  disabled: boolean
  onChoose: (confirmed: boolean) => void
  onAnswer: (confirmed: boolean) => void
}) {
  const status =
    submitted === undefined ? entry.status : submitted ? "approved" : "declined"
  const title = entry.tool ? humanizeToolName(entry.tool) : "Confirmation"
  const answer = (confirmed: boolean) =>
    multiple ? onChoose(confirmed) : onAnswer(confirmed)
  return (
    <div
      className="workspace-agent-confirmation"
      data-status={status}
      role="group"
      aria-label={`Confirm ${title}`}
    >
      <p className="workspace-agent-confirmation-title">
        <ShieldQuestion size={14} strokeWidth={1.8} aria-hidden="true" />
        {title}
      </p>
      <p className="workspace-agent-confirmation-hint">
        {confirmationHint(entry.tool, entry.hint)}
      </p>
      {entry.arguments && Object.keys(entry.arguments).length > 0 && (
        <details>
          <summary>Details</summary>
          <pre>{JSON.stringify(entry.arguments, null, 2)}</pre>
        </details>
      )}
      {actionable ? (
        <div className="workspace-agent-confirmation-actions">
          {/* The action-row convention (DESIGN.md §1): the same size, Decline
              as outline on the left (destructive once chosen), Approve as
              the default on the right. */}
          <Button
            variant={multiple && choice === false ? "destructive" : "outline"}
            size="sm"
            aria-pressed={multiple ? choice === false : undefined}
            disabled={disabled}
            onClick={() => answer(false)}
          >
            Decline
          </Button>
          <Button
            variant={multiple && choice !== true ? "outline" : "default"}
            size="sm"
            aria-pressed={multiple ? choice === true : undefined}
            disabled={disabled}
            onClick={() => answer(true)}
          >
            Approve
          </Button>
        </div>
      ) : (
        status !== "pending" && (
          <p className="workspace-agent-confirmation-status">
            {status === "approved" ? "Approved" : "Declined"}
          </p>
        )
      )}
    </div>
  )
}

/** A file the primary chat holds: its thread, name and version. */
export type ArtifactTarget = {
  threadId: string
  name: string
  version?: number
}

/**
 * Opens a file from the primary chat beside the chat, in the agent session's
 * work column. Without it (nothing provides one), a file opens in a new tab
 * or downloads.
 */
export const OpenArtifactContext = createContext<
  ((file: ArtifactTarget) => void) | null
>(null)

function ArtifactItem({
  entry,
  organizationId,
  threadId,
  card = false,
}: {
  entry: ArtifactEntry
  organizationId: string
  threadId: string
  /** As a run card's output rather than a chip in a log. */
  card?: boolean
}) {
  const openBeside = useContext(OpenArtifactContext)
  const [error, setError] = useState("")
  const [opening, setOpening] = useState(false)
  const url = artifactContentUrl(
    organizationId,
    threadId,
    entry.name,
    entry.version
  )
  const fileName = artifactFileName(entry.name)

  // Where the session offers it, a plain click opens the file in the in-app
  // viewer beside the chat, and downloading is the second way; a click that
  // asks for a new tab or window still gets one. Otherwise the plain URL
  // relies on the session cookie, which may have expired since the
  // conversation loaded: read the file's headers first (that refreshes it,
  // and reports a file that is gone), then open or download it.
  async function open(event: MouseEvent<HTMLElement>, download = false) {
    event.preventDefault()
    const plainClick =
      event.button === 0 && !(event.metaKey || event.ctrlKey || event.shiftKey)
    if (openBeside && !download && plainClick) {
      openBeside({ threadId, name: entry.name, version: entry.version })
      return
    }
    if (opening) return
    setOpening(true)
    setError("")
    try {
      const link = await fetchArtifactLink(organizationId, threadId, {
        name: entry.name,
        version: entry.version,
      })
      if (link.inline && !download) {
        window.open(link.url, "_blank", "noopener,noreferrer")
      } else {
        const anchor = document.createElement("a")
        anchor.href = link.url
        anchor.download = fileName
        document.body.appendChild(anchor)
        anchor.click()
        anchor.remove()
      }
    } catch (failure) {
      setError(
        failure instanceof ApiError && failure.status === 404
          ? "This file is no longer available."
          : "Couldn't open this file. Try again."
      )
    } finally {
      setOpening(false)
    }
  }

  const failure = error && (
    <p className="workspace-agent-error" role="status">
      {error}
    </p>
  )
  const download = openBeside && (
    <a
      className={cn(
        buttonVariants({ variant: "ghost", size: "icon-sm" }),
        "workspace-agent-artifact-download"
      )}
      href={url}
      download={fileName}
      aria-label={`Download ${fileName}`}
      aria-busy={opening || undefined}
      onClick={(event) => void open(event, true)}
    >
      <Download size={14} strokeWidth={1.7} aria-hidden="true" />
    </a>
  )
  if (card) {
    // A run card's output: the file's kind, its name and version, and Open
    // (beside the chat, where the session offers it) and Download.
    // Links, not buttons, so a click that asks for a new tab still gets one.
    return (
      <div className="agent-run-file">
        {isImageArtifact(entry.name) && (
          <img
            className="agent-run-file-preview"
            src={url}
            alt=""
            loading="lazy"
          />
        )}
        <div className="agent-run-file-row">
          <FileKindBadge name={fileName} />
          <span className="agent-run-file-text">
            <strong>{fileName}</strong>
            <small>
              {entry.version ? `Version ${entry.version} · ` : ""}Made in this
              chat
            </small>
          </span>
          <a
            className={cn(
              buttonVariants({ variant: "outline", size: "sm" }),
              "agent-run-file-open"
            )}
            href={url}
            aria-label={`Open ${fileName}`}
            aria-busy={opening || undefined}
            onClick={(event) => void open(event)}
          >
            Open
          </a>
          <a
            className={cn(
              buttonVariants({ variant: "ghost", size: "icon-sm" }),
              "agent-run-file-download"
            )}
            href={url}
            download={fileName}
            aria-label={`Download ${fileName}`}
            onClick={(event) => void open(event, true)}
          >
            <Download strokeWidth={1.7} aria-hidden="true" />
          </a>
        </div>
        {failure}
      </div>
    )
  }
  if (isImageArtifact(entry.name)) {
    return (
      <figure className="workspace-agent-artifact">
        <a
          className="workspace-agent-artifact-image"
          href={url}
          aria-label={`Open ${fileName}`}
          aria-busy={(!openBeside && opening) || undefined}
          onClick={(event) => void open(event)}
        >
          <img src={url} alt={fileName} loading="lazy" />
        </a>
        <figcaption>
          {fileName}
          {download}
        </figcaption>
        {failure}
      </figure>
    )
  }
  return (
    <div className="workspace-agent-artifact">
      <div className="workspace-agent-artifact-row">
        <a
          className="workspace-agent-artifact-file"
          href={url}
          download={openBeside ? undefined : fileName}
          aria-busy={(!openBeside && opening) || undefined}
          onClick={(event) => void open(event)}
        >
          <FileText size={15} strokeWidth={1.7} aria-hidden="true" />
          <span>{fileName}</span>
        </a>
        {download}
      </div>
      {failure}
    </div>
  )
}

/**
 * A file a run kept, where Go offers no route to read it (a specialist's
 * conversation): its name only, never a link that could not open.
 */
function ArtifactName({
  entry,
  card = false,
}: {
  entry: ArtifactEntry
  card?: boolean
}) {
  const fileName = artifactFileName(entry.name)
  if (card)
    return (
      <div className="agent-run-file">
        <div className="agent-run-file-row">
          <FileKindBadge name={fileName} />
          <span className="agent-run-file-text">
            <strong>{fileName}</strong>
            <small>Saved by this run · can't be opened here yet</small>
          </span>
        </div>
      </div>
    )
  return (
    <div className="workspace-agent-artifact">
      <span className="workspace-agent-artifact-file" data-link="false">
        <FileText size={15} strokeWidth={1.7} aria-hidden="true" />
        <span>{fileName}</span>
      </span>
    </div>
  )
}

/** The owner a request was made to, as the asker calls them: a first name, when the request says whose it is. */
function ownerFirstName(request: AgentRequest | undefined): string | undefined {
  const target = request?.target
  const name = target?.user_display_name?.trim() || target?.user_email?.trim()
  return name ? firstName(name) : undefined
}

/** A pointer reads only the request Go authorizes this member to see. */
export function AgentRequestCard({
  entry,
  organizationId,
}: {
  entry: RequestEntry
  organizationId: string
}) {
  const query = useQuery({
    ...agentRequestQueryOptions(organizationId, entry.requestId),
    retry: false,
    refetchInterval: (current) =>
      requestCardPollInterval(current.state.data, current.state.error),
  })
  // A refusal wins over cached data, including a previously visible answer.
  if (isRequestReadRefused(query.error)) return null
  const request = query.data
  const title = request ? `Asked ${request.target.agent_name}` : "Agent request"
  const answer = request && requestCardAnswer(request)
  // What the owner wrote is shown as they wrote it, never read as Markdown.
  const written = request && isOwnerWritten(request)
  return (
    <div className="workspace-agent-request" role="group" aria-label={title}>
      <p className="workspace-agent-confirmation-title">
        <Inbox size={14} strokeWidth={1.8} aria-hidden="true" />
        {title}
      </p>
      {(request?.task || entry.text) && (
        <p className="workspace-agent-request-task">
          {request?.task || entry.text}
        </p>
      )}
      <p className="workspace-agent-note" aria-live="polite">
        {requestCardState(request, entry.status, ownerFirstName(request))}
      </p>
      <div aria-live="polite">
        {answer &&
          (written ? (
            <p className="workspace-agent-request-reply">{answer}</p>
          ) : (
            <AgentMarkdown text={answer} />
          ))}
        {request?.status === "rejected" && request.decision?.message && (
          <p className="workspace-agent-note">{request.decision.message}</p>
        )}
      </div>
      {query.isPending && (
        <p className="workspace-agent-note">Loading request…</p>
      )}
      {query.error && (
        <p className="workspace-agent-note" role="status">
          Couldn't load the latest request.{" "}
          <button
            className="workspace-agent-text-button"
            type="button"
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            Try again
          </button>
        </p>
      )}
    </div>
  )
}

/**
 * A conversation's entries, as `buildTimeline` makes them: the member's
 * messages, the agent's answers in Markdown (a growing draft while one
 * streams), tool calls, confirmations to answer, the files runs kept, and
 * runs that failed.
 *
 * As a `session` (an agent's working session), each task the member gave is
 * a task line with one run card under it (`agent-run.tsx`). As a `log`, the
 * messenger layout: bubbles, and tool calls folded into quiet rows.
 */
export function AgentTimeline({
  entries,
  items,
  variant = "log",
  running,
  ended = !running,
  onStop,
  agentName,
  actionable,
  submitted,
  answering,
  onAnswer,
  organizationId,
  threadId,
}: {
  entries: readonly TimelineEntry[]
  /**
   * The items `entries` were built from, so a session can place each run's
   * start, end and usage in its card (`groupTurns`).
   */
  items?: readonly Item[]
  variant?: "session" | "log"
  /** Whether a run is streaming now. */
  running: boolean
  /**
   * Whether the run is definitely over: nothing streams and nothing can
   * resume, so a draft or a tool call it left unfinished reads as interrupted.
   * Without it, that is whenever nothing streams. A chat that can tell a
   * socket that dropped, or a run about to resume, from a run that ended says
   * so here, and those rows keep their state meanwhile.
   */
  ended?: boolean
  /** Stops the run that streams now; a session's working card offers it. */
  onStop?: () => void
  /** The primary agent's display name; anyone else's messages are labelled. */
  agentName?: string
  /** The confirmations the member can answer now, by call id. */
  actionable: ReadonlySet<string>
  /** Answers sent and not yet recorded, by call id. */
  submitted: Readonly<Record<string, boolean>>
  /** Whether answering is paused, such as while the socket reconnects. */
  answering: boolean
  /** Every waiting confirmation's answer, together: Go wants them in one run. */
  onAnswer: (answers: ConfirmationAnswer[]) => void
  organizationId: string
  /**
   * The primary agent's thread, whose files open through its artifact
   * route. A specialist's conversation has no such route in Go, so without
   * it files are shown by name only.
   */
  threadId?: string
}) {
  const [choices, setChoices] = useState<Record<string, boolean>>({})
  const session = variant === "session"
  const open = entries.filter(
    (entry): entry is ConfirmationEntry =>
      entry.kind === "confirmation" &&
      actionable.has(entry.callId) &&
      !(entry.callId in submitted)
  )
  const multiple = open.length > 1
  const lastOpenKey = open.at(-1)?.key

  function answerAll() {
    onAnswer(
      open.map((entry) => ({
        id: entry.callId,
        confirmed: choices[entry.callId] ?? false,
      }))
    )
    setChoices({})
  }

  function renderEntry(entry: DisplayEntry): ReactNode {
    switch (entry.kind) {
      case "message":
        return (
          <MessageBlock
            key={entry.key}
            entry={entry}
            ended={ended}
            agentName={agentName}
            session={session}
          />
        )
      case "tools":
        return <ToolGroup key={entry.key} tools={entry.tools} ended={ended} />
      case "confirmation": {
        const card = (
          <ConfirmationCard
            key={entry.key}
            entry={entry}
            actionable={open.includes(entry)}
            submitted={submitted[entry.callId]}
            multiple={multiple}
            choice={choices[entry.callId]}
            disabled={answering}
            onChoose={(confirmed) =>
              setChoices((current) => ({
                ...current,
                [entry.callId]: confirmed,
              }))
            }
            onAnswer={(confirmed) =>
              onAnswer([{ id: entry.callId, confirmed }])
            }
          />
        )
        if (!multiple || entry.key !== lastOpenKey) return card
        return (
          <Fragment key={entry.key}>
            {card}
            <div className="workspace-agent-answers">
              <Button
                size="sm"
                disabled={
                  answering || open.some((item) => !(item.callId in choices))
                }
                onClick={answerAll}
              >
                Send answers
              </Button>
            </div>
          </Fragment>
        )
      }
      case "artifact":
        return threadId ? (
          <ArtifactItem
            key={entry.key}
            entry={entry}
            organizationId={organizationId}
            threadId={threadId}
            card={session}
          />
        ) : (
          <ArtifactName key={entry.key} entry={entry} card={session} />
        )
      case "request":
        return (
          <AgentRequestCard
            key={entry.key}
            entry={entry}
            organizationId={organizationId}
          />
        )
      case "run_failed":
        return (
          <p className="workspace-agent-error" key={entry.key}>
            {entry.error || runFailureWords(entry.code).words}
          </p>
        )
    }
  }

  if (!session) return <>{groupToolActivity(entries).map(renderEntry)}</>

  const turns = groupTurns(entries, items)
  const waiting = new Set(open.map((entry) => entry.callId))
  return (
    <>
      {turns.map((turn, index) => {
        const live = index === turns.length - 1 && (running || !ended)
        const { start } = turnTimes(turn, { finished: false })
        return (
          <section className="agent-turn" key={turn.key}>
            {turn.prompt?.role === "member" ? (
              <TaskLine entry={turn.prompt} at={start} />
            ) : (
              turn.prompt && <AppsUpdate entry={turn.prompt} />
            )}
            <RunCard
              turn={turn}
              status={turnStatus(turn, { live, waiting })}
              ended={ended}
              onStop={running ? onStop : undefined}
              renderEntry={renderEntry}
            />
          </section>
        )
      })}
    </>
  )
}
