// A conversation's thread (F18): every ask in it, oldest first, with where
// each stands and, once answered, what the answer said, so the exchange
// reads in order. One is open beside it, as its card; choosing another
// opens that one.
import type {
  AgentRequest,
  AgentRequestBox,
  AgentRequestParty,
} from "@/lib/agents/agent-requests"
import { statusLabel } from "./request-card-format"
import type { Reader } from "./request-card-format"
import type { RequestConversation } from "./requests-view-group"
import { rowTime } from "./requests-view-format"
import type { PeopleNames } from "./requests-view-queries"

/**
 * A party as a row names them (H5): "You" for the reader; else their full
 * name, with their email beside it when another member shares their first
 * name.
 */
export function partyName(
  party: AgentRequestParty,
  viewerId: string,
  people: Pick<PeopleNames, "nameOf" | "hintOf">
): { name: string; hint?: string } {
  if (party.user_id === viewerId) return { name: "You" }
  const name =
    people.nameOf(party.user_id) ||
    party.user_display_name?.trim() ||
    party.user_email?.trim() ||
    "Someone"
  const hint = people.hintOf(party.user_id)
  return hint ? { name, hint } : { name }
}

function answerOf(request: AgentRequest): string | undefined {
  if (request.status !== "completed") return undefined
  return request.result?.summary.trim() || undefined
}

export function RequestThread({
  conversation,
  box,
  openId,
  reader,
  people,
  agentName,
  onSelect,
}: {
  conversation: RequestConversation
  box: AgentRequestBox
  /** The ask open as the card. */
  openId: string
  reader: Reader
  people: Pick<PeopleNames, "nameOf" | "hintOf">
  agentName?: string
  onSelect: (requestId: string) => void
}) {
  const count = conversation.requests.length
  if (count < 2) return null
  const latest = conversation.latest
  const party = box === "inbox" ? latest.requester : latest.target
  const { name, hint } = partyName(party, reader.viewerId, people)
  const label = `Conversation with ${name === "You" ? "yourself" : name}`
  return (
    <section className="requests-thread" aria-label={label}>
      <p className="workspace-eyebrow">
        {label}
        {hint && ` (${hint})`} · {count} messages
      </p>
      <ol className="requests-thread-list">
        {[...conversation.requests].reverse().map((request) => {
          const open = request.id === openId
          const answer = answerOf(request)
          return (
            <li key={request.id}>
              <button
                type="button"
                className={
                  open ? "requests-thread-ask is-active" : "requests-thread-ask"
                }
                aria-current={open ? "true" : undefined}
                onClick={() => onSelect(request.id)}
              >
                <span className="requests-thread-what">
                  {request.task.trim() || request.purpose.trim()}
                </span>
                <small>
                  {statusLabel(
                    request,
                    reader,
                    box === "inbox" ? agentName : undefined
                  )}{" "}
                  · {rowTime(request.created_at, reader.now)}
                </small>
                {answer && (
                  <span className="requests-thread-answer">{answer}</span>
                )}
              </button>
            </li>
          )
        })}
      </ol>
    </section>
  )
}
