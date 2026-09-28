// A reusable list/add/remove editor for the Cedar policies that narrow who
// may read or search a resource. It knows nothing about organizations,
// corpora, or any other resource shape: the caller identifies the resource
// (`resource`) and supplies the calls to make for it (`client`), so the same
// editor can be mounted for a knowledge base today and a file, teamspace or
// agent later.
import { useId, useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Plus, ShieldMinus } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { ApiError } from "@/lib/api/client"
import { policyErrorMessage } from "@/lib/policies/client"
import type { Policy, PolicyInput, ReadOptions } from "@/lib/policies/client"

/**
 * Identifies what a `PolicyEditor` manages policies for, so its query cache
 * key is unique per resource without this component needing to know
 * anything about that resource's own shape.
 */
export type PolicyResource = {
  /** What kind of thing this is, e.g. "corpus". Keeps one kind's cache
   * entries from colliding with another's. */
  kind: string
  id: string
}

/**
 * The calls a `PolicyEditor` makes. The caller binds these to one resource
 * (an organization and a corpus today) so this component never has to.
 */
export type PolicyEditorClient = {
  list: (options?: ReadOptions) => Promise<Policy[]>
  create: (input: PolicyInput) => Promise<Policy>
  remove: (policyId: string) => Promise<void>
}

const examplePolicyText = `// Hide documents marked confidential from everyone but admins.
forbid (principal, action == Oryxa::Action::"read", resource is Oryxa::Document)
when { resource.metadata has confidential && resource.metadata.confidential == "true" }
unless { context.acts_as_admin };`

const notAllowedMessage = "Only organization admins can manage policies"

/** Long enough that most real policies get a "Show more" toggle, short
 * enough that the list stays scannable. */
const excerptLimit = 160

function isLongPolicyText(text: string): boolean {
  return text.trim().length > excerptLimit
}

function createdLabel(policy: Pick<Policy, "created_by" | "created_at">) {
  const time = Date.parse(policy.created_at)
  const when = Number.isNaN(time)
    ? "at an unknown time"
    : new Date(time).toLocaleString(undefined, {
        dateStyle: "medium",
        timeStyle: "short",
      })
  return `Added by ${policy.created_by} · ${when}`
}

function PolicyRow({
  policy,
  confirming,
  removing,
  removeError,
  onAskRemove,
  onCancelRemove,
  onRemove,
}: {
  policy: Policy
  confirming: boolean
  removing: boolean
  removeError?: string
  onAskRemove: () => void
  onCancelRemove: () => void
  onRemove: () => void
}) {
  const [expanded, setExpanded] = useState(false)
  const long = isLongPolicyText(policy.text)

  return (
    <li className="policy-editor-item">
      <div className="policy-editor-item-main">
        <div className="policy-editor-item-head">
          <strong>{policy.name}</strong>
          {!confirming && (
            <button
              className="policy-editor-remove"
              type="button"
              aria-label={`Remove ${policy.name}`}
              onClick={onAskRemove}
            >
              Remove
            </button>
          )}
        </div>
        <pre className="policy-editor-text" data-expanded={expanded}>
          {policy.text}
        </pre>
        {long && (
          <button
            className="policy-editor-toggle"
            type="button"
            aria-expanded={expanded}
            onClick={() => setExpanded((current) => !current)}
          >
            {expanded ? "Show less" : "Show more"}
          </button>
        )}
        <small className="policy-editor-meta">{createdLabel(policy)}</small>
        {confirming && (
          <div className="policy-editor-confirm">
            <p>
              Remove “{policy.name}”? People it was restricting may be able to
              see more once it’s gone.
            </p>
            {removeError && (
              <p className="policy-editor-error" role="alert">
                {removeError}
              </p>
            )}
            <div className="policy-editor-form-actions">
              <button
                type="button"
                disabled={removing}
                onClick={onCancelRemove}
              >
                Cancel
              </button>
              <button
                className="is-danger"
                type="button"
                disabled={removing}
                onClick={onRemove}
              >
                {removing ? "Removing…" : "Remove"}
              </button>
            </div>
          </div>
        )}
      </div>
    </li>
  )
}

