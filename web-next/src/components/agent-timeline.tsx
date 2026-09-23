import { useState } from "react"
import type { MouseEvent, ReactNode } from "react"
import Markdown from "react-markdown"
import type { Components } from "react-markdown"
import remarkGfm from "remark-gfm"
import {
  Ban,
  CircleAlert,
  CircleCheck,
  FileText,
  Inbox,
  LoaderCircle,
  ShieldQuestion,
} from "lucide-react"
import { ApiError } from "@/lib/api/client"
import { artifactContentUrl, fetchArtifactLink } from "@/lib/threads/client"
import type { ConfirmationAnswer } from "@/lib/threads/client"
import {
  artifactFileName,
  confirmationHint,
  humanizeToolName,
  isImageArtifact,
} from "@/lib/threads/timeline"
import type {
  ArtifactEntry,
  ConfirmationEntry,
  MessageEntry,
  TimelineEntry,
  ToolEntry,
  ToolState,
} from "@/lib/threads/timeline"

/** Consecutive tool calls, shown as one quiet row. */
export type ToolGroupEntry = { kind: "tools"; key: string; tools: ToolEntry[] }

export type DisplayEntry = Exclude<TimelineEntry, ToolEntry> | ToolGroupEntry

/**
 * Folds each run of consecutive tool calls into one entry. Messages,
 * confirmations, files and failures stay boundaries between groups.
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
    group = undefined
    grouped.push(entry)
  }
  return grouped
}

/** The state a group of calls shows: whatever still needs attention first. */
export function toolGroupState(tools: readonly ToolEntry[]): ToolState {
  for (const state of ["waiting_approval", "running", "failed"] as const) {
    if (tools.some((tool) => tool.state === state)) return state
  }
  return tools.every((tool) => tool.state === "declined") ? "declined" : "done"
}

const toolStateWords: Record<ToolState, string> = {
  running: "Running",
  waiting_approval: "Waiting for you",
  declined: "Declined",
  failed: "Failed",
  done: "Done",
}

function ToolStateIcon({ state }: { state: ToolState }) {
  const props = { size: 14, strokeWidth: 1.8, "aria-hidden": true }
  switch (state) {
    case "running":
      return <LoaderCircle {...props} className="workspace-agent-spin" />
    case "waiting_approval":
      return <ShieldQuestion {...props} />
    case "failed":
      return <CircleAlert {...props} />
    case "declined":
      return <Ban {...props} />
    case "done":
      return <CircleCheck {...props} />
  }
}

function ToolGroup({ tools }: { tools: ToolEntry[] }) {
  const state = toolGroupState(tools)
  const names = tools.map((tool) => humanizeToolName(tool.tool) || "A tool")
  const status =
    tools.length > 1
      ? `${tools.length} calls · ${toolStateWords[state]}`
      : toolStateWords[state]
  const errors = tools.flatMap((tool) => (tool.error ? [tool.error] : []))
  return (
    <div
      className="workspace-agent-tools"
      data-state={state}
      title={errors.length > 0 ? errors.join("\n") : undefined}
    >
      <ToolStateIcon state={state} />
      <span className="workspace-agent-tools-names">{names.join(" · ")}</span>
      <span className="workspace-agent-tools-status">{status}</span>
    </div>
  )
}

// Links leave the conversation for a new tab and say nothing about where
// they came from. An image a model writes is offered as a link rather than
// loaded, so an answer cannot fetch from any server on its own.
const markdownComponents: Components = {
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

function ConfirmationCard({
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
  threadId: string
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
        blocks.push(<ToolGroup key={entry.key} tools={entry.tools} />)
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
          <ArtifactItem
            key={entry.key}
            entry={entry}
            organizationId={organizationId}
            threadId={threadId}
          />
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
