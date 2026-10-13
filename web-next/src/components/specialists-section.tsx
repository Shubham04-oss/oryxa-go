// Agents mode → Specialists: the member's specialists, and the plain form
// that creates or edits one with createAgent / updateAgent.
import { useId, useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Plus } from "lucide-react"
import { AgentsNotice, SpecialistEntry } from "./agents-parts"
import {
  agentsErrorMessage,
  cleanLine,
  plural,
  runeCount,
} from "./agents-format"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import type { AgentsFocusTarget } from "@/hooks/use-agents-mode"
import { ApiError } from "@/lib/api/client"
import { afterCreateAgent, afterUpdateAgent } from "@/lib/agents/invalidation"
import { createAgent, updateAgent } from "@/lib/agents/managed-agents"
import type {
  ManagedAgent,
  UpdateAgentInput,
} from "@/lib/agents/managed-agents"
import { isUnavailableHere, specialistAgents } from "@/lib/agents/policy"
import { agentKeys, agentsQueryOptions } from "@/lib/agents/queries"

// managedagents.validateDefinition
const NAME_MAX = 60
const DESCRIPTION_MAX = 500
const INSTRUCTIONS_MAX = 20_000

type Field = "name" | "description" | "instructions"
type FieldErrors = Partial<Record<Field, string>>

function validate(fields: Record<Field, string>): FieldErrors {
  const errors: FieldErrors = {}
  if (!fields.name) errors.name = "Give it a name."
  else if (runeCount(fields.name) > NAME_MAX)
    errors.name = `Use at most ${NAME_MAX} characters.`
  if (runeCount(fields.description) > DESCRIPTION_MAX)
    errors.description = `Keep it to ${DESCRIPTION_MAX} characters.`
  if (!fields.instructions)
    errors.instructions = "Write the instructions it follows."
  else if (runeCount(fields.instructions) > INSTRUCTIONS_MAX)
    errors.instructions = "Keep the instructions under 20,000 characters."
  return errors
}

/** `“Scout” is taken…`: names are unique across everyone's agents. */
function nameTakenCopy(name: string) {
  return `“${name}” is taken. Agent names are unique across everyone’s agents in this workspace.`
}

/**
 * Creates a specialist, or edits one when `agent` is given. An edit sends
 * only what changed, since the server refuses an empty change.
 */
