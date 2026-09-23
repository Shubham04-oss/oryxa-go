import { useState } from "react"
import type { FormEvent } from "react"
import {
  ArrowUpRight,
  BookOpenText,
  FileText,
  Link2,
  Plus,
  Search,
  StickyNote,
} from "lucide-react"
import { z } from "zod"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"

type KnowledgeSource = {
  title: string
  kind: "Document" | "Link" | "Note"
}

export type KnowledgeBase = {
  id: string
  name: string
  description: string
  updated: string
  sources: KnowledgeSource[]
}

export const initialKnowledgeBases: KnowledgeBase[] = [
  {
    id: "product-handbook",
    name: "Product handbook",
    description: "Product principles, specs, and decisions in one place.",
    updated: "Today",
    sources: [
      { title: "Product overview", kind: "Document" },
      { title: "Design principles", kind: "Document" },
      { title: "Decision log", kind: "Note" },
    ],
  },
  {
    id: "customer-insights",
    name: "Customer insights",
    description: "Research notes and feedback that shape what we build.",
    updated: "Yesterday",
    sources: [
      { title: "Interview highlights", kind: "Document" },
      { title: "Feedback board", kind: "Link" },
      { title: "Recurring themes", kind: "Note" },
    ],
  },
  {
    id: "launch-playbook",
    name: "Launch playbook",
    description: "The shared context behind our upcoming release.",
    updated: "3 days ago",
    sources: [
      { title: "Launch brief", kind: "Document" },
      { title: "Rollout checklist", kind: "Document" },
      { title: "Release notes", kind: "Document" },
    ],
  },
]

const newBaseSchema = z.object({
  name: z.string().trim().min(2, "Use at least 2 characters.").max(60),
  description: z.string().trim().max(200),
})

const sourceIcons = {
  Document: FileText,
  Link: Link2,
  Note: StickyNote,
}

export function KnowledgeBases({
  bases,
  onCreate,
  selectedId,
  onSelect,
}: {
  bases: KnowledgeBase[]
  onCreate: (base: KnowledgeBase) => void
  selectedId: string | null
  onSelect: (id: string | null) => void
}) {
  const [query, setQuery] = useState("")
  const [isCreating, setIsCreating] = useState(false)
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [nameError, setNameError] = useState("")

  const selectedBase = bases.find((base) => base.id === selectedId)
  const filteredBases = bases.filter((base) =>
    `${base.name} ${base.description}`
      .toLocaleLowerCase()
      .includes(query.trim().toLocaleLowerCase())
  )

  function createBase(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const result = newBaseSchema.safeParse({ name, description })
    if (!result.success) {
      setNameError(
        result.error.issues.find((issue) => issue.path[0] === "name")
          ?.message ?? "Check this field."
      )
      return
    }

    const base: KnowledgeBase = {
      id: crypto.randomUUID(),
      name: result.data.name,
      description: result.data.description,
      updated: "Just now",
      sources: [],
    }
    onCreate(base)
    onSelect(base.id)
    setIsCreating(false)
    setName("")
    setDescription("")
    setNameError("")
    setQuery("")
  }

  return (
    <div className="workspace-knowledge-page">
      {selectedBase ? (
        <>
          <div className="workspace-knowledge-section-heading">
            <div>
              <h3>Sources</h3>
              <p>Documents, links, and notes kept in this base.</p>
            </div>
            <span>{selectedBase.sources.length}</span>
          </div>
          {selectedBase.sources.length ? (
            <ul className="workspace-knowledge-source-list">
              {selectedBase.sources.map((source) => {
                const Icon = sourceIcons[source.kind]
                return (
                  <li key={`${source.kind}-${source.title}`}>
                    <span className="workspace-knowledge-source-icon">
                      <Icon size={17} strokeWidth={1.7} />
                    </span>
                    <span className="workspace-knowledge-source-name">
                      <strong>{source.title}</strong>
                      <small>{source.kind}</small>
                    </span>
                  </li>
                )
              })}
            </ul>
          ) : (
            <div className="workspace-knowledge-empty">
              <FileText size={23} strokeWidth={1.5} />
              <strong>No sources yet</strong>
              <p>This knowledge base is ready for its first source.</p>
            </div>
          )}
        </>
      ) : (
        <>
          <div className="workspace-knowledge-intro">
            <button
              className="workspace-knowledge-create-button"
              type="button"
              aria-expanded={isCreating}
              onClick={() => setIsCreating((current) => !current)}
            >
              <Plus size={17} strokeWidth={1.8} />
              New knowledge base
            </button>
          </div>

          {isCreating && (
            <form className="workspace-knowledge-form" onSubmit={createBase}>
              <div>
                <h3>New knowledge base</h3>
                <p>Give this collection a name. You can add sources later.</p>
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
              />
              <div className="workspace-knowledge-form-actions">
                <button type="button" onClick={() => setIsCreating(false)}>
                  Cancel
                </button>
                <button type="submit">Create base</button>
              </div>
            </form>
          )}

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
            <span>
              {filteredBases.length}{" "}
              {filteredBases.length === 1 ? "base" : "bases"}
            </span>
          </div>

          {filteredBases.length ? (
            <div className="workspace-knowledge-grid">
              {filteredBases.map((base) => (
                <button
                  className="workspace-knowledge-card"
                  type="button"
                  key={base.id}
                  aria-label={`Open ${base.name}`}
                  onClick={() => onSelect(base.id)}
                >
                  <span className="workspace-knowledge-card-top">
                    <span className="workspace-knowledge-icon">
                      <BookOpenText size={20} strokeWidth={1.6} />
                    </span>
                    <ArrowUpRight size={17} strokeWidth={1.7} />
                  </span>
                  <strong>{base.name}</strong>
                  <span className="workspace-knowledge-card-description">
                    {base.description || "A space for shared context."}
                  </span>
                  <span className="workspace-knowledge-card-meta">
                    {base.sources.length}{" "}
                    {base.sources.length === 1 ? "source" : "sources"}
                    <span aria-hidden="true">·</span>
                    Updated {base.updated.toLocaleLowerCase()}
                  </span>
                </button>
              ))}
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
