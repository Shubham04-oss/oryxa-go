import { useId, useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ArrowUpRight,
  Check,
  CircleAlert,
  Clock3,
  KeyRound,
  Plus,
  RotateCw,
} from "lucide-react"
import {
  appsErrorMessage,
  appsQueryKey,
  canReplaceCredential,
  catalogTriggerTypesQueryOptions,
  connectionActionsQueryOptions,
  connectionTargetsQueryOptions,
  connectionsQueryOptions,
  createTrigger,
  isOryxaConnection,
  removeTrigger,
  replaceCredentials,
  signinDescription,
  signinManifestQueryOptions,
  triggersQueryOptions,
} from "@/lib/apps/client"
import type {
  Connection,
  ConnectionAction,
  Toolkit,
  TriggerType,
} from "@/lib/apps/client"
import { ApiError } from "@/lib/api/client"
import {
  defaultSourceIntervalSeconds,
  effectiveKinds,
  kindsQueryOptions,
  knowledgeQueryKey,
  providersQueryOptions,
  registerSource,
  removeSource,
  setSourceKinds,
  sourceReasonText,
  sourceRunsQueryKey,
  sourceRunsQueryOptions,
  sourcesQueryOptions,
  syncSource,
} from "@/lib/knowledge/client"
import type { Source } from "@/lib/knowledge/client"
import {
  buildTriggerConfig,
  intervalChoices,
  isSourceProvider,
  prettyTime,
  sentence,
  sourceFields,
  sourceName,
  sourceTarget,
  sourceTargetProblem,
  triggerCapability,
} from "./workspace-tool-helpers"
import type { SourceProvider, TriggerField } from "./workspace-tool-helpers"
import { CredentialDialog } from "./workspace-tool-credential-dialog"

type Pane = "events" | "indexing" | "runs"

const previewEvents: Array<
  Pick<TriggerType, "description" | "name" | "trigger">
> = [
  {
    trigger: "EXAMPLE_NEW_ITEM",
    name: "New item",
    description: "Wake your agents when something new arrives.",
  },
  {
    trigger: "EXAMPLE_ITEM_UPDATED",
    name: "Item updated",
    description: "Keep work current when an item changes.",
  },
]

/** Go writes advice in lower case, as a clause; a row shows it as a sentence. */
function advice(text: string): string {
  const trimmed = text.trim()
  return sentence(trimmed.charAt(0).toUpperCase() + trimmed.slice(1))
}

function intervalLabel(seconds: number): string {
  return (
    intervalChoices.find((choice) => choice.value === seconds)?.label ||
    `Every ${seconds}s`
  )
}

/** Everything cached about the sources of one organization, for invalidation. */
function sourcesKey(organizationId: string) {
  return [...knowledgeQueryKey(organizationId), "sources"] as const
}

/**
 * What an active connection does for the member's agents: the events it
 * wakes them for, what it indexes, and how those runs went. Only an active
 * connection has these (apps and knowledge answer 409 otherwise), so the page
 * mounts this for nothing else.
 *
 * A connection made through Oryxa's own sign-in shows how it signs in
 * instead, and replaces its key. Its app's events, indexing and tools arrive
 * with that app's integration; until then apps refuses them for it
 * (`not_connectable`, "not available yet"), so none is offered.
 */
export function ConnectionWorkspace(props: {
  connection: Connection
  toolkit: Toolkit
  organizationId: string
  preview: boolean
}) {
  if (!props.preview && isOryxaConnection(props.connection)) {
    return (
      <OryxaSignin
        connection={props.connection}
        toolkit={props.toolkit}
        organizationId={props.organizationId}
      />
    )
  }
  if (!props.preview && props.connection.broker === "pipedream") {
    return <PipedreamWorkspace {...props} />
  }
  return <ComposioWorkspace {...props} />
}

/**
 * How an Oryxa connection signs in, and "Replace key" for one that holds a
 * key, a password or client credentials. An OAuth connection has nothing to
 * replace: it is connected again instead.
 */
function OryxaSignin({
  connection,
  toolkit,
  organizationId,
}: {
  connection: Connection
  toolkit: Toolkit
  organizationId: string
}) {
  const queryClient = useQueryClient()
  const [replacing, setReplacing] = useState(false)
  const [replaced, setReplaced] = useState(false)
  const kind = connection.credential_kind ?? ""
  const manifest = useQuery({
    ...signinManifestQueryOptions(connection.toolkit),
    enabled: replacing,
  })
  const methods = (manifest.data?.methods ?? []).filter(
    (method) => method.kind === kind
  )
  const loadError = manifest.isError
    ? appsErrorMessage(
        manifest.error,
        `${toolkit.name}'s sign-in could not be loaded.`
      )
    : manifest.isSuccess && !methods.length
      ? `${toolkit.name} no longer signs in this way. Disconnect it and connect it again.`
      : undefined

  // The values go from the dialog straight into the request, never into a
  // mutation's variables, which TanStack Query keeps after it settles.
  async function replace(_kind: string, values: Record<string, string>) {
    const updated = await replaceCredentials(organizationId, connection.id, {
      fields: values,
      kind,
    })
    queryClient.setQueryData<Connection[]>(
      connectionsQueryOptions(organizationId).queryKey,
      (current) =>
        current?.map((item) =>
          item.id === updated.id ? { ...item, ...updated } : item
        )
    )
    setReplacing(false)
    setReplaced(true)
    void queryClient.invalidateQueries({
      queryKey: [...appsQueryKey(organizationId), "connections"],
    })
  }

  return (
    <div className="workspace-tool-workspace">
      <div className="workspace-tool-pane">
        <div className="workspace-tool-pane-head">
          <div>
            <h4>Sign-in</h4>
            <p>{signinDescription(connection, toolkit.name)}</p>
          </div>
          {canReplaceCredential(connection) && (
            <button
              type="button"
              className="workspace-tool-action"
              onClick={() => {
                setReplaced(false)
                setReplacing(true)
              }}
            >
              <KeyRound size={14} /> Replace key
            </button>
          )}
        </div>
        {replaced && (
          <p className="workspace-tool-muted" role="status">
            Key replaced. {toolkit.name} accepted the new one.
          </p>
        )}
        <p className="workspace-tool-muted">
          Events, indexing and runs aren't available yet for apps connected
          through Oryxa's own sign-in.
        </p>
      </div>
      {replacing && (
        <CredentialDialog
          mode="replace"
          appName={toolkit.name}
          methods={methods}
          loading={manifest.isPending}
          loadError={loadError}
          onRetry={() => void manifest.refetch()}
          onSubmit={replace}
          onClose={() => setReplacing(false)}
        />
      )}
    </div>
  )
}

