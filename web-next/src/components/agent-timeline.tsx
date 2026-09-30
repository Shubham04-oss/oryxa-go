import { isValidElement, useRef, useState } from "react"
import type { MouseEvent, ReactNode } from "react"
import Markdown from "react-markdown"
import type { Components } from "react-markdown"
import remarkGfm from "remark-gfm"
import { ThinkingOrb } from "thinking-orbs"
import type { OrbState } from "thinking-orbs"
import { FileText, Inbox, ShieldQuestion } from "lucide-react"
import { ApiError } from "@/lib/api/client"
import { toolkitName } from "@/lib/apps/client"
import type {
  SpecialistResult,
  SpecialistResultStatus,
} from "@/lib/specialists/result"
import { artifactContentUrl, fetchArtifactLink } from "@/lib/threads/client"
import type { ConfirmationAnswer } from "@/lib/threads/client"
import {
  artifactFileName,
  confirmationHint,
  humanizeToolName,
  isImageArtifact,
} from "@/lib/threads/timeline"
import { featuredSlugs, toolkitIcon } from "./workspace-tools-data"
import type {
  ArtifactEntry,
  ConfirmationEntry,
  MessageEntry,
  TimelineEntry,
  ToolEntry,
  ToolState,
} from "@/lib/threads/timeline"

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
    if (tools.some((tool) => tool.state === state)) return state
  }
  return tools.every((tool) => tool.state === "declined") ? "declined" : "done"
}

/**
 * A call is only still running while its run streams. With no run streaming,
 * one still running was left behind by a run that was stopped or broke off
 * (its end marker never came), so it reads as interrupted, as a draft does.
 */
function settledCalls(
  tools: readonly ToolEntry[],
  running: boolean
): ToolEntry[] {
  if (running) return [...tools]
  return tools.map((tool) =>
    tool.state === "running" ? { ...tool, state: "interrupted" } : tool
  )
}

const toolStateWords: Record<ToolState, string> = {
  running: "Running",
  waiting_approval: "Waiting for you",
  declined: "Declined",
  failed: "Failed",
  interrupted: "Interrupted",
  done: "Done",
}

function toolOrbState(tool: string): OrbState {
  const name = tool.toLowerCase()
  if (/search|knowledge|read|list|source|snapshot/.test(name))
    return "searching"
  if (/connect|browser|navigate/.test(name)) return "connecting"
  if (/create|write|artifact|workflow|update/.test(name)) return "shaping"
  if (/code|sandbox|execute|run/.test(name)) return "solving"
  return "working"
}

const appToolPrefixes = [
  ...featuredSlugs,
  "slack",
  "hubspot",
  "salesforce",
  "apollo",
  "clay",
  "luma",
  "outlook",
  "microsoftteams",
]
const appActionPrefixes = (
  [
    ...appToolPrefixes.map((slug): [string, string] => [slug, slug]),
    ["google_drive", "googledrive"],
    ["google_calendar", "googlecalendar"],
    ["microsoft_teams", "microsoftteams"],
  ] as [string, string][]
).sort(([a], [b]) => b.length - a.length)

/** The app slug is explicit on discovery calls and prefixes Composio's action slugs. */
function calledApp(tool: ToolEntry): string | undefined {
  const name = tool.tool.toLowerCase()
  const arguments_ = tool.arguments
  if (name.startsWith("apps_")) {
    const toolkit = arguments_?.toolkit
    if (typeof toolkit === "string" && /^[\w-]{1,64}$/.test(toolkit))
      return toolkit.toLowerCase()
    if (!/^apps_run_(read|write)_tool$/.test(name)) return undefined
  }
  const action = name.startsWith("apps_")
    ? typeof arguments_?.tool === "string"
      ? arguments_.tool.toLowerCase()
      : undefined
    : name
  if (!action) return undefined
  const normalized = action.replace(/-/g, "_")
  for (const [prefix, slug] of appActionPrefixes) {
    if (normalized.startsWith(`${prefix}_`)) return slug
  }
  if (name.startsWith("apps_") && /^[a-z][a-z0-9]*_/.test(action))
    return action.split("_")[0]
  return undefined
}

