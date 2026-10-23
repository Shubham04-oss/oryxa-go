// What a specialist may reach and which model it answers on (I3, I9): its
// owner chooses the apps it reads, and in which of them it may change
// things; the collections (rag corpora) it may search; and lite or main. A
// grant replaces the whole grant on save, so each card keeps the other's part.
import { useMemo, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { agentsErrorMessage } from "./agents-format"
import { countLabel } from "./knowledge-format"
import { afterUpdateAgent } from "@/lib/agents/invalidation"
import { ALL_APPS, ALL_CORPORA, updateAgent } from "@/lib/agents/managed-agents"
import type {
  AgentGrant,
  ManagedAgent,
  ModelTier,
} from "@/lib/agents/managed-agents"
import { connectionsQueryOptions, toolkitName } from "@/lib/apps/client"
import { corporaQueryOptions } from "@/lib/rag/client"
import { cn } from "@/lib/utils"

/** A specialist read before it had a grant reads everything, writes nothing. */
export function grantOf(agent: ManagedAgent): AgentGrant {
  return agent.grant ?? { apps: [ALL_APPS] }
}

export type AppChoice = {
  /** Every app the owner connects, read only, unless chosen one by one. */
  every: boolean
  reads: string[]
  writes: string[]
}

export function choiceOf(grant: AgentGrant): AppChoice {
  const every = grant.apps.includes(ALL_APPS)
  return {
    every,
    reads: every ? [] : grant.apps,
    writes: grant.write_apps ?? [],
  }
}

/** The grant a choice saves as; tools and corpora are kept as they were. */
export function grantFrom(choice: AppChoice, previous: AgentGrant): AgentGrant {
  const reads = choice.every ? [ALL_APPS] : [...new Set(choice.reads)].sort()
  // An app it may change things in is one it reads.
  const writes = [...new Set(choice.writes)]
    .filter((app) => choice.every || choice.reads.includes(app))
    .sort()
  return {
    apps: reads,
    tools: previous.tools ?? [],
    write_apps: writes,
    write_tools: previous.write_tools ?? [],
    corpora: previous.corpora ?? [ALL_CORPORA],
  }
}

/** Which collections it may search: all, the ones chosen, or none. */
export type CorporaChoice = {
  scope: "all" | "some" | "none"
  /** The chosen corpus ids, when `scope` is "some". */
  ids: string[]
}

/** A grant without corpora, or saved before they existed, searches all. */
export function corporaChoiceOf(grant: AgentGrant): CorporaChoice {
  const corpora = grant.corpora
  if (!corpora || corpora.includes(ALL_CORPORA))
    return { scope: "all", ids: [] }
  if (corpora.length === 0) return { scope: "none", ids: [] }
  return { scope: "some", ids: corpora }
}

/** The grant a corpora choice saves as; its apps and tools are kept. */
export function grantWithCorpora(
  choice: CorporaChoice,
  previous: AgentGrant
): AgentGrant {
  const corpora =
    choice.scope === "all"
      ? [ALL_CORPORA]
      : choice.scope === "none"
        ? []
        : [...new Set(choice.ids)].sort()
  return {
    apps: previous.apps,
    tools: previous.tools ?? [],
    write_apps: previous.write_apps ?? [],
    write_tools: previous.write_tools ?? [],
    corpora,
  }
}

function sameList(x: string[], y: string[]): boolean {
  return (
    x.length === y.length &&
    [...x].sort().every((v, i) => v === [...y].sort()[i])
  )
}

function sameChoice(a: AppChoice, b: AppChoice): boolean {
  return (
    a.every === b.every &&
    sameList(a.reads, b.reads) &&
    sameList(a.writes, b.writes)
  )
}

const tiers: { value: ModelTier; label: string; detail: string }[] = [
  {
    value: "lite",
    label: "Lite",
    detail: "Quick and light: lookups, summaries and short answers.",
  },
  {
    value: "main",
    label: "Main",
    detail: "Reasons through harder work. Slower, and costs more.",
  },
]

export function SpecialistAccess({
  organizationId,
  agent,
}: {
  organizationId: string
  agent: ManagedAgent
}) {
  return (
    <>
      <ModelCard organizationId={organizationId} agent={agent} />
      <AppsCard organizationId={organizationId} agent={agent} />
      <CollectionsCard organizationId={organizationId} agent={agent} />
    </>
  )
}

function ModelCard({
  organizationId,
  agent,
}: {
  organizationId: string
  agent: ManagedAgent
}) {
  const queryClient = useQueryClient()
  const tier = agent.model_tier ?? "lite"
  const change = useMutation({
    mutationFn: (model_tier: ModelTier) =>
      updateAgent(organizationId, agent.id, { model_tier }),
    onSuccess: (updated) =>
      afterUpdateAgent(queryClient, organizationId, updated),
  })
  const shown = change.isPending ? change.variables : tier
  return (
    <div className="workspace-work-card">
      <h3>Model</h3>
      <div
        className="workspace-access-tiers"
        role="radiogroup"
        aria-label={`${agent.name}'s model`}
      >
        {tiers.map((option) => (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={shown === option.value}
            className={cn(
              "workspace-access-tier",
              shown === option.value && "is-active"
            )}
            disabled={change.isPending}
            onClick={() => {
              if (option.value !== tier) change.mutate(option.value)
            }}
          >
            <strong>{option.label}</strong>
            <span>{option.detail}</span>
          </button>
        ))}
      </div>
      {change.isError && (
        <p className="workspace-agents-error" role="alert">
          {agentsErrorMessage(change.error, "Couldn't change its model.")}
        </p>
      )}
    </div>
  )
}

function AppsCard({
  organizationId,
  agent,
}: {
  organizationId: string
  agent: ManagedAgent
}) {
  const queryClient = useQueryClient()
  const connections = useQuery(connectionsQueryOptions(organizationId))
  const saved = choiceOf(grantOf(agent))
  const [draft, setDraft] = useState<AppChoice>(saved)
  const save = useMutation({
    mutationFn: (choice: AppChoice) =>
      updateAgent(organizationId, agent.id, {
        grant: grantFrom(choice, grantOf(agent)),
      }),
    onSuccess: (updated) => {
      setDraft(choiceOf(grantOf(updated)))
      return afterUpdateAgent(queryClient, organizationId, updated)
    },
  })

  // The owner's connected apps, and any app the grant names that isn't
  // connected any more, so it can still be taken off.
  const apps = useMemo(() => {
    const connected = new Set(
      (connections.data ?? [])
        .filter((connection) => connection.status === "active")
        .map((connection) => connection.toolkit.toLowerCase())
    )
    const named = new Set([...saved.reads, ...saved.writes])
    return [...new Set([...connected, ...named])]
      .sort((a, b) => toolkitName(a).localeCompare(toolkitName(b)))
      .map((app) => ({ app, connected: connected.has(app) }))
  }, [connections.data, saved.reads, saved.writes])

  const dirty = !sameChoice(draft, saved)
  const reads = (app: string) => draft.every || draft.reads.includes(app)
  const writes = (app: string) => reads(app) && draft.writes.includes(app)
  const toggle = (list: string[], app: string, on: boolean) =>
    on ? [...list, app] : list.filter((item) => item !== app)

  return (
    <div className="workspace-work-card">
      <h3>Apps</h3>
      <p className="workspace-agents-note">
        It works through your connections, so it never reaches more than you
        can.
      </p>
      <div
        className="workspace-access-scope"
        role="radiogroup"
        aria-label="Which apps it may use"
      >
        <label>
          <input
            type="radio"
            name={`scope-${agent.id}`}
            checked={draft.every}
            onChange={() => setDraft({ ...draft, every: true })}
          />
          <span>
            <strong>Every app you connect</strong>
            <small>It reads them all, including ones you connect later.</small>
          </span>
        </label>
        <label>
          <input
            type="radio"
            name={`scope-${agent.id}`}
            checked={!draft.every}
            onChange={() =>
              setDraft({
                ...draft,
                every: false,
                reads: draft.reads.length
                  ? draft.reads
                  : apps
                      .filter((item) => item.connected)
                      .map((item) => item.app),
              })
            }
          />
          <span>
            <strong>Only the apps you choose</strong>
            <small>It can't see the others at all.</small>
          </span>
        </label>
      </div>

      {connections.isPending ? (
        <p className="workspace-work-empty">Loading your apps…</p>
      ) : apps.length === 0 ? (
        <p className="workspace-work-empty">
          You haven't connected any apps yet. Connect them from Tools.
        </p>
      ) : (
        <ul className="workspace-access-apps" aria-label="Apps">
          {apps.map(({ app, connected }) => {
            const name = toolkitName(app)
            return (
              <li key={app} data-off={reads(app) ? undefined : "true"}>
                <label className="workspace-access-read">
                  <input
                    type="checkbox"
                    checked={reads(app)}
                    disabled={draft.every}
                    aria-label={`${agent.name} may use ${name}`}
                    onChange={(event) =>
                      setDraft({
                        ...draft,
                        reads: toggle(draft.reads, app, event.target.checked),
                        writes: event.target.checked
                          ? draft.writes
                          : draft.writes.filter((item) => item !== app),
                      })
                    }
                  />
                  <span>
                    <strong>{name}</strong>
                    {!connected && <small>Not connected</small>}
                  </span>
                </label>
                <label className="workspace-access-write">
                  <span>May change things</span>
                  <input
                    type="checkbox"
                    role="switch"
                    checked={writes(app)}
                    disabled={!reads(app)}
                    aria-label={`${agent.name} may change things in ${name}`}
                    onChange={(event) =>
                      setDraft({
                        ...draft,
                        writes: toggle(draft.writes, app, event.target.checked),
                      })
                    }
                  />
                </label>
              </li>
            )
          })}
        </ul>
      )}

      {!draft.every && draft.reads.length === 0 && (
        <p className="workspace-agents-note">
          With no apps chosen it can only talk: it reads none of your apps.
        </p>
      )}
      {save.isError && (
        <p className="workspace-agents-error" role="alert">
          {agentsErrorMessage(save.error, "Couldn't save its apps.")}
        </p>
      )}
      <div className="workspace-agents-actions" data-align="start">
        <button
          className="workspace-agents-button"
          data-variant="primary"
          type="button"
          disabled={!dirty || save.isPending}
          onClick={() => save.mutate(draft)}
        >
          {save.isPending ? "Saving…" : "Save apps"}
        </button>
        {dirty && (
          <button
            className="workspace-agents-button"
            type="button"
            disabled={save.isPending}
            onClick={() => {
              save.reset()
              setDraft(saved)
            }}
          >
            Cancel
          </button>
        )}
      </div>
    </div>
  )
}

function CollectionsCard({
  organizationId,
  agent,
}: {
  organizationId: string
  agent: ManagedAgent
}) {
  const queryClient = useQueryClient()
  const corpora = useQuery(corporaQueryOptions(organizationId))
  const saved = corporaChoiceOf(grantOf(agent))
  const [draft, setDraft] = useState<CorporaChoice>(saved)
  const save = useMutation({
    mutationFn: (choice: CorporaChoice) =>
      updateAgent(organizationId, agent.id, {
        grant: grantWithCorpora(choice, grantOf(agent)),
      }),
    onSuccess: (updated) => {
      setDraft(corporaChoiceOf(grantOf(updated)))
      return afterUpdateAgent(queryClient, organizationId, updated)
    },
  })

  // The collections the owner can see, and any the grant names that they
  // can't any more (deleted, or no longer shared), so it can be taken off.
  const collections = useMemo(() => {
    const seen = corpora.data ?? []
    const known = new Set(seen.map((corpus) => corpus.id))
    return [
      ...[...seen]
        .sort((a, b) => a.name.localeCompare(b.name))
        .map((corpus) => ({
          id: corpus.id,
          name: corpus.name,
          detail: countLabel(corpus.document_count, "file"),
          available: true,
        })),
      ...saved.ids
        .filter((id) => !known.has(id))
        .map((id) => ({ id, name: id, detail: "", available: false })),
    ]
  }, [corpora.data, saved.ids])

  const dirty =
    draft.scope !== saved.scope ||
    (draft.scope === "some" && !sameList(draft.ids, saved.ids))
  const searches = (id: string) =>
    draft.scope === "all" || (draft.scope === "some" && draft.ids.includes(id))
  const scopes: {
    value: CorporaChoice["scope"]
    label: string
    detail: string
  }[] = [
    {
      value: "all",
      label: "All collections",
      detail: "Every collection you can see, including ones made later.",
    },
    {
      value: "some",
      label: "Only these",
      detail: "It can't see the others at all.",
    },
    {
      value: "none",
      label: "None",
      detail: "It searches no collections.",
    },
  ]

  return (
    <div className="workspace-work-card">
      <h3>Collections</h3>
      <p className="workspace-agents-note">
        What it may search in your Library. It never sees a collection you
        can't.
      </p>
      <div
        className="workspace-access-scope"
        role="radiogroup"
        aria-label="Which collections it may search"
      >
        {scopes.map((option) => (
          <label key={option.value}>
            <input
              type="radio"
              name={`corpora-${agent.id}`}
              checked={draft.scope === option.value}
              // A pick starts from every collection it can see, so wait
              // for them.
              disabled={
                option.value === "some" &&
                draft.scope !== "some" &&
                corpora.isPending
              }
              onChange={() =>
                setDraft({
                  scope: option.value,
                  ids:
                    option.value === "some" && draft.ids.length === 0
                      ? collections
                          .filter((item) => item.available)
                          .map((item) => item.id)
                      : draft.ids,
                })
              }
            />
            <span>
              <strong>{option.label}</strong>
              <small>{option.detail}</small>
            </span>
          </label>
        ))}
      </div>

      {draft.scope === "some" &&
        (corpora.isPending ? (
          <p className="workspace-work-empty">Loading your collections…</p>
        ) : corpora.isError ? (
          <p className="workspace-agents-error" role="alert">
            Couldn't load your collections.
          </p>
        ) : collections.length === 0 ? (
          <p className="workspace-work-empty">
            You have no collections yet. Make them in Library.
          </p>
        ) : (
          <ul className="workspace-access-apps" aria-label="Collections">
            {collections.map(({ id, name, detail, available }) => (
              <li key={id} data-off={searches(id) ? undefined : "true"}>
                <label className="workspace-access-read">
                  <input
                    type="checkbox"
                    checked={searches(id)}
                    aria-label={`${agent.name} may search ${name}`}
                    onChange={(event) =>
                      setDraft({
                        ...draft,
                        ids: event.target.checked
                          ? [...draft.ids, id]
                          : draft.ids.filter((item) => item !== id),
                      })
                    }
                  />
                  <span>
                    <strong>{name}</strong>
                    {available ? (
                      <small className="workspace-access-detail">
                        {detail}
                      </small>
                    ) : (
                      <small>No longer available</small>
                    )}
                  </span>
                </label>
              </li>
            ))}
          </ul>
        ))}

      {draft.scope === "some" && draft.ids.length === 0 && (
        <p className="workspace-agents-note">
          With no collections chosen it searches none of them.
        </p>
      )}
      {save.isError && (
        <p className="workspace-agents-error" role="alert">
          {agentsErrorMessage(save.error, "Couldn't save its collections.")}
        </p>
      )}
      <div className="workspace-agents-actions" data-align="start">
        <button
          className="workspace-agents-button"
          data-variant="primary"
          type="button"
          disabled={!dirty || save.isPending}
          onClick={() => save.mutate(draft)}
        >
          {save.isPending ? "Saving…" : "Save collections"}
        </button>
        {dirty && (
          <button
            className="workspace-agents-button"
            type="button"
            disabled={save.isPending}
            onClick={() => {
              save.reset()
              setDraft(saved)
            }}
          >
            Cancel
          </button>
        )}
      </div>
    </div>
  )
}
