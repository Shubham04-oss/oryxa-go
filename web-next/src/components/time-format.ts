// How a moment reads on screen, in the viewer's own locale: a day's name and
// the clock time of something that happened on it. Pure, so the request
// views and their tests share one set of rules.

const clockFormat = new Intl.DateTimeFormat(undefined, {
  hour: "numeric",
  minute: "2-digit",
})
const dayThisYearFormat = new Intl.DateTimeFormat(undefined, {
  weekday: "short",
  day: "numeric",
  month: "short",
})
const dateFormat = new Intl.DateTimeFormat(undefined, {
  day: "numeric",
  month: "short",
  year: "numeric",
})
const fullFormat = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
})

function parsed(value: string): Date | undefined {
  const at = Date.parse(value)
  return Number.isNaN(at) ? undefined : new Date(at)
}

/** The local calendar day of a moment, as `YYYY-MM-DD`. */
function localDay(at: Date): string {
  const month = String(at.getMonth() + 1).padStart(2, "0")
  const date = String(at.getDate()).padStart(2, "0")
  return `${at.getFullYear()}-${month}-${date}`
}

/** The local calendar day before `at`'s. */
function dayBefore(at: Date): string {
  const before = new Date(at)
  before.setDate(before.getDate() - 1)
  return localDay(before)
}

/** A day separator's words: "Today", "Yesterday", or the date. */
export function dayLabel(value: string, now = Date.now()): string {
  const at = parsed(value)
  if (!at) return ""
  const today = new Date(now)
  const key = localDay(at)
  if (key === localDay(today)) return "Today"
  if (key === dayBefore(today)) return "Yesterday"
  return at.getFullYear() === today.getFullYear()
    ? dayThisYearFormat.format(at)
    : dateFormat.format(at)
}

/** The time of day of something, the clock only: its day is said beside it. */
export function clockTime(value: string): string {
  const at = parsed(value)
  return at ? clockFormat.format(at) : ""
}

/** A moment in full, the date and the time: what a hover says. A value that is not a time is returned as it is. */
export function fullTime(value: string): string {
  const at = Date.parse(value)
  return Number.isNaN(at) ? value : fullFormat.format(at)
}
