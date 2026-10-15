// The owner answering a request in their own words (DESIGN.md §3, "Requests"):
// a textarea, what it does (their agent never runs, and the asker gets the
// words as written) and, near the limit, how many characters are left. The
// card owns the draft and the call; these are only what it shows:
//
//   - `ReplyOption`, the quieter way in beside the choices a request already
//     has: a "Reply yourself" text button that opens the field, with Send
//     reply and Cancel;
//   - `ReplyFirst`, for a request that needs the owner's own judgment
//     (`answer_by: "owner"`): the field is open from the start, Send reply is
//     the primary action, then "Let my agent answer", then Deny.
import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react"
import type { FormEvent, KeyboardEvent, RefObject } from "react"
import {
  ASKING_AGENT,
  DENY,
  LET_MY_AGENT_ANSWER,
  REPLY_COUNTER_FROM,
  REPLY_YOURSELF,
  SENDING_REPLY,
  SEND_REPLY,
  denyName,
  letMyAgentAnswerName,
  replyLength,
  replyYourselfName,
} from "./request-card-format"
import { REPLY_MAX_CHARACTERS } from "@/lib/agents/agent-requests"
import { Button } from "@/components/ui/button"

/** The most lines the field grows to before it scrolls. */
const MAX_LINES = 8

type FieldProps = {
  /** The asker's first name, as a sentence says it. */
  askerName: string
  value: string
  onChange: (value: string) => void
  /** A send is on its way: what was written stays, and cannot change. */
  busy: boolean
  /** The last send was refused for what was written (empty, too long). */
  invalid: boolean
  /** The card focuses the field: when it opens, and when a send failed. */
  textareaRef: RefObject<HTMLTextAreaElement | null>
  /** How many lines it starts with: one is a pill, which softens as the text wraps. */
  lines?: number
  /** What the empty field says: "Your reply to Raya". */
  placeholder?: string
  /** What it says under the field about what the reply does. */
  hint?: string
  onSend: () => void
}

/**
 * The field. It follows the chat composer: a pill at one line, softening to a
 * large radius once the text wraps (workspace.css `.workspace-composer`),
 * growing up to eight lines and scrolling after. Command or Control with
 * Enter sends, as Enter alone is a new line.
 */
function ReplyField({
  askerName,
  value,
  onChange,
  busy,
  invalid,
  textareaRef,
  lines = 1,
  placeholder = `Your reply to ${askerName}`,
  hint = `${askerName} gets your words as you write them. Your agent doesn't run.`,
  onSend,
}: FieldProps) {
  const hintId = useId()
  const countId = useId()
  const [multiline, setMultiline] = useState(lines > 1)
  const count = replyLength(value)
  const over = count > REPLY_MAX_CHARACTERS
  const counting = count >= REPLY_COUNTER_FROM

  const fit = useCallback(() => {
    const textarea = textareaRef.current
    if (!textarea) return
    const style = getComputedStyle(textarea)
    const lineHeight = Number.parseFloat(style.lineHeight) || 18
    const padding =
      (Number.parseFloat(style.paddingTop) || 0) +
      (Number.parseFloat(style.paddingBottom) || 0)
    const shortest = lineHeight * lines + padding
    const tallest = lineHeight * MAX_LINES + padding
    textarea.style.height = "auto"
    const content = textarea.value ? textarea.scrollHeight : shortest
    const height = Math.min(Math.max(content, shortest), tallest)
    textarea.style.height = `${height}px`
    textarea.style.overflowY = content > tallest ? "auto" : "hidden"
    setMultiline(height > lineHeight + padding + 1)
  }, [textareaRef, lines])

  useLayoutEffect(fit, [fit, value])

  useEffect(() => {
    const textarea = textareaRef.current
    if (!textarea || typeof ResizeObserver === "undefined") return
    let width = textarea.clientWidth
    const observer = new ResizeObserver(() => {
      if (textarea.clientWidth === width) return
      width = textarea.clientWidth
      fit()
    })
    observer.observe(textarea)
    return () => observer.disconnect()
  }, [fit, textareaRef])

  function sendOnModifiedEnter(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key !== "Enter" || !(event.metaKey || event.ctrlKey)) return
    event.preventDefault()
    if (!busy) onSend()
  }

  return (
    <div className="workspace-request-reply-box">
      <div
        className="workspace-request-reply-field"
        data-multiline={multiline ? "true" : "false"}
        data-invalid={invalid || over ? "true" : undefined}
      >
        <textarea
          ref={textareaRef}
          rows={lines}
          aria-label={replyYourselfName(askerName)}
          aria-describedby={counting ? `${hintId} ${countId}` : hintId}
          aria-invalid={invalid || over ? true : undefined}
          placeholder={placeholder}
          value={value}
          readOnly={busy}
          onChange={(event) => onChange(event.target.value)}
          onKeyDown={sendOnModifiedEnter}
        />
      </div>
      <p className="workspace-request-reply-meta">
        <span id={hintId}>{hint}</span>
        {counting && (
          <span
            className="workspace-request-reply-count"
            id={countId}
            data-over={over ? "true" : undefined}
          >
            {count} / {REPLY_MAX_CHARACTERS}
          </span>
        )}
      </p>
    </div>
  )
}

