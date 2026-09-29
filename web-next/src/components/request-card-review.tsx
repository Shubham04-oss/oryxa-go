// An answer the answer guard holds for its owner (DESIGN.md §3, "Side
// conversations": the same choices, here on a request's own card): the answer
// as Send shares it, a preview of what Send masked shares, and Don't send,
// Send and Send masked (the primary). One the guard could not check has no
// masked version, so neither the preview nor Send masked. The asker never
// sees any of it, only that it waits.
import { useState } from "react"
import { AgentMarkdown } from "./agent-timeline"
import { capitalized } from "./side-conversation-format"
import { resultText } from "@/lib/side/asking"
import type {
  AgentRequest,
  AgentRequestReviewAction,
} from "@/lib/agents/agent-requests"

/** From how many characters an answer folds away behind "Show more". */
const LONG_ANSWER = 280

function isLong(text: string): boolean {
  return [...text].length > LONG_ANSWER || text.split("\n").length > 6
}

export function HeldAnswerReview({
  request,
  agentName,
  askerName,
  busy,
  pressed,
  onReview,
  onReload,
}: {
  request: AgentRequest
  /** The owner's agent, as its owner calls it: "Krude", or "your agent". */
  agentName: string
  /** The asker's first name. */
  askerName: string
  /** A review is on its way. */
  busy: boolean
  /** The choice on its way, if any. */
  pressed: AgentRequestReviewAction | undefined
  onReview: (action: AgentRequestReviewAction) => void
  /** Reads the request again: its held answer did not come with it. */
  onReload: () => void
}) {
  const [expanded, setExpanded] = useState(false)
  const held = request.held
  const stored = held ? resultText({ result: held.result }) : undefined
  const masked = held?.masked_result
    ? resultText({ result: held.masked_result })
    : undefined
  const failed = held?.result.status === "failed"
  const folded = stored !== undefined && isLong(stored) && !expanded
  const to = `${agentName}'s answer to ${askerName}`

  return (
    <div className="workspace-request-held">
      {stored !== undefined ? (
        <>
          {failed && (
            <p className="workspace-request-held-label">
              {capitalized(agentName)} couldn't finish the request, and says:
            </p>
          )}
          <div
            className="workspace-request-answer"
            data-folded={folded ? "true" : undefined}
          >
            <AgentMarkdown text={stored} />
          </div>
          {masked !== undefined && (
            <>
              <p className="workspace-request-held-label">
                Send masked shares:
              </p>
              <div
                className="workspace-request-answer"
                data-masked="true"
                data-folded={folded ? "true" : undefined}
              >
                <AgentMarkdown text={masked} />
              </div>
            </>
          )}
          {isLong(stored) && (
            <button
              className="workspace-agent-text-button"
              type="button"
              aria-expanded={expanded}
              onClick={() => setExpanded((open) => !open)}
            >
              {expanded ? "Show less" : "Show more"}
            </button>
          )}
        </>
      ) : (
        <p className="workspace-agent-note" role="status">
          The held answer didn't come with this request.{" "}
          <button
            className="workspace-agent-text-button"
            type="button"
            onClick={onReload}
          >
            Load it again
          </button>
        </p>
      )}
      <div className="workspace-request-actions">
        <button
          className="workspace-agent-pill"
          type="button"
          aria-label={`Don't send ${to}`}
          disabled={busy}
          onClick={() => onReview("withhold")}
        >
          {busy && pressed === "withhold" ? "Withholding…" : "Don't send"}
        </button>
        {stored !== undefined && (
          <>
            <button
              className="workspace-agent-pill"
              type="button"
              aria-label={`Send ${to}`}
              disabled={busy}
              onClick={() => onReview("send")}
            >
              {busy && pressed === "send" ? "Sending…" : "Send"}
            </button>
            {masked !== undefined && (
              <button
                className="workspace-agent-pill"
                data-primary="true"
                type="button"
                aria-label={`Send ${to}, masked`}
                disabled={busy}
                onClick={() => onReview("send_masked")}
              >
                {busy && pressed === "send_masked" ? "Sending…" : "Send masked"}
              </button>
            )}
          </>
        )}
      </div>
    </div>
  )
}
