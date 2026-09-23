export type TriggerField = {
  name: string
  title: string
  description?: string
  required: boolean
  default?: boolean | number | string
  type: "boolean" | "integer" | "number" | "select" | "string" | "strings"
  options?: Array<{ label: string; value: boolean | number | string }>
}

type Property = {
  type?: string | string[]
  title?: string
  description?: string
  default?: boolean | number | string
  enum?: unknown[]
  items?: Property
  anyOf?: Property[]
  oneOf?: Property[]
  allOf?: Property[]
}

export type TriggerCapability =
  | { kind: "none" }
  | { kind: "unsupported" }
  | { kind: "form"; fields: TriggerField[] }

function types(value?: string | string[]) {
  return (Array.isArray(value) ? value : value ? [value] : []).filter(
    (item) => item !== "null"
  )
}

function options(values?: unknown[]) {
  if (!values?.length) return undefined
  if (
    values.some(
      (value) =>
        typeof value !== "string" &&
        typeof value !== "number" &&
        typeof value !== "boolean"
    )
  )
    return undefined
  return values.map((value) => ({
    label: String(value),
    value: value as boolean | number | string,
  }))
}

function fieldShape(
  property?: Property
): Pick<TriggerField, "type" | "options"> | undefined {
  if (!property) return undefined
  const choices = options(property.enum)
  if (choices) return { type: "select", options: choices }
  const declared = types(property.type)
  if (declared.includes("array")) {
    const itemTypes = types(property.items?.type)
    const itemOptions = options(property.items?.enum)
    if (
      itemOptions ||
      itemTypes.length === 0 ||
      itemTypes.some((name) =>
        ["string", "number", "integer", "boolean"].includes(name)
      )
    )
      return { type: "strings", options: itemOptions }
    return undefined
  }
  for (const name of declared) {
    if (
      name === "boolean" ||
      name === "integer" ||
      name === "number" ||
      name === "string"
    )
      return { type: name }
  }
  for (const branch of [
    ...(property.anyOf || []),
    ...(property.oneOf || []),
    ...(property.allOf || []),
  ]) {
    const shape = fieldShape(branch)
    if (shape) return shape
  }
  return undefined
}

export function triggerCapability(schema: unknown): TriggerCapability {
  if (!schema || typeof schema !== "object") return { kind: "none" }
  const object = schema as {
    properties?: Record<string, Property>
    required?: string[]
  }
  const properties = object.properties || {}
  const required = new Set(object.required || [])
  if ([...required].some((name) => !fieldShape(properties[name])))
    return { kind: "unsupported" }
  const fields: TriggerField[] = []
  for (const [name, property] of Object.entries(properties)) {
    const shape = fieldShape(property)
    if (!shape) continue
    fields.push({
      ...shape,
      name,
      title:
        property.title ||
        name
          .replace(/[_-]+/g, " ")
          .replace(/^./, (letter) => letter.toUpperCase()),
      description: property.description,
      default: property.default,
      required: required.has(name),
    })
  }
  if (fields.length === 0)
    return required.size ? { kind: "unsupported" } : { kind: "none" }
  return { kind: "form", fields }
}

export function buildTriggerConfig(
  fields: TriggerField[],
  values: Partial<Record<string, boolean | number | string>>
): { config: Record<string, unknown>; problem?: string } {
  const config: Record<string, unknown> = {}
  for (const field of fields) {
    let value: boolean | number | string | undefined = values[field.name]
    if (field.type === "boolean") {
      config[field.name] =
        typeof value === "boolean" ? value : Boolean(field.default)
      continue
    }
    if (value === undefined || (typeof value === "string" && !value.trim())) {
      value = field.default
      if (value === undefined) {
        if (field.required)
          return { config, problem: `${field.title} is required.` }
        continue
      }
    }
    if (field.type === "strings") {
      const mapped = new Map(
        field.options?.map((item) => [item.label, item.value])
      )
      const entries = String(value)
        .split("\n")
        .map((line) => line.trim())
        .filter(Boolean)
        .map((line) => mapped.get(line) ?? line)
      if (!entries.length && field.required)
        return { config, problem: `${field.title} is required.` }
      if (entries.length) config[field.name] = entries
    } else if (field.type === "integer" || field.type === "number") {
      const number = Number(value)
      if (
        !Number.isFinite(number) ||
        (field.type === "integer" && !Number.isInteger(number))
      )
        return {
          config,
          problem: `${field.title} must be ${field.type === "integer" ? "a whole" : "a valid"} number.`,
        }
      config[field.name] = number
    } else {
      config[field.name] = value
    }
  }
  return { config }
}

export const sourceProviders = [
  "github",
  "notion",
  "slack",
  "googledrive",
  "gmail",
] as const
export type SourceProvider = (typeof sourceProviders)[number]

export function isSourceProvider(value: string): value is SourceProvider {
  return sourceProviders.some((provider) => provider === value)
}

export const sourceFields: Record<
  SourceProvider,
  { label: string; placeholder: string; help: string; required: boolean }
> = {
  github: {
    label: "Repository",
    placeholder: "owner/name",
    help: "Indexes issues and pull requests.",
    required: true,
  },
  notion: {
    label: "Database ID",
    placeholder: "Leave blank for all pages",
    help: "Index one database or every page this connection can see.",
    required: false,
  },
  slack: {
    label: "Channel ID",
    placeholder: "C0123456789",
    help: "Indexes one channel's message history.",
    required: true,
  },
  googledrive: {
    label: "Folder ID",
    placeholder: "Leave blank for the whole Drive",
    help: "Indexes a folder or the whole Drive.",
    required: false,
  },
  gmail: {
    label: "Label ID",
    placeholder: "INBOX",
    help: "Indexes one mail label; Inbox is the default.",
    required: false,
  },
}

export const intervalChoices = [
  { label: "Every 5 minutes", value: 300 },
  { label: "Every 15 minutes", value: 900 },
  { label: "Every hour", value: 3600 },
  { label: "Every 6 hours", value: 21600 },
  { label: "Every day", value: 86400 },
  { label: "Every week", value: 604800 },
]

export function sourceTarget(provider: SourceProvider, raw: string) {
  const value = raw.trim()
  switch (provider) {
    case "github":
      return { repository: value }
    case "slack":
      return { channel: value }
    case "notion":
      return value ? { database: value } : {}
    case "googledrive":
      return value ? { folder: value } : {}
    case "gmail":
      return value ? { label: value } : {}
  }
}

export function sourceTargetProblem(provider: SourceProvider, raw: string) {
  const value = raw.trim()
  if (provider === "github" && !/^[^\s/]+\/[^\s/]+$/.test(value))
    return "Enter a repository as owner/name."
  if (provider === "slack" && (!value || /\s/.test(value)))
    return "Enter a channel ID, such as C0123456789."
  if ((provider === "googledrive" || provider === "gmail") && /\s/.test(value))
    return "Enter an ID without spaces."
  return undefined
}

export function sourceName(provider: string, target: Record<string, string>) {
  switch (provider) {
    case "github":
      return target.repository || "Repository"
    case "slack":
      return target.channel ? `#${target.channel}` : "Channel"
    case "notion":
      return target.database ? `Database ${target.database}` : "All pages"
    case "googledrive":
      return target.folder ? `Folder ${target.folder}` : "My Drive"
    case "gmail":
      return target.label || "INBOX"
    default:
      return Object.values(target).join(", ") || "Source"
  }
}
