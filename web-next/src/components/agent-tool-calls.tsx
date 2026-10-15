import type { OrbState } from "thinking-orbs"
import { toolkitName } from "@/lib/apps/client"
import { humanizeToolName } from "@/lib/threads/timeline"
import type { ToolEntry, ToolState } from "@/lib/threads/timeline"
import { featuredSlugs, toolkitIcon } from "./workspace-tools-data"

// What the agent conversation says about tool calls, shared by the folded
// tool row and the run card's steps.

export const toolStateWords: Record<ToolState, string> = {
  running: "Running",
  waiting_approval: "Waiting for you",
  declined: "Declined",
  failed: "Failed",
  interrupted: "Interrupted",
  done: "Done",
}

/**
 * A call still running once its run has definitely ended was left behind by
 * a run that was stopped or broke off (its end marker never came), so it
 * reads as interrupted, as a draft does. While the run may still be alive,
 * just not streaming here, it stays as it was.
 */
export function settledCalls(
  tools: readonly ToolEntry[],
  ended: boolean
): ToolEntry[] {
  if (!ended) return [...tools]
  return tools.map((tool) =>
    tool.state === "running" ? { ...tool, state: "interrupted" } : tool
  )
}

export function toolOrbState(tool: string): OrbState {
  const name = tool.toLowerCase()
  if (/search|knowledge|read|list|source|snapshot/.test(name))
    return "searching"
  if (/connect|browser|navigate/.test(name)) return "connecting"
  if (/create|write|artifact|workflow|update/.test(name)) return "shaping"
  if (/code|sandbox|execute|run/.test(name)) return "solving"
  return "working"
}

const appToolPrefixes = [
  ...featuredSlugs,
  "slack",
  "hubspot",
  "salesforce",
  "apollo",
  "clay",
  "luma",
  "outlook",
  "microsoftteams",
]
const appActionPrefixes = (
  [
    ...appToolPrefixes.map((slug): [string, string] => [slug, slug]),
    ["google_drive", "googledrive"],
    ["google_calendar", "googlecalendar"],
    ["microsoft_teams", "microsoftteams"],
  ] as [string, string][]
).sort(([a], [b]) => b.length - a.length)

/** The app slug is explicit on discovery calls and prefixes Composio's action slugs. */
export function calledApp(tool: ToolEntry): string | undefined {
  const name = tool.tool.toLowerCase()
  const arguments_ = tool.arguments
  if (name.startsWith("apps_")) {
    const toolkit = arguments_?.toolkit
    if (typeof toolkit === "string" && /^[\w-]{1,64}$/.test(toolkit))
      return toolkit.toLowerCase()
    if (!/^apps_run_(read|write)_tool$/.test(name)) return undefined
  }
  const action = name.startsWith("apps_")
    ? typeof arguments_?.tool === "string"
      ? arguments_.tool.toLowerCase()
      : undefined
    : name
  if (!action) return undefined
  const normalized = action.replace(/-/g, "_")
  for (const [prefix, slug] of appActionPrefixes) {
    if (normalized.startsWith(`${prefix}_`)) return slug
  }
  if (name.startsWith("apps_") && /^[a-z][a-z0-9]*_/.test(action))
    return action.split("_")[0]
  return undefined
}

export function AppCallIcon({ slug }: { slug: string }) {
  const icon = toolkitIcon(slug)
  const name = icon?.title || toolkitName(slug)
  return (
    <span
      className="workspace-agent-tool-app"
      role="img"
      aria-label={name}
      title={name}
    >
      {icon ? (
        <svg viewBox="0 0 24 24" fill={`#${icon.hex}`} aria-hidden="true">
          <path d={icon.path} />
        </svg>
      ) : (
        name.charAt(0).toUpperCase()
      )}
    </span>
  )
}

/** One call in words: the app and what it did, or the tool's name. */
export function toolCallLabel(tool: ToolEntry): string {
  const app = calledApp(tool)
  if (!app) return humanizeToolName(tool.tool) || "Tool call"
  const name = toolkitIcon(app)?.title || toolkitName(app)
  if (tool.tool === "apps_search_tools") return `Find ${name} tools`
  if (tool.tool === "apps_connections") return `Check ${name} connection`
  if (tool.tool === "apps_run_read_tool") return `${name} · Read data`
  if (tool.tool === "apps_run_write_tool") return `${name} · Make change`
  return humanizeToolName(tool.tool) || name
}