/**
 * A Pipedream connection: the events it wakes agents for, and the tool runs
 * made through it. Indexing doesn't go through Pipedream (the owner's
 * decision), so it has no indexing or sync runs.
 */
function PipedreamWorkspace({
  connection,
  toolkit,
  organizationId,
}: {
  connection: Connection
  toolkit: Toolkit
  organizationId: string
}) {
  const [pane, setPane] = useState<"events" | "runs">("events")
  const tabs: Array<{ key: "events" | "runs"; label: string }> = [
    { key: "events", label: "Events" },
    { key: "runs", label: "Runs" },
  ]
  return (
    <div className="workspace-tool-workspace">
      <div
        className="workspace-tool-workspace-tabs"
        role="tablist"
        aria-label={`${toolkit.name} tools`}
      >
        {tabs.map((tab) => (
          <button
            type="button"
            role="tab"
            aria-selected={pane === tab.key}
            className={pane === tab.key ? "is-active" : ""}
            key={tab.key}
            onClick={() => setPane(tab.key)}
          >
            {tab.label}
          </button>
        ))}
      </div>
      <p className="workspace-tool-preview-note">
        Connected through Pipedream. Indexing for this app isn't available yet.
      </p>
      {pane === "events" && (
        <EventsPane
          connection={connection}
          toolkit={toolkit}
          organizationId={organizationId}
          preview={false}
        />
      )}
      {pane === "runs" && (
        <ToolRunsPane
          connection={connection}
          toolkit={toolkit}
          organizationId={organizationId}
        />
      )}
    </div>
  )
}

/** A tool's id as a member reads it: "slack-send-message" → "Send message". */
export function toolLabel(tool: string, toolkit: string): string {
  const prefix = new RegExp(
    `^${toolkit.replace(/[^a-z0-9]/gi, "[-_]?")}[-_]`,
    "i"
  )
  const words = tool
    .replace(prefix, "")
    .replace(/[-_]+/g, " ")
    .trim()
    .toLowerCase()
  return words ? words.charAt(0).toUpperCase() + words.slice(1) : tool
}

function whoRan(action: ConnectionAction): string {
  if (!action.agent) return "You"
  return action.agent_role === "primary" ? "Your agent" : "A specialist"
}

