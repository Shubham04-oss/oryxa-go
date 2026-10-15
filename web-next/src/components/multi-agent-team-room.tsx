// A multi-agent team's conversation as the focused view's naked
// conversation (DESIGN.md §3). What it shows and sends comes through
// useAgentTeamRoom, the seam its Durable Object will take over.
import { useRef, useState } from "react"
import { AgentComposer } from "./agent-composer"
import { AgentConversation } from "./agent-conversation"
import { AgentTimeline } from "./agent-timeline"
import { agentIdentity, runeCount } from "./agents-format"
import { IdentityAvatar } from "./workspace-avatar"
import {
  AGENT_TEAM_MESSAGE_MAX_RUNES,
  useAgentTeamRoom,
} from "@/hooks/use-agent-team-room"
import type { AgentTeam, AgentTeamMessage } from "@/lib/agents/agent-teams"
import { agentTeamSeats } from "@/lib/agents/policy"
import type { MessageEntry } from "@/lib/threads/timeline"

/**
 * The room's messages as timeline entries, so a team reads as a working
 * session like any agent chat: the member's messages are tasks, and the
 * agents' answers after one are its run, each labelled with who wrote it.
 * A message still being sent is the member's own, so it is a task too.
 */
export function roomEntries(
  messages: readonly AgentTeamMessage[],
  sending?: string | null
): MessageEntry[] {
  const entries: MessageEntry[] = messages.map((message) => ({
    kind: "message",
    key: `room:${message.id}`,
    id: message.id,
    at: message.created_at,
    author: message.kind === "member" ? "member" : message.author_name,
    role: message.kind === "member" ? "member" : "agent",
    text: message.body,
    state: "complete",
  }))
  if (sending)
    entries.push({
      kind: "message",
      key: "room:sending",
      author: "member",
      role: "member",
      text: sending,
      state: "complete",
    })
  return entries
}

const noConfirmations: ReadonlySet<string> = new Set()

export function AgentTeamRoom({
  organizationId,
  team,
  coordinatorPaused = false,
  onComposingChange,
}: {
  organizationId: string
  team: AgentTeam
  /** The member's primary agent is paused, so it cannot coordinate. */
  coordinatorPaused?: boolean
  onComposingChange?: (composing: boolean) => void
}) {
  const room = useAgentTeamRoom(organizationId, team.id)
  const [draft, setDraft] = useState("")
  const [draftError, setDraftError] = useState("")
  const [sent, setSent] = useState(0)
  const composerRef = useRef<HTMLDivElement>(null)
  const seats = agentTeamSeats(team)
  const active = team.status === "active"
  const replying = Boolean(room.sending)

  async function send(message: string) {
    if (runeCount(message) > AGENT_TEAM_MESSAGE_MAX_RUNES) {
      setDraftError("A message holds up to 4,000 characters.")
      return
    }
    setDraft("")
    setDraftError("")
    setSent((count) => count + 1)
    if (!(await room.send(message))) {
      setDraft((current) => (current.trim() ? current : message))
    }
  }

  function mention(handle: string) {
    setDraft((current) =>
      current.trim() ? `${current.trimEnd()} @${handle} ` : `@${handle} `
    )
    composerRef.current?.querySelector("textarea")?.focus()
  }

  const problem = draftError || room.sendError
  const status = !active ? (
    "This team is paused. Resume it to send messages."
  ) : problem ? (
    <span role="alert">{problem}</span>
  ) : replying ? (
    "The team is working; this can take up to a minute and a half. You can send again once it has answered."
  ) : coordinatorPaused ? (
    `${team.coordinator_name} is paused, so only the specialists you @mention answer.`
  ) : (
    `${team.coordinator_name} answers by default; @mention a specialist to ask it directly.`
  )

  return (
    <section
      className="workspace-conversation workspace-agents-room"
      aria-label={`${team.name} conversation`}
    >
      <div className="workspace-conversation-presence">
        <span className="workspace-presence-pill">
          {!active ? "Paused" : replying ? "Replying" : "Ready"}
        </span>
        <div
          className="workspace-participants workspace-agents-seats"
          role="group"
          aria-label="Mention a teammate"
        >
          {seats.map((seat) => {
            const paused =
              !seat.active || (seat.handle === "primary" && coordinatorPaused)
            return (
              <button
                className="workspace-agents-seat"
                type="button"
                key={seat.agent_id}
                aria-label={`Mention ${seat.name}`}
                title={`${seat.name} · @${seat.handle}${paused ? " (paused)" : ""}`}
                disabled={!active || replying || paused}
                onClick={() => mention(seat.handle)}
              >
                <IdentityAvatar
                  identity={agentIdentity({
                    id: seat.agent_id,
                    name: seat.name,
                  })}
                />
              </button>
            )
          })}
        </div>
      </div>

      <AgentConversation
        agentName={team.name}
        state={room.loading ? "loading" : "ready"}
        error={room.readError}
        onRetry={room.readError ? room.reload : undefined}
        follow={sent}
        variant="session"
      >
        {room.messages.length === 0 && !room.sending && !room.readError && (
          <p className="workspace-agent-empty">
            Nothing here yet. Give the team a task: {team.coordinator_name}{" "}
            coordinates, and an @mention reaches a specialist directly.
          </p>
        )}
        <AgentTimeline
          variant="session"
          entries={roomEntries(
            room.messages,
            room.sending && !room.sendingStored ? room.sending : null
          )}
          running={replying}
          actionable={noConfirmations}
          submitted={{}}
          answering
          onAnswer={() => {}}
          organizationId={organizationId}
        />
      </AgentConversation>

      <div className="workspace-agents-room-composer" ref={composerRef}>
        <AgentComposer
          label={`Message ${team.name}`}
          placeholder={active ? "Ask the team…" : "This team is paused"}
          value={draft}
          onChange={(value) => {
            setDraft(value)
            if (draftError) setDraftError("")
            if (room.sendError) room.clearSendError()
          }}
          onSubmit={(message) => void send(message)}
          disabled={!active || replying}
          status={status}
          onFocus={() => onComposingChange?.(true)}
          onBlur={() => onComposingChange?.(false)}
        />
      </div>
    </section>
  )
}
