import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react"
import type { FormEvent, KeyboardEvent, ReactNode } from "react"
import { ArrowUp, Eye, Square } from "lucide-react"
import { Badge } from "@/components/ui/badge"

const maxRows = 6

/**
 * What the page shows, as the agent will be told when a message is sent
 * ("Looking at EU launch"). The agent panel provides it; a composer outside
 * it (a DM, a room) has none and shows nothing.
 */
export const AgentComposerLookingAt = createContext<string | undefined>(
  undefined
)

export function AgentComposer({
  label,
  placeholder,
  value,
  onChange,
  onSubmit,
  disabled = false,
  sendDisabled = false,
  streaming = false,
  onStop,
  status,
  leading,
  context,
  onFocus,
  onBlur,
}: {
  label: string
  placeholder?: string
  value: string
  onChange: (value: string) => void
  onSubmit?: (message: string) => void
  disabled?: boolean
  /** The field stays editable, but what it holds cannot be sent (say why in `status`). */
  sendDisabled?: boolean
  streaming?: boolean
  onStop?: () => void
  status?: ReactNode
  /** A control inside the pill, before the field: a side conversation's "who this goes to". */
  leading?: ReactNode
  /** A quiet line above the field: what an agent's task bar says it sees (`LookingAt`). */
  context?: ReactNode
  onFocus?: () => void
  onBlur?: () => void
}) {
  const statusId = useId()
  const lookingAt = useContext(AgentComposerLookingAt)?.trim()
  const message = value.trim()
  const canSend = !disabled && !sendDisabled && !streaming && message.length > 0
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  // The field is a one-line pill until its text wraps; then it grows up to
  // six lines and softens to a large radius (.workspace-composer
  // [data-multiline] in workspace.css).
  const [multiline, setMultiline] = useState(false)

  // Sized here rather than by react-textarea-autosize, which measures only
  // when the value or the window changes: opened with the panel, it measured
  // while the panel was still widening from its rail, wrapped the
  // placeholder, and kept that tall height once the panel was full width.
  const fit = useCallback(() => {
    const textarea = textareaRef.current
    if (!textarea) return
    const style = getComputedStyle(textarea)
    const lineHeight = Number.parseFloat(style.lineHeight) || 20
    const padding =
      (Number.parseFloat(style.paddingTop) || 0) +
      (Number.parseFloat(style.paddingBottom) || 0)
    const oneLine = lineHeight + padding
    const tallest = lineHeight * maxRows + padding
    textarea.style.height = "auto"
    // An empty field keeps one line whatever its placeholder would wrap to.
    const content = textarea.value ? textarea.scrollHeight : oneLine
    const height = Math.min(Math.max(content, oneLine), tallest)
    textarea.style.height = `${height}px`
    textarea.style.overflowY = content > tallest ? "auto" : "hidden"
    setMultiline(height > oneLine + 1)
  }, [])

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
  }, [fit])

  function submit(event?: FormEvent<HTMLFormElement>) {
    event?.preventDefault()
    if (canSend) onSubmit?.(message)
  }

  function submitOnEnter(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key !== "Enter" || event.shiftKey) return
    // An input method confirms its candidate with Enter. Safari ends the
    // composition before that keydown, which it still reports as 229.
    if (event.nativeEvent.isComposing || event.keyCode === 229) return
    event.preventDefault()
    submit()
  }

  return (
    <form className="workspace-agent-composer" onSubmit={submit}>
      {/* Two "Looking at" sources: the agent panel's place (the context
          provider) wins; a task bar's own reading shows only without it. */}
      {lookingAt ? (
        <Badge
          variant="secondary"
          className="workspace-agent-looking-at"
          title={`Sent with your message: you are looking at ${lookingAt}`}
        >
          <Eye aria-hidden="true" />
          <span>Looking at {lookingAt}</span>
        </Badge>
      ) : (
        context && (
          <div className="workspace-agent-composer-context">{context}</div>
        )
      )}
      <div
        className="workspace-composer workspace-agent-composer-field"
        data-multiline={multiline ? "true" : "false"}
        data-leading={leading ? "true" : undefined}
      >
        {leading}
        <textarea
          ref={textareaRef}
          rows={1}
          aria-label={label}
          aria-describedby={status ? statusId : undefined}
          placeholder={placeholder}
          value={value}
          disabled={disabled}
          onChange={(event) => onChange(event.target.value)}
          onKeyDown={submitOnEnter}
          onFocus={onFocus}
          onBlur={onBlur}
        />
        {streaming ? (
          <button
            className="workspace-composer-send"
            type="button"
            aria-label="Stop response"
            onClick={onStop}
          >
            <Square size={12} strokeWidth={2} fill="currentColor" />
          </button>
        ) : (
          <button
            className="workspace-composer-send"
            type="submit"
            aria-label="Send message"
            disabled={!canSend}
          >
            <ArrowUp size={16} strokeWidth={2} />
          </button>
        )}
      </div>
      {status && (
        <p className="workspace-agent-composer-status" id={statusId}>
          {status}
        </p>
      )}
    </form>
  )
}