function runTime(action: ConnectionAction): string | undefined {
  if (!action.finished_at) return undefined
  const ms = Date.parse(action.finished_at) - Date.parse(action.created_at)
  if (!Number.isFinite(ms) || ms < 0) return undefined
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`
}

/**
 * The tool runs made through a connection, newest first: who ran which tool,
 * whether it only read or changed something, and how it ended. What the app
 * answered is never shown here.
 */
function ToolRunsPane({
  connection,
  toolkit,
  organizationId,
}: {
  connection: Connection
  toolkit: Toolkit
  organizationId: string
}) {
  const runs = useQuery(
    connectionActionsQueryOptions(organizationId, connection.id)
  )
  const missing = runs.error instanceof ApiError && runs.error.status === 404
  return (
    <div className="workspace-tool-pane" role="tabpanel">
      <div className="workspace-tool-pane-head">
        <div>
          <h4>Recent tool runs</h4>
          <p>What your agents and you did through {toolkit.name}.</p>
        </div>
      </div>
      {runs.isPending ? (
        <p className="workspace-tool-muted">Loading runs…</p>
      ) : missing ? (
        <p className="workspace-tool-muted">
          Run history for this app isn't available yet.
        </p>
      ) : runs.isError ? (
        <p className="workspace-tool-inline-error" role="alert">
          {appsErrorMessage(runs.error, "Runs could not be loaded.")}{" "}
          <button
            type="button"
            className="workspace-tool-text-button"
            onClick={() => void runs.refetch()}
          >
            Retry
          </button>
        </p>
      ) : runs.data.length === 0 ? (
        <p className="workspace-tool-muted">
          No runs yet. They show up here when an agent uses {toolkit.name}.
        </p>
      ) : (
        <ul className="workspace-tool-runs" aria-label="Tool runs">
          {runs.data.map((action) => (
            <li className="workspace-tool-row" key={action.id}>
              <span
                className={`workspace-tool-dot ${action.status === "succeeded" ? "is-live" : action.status === "failed" ? "is-error" : ""}`}
              />
              <div className="workspace-tool-row-copy">
                <strong>{toolLabel(action.tool, connection.toolkit)}</strong>
                <small>
                  {whoRan(action)} ·{" "}
                  {action.read_only ? "Read only" : "Changed something"} ·{" "}
                  {prettyTime(action.created_at)}
                  {runTime(action) ? ` · ${runTime(action)}` : ""}
                </small>
                {action.status === "failed" && action.error && (
                  <small className="workspace-tool-run-error">
                    {sentence(action.error)}
                  </small>
                )}
              </div>
              <span className="sr-only">
                {action.status === "succeeded"
                  ? "Succeeded"
                  : action.status === "failed"
                    ? "Failed"
                    : "Running"}
              </span>
              {action.status === "running" ? (
                <Clock3 size={16} aria-hidden="true" />
              ) : action.status === "succeeded" ? (
                <Check size={16} aria-hidden="true" />
              ) : (
                <CircleAlert size={16} aria-hidden="true" />
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function ComposioWorkspace({
  connection,
  toolkit,
  organizationId,
  preview,
}: {
  connection: Connection
  toolkit: Toolkit
  organizationId: string
  preview: boolean
}) {
  const [pane, setPane] = useState<Pane>("events")
  const [runsSourceId, setRunsSourceId] = useState<string | null>(null)

  const tabs: Array<{ key: Pane; label: string }> = [
    { key: "events", label: "Events" },
    { key: "indexing", label: "Indexing" },
    { key: "runs", label: "Runs" },
  ]

  return (
    <div className="workspace-tool-workspace">
      <div
        className="workspace-tool-workspace-tabs"
        role="tablist"
        aria-label={`${toolkit.name} tools`}
      >
        {tabs.map((tab) => (
          <button
            type="button"
            role="tab"
            aria-selected={pane === tab.key}
            className={pane === tab.key ? "is-active" : ""}
            key={tab.key}
            onClick={() => setPane(tab.key)}
          >
            {tab.label}
          </button>
        ))}
      </div>
      {preview && (
        <p className="workspace-tool-preview-note">
          Example only — connect the Apps service to manage real subscriptions
          and runs.
        </p>
      )}
      {pane === "events" && (
        <EventsPane
          connection={connection}
          toolkit={toolkit}
          organizationId={organizationId}
          preview={preview}
        />
      )}
      {pane === "indexing" && (
        <IndexingPane
          connection={connection}
          organizationId={organizationId}
          preview={preview}
          toolkit={toolkit}
          onRuns={(sourceId) => {
            setRunsSourceId(sourceId)
            setPane("runs")
          }}
        />
      )}
      {pane === "runs" && (
        <RunsPane
          connection={connection}
          organizationId={organizationId}
          preview={preview}
          initialSourceId={runsSourceId}
        />
      )}
    </div>
  )
}

function EventsPane({
  connection,
  toolkit,
  organizationId,
  preview,
}: {
  connection: Connection
  toolkit: Toolkit
  organizationId: string
  preview: boolean
}) {
  const queryClient = useQueryClient()
  const types = useQuery({
    ...catalogTriggerTypesQueryOptions(toolkit.toolkit),
    enabled: !preview,
  })
  const triggers = useQuery({
    ...triggersQueryOptions(organizationId, connection.id),
    enabled: !preview,
  })
  const [adding, setAdding] = useState(false)
  const [query, setQuery] = useState("")
  const [chosen, setChosen] = useState<TriggerType | null>(null)
  const [values, setValues] = useState<
    Record<string, boolean | number | string>
  >({})
  const [formProblem, setFormProblem] = useState("")
  const [stopId, setStopId] = useState<string | null>(null)
  const refresh = () =>
    queryClient.invalidateQueries({
      queryKey: triggersQueryOptions(organizationId, connection.id).queryKey,
    })
  const add = useMutation({
    mutationFn: (input: { trigger: string; config: Record<string, unknown> }) =>
      createTrigger(organizationId, {
        connectionId: connection.id,
        trigger: input.trigger,
        config: input.config,
      }),
    onSuccess: () => {
      setChosen(null)
      setValues({})
      setAdding(false)
      return refresh()
    },
  })
  const stop = useMutation({
    mutationFn: (triggerId: string) => removeTrigger(organizationId, triggerId),
    onSuccess: () => {
      setStopId(null)
      return refresh()
    },
  })

  const loading = !preview && (types.isPending || triggers.isPending)
  const typeList = types.data ?? []
  const watched = (triggers.data ?? []).filter(
    (item) => item.status !== "removed"
  )
  const typeBySlug = new Map(typeList.map((type) => [type.trigger, type]))
  const subscribed = new Set(watched.map((item) => item.trigger))
  const available = typeList.filter(
    (type) =>
      !subscribed.has(type.trigger) &&
      `${type.name} ${type.description}`
        .toLowerCase()
        .includes(query.toLowerCase())
  )
  const capability = chosen ? triggerCapability(chosen.config_schema) : null
  const loadProblem = triggers.isError
    ? appsErrorMessage(triggers.error, "Watched events could not be loaded.")
    : types.isError
      ? appsErrorMessage(
          types.error,
          `${toolkit.name}'s events could not be loaded.`
        )
      : ""

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (
      !chosen ||
      preview ||
      add.isPending ||
      capability?.kind === "unsupported"
    )
      return
    const outcome =
      capability?.kind === "form"
        ? buildTriggerConfig(capability.fields, values)
        : { config: {} }
    if (outcome.problem) {
      setFormProblem(outcome.problem)
      return
    }
    setFormProblem("")
    add.mutate({ trigger: chosen.trigger, config: outcome.config })
  }

  return (
    <div className="workspace-tool-pane" role="tabpanel">
      <div className="workspace-tool-pane-head">
        <div>
          <h4>Events your agents watch</h4>
          <p>
            Incoming app activity can wake your agents and refresh indexed
            knowledge.
          </p>
        </div>
        <button
          type="button"
          className="workspace-tool-action"
          disabled={preview || loading || types.isError}
          onClick={() => setAdding((value) => !value)}
        >
          <Plus size={14} /> Add event
        </button>
      </div>
      {loading && <p className="workspace-tool-muted">Loading events…</p>}
      {loadProblem && (
        <p className="workspace-tool-inline-error" role="alert">
          {loadProblem}{" "}
          <button
            type="button"
            className="workspace-tool-text-button"
            onClick={() => {
              if (triggers.isError) void triggers.refetch()
              if (types.isError) void types.refetch()
            }}
          >
            Retry
          </button>
        </p>
      )}
      {stop.isError && (
        <p className="workspace-tool-inline-error" role="alert">
          {appsErrorMessage(stop.error, "Could not stop watching this event.")}
        </p>
      )}
      {!preview && triggers.isSuccess && watched.length === 0 && (
        <p className="workspace-tool-muted">
          No events watched yet. Choose an event below to start.
        </p>
      )}
      {watched.map((trigger) => {
        const type = typeBySlug.get(trigger.trigger)
        return (
          <div className="workspace-tool-row" key={trigger.id}>
            <span
              className={`workspace-tool-dot ${trigger.status === "active" ? "is-live" : ""}`}
            />
            <div className="workspace-tool-row-copy">
              <strong>{type?.name || trigger.trigger}</strong>
              <small>
                {trigger.status === "active"
                  ? `${trigger.delivery_count} deliveries · Last ${prettyTime(trigger.last_delivery_at)}`
                  : trigger.status_reason
                    ? advice(trigger.status_reason)
                    : "Disabled"}
              </small>
              {trigger.setup && <small>{advice(trigger.setup)}</small>}
            </div>
            {stopId === trigger.id ? (
              <span className="workspace-tool-confirm">
                <button type="button" onClick={() => setStopId(null)}>
                  Cancel
                </button>
                <button
                  type="button"
                  disabled={stop.isPending}
                  onClick={() => stop.mutate(trigger.id)}
                >
                  {stop.isPending ? "Stopping…" : "Confirm stop"}
                </button>
              </span>
            ) : (
              <button
                type="button"
                className="workspace-tool-text-button"
                onClick={() => {
                  stop.reset()
                  setStopId(trigger.id)
                }}
              >
                Stop
              </button>
            )}
          </div>
        )
      })}
      {preview &&
        previewEvents.map((type) => (
          <div className="workspace-tool-row" key={type.trigger}>
            <span className="workspace-tool-dot" />
            <div className="workspace-tool-row-copy">
              <strong>{type.name}</strong>
              <small>{type.description}</small>
            </div>
            <span className="workspace-tool-example">Example</span>
          </div>
        ))}
      {adding && !preview && (
        <div className="workspace-tool-form-panel">
          <div className="workspace-tool-pane-head">
            <div>
              <h5>Choose an event</h5>
              <p>Only events available to this connected app appear here.</p>
            </div>
            <button
              type="button"
              className="workspace-tool-text-button"
              onClick={() => {
                setAdding(false)
                setChosen(null)
                add.reset()
              }}
            >
              Cancel
            </button>
          </div>
          {!chosen ? (
            <>
              {typeList.length > 5 && (
                <input
                  className="workspace-tool-input"
                  aria-label="Search events"
                  placeholder="Search events"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
              )}
              {available.length === 0 && (
                <p className="workspace-tool-muted">
                  No more events available.
                </p>
              )}
              <div className="workspace-tool-choice-list">
                {available.map((type) => (
                  <button
                    type="button"
                    key={type.trigger}
                    onClick={() => {
                      setChosen(type)
                      setValues({})
                      setFormProblem("")
                      add.reset()
                    }}
                  >
                    <span>
                      <strong>{type.name}</strong>
                      <small>{type.description}</small>
                    </span>
                    <ArrowUpRight size={15} />
                  </button>
                ))}
              </div>
            </>
          ) : (
            <form onSubmit={submit} className="workspace-tool-config-form">
              <button
                type="button"
                className="workspace-tool-text-button"
                onClick={() => setChosen(null)}
              >
                ← All events
              </button>
              <h5>{chosen.name}</h5>
              <p>{chosen.description}</p>
              {chosen.manual_setup && (
                <p className="workspace-tool-warning">
                  <CircleAlert size={14} /> This event also needs a webhook
                  configured in {toolkit.name}. {chosen.instructions}
                </p>
              )}
              {capability?.kind === "unsupported" ? (
                <p className="workspace-tool-warning">
                  This event needs a complex setup that this form cannot collect
                  yet.
                </p>
              ) : capability?.kind === "form" ? (
                capability.fields.map((field) => (
                  <TriggerFieldInput
                    key={field.name}
                    field={field}
                    value={values[field.name]}
                    onChange={(value) =>
                      setValues((current) => ({
                        ...current,
                        [field.name]: value,
                      }))
                    }
                  />
                ))
              ) : (
                <p className="workspace-tool-muted">No configuration needed.</p>
              )}
              {(formProblem || add.isError) && (
                <p className="workspace-tool-inline-error" role="alert">
                  {formProblem ||
                    appsErrorMessage(add.error, "Could not watch this event.")}
                </p>
              )}
              <button
                type="submit"
                className="workspace-tool-action"
                disabled={add.isPending || capability?.kind === "unsupported"}
              >
                {add.isPending ? "Adding…" : "Watch event"}
              </button>
            </form>
          )}
        </div>
      )}
    </div>
  )
}

