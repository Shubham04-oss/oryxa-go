import {
  siAirtable,
  siAsana,
  siDiscord,
  siDropbox,
  siFigma,
  siGithub,
  siGmail,
  siGooglecalendar,
  siGoogledrive,
  siJira,
  siLinear,
  siNotion,
  siStripe,
  siTrello,
  siZoom,
} from "simple-icons"
import type { SimpleIcon } from "simple-icons"
import { toolkitName } from "@/lib/apps/client"
import type { Connection, Toolkit } from "@/lib/apps/client"

export type { Connection, Toolkit } from "@/lib/apps/client"

export const featuredGroups = [
  {
    name: "Communication",
    description: "Conversations, meetings and calendars.",
    slugs: ["gmail", "discord", "zoom", "googlecalendar"],
  },
  {
    name: "Planning & projects",
    description: "Keep work moving across every team.",
    slugs: ["notion", "linear", "asana", "trello"],
  },
  {
    name: "Files & knowledge",
    description: "Bring shared context into reach.",
    slugs: ["googledrive", "dropbox", "airtable"],
  },
  {
    name: "Build & business",
    description: "Connect the systems behind your product.",
    slugs: ["github", "figma", "jira", "stripe"],
  },
] as const

/** Every app the browse view features, read one by one from the catalog. */
export const featuredSlugs: string[] = featuredGroups.flatMap((group) => [
  ...group.slugs,
])

const iconBySlug: Record<string, SimpleIcon> = {
  airtable: siAirtable,
  asana: siAsana,
  discord: siDiscord,
  dropbox: siDropbox,
  figma: siFigma,
  github: siGithub,
  gmail: siGmail,
  googlecalendar: siGooglecalendar,
  googledrive: siGoogledrive,
  jira: siJira,
  linear: siLinear,
  notion: siNotion,
  stripe: siStripe,
  trello: siTrello,
  zoom: siZoom,
}

export function toolkitIcon(slug: string): SimpleIcon | undefined {
  return iconBySlug[slug.toLowerCase()]
}

/**
 * Stands in for an app the catalog has not described (yet, or at all), so a
 * real connection is never hidden for want of its logo and description.
 */
export function placeholderToolkit(slug: string): Toolkit {
  return {
    toolkit: slug,
    name: toolkitName(slug),
    description: "",
    categories: [],
    auth_schemes: [],
    connectable: false,
    tools_count: 0,
    triggers_count: 0,
  }
}

const previewNames: Record<string, [string, string]> = {
  gmail: ["Gmail", "Keep email within reach for the work that matters."],
  discord: ["Discord", "Bring community conversations into your workflow."],
  zoom: ["Zoom", "Meetings and follow-ups in one place."],
  googlecalendar: ["Google Calendar", "See and manage your schedule."],
  notion: ["Notion", "Connect docs, notes and shared plans."],
  linear: ["Linear", "Track product issues and projects."],
  asana: ["Asana", "Stay aligned on projects and tasks."],
  trello: ["Trello", "Work with boards, cards and checklists."],
  googledrive: ["Google Drive", "Find the files your team works from."],
  dropbox: ["Dropbox", "Access shared files and folders."],
  airtable: ["Airtable", "Bring structured workspace data together."],
  github: ["GitHub", "Connect repositories, issues and pull requests."],
  figma: ["Figma", "Keep design work close to the team."],
  jira: ["Jira", "Follow the work across engineering projects."],
  stripe: ["Stripe", "See the business context behind your work."],
}

export const previewToolkits: Toolkit[] = featuredGroups.flatMap((group) =>
  group.slugs.map((slug) => ({
    toolkit: slug,
    name: previewNames[slug][0],
    description: previewNames[slug][1],
    categories: [{ id: group.name, name: group.name }],
    auth_schemes: [],
    connectable: false,
    tools_count: 0,
    triggers_count: 0,
  }))
)

// These are visual examples only, never interpreted as authenticated accounts.
export const previewConnections: Connection[] = [
  "github",
  "notion",
  "figma",
  "googledrive",
].map((toolkit): Connection => ({
  id: `preview-${toolkit}`,
  toolkit,
  status: "active",
  member_id: "preview",
  auto_subscribe: false,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
}))

export function groupToolkits(toolkits: Toolkit[], useFeatured = false) {
  if (useFeatured) {
    return featuredGroups
      .map((group) => ({
        name: group.name,
        description: group.description,
        toolkits: group.slugs
          .map((slug) => toolkits.find((toolkit) => toolkit.toolkit === slug))
          .filter((toolkit): toolkit is Toolkit => Boolean(toolkit)),
      }))
      .filter((group) => group.toolkits.length > 0)
  }

  const groups = new Map<string, Toolkit[]>()
  for (const toolkit of toolkits) {
    const name = toolkit.categories[0]?.name || "More tools"
    groups.set(name, [...(groups.get(name) || []), toolkit])
  }
  return [...groups].map(([name, items]) => ({
    name,
    description: "",
    toolkits: items,
  }))
}