function AddPolicyForm({
  create,
  onAdded,
  onCancel,
}: {
  create: PolicyEditorClient["create"]
  onAdded: () => void
  onCancel: () => void
}) {
  const id = useId()
  const [name, setName] = useState("")
  const [text, setText] = useState(examplePolicyText)
  const [nameError, setNameError] = useState("")
  const [textError, setTextError] = useState("")

  const add = useMutation({
    mutationFn: (input: PolicyInput) => create(input),
    onSuccess: onAdded,
  })

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const trimmedName = name.trim()
    const trimmedText = text.trim()
    const nextNameError = trimmedName ? "" : "Name this policy."
    const nextTextError = trimmedText ? "" : "Write the policy’s Cedar text."
    setNameError(nextNameError)
    setTextError(nextTextError)
    if (nextNameError || nextTextError) return
    add.mutate({ name: trimmedName, text: trimmedText })
  }

  return (
    <form className="policy-editor-form" onSubmit={submit} noValidate>
      <label htmlFor={`${id}-name`}>Name</label>
      <Input
        id={`${id}-name`}
        value={name}
        onChange={(event) => {
          setName(event.target.value)
          setNameError("")
        }}
        aria-invalid={Boolean(nameError)}
        aria-describedby={nameError ? `${id}-name-error` : undefined}
        placeholder="e.g. Hide confidential documents"
        disabled={add.isPending}
      />
      {nameError && (
        <p className="policy-editor-error" id={`${id}-name-error`}>
          {nameError}
        </p>
      )}
      <label htmlFor={`${id}-text`}>Cedar policy</label>
      <Textarea
        className="policy-editor-textarea"
        id={`${id}-text`}
        value={text}
        onChange={(event) => {
          setText(event.target.value)
          setTextError("")
        }}
        aria-invalid={Boolean(textError)}
        aria-describedby={textError ? `${id}-text-error` : undefined}
        rows={6}
        disabled={add.isPending}
      />
      {textError && (
        <p className="policy-editor-error" id={`${id}-text-error`}>
          {textError}
        </p>
      )}
      {add.isError && (
        <p className="policy-editor-error" role="alert">
          {policyErrorMessage(
            add.error,
            "Could not add that policy. Try again."
          )}
        </p>
      )}
      <div className="policy-editor-form-actions">
        <button type="button" disabled={add.isPending} onClick={onCancel}>
          Cancel
        </button>
        <button type="submit" disabled={add.isPending}>
          {add.isPending ? "Adding…" : "Add policy"}
        </button>
      </div>
    </form>
  )
}

export function PolicyEditor({
  resource,
  client,
  canManage,
}: {
  resource: PolicyResource
  client: PolicyEditorClient
  /** Managing policies is always an organization-admin action; the caller
   * decides who that is for the resource being shown. */
  canManage: boolean
}) {
  const queryClient = useQueryClient()
  const queryKey = ["policy-editor", resource.kind, resource.id] as const
  const policiesQuery = useQuery({
    queryKey,
    queryFn: ({ signal }) => client.list({ signal }),
    enabled: canManage,
  })
  const [adding, setAdding] = useState(false)
  const [confirmingId, setConfirmingId] = useState<string | null>(null)

  const remove = useMutation({
    mutationFn: (policyId: string) => client.remove(policyId),
    onSuccess: () => {
      setConfirmingId(null)
      void queryClient.invalidateQueries({ queryKey })
    },
  })

  const intro = (
    <p className="policy-editor-intro">
      Policies can only restrict who sees what here; they never grant access.
    </p>
  )

  // Either the caller already knows this viewer can't manage policies, or it
  // let the request go and the server said so itself (a stale role, most
  // likely). Either way there is nothing else to offer here: a 403 on the
  // list almost certainly means the same for adding or removing one.
  const forbidden =
    policiesQuery.isError &&
    policiesQuery.error instanceof ApiError &&
    policiesQuery.error.status === 403
  if (!canManage || forbidden) {
    return (
      <div className="policy-editor">
        {intro}
        <p className="policy-editor-hint">{notAllowedMessage}</p>
      </div>
    )
  }

  const policies = policiesQuery.data ?? []

  return (
    <div className="policy-editor">
      <div className="policy-editor-bar">
        {intro}
        {!adding && (
          <button
            className="policy-editor-create-button"
            type="button"
            aria-expanded={adding}
            onClick={() => setAdding(true)}
          >
            <Plus size={15} strokeWidth={1.8} />
            Add policy
          </button>
        )}
      </div>

      {adding && (
        <AddPolicyForm
          create={client.create}
          onAdded={() => {
            setAdding(false)
            void queryClient.invalidateQueries({ queryKey })
          }}
          onCancel={() => setAdding(false)}
        />
      )}

      {policiesQuery.isPending ? (
        <p className="policy-editor-loading" role="status">
          Loading policies…
        </p>
      ) : policiesQuery.isError ? (
        <div className="policy-editor-alert" role="alert">
          <span>
            {policyErrorMessage(policiesQuery.error, "Couldn’t load policies.")}
          </span>
          <button type="button" onClick={() => void policiesQuery.refetch()}>
            Try again
          </button>
        </div>
      ) : policies.length ? (
        <ul className="policy-editor-list">
          {policies.map((policy) => (
            <PolicyRow
              key={policy.id}
              policy={policy}
              confirming={confirmingId === policy.id}
              removing={remove.isPending && remove.variables === policy.id}
              removeError={
                remove.isError && remove.variables === policy.id
                  ? policyErrorMessage(
                      remove.error,
                      "Could not remove it. Try again."
                    )
                  : undefined
              }
              onAskRemove={() => {
                remove.reset()
                setConfirmingId(policy.id)
              }}
              onCancelRemove={() => setConfirmingId(null)}
              onRemove={() => remove.mutate(policy.id)}
            />
          ))}
        </ul>
      ) : (
        <div className="policy-editor-empty">
          <ShieldMinus size={20} strokeWidth={1.5} />
          <strong>No policies yet</strong>
          <p>Add one to narrow who can read or search here.</p>
        </div>
      )}
    </div>
  )
}
