import { useEffect, useState } from "react"
import type { FormEvent } from "react"
import {
  ArrowUpRight,
  Check,
  CircleAlert,
  Clock3,
  Plus,
  RotateCw,
} from "lucide-react"
import { readApi, writeApi } from "./workspace-tools-api"
import {
  buildTriggerConfig,
  intervalChoices,
  isSourceProvider,
  sourceFields,
  sourceName,
  sourceTarget,
  sourceTargetProblem,
  triggerCapability,
} from "./workspace-tool-helpers"
import type { SourceProvider, TriggerField } from "./workspace-tool-helpers"
import type { Connection, Toolkit } from "./workspace-tools-data"

type Pane = "events" | "indexing" | "runs"

type TriggerType = {
  trigger: string
  name: string
  description: string
  kind: string
  config_schema?: unknown
  instructions?: string
  manual_setup?: boolean
}

type Trigger = {
  id: string
  trigger: string
  status: "active" | "disabled" | "removed"
  kind?: string
  setup?: string
  delivery_count: number
  last_delivery_at?: string
}

type Source = {
  id: string
  provider: string
  target: Record<string, string>
  status: "active" | "paused" | "removed"
  reason?: string
  interval_seconds: number
  last_synced_at?: string
}

type Run = {
  id: string
  status: "running" | "completed" | "failed"
  started_at: string
  documents_seen: number
  documents_published: number
  documents_removed: number
  documents_excluded?: number
  error?: string
}

type Target = {
  id: string
  name: string
  description?: string
  target: Record<string, string>
}

const previewEvents: TriggerType[] = [
  {
    trigger: "EXAMPLE_NEW_ITEM",
    name: "New item",
    description: "Wake your agents when something new arrives.",
    kind: "webhook",
  },
  {
    trigger: "EXAMPLE_ITEM_UPDATED",
    name: "Item updated",
    description: "Keep work current when an item changes.",
    kind: "webhook",
  },
]

function prettyTime(value?: string) {
  if (!value) return "Never"
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return "Unknown"
  return date.toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  })
}

function errorText(error: unknown) {
  return error instanceof Error
    ? error.message
    : "This request could not be completed."
}

const TRIGGER_TYPES_PAGE_LIMIT = 50
const TRIGGER_TYPES_MAX_PAGES = 10

// The catalog clamps `limit` to 50 server-side, so an app with more trigger
// types than fit on one page needs its `next_cursor` followed to be listed
// in full.
async function fetchTriggerTypes(toolkit: string): Promise<TriggerType[]> {
  const types: TriggerType[] = []
  let cursor: string | null = null
  for (let page = 0; page < TRIGGER_TYPES_MAX_PAGES; page++) {
    const params = new URLSearchParams({
      limit: String(TRIGGER_TYPES_PAGE_LIMIT),
    })
    if (cursor) params.set("cursor", cursor)
    const data = await readApi<{
      triggers: TriggerType[]
      next_cursor: string | null
    }>(`/v1/apps/catalog/${encodeURIComponent(toolkit)}/triggers?${params}`)
    types.push(...data.triggers)
    if (!data.next_cursor) break
    cursor = data.next_cursor
  }
  return types
}

