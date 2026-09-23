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

export type Toolkit = {
  toolkit: string
  name: string
  description: string
  logo?: string
  categories: { id: string; name: string }[]
  connectable: boolean
  tools_count: number
  triggers_count: number
}

export type Connection = {
  id: string
  toolkit: string
  status: "pending" | "active" | "failed" | "expired" | "removed"
  label?: string
  connected_at?: string
}

export type Organization = { id: string; name: string }

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
    connectable: false,
    tools_count: 0,
    triggers_count: 0,
  }))
)

// These are visual examples only, never interpreted as authenticated accounts.
export const previewConnections: Connection[] = [
  { id: "preview-github", toolkit: "github", status: "active" },
  { id: "preview-notion", toolkit: "notion", status: "active" },
  { id: "preview-figma", toolkit: "figma", status: "active" },
  { id: "preview-googledrive", toolkit: "googledrive", status: "active" },
]

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
