// Pieces the Agents-mode views share: entry cards, status pills, notices,
// a confirm step, workflow rows and the focused view's work column.
import { useQuery } from "@tanstack/react-query"
import type { ReactNode } from "react"
import {
  ArrowLeft,
  ArrowUpRight,
  ChevronRight,
  GitBranch,
  MoreHorizontal,
} from "lucide-react"
import { IdentityAvatar } from "./workspace-avatar"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  agentIdentity,
  formatDate,
  plural,
  statusLabel,
  timeAgo,
} from "./agents-format"
import type { AgentTeam } from "@/lib/agents/agent-teams"
import type {
  AgentWorkflow,
  WorkflowExecution,
  WorkflowSchedule,
} from "@/lib/agents/agent-workflows"
import type { ManagedAgent } from "@/lib/agents/managed-agents"
import { isExecutionInFlight } from "@/lib/agents/policy"
import { workflowExecutionsQueryOptions } from "@/lib/agents/queries"
import { cn } from "@/lib/utils"

export function StatusPill({ status }: { status: "active" | "disabled" }) {
  return (
    <span className="workspace-agents-status" data-status={status}>
      {statusLabel(status)}
    </span>
  )
}

/** An empty, unavailable or failed state, said plainly. */
export function AgentsNotice({
  title,
  children,
  action,
  alert = false,
}: {
  title: string
  children?: ReactNode
  action?: ReactNode
  alert?: boolean
}) {
  return (
    <div className="workspace-agents-notice" role={alert ? "alert" : undefined}>
      <strong>{title}</strong>
      {children && <p>{children}</p>}
      {action}
    </div>
  )
}

/**
 * An agent's voxel avatar beside its visible name. Hidden from assistive
 * technology there, so the name is not read out twice.
 */
export function AgentFace({
  agent,
  className,
}: {
  agent: { id: string; name: string }
  className?: string
}) {
  return (
    <span className={cn("workspace-agents-face", className)} aria-hidden="true">
      <IdentityAvatar identity={agentIdentity(agent)} />
    </span>
  )
}

/**
 * A few overlapping voxel avatars: a team's coordinator and specialists.
 * Decorative: the team's name and roster are always written next to it.
 */
export function AgentAvatarStack({
  agents,
  max = 4,
}: {
  agents: Array<{ id: string; name: string }>
  max?: number
}) {
  return (
    <span className="workspace-agents-stack" aria-hidden="true">
      {agents.slice(0, max).map((agent) => (
        <IdentityAvatar identity={agentIdentity(agent)} key={agent.id} />
      ))}
      {agents.length > max && (
        <span className="workspace-agents-stack-more">
          +{agents.length - max}
        </span>
      )}
    </span>
  )
}

/** Everyone who works in a team, coordinator first. */
export function teamAgents(team: AgentTeam) {
  return [
    { id: team.coordinator_agent_id, name: team.coordinator_name },
    ...team.members.map((member) => ({
      id: member.agent_id,
      name: member.name,
    })),
  ]
}

export function SpecialistEntry({
  agent,
  onOpen,
}: {
  agent: ManagedAgent
  onOpen: () => void
}) {
  return (
    <button
      className="workspace-entry"
      type="button"
      aria-label={`Open ${agent.name}`}
      data-screen-context-kind="specialist"
      data-screen-context-id={agent.id}
      data-screen-context-name={agent.name}
      onClick={onOpen}
    >
      <span className="workspace-entry-kind">
        <IdentityAvatar identity={agentIdentity(agent)} />
        Specialist
        <StatusPill status={agent.status} />
      </span>
      <span className="workspace-entry-title">
        <strong>{agent.name}</strong>
        <ArrowUpRight size={17} strokeWidth={1.7} />
      </span>
      <span className="workspace-entry-description">
        {agent.description || "No description yet."}
      </span>
      <span className="workspace-agents-entry-meta">
        Created {formatDate(agent.created_at)}
      </span>
    </button>
  )
}

export function AgentTeamEntry({
  team,
  onOpen,
}: {
  team: AgentTeam
  onOpen: () => void
}) {
  return (
    <button
      className="workspace-entry"
      type="button"
      aria-label={`Open ${team.name}`}
      data-screen-context-kind="agent_team"
      data-screen-context-id={team.id}
      data-screen-context-name={team.name}
      onClick={onOpen}
    >
      <span className="workspace-entry-kind">
        <AgentAvatarStack agents={teamAgents(team)} />
        Multi-agent team
        <StatusPill status={team.status} />
      </span>
      <span className="workspace-entry-title">
        <strong>{team.name}</strong>
        <ArrowUpRight size={17} strokeWidth={1.7} />
      </span>
      <span className="workspace-entry-description">
        {team.description || "No purpose written yet."}
      </span>
      <span className="workspace-agents-entry-meta">
        {plural(team.members.length, "specialist")} · Led by{" "}
        {team.coordinator_name}
      </span>
    </button>
  )
}

