import { useId } from "react"
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

const planetPalettes = [
  {
    sky: "#2a2440",
    light: "#e3bbf8",
    dark: "#795cae",
    ring: "#ead7ff",
    band: "#a677ce",
  },
  {
    sky: "#1d3540",
    light: "#9be4df",
    dark: "#398ca0",
    ring: "#d4f6ed",
    band: "#67b7b9",
  },
  {
    sky: "#3b2b27",
    light: "#ffd3a0",
    dark: "#be7255",
    ring: "#ffe9c9",
    band: "#e99d75",
  },
  {
    sky: "#3e2936",
    light: "#ffb5b3",
    dark: "#bd5d81",
    ring: "#ffe1dc",
    band: "#dc849d",
  },
  {
    sky: "#23324b",
    light: "#b7cbff",
    dark: "#607fc0",
    ring: "#e0e8ff",
    band: "#849fe1",
  },
] as const

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

export function TeamspaceAvatar({
  name,
  className,
}: {
  name: string
  className?: string
}) {
  const gradientId = useId()
  const frontRingId = useId()
  const variant = stableHash(name) % planetPalettes.length
  const palette = planetPalettes[variant]

  return (
    <Avatar
      className={cn("workspace-avatar", className)}
      aria-label={`${name} teamspace`}
      data-avatar-kind="planet"
      data-planet-variant={variant}
    >
      <AvatarFallback className="workspace-planet-fallback">
        <svg
          className="workspace-planet-art"
          viewBox="0 0 64 64"
          aria-hidden="true"
        >
          <defs>
            <radialGradient id={gradientId} cx="30%" cy="26%" r="78%">
              <stop offset="0%" stopColor={palette.light} />
              <stop offset="100%" stopColor={palette.dark} />
            </radialGradient>
            <clipPath id={frontRingId}>
              <rect x="0" y="32" width="64" height="32" />
            </clipPath>
          </defs>
          <circle cx="32" cy="32" r="32" fill={palette.sky} />
          <circle cx="12" cy="14" r="1.5" fill="#fff" opacity=".75" />
          <circle cx="51" cy="48" r="1" fill="#fff" opacity=".55" />
          {variant === 0 && (
            <ellipse
              cx="32"
              cy="32"
              rx="27"
              ry="9"
              fill="none"
              stroke={palette.ring}
              strokeWidth="4"
              opacity=".6"
              transform="rotate(-22 32 32)"
            />
          )}
          <circle cx="32" cy="32" r="17" fill={`url(#${gradientId})`} />
          {variant === 0 && (
            <>
              <path
                d="M18 29c8 3 20 3 28-1M18 36c9 4 20 4 28 0"
                fill="none"
                stroke={palette.band}
                strokeWidth="2.5"
                opacity=".5"
              />
              <ellipse
                cx="32"
                cy="32"
                rx="27"
                ry="9"
                fill="none"
                stroke={palette.ring}
                strokeWidth="4"
                clipPath={`url(#${frontRingId})`}
                transform="rotate(-22 32 32)"
              />
            </>
          )}
          {variant === 1 && (
            <g fill={palette.sky} opacity=".62">
              <path d="M22 19c5-3 9 0 10 4l-3 4-6-1-4 4-3-2c1-4 3-7 6-9Z" />
              <path d="M39 26c5 0 8 4 9 8l-6 2-2 5-5 4-3-5 2-6 5-3Z" />
              <path d="M20 38c4 0 6 2 7 5l-2 5c-4-2-6-5-7-8Z" />
            </g>
          )}
          {variant === 2 && (
            <g fill={palette.sky} opacity=".68">
              <circle cx="25" cy="26" r="4" />
              <circle cx="39" cy="36" r="5" />
              <circle cx="24" cy="40" r="2.5" />
              <circle cx="39" cy="23" r="2" />
            </g>
          )}
          {variant === 3 && (
            <g fill="none" stroke={palette.band} strokeWidth="3" opacity=".73">
              <path d="M19 25c9 4 18 4 26 0" />
              <path d="M16 32c11 5 23 5 32 0" />
              <path d="M19 39c9 4 18 4 26 0" />
            </g>
          )}
          {variant === 4 && (
            <>
              <path
                d="M18 29c8-3 20-3 28 0M18 36c8-2 20-2 28 1"
                fill="none"
                stroke={palette.band}
                strokeWidth="3"
                opacity=".65"
              />
              <ellipse
                cx="38"
                cy="34"
                rx="5"
                ry="3"
                fill={palette.ring}
                opacity=".65"
              />
            </>
          )}
        </svg>
      </AvatarFallback>
    </Avatar>
  )
}
