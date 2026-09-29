// A multi-agent team's conversation as the focused view's naked
// conversation (DESIGN.md §3). What it shows and sends comes through
// useAgentTeamRoom, the seam its Durable Object will take over.
import { useRef, useState } from "react"
import { AgentComposer } from "./agent-composer"
import { AgentFace } from "./agents-parts"
import { AgentConversation } from "./agent-conversation"
import { AgentActivity, AgentMarkdown } from "./agent-timeline"
import { agentIdentity, formatTime, runeCount } from "./agents-format"
import { IdentityAvatar } from "./workspace-avatar"
import {
  AGENT_TEAM_MESSAGE_MAX_RUNES,
  useAgentTeamRoom,
} from "@/hooks/use-agent-team-room"
import type { AgentTeam, AgentTeamMessage } from "@/lib/agents/agent-teams"
import { agentTeamSeats } from "@/lib/agents/policy"

function RoomLine({ message }: { message: AgentTeamMessage }) {
  if (message.kind === "member") {
    return (
      <p className="workspace-agent-message" data-from="member">
        {message.body}
      </p>
    )
  }
  const time = formatTime(message.created_at)
  return (
    <article
      className="workspace-agents-room-line"
      aria-label={`${message.author_name}${time ? `, ${time}` : ""}`}
    >
      <AgentFace
        agent={{
          id: message.author_agent_id || message.author_name,
          name: message.author_name,
        }}
      />
      <div className="workspace-agents-room-body">
        <p className="workspace-agents-room-meta">
          <strong>{message.author_name}</strong>
          {time && <time dateTime={message.created_at}>{time}</time>}
        </p>
        <AgentMarkdown text={message.body} />
      </div>
    </article>
  )
}

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
    "You can send again once the team has answered."
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
      >
        {room.messages.length === 0 && !room.sending && !room.readError && (
          <p className="workspace-agent-empty">
            Nothing here yet. Give the team a task: {team.coordinator_name}{" "}
            coordinates, and an @mention reaches a specialist directly.
          </p>
        )}
        {room.messages.map((message) => (
          <RoomLine message={message} key={message.id} />
        ))}
        {room.sending && !room.sendingStored && (
          <div className="workspace-agents-pending" aria-busy="true">
            <p className="workspace-agent-message" data-from="member">
              {room.sending}
            </p>
            <small>Sending…</small>
          </div>
        )}
        {room.sending && (
          <AgentActivity
            state="weaving"
            label="The team is replying. This can take up to a minute and a half."
          />
        )}
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