/**
 * A team on Agent overview (AgentsNew board): its chain, the coordinator
 * leading to each specialist, then its name, purpose and status. The whole
 * row opens the team.
 */
export function AgentTeamRow({
  team,
  onOpen,
}: {
  team: AgentTeam
  onOpen: () => void
}) {
  const members = [...team.members].sort(
    (left, right) => left.position - right.position
  )
  return (
    <button
      className="agents-team-row"
      type="button"
      aria-label={`Open ${team.name}`}
      data-screen-context-kind="agent_team"
      data-screen-context-id={team.id}
      data-screen-context-name={team.name}
      onClick={onOpen}
    >
      <span className="agents-team-chain" aria-hidden="true">
        <IdentityAvatar
          identity={agentIdentity({
            id: team.coordinator_agent_id,
            name: team.coordinator_name,
          })}
        />
        {members.length > 0 && <ChevronRight className="agents-team-arrow" />}
        <AgentAvatarStack
          agents={members.map((member) => ({
            id: member.agent_id,
            name: member.name,
          }))}
          max={3}
        />
      </span>
      <span className="agents-team-copy">
        <strong>{team.name}</strong>
        <small>
          {[
            team.description || undefined,
            `led by ${team.coordinator_name}, ${plural(members.length, "specialist")}`,
          ]
            .filter(Boolean)
            .join(" · ")}
        </small>
      </span>
      <StatusPill status={team.status} />
    </button>
  )
}

/**
 * A specialist on Agent overview: a filled card with its name, kind and
 * status, what it does, and when it was paused (Go keeps no actor, so the
 * card says when, never who). Its ⋯ menu opens, pauses or resumes, and edits.
 */
export function SpecialistCard({
  agent,
  busy = false,
  onOpen,
  onToggle,
  onEdit,
}: {
  agent: ManagedAgent
  /** Its pause or resume is on its way. */
  busy?: boolean
  onOpen: () => void
  onToggle: () => void
  onEdit: () => void
}) {
  const paused = agent.status !== "active"
  return (
    <article
      className="agents-specialist-card"
      aria-label={agent.name}
      data-status={agent.status}
      data-screen-context-kind="specialist"
      data-screen-context-id={agent.id}
      data-screen-context-name={agent.name}
    >
      <div className="agents-specialist-head">
        <AgentFace agent={agent} />
        <button
          className="agents-specialist-open"
          type="button"
          aria-label={`Open ${agent.name}`}
          onClick={onOpen}
        >
          <strong>{agent.name}</strong>
          <small>Specialist · {statusLabel(agent.status)}</small>
        </button>
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={`More for ${agent.name}`}
                disabled={busy}
              />
            }
          >
            <MoreHorizontal />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onClick={onOpen}>Open</DropdownMenuItem>
            <DropdownMenuItem onClick={onToggle}>
              {paused ? "Resume" : "Pause"}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={onEdit}>Edit</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <p className="agents-specialist-description">
        {agent.description || "No description yet."}
      </p>
      <small className="agents-specialist-meta">
        {busy
          ? paused
            ? "Resuming…"
            : "Pausing…"
          : paused
            ? `Paused · updated ${formatDate(agent.updated_at)}`
            : `Created ${formatDate(agent.created_at)}`}
      </small>
    </article>
  )
}

/**
 * The second step of a destructive action. `blocked` explains a refusal the
 * server would give, known in advance, and keeps the action unavailable.
 */
export function ConfirmStep({
  question,
  detail,
  blocked,
  confirmLabel,
  pendingLabel,
  pending,
  error,
  onConfirm,
  onCancel,
}: {
  question: string
  detail?: string
  blocked?: string
  confirmLabel: string
  pendingLabel: string
  pending: boolean
  error?: string
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <div
      className="workspace-agents-confirm"
      role="group"
      aria-label={question}
    >
      <p>
        <strong>{question}</strong>
        {detail && <> {detail}</>}
      </p>
      {blocked && <p className="workspace-agents-note">{blocked}</p>}
      {error && (
        <p className="workspace-agents-error" role="alert">
          {error}
        </p>
      )}
      <div className="workspace-agents-actions">
        <button
          className="workspace-agents-button"
          type="button"
          onClick={onCancel}
          autoFocus
        >
          Cancel
        </button>
        <button
          className="workspace-agents-button"
          data-variant="danger"
          type="button"
          disabled={pending || Boolean(blocked)}
          onClick={onConfirm}
        >
          {pending ? pendingLabel : confirmLabel}
        </button>
      </div>
    </div>
  )
}

function scheduleLabel(schedule?: WorkflowSchedule): string {
  if (!schedule) return "Runs when started"
  const when = `${schedule.expression}, ${schedule.timezone}`
  return schedule.state === "enabled" ? when : `Schedule off (${when})`
}

const executionWords: Record<WorkflowExecution["status"], string> = {
  queued: "Queued",
  running: "Running now",
  succeeded: "Succeeded",
  needs_confirmation: "Waiting for your approval",
  failed: "Failed",
}

