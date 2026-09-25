// Wording shared by the knowledge bases list and a knowledge base's detail.
import {
  connectedAppName,
  contentTypeName,
  documentOrigin,
  parseTimestamp,
} from "@/lib/rag/client"
import type {
  Corpus,
  CorpusDocument,
  DocumentOrigin,
  DocumentSource,
  RagViewer,
} from "@/lib/rag/client"

const relative = new Intl.RelativeTimeFormat("en", { numeric: "auto" })

/** "just now", "5 minutes ago", "yesterday", or "on Sep 3". */
export function relativeTime(value: string, now = Date.now()): string {
  const time = parseTimestamp(value)
  if (Number.isNaN(time)) return "at an unknown time"
  // A timestamp slightly ahead of this clock is skew, not the future.
  const seconds = (now - time) / 1000
  if (seconds < 45) return "just now"
  if (seconds < 45 * 60) {
    return relative.format(-Math.round(seconds / 60), "minute")
  }
  if (seconds < 22 * 3600) {
    return relative.format(-Math.round(seconds / 3600), "hour")
  }
  if (seconds < 26 * 86400) {
    return relative.format(-Math.round(seconds / 86400), "day")
  }
  const date = new Date(time)
  return `on ${date.toLocaleDateString("en", {
    month: "short",
    day: "numeric",
    ...(date.getFullYear() === new Date(now).getFullYear()
      ? {}
      : { year: "numeric" }),
  })}`
}

/**
 * When a knowledge base last changed. Rag sets `updated_at` at creation and
 * never moves it, so this reads "Created …" until it ever does.
 */
export function corpusTimeLabel(
  corpus: Pick<Corpus, "created_at" | "updated_at">,
  now = Date.now()
): string {
  const created = parseTimestamp(corpus.created_at)
  const updated = parseTimestamp(corpus.updated_at)
  return updated - created > 1000
    ? `Updated ${relativeTime(corpus.updated_at, now)}`
    : `Created ${relativeTime(corpus.created_at, now)}`
}

export function corpusScopeLabel(
  corpus: Pick<Corpus, "scope" | "member_id">,
  viewer: RagViewer
): string {
  switch (corpus.scope) {
    case "organization":
      return "Whole workspace"
    case "team":
      return "Team"
    default:
      return corpus.member_id === viewer.userId ? "Just you" : "Private"
  }
}

export function countLabel(count: number, noun: string): string {
  return `${count.toLocaleString("en")} ${noun}${count === 1 ? "" : "s"}`
}

export function originLabel(
  origin: DocumentOrigin,
  document: Pick<CorpusDocument, "content_type">
): string {
  switch (origin.kind) {
    case "app":
      return origin.name
    case "file":
      return `File · ${contentTypeName(document.content_type)}`
    case "link":
      return "Link"
    default:
      return "Note"
  }
}

/** A document's title, or the best name its source gives it. */
export function documentTitle(
  document: Pick<CorpusDocument, "title" | "source" | "metadata">
): string {
  const title = document.title?.trim()
  if (title) return title
  const origin = documentOrigin(document)
  switch (origin.kind) {
    case "link":
      return origin.host ?? "Untitled link"
    case "file":
      return "Untitled file"
    case "app":
      return `Untitled ${origin.name} document`
    default:
      return "Untitled note"
  }
}

/**
 * Where a cited passage came from. A citation carries its document's source
 * but not its metadata, so a connected app's document shows as its link.
 */
export function citationSourceLabel(source: DocumentSource): string {
  switch (source.kind) {
    case "file":
      return "File"
    case "inline":
      return "Note"
    default: {
      if (source.ref?.startsWith("knowledge:")) return "Connected app"
      try {
        return source.ref ? new URL(source.ref).host || "Link" : "Link"
      } catch {
        return "Link"
      }
    }
  }
}

/** One small, muted fact about a cited passage's provenance. */
export type ProvenanceChip = {
  key: "provider" | "document_kind" | "category"
  label: string
  /** Set only on the `provider` chip, to look up its logo. */
  provider?: string
}

/** "work_material" → "Work material". Generic, so a category or kind this
 * app does not know about yet still reads as words, not a raw token. */
function humanizeToken(value: string): string {
  const spaced = value.trim().replace(/[_-]+/g, " ")
  return spaced ? spaced.charAt(0).toUpperCase() + spaced.slice(1) : spaced
}

/**
 * A citation's provenance, from its `metadata` — present only for a document
 * a connected app filled in
 * (`rag/internal/application/knowledge_events.go`): the source app
 * (`provider`), the kind of document at its source (`document_kind`, e.g.
 * `pull_request`), and its semantic category (`category`, e.g.
 * `work_material`, from knowledge's quality judge). Empty when `metadata` is
 * absent or carries none of these — an inline note, a plain link, or an
 * answer from an older rag build with no `metadata` on its citations.
 */
export function citationProvenance(
  metadata: Partial<Record<string, string>> | undefined
): ProvenanceChip[] {
  if (!metadata) return []
  const chips: ProvenanceChip[] = []
  const provider = metadata.provider?.trim()
  if (provider) {
    chips.push({
      key: "provider",
      label: connectedAppName(provider),
      provider: provider.toLowerCase(),
    })
  }
  const kind = metadata.document_kind?.trim()
  if (kind) chips.push({ key: "document_kind", label: humanizeToken(kind) })
  const category = metadata.category?.trim()
  if (category) {
    chips.push({ key: "category", label: humanizeToken(category) })
  }
  return chips
}
