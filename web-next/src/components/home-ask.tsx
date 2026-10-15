// My workspace's large composer (WorkspaceV3): one pill in the middle of the
// page that asks Krude. Sending starts a new chat in the agent panel through
// the shell's chat-choice queue (onNewChat), as the old Home box did. Under
// it, a few suggestions drawn from what the member really has.
import { Button } from "@/components/ui/button"
import { AgentComposer } from "./agent-composer"

export type HomeSuggestion = {
  key: string
  label: string
  run: () => void
}

export function HomeAsk({
  draft,
  onDraftChange,
  enabled,
  onNewChat,
  suggestions,
}: {
  draft: string
  onDraftChange: (draft: string) => void
  enabled: boolean
  onNewChat?: (message?: string) => void
  suggestions: HomeSuggestion[]
}) {
  return (
    <section className="home-ask" aria-label="Your agent">
      <AgentComposer
        label="Start a new conversation"
        placeholder="Ask Krude anything"
        value={draft}
        onChange={onDraftChange}
        sendDisabled={!enabled || !onNewChat}
        status={
          !enabled
            ? "Sign in to start a conversation."
            : !onNewChat
              ? "Conversation shortcuts are unavailable."
              : undefined
        }
        onSubmit={(message) => {
          onNewChat?.(message)
          onDraftChange("")
        }}
      />
      {suggestions.length > 0 && (
        <div className="home-chips" aria-label="Suggestions">
          {suggestions.map((suggestion) => (
            <Button
              key={suggestion.key}
              className="home-chip"
              variant="outline"
              size="sm"
              onClick={suggestion.run}
            >
              {suggestion.label}
            </Button>
          ))}
        </div>
      )}
    </section>
  )
}