function executionLabel(execution: WorkflowExecution): string {
  if (isExecutionInFlight(execution)) return executionWords[execution.status]
  const at =
    execution.completed_at ?? execution.started_at ?? execution.created_at
  return `${executionWords[execution.status]} ${timeAgo(at)}`
}

/** Recent executions shown per workflow, newest first. */
export const RECENT_EXECUTIONS = 5

/**
 * One workflow and its recent executions, read-only: the executions query
 * re-reads every 2 s while one is queued or running.
 */
export function WorkflowRow({
  organizationId,
  workflow,
  teamName,
}: {
  organizationId: string
  workflow: AgentWorkflow
  teamName?: string
}) {
  const executions = useQuery(
    workflowExecutionsQueryOptions(
      organizationId,
      workflow.id,
      RECENT_EXECUTIONS
    )
  )
  const latest = executions.data?.[0]
  const details = [
    teamName,
    scheduleLabel(workflow.schedule),
    workflow.status === "disabled" ? "Paused" : undefined,
  ].filter(Boolean)
  return (
    <li
      className="workspace-agents-workflow"
      data-screen-context-kind="workflow"
      data-screen-context-id={workflow.id}
      data-screen-context-name={workflow.name}
    >
      <span className="workspace-agents-workflow-icon" aria-hidden="true">
        <GitBranch size={16} strokeWidth={1.7} />
      </span>
      <span className="workspace-agents-workflow-main">
        <strong>{workflow.name}</strong>
        <small>{details.join(" · ")}</small>
      </span>
      <span className="workspace-agents-workflow-runs">
        <span
          className="workspace-agents-workflow-last"
          data-status={latest?.status}
        >
          {executions.isPending
            ? "Reading runs…"
            : executions.isError
              ? "Runs unavailable"
              : latest
                ? executionLabel(latest)
                : "Not run yet"}
        </span>
        {executions.data && executions.data.length > 0 && (
          // The latest run is read out above; the dots are its history at
          // a glance, each with its own label on hover.
          <span className="workspace-agents-workflow-dots" aria-hidden="true">
            {executions.data.map((execution) => (
              <span
                className="workspace-agents-workflow-dot"
                data-status={execution.status}
                key={execution.id}
                title={executionLabel(execution)}
              />
            ))}
          </span>
        )}
      </span>
    </li>
  )
}

/**
 * The focused view's work column (DESIGN.md §3): tabs above the panel, the
 * selected view's heading and state at the panel's top.
 */
export function AgentsWorkColumn<TTab extends string>({
  label,
  tabs,
  tab,
  onTab,
  title,
  description,
  status,
  children,
}: {
  label: string
  tabs: readonly TTab[]
  tab: TTab
  onTab: (tab: TTab) => void
  title: string
  description?: string
  status?: ReactNode
  children: ReactNode
}) {
  return (
    <div className="workspace-work-column">
      <div className="workspace-work-tabs" aria-label="Work views">
        {tabs.map((item) => (
          <button
            key={item}
            type="button"
            className={cn("workspace-work-tab", tab === item && "is-active")}
            aria-pressed={tab === item}
            onClick={() => onTab(item)}
          >
            {item}
          </button>
        ))}
      </div>
      <section
        className="workspace-work-panel"
        aria-label={`${label} work panel`}
      >
        <div className="workspace-work-heading">
          <div>
            <span className="workspace-work-eyebrow">{tab}</span>
            <h2>{title}</h2>
            {description && <p>{description}</p>}
          </div>
          {status}
        </div>
        <div className="workspace-work-body">{children}</div>
      </section>
    </div>
  )
}

/** The focused view's compact header: back, identity, title and kind. */
export function FocusHeader({
  title,
  kind,
  avatar,
  onBack,
}: {
  title: string
  kind: string
  avatar?: ReactNode
  onBack: () => void
}) {
  return (
    <header className="workspace-focus-header">
      <button
        className="workspace-icon-button"
        type="button"
        aria-label="Back to workspace"
        onClick={onBack}
      >
        <ArrowLeft size={17} strokeWidth={1.7} />
      </button>
      {avatar}
      <div>
        <h1>{title}</h1>
        <p>{kind}</p>
      </div>
    </header>
  )
}

/** A focused view whose specialist or team is loading, gone or unreadable. */
export function FocusFallback({
  kind,
  onBack,
  loading,
  message,
  onRetry,
}: {
  kind: string
  onBack: () => void
  loading: boolean
  message: string
  onRetry?: () => void
}) {
  return (
    <div className="workspace-focus">
      <FocusHeader title={kind} kind="Agents" onBack={onBack} />
      <main className="workspace-agents-focus-fallback">
        {loading ? (
          <p className="workspace-agents-muted" role="status">
            Loading…
          </p>
        ) : (
          <AgentsNotice
            title={message}
            alert={Boolean(onRetry)}
            action={
              onRetry && (
                <button
                  className="workspace-agents-button"
                  type="button"
                  onClick={onRetry}
                >
                  Try again
                </button>
              )
            }
          />
        )}
      </main>
    </div>
  )
}
