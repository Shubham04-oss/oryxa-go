import { useId } from "react"
import type { FormEvent, KeyboardEvent, ReactNode } from "react"
import TextareaAutosize from "react-textarea-autosize"
import { Send, Square } from "lucide-react"

export function AgentComposer({
  label,
  placeholder,
  value,
  onChange,
  onSubmit,
  disabled = false,
  streaming = false,
  onStop,
  status,
  onFocus,
  onBlur,
}: {
  label: string
  placeholder?: string
  value: string
  onChange: (value: string) => void
  onSubmit?: (message: string) => void
  disabled?: boolean
  streaming?: boolean
  onStop?: () => void
  status?: ReactNode
  onFocus?: () => void
  onBlur?: () => void
}) {
  const statusId = useId()
  const message = value.trim()
  const canSend = !disabled && !streaming && message.length > 0

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
      <div className="workspace-agent-composer-field">
        <TextareaAutosize
          aria-label={label}
          aria-describedby={status ? statusId : undefined}
          placeholder={placeholder}
          value={value}
          disabled={disabled}
          minRows={1}
          maxRows={6}
          onChange={(event) => onChange(event.target.value)}
          onKeyDown={submitOnEnter}
          onFocus={onFocus}
          onBlur={onBlur}
        />
        {streaming ? (
          <button type="button" aria-label="Stop response" onClick={onStop}>
            <Square size={12} strokeWidth={2} fill="currentColor" />
          </button>
        ) : (
          <button type="submit" aria-label="Send message" disabled={!canSend}>
            <Send size={15} strokeWidth={1.8} />
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
