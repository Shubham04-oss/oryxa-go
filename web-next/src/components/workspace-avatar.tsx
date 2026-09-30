import { Avatar as DiceBearAvatar, Style } from "@dicebear/core"
import lorelei from "@dicebear/styles/lorelei.json" with { type: "json" }
import voxelArt from "@dicebear/styles/voxel-art.json" with { type: "json" }
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { cn } from "@/lib/utils"

export type AvatarIdentity = {
  id: string
  name: string
  initials: string
  kind: "user" | "agent"
}

export const people = {
  avery: {
    id: "user-avery-morgan",
    name: "Avery Morgan",
    initials: "AM",
    kind: "user",
  },
  jordan: {
    id: "user-jordan-lee",
    name: "Jordan Lee",
    initials: "JL",
    kind: "user",
  },
  sam: {
    id: "user-sam-kim",
    name: "Sam Kim",
    initials: "SK",
    kind: "user",
  },
  noah: {
    id: "user-noah",
    name: "Noah",
    initials: "NO",
    kind: "user",
  },
} satisfies Record<string, AvatarIdentity>

export const agents = {
  research: {
    id: "agent-research",
    name: "Research agent",
    initials: "RA",
    kind: "agent",
  },
  planning: {
    id: "agent-planning",
    name: "Planning agent",
    initials: "PA",
    kind: "agent",
  },
  review: {
    id: "agent-review",
    name: "Review agent",
    initials: "RV",
    kind: "agent",
  },
  specialist: {
    id: "agent-research-specialist",
    name: "Research specialist",
    initials: "RS",
    kind: "agent",
  },
} satisfies Record<string, AvatarIdentity>

const styles = {
  user: new Style(lorelei),
  agent: new Style(voxelArt),
}
const avatarSources = new Map<string, string>()
const userLooks = [
  {
    backgroundColor: "#d8e9dc",
    hairVariant: "variant05",
    hairColor: "#5b473d",
    skinColor: "#d8a98a",
    glassesProbability: 0,
  },
  {
    backgroundColor: "#dcdaf1",
    hairVariant: "variant25",
    hairColor: "#303346",
    skinColor: "#b77e62",
    glassesVariant: "variant02",
    glassesProbability: 100,
  },
  {
    backgroundColor: "#f3dfd5",
    hairVariant: "variant42",
    hairColor: "#6e3f39",
    skinColor: "#edc4a6",
    earringsProbability: 100,
  },
  {
    backgroundColor: "#d9e8ed",
    hairVariant: "variant12",
    hairColor: "#352e33",
    skinColor: "#c28f73",
    frecklesProbability: 100,
  },
] as const
const knownUserLookIndices: Record<string, number> = {
  [people.avery.id]: 0,
  [people.jordan.id]: 1,
  [people.sam.id]: 2,
  [people.noah.id]: 3,
}

function stableHash(value: string) {
  let hash = 0
  for (const character of value)
    hash = (hash * 31 + character.charCodeAt(0)) >>> 0
  return hash
}

function userLook(id: string) {
  return userLooks[
    knownUserLookIndices[id] ?? stableHash(id) % userLooks.length
  ]
}

export function avatarDataUri(identity: AvatarIdentity) {
  const cacheKey = `${identity.kind}:${identity.id}`
  let source = avatarSources.get(cacheKey)
  if (!source) {
    source =
      identity.kind === "user"
        ? new DiceBearAvatar(styles.user, {
            seed: identity.id,
            ...userLook(identity.id),
          }).toDataUri()
        : new DiceBearAvatar(styles.agent, { seed: identity.id }).toDataUri()
    avatarSources.set(cacheKey, source)
  }
  return source
}

export function IdentityAvatar({
  identity,
  className,
}: {
  identity: AvatarIdentity
  className?: string
}) {
  return (
    <Avatar
      className={cn("workspace-avatar", className)}
      aria-label={identity.name}
    >
      <AvatarImage src={avatarDataUri(identity)} alt="" />
      <AvatarFallback>{identity.initials}</AvatarFallback>
    </Avatar>
  )
}