function submitted(send: () => void) {
  return (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    send()
  }
}

/**
 * Reply yourself, the quieter option under a request's own choices (Share
 * answer, Allow for 1 hour, Deny; Share answer, Deny; or a held answer's Send
 * choices). Closed, it is a text button; open, the field takes focus, with
 * Cancel (which drops what was written and gives focus back) and Send reply.
 */
export function ReplyOption({
  askerName,
  value,
  onChange,
  busy,
  sending,
  invalid,
  textareaRef,
  placeholder,
  hint,
  onSend,
  onCancel,
  open: openGiven,
  onOpenChange,
}: Omit<FieldProps, "lines"> & {
  /** It is this reply that is on its way, not another choice. */
  sending: boolean
  onCancel: () => void
  /**
   * Held by the card when its choices grid offers Reply yourself (R): the
   * field shows while `open`, and there is no text button of its own.
   */
  open?: boolean
  onOpenChange?: (open: boolean) => void
}) {
  const [ownOpen, setOwnOpen] = useState(false)
  const controlled = openGiven !== undefined
  const open = controlled ? openGiven : ownOpen
  const setOpen = (next: boolean) => {
    if (controlled) onOpenChange?.(next)
    else setOwnOpen(next)
  }
  const buttonRef = useRef<HTMLButtonElement>(null)
  const giveFocusBack = useRef(false)

  useEffect(() => {
    if (open) textareaRef.current?.focus()
  }, [open, textareaRef])

  useEffect(() => {
    if (open || !giveFocusBack.current) return
    giveFocusBack.current = false
    buttonRef.current?.focus()
  }, [open])

  if (!open && controlled) return null
  if (!open) {
    return (
      <div className="workspace-request-quiet">
        <Button
          variant="link"
          size="sm"
          className="workspace-request-quiet-button"
          ref={buttonRef}
          aria-label={replyYourselfName(askerName)}
          disabled={busy}
          onClick={() => setOpen(true)}
        >
          {REPLY_YOURSELF}
        </Button>
      </div>
    )
  }

  return (
    <form className="workspace-request-reply" onSubmit={submitted(onSend)}>
      <ReplyField
        askerName={askerName}
        value={value}
        onChange={onChange}
        busy={busy}
        invalid={invalid}
        textareaRef={textareaRef}
        placeholder={placeholder}
        hint={hint}
        onSend={onSend}
      />
      <div className="workspace-request-actions">
        <Button
          variant="outline"
          disabled={busy}
          onClick={() => {
            giveFocusBack.current = !controlled
            onCancel()
            setOpen(false)
          }}
        >
          Cancel
        </Button>
        <Button type="submit" data-primary="true" disabled={busy}>
          {sending ? SENDING_REPLY : SEND_REPLY}
        </Button>
      </div>
    </form>
  )
}

/** Which of the three choices is on its way. */
export type FirstChoice = "reply" | "agent" | "deny"

/**
 * A request that needs the owner's own judgment: the field is open (and takes
 * no focus by itself, for a desk may hold several), Send reply is the primary
 * action, "Let my agent answer" comes second and Deny last. `agentDetail`
 * says what letting their agent answer lets it do, where that needs saying.
 */
export function ReplyFirst({
  askerName,
  value,
  onChange,
  busy,
  invalid,
  textareaRef,
  onSend,
  pressed,
  agentDetail,
  onAgent,
  onDeny,
}: Omit<FieldProps, "lines"> & {
  pressed: FirstChoice | undefined
  agentDetail?: string
  onAgent: () => void
  onDeny: () => void
}) {
  const detailId = useId()
  return (
    <form
      className="workspace-request-reply"
      data-first="true"
      onSubmit={submitted(onSend)}
    >
      <ReplyField
        askerName={askerName}
        value={value}
        onChange={onChange}
        busy={busy}
        invalid={invalid}
        textareaRef={textareaRef}
        lines={2}
        placeholder={`Reply yourself to ${askerName}`}
        onSend={onSend}
      />
      <div
        className="workspace-request-actions"
        data-align="start"
        data-first="true"
      >
        <Button type="submit" data-primary="true" disabled={busy}>
          {pressed === "reply" ? SENDING_REPLY : SEND_REPLY}
        </Button>
        <Button
          variant="outline"
          aria-label={letMyAgentAnswerName(askerName)}
          aria-describedby={agentDetail ? detailId : undefined}
          disabled={busy}
          onClick={onAgent}
        >
          {pressed === "agent" ? ASKING_AGENT : LET_MY_AGENT_ANSWER}
        </Button>
        <Button
          variant="destructive"
          className="workspace-request-deny-end"
          aria-label={denyName(askerName)}
          disabled={busy}
          onClick={onDeny}
        >
          {pressed === "deny" ? "Denying…" : DENY}
        </Button>
      </div>
      {agentDetail && (
        <p className="workspace-request-reply-detail" id={detailId}>
          {agentDetail}
        </p>
      )}
    </form>
  )
}
