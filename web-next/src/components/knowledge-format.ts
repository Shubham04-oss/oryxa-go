// Wording shared by the knowledge bases list and a knowledge base's detail.
import {
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
