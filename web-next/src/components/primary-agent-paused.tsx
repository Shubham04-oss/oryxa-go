import { useMutation, useQueryClient } from "@tanstack/react-query"
import { agentsErrorMessage } from "./agents-format"
import { afterUpdateAgent } from "@/lib/agents/invalidation"
import { updateAgent } from "@/lib/agents/managed-agents"

/**
 * A paused primary agent answers no one, in Oryxa or in Slack, until it is
 * back: said once where the member talks to it, with Resume.
 */
export function PrimaryAgentPaused({
  organizationId,
  agent,
}: {
  organizationId: string
  agent: { id: string; name: string }
}) {
  const queryClient = useQueryClient()
  const resume = useMutation({
    mutationFn: () =>
      updateAgent(organizationId, agent.id, { status: "active" }),
    onSuccess: (updated) =>
      afterUpdateAgent(queryClient, organizationId, updated),
  })
  return (
    <div className="workspace-agent-paused">
      <p>
        <strong>{agent.name} is paused.</strong> It isn’t answering until it’s
        back.
      </p>
      <button
        className="workspace-agent-pill"
        data-primary="true"
        type="button"
        disabled={resume.isPending}
        onClick={() => resume.mutate()}
      >
        {resume.isPending ? "Resuming…" : `Resume ${agent.name}`}
      </button>
      {resume.isError && (
        <p className="workspace-agent-error" role="alert">
          {agentsErrorMessage(resume.error, "Could not resume it.")}
        </p>
      )}
    </div>
  )
}