function TriggerFieldInput({
  field,
  value,
  onChange,
}: {
  field: TriggerField
  value?: boolean | number | string
  onChange: (value: boolean | number | string) => void
}) {
  const id = `trigger-${field.name}`
  if (field.type === "boolean")
    return (
      <label className="workspace-tool-check">
        <input
          type="checkbox"
          checked={typeof value === "boolean" ? value : Boolean(field.default)}
          onChange={(event) => onChange(event.target.checked)}
        />
        {field.title}
      </label>
    )
  return (
    <label className="workspace-tool-field" htmlFor={id}>
      <span>
        {field.title}
        {field.required ? " *" : ""}
      </span>
      {field.type === "select" ? (
        <select
          id={id}
          value={
            value === undefined
              ? field.default === undefined
                ? ""
                : JSON.stringify(field.default)
              : JSON.stringify(value)
          }
          onChange={(event) =>
            onChange(
              JSON.parse(event.target.value) as boolean | number | string
            )
          }
        >
          <option value="" disabled>
            Choose an option
          </option>
          {field.options?.map((item) => (
            <option
              key={JSON.stringify(item.value)}
              value={JSON.stringify(item.value)}
            >
              {item.label}
            </option>
          ))}
        </select>
      ) : field.type === "strings" ? (
        <textarea
          id={id}
          value={String(value ?? field.default ?? "")}
          onChange={(event) => onChange(event.target.value)}
          placeholder="One value per line"
          rows={3}
        />
      ) : (
        <input
          id={id}
          type={field.type === "string" ? "text" : "number"}
          step={field.type === "integer" ? "1" : "any"}
          value={String(value ?? field.default ?? "")}
          onChange={(event) => onChange(event.target.value)}
        />
      )}
      {field.description && <small>{field.description}</small>}
    </label>
  )
}