function AppCallIcon({ slug }: { slug: string }) {
  const icon = toolkitIcon(slug)
  const name = icon?.title || toolkitName(slug)
  return (
    <span
      className="workspace-agent-tool-app"
      role="img"
      aria-label={name}
      title={name}
    >
      {icon ? (
        <svg viewBox="0 0 24 24" fill={`#${icon.hex}`} aria-hidden="true">
          <path d={icon.path} />
        </svg>
      ) : (
        name.charAt(0).toUpperCase()
      )}
    </span>
  )
}

function toolGroupLabel(tools: ToolEntry[]): string {
  if (tools.length > 1) return `${tools.length} tool calls`
  const tool = tools[0]
  const app = calledApp(tool)
  if (!app) return humanizeToolName(tool.tool) || "Tool call"
  const name = toolkitIcon(app)?.title || toolkitName(app)
  if (tool.tool === "apps_search_tools") return `Find ${name} tools`
  if (tool.tool === "apps_connections") return `Check ${name} connection`
  if (tool.tool === "apps_run_read_tool") return `${name} · Read data`
  if (tool.tool === "apps_run_write_tool") return `${name} · Make change`
  return humanizeToolName(tool.tool) || name
}

function ToolGroup({
  tools: calls,
  running,
}: {
  tools: ToolEntry[]
  /** Whether a run is streaming now. */
  running: boolean
}) {
  const tools = settledCalls(calls, running)
  const state = toolGroupState(tools)
  const active = tools.find((tool) => tool.state === "running") || tools[0]
  const apps = [
    ...new Set(
      tools.map(calledApp).filter((slug): slug is string => Boolean(slug))
    ),
  ]
  const errors = tools.flatMap((tool) => (tool.error ? [tool.error] : []))
  const details = tools
    .map((tool) => `${toolGroupLabel([tool])} · ${toolStateWords[tool.state]}`)
    .join("\n")
  return (
    <div
      className="workspace-agent-tools"
      data-state={state}
      role="group"
      aria-label={details.replaceAll("\n", "; ")}
      title={details}
    >
      <ThinkingOrb
        size={20}
        theme="dark"
        state={
          state === "waiting_approval" ? "listening" : toolOrbState(active.tool)
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
      <ThinkingOrb size={20} theme="dark" state={state} aria-hidden="true" />
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
function WorkResult({ result }: { result: SpecialistResult }) {
  const tools = [
    ...new Set(result.tools_used.map(humanizeToolName).filter(Boolean)),
  ]
  const files = [
    ...new Set(result.artifacts.map(artifactFileName).filter(Boolean)),
  ]
  const words = [
    resultStatusWords[result.status],
    tools.length > 0 ? `Used ${tools.join(", ")}` : undefined,
    files.length > 0 ? `Saved ${files.join(", ")}` : undefined,
    ...result.handoffs.map(
      (handoff) => `Suggests ${handoff.target}: ${handoff.request}`
    ),
  ].filter((part): part is string => Boolean(part))
  if (words.length === 0) return null
  return (
    <p className="workspace-agent-result" data-status={result.status}>
      {words.join(" · ")}
    </p>
  )
}

function MessageBlock({
  entry,
  running,
  agentName,
}: {
  entry: MessageEntry
  running: boolean
  agentName?: string
}) {
  if (entry.role === "member") {
    return (
      <p className="workspace-agent-message" data-from="member">
        {entry.text}
      </p>
    )
  }
  if (entry.role === "apps") return <AppsUpdate entry={entry} />
  // A draft is only still being written while its run streams; one left
  // behind by a stopped or broken run reads as interrupted.
  const streaming = entry.state === "streaming" && running
  const interrupted =
    entry.state === "interrupted" || (entry.state === "streaming" && !running)
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
      {entry.result && <WorkResult result={entry.result} />}
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
          <button
            className="workspace-agent-pill"
            type="button"
            aria-pressed={multiple ? choice === false : undefined}
            disabled={disabled}
            onClick={() => answer(false)}
          >
            Decline
          </button>
          <button
            className="workspace-agent-pill"
            type="button"
            data-primary={multiple ? undefined : "true"}
            aria-pressed={multiple ? choice === true : undefined}
            disabled={disabled}
            onClick={() => answer(true)}
          >
            Approve
          </button>
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

function ArtifactItem({
  entry,
  organizationId,
  threadId,
}: {
  entry: ArtifactEntry
  organizationId: string
  threadId: string
}) {
  const [error, setError] = useState("")
  const [opening, setOpening] = useState(false)
  const url = artifactContentUrl(
    organizationId,
    threadId,
    entry.name,
    entry.version
  )
  const fileName = artifactFileName(entry.name)

  // The plain URL relies on the session cookie, which may have expired since
  // the conversation loaded: read the file's headers first (that refreshes
  // it, and reports a file that is gone), then open or download it.
  async function open(event: MouseEvent<HTMLAnchorElement>) {
    event.preventDefault()
    if (opening) return
    setOpening(true)
    setError("")
    try {
      const link = await fetchArtifactLink(organizationId, threadId, {
        name: entry.name,
        version: entry.version,
      })
      if (link.inline) {
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
  if (isImageArtifact(entry.name)) {
    return (
      <figure className="workspace-agent-artifact">
        <a
          className="workspace-agent-artifact-image"
          href={url}
          aria-label={`Open ${fileName}`}
          aria-busy={opening || undefined}
          onClick={(event) => void open(event)}
        >
          <img src={url} alt={fileName} loading="lazy" />
        </a>
        <figcaption>{fileName}</figcaption>
        {failure}
      </figure>
    )
  }
  return (
    <div className="workspace-agent-artifact">
      <a
        className="workspace-agent-artifact-file"
        href={url}
        download={fileName}
        aria-busy={opening || undefined}
        onClick={(event) => void open(event)}
      >
        <FileText size={15} strokeWidth={1.7} aria-hidden="true" />
        <span>{fileName}</span>
      </a>
      {failure}
    </div>
  )
}

/**
 * A file a run kept, where Go offers no route to read it (a specialist's
 * conversation): its name only, never a link that could not open.
 */
function ArtifactName({ entry }: { entry: ArtifactEntry }) {
  return (
    <div className="workspace-agent-artifact">
      <span className="workspace-agent-artifact-file" data-link="false">
        <FileText size={15} strokeWidth={1.7} aria-hidden="true" />
        <span>{artifactFileName(entry.name)}</span>
      </span>
    </div>
  )
}

/**
 * A conversation's entries, as `buildTimeline` makes them: the member's
 * messages, the agent's answers in Markdown (a growing draft while one
 * streams), tool calls folded into quiet rows, confirmations to answer,
 * the files runs kept, and runs that failed.
 */
export function AgentTimeline({
  entries,
  running,
  agentName,
  actionable,
  submitted,
  answering,
  onAnswer,
  organizationId,
  threadId,
}: {
  entries: readonly TimelineEntry[]
  /** Whether a run is streaming now. */
  running: boolean
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
  const display = groupToolActivity(entries)
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

  const blocks: ReactNode[] = []
  for (const entry of display) {
    switch (entry.kind) {
      case "message":
        blocks.push(
          <MessageBlock
            key={entry.key}
            entry={entry}
            running={running}
            agentName={agentName}
          />
        )
        break
      case "tools":
        blocks.push(
          <ToolGroup key={entry.key} tools={entry.tools} running={running} />
        )
        break
      case "confirmation": {
        const isOpen = open.includes(entry)
        blocks.push(
          <ConfirmationCard
            key={entry.key}
            entry={entry}
            actionable={isOpen}
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
        if (multiple && entry.key === lastOpenKey) {
          blocks.push(
            <div className="workspace-agent-answers" key="answers">
              <button
                className="workspace-agent-pill"
                type="button"
                data-primary="true"
                disabled={
                  answering || open.some((item) => !(item.callId in choices))
                }
                onClick={answerAll}
              >
                Send answers
              </button>
            </div>
          )
        }
        break
      }
      case "artifact":
        blocks.push(
          threadId ? (
            <ArtifactItem
              key={entry.key}
              entry={entry}
              organizationId={organizationId}
              threadId={threadId}
            />
          ) : (
            <ArtifactName key={entry.key} entry={entry} />
          )
        )
        break
      case "run_failed":
        blocks.push(
          <p className="workspace-agent-error" key={entry.key}>
            {entry.error || "Your agent couldn't answer. Try again."}
          </p>
        )
        break
    }
  }
  return <>{blocks}</>
}
