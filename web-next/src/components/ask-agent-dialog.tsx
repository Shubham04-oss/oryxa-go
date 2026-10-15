// "Ask <agent>" (People and Messages, DESIGN.md §3): a question to a
// teammate's agent, filed as an agent request in the member's own session
// (createAgentRequest). It names no teamspace, so the agent's owner always
// approves it first, and the request is a line in the DM between the two of
// them. The answer is never shown here: it lands in Requests → Sent, which
// "Open in Requests" opens at the request.
import { useId, useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { LoaderCircle } from "lucide-react"
import type { DmPerson, DmPersonAgent } from "./direct-message-people"
import { firstName } from "./direct-message-format"
import { agentIdentity } from "./teamspace-format"
import { IdentityAvatar } from "./workspace-avatar"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogTitle,
} from "@/components/ui/dialog"
import { Textarea } from "@/components/ui/textarea"
import {
  AGENT_REQUEST_TASK_MAX,
  createAgentRequest,
} from "@/lib/agents/agent-requests"
import { agentKeys } from "@/lib/agents/queries"
import { ApiError } from "@/lib/api/client"
import { useOpenRequest } from "@/lib/dms/request-navigation"

/** Whose agent a member is asking. */
export type AskAgentTarget = { person: DmPerson; agent: DmPersonAgent }

/** Why a request wasn't filed, in a line a member can act on. */
function refusal(error: unknown, short: string): string {
  if (error instanceof ApiError) {
    if (error.status === 403 || error.status === 404) {
      return `You can't ask ${short}'s agent from here.`
    }
    if (error.status === 400) return "Oryxa couldn't take that question."
  }
  return "Couldn't send your question. Try again."
}

export function AskAgentDialog({
  organizationId,
  target,
  onClose,
}: {
  organizationId: string
  target: AskAgentTarget
  onClose: () => void
}) {
  const { person, agent } = target
  const short = firstName(person.name)
  const [task, setTask] = useState("")
  const fieldId = useId()
  const queryClient = useQueryClient()
  const openRequest = useOpenRequest()
  const ask = useMutation({
    mutationFn: (question: string) =>
      createAgentRequest(organizationId, {
        target_agent_id: agent.id,
        purpose: `A teammate asked ${agent.name} directly in Oryxa.`,
        task: question,
      }),
    onSuccess: () =>
      queryClient.invalidateQueries({
        queryKey: agentKeys.requests(organizationId),
      }),
  })
  const question = task.trim()
  const tooLong = question.length > AGENT_REQUEST_TASK_MAX
  const sent = ask.data

  function submit(event: Pick<FormEvent, "preventDefault">) {
    event.preventDefault()
    if (!question || tooLong || ask.isPending) return
    ask.mutate(question)
  }

  return (
    <Dialog
      open
      disablePointerDismissal={ask.isPending}
      onOpenChange={(open) => {
        if (!open && !ask.isPending) onClose()
      }}
    >
      <DialogContent
        className="ask-agent-dialog"
        showCloseButton={!ask.isPending}
      >
        <div className="ask-agent-head">
          <IdentityAvatar identity={agentIdentity(agent.id, agent.name)} />
          <div>
            <DialogTitle>Ask {agent.name}</DialogTitle>
            <DialogDescription>
              {sent
                ? `Sent. ${short} approves it before ${agent.name} answers, and the answer arrives in Requests.`
                : `${agent.name} is ${short}'s agent. ${short} approves your question before ${agent.name} answers.`}
            </DialogDescription>
          </div>
        </div>
        {sent ? (
          <DialogFooter className="ask-agent-actions">
            {openRequest && (
              <Button
                variant="outline"
                onClick={() => {
                  onClose()
                  openRequest(sent.id)
                }}
              >
                Open in Requests
              </Button>
            )}
            <Button onClick={onClose}>Done</Button>
          </DialogFooter>
        ) : (
          <form className="ask-agent-form" onSubmit={submit} noValidate>
            <label htmlFor={fieldId}>Your question</label>
            <Textarea
              id={fieldId}
              autoFocus
              rows={4}
              value={task}
              placeholder={`What should ${agent.name} look into?`}
              aria-invalid={tooLong || undefined}
              disabled={ask.isPending}
              onChange={(event) => setTask(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
                  submit(event)
                }
              }}
            />
            {tooLong && (
              <p className="ask-agent-error" role="alert">
                A question holds at most{" "}
                {AGENT_REQUEST_TASK_MAX.toLocaleString("en")} characters.
              </p>
            )}
            {ask.isError && (
              <p className="ask-agent-error" role="alert">
                {refusal(ask.error, short)}
              </p>
            )}
            <DialogFooter className="ask-agent-actions">
              <Button
                type="button"
                variant="outline"
                disabled={ask.isPending}
                onClick={onClose}
              >
                Cancel
              </Button>
              <Button
                type="submit"
                disabled={!question || tooLong || ask.isPending}
              >
                {ask.isPending && (
                  <LoaderCircle className="animate-spin" aria-hidden="true" />
                )}
                Send to {agent.name}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