function IndexingPane({
  connection,
  organizationId,
  preview,
  toolkit,
  onRuns,
}: {
  connection: Connection
  organizationId: string
  preview: boolean
  toolkit: Toolkit
  onRuns: (sourceId: string) => void
}) {
  const queryClient = useQueryClient()
  const providers = useQuery({ ...providersQueryOptions(), enabled: !preview })
  const listed = useQuery({
    ...sourcesQueryOptions(organizationId, { connectionId: connection.id }),
    enabled: !preview,
  })
  const [adding, setAdding] = useState(false)
  const [kindsId, setKindsId] = useState<string | null>(null)
  const [removeId, setRemoveId] = useState<string | null>(null)
  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: sourcesKey(organizationId) })
  const sync = useMutation({
    mutationFn: (sourceId: string) => syncSource(organizationId, sourceId),
    onSuccess: (_source, sourceId) =>
      Promise.all([
        refresh(),
        queryClient.invalidateQueries({
          queryKey: sourceRunsQueryKey(organizationId, sourceId),
        }),
      ]),
  })
  const remove = useMutation({
    mutationFn: (sourceId: string) => removeSource(organizationId, sourceId),
    onSuccess: () => {
      setRemoveId(null)
      return refresh()
    },
  })

  const loading = !preview && (providers.isPending || listed.isPending)
  const canIndex =
    isSourceProvider(toolkit.toolkit) &&
    (providers.data ?? []).includes(toolkit.toolkit)
  const sources = (listed.data ?? []).filter(
    (source) => source.status !== "removed"
  )
  const loadProblem = providers.isError
    ? appsErrorMessage(providers.error, "Indexing could not be checked.")
    : listed.isError
      ? appsErrorMessage(listed.error, "Sources could not be loaded.")
      : ""
  const actionProblem = sync.isError
    ? appsErrorMessage(sync.error, "Could not queue a run.")
    : remove.isError
      ? appsErrorMessage(remove.error, "Could not remove the source.")
      : ""

  return (
    <div className="workspace-tool-pane" role="tabpanel">
      <div className="workspace-tool-pane-head">
        <div>
          <h4>Searchable knowledge</h4>
          <p>
            Register what your agents may index, and choose how often it
            refreshes.
          </p>
        </div>
        <button
          type="button"
          className="workspace-tool-action"
          disabled={preview || loading || !canIndex}
          onClick={() => setAdding((value) => !value)}
        >
          <Plus size={14} /> Add source
        </button>
      </div>
      {loadProblem && (
        <p className="workspace-tool-inline-error" role="alert">
          {loadProblem}{" "}
          <button
            type="button"
            className="workspace-tool-text-button"
            onClick={() => {
              if (providers.isError) void providers.refetch()
              if (listed.isError) void listed.refetch()
            }}
          >
            Retry
          </button>
        </p>
      )}
      {actionProblem && (
        <p className="workspace-tool-inline-error" role="alert">
          {actionProblem}
        </p>
      )}
      {loading && <p className="workspace-tool-muted">Loading sources…</p>}
      {!preview && providers.isSuccess && !canIndex && (
        <p className="workspace-tool-muted">
          Indexing is not available for this app in this deployment.
        </p>
      )}
      {!preview && canIndex && listed.isSuccess && !sources.length && (
        <p className="workspace-tool-muted">
          No sources registered yet. Add one to make its content searchable.
        </p>
      )}
      {preview && (
        <div className="workspace-tool-row">
          <span className="workspace-tool-dot" />
          <div className="workspace-tool-row-copy">
            <strong>Example source</strong>
            <small>Scheduled hourly · no real runs</small>
          </div>
          <span className="workspace-tool-example">Example</span>
        </div>
      )}
      {sources.map((source) => {
        const name = sourceName(source.provider, source.target)
        const confirming = removeId === source.id
        return (
          <div key={source.id}>
            <div className="workspace-tool-row">
              <span
                className={`workspace-tool-dot ${source.status === "active" ? "is-live" : ""}`}
              />
              <div className="workspace-tool-row-copy">
                <strong>{name}</strong>
                <small>
                  {confirming
                    ? "Stops indexing it and removes what it indexed from search."
                    : source.status === "paused"
                      ? sourceReasonText(source.reason)
                      : `${intervalLabel(source.interval_seconds)} · Last run ${prettyTime(source.last_synced_at)}${source.include_kinds ? " · Chosen kinds" : ""}`}
                </small>
              </div>
              {confirming ? (
                <span className="workspace-tool-confirm">
                  <button
                    type="button"
                    disabled={remove.isPending}
                    onClick={() => setRemoveId(null)}
                  >
                    Cancel
                  </button>
                  <button
                    type="button"
                    disabled={remove.isPending}
                    onClick={() => remove.mutate(source.id)}
                    aria-label={`Stop indexing ${name}`}
                  >
                    {remove.isPending ? "Removing…" : "Confirm remove"}
                  </button>
                </span>
              ) : (
                <span className="workspace-tool-row-actions">
                  <button
                    type="button"
                    className="workspace-tool-text-button"
                    onClick={() => onRuns(source.id)}
                  >
                    Runs
                  </button>
                  <button
                    type="button"
                    className="workspace-tool-text-button"
                    aria-expanded={kindsId === source.id}
                    aria-label={`Kinds of document indexed from ${name}`}
                    onClick={() =>
                      setKindsId((current) =>
                        current === source.id ? null : source.id
                      )
                    }
                  >
                    Kinds
                  </button>
                  <button
                    type="button"
                    className="workspace-tool-text-button"
                    disabled={
                      (sync.isPending && sync.variables === source.id) ||
                      source.status !== "active"
                    }
                    onClick={() => sync.mutate(source.id)}
                    aria-label={`Run ${name} now`}
                  >
                    <RotateCw size={13} />{" "}
                    {sync.isPending && sync.variables === source.id
                      ? "Queuing…"
                      : "Run now"}
                  </button>
                  <button
                    type="button"
                    className="workspace-tool-text-button"
                    aria-label={`Remove ${name}`}
                    onClick={() => {
                      remove.reset()
                      setRemoveId(source.id)
                    }}
                  >
                    Remove
                  </button>
                </span>
              )}
            </div>
            {kindsId === source.id && !confirming && (
              <SourceKindsForm
                source={source}
                organizationId={organizationId}
                onClose={() => setKindsId(null)}
              />
            )}
          </div>
        )
      })}
      {adding && !preview && canIndex && (
        <IndexSourceForm
          connection={connection}
          organizationId={organizationId}
          provider={toolkit.toolkit as SourceProvider}
          onCancel={() => setAdding(false)}
          onAdded={() => setAdding(false)}
        />
      )}
    </div>
  )
}