export function SpecialistForm({
  organizationId,
  agent,
  onDone,
  onCancel,
}: {
  organizationId: string
  agent?: ManagedAgent
  onDone: (agent: ManagedAgent) => void
  onCancel: () => void
}) {
  const queryClient = useQueryClient()
  const id = useId()
  const [name, setName] = useState(agent?.name ?? "")
  const [description, setDescription] = useState(agent?.description ?? "")
  const [instructions, setInstructions] = useState(agent?.instructions ?? "")
  const [errors, setErrors] = useState<FieldErrors>({})
  const [formError, setFormError] = useState("")
  const save = useMutation({
    mutationFn: (input: Record<Field, string>) => {
      if (!agent) return createAgent(organizationId, input)
      const change: UpdateAgentInput = {}
      if (input.name !== agent.name) change.name = input.name
      if (input.description !== agent.description)
        change.description = input.description
      if (input.instructions !== agent.instructions)
        change.instructions = input.instructions
      return Object.keys(change).length
        ? updateAgent(organizationId, agent.id, change)
        : Promise.resolve(agent)
    },
    onSuccess: async (saved) => {
      if (agent) {
        if (saved !== agent)
          await afterUpdateAgent(queryClient, organizationId, saved)
      } else {
        queryClient.setQueryData(
          agentKeys.agent(organizationId, saved.id),
          saved
        )
        await afterCreateAgent(queryClient, organizationId)
      }
      onDone(saved)
    },
    onError: (error, input) => {
      if (error instanceof ApiError && error.code === "name_conflict") {
        setErrors({ name: nameTakenCopy(input.name) })
        return
      }
      setFormError(
        agentsErrorMessage(
          error,
          agent
            ? "Could not save your changes. Try again."
            : "Could not create the specialist. Try again.",
          {
            not_found: "This specialist no longer exists.",
            unavailable_here: "Specialists aren't set up on this server.",
          }
        )
      )
    },
  })

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (save.isPending) return
    const fields = {
      name: cleanLine(name),
      description: description.trim(),
      instructions: instructions.trim(),
    }
    const found = validate(fields)
    setErrors(found)
    setFormError("")
    if (Object.keys(found).length === 0) save.mutate(fields)
  }

  function field(key: Field) {
    return {
      id: `${id}-${key}`,
      "aria-invalid": Boolean(errors[key]) || undefined,
      "aria-describedby": errors[key] ? `${id}-${key}-error` : undefined,
    }
  }

  function fieldError(key: Field) {
    return (
      errors[key] && (
        <p className="workspace-agents-field-error" id={`${id}-${key}-error`}>
          {errors[key]}
        </p>
      )
    )
  }

  return (
    <form
      className="workspace-agents-form"
      onSubmit={submit}
      noValidate
      aria-label={agent ? `Edit ${agent.name}` : "New specialist"}
    >
      {!agent && (
        <div className="workspace-agents-form-intro">
          <h3>New specialist</h3>
          <p>
            An agent for one kind of work. It follows these instructions every
            time it runs.
          </p>
        </div>
      )}
      <label htmlFor={`${id}-name`}>Name</label>
      <Input
        {...field("name")}
        value={name}
        onChange={(event) => {
          setName(event.target.value)
          if (errors.name) setErrors({ ...errors, name: undefined })
        }}
        placeholder="e.g. Pipeline reviewer"
        autoComplete="off"
        autoFocus
      />
      {fieldError("name")}
      <label htmlFor={`${id}-description`}>
        Description <span>optional</span>
      </label>
      <Input
        {...field("description")}
        value={description}
        onChange={(event) => setDescription(event.target.value)}
        placeholder="What it's for, in a line"
        autoComplete="off"
      />
      {fieldError("description")}
      <label htmlFor={`${id}-instructions`}>Instructions</label>
      <Textarea
        {...field("instructions")}
        value={instructions}
        onChange={(event) => setInstructions(event.target.value)}
        placeholder="What it works on, what to check, and what a finished answer contains."
        rows={6}
      />
      {fieldError("instructions")}
      {formError && (
        <p className="workspace-agents-error" role="alert">
          {formError}
        </p>
      )}
      <div className="workspace-agents-actions">
        <button
          className="workspace-agents-button"
          type="button"
          onClick={onCancel}
        >
          Cancel
        </button>
        <button
          className="workspace-agents-button"
          data-variant="primary"
          type="submit"
          disabled={save.isPending}
        >
          {agent
            ? save.isPending
              ? "Saving…"
              : "Save changes"
            : save.isPending
              ? "Creating…"
              : "Create specialist"}
        </button>
      </div>
    </form>
  )
}

export function SpecialistsSection({
  organizationId,
  onOpen,
}: {
  organizationId: string
  onOpen: (target: AgentsFocusTarget) => void
}) {
  const agents = useQuery(agentsQueryOptions(organizationId))
  const [creating, setCreating] = useState(false)
  const specialists = agents.data ? specialistAgents(agents.data) : []
  const unavailable = isUnavailableHere(agents.error)

  return (
    <div className="workspace-agents-page">
      <div className="workspace-agents-toolbar">
        <span className="workspace-agents-count">
          {agents.data && plural(specialists.length, "specialist")}
        </span>
        <button
          className="workspace-agents-button"
          data-variant="primary"
          type="button"
          aria-expanded={creating}
          disabled={unavailable}
          onClick={() => setCreating((open) => !open)}
        >
          <Plus size={16} strokeWidth={1.8} />
          New specialist
        </button>
      </div>

      {creating && (
        <SpecialistForm
          organizationId={organizationId}
          onCancel={() => setCreating(false)}
          onDone={(created) => {
            setCreating(false)
            onOpen({ kind: "specialist", agentId: created.id })
          }}
        />
      )}

      {agents.isPending ? (
        <p className="workspace-agents-muted" role="status">
          Loading your specialists…
        </p>
      ) : agents.isError ? (
        <AgentsNotice
          title={
            unavailable
              ? "Specialists aren't set up on this server."
              : "Could not load your specialists."
          }
          alert={!unavailable}
          action={
            !unavailable && (
              <button
                className="workspace-agents-button"
                type="button"
                onClick={() => void agents.refetch()}
              >
                Try again
              </button>
            )
          }
        />
      ) : specialists.length ? (
        <div className="workspace-entry-grid workspace-agents-grid">
          {specialists.map((agent) => (
            <SpecialistEntry
              agent={agent}
              key={agent.id}
              onOpen={() => onOpen({ kind: "specialist", agentId: agent.id })}
            />
          ))}
        </div>
      ) : (
        !creating && (
          <AgentsNotice title="No specialists yet.">
            A specialist is an agent for one kind of work, with its own
            instructions. Your primary agent and your teams can hand it work.
          </AgentsNotice>
        )
      )}
    </div>
  )
}