export function ConnectionWorkspace({
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
  const [types, setTypes] = useState<TriggerType[]>([])
  const [triggers, setTriggers] = useState<Trigger[]>([])
  const [sources, setSources] = useState<Source[]>([])
  const [providers, setProviders] = useState<string[]>([])
  const [eventLoading, setEventLoading] = useState(false)
  const [sourceLoading, setSourceLoading] = useState(false)
  const [problem, setProblem] = useState("")
  const [eventRefresh, setEventRefresh] = useState(0)
  const [sourceRefresh, setSourceRefresh] = useState(0)
  const [runsSourceId, setRunsSourceId] = useState<string | null>(null)

  useEffect(() => {
    if (preview || pane !== "events") return
    let cancelled = false
    setEventLoading(true)
    setProblem("")
    const root = `/v1/organizations/${encodeURIComponent(organizationId)}`
    const search = new URLSearchParams({ connection_id: connection.id })
    Promise.all([
      fetchTriggerTypes(toolkit.toolkit),
      readApi<{ triggers: Trigger[] }>(`${root}/apps/triggers?${search}`),
    ])
      .then(([catalogTypes, subscribed]) => {
        if (cancelled) return
        setTypes(catalogTypes)
        setTriggers(subscribed.triggers)
      })
      .catch((error: unknown) => {
        if (!cancelled) setProblem(errorText(error))
      })
      .finally(() => {
        if (!cancelled) setEventLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [
    connection.id,
    eventRefresh,
    organizationId,
    pane,
    preview,
    toolkit.toolkit,
  ])

  useEffect(() => {
    if (preview || (pane !== "indexing" && pane !== "runs")) return
    let cancelled = false
    setSourceLoading(true)
    setProblem("")
    const root = `/v1/organizations/${encodeURIComponent(organizationId)}`
    const search = new URLSearchParams({ connection_id: connection.id })
    Promise.all([
      readApi<{ providers: string[] }>("/v1/sources/providers"),
      readApi<{ sources: Source[] }>(`${root}/sources?${search}`),
    ])
      .then(([available, listed]) => {
        if (cancelled) return
        setProviders(available.providers)
        setSources(listed.sources)
      })
      .catch((error: unknown) => {
        if (!cancelled) setProblem(errorText(error))
      })
      .finally(() => {
        if (!cancelled) setSourceLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [connection.id, organizationId, pane, preview, sourceRefresh])

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
      {problem && (
        <p className="workspace-tool-inline-error" role="alert">
          {problem}
        </p>
      )}
      {pane === "events" && (
        <EventsPane
          connection={connection}
          organizationId={organizationId}
          preview={preview}
          triggers={triggers}
          types={preview ? previewEvents : types}
          loading={eventLoading}
          onChange={() => setEventRefresh((current) => current + 1)}
        />
      )}
      {pane === "indexing" && (
        <IndexingPane
          connection={connection}
          organizationId={organizationId}
          preview={preview}
          toolkit={toolkit}
          providers={providers}
          sources={sources}
          loading={sourceLoading}
          onChange={() => setSourceRefresh((current) => current + 1)}
          onRuns={(sourceId) => {
            setRunsSourceId(sourceId)
            setPane("runs")
          }}
        />
      )}
      {pane === "runs" && (
        <RunsPane
          organizationId={organizationId}
          preview={preview}
          sources={sources}
          loading={sourceLoading}
          initialSourceId={runsSourceId}
        />
      )}
    </div>
  )
}

function EventsPane({
  connection,
  organizationId,
  preview,
  types,
  triggers,
  loading,
  onChange,
}: {
  connection: Connection
  organizationId: string
  preview: boolean
  types: TriggerType[]
  triggers: Trigger[]
  loading: boolean
  onChange: () => void
}) {
  const [adding, setAdding] = useState(false)
  const [query, setQuery] = useState("")
  const [chosen, setChosen] = useState<TriggerType | null>(null)
  const [values, setValues] = useState<
    Record<string, boolean | number | string>
  >({})
  const [busy, setBusy] = useState(false)
  const [problem, setProblem] = useState("")
  const [stopId, setStopId] = useState<string | null>(null)
  const typeBySlug = new Map(types.map((type) => [type.trigger, type]))
  const subscribed = new Set(
    triggers
      .filter((item) => item.status !== "removed")
      .map((item) => item.trigger)
  )
  const available = types.filter(
    (type) =>
      !subscribed.has(type.trigger) &&
      `${type.name} ${type.description}`
        .toLowerCase()
        .includes(query.toLowerCase())
  )
  const capability = chosen ? triggerCapability(chosen.config_schema) : null

  async function add(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!chosen || preview || busy || capability?.kind === "unsupported") return
    const outcome =
      capability?.kind === "form"
        ? buildTriggerConfig(capability.fields, values)
        : { config: {} }
    if (outcome.problem) {
      setProblem(outcome.problem)
      return
    }
    setBusy(true)
    setProblem("")
    try {
      await writeApi(
        `/v1/organizations/${encodeURIComponent(organizationId)}/apps/triggers`,
        "POST",
        {
          connection_id: connection.id,
          trigger: chosen.trigger,
          config: outcome.config,
        }
      )
      setChosen(null)
      setValues({})
      setAdding(false)
      onChange()
    } catch (error) {
      setProblem(errorText(error))
    } finally {
      setBusy(false)
    }
  }

  async function stop(id: string) {
    setBusy(true)
    setProblem("")
    try {
      await writeApi(
        `/v1/organizations/${encodeURIComponent(organizationId)}/apps/triggers/${encodeURIComponent(id)}`,
        "DELETE"
      )
      setStopId(null)
      onChange()
    } catch (error) {
      setProblem(errorText(error))
    } finally {
      setBusy(false)
    }
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
          disabled={preview || loading}
          onClick={() => setAdding((value) => !value)}
        >
          <Plus size={14} /> Add event
        </button>
      </div>
      {loading && <p className="workspace-tool-muted">Loading events…</p>}
      {problem && (
        <p className="workspace-tool-inline-error" role="alert">
          {problem}
        </p>
      )}
      {!loading &&
        triggers.filter((item) => item.status !== "removed").length === 0 &&
        !preview && (
          <p className="workspace-tool-muted">
            No events watched yet. Choose an event below to start.
          </p>
        )}
      {triggers
        .filter((item) => item.status !== "removed")
        .map((trigger) => {
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
                    : trigger.setup || "Disabled"}
                </small>
              </div>
              {stopId === trigger.id ? (
                <span className="workspace-tool-confirm">
                  <button type="button" onClick={() => setStopId(null)}>
                    Cancel
                  </button>
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => void stop(trigger.id)}
                  >
                    Confirm stop
                  </button>
                </span>
              ) : (
                <button
                  type="button"
                  className="workspace-tool-text-button"
                  onClick={() => setStopId(trigger.id)}
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
              }}
            >
              Cancel
            </button>
          </div>
          {!chosen ? (
            <>
              {types.length > 5 && (
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
                      setProblem("")
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
            <form
              onSubmit={(event) => void add(event)}
              className="workspace-tool-config-form"
            >
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
                  configured in {connection.toolkit}. {chosen.instructions}
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
              <button
                type="submit"
                className="workspace-tool-action"
                disabled={busy || capability?.kind === "unsupported"}
              >
                {busy ? "Adding…" : "Watch event"}
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
  providers,
  sources,
  loading,
  onChange,
  onRuns,
}: {
  connection: Connection
  organizationId: string
  preview: boolean
  toolkit: Toolkit
  providers: string[]
  sources: Source[]
  loading: boolean
  onChange: () => void
  onRuns: (sourceId: string) => void
}) {
  const [adding, setAdding] = useState(false)
  const [busyId, setBusyId] = useState<string | null>(null)
  const [problem, setProblem] = useState("")
  const canIndex =
    isSourceProvider(toolkit.toolkit) && providers.includes(toolkit.toolkit)

  async function sync(source: Source) {
    setBusyId(source.id)
    setProblem("")
    try {
      await writeApi(
        `/v1/organizations/${encodeURIComponent(organizationId)}/sources/${encodeURIComponent(source.id)}/sync`,
        "POST"
      )
      onChange()
    } catch (error) {
      setProblem(errorText(error))
    } finally {
      setBusyId(null)
    }
  }

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
      {problem && (
        <p className="workspace-tool-inline-error" role="alert">
          {problem}
        </p>
      )}
      {loading && <p className="workspace-tool-muted">Loading sources…</p>}
      {!loading && !preview && !canIndex && (
        <p className="workspace-tool-muted">
          Indexing is not available for this app in this deployment.
        </p>
      )}
      {!loading && !preview && canIndex && !sources.length && (
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
      {sources
        .filter((source) => source.status !== "removed")
        .map((source) => (
          <div className="workspace-tool-row" key={source.id}>
            <span
              className={`workspace-tool-dot ${source.status === "active" ? "is-live" : ""}`}
            />
            <div className="workspace-tool-row-copy">
              <strong>{sourceName(source.provider, source.target)}</strong>
              <small>
                {source.status === "paused"
                  ? source.reason || "Needs reconnecting"
                  : `${intervalChoices.find((choice) => choice.value === source.interval_seconds)?.label || `Every ${source.interval_seconds}s`} · Last run ${prettyTime(source.last_synced_at)}`}
              </small>
            </div>
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
                disabled={busyId === source.id || source.status !== "active"}
                onClick={() => void sync(source)}
                aria-label={`Run ${sourceName(source.provider, source.target)} now`}
              >
                <RotateCw size={13} />{" "}
                {busyId === source.id ? "Queuing…" : "Run now"}
              </button>
            </span>
          </div>
        ))}
      {adding && !preview && canIndex && (
        <IndexSourceForm
          connection={connection}
          organizationId={organizationId}
          provider={toolkit.toolkit as SourceProvider}
          onCancel={() => setAdding(false)}
          onAdded={() => {
            setAdding(false)
            onChange()
          }}
        />
      )}
    </div>
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
  const [targets, setTargets] = useState<Target[]>([])
  const [targetId, setTargetId] = useState("")
  const [manual, setManual] = useState(false)
  const [rawTarget, setRawTarget] = useState("")
  const [interval, setInterval] = useState(3600)
  const [busy, setBusy] = useState(false)
  const [problem, setProblem] = useState("")
  const field = sourceFields[provider]

  useEffect(() => {
    let cancelled = false
    readApi<{ targets: Target[] }>(
      `/v1/organizations/${encodeURIComponent(organizationId)}/apps/connections/${encodeURIComponent(connection.id)}/targets`
    )
      .then((data) => {
        if (!cancelled) setTargets(data.targets)
      })
      .catch(() => {
        /* Manual target entry remains available. */
      })
    return () => {
      cancelled = true
    }
  }, [connection.id, organizationId])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const picker = targets.length > 0 && !manual
    const chosen = targets.find((target) => target.id === targetId)
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
    setBusy(true)
    setProblem("")
    try {
      await writeApi(
        `/v1/organizations/${encodeURIComponent(organizationId)}/sources`,
        "POST",
        {
          connection_id: connection.id,
          provider,
          target: chosen?.target || sourceTarget(provider, rawTarget),
          interval_seconds: interval,
        }
      )
      onAdded()
    } catch (error) {
      setProblem(errorText(error))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form
      className="workspace-tool-form-panel workspace-tool-config-form"
      onSubmit={(event) => void submit(event)}
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
        {targets.length > 0 && !manual ? (
          <select
            value={targetId}
            onChange={(event) => setTargetId(event.target.value)}
          >
            <option value="">Choose a {field.label.toLowerCase()}…</option>
            {targets.map((target) => (
              <option key={target.id} value={target.id}>
                {target.name}
              </option>
            ))}
          </select>
        ) : (
          <input
            value={rawTarget}
            onChange={(event) => setRawTarget(event.target.value)}
            placeholder={field.placeholder}
          />
        )}
      </label>
      {targets.length > 0 && (
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
          value={interval}
          onChange={(event) => setInterval(Number(event.target.value))}
        >
          {intervalChoices.map((choice) => (
            <option key={choice.value} value={choice.value}>
              {choice.label}
            </option>
          ))}
        </select>
      </label>
      {problem && (
        <p className="workspace-tool-inline-error" role="alert">
          {problem}
        </p>
      )}
      <button className="workspace-tool-action" type="submit" disabled={busy}>
        {busy ? "Adding…" : "Register source"}
      </button>
    </form>
  )
}

function RunsPane({
  organizationId,
  preview,
  sources,
  loading,
  initialSourceId,
}: {
  organizationId: string
  preview: boolean
  sources: Source[]
  loading: boolean
  initialSourceId: string | null
}) {
  const [sourceId, setSourceId] = useState(initialSourceId || "")
  const [runs, setRuns] = useState<Run[]>([])
  const [busy, setBusy] = useState(false)
  const [problem, setProblem] = useState("")
  const selectedId = sources.some((source) => source.id === sourceId)
    ? sourceId
    : sources[0]?.id || ""

  useEffect(() => {
    if (preview || !selectedId) return
    let cancelled = false
    setBusy(true)
    setProblem("")
    setRuns([])
    readApi<{ runs: Run[] }>(
      `/v1/organizations/${encodeURIComponent(organizationId)}/sources/${encodeURIComponent(selectedId)}/runs?limit=10`
    )
      .then((data) => {
        if (!cancelled) setRuns(data.runs)
      })
      .catch((error: unknown) => {
        if (!cancelled) setProblem(errorText(error))
      })
      .finally(() => {
        if (!cancelled) setBusy(false)
      })
    return () => {
      cancelled = true
    }
  }, [organizationId, preview, selectedId])

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
      {loading || busy ? (
        <p className="workspace-tool-muted">Loading runs…</p>
      ) : null}
      {problem && (
        <p className="workspace-tool-inline-error" role="alert">
          {problem}
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
      ) : !loading && !selectedId ? (
        <p className="workspace-tool-muted">
          Register a source in Indexing to see its runs here.
        </p>
      ) : !busy && !runs.length ? (
        <p className="workspace-tool-muted">No runs yet for this source.</p>
      ) : (
        runs.map((run) => (
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