/**
 * Which kinds of document a source indexes. Everything read is sorted into a
 * kind as it arrives; a new choice is applied to what is already indexed on
 * the source's next run, which knowledge brings forward.
 */
function SourceKindsForm({
  source,
  organizationId,
  onClose,
}: {
  source: Source
  organizationId: string
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const kinds = useQuery(kindsQueryOptions())
  const [chosen, setChosen] = useState<Set<string> | null>(null)
  const selected =
    chosen ??
    new Set(kinds.data ? effectiveKinds(kinds.data, source.include_kinds) : [])
  const save = useMutation({
    mutationFn: (include: string[] | null) =>
      setSourceKinds(organizationId, source.id, include),
    onSuccess: () => {
      onClose()
      return queryClient.invalidateQueries({
        queryKey: sourcesKey(organizationId),
      })
    },
  })
  const name = sourceName(source.provider, source.target)

  function toggle(kind: string, checked: boolean) {
    const next = new Set(selected)
    if (checked) next.add(kind)
    else next.delete(kind)
    setChosen(next)
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!kinds.data || selected.size === 0 || save.isPending) return
    // Sent in the order kinds are shown, as knowledge keeps them.
    save.mutate(
      kinds.data
        .filter((kind) => selected.has(kind.name))
        .map((kind) => kind.name)
    )
  }

  return (
    <form
      className="workspace-tool-form-panel workspace-tool-config-form"
      aria-label={`Kinds indexed from ${name}`}
      onSubmit={submit}
    >
      <div className="workspace-tool-pane-head">
        <div>
          <h5>What to index from {name}</h5>
          <p>
            Everything read here is sorted into kinds. Choose which your agents
            can search; what is already indexed follows on the next run.
          </p>
        </div>
        <button
          type="button"
          className="workspace-tool-text-button"
          disabled={save.isPending}
          onClick={onClose}
        >
          Cancel
        </button>
      </div>
      {kinds.isPending && (
        <p className="workspace-tool-muted">Loading kinds…</p>
      )}
      {kinds.isError && (
        <p className="workspace-tool-inline-error" role="alert">
          {appsErrorMessage(kinds.error, "The kinds could not be loaded.")}
        </p>
      )}
      {kinds.data && (
        <div className="workspace-tool-kinds">
          {kinds.data.map((kind) => (
            <label className="workspace-tool-check" key={kind.name}>
              <input
                type="checkbox"
                checked={selected.has(kind.name)}
                onChange={(event) => toggle(kind.name, event.target.checked)}
              />
              <span>
                <strong>{kind.label}</strong>
                <small>
                  {kind.hint}
                  {kind.default ? "" : " · off by default"}
                </small>
              </span>
            </label>
          ))}
        </div>
      )}
      {kinds.data && selected.size === 0 && (
        <p className="workspace-tool-warning">
          Choose at least one kind. To index nothing, remove the source.
        </p>
      )}
      {save.isError && (
        <p className="workspace-tool-inline-error" role="alert">
          {appsErrorMessage(save.error, "Could not save the choice.")}
        </p>
      )}
      <span className="workspace-tool-row-actions">
        <button
          type="button"
          className="workspace-tool-text-button"
          disabled={save.isPending || !kinds.data}
          onClick={() => save.mutate(null)}
        >
          Use defaults
        </button>
        <button
          type="submit"
          className="workspace-tool-action"
          disabled={save.isPending || !kinds.data || selected.size === 0}
        >
          {save.isPending ? "Saving…" : "Save kinds"}
        </button>
      </span>
    </form>
  )
}

