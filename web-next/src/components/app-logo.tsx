// A connected app's brand mark, shared by the Library's sources and its
// cited passages.
import { siGithub, siGmail, siGoogledrive, siNotion } from "simple-icons"
import type { SimpleIcon } from "simple-icons"

// simple-icons carries no Slack mark (removed for its trademark policy); a
// Slack chip falls back to its initial, same as any other unmapped app.
const appIcons: Partial<Record<string, SimpleIcon>> = {
  github: siGithub,
  gmail: siGmail,
  googledrive: siGoogledrive,
  notion: siNotion,
}

/** A connected app's brand mark, or its initial when this app has none. */
export function AppLogo({
  provider,
  name,
}: {
  provider: string
  name: string
}) {
  const icon = appIcons[provider.toLowerCase()]
  return icon ? (
    <svg viewBox="0 0 24 24" fill={`#${icon.hex}`} aria-hidden="true">
      <path d={icon.path} />
    </svg>
  ) : (
    <>{name.charAt(0)}</>
  )
}
