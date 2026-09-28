import { useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import type { UseQueryResult } from "@tanstack/react-query"
import {
  ArrowUpRight,
  BookOpenText,
  Plus,
  Puzzle,
  RotateCw,
  Search,
} from "lucide-react"
import { z } from "zod"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { ApiError } from "@/lib/api/client"
import type { Role } from "@/lib/auth/client"
import {
  afterCreateCorpus,
  canCreateOrganizationCorpus,
  capabilitiesQueryOptions,
  corporaQueryOptions,
  corpusDescription,
  createCorpus,
  isFromConnectedApps,
  ragErrorMessage,
} from "@/lib/rag/client"
import type {
  Corpus,
  CorpusScope,
  CreateCorpusInput,
  RagViewer,
} from "@/lib/rag/client"
import { KnowledgeBaseDetail } from "./knowledge-base-detail"
import {
  corpusScopeLabel,
  corpusTimeLabel,
  countLabel,
} from "./knowledge-format"

/** The knowledge base open in the canvas; the shell titles its header with it. */
export type SelectedKnowledgeBase = { id: string; name: string }

const newBaseSchema = z.object({
  name: z
    .string()
    .trim()
    .min(2, "Use at least 2 characters.")
    .max(60, "Use at most 60 characters."),
  description: z.string().trim().max(200, "Use at most 200 characters."),
})

function createError(error: unknown): string {
  if (error instanceof ApiError && error.status === 409) {
    return "A knowledge base with a very similar name already exists here. Try another name."
  }
  return ragErrorMessage(
    error,
    "Could not create this knowledge base. Try again."
  )
}

function KnowledgeBaseList({
  organizationId,
  organizationName,
  viewer,
  corpora,
  unavailable,
  onSelect,
}: {
  organizationId?: string
  organizationName?: string
  viewer: RagViewer
  corpora: UseQueryResult<Corpus[]>
  unavailable: boolean
  onSelect: (base: SelectedKnowledgeBase) => void
}) {
  const queryClient = useQueryClient()
  const [query, setQuery] = useState("")
  const [isCreating, setIsCreating] = useState(false)
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [scope, setScope] = useState<CorpusScope>("member")
  const [nameError, setNameError] = useState("")
  const [formError, setFormError] = useState("")
  const canShare = canCreateOrganizationCorpus(viewer)

  const create = useMutation({
    mutationFn: (input: CreateCorpusInput) => {
      if (!organizationId) throw new Error("No workspace is selected.")
      return createCorpus(organizationId, input)
    },
    onSuccess: (created) => {
      if (organizationId) {
        void afterCreateCorpus(queryClient, organizationId, created)
      }
      setIsCreating(false)
      setName("")
      setDescription("")
      setScope("member")
      setQuery("")
      onSelect({ id: created.id, name: created.name })
    },
    onError: (error) => setFormError(createError(error)),
  })

  const bases = corpora.data ?? []
  const filteredBases = bases.filter((base) =>
    `${base.name} ${corpusDescription(base)}`
      .toLocaleLowerCase()
      .includes(query.trim().toLocaleLowerCase())
  )

  function createBase(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const result = newBaseSchema.safeParse({ name, description })
    if (!result.success) {
      const issue = (field: string) =>
        result.error.issues.find((item) => item.path[0] === field)?.message
      setNameError(issue("name") ?? "")
      setFormError(issue("description") ?? "")
      return
    }
    setFormError("")
    create.mutate({
      name: result.data.name,
      description: result.data.description,
      scope: canShare ? scope : "member",
    })
  }

  function cancelCreate() {
    setIsCreating(false)
    setNameError("")
    setFormError("")
    create.reset()
  }

  return (
    <div className="workspace-knowledge-page">
      {unavailable ? (
        <div className="workspace-knowledge-notice" role="status">
          {organizationId
            ? "Layout preview · The knowledge service isn’t reachable here, so there are no knowledge bases to show."
            : "Layout preview · Sign in to a workspace to see its knowledge bases."}
        </div>
      ) : (
        <div className="workspace-knowledge-intro">
          <button
            className="workspace-knowledge-create-button"
            type="button"
            aria-expanded={isCreating}
            onClick={() => (isCreating ? cancelCreate() : setIsCreating(true))}
          >
            <Plus size={17} strokeWidth={1.8} />
            New knowledge base
          </button>
        </div>
      )}

      {isCreating && !unavailable && (
        <form
          className="workspace-knowledge-form"
          onSubmit={createBase}
          noValidate
        >
          <div>
            <h3>New knowledge base</h3>
            <p>Give this collection a name. You can add content next.</p>
          </div>
          <label htmlFor="knowledge-base-name">Name</label>
          <Input
            id="knowledge-base-name"
            value={name}
            onChange={(event) => {
              setName(event.target.value)
              setNameError("")
            }}
            aria-invalid={Boolean(nameError)}
            aria-describedby={
              nameError ? "knowledge-base-name-error" : undefined
            }
            placeholder="e.g. Engineering handbook"
            maxLength={60}
            disabled={create.isPending}
          />
          {nameError && (
            <p
              className="workspace-knowledge-error"
              id="knowledge-base-name-error"
            >
              {nameError}
            </p>
          )}
          <label htmlFor="knowledge-base-description">Description</label>
          <Textarea
            id="knowledge-base-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            placeholder="What belongs here?"
            maxLength={200}
            rows={2}
            disabled={create.isPending}
          />
          {canShare ? (
            <fieldset
              className="workspace-knowledge-choice"
              disabled={create.isPending}
            >
              <legend>Who can use it</legend>
              <label>
                <input
                  type="radio"
                  name="knowledge-base-scope"
                  value="member"
                  checked={scope === "member"}
                  onChange={() => setScope("member")}
                />
                Just you
              </label>
              <label>
                <input
                  type="radio"
                  name="knowledge-base-scope"
                  value="organization"
                  checked={scope === "organization"}
                  onChange={() => setScope("organization")}
                />
                Everyone in {organizationName || "this workspace"}
              </label>
            </fieldset>
          ) : null}
          <p className="workspace-knowledge-hint">
            {canShare && scope === "organization"
              ? "Everyone in the workspace can search it. Only admins can change what’s in it."
              : "Only you can see it, along with your workspace’s admins."}
          </p>
          {formError && (
            <p className="workspace-knowledge-error" role="alert">
              {formError}
            </p>
          )}
          <div className="workspace-knowledge-form-actions">
            <button type="button" onClick={cancelCreate}>
              Cancel
            </button>
            <button type="submit" disabled={create.isPending}>
              {create.isPending ? "Creating…" : "Create base"}
            </button>
          </div>
        </form>
      )}

      {unavailable ? null : corpora.isPending ? (
        <>
          <p className="sr-only" role="status">
            Loading knowledge bases…
          </p>
          <div
            className="workspace-knowledge-grid workspace-knowledge-toolbar-gap"
            aria-hidden="true"
          >
            {[0, 1, 2].map((item) => (
              <div
                className="workspace-knowledge-card workspace-knowledge-card-skeleton"
                key={item}
              />
            ))}
          </div>
        </>
      ) : corpora.isError ? (
        <div className="workspace-knowledge-alert" role="alert">
          <span>
            {ragErrorMessage(
              corpora.error,
              "Couldn’t load your knowledge bases."
            )}
          </span>
          <button type="button" onClick={() => void corpora.refetch()}>
            <RotateCw size={14} strokeWidth={1.8} />
            Try again
          </button>
        </div>
      ) : bases.length === 0 ? (
        <div className="workspace-knowledge-empty workspace-knowledge-toolbar-gap">
          <BookOpenText size={23} strokeWidth={1.5} />
          <strong>No knowledge bases yet</strong>
          <p>
            Create one, then add notes, links or text files for your agents to
            search.
          </p>
        </div>
      ) : (
        <>
          <div className="workspace-knowledge-toolbar">
            <label className="workspace-knowledge-search">
              <Search size={17} strokeWidth={1.7} aria-hidden="true" />
              <span className="sr-only">Search knowledge bases</span>
              <Input
                type="search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search knowledge bases"
              />
            </label>
            <span>{countLabel(filteredBases.length, "base")}</span>
          </div>

          {filteredBases.length ? (
            <div className="workspace-knowledge-grid">
              {filteredBases.map((base) => {
                const fromApps = isFromConnectedApps(base)
                const baseDescription = corpusDescription(base)
                return (
                  <button
                    className="workspace-knowledge-card"
                    type="button"
                    key={base.id}
                    data-screen-context-kind="knowledge_base"
                    data-screen-context-id={base.id}
                    data-screen-context-name={base.name}
                    aria-label={`Open ${base.name}`}
                    onClick={() => onSelect({ id: base.id, name: base.name })}
                  >
                    <span className="workspace-knowledge-card-top">
                      <span className="workspace-knowledge-icon">
                        {fromApps ? (
                          <Puzzle size={20} strokeWidth={1.6} />
                        ) : (
                          <BookOpenText size={20} strokeWidth={1.6} />
                        )}
                      </span>
                      <ArrowUpRight size={17} strokeWidth={1.7} />
                    </span>
                    <strong>{base.name}</strong>
                    {(baseDescription || fromApps) && (
                      <span className="workspace-knowledge-card-description">
                        {baseDescription || "Indexed from your connected apps."}
                      </span>
                    )}
                    <span className="workspace-knowledge-card-meta">
                      <span>{corpusScopeLabel(base, viewer)}</span>
                      <span aria-hidden="true">·</span>
                      <span>{countLabel(base.document_count, "document")}</span>
                      <span aria-hidden="true">·</span>
                      <span>{corpusTimeLabel(base)}</span>
                    </span>
                  </button>
                )
              })}
            </div>
          ) : (
            <div className="workspace-knowledge-empty">
              <Search size={23} strokeWidth={1.5} />
              <strong>No knowledge bases found</strong>
              <p>Try a different search.</p>
            </div>
          )}
        </>
      )}
    </div>
  )
}

export function KnowledgeBases({
  organizationId,
  organizationName,
  organizationRole,
  userId,
  selected,
  onSelect,
  onOpenTools,
}: {
  /** From the shell; without one there is nothing to read. */
  organizationId?: string
  organizationName?: string
  organizationRole?: Role
  userId?: string
  selected: SelectedKnowledgeBase | null
  onSelect: (base: SelectedKnowledgeBase | null) => void
  onOpenTools?: () => void
}) {
  const viewer: RagViewer = { userId, role: organizationRole }
  const capabilities = useQuery({
    ...capabilitiesQueryOptions(),
    enabled: Boolean(organizationId),
  })
  const corpora = useQuery({
    ...corporaQueryOptions(organizationId ?? ""),
    enabled: Boolean(organizationId),
  })
  // Rag answers its capabilities without a session; when neither read gets
  // through, the service itself is missing rather than the member's access.
  const unavailable =
    !organizationId || (corpora.isError && capabilities.isError)

  if (organizationId && selected && !unavailable) {
    return (
      <div className="workspace-knowledge-page">
        <KnowledgeBaseDetail
          key={selected.id}
          organizationId={organizationId}
          corpusId={selected.id}
          name={selected.name}
          viewer={viewer}
          answering={capabilities.data?.answering}
          onClose={() => onSelect(null)}
          onOpenTools={onOpenTools}
        />
      </div>
    )
  }

  return (
    <KnowledgeBaseList
      organizationId={organizationId}
      organizationName={organizationName}
      viewer={viewer}
      corpora={corpora}
      unavailable={unavailable}
      onSelect={onSelect}
    />
  )
}