function IndexSourceForm({
  connection,
  organizationId,
  provider,
  onCancel,
  onAdded,
}: {
  connection: Connection
  organizationId: string
  provider: SourceProvider
  onCancel: () => void
  onAdded: () => void
}) {
  const queryClient = useQueryClient()
  // The app is asked what it holds, so nobody has to know a channel's id.
  // Typing one stays possible: listing can be refused a scope or find
  // nothing, and neither should stop a member indexing what they can name.
  const targets = useQuery(
    connectionTargetsQueryOptions(organizationId, connection.id)
  )
  const [targetId, setTargetId] = useState("")
  const [manual, setManual] = useState(false)
  const [rawTarget, setRawTarget] = useState("")
  const [intervalSeconds, setIntervalSeconds] = useState(
    defaultSourceIntervalSeconds
  )
  const [problem, setProblem] = useState("")
  const register = useMutation({
    mutationFn: (target: Record<string, string>) =>
      registerSource(organizationId, {
        connectionId: connection.id,
        provider,
        target,
        intervalSeconds,
      }),
    onSuccess: () => {
      onAdded()
      return queryClient.invalidateQueries({
        queryKey: sourcesKey(organizationId),
      })
    },
  })
  const field = sourceFields[provider]
  const discovered = targets.data ?? []
  const picker = discovered.length > 0 && !manual
  const hintId = useId()
  const hint = targets.isPending
    ? "Reading what this app holds…"
    : targets.isError
      ? "The app didn't list what it holds; enter one by hand."
      : ""

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (register.isPending) return
    // A choice from the list is registered with the target the app listed
    // it with, not one rebuilt from its id.
    const chosen = picker
      ? discovered.find((target) => target.id === targetId)
      : undefined
    if (picker && !chosen) {
      setProblem(`Choose a ${field.label.toLowerCase()} or enter one manually.`)
      return
    }
    const validation = picker
      ? undefined
      : sourceTargetProblem(provider, rawTarget)
    if (validation) {
      setProblem(validation)
      return
    }
    setProblem("")
    register.mutate(chosen?.target ?? sourceTarget(provider, rawTarget))
  }

  return (
    <form
      className="workspace-tool-form-panel workspace-tool-config-form"
      onSubmit={submit}
    >
      <div className="workspace-tool-pane-head">
        <div>
          <h5>Add to searchable knowledge</h5>
          <p>{field.help}</p>
        </div>
        <button
          type="button"
          className="workspace-tool-text-button"
          onClick={onCancel}
        >
          Cancel
        </button>
      </div>
      <label className="workspace-tool-field">
        <span>
          {field.label}
          {field.required ? " *" : " (optional)"}
        </span>
        {picker ? (
          <select
            value={targetId}
            aria-describedby={hint ? hintId : undefined}
            onChange={(event) => setTargetId(event.target.value)}
          >
            <option value="">Choose a {field.label.toLowerCase()}…</option>
            {discovered.map((target) => (
              <option key={target.id} value={target.id}>
                {target.name}
              </option>
            ))}
          </select>
        ) : (
          <input
            value={rawTarget}
            aria-describedby={hint ? hintId : undefined}
            onChange={(event) => setRawTarget(event.target.value)}
            placeholder={field.placeholder}
          />
        )}
      </label>
      {hint && (
        <small className="workspace-tool-field-hint" id={hintId}>
          {hint}
        </small>
      )}
      {discovered.length > 0 && (
        <button
          type="button"
          className="workspace-tool-text-button workspace-tool-manual-toggle"
          onClick={() => {
            setManual((value) => !value)
            setTargetId("")
            setRawTarget("")
          }}
        >
          {manual ? "Choose from app instead" : "Enter one manually"}
        </button>
      )}
      <label className="workspace-tool-field">
        <span>Refresh schedule</span>
        <select
          value={intervalSeconds}
          onChange={(event) => setIntervalSeconds(Number(event.target.value))}
        >
          {intervalChoices.map((choice) => (
            <option key={choice.value} value={choice.value}>
              {choice.label}
            </option>
          ))}
        </select>
      </label>
      {(problem || register.isError) && (
        <p className="workspace-tool-inline-error" role="alert">
          {problem ||
            appsErrorMessage(register.error, "Could not register the source.")}
        </p>
      )}
      <button
        className="workspace-tool-action"
        type="submit"
        disabled={register.isPending}
      >
        {register.isPending ? "Adding…" : "Register source"}
      </button>
    </form>
  )
}

function RunsPane({
  connection,
  organizationId,
  preview,
  initialSourceId,
}: {
  connection: Connection
  organizationId: string
  preview: boolean
  initialSourceId: string | null
}) {
  const listed = useQuery({
    ...sourcesQueryOptions(organizationId, { connectionId: connection.id }),
    enabled: !preview,
  })
  const sources = (listed.data ?? []).filter(
    (source) => source.status !== "removed"
  )
  const [sourceId, setSourceId] = useState(initialSourceId || "")
  const selectedId = sources.some((source) => source.id === sourceId)
    ? sourceId
    : sources[0]?.id || ""
  const runs = useQuery({
    ...sourceRunsQueryOptions(organizationId, selectedId),
    enabled: !preview && Boolean(selectedId),
  })
  const loading =
    !preview && (listed.isPending || (Boolean(selectedId) && runs.isPending))
  const problem = listed.isError
    ? appsErrorMessage(listed.error, "Sources could not be loaded.")
    : runs.isError
      ? appsErrorMessage(runs.error, "Runs could not be loaded.")
      : ""

  return (
    <div className="workspace-tool-pane" role="tabpanel">
      <div className="workspace-tool-pane-head">
        <div>
          <h4>Recent sync runs</h4>
          <p>See when a registered source last brought new context in.</p>
        </div>
        {!preview && sources.length > 0 && (
          <select
            className="workspace-tool-source-select"
            value={selectedId}
            onChange={(event) => setSourceId(event.target.value)}
            aria-label="Run source"
          >
            {sources.map((source) => (
              <option key={source.id} value={source.id}>
                {sourceName(source.provider, source.target)}
              </option>
            ))}
          </select>
        )}
      </div>
      {loading && <p className="workspace-tool-muted">Loading runs…</p>}
      {problem && (
        <p className="workspace-tool-inline-error" role="alert">
          {problem}{" "}
          <button
            type="button"
            className="workspace-tool-text-button"
            onClick={() => {
              if (listed.isError) void listed.refetch()
              else void runs.refetch()
            }}
          >
            Retry
          </button>
        </p>
      )}
      {preview ? (
        <div className="workspace-tool-row">
          <span className="workspace-tool-dot" />
          <div className="workspace-tool-row-copy">
            <strong>Example run</strong>
            <small>Published 8 documents · no real run recorded</small>
          </div>
          <span className="workspace-tool-example">Example</span>
        </div>
      ) : listed.isSuccess && !selectedId ? (
        <p className="workspace-tool-muted">
          Register a source in Indexing to see its runs here.
        </p>
      ) : runs.isSuccess && !runs.data.length ? (
        <p className="workspace-tool-muted">No runs yet for this source.</p>
      ) : (
        (runs.data ?? []).map((run) => (
          <div className="workspace-tool-row" key={run.id}>
            <span
              className={`workspace-tool-dot ${run.status === "completed" ? "is-live" : run.status === "failed" ? "is-error" : ""}`}
            />
            <div className="workspace-tool-row-copy">
              <strong>
                {run.status === "completed"
                  ? "Completed"
                  : run.status === "failed"
                    ? "Failed"
                    : "Running"}{" "}
                · {prettyTime(run.started_at)}
              </strong>
              <small>
                {run.documents_published} published · {run.documents_removed}{" "}
                removed · {run.documents_seen} seen
                {run.documents_excluded > 0
                  ? ` · ${run.documents_excluded} left out`
                  : ""}
              </small>
              {run.error && (
                <small className="workspace-tool-run-error">{run.error}</small>
              )}
            </div>
            {run.status === "running" ? (
              <Clock3 size={16} />
            ) : run.status === "completed" ? (
              <Check size={16} />
            ) : (
              <CircleAlert size={16} />
            )}
          </div>
        ))
      )}
    </div>
  )
}
